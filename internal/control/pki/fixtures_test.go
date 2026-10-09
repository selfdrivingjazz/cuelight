package pki

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"
)

type fixedClock struct {
	now time.Time
}

func (clock *fixedClock) Now() time.Time { return clock.now }

type sequenceSource struct {
	serial int64
	epoch  int64
}

func (source *sequenceSource) NextSerial(context.Context) (*big.Int, error) {
	source.serial++
	return big.NewInt(source.serial), nil
}

func (source *sequenceSource) NextEpoch(context.Context) (string, error) {
	source.epoch++
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", source.epoch), nil
}

type counterReader struct {
	next byte
}

func (reader *counterReader) Read(value []byte) (int, error) {
	for index := range value {
		reader.next++
		value[index] = reader.next
	}
	return len(value), nil
}

type cachedIdempotency struct {
	requestHash string
	response    []byte
	expiresAt   time.Time
}

type memoryRepository struct {
	mu          sync.Mutex
	tokens      map[TokenHash]Token
	nodes       map[string]Node
	creds       map[string]Credential
	idempotency map[string]cachedIdempotency
	nextToken   int
	nextNode    int
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		tokens:      make(map[TokenHash]Token),
		nodes:       make(map[string]Node),
		creds:       make(map[string]Credential),
		idempotency: make(map[string]cachedIdempotency),
	}
}

func (repository *memoryRepository) IdempotentTransaction(ctx context.Context, request IdempotencyRequest, fn func(Store) ([]byte, error)) ([]byte, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Operation == "" || request.Key == "" || request.RequestHash == "" || !request.ExpiresAt.After(request.CreatedAt) {
		return nil, ErrInvalidMutation
	}
	key := request.ActorID + "\x00" + string(request.Operation) + "\x00" + request.Key
	if cached, exists := repository.idempotency[key]; exists && cached.expiresAt.After(request.CreatedAt) {
		if cached.requestHash != request.RequestHash {
			return nil, ErrIdempotencyConflict
		}
		return append([]byte(nil), cached.response...), nil
	}

	tokens := cloneTokens(repository.tokens)
	nodes := cloneNodes(repository.nodes)
	credentials := cloneCredentials(repository.creds)
	idempotency := cloneIdempotency(repository.idempotency)
	nextToken, nextNode := repository.nextToken, repository.nextNode
	rollback := func() {
		repository.tokens = tokens
		repository.nodes = nodes
		repository.creds = credentials
		repository.idempotency = idempotency
		repository.nextToken = nextToken
		repository.nextNode = nextNode
	}
	response, err := fn(repository)
	if err != nil {
		rollback()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		rollback()
		return nil, err
	}
	repository.idempotency[key] = cachedIdempotency{
		requestHash: request.RequestHash,
		response:    append([]byte(nil), response...),
		expiresAt:   request.ExpiresAt,
	}
	return append([]byte(nil), response...), nil
}

func (repository *memoryRepository) CreateToken(_ context.Context, token Token) (Token, error) {
	if _, exists := repository.tokens[token.Hash]; exists {
		return Token{}, fmt.Errorf("duplicate token")
	}
	repository.nextToken++
	token.ID = fmt.Sprintf("token-%d", repository.nextToken)
	repository.tokens[token.Hash] = cloneToken(token)
	return cloneToken(token), nil
}

func (repository *memoryRepository) ConsumeToken(_ context.Context, hash TokenHash, at time.Time) (Token, error) {
	token, exists := repository.tokens[hash]
	if !exists {
		return Token{}, ErrTokenNotFound
	}
	if token.ConsumedAt != nil {
		return Token{}, ErrTokenConsumed
	}
	consumedAt := at
	token.ConsumedAt = &consumedAt
	repository.tokens[hash] = cloneToken(token)
	return cloneToken(token), nil
}

func (repository *memoryRepository) CreateNode(_ context.Context, name, epoch string, at time.Time) (Node, error) {
	repository.nextNode++
	id := fmt.Sprintf("node-%d", repository.nextNode)
	if name == "" {
		name = id
	}
	node := Node{ID: id, Name: name, EventEpoch: epoch, CredentialState: CredentialActive, CreatedAt: at}
	repository.nodes[id] = node
	return node, nil
}

func (repository *memoryRepository) Node(_ context.Context, id string) (Node, error) {
	node, exists := repository.nodes[id]
	if !exists {
		return Node{}, ErrNodeNotFound
	}
	return node, nil
}

func (repository *memoryRepository) UpdateNode(_ context.Context, node Node) error {
	if _, exists := repository.nodes[node.ID]; !exists {
		return ErrNodeNotFound
	}
	repository.nodes[node.ID] = node
	return nil
}

