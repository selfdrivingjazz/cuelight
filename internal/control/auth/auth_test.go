package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type memoryTokenRepository struct {
	records []TokenRecord
}

func (repository *memoryTokenRepository) Create(_ context.Context, record TokenRecord) error {
	for _, existing := range repository.records {
		if existing.ID == record.ID || existing.Prefix == record.Prefix && existing.Hash == record.Hash {
			return ErrAlreadyExists
		}
	}
	repository.records = append(repository.records, cloneRecord(record))
	return nil
}

func (repository *memoryTokenRepository) FindByPrefix(_ context.Context, prefix string) ([]TokenRecord, error) {
	var result []TokenRecord
	for _, record := range repository.records {
		if record.Prefix == prefix {
			result = append(result, cloneRecord(record))
		}
	}
	if len(result) == 0 {
		return nil, ErrNotFound
	}
	return result, nil
}

func (repository *memoryTokenRepository) List(context.Context) ([]TokenRecord, error) {
	result := make([]TokenRecord, len(repository.records))
	for index := range repository.records {
		result[index] = cloneRecord(repository.records[index])
	}
	return result, nil
}

func (repository *memoryTokenRepository) Revoke(_ context.Context, id string, at time.Time) (TokenRecord, error) {
	for index := range repository.records {
		if repository.records[index].ID != id {
			continue
		}
		if repository.records[index].RevokedAt == nil {
			repository.records[index].RevokedAt = &at
		}
		return cloneRecord(repository.records[index]), nil
	}
	return TokenRecord{}, ErrNotFound
}

func TestCreateReturnsOneTime256BitSecret(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.FixedZone("test", 3600))
	entropy := append(bytes.Repeat([]byte{0xa5}, secretBytes), bytes.Repeat([]byte{0x3c}, 16)...)
	repository := &memoryTokenRepository{}
	service := NewService(repository, fixedClock{now: now}, bytes.NewReader(entropy))

	created, err := service.Create(context.Background(), "actor-1", []string{"node_read", "security_admin", "node_read"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, SecretPrefix) {
		t.Fatalf("secret %q does not have prefix %q", created.Secret, SecretPrefix)
	}
	encoded := strings.TrimPrefix(created.Secret, SecretPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("secret is not unpadded base64url: %v", err)
	}
	if len(decoded) != secretBytes {
		t.Fatalf("decoded secret has %d bytes, want %d", len(decoded), secretBytes)
	}
	if len(repository.records) != 1 {
		t.Fatalf("repository contains %d records, want 1", len(repository.records))
	}
	record := repository.records[0]
	if record.Hash != sha256.Sum256([]byte(created.Secret)) {
		t.Fatal("stored hash does not match returned secret")
	}
	if created.Prefix != record.Prefix || created.Prefix != SecretPrefix+encoded[:lookupPrefixSize] {
		t.Fatalf("lookup prefix = %q, want %q", created.Prefix, SecretPrefix+encoded[:lookupPrefixSize])
	}
	if created.CreatedAt.Location() != time.UTC || !created.CreatedAt.Equal(now) {
		t.Fatalf("created time = %v, want UTC %v", created.CreatedAt, now)
	}
	if strings.Contains(string(record.Hash[:]), created.Secret) {
		t.Fatal("record unexpectedly contains the one-time secret")
	}
	if len(created.Scopes) != 2 || created.Scopes[0] != "node_read" || created.Scopes[1] != "security_admin" {
		t.Fatalf("normalized scopes = %v", created.Scopes)
	}
}

