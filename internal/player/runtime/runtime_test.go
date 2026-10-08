package runtime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/player/renderer"
	playerstate "github.com/jmill/cable/internal/player/state"
	"github.com/jmill/cable/internal/release"
)

func TestDuplicateAndOlderDesiredAreNoOps(t *testing.T) {
	h := newHarness(t, []byte("one"), DeliveryStream)
	assignment := h.assignment(2, DeliveryStream)
	if err := h.runtime.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if err := h.runtime.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	older := assignment
	older.DesiredGeneration = 1
	if err := h.runtime.Reconcile(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	if h.controller.fetches != 1 {
		t.Fatalf("release fetches = %d, want 1", h.controller.fetches)
	}
	if got := h.runtime.Snapshot(); got.DesiredGenerationSeen != 2 || got.ObservedGeneration != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestStreamAndCacheThenPlayMaterialization(t *testing.T) {
	t.Run("stream uses loopback capability", func(t *testing.T) {
		h := newHarness(t, []byte("stream bytes"), DeliveryStream)
		if err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryStream)); err != nil {
			t.Fatal(err)
		}
		if h.streamer.opens != 1 || h.cas.puts != 0 {
			t.Fatalf("stream opens=%d cache puts=%d", h.streamer.opens, h.cas.puts)
		}
		committed := h.renderer.commits[0]
		if !strings.HasPrefix(committed.Source, "http://127.0.0.1:") {
			t.Fatalf("source = %q", committed.Source)
		}
		if committed.Fit != "fit" || !committed.Loop || !committed.Muted || committed.Volume != 10 || committed.Orientation != 0 {
			t.Fatalf("signed playback payload not carried to renderer: %+v", committed)
		}
		if err := h.runtime.renewActive(context.Background()); err != nil {
			t.Fatal(err)
		}
		if h.streamer.renews != 1 || h.streamer.revokes != 0 {
			t.Fatalf("renewals=%d revocations=%d", h.streamer.renews, h.streamer.revokes)
		}
		h.installRelease(t, []byte("replacement"), DeliveryStream)
		if err := h.runtime.Reconcile(context.Background(), h.assignment(2, DeliveryStream)); err != nil {
			t.Fatal(err)
		}
		if h.streamer.revokes != 1 {
			t.Fatalf("replaced capability revocations = %d", h.streamer.revokes)
		}
	})
	t.Run("cache cleans partials checks capacity and commits digest", func(t *testing.T) {
		h := newHarness(t, []byte("cached bytes"), DeliveryCacheThenPlay)
		h.cas.expired = []string{"/cas/.tmp/stale.partial"}
		if err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryCacheThenPlay)); err != nil {
			t.Fatal(err)
		}
		if h.cas.puts != 1 || len(h.cas.removed) != 1 {
			t.Fatalf("puts=%d removed=%v", h.cas.puts, h.cas.removed)
		}
		if h.renderer.commits[0].Source != "/cas/"+h.artifactDigest {
			t.Fatalf("source = %q", h.renderer.commits[0].Source)
		}
	})
}

