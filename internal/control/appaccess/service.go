// Package appaccess lets web media items with app access ("Cuelight apps")
// read the media library and the screen's remote input. A node presenting such
// an item mints an app token for it; the page receives the token in its URL
// fragment and calls the read-only /app API with it.
package appaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid app access request")
	ErrUnauthorized = errors.New("app token is invalid or expired")
	ErrForbidden    = errors.New("node is not assigned to this app media item")
	ErrNotFound     = errors.New("app resource not found")
)

// DefaultTTL bounds an app token; pages renew well before expiry.
const DefaultTTL = 24 * time.Hour

// Grant is what an app token authorizes: one node showing one media item.
type Grant struct {
	NodeID    string
	NodeName  string
	MediaID   string
	ExpiresAt time.Time
}

// Token is a freshly minted bearer token; only its hash is stored. APIURL is
// the control plane's public API base the page should call.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	APIURL    string    `json:"apiUrl"`
}

type Repository interface {
	CreateAppToken(ctx context.Context, hash []byte, grant Grant, now time.Time) error
	// AppGrant resolves a stored, unexpired token of a node that still holds
	// a valid credential; ErrUnauthorized otherwise.
	AppGrant(ctx context.Context, hash []byte, now time.Time) (Grant, error)
	// NodeAppMedia returns the app-access web media item the node's current
	// assignment targets ("" when none or the node is revoked).
	NodeAppMedia(ctx context.Context, nodeID string, now time.Time) (string, error)
}

type Service struct {
	Repository Repository
	// APIURL is the public API base (https://<control>/api/v1) handed to
	// pages; empty lets handlers derive it from the request.
	APIURL string
	TTL    time.Duration
	Now    func() time.Time
	Random io.Reader
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Mint issues an app token for nodeID and mediaID. It is refused unless the
// node's current assignment targets that app-access media item.
func (s *Service) Mint(ctx context.Context, nodeID, mediaID string) (Token, error) {
	nodeID, mediaID = strings.TrimSpace(nodeID), strings.TrimSpace(mediaID)
	if nodeID == "" || mediaID == "" {
		return Token{}, ErrInvalid
	}
	now := s.now()
	assigned, err := s.Repository.NodeAppMedia(ctx, nodeID, now)
	if err != nil {
		return Token{}, err
	}
	if assigned == "" || assigned != mediaID {
		return Token{}, ErrForbidden
	}
	random := s.Random
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(random, raw); err != nil {
		return Token{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	grant := Grant{NodeID: nodeID, MediaID: mediaID, ExpiresAt: now.Add(ttl)}
	if err := s.Repository.CreateAppToken(ctx, hashToken(token), grant, now); err != nil {
		return Token{}, err
	}
	return Token{Token: token, ExpiresAt: grant.ExpiresAt, APIURL: s.APIURL}, nil
}

// Authenticate resolves a bearer token to its grant. A token stops working
// when it expires, its node is revoked or unregistered, the item loses app
// access, or the node is reassigned away from the item.
func (s *Service) Authenticate(ctx context.Context, token string) (Grant, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Grant{}, ErrUnauthorized
	}
	now := s.now()
	grant, err := s.Repository.AppGrant(ctx, hashToken(token), now)
	if err != nil {
		return Grant{}, err
	}
	assigned, err := s.Repository.NodeAppMedia(ctx, grant.NodeID, now)
	if err != nil {
		return Grant{}, err
	}
	if assigned != grant.MediaID {
		return Grant{}, ErrUnauthorized
	}
	return grant, nil
}

// Renew mints a successor token for an authenticated grant.
func (s *Service) Renew(ctx context.Context, grant Grant) (Token, error) {
	token, err := s.Mint(ctx, grant.NodeID, grant.MediaID)
	if errors.Is(err, ErrForbidden) {
		return Token{}, ErrUnauthorized
	}
	return token, err
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
