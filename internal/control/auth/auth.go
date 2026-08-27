package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	SecretPrefix       = "cable_admin_"
	ScopeSecurityAdmin = "security_admin"

	secretBytes       = 32
	encodedSecretSize = 43
	lookupPrefixSize  = 8
)

var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrInvalidCredential = errors.New("invalid administrator credential")
	ErrTokenRevoked      = errors.New("administrator token revoked")
	ErrInsufficientScope = errors.New("administrator token has insufficient scope")
	ErrActorRequired     = errors.New("authenticated actor required")
)

// Clock and RandomSource make all time- and entropy-dependent behavior injectable.
type Clock interface {
	Now() time.Time
}

type RandomSource interface {
	Read([]byte) (int, error)
}

// TokenRecord is the persistence form. Secret material is represented only by
// its SHA-256 hash and is never retained by Service.
type TokenRecord struct {
	ID        string
	ActorID   string
	Prefix    string
	Hash      [sha256.Size]byte
	Scopes    []string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// TokenMetadata is safe to return from list and mutation operations.
type TokenMetadata struct {
	ID        string
	ActorID   string
	Prefix    string
	Scopes    []string
	CreatedAt time.Time
	RevokedAt *time.Time
	Revoked   bool
}

type CreatedToken struct {
	TokenMetadata
	// Secret is returned only from Create. It is not part of TokenRecord and
	// therefore cannot be retrieved through this package after creation.
	Secret string
}

type Actor struct {
	ID      string
	TokenID string
	Scopes  []string
}

type TokenRepository interface {
	Create(context.Context, TokenRecord) error
	FindByPrefix(context.Context, string) ([]TokenRecord, error)
	List(context.Context) ([]TokenRecord, error)
	Revoke(context.Context, string, time.Time) (TokenRecord, error)
}

type Service struct {
	repository TokenRepository
	clock      Clock
	random     RandomSource
}

func NewService(repository TokenRepository, clock Clock, random RandomSource) *Service {
	return &Service{repository: repository, clock: clock, random: random}
}

// Create generates a 256-bit administrator secret for actorID. The returned
// secret is the only copy available to the caller.
func (s *Service) Create(ctx context.Context, actorID string, scopes []string) (CreatedToken, error) {
	if actorID == "" {
		return CreatedToken{}, fmt.Errorf("%w: actor ID is required", ErrInvalidArgument)
	}
	normalizedScopes, err := normalizeScopes(scopes)
	if err != nil {
		return CreatedToken{}, err
	}

	secret, prefix, hash, err := generateSecret(s.random)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate administrator token: %w", err)
	}
	id, err := randomUUID(s.random)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate administrator token ID: %w", err)
	}
	record := TokenRecord{
		ID:        id,
		ActorID:   actorID,
		Prefix:    prefix,
		Hash:      hash,
		Scopes:    normalizedScopes,
		CreatedAt: s.clock.Now().UTC(),
	}
	if err := s.repository.Create(ctx, cloneRecord(record)); err != nil {
		return CreatedToken{}, fmt.Errorf("create administrator token: %w", err)
	}
	return CreatedToken{TokenMetadata: metadata(record), Secret: secret}, nil
}

// Authenticate checks every record sharing the lookup prefix. Hash comparison
// is constant time and does not stop at the first prefix collision.
func (s *Service) Authenticate(ctx context.Context, secret string, requiredScopes ...string) (Actor, error) {
	prefix, hash, err := parseSecret(secret)
	if err != nil {
		return Actor{}, ErrInvalidCredential
	}
	records, err := s.repository.FindByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Actor{}, ErrInvalidCredential
		}
		return Actor{}, fmt.Errorf("find administrator token: %w", err)
	}

	matched := -1
	for index := range records {
		if subtle.ConstantTimeCompare(hash[:], records[index].Hash[:]) == 1 {
			matched = index
		}
	}
	if matched < 0 {
		return Actor{}, ErrInvalidCredential
	}
	record := records[matched]
	if record.RevokedAt != nil {
		return Actor{}, ErrTokenRevoked
	}
	for _, required := range requiredScopes {
		if required == "" {
			return Actor{}, fmt.Errorf("%w: required scope is empty", ErrInvalidArgument)
		}
		if !slices.Contains(record.Scopes, required) {
			return Actor{}, fmt.Errorf("%w: %s", ErrInsufficientScope, required)
		}
	}
	return Actor{ID: record.ActorID, TokenID: record.ID, Scopes: slices.Clone(record.Scopes)}, nil
}