func TestMediaScheduleWebItemsMaterializeWithoutArtifacts(t *testing.T) {
	h := newHarness(t, []byte("bootstrap"), DeliveryCacheThenPlay)
	media := []byte("program media")
	sum := sha256.Sum256(media)
	mediaDigest := hex.EncodeToString(sum[:])
	envelope, err := release.Sign("schedule-release", "publisher-1", release.PayloadV1{
		SchemaVersion: release.SchemaVersion, MinimumProtocolVersion: release.ProtocolV1,
		Presentation: release.Presentation{Kind: release.PresentationMediaSchedule, MediaSchedule: &release.MediaSchedulePresentation{
			Timezone: "UTC",
			Window:   release.MediaScheduleWindow{StartsAt: "2026-10-04T00:00:00Z", EndsAt: "2026-10-04T00:02:00Z"},
			Items: []release.MediaScheduleItem{
				{ID: "file", StartsAt: "2026-10-04T00:00:00Z", EndsAt: "2026-10-04T00:01:00Z", ArtifactDigest: mediaDigest, MediaType: "video/mp4", Fit: "fit"},
				{ID: "page", StartsAt: "2026-10-04T00:01:00Z", EndsAt: "2026-10-04T00:02:00Z", Web: &release.MediaScheduleWebItem{URL: "https://example.com/page"}},
			},
			ArtifactClosure: []string{mediaDigest}, AllowedDeliveryModes: []string{DeliveryCacheThenPlay},
		}},
		Artifacts:        []release.Artifact{{Digest: mediaDigest, Size: int64(len(media)), MediaType: "video/mp4"}},
		RequiredFeatures: []string{release.FeatureWeb},
	}, h.private)
	if err != nil {
		t.Fatal(err)
	}
	h.controller.envelope = envelope
	h.controller.artifacts = map[string][]byte{mediaDigest: media}
	// A node without the web feature (appliance mpv) must refuse the release.
	if err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryCacheThenPlay)); !errors.Is(err, ErrIncompatibleRelease) {
		t.Fatalf("non-web node error = %v", err)
	}
	h.runtime.config.Features = append(h.runtime.config.Features, release.FeatureWeb)
	if err := h.runtime.Reconcile(context.Background(), h.assignment(2, DeliveryCacheThenPlay)); err != nil {
		t.Fatal(err)
	}
	if h.cas.puts != 1 {
		t.Fatalf("cached artifacts = %d, want 1", h.cas.puts)
	}
	schedule := h.renderer.commits[len(h.renderer.commits)-1].Schedule
	if len(schedule) != 2 || schedule[0].Source != "/cas/"+mediaDigest || schedule[0].WebURL != "" || schedule[1].WebURL != "https://example.com/page" || schedule[1].Source != "" {
		t.Fatalf("schedule = %#v", schedule)
	}
}

func TestWebReleaseCreatesRemoteSessionWithoutArtifacts(t *testing.T) {
	h := newHarness(t, []byte("bootstrap"), DeliveryStream)
	h.runtime.config.Features = append(h.runtime.config.Features, "chromium")
	envelope, err := release.Sign("web-release", "publisher-1", release.PayloadV1{
		SchemaVersion: release.SchemaVersion, MinimumProtocolVersion: release.ProtocolV1,
		Presentation:     release.Presentation{Kind: release.PresentationWeb, Web: &release.WebPresentation{URL: "https://example.com/app"}},
		RequiredFeatures: []string{"chromium"},
	}, h.private)
	if err != nil {
		t.Fatal(err)
	}
	h.controller.envelope = envelope
	assignment := h.assignment(1, DeliveryStream)
	assignment.PresentationKind = release.PresentationWeb
	assignment.RemoteInput = true
	if err := h.runtime.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	candidate := h.renderer.commits[0]
	if candidate.Kind != release.PresentationWeb || candidate.WebURL != "https://example.com/app" || candidate.Remote == nil {
		t.Fatalf("web candidate = %#v", candidate)
	}
	if h.controller.remoteCreates != 1 || h.controller.downloads != 0 || h.cas.puts != 0 {
		t.Fatalf("remote=%d downloads=%d puts=%d", h.controller.remoteCreates, h.controller.downloads, h.cas.puts)
	}
	cached := h.assignment(2, DeliveryCacheThenPlay)
	cached.PresentationKind = release.PresentationWeb
	if err := h.runtime.Reconcile(context.Background(), cached); !errors.Is(err, ErrIncompatibleRelease) {
		t.Fatalf("cache_then_play web = %v", err)
	}
}

