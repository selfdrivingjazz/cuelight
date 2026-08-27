package runtime

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/player/renderer"
	"github.com/jmill/cable/internal/release"
)

type Reconciler struct {
	config Config
	deps   Dependencies

	snapshot       Snapshot
	etag           string
	epoch          string
	reportSeq      uint64
	watermark      uint64
	eventSeq       uint64
	events         []playerclient.NodeEvent
	conditions     map[string]playerclient.Condition
	lkg            renderer.Candidate
	activeRevoke   func()
	activeDelivery string
	recovered      bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
func (systemClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func New(config Config, dependencies Dependencies) (*Reconciler, error) {
	if dependencies.Controller == nil || dependencies.Journal == nil || dependencies.CAS == nil || dependencies.Capacity == nil || dependencies.Renderer == nil {
		return nil, errors.New("controller, journal, CAS, capacity, and renderer are required")
	}
	if config.Capabilities.Platform == "" || config.Capabilities.Architecture == "" || config.Capabilities.ProtocolVersion == "" || config.Capabilities.RendererVersion == "" || config.Capabilities.ProfileID == "" {
		return nil, errors.New("complete player capabilities are required")
	}
	if config.EventEpoch == "" || config.PublisherKeyID == "" || len(config.PublisherPublicKey) != 32 {
		return nil, errors.New("event epoch and publisher verification key are required")
	}
	if config.Probation <= 0 || config.ReconnectMinimum <= 0 || config.ReconnectMaximum < config.ReconnectMinimum || config.PartialGrace < 0 || config.StreamTTL <= config.Probation {
		return nil, errors.New("invalid runtime timing configuration")
	}
	renewalLimit := config.StreamTTL / 2
	if config.PollWait >= renewalLimit || config.ReconnectMaximum >= renewalLimit {
		return nil, errors.New("poll and reconnect durations must be less than half the stream TTL")
	}
	if config.PollWait < 0 || config.MaxEvents < 1 || config.MaxEventBatch < 1 || config.MaxEventBatch > config.MaxEvents || config.MaxConditions < 1 || config.MaxMessageRunes < 1 {
		return nil, errors.New("invalid runtime output bounds")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = systemClock{}
	}
	epoch, reportSequence, watermark, err := dependencies.Journal.EventProgress()
	if err != nil {
		return nil, fmt.Errorf("load runtime event progress: %w", err)
	}
	if epoch == "" {
		epoch = config.EventEpoch
		if err := dependencies.Journal.SetEventProgress(epoch, reportSequence, watermark); err != nil {
			return nil, fmt.Errorf("initialize runtime event progress: %w", err)
		}
	} else if epoch != config.EventEpoch {
		return nil, errors.New("configured event epoch differs from durable player epoch")
	}
	return &Reconciler{
		config:     config,
		deps:       dependencies,
		epoch:      epoch,
		reportSeq:  reportSequence,
		watermark:  watermark,
		eventSeq:   watermark,
		conditions: make(map[string]playerclient.Condition),
	}, nil
}

func (r *Reconciler) Snapshot() Snapshot { return r.snapshot }

// Recover inspects the renderer before contacting the controller. A healthy
// renderer remains usable as LKG during an offline startup.
func (r *Reconciler) Recover(ctx context.Context) error {
	if r.recovered {
		return nil
	}
	r.recovered = true
	status, err := r.deps.Renderer.Current(ctx)
	if err != nil {
		r.condition("RendererReady", false, "StartupInspectionFailed", err.Error())
		r.event("renderer_inspection_failed", 0, map[string]any{"reason": "current renderer unavailable"})
		return nil
	}
	if status.Healthy && status.ReleaseDigest != "" && status.Path != "" {
		r.lkg = status.Candidate
		if r.lkg.ReleaseDigest != status.ReleaseDigest {
			r.lkg = renderer.Candidate{ReleaseDigest: status.ReleaseDigest, Source: status.Path, Fit: "fit"}
		}
		r.snapshot.RenderingDigest = status.ReleaseDigest
		r.snapshot.HealthyDigest = status.ReleaseDigest
		r.snapshot.LastKnownGoodDigest = status.ReleaseDigest
		r.condition("RendererReady", true, "RecoveredHealthyPlayback", "")
	}
	return nil
}

func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.Recover(ctx); err != nil {
		return err
	}
	backoff := r.config.ReconnectMinimum
	capabilitiesSent := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.renewActive(ctx); err != nil {
			generation := r.snapshot.ObservedGeneration
			r.stopActive(ctx)
			if generation > 0 {
				_ = r.deps.Journal.RecordTransition(generation, StateSafe, "stop playback", "StreamCapabilityRenewalFailed")
			}
			r.condition("SafeMode", true, "StreamCapabilityRenewalFailed", "")
			r.event("safe_mode_entered", generation, nil)
		}
		if !capabilitiesSent {
			capabilities := r.config.Capabilities
			if free, err := r.deps.Capacity.AvailableBytes(ctx); err == nil && free >= 0 {
				capabilities.FreeBytes = free
			}
			if err := r.deps.Controller.PutCapabilities(ctx, capabilities, stableKey(r.epoch, "capabilities", 0)); err != nil {
				r.controllerLost(err)
				if err := r.deps.Clock.Sleep(ctx, backoff); err != nil {
					return err
				}
				backoff = nextBackoff(backoff, r.config.ReconnectMaximum)
				continue
			}
			capabilitiesSent = true
			r.controllerRestored()
			backoff = r.config.ReconnectMinimum
		}

		result, err := r.deps.Controller.GetAssignment(ctx, r.etag, r.config.PollWait)
		if err != nil {
			r.controllerLost(err)
			capabilitiesSent = false
			if err := r.deps.Clock.Sleep(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff, r.config.ReconnectMaximum)
			continue
		}
		r.controllerRestored()
		backoff = r.config.ReconnectMinimum
		if result.ETag != "" {
			r.etag = result.ETag
		}
		if !result.NotModified {
			reconcileErr := r.Reconcile(ctx, result.Assignment)
			if reconcileErr != nil {
				var requestErr *playerclient.RequestError
				var statusErr *playerclient.StatusError
				if errors.As(reconcileErr, &requestErr) || errors.As(reconcileErr, &statusErr) {
					r.controllerLost(reconcileErr)
					capabilitiesSent = false
				}
			}
		} else {
			r.inspectActive(ctx)
		}
		if err := r.flush(ctx); err != nil {
			r.controllerLost(err)
			capabilitiesSent = false
			if err := r.deps.Clock.Sleep(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff, r.config.ReconnectMaximum)
		}
	}
}

