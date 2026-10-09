package assignment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	canonical "github.com/jmilldotdev/cuelight/internal/release"
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

// publisherFixture resolves media IDs to prepared envelopes.
type publisherFixture struct {
	byMedia map[string]canonical.EnvelopeV1
	keys    []string
}

func (p *publisherFixture) PublishTarget(_ context.Context, _, key, mediaID, playlistID string) (canonical.EnvelopeV1, error) {
	p.keys = append(p.keys, key)
	envelope, ok := p.byMedia[mediaID+playlistID]
	if !ok {
		return canonical.EnvelopeV1{}, ErrTargetNotFound
	}
	return envelope, nil
}

func serviceFixture(tx *assignmentTransactionFixture, envelopes ...canonical.EnvelopeV1) Service {
	publisher := &publisherFixture{byMedia: map[string]canonical.EnvelopeV1{}}
	for _, envelope := range envelopes {
		publisher.byMedia["media-"+envelope.ID] = envelope
	}
	return Service{Assignments: assignmentRepositoryFixture{tx: tx}, Releases: publisher}
}

func generation(value int64) *int64 { return &value }

func TestAssignReplayGenerationAndIdempotencyConflict(t *testing.T) {
	release := releaseFixture(t, 1, DeliveryStream, DeliveryCacheThenPlay)
	tx := newAssignmentTransactionFixture(release)
	service := serviceFixture(tx, release)
	mutation := Mutation{ActorID: "actor-one", IdempotencyKey: "request-one"}
	input := target(release, DeliveryStream, 0)

	created, err := service.AssignTarget(context.Background(), mutation, "node-one", input)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Assignment.DesiredGeneration != 1 || len(tx.history) != 1 {
		t.Fatalf("unexpected first assignment: %#v", created)
	}
	replayed, err := service.AssignTarget(context.Background(), mutation, "node-one", input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Created || replayed.Assignment != created.Assignment || len(tx.history) != 1 {
		t.Fatalf("replay created another generation: %#v", replayed)
	}

	changed := input
	changed.DeliveryMode = DeliveryCacheThenPlay
	if _, err := service.AssignTarget(context.Background(), mutation, "node-one", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v, want idempotency conflict", err)
	}
	if _, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "request-two"}, "node-one", input); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("got %v, want stale expected generation", err)
	}
	current := input
	current.ExpectedGeneration = nil
	next, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "request-three"}, "node-one", current)
	if err != nil || next.Assignment.ExpectedGeneration != 1 || next.Assignment.DesiredGeneration != 2 {
		t.Fatalf("nil expected generation must assign over the current one: %#v %v", next, err)
	}
}

func TestAssignDefaultsDeliveryModeFromRelease(t *testing.T) {
	both := releaseFixture(t, 1, DeliveryStream, DeliveryCacheThenPlay)
	streamOnly := releaseFixture(t, 2, DeliveryStream)
	tx := newAssignmentTransactionFixture(both, streamOnly)
	service := serviceFixture(tx, both, streamOnly)
	cached, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "one"}, "node-one", Target{MediaID: "media-" + both.ID})
	if err != nil || cached.Assignment.DeliveryMode != DeliveryCacheThenPlay || cached.Assignment.MediaID != "media-"+both.ID {
		t.Fatalf("got %#v %v, want cache_then_play for a cacheable release", cached, err)
	}
	streamed, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "two"}, "node-one", Target{MediaID: "media-" + streamOnly.ID})
	if err != nil || streamed.Assignment.DeliveryMode != DeliveryStream {
		t.Fatalf("got %#v %v, want stream for a stream-only release", streamed, err)
	}
}

func TestAssignRequiresExactlyOneTarget(t *testing.T) {
	release := releaseFixture(t, 1, DeliveryStream)
	tx := newAssignmentTransactionFixture(release)
	service := serviceFixture(tx, release)
	for name, input := range map[string]Target{
		"none": {},
		"both": {MediaID: "media-" + release.ID, PlaylistID: "playlist-one"},
	} {
		if _, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: name}, "node-one", input); !errors.Is(err, ErrInvalidAssignment) {
			t.Fatalf("%s: got %v, want invalid assignment", name, err)
		}
	}
	if _, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "missing"}, "node-one", Target{MediaID: "media-missing"}); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("got %v, want target not found", err)
	}
	if len(tx.history) != 0 {
		t.Fatal("invalid assignment was added to history")
	}
}