func (repository *memoryRepository) CreateCredential(_ context.Context, credential Credential) error {
	key := credentialKey(credential.NodeID, credential.Serial)
	if _, exists := repository.creds[key]; exists {
		return fmt.Errorf("duplicate credential")
	}
	repository.creds[key] = cloneCredential(credential)
	return nil
}

func (repository *memoryRepository) Credential(_ context.Context, nodeID, serial string) (Credential, error) {
	credential, exists := repository.creds[credentialKey(nodeID, serial)]
	if !exists {
		return Credential{}, ErrCredentialNotFound
	}
	return cloneCredential(credential), nil
}

func (repository *memoryRepository) HasActiveCredential(_ context.Context, nodeID string, at time.Time) (bool, error) {
	for _, credential := range repository.creds {
		if credential.NodeID == nodeID && credential.RevokedAt == nil && credential.NotAfter.After(at) {
			return true, nil
		}
	}
	return false, nil
}

func (repository *memoryRepository) RevokeCredential(_ context.Context, nodeID, serial string, at time.Time) (Credential, error) {
	key := credentialKey(nodeID, serial)
	credential, exists := repository.creds[key]
	if !exists {
		return Credential{}, ErrCredentialNotFound
	}
	if credential.RevokedAt != nil {
		return Credential{}, ErrCredentialRevoked
	}
	revokedAt := at
	credential.RevokedAt = &revokedAt
	repository.creds[key] = credential
	return cloneCredential(credential), nil
}

func (repository *memoryRepository) RevokeNodeCredentials(_ context.Context, nodeID string, at time.Time) ([]Credential, error) {
	var revoked []Credential
	for key, credential := range repository.creds {
		if credential.NodeID != nodeID || credential.RevokedAt != nil {
			continue
		}
		revokedAt := at
		credential.RevokedAt = &revokedAt
		repository.creds[key] = credential
		revoked = append(revoked, cloneCredential(credential))
	}
	return revoked, nil
}

func credentialKey(nodeID, serial string) string { return nodeID + "\x00" + serial }

func cloneTokens(source map[TokenHash]Token) map[TokenHash]Token {
	result := make(map[TokenHash]Token, len(source))
	for hash, token := range source {
		result[hash] = cloneToken(token)
	}
	return result
}

func cloneToken(token Token) Token {
	if token.ConsumedAt != nil {
		consumedAt := *token.ConsumedAt
		token.ConsumedAt = &consumedAt
	}
	return token
}

func cloneNodes(source map[string]Node) map[string]Node {
	result := make(map[string]Node, len(source))
	for id, node := range source {
		result[id] = node
	}
	return result
}

func cloneCredentials(source map[string]Credential) map[string]Credential {
	result := make(map[string]Credential, len(source))
	for key, credential := range source {
		result[key] = cloneCredential(credential)
	}
	return result
}

func cloneIdempotency(source map[string]cachedIdempotency) map[string]cachedIdempotency {
	result := make(map[string]cachedIdempotency, len(source))
	for key, cached := range source {
		cached.response = append([]byte(nil), cached.response...)
		result[key] = cached
	}
	return result
}

func cloneCredential(credential Credential) Credential {
	if credential.RevokedAt != nil {
		revokedAt := *credential.RevokedAt
		credential.RevokedAt = &revokedAt
	}
	return credential
}

type testEnvironment struct {
	service    *Service
	repository *memoryRepository
	clock      *fixedClock
	ca         *x509.Certificate
	publisher  ed25519.PublicKey
}

func newTestEnvironment(t *testing.T) testEnvironment {
	t.Helper()
	now := time.Date(2032, time.March, 4, 5, 6, 7, 0, time.UTC)
	caPublicKey, caPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(9000),
		Subject:               pkix.Name{CommonName: "Cuelight test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublicKey, caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewCertificateAuthority(caCertificate, caPrivateKey, 24*time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	publisher, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	clock := &fixedClock{now: now}
	source := &sequenceSource{serial: 100}
	sealer, err := NewAESGCMSealer(bytes.Repeat([]byte{0x5a}, AESGCMKeySize), &counterReader{next: 100})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		repository,
		authority,
		sealer,
		clock,
		source,
		source,
		PublisherKey{ID: "publisher-1", PublicKey: publisher},
		ServiceOptions{TokenLifetime: time.Hour, IdempotencyLifetime: 24 * time.Hour, Random: &counterReader{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return testEnvironment{service: service, repository: repository, clock: clock, ca: caCertificate, publisher: publisher}
}

func newPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey
}

func enrollNode(t *testing.T, environment testEnvironment) (Enrollment, TokenSecret) {
	t.Helper()
	token, err := environment.service.CreateEnrollmentToken(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "enroll-token-1"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := environment.service.Enroll(context.Background(), EnrollRequest{
		Token:          token.Secret,
		IdempotencyKey: "enroll-node-1",
		Credential:     CredentialRequest{PublicKey: newPublicKey(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return enrollment, token
}
