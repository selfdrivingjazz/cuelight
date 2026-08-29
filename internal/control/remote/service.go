package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	ErrReplay         = errors.New("remote event sequence is not newer")
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
	tokenHash     [32]byte
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
	value := &session{id: id, nodeID: nodeID, generation: generation, tokenHash: sha256.Sum256([]byte(token)), showQR: policy.ShowQR, expiresAt: now.Add(s.TTL), events: []Event{}, wake: make(chan struct{}), windowStarted: now}
	s.mutex.Lock()
	for sessionID, existing := range s.sessions {
		if existing.nodeID == nodeID {
			close(existing.wake)
			delete(s.sessions, sessionID)
		}
	}
	s.sessions[id] = value
	s.mutex.Unlock()
	base := strings.TrimSuffix(s.PublicURL, "/")
	return SessionCreated{ID: id, Token: token, RemoteURL: base + "/remote/" + url.PathEscape(id) + "#token=" + url.QueryEscape(token), ShowQR: policy.ShowQR, ExpiresAt: value.expiresAt}, nil
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
	if hash != value.tokenHash {
		return ErrUnauthorized
	}
	policy, err := s.Policies.RemotePolicy(ctx, value.nodeID, value.generation)
	if err != nil || !policy.Enabled {
		return ErrDisabled
	}
	if event.Sequence <= value.lastSequence {
		return ErrReplay
	}
	if now.Sub(value.windowStarted) >= time.Minute {
		value.windowStarted, value.windowCount = now, 0
	}
	if value.windowCount >= 120 {
		return ErrRateLimited
	}
	value.windowCount++
	value.lastSequence = event.Sequence
	value.events = append(value.events, event)
	if len(value.events) > 256 {
		value.events = append([]Event(nil), value.events[len(value.events)-256:]...)
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

func validateEvent(event Event) error {
	if event.Sequence <= 0 {
		return ErrInvalid
	}
	switch event.Type {
	case "key":
		switch event.Key {
		case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Enter", "Escape", "Backspace", "Tab", "VolumeUp", "VolumeDown", "Mute", "ChannelUp", "ChannelDown", "Guide", "Info":
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
