package assignment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	canonical "github.com/jmill/cable/internal/release"
)

type assignmentRepositoryFixture struct {
	tx *assignmentTransactionFixture
}

func (r assignmentRepositoryFixture) Transact(_ context.Context, _ string, fn func(Transaction) error) error {
	return fn(r.tx)
}

type assignmentTransactionFixture struct {
	generation  int64
	current     Assignment
	hasCurrent  bool
	releases    map[string]canonical.EnvelopeV1
	history     []Assignment
	observation *Observation
	idempotency map[string]IdempotencyRecord
	nextID      int
	authorized  []Authorization
}

func newAssignmentTransactionFixture(envelopes ...canonical.EnvelopeV1) *assignmentTransactionFixture {
	releases := make(map[string]canonical.EnvelopeV1, len(envelopes))
	for _, envelope := range envelopes {
		releases[envelope.ID] = envelope
	}
	return &assignmentTransactionFixture{
		releases:    releases,
		idempotency: make(map[string]IdempotencyRecord),
	}
}

func (t *assignmentTransactionFixture) CurrentGeneration() (int64, error) {
	return t.generation, nil
}

func (t *assignmentTransactionFixture) CurrentAssignment() (Assignment, bool, error) {
	return t.current, t.hasCurrent, nil
}

func (t *assignmentTransactionFixture) ReleaseByID(id string) (canonical.EnvelopeV1, bool, error) {
	envelope, found := t.releases[id]
	return envelope, found, nil
}

func (t *assignmentTransactionFixture) AssignedRelease(digest string, atMostGeneration int64) (Assignment, bool, error) {
	for index := len(t.history) - 1; index >= 0; index-- {
		assigned := t.history[index]
		if assigned.ReleaseDigest == digest && assigned.DesiredGeneration <= atMostGeneration {
			return assigned, true, nil
		}
	}
	return Assignment{}, false, nil
}

func (t *assignmentTransactionFixture) LatestObservation() (Observation, bool, error) {
	if t.observation == nil {
		return Observation{}, false, nil
	}
	return *t.observation, true, nil
}

func (t *assignmentTransactionFixture) Idempotency(actorID, key string) (IdempotencyRecord, bool, error) {
	record, found := t.idempotency[actorID+"\x00"+key]
	return record, found, nil
}

func (t *assignmentTransactionFixture) NewAssignmentID() (string, error) {
	t.nextID++
	return fmt.Sprintf("assignment-%d", t.nextID), nil
}

func (t *assignmentTransactionFixture) InsertAssignment(assigned Assignment) error {
	if assigned.DesiredGeneration != t.generation+1 {
		return errors.New("non-monotonic fixture insertion")
	}
	t.generation = assigned.DesiredGeneration
	t.current = assigned
	t.hasCurrent = true
	t.history = append(t.history, assigned)
	return nil
}

func (t *assignmentTransactionFixture) ReplaceAuthorizations(authorizations []Authorization) error {
	t.authorized = append(t.authorized[:0], authorizations...)
	return nil
}

func (t *assignmentTransactionFixture) PutIdempotency(actorID, key string, record IdempotencyRecord) error {
	t.idempotency[actorID+"\x00"+key] = record
	return nil
}

func TestPutReplayGenerationAndIdempotencyConflict(t *testing.T) {
	release := releaseFixture(t, 1, DeliveryStream, DeliveryCacheThenPlay)
	tx := newAssignmentTransactionFixture(release)
	service := Service{Assignments: assignmentRepositoryFixture{tx: tx}}
	mutation := Mutation{ActorID: "actor-one", IdempotencyKey: "request-one"}
	input := putInput(release, DeliveryStream, 0)

	created, err := service.Put(context.Background(), "node-one", mutation, input)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Assignment.DesiredGeneration != 1 || len(tx.history) != 1 {
		t.Fatalf("unexpected first assignment: %#v", created)
	}
	replayed, err := service.Put(context.Background(), "node-one", mutation, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Created || replayed.Assignment != created.Assignment || len(tx.history) != 1 {
		t.Fatalf("replay created another generation: %#v", replayed)
	}

	changed := input
	changed.DeliveryMode = DeliveryCacheThenPlay
	if _, err := service.Put(context.Background(), "node-one", mutation, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v, want idempotency conflict", err)
	}
	if _, err := service.Put(context.Background(), "node-one", Mutation{ActorID: "actor-one", IdempotencyKey: "request-two"}, input); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("got %v, want stale expected generation", err)
	}
}

func TestPutRequiresExactReleaseBindingAndAllowedDeliveryMode(t *testing.T) {
	one := releaseFixture(t, 1, DeliveryStream)
	two := releaseFixture(t, 2, DeliveryStream)
	tests := []struct {
		name  string
		input PutInput
		want  error
	}{
		{name: "digest belongs to another release", input: PutInput{ReleaseID: one.ID, ReleaseDigest: two.Digest, DeliveryMode: DeliveryStream}, want: ErrReleaseMismatch},
		{name: "release ID is absent", input: PutInput{ReleaseID: "release-missing", ReleaseDigest: one.Digest, DeliveryMode: DeliveryStream}, want: ErrReleaseNotFound},
		{name: "known mode not allowed by release", input: PutInput{ReleaseID: one.ID, ReleaseDigest: one.Digest, DeliveryMode: DeliveryCacheThenPlay}, want: ErrDeliveryMode},
		{name: "unknown mode", input: PutInput{ReleaseID: one.ID, ReleaseDigest: one.Digest, DeliveryMode: "download"}, want: ErrDeliveryMode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := newAssignmentTransactionFixture(one, two)
			service := Service{Assignments: assignmentRepositoryFixture{tx: tx}}
			_, err := service.Put(context.Background(), "node-one", Mutation{ActorID: "actor-one", IdempotencyKey: test.name}, test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			if len(tx.history) != 0 {
				t.Fatal("invalid assignment was added to history")
			}
		})
	}
}