// Reconcile applies one desired assignment. Older and exact duplicate desired
// generations are idempotent; only a durably HEALTHY exact duplicate can
// restore observed generation after reboot.
func (r *Reconciler) Reconcile(ctx context.Context, assignment playerclient.Assignment) error {
	generation, err := validateAssignment(assignment)
	if err != nil {
		return &RuntimeError{Kind: ErrInvalidAssignment, Operation: "validate assignment", Err: err}
	}
	if generation < r.snapshot.DesiredGenerationSeen {
		return nil
	}
	if generation > r.snapshot.DesiredGenerationSeen {
		r.snapshot.DesiredGenerationSeen = generation
	}

	accepted, err := r.deps.Journal.AcceptGeneration(generation, assignment.ReleaseDigest)
	if err != nil {
		return &RuntimeError{Kind: ErrActivationFailed, Generation: generation, Operation: "persist desired generation", Err: err}
	}
	if !accepted {
		attempt, attemptErr := r.deps.Journal.Attempt(generation)
		if attemptErr != nil || attempt.ReleaseDigest != assignment.ReleaseDigest {
			return nil
		}
		if attempt.State == StateHealthy {
			status, currentErr := r.deps.Renderer.Current(ctx)
			if currentErr == nil && status.Healthy && status.ReleaseDigest == assignment.ReleaseDigest {
				candidate := status.Candidate
				if candidate.ReleaseDigest != status.ReleaseDigest {
					candidate = r.lkg
				}
				if candidate.ReleaseDigest != status.ReleaseDigest {
					candidate = renderer.Candidate{ReleaseDigest: status.ReleaseDigest, Source: status.Path, Fit: "fit"}
				}
				r.restoreHealthy(generation, candidate)
				r.activeDelivery = assignment.DeliveryMode
				return nil
			}
		}
	}

	if err := r.transition(generation, StateFetching, "fetch exact release", ""); err != nil {
		return err
	}
	envelope, err := r.deps.Controller.FetchRelease(ctx, assignment.ReleaseID)
	if err != nil {
		return r.fail(ctx, generation, "fetch release", err)
	}
	if envelope.ID != assignment.ReleaseID || envelope.Digest != assignment.ReleaseDigest {
		return r.fail(ctx, generation, "bind release", ErrReleaseBinding)
	}
	if err := release.Verify(envelope, r.config.PublisherKeyID, r.config.PublisherPublicKey, r.config.Capabilities.ProtocolVersion); err != nil {
		return r.fail(ctx, generation, "verify release", err)
	}
	if err := r.checkProfile(envelope, assignment.DeliveryMode); err != nil {
		return r.fail(ctx, generation, "check playback profile", err)
	}

	candidate, revoke, err := r.materialize(ctx, envelope, assignment.DeliveryMode)
	if err != nil {
		return r.fail(ctx, generation, "materialize release", err)
	}
	keepSource := false
	defer func() {
		if !keepSource && revoke != nil {
			revoke()
		}
	}()
	r.snapshot.CandidateDigest = candidate.ReleaseDigest
	if err := r.transition(generation, StatePrepared, "prepare renderer", ""); err != nil {
		return r.fail(ctx, generation, "persist prepared", err)
	}
	if err := r.deps.Renderer.Prepare(ctx, candidate); err != nil {
		return r.fail(ctx, generation, "prepare renderer", err)
	}
	if err := r.transition(generation, StateRendering, "start candidate", ""); err != nil {
		return r.fail(ctx, generation, "persist rendering", err)
	}
	if err := r.deps.Renderer.Start(ctx, candidate); err != nil {
		return r.fail(ctx, generation, "start renderer", err)
	}
	r.snapshot.RenderingDigest = candidate.ReleaseDigest
	if err := r.transition(generation, StateProbation, "fixed probation", ""); err != nil {
		return r.fail(ctx, generation, "persist probation", err)
	}
	if err := r.deps.Clock.Sleep(ctx, r.config.Probation); err != nil {
		return r.fail(ctx, generation, "wait probation", err)
	}
	status, err := r.deps.Renderer.Health(ctx, candidate)
	if err != nil || !status.Healthy || status.ReleaseDigest != candidate.ReleaseDigest {
		if err == nil {
			err = errors.New("renderer did not report the exact candidate healthy")
		}
		return r.fail(ctx, generation, "verify probation", err)
	}
	if err := r.transition(generation, StateCommitting, "commit candidate", ""); err != nil {
		return r.fail(ctx, generation, "persist commit intent", err)
	}
	if err := r.deps.Renderer.Commit(ctx, candidate); err != nil {
		return r.fail(ctx, generation, "commit renderer", err)
	}
	if err := r.transition(generation, StateHealthy, "activate candidate", "healthy after fixed probation"); err != nil {
		return r.fail(ctx, generation, "persist healthy activation", err)
	}

	previousRevoke := r.activeRevoke
	r.activeRevoke = revoke
	r.activeDelivery = assignment.DeliveryMode
	keepSource = true
	if previousRevoke != nil {
		previousRevoke()
	}
	r.restoreHealthy(generation, candidate)
	r.condition("PlaybackHealthy", true, "CandidateCommitted", "")
	r.event("release_healthy", generation, map[string]any{"releaseDigest": candidate.ReleaseDigest})
	return nil
}

