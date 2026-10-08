package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fixedIDs struct{ id string }

func (i fixedIDs) NewID() (string, error) { return i.id, nil }

var testTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func testExpected() ExpectedPreview {
	return ExpectedPreview{Expected: true, ReleaseDigest: strings.Repeat("a", 64), RevisionDigest: strings.Repeat("b", 64), EffectiveAt: testTime}
}
func testService(clock *fixedClock) (*Service, *MemoryRepository) {
	repository := NewMemoryRepository()
	return &Service{Repository: repository, Clock: clock, IDs: fixedIDs{id: "job-1"}, Config: Config{DefaultTTL: time.Minute, MaxTTL: 10 * time.Minute, RetentionTTL: time.Hour}}, repository
}

func TestScreenshotJobEnforcesIdempotencyBindingAndUnsupportedTerminalResult(t *testing.T) {
	clock := &fixedClock{now: testTime}
	service, repository := testService(clock)
	input := CreateInput{NodeID: "node-a", Expected: testExpected()}
	first, err := service.Create(context.Background(), Mutation{ActorID: "operator", IdempotencyKey: "request-1"}, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(context.Background(), Mutation{ActorID: "operator", IdempotencyKey: "request-1"}, input)
	if err != nil || !replay.Replayed || replay.Job.ID != first.Job.ID {
		t.Fatalf("replay=(%#v,%v)", replay, err)
	}
	_, err = service.Create(context.Background(), Mutation{ActorID: "operator", IdempotencyKey: "request-1"}, CreateInput{NodeID: "node-b", Expected: testExpected()})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different replay error=%v", err)
	}
	if _, err := service.Claim(context.Background(), "node-b", first.Job.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-node claim=%v", err)
	}
	if _, err := service.Claim(context.Background(), "node-a", first.Job.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteUnsupported(context.Background(), "node-a", first.Job.ID, "renderer capture is unsupported")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusUnsupported || completed.CompletionAt == nil || completed.ErrorReason == "" {
		t.Fatalf("unsupported result=%#v", completed)
	}
	if len(repository.Audit) != 3 || repository.Audit[0].Action != "screenshot.requested" || repository.Audit[2].Action != "screenshot.unsupported" {
		t.Fatalf("audit=%#v", repository.Audit)
	}
	if _, err := service.CompleteUnsupported(context.Background(), "node-a", first.Job.ID, "retry"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal replay=%v", err)
	}
}

func TestScreenshotJobExpiresAndCannotCompleteAfterTTL(t *testing.T) {
	clock := &fixedClock{now: testTime}
	service, _ := testService(clock)
	created, err := service.Create(context.Background(), Mutation{ActorID: "operator", IdempotencyKey: "request"}, CreateInput{NodeID: "node-a", Expected: testExpected(), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = testTime.Add(2 * time.Minute)
	if _, err := service.Claim(context.Background(), "node-a", created.Job.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired claim=%v", err)
	}
	job, err := service.Get(context.Background(), "operator", created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusExpired || job.CompletionAt == nil {
		t.Fatalf("expired job=%#v", job)
	}
}

type artifactCatalogStub struct {
	record ArtifactRecord
	err    error
}

func (c artifactCatalogStub) LookupArtifact(context.Context, string) (ArtifactRecord, error) {
	return c.record, c.err
}

func TestScreenshotCompletionValidatesCatalogMetadata(t *testing.T) {
	clock := &fixedClock{now: testTime}
	service, _ := testService(clock)
	created, err := service.Create(context.Background(), Mutation{ActorID: "operator", IdempotencyKey: "catalog"}, CreateInput{NodeID: "node-a", Expected: testExpected()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), "node-a", created.Job.ID); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("c", 64)
	_, err = service.Complete(context.Background(), "node-a", created.Job.ID, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: "image/png"})
	if !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("without catalog error=%v, want unavailable", err)
	}
	service.Artifacts = artifactCatalogStub{record: ArtifactRecord{Digest: digest, MediaType: "image/jpeg", State: "ready"}}
	_, err = service.Complete(context.Background(), "node-a", created.Job.ID, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: "image/png"})
	if !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("mismatched catalog error=%v, want mismatch", err)
	}
	service.Artifacts = artifactCatalogStub{record: ArtifactRecord{Digest: digest, MediaType: "image/png", State: "missing"}}
	_, err = service.Complete(context.Background(), "node-a", created.Job.ID, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: "image/png"})
	if !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("missing artifact error=%v, want mismatch", err)
	}
	service.Artifacts = artifactCatalogStub{record: ArtifactRecord{Digest: digest, MediaType: "image/png", State: "ready"}}
	_, err = service.Complete(context.Background(), "node-b", created.Job.ID, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: "image/png"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-node completion error=%v, want forbidden", err)
	}
	completed, err := service.Complete(context.Background(), "node-a", created.Job.ID, Completion{Status: StatusCompleted, ArtifactDigest: digest, MediaType: "image/png"})
	if err != nil || completed.Status != StatusCompleted || completed.ResultArtifactDigest != digest {
		t.Fatalf("completed=(%#v,%v)", completed, err)
	}
}
