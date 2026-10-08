// Package cableguide authorizes the control-hosted Cable guide (cable-web at
// /cable/) for one node and one deployment, and serves the guide's live
// presentation, media and remote-input stream.
package cableguide

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
	ErrInvalid      = errors.New("invalid cable guide request")
	ErrUnauthorized = errors.New("cable guide token is invalid or expired")
	ErrForbidden    = errors.New("node is not assigned to this Cable deployment")
	ErrNotFound     = errors.New("cable guide resource not found")
)

// DefaultTTL bounds a guide token; the guide renews well before expiry.
const DefaultTTL = 24 * time.Hour

// Grant is what a guide token authorizes: one node viewing one deployment.
type Grant struct {
	NodeID       string
	DeploymentID string
	ExpiresAt    time.Time
}

// Token is a freshly minted bearer token; only its hash is stored.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Repository interface {
	CreateCableGuideToken(ctx context.Context, hash []byte, grant Grant, now time.Time) error
	CableGuideGrant(ctx context.Context, hash []byte, now time.Time) (Grant, error)
	// NodeCableDeployment returns the deployment whose Cable media item the
	// node's current assignment targets ("" when none or the node is revoked).
	NodeCableDeployment(ctx context.Context, nodeID string, now time.Time) (string, error)
	DeploymentReferencesAsset(ctx context.Context, deploymentID, assetID string) (bool, error)
}

type Service struct {
	Repository Repository
	TTL        time.Duration
	Now        func() time.Time
	Random     io.Reader
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Mint issues a guide token for nodeID and deploymentID. It is refused unless
// the node's current assignment targets that deployment's Cable media item.
func (s *Service) Mint(ctx context.Context, nodeID, deploymentID string) (Token, error) {
	nodeID, deploymentID = strings.TrimSpace(nodeID), strings.TrimSpace(deploymentID)
	if nodeID == "" || deploymentID == "" {
		return Token{}, ErrInvalid
	}
	now := s.now()
	assigned, err := s.Repository.NodeCableDeployment(ctx, nodeID, now)
	if err != nil {
		return Token{}, err
	}
	if assigned == "" || assigned != deploymentID {
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
	grant := Grant{NodeID: nodeID, DeploymentID: deploymentID, ExpiresAt: now.Add(ttl)}
	if err := s.Repository.CreateCableGuideToken(ctx, hashToken(token), grant, now); err != nil {
		return Token{}, err
	}
	return Token{Token: token, ExpiresAt: grant.ExpiresAt}, nil
}

// Authenticate resolves a bearer token to its grant. A token stops working
// when it expires, its node is revoked, or the node is reassigned away from
// the deployment.
func (s *Service) Authenticate(ctx context.Context, token string) (Grant, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Grant{}, ErrUnauthorized
	}
	now := s.now()
	grant, err := s.Repository.CableGuideGrant(ctx, hashToken(token), now)
	if err != nil {
		return Grant{}, err
	}
	assigned, err := s.Repository.NodeCableDeployment(ctx, grant.NodeID, now)
	if err != nil {
		return Grant{}, err
	}
	if assigned != grant.DeploymentID {
		return Grant{}, ErrUnauthorized
	}
	return grant, nil
}

// Renew mints a successor token for an authenticated grant.
func (s *Service) Renew(ctx context.Context, grant Grant) (Token, error) {
	token, err := s.Mint(ctx, grant.NodeID, grant.DeploymentID)
	if errors.Is(err, ErrForbidden) {
		return Token{}, ErrUnauthorized
	}
	return token, err
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