func validateAssignment(assignment playerclient.Assignment) (uint64, error) {
	if assignment.ID == "" || assignment.ReleaseID == "" || assignment.ReleaseDigest == "" || assignment.DesiredGeneration <= 0 || assignment.ExpectedGeneration < 0 {
		return 0, errors.New("identity, release binding, and positive desired generation are required")
	}
	if assignment.DeliveryMode != DeliveryStream && assignment.DeliveryMode != DeliveryCacheThenPlay {
		return 0, errors.New("unsupported delivery mode")
	}
	if err := release.ValidateDigest(assignment.ReleaseDigest); err != nil {
		return 0, err
	}
	return uint64(assignment.DesiredGeneration), nil
}

func (r *Reconciler) checkProfile(envelope release.EnvelopeV1, mode string) error {
	if !slices.Contains(envelope.Payload.Playback.AllowedDeliveryMode, mode) {
		return fmt.Errorf("%w: delivery mode %q", ErrIncompatibleRelease, mode)
	}
	for _, feature := range envelope.Payload.RequiredFeatures {
		if !slices.Contains(r.config.Features, feature) {
			return fmt.Errorf("%w: required feature %q", ErrIncompatibleRelease, feature)
		}
	}
	for _, artifact := range envelope.Payload.Artifacts {
		if len(r.config.AcceptedMediaTypes) > 0 && !slices.Contains(r.config.AcceptedMediaTypes, artifact.MediaType) {
			return fmt.Errorf("%w: media type %q", ErrIncompatibleRelease, artifact.MediaType)
		}
	}
	return nil
}

