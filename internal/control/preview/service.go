// Package preview contains controller-side expected Cable preview derivation and
// the durable screenshot-job state machine.
package preview

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidArgument     = errors.New("invalid preview argument")
	ErrUnauthorized        = errors.New("preview authorization required")
	ErrForbidden           = errors.New("preview access forbidden")
	ErrNotFound            = errors.New("screenshot job not found")
	ErrConflict            = errors.New("screenshot job conflict")
	ErrExpired             = errors.New("screenshot job expired")
	ErrTerminal            = errors.New("screenshot job is terminal")
	ErrNotClaimed          = errors.New("screenshot job is not claimed")
	ErrIdempotencyConflict = errors.New("idempotency key was already used with a different request")
	ErrArtifactUnavailable = errors.New("preview result artifact is unavailable")
	ErrArtifactMismatch    = errors.New("preview result artifact metadata does not match")
)

// ExpectedPreview is a deterministic, explicitly expected (not observed)
// rendering target derived from the node's healthy release and an authoritative
// immutable Cable revision at EffectiveAt.
type ExpectedPreview struct {
	Expected           bool      `json:"expected"`
	ReleaseDigest      string    `json:"releaseDigest"`
	RevisionDigest     string    `json:"revisionDigest"`
	ChannelID          string    `json:"channelId,omitempty"`
	BlockID            string    `json:"blockId,omitempty"`
	ItemID             string    `json:"itemId,omitempty"`
	EffectiveAt        time.Time `json:"effectiveAt"`
	ChannelWindowStart time.Time `json:"channelWindowStart,omitempty"`
	ChannelWindowEnd   time.Time `json:"channelWindowEnd,omitempty"`
	BlockWindowStart   time.Time `json:"blockWindowStart,omitempty"`
	BlockWindowEnd     time.Time `json:"blockWindowEnd,omitempty"`
	ItemWindowStart    time.Time `json:"itemWindowStart,omitempty"`
	ItemWindowEnd      time.Time `json:"itemWindowEnd,omitempty"`
}

type Status string

const (
	StatusQueued      Status = "queued"
	StatusClaimed     Status = "claimed"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusUnsupported Status = "unsupported"
	StatusExpired     Status = "expired"
)

func (s Status) terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusUnsupported || s == StatusExpired
}

type ScreenshotJob struct {
	ID                     string          `json:"id"`
	NodeID                 string          `json:"nodeId"`
	Expected               ExpectedPreview `json:"expected"`
	RequestedAt            time.Time       `json:"requestedAt"`
	ExpiresAt              time.Time       `json:"expiresAt"`
	ClaimedAt              *time.Time      `json:"claimedAt,omitempty"`
	CompletionAt           *time.Time      `json:"completionAt,omitempty"`
	Status                 Status          `json:"status"`
	ResultArtifactDigest   string          `json:"resultArtifactDigest,omitempty"`
	ResultMediaType        string          `json:"resultMediaType,omitempty"`
	ErrorReason            string          `json:"errorReason,omitempty"`
	RetentionPolicy        string          `json:"retentionPolicy"`
	RetentionUntil         time.Time       `json:"retentionUntil"`
	ArtifactRetentionUntil time.Time       `json:"artifactRetentionUntil"`
}