// List returns safe metadata and requires a security_admin actor in ctx.
func (s *Service) List(ctx context.Context) ([]TokenMetadata, error) {
	if _, err := RequireScope(ctx, ScopeSecurityAdmin); err != nil {
		return nil, err
	}
	records, err := s.repository.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list administrator tokens: %w", err)
	}
	result := make([]TokenMetadata, len(records))
	for index := range records {
		result[index] = metadata(records[index])
	}
	return result, nil
}

// Revoke is idempotent at the token level and requires a security_admin actor
// in ctx. Repository implementations must return the current record when it
// was already revoked.
func (s *Service) Revoke(ctx context.Context, tokenID string) (TokenMetadata, error) {
	if _, err := RequireScope(ctx, ScopeSecurityAdmin); err != nil {
		return TokenMetadata{}, err
	}
	if tokenID == "" {
		return TokenMetadata{}, fmt.Errorf("%w: token ID is required", ErrInvalidArgument)
	}
	record, err := s.repository.Revoke(ctx, tokenID, s.clock.Now().UTC())
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("revoke administrator token: %w", err)
	}
	return metadata(record), nil
}

type actorContextKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	actor.Scopes = slices.Clone(actor.Scopes)
	return context.WithValue(ctx, actorContextKey{}, actor)
}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok || actor.ID == "" {
		return Actor{}, false
	}
	actor.Scopes = slices.Clone(actor.Scopes)
	return actor, true
}

func RequireScope(ctx context.Context, scope string) (Actor, error) {
	actor, ok := ActorFromContext(ctx)
	if !ok {
		return Actor{}, ErrActorRequired
	}
	if scope == "" {
		return Actor{}, fmt.Errorf("%w: required scope is empty", ErrInvalidArgument)
	}
	if !slices.Contains(actor.Scopes, scope) {
		return Actor{}, fmt.Errorf("%w: %s", ErrInsufficientScope, scope)
	}
	return actor, nil
}

func generateSecret(random RandomSource) (string, string, [sha256.Size]byte, error) {
	var raw [secretBytes]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", "", [sha256.Size]byte{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw[:])
	secret := SecretPrefix + encoded
	return secret, SecretPrefix + encoded[:lookupPrefixSize], sha256.Sum256([]byte(secret)), nil
}

func parseSecret(secret string) (string, [sha256.Size]byte, error) {
	if !strings.HasPrefix(secret, SecretPrefix) {
		return "", [sha256.Size]byte{}, ErrInvalidCredential
	}
	encoded := strings.TrimPrefix(secret, SecretPrefix)
	if len(encoded) != encodedSecretSize {
		return "", [sha256.Size]byte{}, ErrInvalidCredential
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != secretBytes {
		return "", [sha256.Size]byte{}, ErrInvalidCredential
	}
	return SecretPrefix + encoded[:lookupPrefixSize], sha256.Sum256([]byte(secret)), nil
}

func randomUUID(random RandomSource) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalidArgument)
	}
	result := slices.Clone(scopes)
	for _, scope := range result {
		if scope == "" || strings.TrimSpace(scope) != scope {
			return nil, fmt.Errorf("%w: scopes must be non-empty and trimmed", ErrInvalidArgument)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func metadata(record TokenRecord) TokenMetadata {
	var revokedAt *time.Time
	if record.RevokedAt != nil {
		value := *record.RevokedAt
		revokedAt = &value
	}
	return TokenMetadata{
		ID:        record.ID,
		ActorID:   record.ActorID,
		Prefix:    record.Prefix,
		Scopes:    slices.Clone(record.Scopes),
		CreatedAt: record.CreatedAt,
		RevokedAt: revokedAt,
		Revoked:   revokedAt != nil,
	}
}

func cloneRecord(record TokenRecord) TokenRecord {
	record.Scopes = slices.Clone(record.Scopes)
	if record.RevokedAt != nil {
		value := *record.RevokedAt
		record.RevokedAt = &value
	}
	return record
}