func TestPresentationModeRejectsOtherReleaseKind(t *testing.T) {
	h := newHarness(t, []byte("media"), DeliveryStream)
	h.runtime.config.PresentationMode = release.PresentationWeb
	err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryStream))
	if !errors.Is(err, ErrIncompatibleRelease) || !errors.Is(err, ErrSafeMode) {
		t.Fatalf("error = %v", err)
	}
	if h.controller.downloads != 0 || len(h.renderer.commits) != 0 {
		t.Fatalf("downloads=%d commits=%v", h.controller.downloads, h.renderer.commits)
	}
	h.runtime.config.PresentationMode = release.PresentationMedia
	if err := h.runtime.Reconcile(context.Background(), h.assignment(2, DeliveryStream)); err != nil {
		t.Fatal(err)
	}
	if err := h.runtime.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := h.controller.reports[len(h.controller.reports)-1]
	if !slices.ContainsFunc(report.Conditions, func(condition playerclient.Condition) bool {
		return condition.Type == "SafeMode" && !condition.Status && condition.Reason == "HealthyReleaseActive"
	}) {
		t.Fatalf("conditions = %#v", report.Conditions)
	}
}

func TestCacheRejectsInsufficientCapacityBeforeDownload(t *testing.T) {
	h := newHarness(t, []byte("too large"), DeliveryCacheThenPlay)
	h.capacity.free = 1
	err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryCacheThenPlay))
	if !errors.Is(err, ErrInsufficientCapacity) || !errors.Is(err, ErrSafeMode) {
		t.Fatalf("error = %v", err)
	}
	if h.controller.downloads != 0 || h.cas.puts != 0 {
		t.Fatalf("downloads=%d puts=%d", h.controller.downloads, h.cas.puts)
	}
	if got := h.runtime.Snapshot(); !got.Safe || got.ObservedGeneration != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestSignatureAndDigestMismatchAreRejected(t *testing.T) {
	t.Run("signature", func(t *testing.T) {
		h := newHarness(t, []byte("signed"), DeliveryStream)
		h.controller.envelope.Signature = "AAAA"
		err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryStream))
		if err == nil || !errors.Is(err, ErrActivationFailed) {
			t.Fatalf("error = %v", err)
		}
		if h.renderer.starts != 0 || h.runtime.Snapshot().ObservedGeneration != 0 {
			t.Fatal("unverified release reached renderer or observed generation")
		}
	})
	t.Run("exact assignment digest", func(t *testing.T) {
		h := newHarness(t, []byte("bound"), DeliveryStream)
		assignment := h.assignment(1, DeliveryStream)
		assignment.ReleaseDigest = strings.Repeat("a", 64)
		err := h.runtime.Reconcile(context.Background(), assignment)
		if !errors.Is(err, ErrReleaseBinding) {
			t.Fatalf("error = %v", err)
		}
		if h.renderer.starts != 0 || h.runtime.Snapshot().ObservedGeneration != 0 {
			t.Fatal("mismatched release reached renderer or observed generation")
		}
	})
	t.Run("download digest", func(t *testing.T) {
		h := newHarness(t, []byte("expected"), DeliveryCacheThenPlay)
		h.controller.artifact = []byte("tampered")
		err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryCacheThenPlay))
		if err == nil || h.renderer.starts != 0 || h.runtime.Snapshot().ObservedGeneration != 0 {
			t.Fatalf("error=%v snapshot=%+v", err, h.runtime.Snapshot())
		}
	})
}

func TestRendererFailureBeforeCommitNeverAdvancesObserved(t *testing.T) {
	for _, operation := range []string{"prepare", "start", "health"} {
		t.Run(operation, func(t *testing.T) {
			h := newHarness(t, []byte(operation), DeliveryStream)
			h.renderer.failOp = operation
			h.renderer.failDigest = h.controller.envelope.Digest
			err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryStream))
			if err == nil {
				t.Fatal("expected failure")
			}
			if len(h.renderer.commits) != 0 || h.runtime.Snapshot().ObservedGeneration != 0 {
				t.Fatalf("commits=%v snapshot=%+v", h.renderer.commits, h.runtime.Snapshot())
			}
		})
	}
}

