package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrDisabled       = errors.New("remote input is disabled")
	ErrInvalid        = errors.New("invalid remote input request")
	ErrUnauthorized   = errors.New("remote session authorization failed")
	ErrExpired        = errors.New("remote session expired")
	ErrRateLimited    = errors.New("remote input rate limit exceeded")
	ErrSessionMissing = errors.New("remote session not found")
)

type Policy struct {
	Enabled bool
	ShowQR  bool
}

type PolicyRepository interface {
	RemotePolicy(context.Context, string, int64) (Policy, error)
}

type Event struct {
	Sequence int64   `json:"sequence"`
	Type     string  `json:"type"`
	Key      string  `json:"key,omitempty"`
	Text     string  `json:"text,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	DeltaX   float64 `json:"deltaX,omitempty"`
	DeltaY   float64 `json:"deltaY,omitempty"`
	Button   string  `json:"button,omitempty"`
	State    string  `json:"state,omitempty"`
}

type SessionCreated struct {
	ID        string    `json:"sessionId"`
	Token     string    `json:"token,omitempty"`
	RemoteURL string    `json:"remoteUrl"`
	ShowQR    bool      `json:"showQr"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type EventPage struct {
	Events       []Event `json:"events"`
	NextSequence int64   `json:"nextSequence"`
}

type session struct {
	id, nodeID    string
	generation    int64
	tokenHashes   [][32]byte
	appToken      string
	appHash       [32]byte
	showQR        bool
	expiresAt     time.Time
	lastSequence  int64
	events        []Event
	wake          chan struct{}
	windowStarted time.Time
	windowCount   int
}

type Service struct {
	Policies  PolicyRepository
	PublicURL string
	TTL       time.Duration
	Now       func() time.Time
	Random    io.Reader
	mutex     sync.Mutex
	sessions  map[string]*session
}

func (s *Service) normalized() error {
	if s.Policies == nil {
		return errors.New("remote policy repository is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(s.PublicURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("remote public URL must be absolute HTTPS")
	}
	if s.TTL <= 0 {
		s.TTL = 10 * time.Minute
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Random == nil {
		s.Random = rand.Reader
	}
	if s.sessions == nil {
		s.sessions = make(map[string]*session)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, nodeID string, generation int64) (SessionCreated, error) {
	if nodeID == "" || generation <= 0 {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	policy, err := s.Policies.RemotePolicy(ctx, nodeID, generation)
	if err != nil {
		return SessionCreated{}, err
	}
	if !policy.Enabled {
		return SessionCreated{}, ErrDisabled
	}
	idBytes, tokenBytes := make([]byte, 16), make([]byte, 32)
	if _, err := io.ReadFull(s.Random, idBytes); err != nil {
		return SessionCreated{}, err
	}
	if _, err := io.ReadFull(s.Random, tokenBytes); err != nil {
		return SessionCreated{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := s.Now().UTC()
	// Sequences start at the creation time in microseconds so they stay
	// monotonic across session renewals and control-plane restarts; consumers
	// holding an older cursor keep receiving the new session's events.
	value := &session{id: id, nodeID: nodeID, generation: generation, tokenHashes: [][32]byte{sha256.Sum256([]byte(token))}, showQR: policy.ShowQR, expiresAt: now.Add(s.TTL), events: []Event{}, wake: make(chan struct{}), windowStarted: now, lastSequence: now.UnixMicro()}
	s.mutex.Lock()
	for sessionID, existing := range s.sessions {
		if existing.nodeID == nodeID {
			close(existing.wake)
			delete(s.sessions, sessionID)
		}
	}
	s.sessions[id] = value
	s.mutex.Unlock()
	return s.created(id, token, policy.ShowQR, value.expiresAt), nil
}

func (s *Service) created(id, token string, showQR bool, expiresAt time.Time) SessionCreated {
	base := strings.TrimSuffix(s.PublicURL, "/")
	return SessionCreated{ID: id, Token: token, RemoteURL: base + "/remote/" + url.PathEscape(id) + "#token=" + url.QueryEscape(token), ShowQR: showQR, ExpiresAt: expiresAt}
}

// maxSessionTokens bounds the hashes a session accepts: the node-issued token
// plus operator-minted links (oldest minted evicted first).
const maxSessionTokens = 8

// Link mints an additional phone token for the node's active session so an
// operator can obtain a remote URL without seeing the on-screen QR code.
func (s *Service) Link(ctx context.Context, nodeID string) (SessionCreated, error) {
	if nodeID == "" {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	s.mutex.Lock()
	value := s.activeSession(nodeID)
	if value == nil {
		s.mutex.Unlock()
		return SessionCreated{}, ErrSessionMissing
	}
	id, generation := value.id, value.generation
	s.mutex.Unlock()
	policy, err := s.Policies.RemotePolicy(ctx, nodeID, generation)
	if err != nil {
		return SessionCreated{}, err
	}
	if !policy.Enabled {
		return SessionCreated{}, ErrDisabled
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(s.Random, tokenBytes); err != nil {
		return SessionCreated{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	s.mutex.Lock()
	defer s.mutex.Unlock()
	value = s.sessions[id]
	if value == nil || !s.Now().UTC().Before(value.expiresAt) {
		return SessionCreated{}, ErrSessionMissing
	}
	if len(value.tokenHashes) >= maxSessionTokens {
		value.tokenHashes = append(value.tokenHashes[:1], value.tokenHashes[2:]...)
	}
	value.tokenHashes = append(value.tokenHashes, sha256.Sum256([]byte(token)))
	return s.created(id, token, value.showQR, value.expiresAt), nil
}

func (s *Service) activeSession(nodeID string) *session {
	now := s.Now().UTC()
	for _, value := range s.sessions {
		if value.nodeID == nodeID && now.Before(value.expiresAt) {
			return value
		}
	}
	return nil
}

// AppSession returns the node's active session with a phone URL for an
// in-page app (a web media item with app access) to display. The app's phone
// token is minted once per session and kept apart from operator links so link
// eviction never invalidates it.
func (s *Service) AppSession(nodeID string) (SessionCreated, error) {
	if nodeID == "" {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	value := s.activeSession(nodeID)
	if value == nil {
		return SessionCreated{}, ErrSessionMissing
	}
	if value.appToken == "" {
		tokenBytes := make([]byte, 32)
		if _, err := io.ReadFull(s.Random, tokenBytes); err != nil {
			return SessionCreated{}, err
		}
		value.appToken = base64.RawURLEncoding.EncodeToString(tokenBytes)
		value.appHash = sha256.Sum256([]byte(value.appToken))
	}
	return s.created(value.id, value.appToken, value.showQR, value.expiresAt), nil
}

func (value *session) authorized(hash [32]byte) bool {
	matched := 0
	for _, stored := range value.tokenHashes {
		matched |= subtle.ConstantTimeCompare(hash[:], stored[:])
	}
	if value.appToken != "" {
		matched |= subtle.ConstantTimeCompare(hash[:], value.appHash[:])
	}
	return matched == 1
}

func (s *Service) Post(ctx context.Context, id, token string, event Event) error {
	if err := validateEvent(event); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	s.mutex.Lock()
	defer s.mutex.Unlock()
	value := s.sessions[id]
	if value == nil {
		return ErrSessionMissing
	}
	now := s.Now().UTC()
	if !now.Before(value.expiresAt) {
		return ErrExpired
	}
	if !value.authorized(hash) {
		return ErrUnauthorized
	}
	policy, err := s.Policies.RemotePolicy(ctx, value.nodeID, value.generation)
	if err != nil || !policy.Enabled {
		return ErrDisabled
	}
	if now.Sub(value.windowStarted) >= time.Minute {
		value.windowStarted, value.windowCount = now, 0
	}
	if value.windowCount >= maxEventsPerMinute {
		return ErrRateLimited
	}
	value.windowCount++
	// Phones, operator links and scripts share one session with independent
	// clocks, so the server orders accepted events; the client value is ignored.
	value.lastSequence++
	event.Sequence = value.lastSequence
	value.events = append(value.events, event)
	if len(value.events) > maxBufferedEvents {
		value.events = append([]Event(nil), value.events[len(value.events)-maxBufferedEvents:]...)
	}
	close(value.wake)
	value.wake = make(chan struct{})
	return nil
}

func (s *Service) Poll(ctx context.Context, nodeID, id string, after int64, wait time.Duration) (EventPage, error) {
	if nodeID == "" || id == "" || after < 0 || wait < 0 || wait > 30*time.Second {
		return EventPage{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return EventPage{}, err
	}
	for {
		s.mutex.Lock()
		value := s.sessions[id]
		if value == nil || value.nodeID != nodeID {
			s.mutex.Unlock()
			return EventPage{}, ErrSessionMissing
		}
		if !s.Now().UTC().Before(value.expiresAt) {
			s.mutex.Unlock()
			return EventPage{}, ErrExpired
		}
		page := EventPage{Events: []Event{}, NextSequence: after}
		for _, event := range value.events {
			if event.Sequence > after {
				page.Events = append(page.Events, event)
				page.NextSequence = event.Sequence
			}
		}
		wake := value.wake
		s.mutex.Unlock()
		if len(page.Events) > 0 || wait == 0 {
			return page, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return EventPage{}, ctx.Err()
		case <-timer.C:
			return page, nil
		case <-wake:
			timer.Stop()
		}
	}
}

const (
	maxEventsPerMinute = 1200
	maxBufferedEvents  = 1024
)

func validateEvent(event Event) error {
	if event.Sequence <= 0 {
		return ErrInvalid
	}
	switch event.Type {
	case "key":
		switch event.Key {
		case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Enter", "Escape", "Backspace", "Tab", "VolumeUp", "VolumeDown", "Mute", "ChannelUp", "ChannelDown", "Guide", "Info",
			"Delete", "Home", "End", "PageUp", "PageDown", "Space", "BrowserBack", "BrowserForward", "BrowserReload":
		default:
			const channelPrefix = "ChannelNumber:"
			number, err := strconv.Atoi(strings.TrimPrefix(event.Key, channelPrefix))
			if !strings.HasPrefix(event.Key, channelPrefix) || err != nil || number < 1 || number > 999 {
				return fmt.Errorf("%w: unsupported key", ErrInvalid)
			}
		}
	case "text":
		if event.Text == "" || len([]rune(event.Text)) > 4096 {
			return ErrInvalid
		}
	case "pointer":
		if event.X < 0 || event.X > 1 || event.Y < 0 || event.Y > 1 {
			return ErrInvalid
		}
		if event.State != "move" && event.State != "down" && event.State != "up" && event.State != "click" {
			return ErrInvalid
		}
		if event.Button != "" && event.Button != "left" && event.Button != "right" && event.Button != "middle" {
			return ErrInvalid
		}
	case "wheel":
		if event.DeltaX < -10 || event.DeltaX > 10 || event.DeltaY < -10 || event.DeltaY > 10 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
