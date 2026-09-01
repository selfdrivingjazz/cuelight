package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/cable"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fixedIDs struct{ id string }

func (i fixedIDs) NewID() (string, error) { return i.id, nil }

var testTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func TestDeriveExpectedPreviewUsesAuthoritativeRevisionAndDeterministicSelection(t *testing.T) {
	revision := cable.CableRevision{
		Digest: "revision-digest",
		Window: cable.RevisionWindow{StartsAt: testTime.Add(-time.Hour), EndsAt: testTime.Add(time.Hour)},
		Channels: []cable.RevisionChannel{
			{ID: "standby", Number: 1, Enabled: true, Standby: true},
			{ID: "later", Number: 5, Enabled: true, Blocks: []cable.RevisionBlock{{ID: "later-block", StartsAt: testTime.Add(-time.Hour), EndsAt: testTime.Add(time.Hour)}}},
			{ID: "primary", Number: 2, Enabled: true, Blocks: []cable.RevisionBlock{
				{ID: "b2", StartsAt: testTime.Add(-time.Hour), EndsAt: testTime.Add(time.Hour), Items: []cable.RevisionProgram{{ID: "item-later", StartsAt: testTime.Add(-time.Minute), EndsAt: testTime.Add(time.Hour), ItemID: "item-authoritative"}}},
			}},
		},
	}
	got, err := DeriveExpectedPreview(ExpectedPreviewInput{HealthyReleaseDigest: "healthy-release", Revision: revision, At: testTime})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Expected || got.ReleaseDigest != "healthy-release" || got.RevisionDigest != "revision-digest" || got.ChannelID != "primary" || got.BlockID != "b2" || got.ItemID != "item-authoritative" || !got.EffectiveAt.Equal(testTime) {
		t.Fatalf("unexpected expected preview: %#v", got)
	}
	boundary, err := DeriveExpectedPreview(ExpectedPreviewInput{HealthyReleaseDigest: "healthy-release", Revision: revision, At: testTime.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if boundary.BlockID != "" || boundary.ItemID != "" {
		t.Fatalf("half-open end selected content: %#v", boundary)
	}
}

func TestDeriveExpectedPreviewRequiresObservedHealthyRelease(t *testing.T) {
	_, err := DeriveExpectedPreview(ExpectedPreviewInput{Revision: cable.CableRevision{Digest: "revision"}, At: testTime})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error=%v, want invalid argument", err)
	}
}

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