func TestProbationFailureRollsBackUsableLKGOrEntersSafe(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		h := newHarness(t, []byte("lkg"), DeliveryStream)
		first := h.assignment(1, DeliveryStream)
		if err := h.runtime.Reconcile(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		lkgDigest := first.ReleaseDigest
		h.installRelease(t, []byte("candidate"), DeliveryStream)
		h.renderer.failOp = "health"
		h.renderer.failDigest = h.controller.envelope.Digest
		err := h.runtime.Reconcile(context.Background(), h.assignment(2, DeliveryStream))
		if err == nil || errors.Is(err, ErrSafeMode) {
			t.Fatalf("error = %v", err)
		}
		got := h.runtime.Snapshot()
		if got.ObservedGeneration != 1 || got.RenderingDigest != lkgDigest || got.Safe {
			t.Fatalf("snapshot = %+v", got)
		}
		attempt, _ := h.journal.Attempt(2)
		if attempt.State != StateRolledBack {
			t.Fatalf("attempt = %+v", attempt)
		}
	})
	t.Run("safe without LKG", func(t *testing.T) {
		h := newHarness(t, []byte("candidate"), DeliveryStream)
		h.renderer.failOp = "health"
		h.renderer.failDigest = h.controller.envelope.Digest
		err := h.runtime.Reconcile(context.Background(), h.assignment(1, DeliveryStream))
		if !errors.Is(err, ErrSafeMode) {
			t.Fatalf("error = %v", err)
		}
		if !h.runtime.Snapshot().Safe || h.renderer.stops != 1 {
			t.Fatalf("snapshot=%+v stops=%d", h.runtime.Snapshot(), h.renderer.stops)
		}
	})
}

func TestRebootRecoversHealthyDuplicateAndOfflineLKG(t *testing.T) {
	h := newHarness(t, []byte("durable"), DeliveryCacheThenPlay)
	assignment := h.assignment(4, DeliveryCacheThenPlay)
	if err := h.runtime.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}

	rebootRenderer := &fakeRenderer{current: renderer.Status{ReleaseDigest: assignment.ReleaseDigest, Healthy: true, Path: "/cas/" + h.artifactDigest}}
	deps := h.deps
	deps.Renderer = rebootRenderer
	rebooted, err := New(h.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebooted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rebooted.Snapshot(); got.LastKnownGoodDigest != assignment.ReleaseDigest || got.ObservedGeneration != 0 {
		t.Fatalf("offline recovery snapshot = %+v", got)
	}
	fetches := h.controller.fetches
	if err := rebooted.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if got := rebooted.Snapshot(); got.ObservedGeneration != 4 || got.HealthyDigest != assignment.ReleaseDigest {
		t.Fatalf("duplicate recovery snapshot = %+v", got)
	}
	if h.controller.fetches != fetches {
		t.Fatal("durably healthy duplicate refetched release")
	}
}

func TestControllerLossUsesBoundedReconnectBackoff(t *testing.T) {
	h := newHarness(t, []byte("offline"), DeliveryStream)
	h.controller.capabilityErr = errors.New("offline")
	ctx, cancel := context.WithCancel(context.Background())
	h.clock.onSleep = cancel
	err := h.runtime.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if h.runtime.Snapshot().ControllerAvailable {
		t.Fatal("controller reported available")
	}
	if len(h.clock.sleeps) != 1 || h.clock.sleeps[0] != h.config.ReconnectMinimum {
		t.Fatalf("sleeps = %v", h.clock.sleeps)
	}
}