func TestAuthenticateHandlesLookupPrefixCollisions(t *testing.T) {
	firstRaw := bytes.Repeat([]byte{0x11}, secretBytes)
	secondRaw := bytes.Repeat([]byte{0x22}, secretBytes)
	copy(secondRaw[:6], firstRaw[:6])
	firstSecret := SecretPrefix + base64.RawURLEncoding.EncodeToString(firstRaw)
	secondSecret := SecretPrefix + base64.RawURLEncoding.EncodeToString(secondRaw)
	first := tokenRecord("token-1", "actor-1", firstSecret, "node_read")
	second := tokenRecord("token-2", "actor-2", secondSecret, "security_admin")
	if first.Prefix != second.Prefix {
		t.Fatalf("test secrets have different lookup prefixes: %q != %q", first.Prefix, second.Prefix)
	}
	repository := &memoryTokenRepository{records: []TokenRecord{first, second}}
	service := NewService(repository, fixedClock{}, bytes.NewReader(nil))

	actor, err := service.Authenticate(context.Background(), secondSecret, "security_admin")
	if err != nil {
		t.Fatal(err)
	}
	if actor.ID != "actor-2" || actor.TokenID != "token-2" {
		t.Fatalf("authenticated actor = %+v", actor)
	}
}

func TestAuthenticateRejectsWrongSecretWithKnownPrefix(t *testing.T) {
	raw := bytes.Repeat([]byte{0x31}, secretBytes)
	wrongRaw := bytes.Repeat([]byte{0x42}, secretBytes)
	copy(wrongRaw[:6], raw[:6])
	secret := SecretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	wrongSecret := SecretPrefix + base64.RawURLEncoding.EncodeToString(wrongRaw)
	service := NewService(
		&memoryTokenRepository{records: []TokenRecord{tokenRecord("token-1", "actor-1", secret, "node_read")}},
		fixedClock{},
		bytes.NewReader(nil),
	)

	_, err := service.Authenticate(context.Background(), wrongSecret)
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("Authenticate error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateRejectsMissingScope(t *testing.T) {
	secret := testSecret(0x51)
	service := NewService(
		&memoryTokenRepository{records: []TokenRecord{tokenRecord("token-1", "actor-1", secret, "node_read")}},
		fixedClock{},
		bytes.NewReader(nil),
	)

	_, err := service.Authenticate(context.Background(), secret, "security_admin")
	if !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("Authenticate error = %v, want ErrInsufficientScope", err)
	}
}

func TestRevokeReturnsMetadataAndPreventsAuthentication(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	secret := testSecret(0x61)
	repository := &memoryTokenRepository{records: []TokenRecord{tokenRecord("token-1", "actor-1", secret, "node_read")}}
	service := NewService(repository, fixedClock{now: now}, bytes.NewReader(nil))
	ctx := WithActor(context.Background(), Actor{ID: "security-actor", Scopes: []string{ScopeSecurityAdmin}})

	revoked, err := service.Revoke(ctx, "token-1")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Revoked || revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(now) {
		t.Fatalf("revoked metadata = %+v", revoked)
	}
	revokedAgain, err := service.Revoke(ctx, "token-1")
	if err != nil {
		t.Fatal(err)
	}
	if !revokedAgain.RevokedAt.Equal(now) {
		t.Fatalf("second revocation changed timestamp to %v", revokedAgain.RevokedAt)
	}
	_, err = service.Authenticate(context.Background(), secret)
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Authenticate error = %v, want ErrTokenRevoked", err)
	}
}

func TestListRequiresActorScopeAndReturnsOnlyMetadata(t *testing.T) {
	secret := testSecret(0x71)
	repository := &memoryTokenRepository{records: []TokenRecord{tokenRecord("token-1", "actor-1", secret, "node_read")}}
	service := NewService(repository, fixedClock{}, bytes.NewReader(nil))

	if _, err := service.List(WithActor(context.Background(), Actor{ID: "actor-2", Scopes: []string{"node_read"}})); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("List error = %v, want ErrInsufficientScope", err)
	}
	items, err := service.List(WithActor(context.Background(), Actor{ID: "actor-2", Scopes: []string{ScopeSecurityAdmin}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "token-1" || items[0].Prefix == "" {
		t.Fatalf("listed metadata = %+v", items)
	}
}

func tokenRecord(id, actorID, secret string, scopes ...string) TokenRecord {
	prefix, hash, err := parseSecret(secret)
	if err != nil {
		panic(err)
	}
	return TokenRecord{ID: id, ActorID: actorID, Prefix: prefix, Hash: hash, Scopes: scopes}
}

func testSecret(value byte) string {
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, secretBytes))
}
