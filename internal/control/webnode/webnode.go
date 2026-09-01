// Package webnode contains the transport-independent domain used by browser nodes.
// It deliberately has no HTTP, SQL, or framework dependencies. Controllers can
// implement Repository and EventStream with their own persistence and transport.
package webnode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidNode            = errors.New("node ID is required")
	ErrInvalidCode            = errors.New("claim code is required")
	ErrActiveClaim            = errors.New("node already has an active claim")
	ErrClaimNotFound          = errors.New("claim code was not found")
	ErrClaimConsumed          = errors.New("claim code has already been consumed")
	ErrClaimExpired           = errors.New("claim code has expired")
	ErrCredentialNotFound     = errors.New("browser credential was not found")
	ErrCredentialRevoked      = errors.New("browser credential has been revoked")
	ErrCredentialExpired      = errors.New("browser credential has expired")
	ErrCredentialScope        = errors.New("browser credential scope is invalid")
	ErrCredentialNotRenewable = errors.New("credential is not renewable yet")
	ErrNodeMismatch           = errors.New("credential is bound to a different node")
	ErrAssignmentNotFound     = errors.New("assignment was not found")
	ErrAssignmentIsolation    = errors.New("assignment belongs to a different node")
	ErrInvalidAssignment      = errors.New("assignment node ID is required")
	ErrInvalidReport          = errors.New("playback report is invalid")
)

const CredentialScopeBrowser = "browser-node"