type Mutation struct{ ActorID, IdempotencyKey string }
type CreateInput struct {
	NodeID   string          `json:"nodeId"`
	Expected ExpectedPreview `json:"expected"`
	TTL      time.Duration   `json:"ttl"`
}
type Completion struct {
	Status         Status `json:"status"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
	MediaType      string `json:"mediaType,omitempty"`
	ErrorReason    string `json:"errorReason,omitempty"`
}

// ArtifactRecord is the immutable catalog metadata for a result object. A
// screenshot completion is accepted only when the catalog confirms this exact
// digest and media type is currently ready.
type ArtifactRecord struct {
	Digest    string
	MediaType string
	State     string
}

type ArtifactCatalog interface {
	LookupArtifact(context.Context, string) (ArtifactRecord, error)
}

// QueueRepository and ArtifactSink/Reader are optional extensions used by
// production player transfer. They keep the original repository contract
// usable by lightweight fixtures while enabling secure queue discovery and
// exact-byte CAS registration.
type QueueRepository interface {
	Next(context.Context, string, time.Time) (ScreenshotJob, bool, error)
}
type ArtifactSink interface {
	StoreScreenshotArtifact(context.Context, string, string, int64, io.Reader) error
}
type ArtifactReader interface {
	Open(context.Context, string) (io.ReadCloser, error)
	Stat(context.Context, string) (ArtifactStat, error)
}
type ArtifactStat struct {
	Digest string
	Size   int64
}
type ResultContent struct {
	Body      io.ReadCloser
	Size      int64
	MediaType string
	ETag      string
}
type CreateResult struct {
	Job      ScreenshotJob
	Replayed bool
}

type AuditEvent struct {
	ID      string    `json:"id"`
	JobID   string    `json:"jobId"`
	Action  string    `json:"action"`
	ActorID string    `json:"actorId,omitempty"`
	NodeID  string    `json:"nodeId"`
	At      time.Time `json:"at"`
	Detail  string    `json:"detail,omitempty"`
}
type CreateRequest struct {
	Job                  ScreenshotJob
	ActorID              string
	IdempotencyKey       string
	RequestDigest        string
	IdempotencyExpiresAt time.Time
	Audit                AuditEvent
}

// Repository is the persistence boundary for screenshot jobs. Create must
// atomically enforce (ActorID, IdempotencyKey, RequestDigest): a live matching
// record is replayed without another job or audit event, while a mismatched
// request returns ErrIdempotencyConflict. Claim and Complete must atomically
// enforce NodeID binding, expiry, and one-way state transitions, persisting the
// supplied audit event with each successful transition.
type Repository interface {
	Create(context.Context, CreateRequest) (ScreenshotJob, bool, error)
	Get(context.Context, string) (ScreenshotJob, bool, error)
	Claim(context.Context, string, string, time.Time, AuditEvent) (ScreenshotJob, error)
	Complete(context.Context, string, string, time.Time, Completion, AuditEvent) (ScreenshotJob, error)
	Expire(context.Context, string, time.Time, AuditEvent) (ScreenshotJob, bool, error)
}
type Authorizer interface {
	Authorize(context.Context, string, string, string) error
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() (string, error) }
type Config struct {
	DefaultTTL, MaxTTL, RetentionTTL, ArtifactRetentionTTL time.Duration
}
type Service struct {
	Repository Repository
	Authorizer Authorizer
	Artifacts  ArtifactCatalog
	Sink       ArtifactSink
	Reader     ArtifactReader
	Clock      Clock
	IDs        IDGenerator
	Config     Config
}

func (s Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) config() Config {
	c := s.Config
	if c.DefaultTTL <= 0 {
		c.DefaultTTL = 5 * time.Minute
	}
	if c.MaxTTL <= 0 {
		c.MaxTTL = 15 * time.Minute
	}
	if c.RetentionTTL <= 0 {
		c.RetentionTTL = 24 * time.Hour
	}
	if c.ArtifactRetentionTTL <= 0 {
		c.ArtifactRetentionTTL = c.RetentionTTL
	}
	return c
}
func (s Service) authorize(ctx context.Context, actor, action, resource string) error {
	if actor == "" {
		return ErrUnauthorized
	}
	if s.Authorizer == nil {
		return nil
	}
	if err := s.Authorizer.Authorize(ctx, actor, action, resource); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	return nil
}

func (s Service) Create(ctx context.Context, m Mutation, input CreateInput) (CreateResult, error) {
	if s.Repository == nil {
		return CreateResult{}, errors.New("preview repository is not configured")
	}
	if err := s.authorize(ctx, m.ActorID, "screenshot.create", input.NodeID); err != nil {
		return CreateResult{}, err
	}
	if strings.TrimSpace(input.NodeID) == "" {
		return CreateResult{}, fmt.Errorf("%w: nodeId is required", ErrInvalidArgument)
	}
	if input.Expected.Expected {
		if !validDigest(input.Expected.ReleaseDigest) || !validDigest(input.Expected.RevisionDigest) || input.Expected.EffectiveAt.IsZero() {
			return CreateResult{}, fmt.Errorf("%w: expected provenance requires valid release/revision digests and effective time", ErrInvalidArgument)
		}
	} else if input.Expected.ReleaseDigest != "" || input.Expected.RevisionDigest != "" || !input.Expected.EffectiveAt.IsZero() {
		return CreateResult{}, fmt.Errorf("%w: expected provenance must set expected=true", ErrInvalidArgument)
	}
	if strings.TrimSpace(m.IdempotencyKey) == "" {
		return CreateResult{}, fmt.Errorf("%w: idempotency key is required", ErrInvalidArgument)
	}
	c := s.config()
	ttl := input.TTL
	if ttl == 0 {
		ttl = c.DefaultTTL
	}
	if ttl <= 0 || ttl > c.MaxTTL {
		return CreateResult{}, fmt.Errorf("%w: ttl must be positive and no greater than %s", ErrInvalidArgument, c.MaxTTL)
	}
	requested := s.now()
	id, err := s.newID()
	if err != nil {
		return CreateResult{}, err
	}
	job := ScreenshotJob{ID: id, NodeID: input.NodeID, Expected: input.Expected, RequestedAt: requested, ExpiresAt: requested.Add(ttl), Status: StatusQueued, RetentionPolicy: "retain-until-expiry", RetentionUntil: requested.Add(ttl).Add(c.RetentionTTL), ArtifactRetentionUntil: requested.Add(ttl).Add(c.ArtifactRetentionTTL)}
	digest, err := requestDigest(input)
	if err != nil {
		return CreateResult{}, err
	}
	stored, replayed, err := s.Repository.Create(ctx, CreateRequest{Job: job, ActorID: m.ActorID, IdempotencyKey: m.IdempotencyKey, RequestDigest: digest, IdempotencyExpiresAt: job.RetentionUntil, Audit: AuditEvent{ID: auditID(id, "screenshot.requested", requested), JobID: id, Action: "screenshot.requested", ActorID: m.ActorID, NodeID: input.NodeID, At: requested}})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Job: stored, Replayed: replayed}, nil
}

func (s Service) Next(ctx context.Context, nodeID string) (ScreenshotJob, bool, error) {
	if s.Repository == nil || strings.TrimSpace(nodeID) == "" {
		return ScreenshotJob{}, false, ErrInvalidArgument
	}
	queue, ok := s.Repository.(QueueRepository)
	if !ok {
		return ScreenshotJob{}, false, ErrNotFound
	}
	return queue.Next(ctx, nodeID, s.now())
}

// CompleteUpload verifies the declared digest and exact byte count before
// handing bytes to the sink. The sink registers the immutable artifact, then
// Complete performs the atomic claimed-to-completed transition.
func (s Service) CompleteUpload(ctx context.Context, nodeID, id, digest, mediaType string, size int64, body io.Reader) (ScreenshotJob, error) {
	if size <= 0 || size > 16<<20 || mediaType != "image/png" || !validDigest(digest) || body == nil {
		return ScreenshotJob{}, fmt.Errorf("%w: invalid screenshot upload metadata", ErrInvalidArgument)
	}
	data := new(bytes.Buffer)
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(data, hash), io.LimitReader(body, size+1))
	if err != nil {
		return ScreenshotJob{}, fmt.Errorf("%w: read screenshot upload: %v", ErrInvalidArgument, err)
	}
	if n != size {
		return ScreenshotJob{}, fmt.Errorf("%w: screenshot byte count mismatch", ErrArtifactMismatch)
	}
	if extra, err := io.Copy(io.Discard, io.LimitReader(body, 1)); err != nil || extra != 0 {
		return ScreenshotJob{}, fmt.Errorf("%w: screenshot body exceeds declared size", ErrArtifactMismatch)
	}
	observed := hex.EncodeToString(hash.Sum(nil))
	if observed != digest {
		return ScreenshotJob{}, fmt.Errorf("%w: screenshot digest mismatch", ErrArtifactMismatch)
	}
	if s.Sink == nil {
		return ScreenshotJob{}, fmt.Errorf("%w: artifact sink is not configured", ErrArtifactUnavailable)
	}
	if err := s.Sink.StoreScreenshotArtifact(ctx, digest, mediaType, size, bytes.NewReader(data.Bytes())); err != nil {
		return ScreenshotJob{}, err
	}
	return s.Complete(ctx, nodeID, id, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: mediaType})
}

func (s Service) Content(ctx context.Context, actorID, id string) (ResultContent, error) {
	if err := s.authorize(ctx, actorID, "screenshot.read", id); err != nil {
		return ResultContent{}, err
	}
	job, err := s.Get(ctx, actorID, id)
	if err != nil {
		return ResultContent{}, err
	}
	if job.Status != StatusCompleted || job.ResultArtifactDigest == "" || !s.now().Before(job.RetentionUntil) {
		return ResultContent{}, ErrExpired
	}
	if s.Reader == nil {
		return ResultContent{}, ErrArtifactUnavailable
	}
	stat, err := s.Reader.Stat(ctx, job.ResultArtifactDigest)
	if err != nil || stat.Digest != job.ResultArtifactDigest {
		return ResultContent{}, ErrArtifactUnavailable
	}
	body, err := s.Reader.Open(ctx, job.ResultArtifactDigest)
	if err != nil {
		return ResultContent{}, ErrArtifactUnavailable
	}

	return ResultContent{Body: body, Size: stat.Size, MediaType: job.ResultMediaType, ETag: artifactETag(job.ResultArtifactDigest)}, nil
}
func artifactETag(digest string) string { return fmt.Sprintf("\"%s\"", digest) }

func (s Service) Get(ctx context.Context, actorID, id string) (ScreenshotJob, error) {
	if err := s.authorize(ctx, actorID, "screenshot.read", id); err != nil {
		return ScreenshotJob{}, err
	}
	if s.Repository == nil || strings.TrimSpace(id) == "" {
		return ScreenshotJob{}, ErrNotFound
	}
	job, ok, err := s.Repository.Get(ctx, id)
	if err != nil {
		return ScreenshotJob{}, err
	}
	if !ok {
		return ScreenshotJob{}, ErrNotFound
	}
	now := s.now()
	if !job.Status.terminal() && !now.Before(job.ExpiresAt) {
		job, _, err = s.Repository.Expire(ctx, id, now, AuditEvent{ID: auditID(id, "screenshot.expired", now), JobID: id, Action: "screenshot.expired", NodeID: job.NodeID, At: now, Detail: "job TTL elapsed"})
		if err != nil {
			return ScreenshotJob{}, err
		}
	}
	return job, nil
}

func (s Service) Claim(ctx context.Context, nodeID, id string) (ScreenshotJob, error) {
	if s.Repository == nil || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(id) == "" {
		return ScreenshotJob{}, ErrInvalidArgument
	}
	now := s.now()
	return s.Repository.Claim(ctx, nodeID, id, now, AuditEvent{ID: auditID(id, "screenshot.claimed", now), JobID: id, Action: "screenshot.claimed", NodeID: nodeID, At: now})
}

func (s Service) Complete(ctx context.Context, nodeID, id string, completion Completion) (ScreenshotJob, error) {
	if s.Repository == nil || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(id) == "" {
		return ScreenshotJob{}, ErrInvalidArgument
	}
	if err := validateCompletion(completion); err != nil {
		return ScreenshotJob{}, err
	}
	if completion.Status == StatusCompleted {
		if s.Artifacts == nil {
			return ScreenshotJob{}, fmt.Errorf("%w: artifact catalog is not configured", ErrArtifactUnavailable)
		}
		artifact, err := s.Artifacts.LookupArtifact(ctx, completion.ArtifactDigest)
		if err != nil {
			return ScreenshotJob{}, fmt.Errorf("%w: %v", ErrArtifactUnavailable, err)
		}
		if artifact.Digest != completion.ArtifactDigest || artifact.MediaType != completion.MediaType || !strings.EqualFold(artifact.State, "ready") {
			return ScreenshotJob{}, fmt.Errorf("%w: digest %s", ErrArtifactMismatch, completion.ArtifactDigest)
		}
	}
	now := s.now()
	action := "screenshot." + string(completion.Status)
	return s.Repository.Complete(ctx, nodeID, id, now, completion, AuditEvent{ID: auditID(id, action, now), JobID: id, Action: action, NodeID: nodeID, At: now, Detail: completion.ErrorReason})
}

func (s Service) CompleteUnsupported(ctx context.Context, nodeID, id, reason string) (ScreenshotJob, error) {
	return s.Complete(ctx, nodeID, id, Completion{Status: StatusUnsupported, ErrorReason: reason})
}
func validateCompletion(c Completion) error {
	if !c.Status.terminal() || c.Status == StatusExpired || c.Status == StatusQueued || c.Status == StatusClaimed {
		return fmt.Errorf("%w: completion status must be completed, failed, or unsupported", ErrInvalidArgument)
	}
	switch c.Status {
	case StatusCompleted:
		if !validDigest(c.ArtifactDigest) || strings.TrimSpace(c.MediaType) == "" {
			return fmt.Errorf("%w: completed result requires a sha256 artifact digest and media type", ErrInvalidArgument)
		}
	case StatusFailed, StatusUnsupported:
		if strings.TrimSpace(c.ErrorReason) == "" {
			return fmt.Errorf("%w: terminal error reason is required", ErrInvalidArgument)
		}
		if c.ArtifactDigest != "" || c.MediaType != "" {
			return fmt.Errorf("%w: failed or unsupported result cannot include an artifact", ErrInvalidArgument)
		}
	}
	return nil
}
func validDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func requestDigest(input CreateInput) (string, error) {
	b, err := json.Marshal(struct {
		NodeID   string
		Expected ExpectedPreview
		TTL      int64
	}{input.NodeID, input.Expected, int64(input.TTL)})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func auditID(jobID, action string, at time.Time) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + action + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}
func (s Service) newID() (string, error) {
	if s.IDs != nil {
		return s.IDs.NewID()
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:])), nil
}

// test fixture. SQL adapters can implement Repository with the same atomic
// transition and audit guarantees.
type MemoryRepository struct {
	mu          sync.Mutex
	jobs        map[string]ScreenshotJob
	idempotency map[string]memoryIdempotency
	Audit       []AuditEvent
}
type memoryIdempotency struct {
	Digest    string
	JobID     string
	ExpiresAt time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{jobs: map[string]ScreenshotJob{}, idempotency: map[string]memoryIdempotency{}}
}
func cloneJob(j ScreenshotJob) ScreenshotJob {
	if j.ClaimedAt != nil {
		v := *j.ClaimedAt
		j.ClaimedAt = &v
	}
	if j.CompletionAt != nil {
		v := *j.CompletionAt
		j.CompletionAt = &v
	}
	return j
}

func (r *MemoryRepository) Next(_ context.Context, nodeID string, now time.Time) (ScreenshotJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var selected ScreenshotJob
	found := false
	for _, job := range r.jobs {
		if job.NodeID != nodeID || job.Status != StatusQueued || !now.Before(job.ExpiresAt) {
			continue
		}
		if !found || job.RequestedAt.Before(selected.RequestedAt) || (job.RequestedAt.Equal(selected.RequestedAt) && job.ID < selected.ID) {
			selected, found = job, true
		}
	}
	return cloneJob(selected), found, nil
}
func (r *MemoryRepository) Create(_ context.Context, req CreateRequest) (ScreenshotJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := req.ActorID + "\x00" + req.IdempotencyKey
	if old, ok := r.idempotency[key]; ok && old.ExpiresAt.After(req.Job.RequestedAt) {
		if old.Digest != req.RequestDigest {
			return ScreenshotJob{}, false, ErrIdempotencyConflict
		}
		return cloneJob(r.jobs[old.JobID]), true, nil
	}
	if _, ok := r.jobs[req.Job.ID]; ok {
		return ScreenshotJob{}, false, fmt.Errorf("%w: duplicate job id", ErrConflict)
	}
	r.jobs[req.Job.ID] = cloneJob(req.Job)
	r.idempotency[key] = memoryIdempotency{req.RequestDigest, req.Job.ID, req.IdempotencyExpiresAt}
	r.Audit = append(r.Audit, req.Audit)
	return cloneJob(req.Job), false, nil
}
func (r *MemoryRepository) Get(_ context.Context, id string) (ScreenshotJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return cloneJob(j), ok, nil
}
func (r *MemoryRepository) Expire(_ context.Context, id string, now time.Time, a AuditEvent) (ScreenshotJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return ScreenshotJob{}, false, nil
	}
	if j.Status.terminal() {
		return cloneJob(j), false, nil
	}
	if now.Before(j.ExpiresAt) {
		return cloneJob(j), false, nil
	}
	j.Status = StatusExpired
	j.ErrorReason = "job TTL elapsed"
	j.CompletionAt = timePtr(now)
	r.jobs[id] = j
	r.Audit = append(r.Audit, a)
	return cloneJob(j), true, nil
}
func (r *MemoryRepository) Claim(_ context.Context, nodeID, id string, now time.Time, a AuditEvent) (ScreenshotJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return ScreenshotJob{}, ErrNotFound
	}
	if j.NodeID != nodeID {
		return ScreenshotJob{}, ErrForbidden
	}
	if j.Status.terminal() {
		if j.Status == StatusExpired {
			return ScreenshotJob{}, ErrExpired
		}
		return ScreenshotJob{}, ErrTerminal
	}
	if !now.Before(j.ExpiresAt) {
		j.Status = StatusExpired
		j.ErrorReason = "job TTL elapsed"
		j.CompletionAt = timePtr(now)
		r.jobs[id] = j
		r.Audit = append(r.Audit, AuditEvent{ID: a.ID, JobID: id, Action: "screenshot.expired", NodeID: nodeID, At: now, Detail: j.ErrorReason})
		return ScreenshotJob{}, ErrExpired
	}
	if j.Status != StatusQueued {
		return ScreenshotJob{}, ErrConflict
	}
	j.Status = StatusClaimed
	j.ClaimedAt = timePtr(now)
	r.jobs[id] = j
	r.Audit = append(r.Audit, a)
	return cloneJob(j), nil
}
func (r *MemoryRepository) Complete(_ context.Context, nodeID, id string, now time.Time, c Completion, a AuditEvent) (ScreenshotJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return ScreenshotJob{}, ErrNotFound
	}
	if j.NodeID != nodeID {
		return ScreenshotJob{}, ErrForbidden
	}
	if j.Status.terminal() {
		if j.Status == StatusExpired {
			return ScreenshotJob{}, ErrExpired
		}
		return ScreenshotJob{}, ErrTerminal
	}
	if j.Status != StatusClaimed {
		return ScreenshotJob{}, ErrNotClaimed
	}
	if !now.Before(j.ExpiresAt) {
		j.Status = StatusExpired
		j.ErrorReason = "job TTL elapsed"
		j.CompletionAt = timePtr(now)
		r.jobs[id] = j
		r.Audit = append(r.Audit, AuditEvent{ID: a.ID, JobID: id, Action: "screenshot.expired", NodeID: nodeID, At: now, Detail: j.ErrorReason})
		return ScreenshotJob{}, ErrExpired
	}
	j.Status = c.Status
	j.CompletionAt = timePtr(now)
	j.ResultArtifactDigest = c.ArtifactDigest
	j.ResultMediaType = c.MediaType
	j.ErrorReason = c.ErrorReason
	r.jobs[id] = j
	r.Audit = append(r.Audit, a)
	return cloneJob(j), nil
}
func timePtr(v time.Time) *time.Time { return &v }