func TestObservedGenerationAndOutputBounds(t *testing.T) {
	h := newHarness(t, []byte("bounds"), DeliveryStream)
	for range h.config.MaxEvents + 5 {
		h.runtime.event("failure", 0, nil)
	}
	for index := range h.config.MaxConditions + 3 {
		h.runtime.condition(string(rune('a'+index)), false, "Failure", strings.Repeat("x", 100))
	}
	if len(h.runtime.events) != h.config.MaxEvents {
		t.Fatalf("events = %d", len(h.runtime.events))
	}
	if err := h.runtime.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventID := h.controller.events[0].Events[0].ID
	if len(eventID) != 36 || strings.Count(eventID, "-") != 4 || eventID[14] != '5' || eventID != eventUUID(h.config.EventEpoch, uint64(h.controller.events[0].Events[0].Sequence)) {
		t.Fatalf("event ID is not a deterministic UUID: %q", eventID)
	}
	report := h.controller.reports[len(h.controller.reports)-1]
	if len(report.Conditions) != h.config.MaxConditions {
		t.Fatalf("conditions = %d", len(report.Conditions))
	}
	for _, condition := range report.Conditions {
		if len([]rune(condition.Message)) > h.config.MaxMessageRunes {
			t.Fatalf("unbounded message %q", condition.Message)
		}
	}
	if report.ObservedGeneration != 0 {
		t.Fatalf("observed generation = %d before activation", report.ObservedGeneration)
	}
}

type harness struct {
	runtime        *Reconciler
	config         Config
	deps           Dependencies
	controller     *fakeController
	journal        *fakeJournal
	cas            *fakeCAS
	capacity       *fakeCapacity
	streamer       *fakeStreamer
	renderer       *fakeRenderer
	clock          *fakeClock
	private        ed25519.PrivateKey
	artifactDigest string
}

func newHarness(t *testing.T, content []byte, mode string) *harness {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		controller: &fakeController{}, journal: newFakeJournal(), cas: newFakeCAS(), capacity: &fakeCapacity{free: 1 << 30},
		streamer: &fakeStreamer{source: "http://127.0.0.1:9876/media/token"}, renderer: &fakeRenderer{}, clock: &fakeClock{now: time.Unix(1000, 0)}, private: private,
	}
	h.config = Config{
		Capabilities: playerclient.CapabilityReport{Platform: "linux", Architecture: "arm64", ProtocolVersion: release.ProtocolV1, RendererVersion: "mpv-test", ProfileID: "profile-1", OutputReady: true, ClockReliable: true},
		EventEpoch:   "11111111-1111-1111-1111-111111111111", PublisherKeyID: "publisher-1", PublisherPublicKey: public,
		Features: []string{"loop"}, AcceptedMediaTypes: []string{"video/mp4"}, Probation: time.Second, PollWait: time.Second,
		ReconnectMinimum: time.Second, ReconnectMaximum: 8 * time.Second, PartialGrace: time.Minute, StreamTTL: time.Minute,
		MaxEvents: 8, MaxEventBatch: 4, MaxConditions: 4, MaxMessageRunes: 16,
	}
	h.installRelease(t, content, mode)
	h.deps = Dependencies{Controller: h.controller, Journal: h.journal, CAS: h.cas, Capacity: h.capacity, Streamer: h.streamer, Renderer: h.renderer, Clock: h.clock}
	h.runtime, err = New(h.config, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) installRelease(t *testing.T, content []byte, mode string) {
	t.Helper()
	sum := sha256.Sum256(content)
	h.artifactDigest = hex.EncodeToString(sum[:])
	envelope, err := release.Sign("release-"+h.artifactDigest[:8], "publisher-1", release.PayloadV1{
		SchemaVersion: release.SchemaVersion, MinimumProtocolVersion: release.ProtocolV1,
		Playback:  release.PlaybackPayload{AssetID: "asset-1", Fit: "fit", Loop: true, Muted: true, Volume: 10, AllowedDeliveryMode: []string{mode}},
		Artifacts: []release.Artifact{{Digest: h.artifactDigest, Size: int64(len(content)), MediaType: "video/mp4"}}, RequiredFeatures: []string{"loop"},
	}, h.private)
	if err != nil {
		t.Fatal(err)
	}
	h.controller.envelope = envelope
	h.controller.artifact = append([]byte(nil), content...)
}

func (h *harness) assignment(generation int64, mode string) playerclient.Assignment {
	return playerclient.Assignment{ID: "assignment", NodeID: "node", ReleaseID: h.controller.envelope.ID, ReleaseDigest: h.controller.envelope.Digest, DeliveryMode: mode, ExpectedGeneration: generation - 1, DesiredGeneration: generation}
}

