// Package webnode pairs browser nodes (TV and desktop web browsers) with the
// control plane. A browser starts a claim and displays its short code; an
// operator redeems the code, which creates a node of class "browser"; the
// browser then collects a scoped bearer credential exactly once and uses it
// in place of mTLS for the player API.
package webnode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidRequest  = errors.New("invalid browser node request")
	ErrUnauthorized    = errors.New("browser node credential is not valid")
	ErrClaimNotFound   = errors.New("claim code is unknown or expired")
	ErrClaimConflict   = errors.New("claim code was already redeemed")
	ErrClaimGone       = errors.New("claim is expired or its credential was already delivered")
	ErrNodeNotFound    = errors.New("browser node not found")
	ErrCodeCollision   = errors.New("claim code collision")
	ErrNotConfigured   = errors.New("browser node service is not configured")
	errEmptyRandomness = errors.New("random source returned no data")
)

const (
	NodeClassBrowser = "browser"
	NodeClassNative  = "native"

	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	codeLength   = 8
	maxNameBytes = 120
	maxUAChars   = 300
)

type Claim struct {
	ID            string
	CodeHash      []byte
	PollTokenHash []byte
	UserAgent     string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	NodeID        string
	Delivered     bool
}

type Credential struct {
	ID         string
	NodeID     string
	TokenHash  []byte
	ExpiresAt  time.Time
	RenewAfter time.Time
	CreatedAt  time.Time
	// EventEpoch is the node's report epoch; filled on reads.
	EventEpoch string
}

// Node is the operator-facing view of a browser node.
type Node struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	NodeClass           string     `json:"nodeClass"`
	CredentialState     string     `json:"credentialState"`
	CredentialExpiresAt *time.Time `json:"credentialExpiresAt,omitempty"`
	LastSeenAt          *time.Time `json:"lastSeenAt,omitempty"`
	UserAgent           string     `json:"userAgent,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}

type ClaimStarted struct {
	ClaimID             string    `json:"claimId"`
	Code                string    `json:"code"`
	DisplayCode         string    `json:"displayCode"`
	PollToken           string    `json:"pollToken"`
	ExpiresAt           time.Time `json:"expiresAt"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
	PairURL             string    `json:"pairUrl,omitempty"`
}

type IssuedCredential struct {
	NodeID     string    `json:"nodeId"`
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expiresAt"`
	RenewAfter time.Time `json:"renewAfter"`
	EventEpoch string    `json:"eventEpoch"`
}

type ClaimStatus struct {
	Status     string            `json:"status"`
	ExpiresAt  *time.Time        `json:"expiresAt,omitempty"`
	Credential *IssuedCredential `json:"credential,omitempty"`
}

// Repository is the persistence contract. Every method that changes more
// than one row must be atomic.
type Repository interface {
	// CreateBrowserClaim inserts a claim and may purge expired unredeemed
	// claims. A duplicate code hash must return ErrCodeCollision.
	CreateBrowserClaim(ctx context.Context, claim Claim, now time.Time) error
	BrowserClaim(ctx context.Context, id string) (Claim, bool, error)
	// RedeemBrowserClaim locks the unexpired claim matching codeHash, creates
	// a browser node, and binds it to the claim.
	RedeemBrowserClaim(ctx context.Context, codeHash []byte, name, actorID string, now time.Time) (Node, error)
	// DeliverBrowserCredential inserts the first credential for a redeemed
	// claim and marks the claim delivered; returns ErrClaimGone if delivered.
	// It returns the node's event epoch.
	DeliverBrowserCredential(ctx context.Context, claimID string, credential Credential, now time.Time) (string, error)
	// BrowserCredential returns an unrevoked, unexpired credential whose node
	// is an active browser node, and records last-seen time.
	BrowserCredential(ctx context.Context, tokenHash []byte, now time.Time) (Credential, bool, error)
	RotateBrowserCredential(ctx context.Context, previousID string, next Credential, now time.Time) error
	ListBrowserNodes(ctx context.Context, now time.Time) ([]Node, error)
	RevokeBrowserNode(ctx context.Context, nodeID string, now time.Time) error
}

