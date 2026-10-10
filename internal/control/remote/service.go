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

// PolicyRepository reports a node's current desired generation and the remote
// policy of its assignment. RemotePolicy's Enabled holds only when every
// assignment from generation since through the current one allowed remote
// input, so a session outlives reassignments that keep remote input on but
// never one that turned it off. CurrentRemotePolicy reports the current
// assignment's policy alone. An unknown node, a node without an assignment or
// a since beyond the current generation is ErrDisabled.
type PolicyRepository interface {
	RemotePolicy(ctx context.Context, nodeID string, since int64) (Policy, int64, error)
	CurrentRemotePolicy(ctx context.Context, nodeID string) (Policy, int64, error)
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
	id, nodeID string
	generation int64
	// qrHash is the node-issued token the screen currently shows; tokenHashes
	// holds earlier node-issued tokens and operator-minted links.
	qrHash        [32]byte
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
	// Concurrent first calls may fill the defaults at once.
	s.mutex.Lock()
	defer s.mutex.Unlock()
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

// Create issues the node a session for its current desired generation. A
// still-active session whose remote input stayed on since its generation is
// rebound instead of replaced, so phones holding its earlier tokens keep
// working across reassignments; otherwise the node's sessions are replaced.
func (s *Service) Create(ctx context.Context, nodeID string, generation int64) (SessionCreated, error) {
	if nodeID == "" || generation <= 0 {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	since, existingID := generation, ""
	s.mutex.Lock()
	if existing := s.activeSession(nodeID); existing != nil && existing.generation <= generation {
		since, existingID = existing.generation, existing.id
	}
	s.mutex.Unlock()
	policy, err := s.currentPolicy(ctx, nodeID, since, generation)
	if errors.Is(err, ErrDisabled) && since != generation {
		since, existingID = generation, ""
		policy, err = s.currentPolicy(ctx, nodeID, generation, generation)
	}
	if err != nil {
		return SessionCreated{}, err
	}
	now := s.Now().UTC()
	fresh, token, err := s.newSession(nodeID, generation, policy.ShowQR, now)
	if err != nil {
		return SessionCreated{}, err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if value := s.sessions[existingID]; value != nil && value.generation == since && now.Before(value.expiresAt) {
		value.tokenHashes = appendBounded(value.tokenHashes, value.qrHash)
		value.qrHash = fresh.qrHash
		value.generation, value.showQR, value.expiresAt = generation, policy.ShowQR, fresh.expiresAt
		// The new presentation polls from the start; earlier input is not its.
		value.events = []Event{}
		return s.created(value.id, token, value.showQR, value.expiresAt), nil
	}
	s.install(fresh)
	return s.created(fresh.id, token, fresh.showQR, fresh.expiresAt), nil
}

// newSession builds a session for the node's generation along with its
// on-screen token; the caller installs it.
func (s *Service) newSession(nodeID string, generation int64, showQR bool, now time.Time) (*session, string, error) {
	idBytes, tokenBytes := make([]byte, 16), make([]byte, 32)
	if _, err := io.ReadFull(s.Random, idBytes); err != nil {
		return nil, "", err
	}
	if _, err := io.ReadFull(s.Random, tokenBytes); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	// Sequences start at the creation time in microseconds so they stay
	// monotonic across session renewals and control-plane restarts; consumers
	// holding an older cursor keep receiving the new session's events.
	return &session{id: base64.RawURLEncoding.EncodeToString(idBytes), nodeID: nodeID, generation: generation, qrHash: sha256.Sum256([]byte(token)), showQR: showQR, expiresAt: now.Add(s.TTL), events: []Event{}, wake: make(chan struct{}), windowStarted: now, lastSequence: now.UnixMicro()}, token, nil
}

// install makes value its node's only session, ending the node's others.
// The caller holds the mutex.
func (s *Service) install(value *session) {
	for sessionID, existing := range s.sessions {
		if existing.nodeID == value.nodeID {
			close(existing.wake)
			delete(s.sessions, sessionID)
		}
	}
	s.sessions[value.id] = value
}

// restore gives a node without an active session one for its current
// generation, as the node's own Create would, when that generation's
// assignment allows remote input. Sessions live only in memory, so this is
// how a screen's in-page app and operator links recover after a control-plane
// restart; the node's next Create rebinds the restored session instead of
// replacing it. A node without an assignment allowing remote input stays
// without a session: ErrSessionMissing.
func (s *Service) restore(ctx context.Context, nodeID string) error {
	s.mutex.Lock()
	active := s.activeSession(nodeID) != nil
	s.mutex.Unlock()
	if active {
		return nil
	}
	policy, generation, err := s.Policies.CurrentRemotePolicy(ctx, nodeID)
	if errors.Is(err, ErrDisabled) || (err == nil && (!policy.Enabled || generation <= 0)) {
		return ErrSessionMissing
	}
	if err != nil {
		return err
	}
	value, _, err := s.newSession(nodeID, generation, policy.ShowQR, s.Now().UTC())
	if err != nil {
		return err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	// A concurrent caller or the node's own Create may have installed one
	// during the lookup; keep that one.
	if s.activeSession(nodeID) == nil {
		s.install(value)
	}
	return nil
}

// currentPolicy returns the node's remote policy when generation is current
// and remote input stayed on from generation since through it.
func (s *Service) currentPolicy(ctx context.Context, nodeID string, since, generation int64) (Policy, error) {
	policy, current, err := s.Policies.RemotePolicy(ctx, nodeID, since)
	if err != nil {
		return Policy{}, err
	}
	if !policy.Enabled || current != generation {
		return Policy{}, ErrDisabled
	}
	return policy, nil
}

func (s *Service) created(id, token string, showQR bool, expiresAt time.Time) SessionCreated {
	base := strings.TrimSuffix(s.PublicURL, "/")
	return SessionCreated{ID: id, Token: token, RemoteURL: base + "/remote/" + url.PathEscape(id) + "#token=" + url.QueryEscape(token), ShowQR: showQR, ExpiresAt: expiresAt}
}

// maxSessionTokens bounds the earlier node-issued tokens and operator-minted
// links a session keeps accepting besides its current on-screen and app
// tokens; the oldest is evicted first.
const maxSessionTokens = 8

func appendBounded(hashes [][32]byte, hash [32]byte) [][32]byte {
	if len(hashes) >= maxSessionTokens {
		hashes = append(hashes[:0], hashes[1:]...)
	}
	return append(hashes, hash)
}

// Link mints an additional phone token for the node's active session so an
// operator can obtain a remote URL without seeing the on-screen QR code. A
// node without one gets a session restored for its current assignment.
func (s *Service) Link(ctx context.Context, nodeID string) (SessionCreated, error) {
	if nodeID == "" {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	if err := s.restore(ctx, nodeID); err != nil {
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
	policy, _, err := s.Policies.RemotePolicy(ctx, nodeID, generation)
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
	value.tokenHashes = appendBounded(value.tokenHashes, sha256.Sum256([]byte(token)))
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
// eviction never invalidates it. A node without an active session gets one
// restored for its current assignment.
func (s *Service) AppSession(ctx context.Context, nodeID string) (SessionCreated, error) {
	if nodeID == "" {
		return SessionCreated{}, ErrInvalid
	}
	if err := s.normalized(); err != nil {
		return SessionCreated{}, err
	}
	if err := s.restore(ctx, nodeID); err != nil {
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
	matched := subtle.ConstantTimeCompare(hash[:], value.qrHash[:])
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
	// Remote input must have stayed on since the session's generation; later
	// reassignments that keep it on leave the session usable.
	policy, _, err := s.Policies.RemotePolicy(ctx, value.nodeID, value.generation)
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