// Clock and IDGenerator make expiry and persistence tests deterministic.
type Clock interface{ Now() time.Time }
type IDGenerator interface {
	NewID() (string, error)
	NewToken() (string, error)
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type randomIDs struct{}

func (randomIDs) NewID() (string, error)    { return randomString(18) }
func (randomIDs) NewToken() (string, error) { return randomString(32) }

type Config struct {
	ClaimTTL      time.Duration
	CredentialTTL time.Duration
	RenewWindow   time.Duration
	Clock         Clock
	IDs           IDGenerator
}

func (c Config) withDefaults() Config {
	if c.ClaimTTL <= 0 {
		c.ClaimTTL = 10 * time.Minute
	}
	if c.CredentialTTL <= 0 {
		c.CredentialTTL = 30 * 24 * time.Hour
	}
	if c.RenewWindow <= 0 || c.RenewWindow >= c.CredentialTTL {
		c.RenewWindow = c.CredentialTTL / 4
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.IDs == nil {
		c.IDs = randomIDs{}
	}
	return c
}

type Claim struct {
	ID           string
	NodeID       string
	CodeHash     string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ConsumedAt   *time.Time
	CredentialID string
}
type ClaimCreated struct {
	Claim Claim
	Code  string
}

type Credential struct {
	ID        string
	NodeID    string
	Hash      string
	Scope     string
	IssuedAt  time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}
type CredentialIssued struct {
	Credential Credential
	Token      string
}

type Assignment struct {
	ID         string
	NodeID     string
	Sequence   int64
	EventEpoch string
	MediaURL   string
	MediaType  string
	DurationMs int64
	CreatedAt  time.Time
}
type AssignmentEvent struct {
	Assignment  Assignment
	PublishedAt time.Time
}

type PlaybackState string

const (
	PlaybackPlaying           PlaybackState = "playing"
	PlaybackAutoplaySuspended PlaybackState = "autoplay_suspended"
	PlaybackOffline           PlaybackState = "offline"
	PlaybackUnsupported       PlaybackState = "unsupported"
)

type PlaybackReport struct {
	AssignmentID string
	EventEpoch   string
	Sequence     int64
	State        PlaybackState
	PositionMs   int64
	ObservedAt   time.Time
}

// Repository is the persistence contract. Implementations must make
// CreateClaim's active-claim check and insertion atomic per node, and must
// update credentials by ID. MemoryRepository is provided for local composition
// and tests; PostgreSQL adapters need not change the service API.
type Repository interface {
	CreateClaim(context.Context, Claim) error
	ClaimByHash(context.Context, string) (Claim, bool, error)
	ConsumeClaimAndCreateCredential(context.Context, string, string, time.Time, Credential) error
	CredentialByHash(context.Context, string) (Credential, bool, error)
	UpdateCredential(context.Context, Credential) error
	RevokeCredential(context.Context, string, time.Time) error
	PutAssignment(context.Context, Assignment) error
	Assignment(context.Context, string) (Assignment, bool, error)
	LatestReport(context.Context, string) (PlaybackReport, bool, error)
	PutReport(context.Context, string, PlaybackReport) error
}

// TimedClaimRepository is optional. It lets an in-memory adapter use the
// service clock while retaining the small Repository contract for SQL stores.
type TimedClaimRepository interface {
	CreateClaimAt(context.Context, Claim, time.Time) error
}

// EventStream is injectable so the same domain can run over SSE, WebSocket, or
// a broker. Subscribe must only deliver events for nodeID and newer sequence.
type EventStream interface {
	Publish(context.Context, AssignmentEvent) error
	Subscribe(context.Context, string, int64) (<-chan AssignmentEvent, error)
}

type Service struct {
	Repo   Repository
	Events EventStream
	Config Config
}

func (s Service) normalized() (Service, error) {
	if s.Repo == nil {
		return s, errors.New("webnode repository is required")
	}
	if s.Events == nil {
		return s, errors.New("webnode event stream is required")
	}
	s.Config = s.Config.withDefaults()
	return s, nil
}

func (s Service) CreateClaim(ctx context.Context, nodeID string) (ClaimCreated, error) {
	if strings.TrimSpace(nodeID) == "" {
		return ClaimCreated{}, ErrInvalidNode
	}
	s, err := s.normalized()
	if err != nil {
		return ClaimCreated{}, err
	}
	now := s.Config.Clock.Now().UTC()
	// Repository enforces active-claim uniqueness atomically. This method's
	// interface intentionally avoids a check-then-insert race.
	id, err := s.Config.IDs.NewID()
	if err != nil {
		return ClaimCreated{}, err
	}
	code, err := generateClaimCode()
	if err != nil {
		return ClaimCreated{}, err
	}
	claim := Claim{ID: id, NodeID: nodeID, CodeHash: hash(code), CreatedAt: now, ExpiresAt: now.Add(s.Config.ClaimTTL)}
	if timed, ok := s.Repo.(TimedClaimRepository); ok {
		err = timed.CreateClaimAt(ctx, claim, now)
	} else {
		err = s.Repo.CreateClaim(ctx, claim)
	}
	if err != nil {
		return ClaimCreated{}, err
	}
	return ClaimCreated{Claim: claim, Code: code}, nil
}

func (s Service) ConsumeClaim(ctx context.Context, nodeID, code string) (CredentialIssued, error) {
	if strings.TrimSpace(nodeID) == "" {
		return CredentialIssued{}, ErrInvalidNode
	}
	if strings.TrimSpace(code) == "" {
		return CredentialIssued{}, ErrInvalidCode
	}
	s, err := s.normalized()
	if err != nil {
		return CredentialIssued{}, err
	}
	claim, ok, err := s.Repo.ClaimByHash(ctx, hash(normalizeCode(code)))
	if err != nil {
		return CredentialIssued{}, err
	}
	if !ok {
		return CredentialIssued{}, ErrClaimNotFound
	}
	if claim.NodeID != nodeID {
		return CredentialIssued{}, ErrNodeMismatch
	}
	now := s.Config.Clock.Now().UTC()
	if claim.ConsumedAt != nil {
		return CredentialIssued{}, ErrClaimConsumed
	}
	if !now.Before(claim.ExpiresAt) {
		return CredentialIssued{}, ErrClaimExpired
	}
	id, err := s.Config.IDs.NewID()
	if err != nil {
		return CredentialIssued{}, err
	}
	token, err := s.Config.IDs.NewToken()
	if err != nil {
		return CredentialIssued{}, err
	}
	credential := Credential{ID: id, NodeID: nodeID, Hash: hash(token), Scope: CredentialScopeBrowser, IssuedAt: now, ExpiresAt: now.Add(s.Config.CredentialTTL)}
	// Claim consumption and credential insertion are one repository transaction:
	// a storage failure cannot burn the one-time claim without issuing a token.
	if err := s.Repo.ConsumeClaimAndCreateCredential(ctx, claim.ID, nodeID, now, credential); err != nil {
		return CredentialIssued{}, err
	}
	return CredentialIssued{Credential: credential, Token: token}, nil
}

// Authorize validates the scoped credential and returns its persisted record.
func (s Service) Authorize(ctx context.Context, nodeID, token string) (Credential, error) {
	if strings.TrimSpace(nodeID) == "" {
		return Credential{}, ErrInvalidNode
	}
	if strings.TrimSpace(token) == "" {
		return Credential{}, ErrCredentialNotFound
	}
	s, err := s.normalized()
	if err != nil {
		return Credential{}, err
	}
	credential, ok, err := s.Repo.CredentialByHash(ctx, hash(token))
	if err != nil {
		return Credential{}, err
	}
	if !ok {
		return Credential{}, ErrCredentialNotFound
	}
	if credential.NodeID != nodeID {
		return Credential{}, ErrNodeMismatch
	}
	if credential.Scope != CredentialScopeBrowser {
		return Credential{}, ErrCredentialScope
	}
	now := s.Config.Clock.Now().UTC()
	if credential.RevokedAt != nil {
		return Credential{}, ErrCredentialRevoked
	}
	if !now.Before(credential.ExpiresAt) {
		return Credential{}, ErrCredentialExpired
	}
	return credential, nil
}

// Renew rotates a credential only inside its renewable window. The old token
// stops working as soon as the repository update commits.
func (s Service) Renew(ctx context.Context, nodeID, token string) (CredentialIssued, error) {
	credential, err := s.Authorize(ctx, nodeID, token)
	if err != nil {
		return CredentialIssued{}, err
	}
	s, err = s.normalized()
	if err != nil {
		return CredentialIssued{}, err
	}
	now := s.Config.Clock.Now().UTC()
	if now.Before(credential.ExpiresAt.Add(-s.Config.RenewWindow)) {
		return CredentialIssued{}, ErrCredentialNotRenewable
	}
	next, err := s.Config.IDs.NewToken()
	if err != nil {
		return CredentialIssued{}, err
	}
	credential.Hash, credential.ExpiresAt, credential.IssuedAt = hash(next), now.Add(s.Config.CredentialTTL), now
	if err := s.Repo.UpdateCredential(ctx, credential); err != nil {
		return CredentialIssued{}, err
	}
	return CredentialIssued{Credential: credential, Token: next}, nil
}
func (s Service) Revoke(ctx context.Context, nodeID, token string) error {
	credential, err := s.Authorize(ctx, nodeID, token)
	if err != nil {
		return err
	}
	s, err = s.normalized()
	if err != nil {
		return err
	}
	return s.Repo.RevokeCredential(ctx, credential.ID, s.Config.Clock.Now().UTC())
}

func (s Service) PublishAssignment(ctx context.Context, assignment Assignment) error {
	if strings.TrimSpace(assignment.ID) == "" || strings.TrimSpace(assignment.NodeID) == "" {
		return ErrInvalidAssignment
	}
	s, err := s.normalized()
	if err != nil {
		return err
	}
	if assignment.CreatedAt.IsZero() {
		assignment.CreatedAt = s.Config.Clock.Now().UTC()
	}
	if err := s.Repo.PutAssignment(ctx, assignment); err != nil {
		return err
	}
	return s.Events.Publish(ctx, AssignmentEvent{Assignment: assignment, PublishedAt: s.Config.Clock.Now().UTC()})
}
func (s Service) Subscribe(ctx context.Context, nodeID, token string, after int64) (<-chan AssignmentEvent, error) {
	if _, err := s.Authorize(ctx, nodeID, token); err != nil {
		return nil, err
	}
	s, err := s.normalized()
	if err != nil {
		return nil, err
	}
	return s.Events.Subscribe(ctx, nodeID, after)
}

func (s Service) Acknowledge(ctx context.Context, nodeID, token string, report PlaybackReport) error {
	if _, err := s.Authorize(ctx, nodeID, token); err != nil {
		return err
	}
	s, err := s.normalized()
	if err != nil {
		return err
	}
	assignment, ok, err := s.Repo.Assignment(ctx, report.AssignmentID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAssignmentNotFound
	}
	if assignment.NodeID != nodeID {
		return ErrAssignmentIsolation
	}
	if !validState(report.State) || report.Sequence < 0 || report.PositionMs < 0 {
		return ErrInvalidReport
	}
	if report.EventEpoch != assignment.EventEpoch || report.Sequence != assignment.Sequence {
		return ErrInvalidReport
	}
	if report.ObservedAt.IsZero() {
		report.ObservedAt = s.Config.Clock.Now().UTC()
	}
	return s.Repo.PutReport(ctx, nodeID, report)
}
func (s Service) ReportPresence(ctx context.Context, nodeID, token string, report PlaybackReport) error {
	return s.Acknowledge(ctx, nodeID, token, report)
}

func validState(state PlaybackState) bool {
	return state == PlaybackPlaying || state == PlaybackAutoplaySuspended || state == PlaybackOffline || state == PlaybackUnsupported
}
func normalizeCode(code string) string {
	return strings.ToUpper(strings.Join(strings.Fields(code), ""))
}
func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func generateClaimCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw[:4]) + "-" + string(raw[4:]), nil
}
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// MemoryRepository provides transaction-safe semantics for development and
// focused tests. Values are copied at the boundary so callers cannot mutate
// state without going through domain methods.
type MemoryRepository struct {
	mu          sync.RWMutex
	claims      map[string]Claim
	credentials map[string]Credential
	assignments map[string]Assignment
	reports     map[string]PlaybackReport
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{claims: map[string]Claim{}, credentials: map[string]Credential{}, assignments: map[string]Assignment{}, reports: map[string]PlaybackReport{}}
}
func (m *MemoryRepository) CreateClaim(ctx context.Context, claim Claim) error {
	return m.CreateClaimAt(ctx, claim, time.Now().UTC())
}
func (m *MemoryRepository) CreateClaimAt(_ context.Context, claim Claim, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.claims {
		if c.NodeID == claim.NodeID && c.ConsumedAt == nil && now.Before(c.ExpiresAt) {
			return ErrActiveClaim
		}
	}
	m.claims[claim.CodeHash] = claim
	return nil
}
func (m *MemoryRepository) ClaimByHash(_ context.Context, codeHash string) (Claim, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.claims[codeHash]
	return c, ok, nil
}
func (m *MemoryRepository) ConsumeClaimAndCreateCredential(_ context.Context, id, node string, at time.Time, credential Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, claim := range m.claims {
		if claim.ID != id {
			continue
		}
		if claim.NodeID != node {
			return ErrNodeMismatch
		}
		if claim.ConsumedAt != nil {
			return ErrClaimConsumed
		}
		if !at.Before(claim.ExpiresAt) {
			return ErrClaimExpired
		}
		if credential.NodeID != node || credential.Hash == "" {
			return ErrCredentialScope
		}
		if _, exists := m.credentials[credential.Hash]; exists {
			return ErrActiveClaim
		}
		consumedAt := at
		claim.ConsumedAt = &consumedAt
		claim.CredentialID = credential.ID
		m.claims[key] = claim
		m.credentials[credential.Hash] = credential
		return nil
	}
	return ErrClaimNotFound
}
func (m *MemoryRepository) CredentialByHash(_ context.Context, h string) (Credential, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.credentials[h]
	return c, ok, nil
}
func (m *MemoryRepository) UpdateCredential(_ context.Context, c Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, old := range m.credentials {
		if old.ID == c.ID {
			delete(m.credentials, h)
			m.credentials[c.Hash] = c
			return nil
		}
	}
	return ErrCredentialNotFound
}
func (m *MemoryRepository) RevokeCredential(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, c := range m.credentials {
		if c.ID == id {
			c.RevokedAt = &at
			m.credentials[h] = c
			return nil
		}
	}
	return ErrCredentialNotFound
}
func (m *MemoryRepository) PutAssignment(_ context.Context, a Assignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignments[a.ID] = a
	return nil
}
func (m *MemoryRepository) Assignment(_ context.Context, id string) (Assignment, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assignments[id]
	return a, ok, nil
}
func (m *MemoryRepository) LatestReport(_ context.Context, node string) (PlaybackReport, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.reports[node]
	return r, ok, nil
}
func (m *MemoryRepository) PutReport(_ context.Context, node string, r PlaybackReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.reports[node]; ok && old.EventEpoch == r.EventEpoch && old.Sequence > r.Sequence {
		return ErrInvalidReport
	}
	m.reports[node] = r
	return nil
}