func (r *Reconciler) materialize(ctx context.Context, envelope release.EnvelopeV1, mode string) (renderer.Candidate, func(), error) {
	artifact := envelope.Payload.Artifacts[0]
	playback := envelope.Payload.Playback
	candidate := renderer.Candidate{
		ReleaseDigest:       envelope.Digest,
		Fit:                 playback.Fit,
		Loop:                playback.Loop,
		Muted:               playback.Muted,
		Volume:              playback.Volume,
		ImageDurationSecond: playback.ImageDurationSecond,
		Orientation:         playback.Orientation,
	}
	if mode == DeliveryStream {
		if r.deps.Streamer == nil {
			return candidate, nil, ErrUnsafeStreamSource
		}
		source, revoke, err := r.deps.Streamer.Open(ctx, artifact.Digest, r.config.StreamTTL)
		if err != nil {
			return candidate, nil, err
		}
		if !isLoopbackURL(source) {
			if revoke != nil {
				revoke()
			}
			return candidate, nil, ErrUnsafeStreamSource
		}
		candidate.Source = source
		return candidate, revoke, nil
	}

	referenced := make(map[string]bool, len(envelope.Payload.Artifacts))
	for _, item := range envelope.Payload.Artifacts {
		referenced[item.Digest] = true
	}
	reconciled, err := r.deps.CAS.Reconcile(referenced, r.config.PartialGrace, r.deps.Clock.Now())
	if err != nil {
		return candidate, nil, err
	}
	for _, partial := range reconciled.ExpiredPartials {
		if err := r.deps.CAS.RemovePartial(partial); err != nil {
			return candidate, nil, err
		}
	}

	missing := make([]release.Artifact, 0, len(envelope.Payload.Artifacts))
	var required int64
	objects := make(map[string]localcas.Object, len(envelope.Payload.Artifacts))
	for _, item := range envelope.Payload.Artifacts {
		object, statErr := r.deps.CAS.Stat(item.Digest)
		if statErr == nil && object.Digest == item.Digest && object.Size == item.Size {
			objects[item.Digest] = object
			continue
		}
		missing = append(missing, item)
		if item.Size > 0 && required > (1<<63-1)-item.Size {
			return candidate, nil, ErrInsufficientCapacity
		}
		required += item.Size
	}
	free, err := r.deps.Capacity.AvailableBytes(ctx)
	if err != nil {
		return candidate, nil, err
	}
	if free < required {
		return candidate, nil, fmt.Errorf("%w: need %d bytes, have %d", ErrInsufficientCapacity, required, free)
	}
	for _, item := range missing {
		object, err := r.download(ctx, item)
		if err != nil {
			return candidate, nil, err
		}
		objects[item.Digest] = object
	}
	candidate.Source = objects[artifact.Digest].Path
	if candidate.Source == "" {
		return candidate, nil, errors.New("CAS committed object has no renderer path")
	}
	return candidate, nil, nil
}

func (r *Reconciler) download(ctx context.Context, artifact release.Artifact) (localcas.Object, error) {
	metadata, err := r.deps.Controller.HeadArtifact(ctx, artifact.Digest)
	if err != nil {
		return localcas.Object{}, err
	}
	if metadata.Size != artifact.Size {
		return localcas.Object{}, ErrReleaseBinding
	}
	content, err := r.deps.Controller.GetArtifact(ctx, artifact.Digest, nil, metadata.ETag)
	if err != nil {
		return localcas.Object{}, err
	}
	if content.Body == nil {
		return localcas.Object{}, errors.New("artifact response body is missing")
	}
	object, putErr := r.deps.CAS.Put(content.Body, artifact.Digest)
	closeErr := content.Body.Close()
	if putErr != nil {
		return localcas.Object{}, putErr
	}
	if closeErr != nil {
		return localcas.Object{}, closeErr
	}
	if content.Partial || content.Size != artifact.Size || object.Digest != artifact.Digest || object.Size != artifact.Size {
		return localcas.Object{}, ErrReleaseBinding
	}
	return object, nil
}