type Config struct {
	ClaimTTL      time.Duration
	CredentialTTL time.Duration
	RenewAfter    time.Duration
	PollInterval  time.Duration
	// PairBaseURL is the operator console origin; pairUrl is
	// PairBaseURL + "/pair?code=<code>".
	PairBaseURL string
}

func (c Config) normalized() Config {
	if c.ClaimTTL <= 0 {
		c.ClaimTTL = 10 * time.Minute
	}
	if c.CredentialTTL <= 0 {
		c.CredentialTTL = 30 * 24 * time.Hour
	}
	if c.RenewAfter <= 0 || c.RenewAfter >= c.CredentialTTL {
		c.RenewAfter = c.CredentialTTL / 2
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 3 * time.Second
	}
	return c
}

type Service struct {
	Repository Repository
	Config     Config
	Now        func() time.Time
	NewID      func() (string, error)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) ready() error {
	if s == nil || s.Repository == nil || s.NewID == nil {
		return ErrNotConfigured
	}
	return nil
}

// StartClaim creates a pending claim. The poll token is the only secret that
// lets this browser collect the credential later.
func (s *Service) StartClaim(ctx context.Context, userAgent string) (ClaimStarted, error) {
	if err := s.ready(); err != nil {
		return ClaimStarted{}, err
	}
	config := s.Config.normalized()
	userAgent = truncate(strings.TrimSpace(userAgent), maxUAChars)
	for range 4 {
		id, err := s.NewID()
		if err != nil {
			return ClaimStarted{}, err
		}
		code, err := generateCode()
		if err != nil {
			return ClaimStarted{}, err
		}
		pollToken, err := randomToken()
		if err != nil {
			return ClaimStarted{}, err
		}
		now := s.now()
		claim := Claim{ID: id, CodeHash: Hash(code), PollTokenHash: Hash(pollToken), UserAgent: userAgent, ExpiresAt: now.Add(config.ClaimTTL), CreatedAt: now}
		err = s.Repository.CreateBrowserClaim(ctx, claim, now)
		if errors.Is(err, ErrCodeCollision) {
			continue
		}
		if err != nil {
			return ClaimStarted{}, err
		}
		started := ClaimStarted{ClaimID: id, Code: code, DisplayCode: code[:4] + "-" + code[4:], PollToken: pollToken, ExpiresAt: claim.ExpiresAt, PollIntervalSeconds: int(config.PollInterval / time.Second)}
		if base := strings.TrimRight(config.PairBaseURL, "/"); base != "" {
			started.PairURL = base + "/pair?code=" + url.QueryEscape(code)
		}
		return started, nil
	}
	return ClaimStarted{}, ErrCodeCollision
}

// PollClaim reports a pending claim or, once redeemed, issues the node's
// first credential exactly once.
func (s *Service) PollClaim(ctx context.Context, claimID, pollToken string) (ClaimStatus, error) {
	if err := s.ready(); err != nil {
		return ClaimStatus{}, err
	}
	claim, ok, err := s.Repository.BrowserClaim(ctx, claimID)
	if err != nil {
		return ClaimStatus{}, err
	}
	if !ok || pollToken == "" || subtle.ConstantTimeCompare(claim.PollTokenHash, Hash(pollToken)) != 1 {
		return ClaimStatus{}, ErrUnauthorized
	}
	now := s.now()
	if claim.Delivered {
		return ClaimStatus{}, ErrClaimGone
	}
	if claim.NodeID == "" {
		if !now.Before(claim.ExpiresAt) {
			return ClaimStatus{}, ErrClaimGone
		}
		expires := claim.ExpiresAt
		return ClaimStatus{Status: "pending", ExpiresAt: &expires}, nil
	}
	issued, credential, err := s.newCredential(claim.NodeID, now)
	if err != nil {
		return ClaimStatus{}, err
	}
	issued.EventEpoch, err = s.Repository.DeliverBrowserCredential(ctx, claim.ID, credential, now)
	if err != nil {
		return ClaimStatus{}, err
	}
	return ClaimStatus{Status: "claimed", Credential: &issued}, nil
}