type fakeController struct {
	envelope           release.EnvelopeV1
	artifact           []byte
	fetches, downloads int
	artifacts          map[string][]byte
	capabilityErr      error
	reports            []playerclient.NodeReport
	events             []playerclient.EventBatch
	remoteCreates      int
}

func (c *fakeController) CreateRemoteSession(context.Context, int64) (playerclient.RemoteSession, error) {
	c.remoteCreates++
	return playerclient.RemoteSession{ID: "remote-session", RemoteURL: "https://control.example/remote/session#token=secret", ShowQR: true, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (c *fakeController) PollRemoteEvents(context.Context, string, int64, time.Duration) (playerclient.RemoteEventPage, error) {
	return playerclient.RemoteEventPage{Events: []playerclient.RemoteEvent{}}, nil
}

func (c *fakeController) PutCapabilities(context.Context, playerclient.CapabilityReport, string) error {
	return c.capabilityErr
}
func (c *fakeController) GetAssignment(context.Context, string, time.Duration) (playerclient.AssignmentResult, error) {
	return playerclient.AssignmentResult{NotModified: true}, nil
}
func (c *fakeController) FetchRelease(context.Context, string) (playerclient.ReleaseEnvelope, error) {
	c.fetches++
	return c.envelope, nil
}
func (c *fakeController) Report(_ context.Context, report playerclient.NodeReport, _ string) (bool, error) {
	c.reports = append(c.reports, report)
	return true, nil
}
func (c *fakeController) AppendEvents(_ context.Context, batch playerclient.EventBatch, _ string) (playerclient.EventAcknowledgement, error) {
	c.events = append(c.events, batch)
	return playerclient.EventAcknowledgement{Epoch: batch.Epoch, AcknowledgedThrough: batch.Events[len(batch.Events)-1].Sequence}, nil
}
func (c *fakeController) HeadArtifact(_ context.Context, digest string) (playerclient.ArtifactMetadata, error) {
	content := c.artifact
	if c.artifacts != nil {
		content = c.artifacts[digest]
	}
	return playerclient.ArtifactMetadata{Size: int64(len(content)), ETag: "artifact-etag"}, nil
}
func (c *fakeController) GetArtifact(_ context.Context, digest string, _ *playerclient.ByteRange, _ string) (playerclient.ArtifactContent, error) {
	c.downloads++
	content := c.artifact
	if c.artifacts != nil {
		content = c.artifacts[digest]
	}
	return playerclient.ArtifactContent{Body: io.NopCloser(bytes.NewReader(content)), Size: int64(len(content))}, nil
}

type fakeJournal struct {
	current, report, watermark uint64
	epoch                      string
	attempts                   map[uint64]playerstate.Attempt
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{attempts: make(map[uint64]playerstate.Attempt)}
}
func (s *fakeJournal) AcceptGeneration(g uint64, digest string) (bool, error) {
	if g <= s.current {
		return false, nil
	}
	s.current = g
	s.attempts[g] = playerstate.Attempt{Generation: g, ReleaseDigest: digest, State: StatePending}
	return true, nil
}
func (s *fakeJournal) RecordTransition(g uint64, state, intent, outcome string) error {
	a, ok := s.attempts[g]
	if !ok {
		return errors.New("unknown")
	}
	a.State, a.Intent, a.Outcome = state, intent, outcome
	s.attempts[g] = a
	return nil
}
func (s *fakeJournal) Attempt(g uint64) (playerstate.Attempt, error) {
	a, ok := s.attempts[g]
	if !ok {
		return a, errors.New("unknown")
	}
	return a, nil
}
func (s *fakeJournal) SetEventProgress(epoch string, report, watermark uint64) error {
	if report < s.report || watermark < s.watermark {
		return errors.New("backwards")
	}
	s.epoch, s.report, s.watermark = epoch, report, watermark
	return nil
}
func (s *fakeJournal) EventProgress() (string, uint64, uint64, error) {
	return s.epoch, s.report, s.watermark, nil
}

type fakeCAS struct {
	objects          map[string][]byte
	puts             int
	expired, removed []string
}

func newFakeCAS() *fakeCAS { return &fakeCAS{objects: make(map[string][]byte)} }
func (s *fakeCAS) Put(reader io.Reader, expected string) (localcas.Object, error) {
	content, err := io.ReadAll(reader)
	if err != nil {
		return localcas.Object{}, err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if digest != expected {
		return localcas.Object{}, errors.New("digest mismatch")
	}
	s.puts++
	s.objects[digest] = content
	return localcas.Object{Digest: digest, Size: int64(len(content)), Path: "/cas/" + digest}, nil
}
func (s *fakeCAS) Stat(digest string) (localcas.Object, error) {
	content, ok := s.objects[digest]
	if !ok {
		return localcas.Object{}, errors.New("missing")
	}
	return localcas.Object{Digest: digest, Size: int64(len(content)), Path: "/cas/" + digest}, nil
}
func (s *fakeCAS) Reconcile(map[string]bool, time.Duration, time.Time) (localcas.ReconcileResult, error) {
	return localcas.ReconcileResult{ExpiredPartials: append([]string(nil), s.expired...)}, nil
}
func (s *fakeCAS) RemovePartial(path string) error { s.removed = append(s.removed, path); return nil }

type fakeCapacity struct {
	free int64
	err  error
}

func (c *fakeCapacity) AvailableBytes(context.Context) (int64, error) { return c.free, c.err }

type fakeStreamer struct {
	source                 string
	opens, renews, revokes int
	renewErr               error
}

func (s *fakeStreamer) Open(context.Context, string, time.Duration) (string, func(), error) {
	s.opens++
	return s.source, func() { s.revokes++ }, nil
}

func (s *fakeStreamer) Renew(_ context.Context, source string, _ time.Duration) error {
	if source != s.source {
		return errors.New("unknown source")
	}
	s.renews++
	return s.renewErr
}

type fakeRenderer struct {
	current            renderer.Status
	failOp, failDigest string
	starts, stops      int
	commits            []renderer.Candidate
}

func (r *fakeRenderer) fails(op string, candidate renderer.Candidate) bool {
	return r.failOp == op && (r.failDigest == "" || r.failDigest == candidate.ReleaseDigest)
}
func (r *fakeRenderer) Prepare(_ context.Context, candidate renderer.Candidate) error {
	if r.fails("prepare", candidate) {
		return errors.New("prepare")
	}
	return nil
}
func (r *fakeRenderer) Start(_ context.Context, candidate renderer.Candidate) error {
	r.starts++
	if r.fails("start", candidate) {
		return errors.New("start")
	}
	r.current = renderer.Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: true, Path: candidate.Source}
	return nil
}
func (r *fakeRenderer) Health(_ context.Context, candidate renderer.Candidate) (renderer.Status, error) {
	if r.fails("health", candidate) {
		return renderer.Status{}, errors.New("health")
	}
	return renderer.Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: true, Path: candidate.Source}, nil
}
func (r *fakeRenderer) Commit(_ context.Context, candidate renderer.Candidate) error {
	if r.fails("commit", candidate) {
		return errors.New("commit")
	}
	r.commits = append(r.commits, candidate)
	r.current = renderer.Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: true, Path: candidate.Source}
	return nil
}
func (r *fakeRenderer) Stop(context.Context) error {
	r.stops++
	r.current = renderer.Status{}
	return nil
}
func (r *fakeRenderer) Current(context.Context) (renderer.Status, error) { return r.current, nil }

type fakeClock struct {
	now     time.Time
	sleeps  []time.Duration
	onSleep func()
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(ctx context.Context, duration time.Duration) error {
	c.sleeps = append(c.sleeps, duration)
	c.now = c.now.Add(duration)
	if c.onSleep != nil {
		c.onSleep()
	}
	return ctx.Err()
}