// MemoryEventStream is a per-node, in-process stream with sequence-based
// replay. A caller can pass an after sequence and receives only newer events.
type memorySubscription struct {
	channel chan AssignmentEvent
	after   int64
}
type MemoryEventStream struct {
	mu          sync.Mutex
	subscribers map[string]map[*memorySubscription]struct{}
	history     []AssignmentEvent
}

func NewMemoryEventStream() *MemoryEventStream {
	return &MemoryEventStream{subscribers: map[string]map[*memorySubscription]struct{}{}}
}
func (m *MemoryEventStream) Publish(_ context.Context, event AssignmentEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, event)
	for sub := range m.subscribers[event.Assignment.NodeID] {
		if event.Assignment.Sequence <= sub.after {
			continue
		}
		select {
		case sub.channel <- event:
			sub.after = event.Assignment.Sequence
		default:
		}
	}
	return nil
}
func (m *MemoryEventStream) Subscribe(ctx context.Context, node string, after int64) (<-chan AssignmentEvent, error) {
	sub := &memorySubscription{channel: make(chan AssignmentEvent, 16), after: after}
	m.mu.Lock()
	if m.subscribers[node] == nil {
		m.subscribers[node] = map[*memorySubscription]struct{}{}
	}
	m.subscribers[node][sub] = struct{}{}
	for _, event := range m.history {
		if event.Assignment.NodeID == node && event.Assignment.Sequence > sub.after {
			select {
			case sub.channel <- event:
				sub.after = event.Assignment.Sequence
			default:
			}
		}
	}
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		if _, ok := m.subscribers[node][sub]; ok {
			delete(m.subscribers[node], sub)
			close(sub.channel)
		}
		m.mu.Unlock()
	}()
	return sub.channel, nil
}

// Ensure the implementation continues to satisfy the public contracts.
var _ Repository = (*MemoryRepository)(nil)
var _ TimedClaimRepository = (*MemoryRepository)(nil)