// RedeemClaim is the operator side of pairing.
func (s *Service) RedeemClaim(ctx context.Context, actorID, code, name string) (Node, error) {
	if err := s.ready(); err != nil {
		return Node{}, err
	}
	normalized := NormalizeCode(code)
	name = strings.TrimSpace(name)
	if len(normalized) != codeLength || name == "" || len(name) > maxNameBytes {
		return Node{}, fmt.Errorf("%w: code must be %d characters and name 1-%d bytes", ErrInvalidRequest, codeLength, maxNameBytes)
	}
	return s.Repository.RedeemBrowserClaim(ctx, Hash(normalized), name, actorID, s.now())
}

// Authenticate resolves a bearer token to its node.
func (s *Service) Authenticate(ctx context.Context, token string) (Credential, error) {
	if err := s.ready(); err != nil {
		return Credential{}, err
	}
	if token == "" {
		return Credential{}, ErrUnauthorized
	}
	credential, ok, err := s.Repository.BrowserCredential(ctx, Hash(token), s.now())
	if err != nil {
		return Credential{}, err
	}
	if !ok {
		return Credential{}, ErrUnauthorized
	}
	return credential, nil
}

// Renew rotates the presented credential; the old token stops working when
// the rotation commits.
func (s *Service) Renew(ctx context.Context, token string) (IssuedCredential, error) {
	current, err := s.Authenticate(ctx, token)
	if err != nil {
		return IssuedCredential{}, err
	}
	now := s.now()
	issued, next, err := s.newCredential(current.NodeID, now)
	if err != nil {
		return IssuedCredential{}, err
	}
	if err := s.Repository.RotateBrowserCredential(ctx, current.ID, next, now); err != nil {
		return IssuedCredential{}, err
	}
	issued.EventEpoch = current.EventEpoch
	return issued, nil
}

func (s *Service) ListNodes(ctx context.Context) ([]Node, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.Repository.ListBrowserNodes(ctx, s.now())
}

func (s *Service) RevokeNode(ctx context.Context, nodeID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(nodeID) == "" {
		return ErrNodeNotFound
	}
	return s.Repository.RevokeBrowserNode(ctx, nodeID, s.now())
}

func (s *Service) newCredential(nodeID string, now time.Time) (IssuedCredential, Credential, error) {
	config := s.Config.normalized()
	id, err := s.NewID()
	if err != nil {
		return IssuedCredential{}, Credential{}, err
	}
	token, err := randomToken()
	if err != nil {
		return IssuedCredential{}, Credential{}, err
	}
	credential := Credential{ID: id, NodeID: nodeID, TokenHash: Hash(token), ExpiresAt: now.Add(config.CredentialTTL), RenewAfter: now.Add(config.RenewAfter), CreatedAt: now}
	return IssuedCredential{NodeID: nodeID, Token: token, ExpiresAt: credential.ExpiresAt, RenewAfter: credential.RenewAfter}, credential, nil
}

// NormalizeCode accepts codes typed with any case, spaces, or dashes.
func NormalizeCode(code string) string {
	var builder strings.Builder
	for _, r := range strings.ToUpper(code) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func Hash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func generateCode() (string, error) {
	limit := big.NewInt(int64(len(codeAlphabet)))
	out := make([]byte, codeLength)
	for index := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[index] = codeAlphabet[n.Int64()]
	}
	return string(out), nil
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	n, err := rand.Read(buffer)
	if err != nil {
		return "", err
	}
	if n != len(buffer) {
		return "", errEmptyRandomness
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