func TestAuthorizationUsesOlderAssignedObservationButRejectsNewerAndArbitraryDigests(t *testing.T) {
	one := releaseFixture(t, 1, DeliveryStream)
	two := releaseFixture(t, 2, DeliveryStream)
	tx := newAssignmentTransactionFixture(one, two)
	service := Service{Assignments: assignmentRepositoryFixture{tx: tx}}

	first, err := service.Put(context.Background(), "node-one", Mutation{ActorID: "actor-one", IdempotencyKey: "one"}, putInput(one, DeliveryStream, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Put(context.Background(), "node-one", Mutation{ActorID: "actor-one", IdempotencyKey: "two"}, putInput(two, DeliveryStream, 1))
	if err != nil {
		t.Fatal(err)
	}
	arbitrary := strings.Repeat("f", 64)
	tx.observation = &Observation{
		EventEpoch:                 "epoch-one",
		Sequence:                   9,
		DesiredGenerationSeen:      1,
		ObservedGeneration:         1,
		CandidateReleaseDigest:     second.Assignment.ReleaseDigest,
		RenderingReleaseDigest:     first.Assignment.ReleaseDigest,
		HealthyReleaseDigest:       arbitrary,
		LastKnownGoodReleaseDigest: first.Assignment.ReleaseDigest,
	}
	if err := service.RecomputeAuthorizations(context.Background(), "node-one"); err != nil {
		t.Fatal(err)
	}

	want := []Authorization{
		authorization(RoleDesired, second.Assignment),
		authorization(RoleRendering, first.Assignment),
		authorization(RoleLKG, first.Assignment),
	}
	if !equalAuthorizations(tx.authorized, want) {
		t.Fatalf("unexpected authorization set: %#v", tx.authorized)
	}
}

func TestAuthorizationRemainsBoundedAcrossRepeatedSupersession(t *testing.T) {
	const releases = 12
	envelopes := make([]canonical.EnvelopeV1, releases)
	for index := range envelopes {
		envelopes[index] = releaseFixture(t, index+1, DeliveryStream)
	}
	tx := newAssignmentTransactionFixture(envelopes...)
	service := Service{Assignments: assignmentRepositoryFixture{tx: tx}}

	for index, envelope := range envelopes {
		generation := int64(index)
		if index != 0 {
			previous := tx.history[index-1]
			tx.observation = &Observation{
				EventEpoch:                 "epoch-one",
				Sequence:                   int64(index),
				DesiredGenerationSeen:      generation,
				ObservedGeneration:         generation,
				CandidateReleaseDigest:     previous.ReleaseDigest,
				RenderingReleaseDigest:     previous.ReleaseDigest,
				HealthyReleaseDigest:       previous.ReleaseDigest,
				LastKnownGoodReleaseDigest: previous.ReleaseDigest,
			}
		}
		result, err := service.Put(
			context.Background(),
			"node-one",
			Mutation{ActorID: "actor-one", IdempotencyKey: fmt.Sprintf("request-%d", index)},
			putInput(envelope, DeliveryStream, generation),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Assignment.DesiredGeneration != generation+1 {
			t.Fatalf("generation jumped: %#v", result.Assignment)
		}
		if len(tx.authorized) > 5 {
			t.Fatalf("authorization grew to %d entries: %#v", len(tx.authorized), tx.authorized)
		}
		if tx.authorized[0] != authorization(RoleDesired, result.Assignment) {
			t.Fatalf("desired authorization is not current: %#v", tx.authorized)
		}
	}
	if len(tx.history) != releases || len(tx.authorized) != 5 {
		t.Fatalf("unexpected final history/authorization sizes: %d/%d", len(tx.history), len(tx.authorized))
	}
}

func putInput(envelope canonical.EnvelopeV1, deliveryMode string, expectedGeneration int64) PutInput {
	return PutInput{
		ReleaseID:          envelope.ID,
		ReleaseDigest:      envelope.Digest,
		DeliveryMode:       deliveryMode,
		ExpectedGeneration: expectedGeneration,
	}
}

func releaseFixture(t *testing.T, number int, deliveryModes ...string) canonical.EnvelopeV1 {
	t.Helper()
	payload := canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: canonical.ProtocolV1,
		Playback: canonical.PlaybackPayload{
			AssetID:             fmt.Sprintf("asset-%d", number),
			Fit:                 "fit",
			Loop:                true,
			Volume:              80,
			AllowedDeliveryMode: deliveryModes,
		},
		Artifacts: []canonical.Artifact{{
			Digest:    fmt.Sprintf("%064x", number),
			Size:      100,
			MediaType: "video/mp4",
		}},
		RequiredFeatures: []string{"mpv"},
	}
	digest, _, err := canonical.Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	return canonical.EnvelopeV1{
		ID:        fmt.Sprintf("release-%d", number),
		Digest:    digest,
		KeyID:     "publisher-one",
		Signature: "fixture-signature",
		Payload:   payload,
	}
}

func equalAuthorizations(left, right []Authorization) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