func TestAssignRequiresExactReleaseBindingAndAllowedDeliveryMode(t *testing.T) {
	one := releaseFixture(t, 1, DeliveryStream)
	two := releaseFixture(t, 2, DeliveryStream)
	mismatched := one
	mismatched.Digest = two.Digest
	absent := one
	absent.ID = "release-missing"
	tests := []struct {
		name      string
		published canonical.EnvelopeV1
		mode      string
		want      error
	}{
		{name: "publisher digest belongs to another release", published: mismatched, mode: DeliveryStream, want: ErrReleaseMismatch},
		{name: "published release is absent", published: absent, mode: DeliveryStream, want: ErrReleaseNotFound},
		{name: "known mode not allowed by release", published: one, mode: DeliveryCacheThenPlay, want: ErrDeliveryMode},
		{name: "unknown mode", published: one, mode: "download", want: ErrDeliveryMode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := newAssignmentTransactionFixture(one, two)
			service := Service{Assignments: assignmentRepositoryFixture{tx: tx}, Releases: &publisherFixture{byMedia: map[string]canonical.EnvelopeV1{"media-one": test.published}}}
			_, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: test.name}, "node-one", Target{MediaID: "media-one", DeliveryMode: test.mode, ExpectedGeneration: generation(0)})
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
	service := serviceFixture(tx, one, two)

	first, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "one"}, "node-one", target(one, DeliveryStream, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "two"}, "node-one", target(two, DeliveryStream, 1))
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
	service := serviceFixture(tx, envelopes...)

	for index, envelope := range envelopes {
		current := int64(index)
		if index != 0 {
			previous := tx.history[index-1]
			tx.observation = &Observation{
				EventEpoch:                 "epoch-one",
				Sequence:                   int64(index),
				DesiredGenerationSeen:      current,
				ObservedGeneration:         current,
				CandidateReleaseDigest:     previous.ReleaseDigest,
				RenderingReleaseDigest:     previous.ReleaseDigest,
				HealthyReleaseDigest:       previous.ReleaseDigest,
				LastKnownGoodReleaseDigest: previous.ReleaseDigest,
			}
		}
		result, err := service.AssignTarget(
			context.Background(),
			Mutation{ActorID: "actor-one", IdempotencyKey: fmt.Sprintf("request-%d", index)},
			"node-one",
			target(envelope, DeliveryStream, current),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Assignment.DesiredGeneration != current+1 {
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

func target(envelope canonical.EnvelopeV1, deliveryMode string, expectedGeneration int64) Target {
	return Target{
		MediaID:            "media-" + envelope.ID,
		DeliveryMode:       deliveryMode,
		ExpectedGeneration: generation(expectedGeneration),
	}
}

func releaseFixture(t *testing.T, number int, deliveryModes ...string) canonical.EnvelopeV1 {
	t.Helper()
	payload := canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: canonical.ProtocolV1,
		Presentation: canonical.Presentation{
			Kind: canonical.PresentationMedia,
			Media: &canonical.MediaPresentation{Playback: canonical.PlaybackPayload{
				AssetID:             fmt.Sprintf("asset-%d", number),
				Fit:                 "fit",
				Loop:                true,
				Volume:              80,
				AllowedDeliveryMode: deliveryModes,
			}},
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

// Each step reassigns the same node, inheriting from the previous step.
func TestAssignInheritsOmittedRemoteSettings(t *testing.T) {
	release := releaseFixture(t, 1, DeliveryStream)
	tx := newAssignmentTransactionFixture(release)
	service := serviceFixture(tx, release)
	on, off := true, false
	for index, step := range []struct {
		name                string
		remoteInput, showQR *bool
		wantRemote, wantQR  bool
		wantErr             bool
	}{
		{name: "no current assignment"},
		{name: "explicit QR needs remote", showQR: &on, wantErr: true},
		{name: "explicit on", remoteInput: &on, showQR: &on, wantRemote: true, wantQR: true},
		{name: "omitted keeps both", wantRemote: true, wantQR: true},
		{name: "explicit QR with explicit remote off", remoteInput: &off, showQR: &on, wantErr: true},
		{name: "remote off drops inherited QR", remoteInput: &off},
		{name: "omitted keeps off"},
		{name: "explicit remote without QR", remoteInput: &on, showQR: &off, wantRemote: true},
		{name: "explicit QR over inherited remote", showQR: &on, wantRemote: true, wantQR: true},
	} {
		input := Target{MediaID: "media-" + release.ID, RemoteInput: step.remoteInput, ShowQR: step.showQR}
		put, err := service.AssignTarget(context.Background(), Mutation{ActorID: "actor", IdempotencyKey: fmt.Sprint("step-", index)}, "node-one", input)
		if step.wantErr {
			if !errors.Is(err, ErrInvalidAssignment) {
				t.Fatalf("%s: err = %v", step.name, err)
			}
			continue
		}
		if err != nil || put.Assignment.RemoteInput != step.wantRemote || put.Assignment.ShowQR != step.wantQR {
			t.Fatalf("%s: remote=%v qr=%v err=%v", step.name, put.Assignment.RemoteInput, put.Assignment.ShowQR, err)
		}
	}
}