func isLoopbackURL(source string) bool {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (r *Reconciler) transition(generation uint64, state, intent, outcome string) error {
	if err := r.deps.Journal.RecordTransition(generation, state, intent, outcome); err != nil {
		return &RuntimeError{Kind: ErrActivationFailed, Generation: generation, Operation: "persist " + strings.ToLower(state), Err: err}
	}
	return nil
}

func (r *Reconciler) fail(ctx context.Context, generation uint64, operation string, cause error) error {
	failure := &RuntimeError{Kind: ErrActivationFailed, Generation: generation, Operation: operation, Err: cause}
	r.condition("PlaybackHealthy", false, "CandidateFailed", operation)
	r.event("release_failed", generation, map[string]any{"operation": operation})
	r.snapshot.CandidateDigest = ""
	if r.rollback(ctx, generation, operation) {
		return failure
	}
	r.stopActive(ctx)
	_ = r.deps.Journal.RecordTransition(generation, StateSafe, "stop playback", operation)
	r.condition("SafeMode", true, "NoUsableLastKnownGood", "")
	r.event("safe_mode_entered", generation, nil)
	return &RuntimeError{Kind: ErrSafeMode, Generation: generation, Operation: operation, Err: failure}
}

func (r *Reconciler) rollback(ctx context.Context, generation uint64, outcome string) bool {
	if r.lkg.ReleaseDigest == "" || r.lkg.Source == "" {
		return false
	}
	if err := r.deps.Renderer.Prepare(ctx, r.lkg); err != nil {
		return false
	}
	if err := r.deps.Renderer.Start(ctx, r.lkg); err != nil {
		return false
	}
	status, err := r.deps.Renderer.Health(ctx, r.lkg)
	if err != nil || !status.Healthy || status.ReleaseDigest != r.lkg.ReleaseDigest {
		return false
	}
	if err := r.deps.Renderer.Commit(ctx, r.lkg); err != nil {
		return false
	}
	if err := r.deps.Journal.RecordTransition(generation, StateRolledBack, r.lkg.ReleaseDigest, outcome); err != nil {
		return false
	}
	r.snapshot.RenderingDigest = r.lkg.ReleaseDigest
	r.snapshot.HealthyDigest = r.lkg.ReleaseDigest
	r.snapshot.LastKnownGoodDigest = r.lkg.ReleaseDigest
	r.snapshot.Safe = false
	r.condition("PlaybackHealthy", true, "RolledBackToLastKnownGood", "")
	r.event("release_rolled_back", generation, map[string]any{"releaseDigest": r.lkg.ReleaseDigest})
	return true
}

func (r *Reconciler) restoreHealthy(generation uint64, candidate renderer.Candidate) {
	r.snapshot.ObservedGeneration = generation
	r.snapshot.RenderingDigest = candidate.ReleaseDigest
	r.snapshot.CandidateDigest = ""
	r.snapshot.HealthyDigest = candidate.ReleaseDigest
	r.snapshot.LastKnownGoodDigest = candidate.ReleaseDigest
	r.snapshot.Safe = false
	r.lkg = candidate
}

func (r *Reconciler) renewActive(ctx context.Context) error {
	if r.activeDelivery != DeliveryStream || r.lkg.Source == "" {
		return nil
	}
	if r.deps.Streamer == nil {
		return ErrUnsafeStreamSource
	}
	return r.deps.Streamer.Renew(ctx, r.lkg.Source, r.config.StreamTTL)
}

func (r *Reconciler) stopActive(ctx context.Context) {
	_ = r.deps.Renderer.Stop(ctx)
	if r.activeRevoke != nil {
		r.activeRevoke()
		r.activeRevoke = nil
	}
	r.activeDelivery = ""
	r.snapshot.RenderingDigest = ""
	r.snapshot.HealthyDigest = ""
	r.snapshot.Safe = true
}

func (r *Reconciler) inspectActive(ctx context.Context) {
	if r.lkg.ReleaseDigest == "" {
		return
	}
	status, err := r.deps.Renderer.Health(ctx, r.lkg)
	if err != nil || !status.Healthy || status.ReleaseDigest != r.lkg.ReleaseDigest {
		r.condition("PlaybackHealthy", false, "ActiveRendererUnhealthy", "")
	} else {
		r.condition("PlaybackHealthy", true, "ActiveRendererHealthy", "")
	}
}

func (r *Reconciler) controllerLost(err error) {
	if r.snapshot.ControllerAvailable || r.conditions["ControllerAvailable"].Reason == "" {
		r.event("controller_lost", 0, nil)
	}
	r.snapshot.ControllerAvailable = false
	r.condition("ControllerAvailable", false, "ConnectionLost", err.Error())
}

func (r *Reconciler) controllerRestored() {
	if !r.snapshot.ControllerAvailable {
		r.event("controller_restored", 0, nil)
	}
	r.snapshot.ControllerAvailable = true
	r.condition("ControllerAvailable", true, "Connected", "")
}

func (r *Reconciler) condition(kind string, status bool, reason, message string) {
	if utf8.RuneCountInString(message) > r.config.MaxMessageRunes {
		message = string([]rune(message)[:r.config.MaxMessageRunes])
	}
	r.conditions[kind] = playerclient.Condition{Type: kind, Status: status, Reason: reason, Message: message}
}

func (r *Reconciler) event(kind string, generation uint64, data map[string]any) {
	r.eventSeq++
	sequence := r.eventSeq
	event := playerclient.NodeEvent{ID: eventUUID(r.epoch, sequence), Epoch: r.epoch, Sequence: int64(sequence), Type: kind, OccurredAt: r.deps.Clock.Now(), Data: data}
	if generation > 0 {
		if event.Data == nil {
			event.Data = make(map[string]any)
		}
		event.Data["generation"] = generation
	}
	if len(r.events) == r.config.MaxEvents {
		dropped := r.events[0].Sequence
		r.events = r.events[1:]
		event.CoversThrough = dropped
	}
	r.events = append(r.events, event)
}

func (r *Reconciler) flush(ctx context.Context) error {
	r.reportSeq++
	if err := r.deps.Journal.SetEventProgress(r.epoch, r.reportSeq, r.watermark); err != nil {
		return err
	}
	conditions := make([]playerclient.Condition, 0, len(r.conditions))
	keys := make([]string, 0, len(r.conditions))
	for key := range r.conditions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if len(keys) > r.config.MaxConditions {
		keys = keys[:r.config.MaxConditions]
	}
	for _, key := range keys {
		conditions = append(conditions, r.conditions[key])
	}
	report := playerclient.NodeReport{
		EventEpoch: r.epoch, Sequence: int64(r.reportSeq),
		DesiredGenerationSeen: int64(r.snapshot.DesiredGenerationSeen), ObservedGeneration: int64(r.snapshot.ObservedGeneration),
		RenderingReleaseDigest: stringPointer(r.snapshot.RenderingDigest), CandidateReleaseDigest: stringPointer(r.snapshot.CandidateDigest),
		HealthyReleaseDigest: stringPointer(r.snapshot.HealthyDigest), LastKnownGoodReleaseDigest: stringPointer(r.snapshot.LastKnownGoodDigest), Conditions: conditions,
	}
	if _, err := r.deps.Controller.Report(ctx, report, stableKey(r.epoch, "report", r.reportSeq)); err != nil {
		return err
	}
	if len(r.events) == 0 {
		return nil
	}
	count := min(len(r.events), r.config.MaxEventBatch)
	batch := slices.Clone(r.events[:count])
	ack, err := r.deps.Controller.AppendEvents(ctx, playerclient.EventBatch{Epoch: r.epoch, Events: batch}, stableKey(r.epoch, "events", uint64(batch[len(batch)-1].Sequence)))
	if err != nil {
		return err
	}
	last := uint64(batch[len(batch)-1].Sequence)
	if ack.Epoch != r.epoch || ack.AcknowledgedThrough < int64(r.watermark) || uint64(ack.AcknowledgedThrough) > last {
		return errors.New("invalid event acknowledgement")
	}
	r.watermark = uint64(ack.AcknowledgedThrough)
	remove := 0
	for remove < len(r.events) && uint64(r.events[remove].Sequence) <= r.watermark {
		remove++
	}
	r.events = r.events[remove:]
	return r.deps.Journal.SetEventProgress(r.epoch, r.reportSeq, r.watermark)
}

func stableKey(epoch, kind string, sequence uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", epoch, kind, sequence)))
	return hex.EncodeToString(sum[:])
}

func eventUUID(epoch string, sequence uint64) string {
	namespace, err := hex.DecodeString(strings.ReplaceAll(epoch, "-", ""))
	if err != nil || len(namespace) != 16 {
		sum := sha256.Sum256([]byte(epoch))
		namespace = sum[:16]
	}
	hash := sha1.New()
	_, _ = hash.Write(namespace)
	_, _ = hash.Write([]byte(fmt.Sprintf("event:%d", sequence)))
	id := hash.Sum(nil)[:16]
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(id)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	result := value
	return &result
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum-current {
		return maximum
	}
	return current * 2
}
