package webnode

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

type testIDs struct{ n int }

func (i *testIDs) NewID() (string, error)    { i.n++; return "id-" + string(rune('0'+i.n)), nil }
func (i *testIDs) NewToken() (string, error) { i.n++; return "token-" + string(rune('0'+i.n)), nil }
func newTestService() (*Service, *testClock) {
	clock := &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return &Service{Repo: NewMemoryRepository(), Events: NewMemoryEventStream(), Config: Config{Clock: clock, IDs: &testIDs{}, ClaimTTL: time.Minute, CredentialTTL: time.Hour, RenewWindow: 20 * time.Minute}}, clock
}

func TestClaimIsSingleUseScopedAndRenewable(t *testing.T) {
	ctx := context.Background()
	svc, clock := newTestService()
	claim, err := svc.CreateClaim(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateClaim(ctx, "node-a"); !errors.Is(err, ErrActiveClaim) {
		t.Fatalf("second active claim error = %v", err)
	}
	if _, err := svc.ConsumeClaim(ctx, "node-b", claim.Code); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("cross-node consume error = %v", err)
	}
	issued, err := svc.ConsumeClaim(ctx, "node-a", claim.Code)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.Credential.Hash == issued.Token {
		t.Fatal("credential token must be returned once and stored hashed")
	}
	if _, err := svc.ConsumeClaim(ctx, "node-a", claim.Code); !errors.Is(err, ErrClaimConsumed) {
		t.Fatalf("replay error = %v", err)
	}
	if _, err := svc.Authorize(ctx, "node-b", issued.Token); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("cross-node credential error = %v", err)
	}
	// Renewal starts in the final configured window and rotates the secret.
	clock.now = issued.Credential.ExpiresAt.Add(-10 * time.Minute)
	renewed, err := svc.Renew(ctx, "node-a", issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Token == issued.Token {
		t.Fatal("renewal must rotate token")
	}
	if _, err := svc.Authorize(ctx, "node-a", issued.Token); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("old token after renewal = %v", err)
	}
	if err := svc.Revoke(ctx, "node-a", renewed.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authorize(ctx, "node-a", renewed.Token); !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("revoked token = %v", err)
	}
}

func TestClaimAndCredentialExpiry(t *testing.T) {
	ctx := context.Background()
	svc, clock := newTestService()
	claim, err := svc.CreateClaim(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	clock.now = claim.Claim.ExpiresAt
	if _, err := svc.ConsumeClaim(ctx, "node-a", claim.Code); !errors.Is(err, ErrClaimExpired) {
		t.Fatalf("expired claim = %v", err)
	}
	claim, err = svc.CreateClaim(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.ConsumeClaim(ctx, "node-a", claim.Code)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = issued.Credential.ExpiresAt
	if _, err := svc.Authorize(ctx, "node-a", issued.Token); !errors.Is(err, ErrCredentialExpired) {
		t.Fatalf("expired credential = %v", err)
	}
}

func TestAssignmentIsolationAndObservedAcknowledgment(t *testing.T) {
	ctx, svc := context.Background(), func() *Service { s, _ := newTestService(); return s }()
	ca, err := svc.CreateClaim(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ConsumeClaim(ctx, "node-a", ca.Code)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := svc.CreateClaim(ctx, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.ConsumeClaim(ctx, "node-b", cb.Code)
	if err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{ID: "assignment-a", NodeID: "node-a", Sequence: 1, EventEpoch: "epoch-1", MediaURL: "https://example.test/a.png", MediaType: "image/png"}
	stream, err := svc.Subscribe(ctx, "node-a", a.Token, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssignment(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream:
		if event.Assignment.ID != assignment.ID {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("assignment was not delivered")
	}
	if _, err := svc.Subscribe(ctx, "node-b", a.Token, 0); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("cross-node subscribe = %v", err)
	}
	if err := svc.Acknowledge(ctx, "node-b", b.Token, PlaybackReport{AssignmentID: assignment.ID, EventEpoch: "epoch-1", Sequence: 1, State: PlaybackPlaying}); !errors.Is(err, ErrAssignmentIsolation) {
		t.Fatalf("cross-node ack = %v", err)
	}
	if err := svc.Acknowledge(ctx, "node-a", a.Token, PlaybackReport{AssignmentID: assignment.ID, EventEpoch: "epoch-1", Sequence: 1, State: PlaybackPlaying}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := svc.Repo.LatestReport(ctx, "node-a")
	if err != nil || !ok {
		t.Fatalf("latest report = %#v, %v, %v", got, ok, err)
	}
	if got.State != PlaybackPlaying || got.ObservedAt.IsZero() {
		t.Fatalf("report was not observed: %#v", got)
	}
}
