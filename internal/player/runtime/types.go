package runtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jmilldotdev/cuelight/internal/media/localcas"
	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/player/renderer"
	playerstate "github.com/jmilldotdev/cuelight/internal/player/state"
	"github.com/jmilldotdev/cuelight/internal/release"
)

const (
	DeliveryStream        = "stream"
	DeliveryCacheThenPlay = "cache_then_play"

	StatePending    = "PENDING"
	StateFetching   = "FETCHING"
	StatePrepared   = "PREPARED"
	StateRendering  = "RENDERING"
	StateProbation  = "PROBATION"
	StateCommitting = "COMMITTING"
	StateHealthy    = "HEALTHY"
	StateRolledBack = "ROLLED_BACK"
	StateSafe       = "SAFE"
)

var (
	ErrInvalidAssignment    = errors.New("invalid desired assignment")
	ErrReleaseBinding       = errors.New("release does not exactly match assignment")
	ErrIncompatibleRelease  = errors.New("release is incompatible with player profile")
	ErrInsufficientCapacity = errors.New("insufficient cache capacity")
	ErrUnsafeStreamSource   = errors.New("stream source is not loopback-only")
	ErrActivationFailed     = errors.New("release activation failed")
	ErrSafeMode             = errors.New("player entered safe mode")
)

type Controller interface {
	PutCapabilities(context.Context, playerclient.CapabilityReport, string) error
	GetAssignment(context.Context, string, time.Duration) (playerclient.AssignmentResult, error)
	FetchRelease(context.Context, string) (playerclient.ReleaseEnvelope, error)
	Report(context.Context, playerclient.NodeReport, string) (bool, error)
	AppendEvents(context.Context, playerclient.EventBatch, string) (playerclient.EventAcknowledgement, error)
	HeadArtifact(context.Context, string) (playerclient.ArtifactMetadata, error)
	GetArtifact(context.Context, string, *playerclient.ByteRange, string) (playerclient.ArtifactContent, error)
}
type RemoteController interface {
	CreateRemoteSession(context.Context, int64) (playerclient.RemoteSession, error)
	PollRemoteEvents(context.Context, string, int64, time.Duration) (playerclient.RemoteEventPage, error)
}

// Journal is deliberately the exact durable surface supplied by player/state.Store.
type Journal interface {
	AcceptGeneration(uint64, string) (bool, error)
	RecordTransition(uint64, string, string, string) error
	Attempt(uint64) (playerstate.Attempt, error)
	SetEventProgress(string, uint64, uint64) error
	EventProgress() (string, uint64, uint64, error)
}

type CAS interface {
	Put(io.Reader, string) (localcas.Object, error)
	Stat(string) (localcas.Object, error)
	Reconcile(map[string]bool, time.Duration, time.Time) (localcas.ReconcileResult, error)
	RemovePartial(string) error
}

type Capacity interface {
	AvailableBytes(context.Context) (int64, error)
}

// StreamSource creates a short-lived renderer URL and a revoker. Implementations
// must bind it to a loopback listener; Reconciler independently enforces that.
type StreamSource interface {
	Open(context.Context, string, time.Duration) (string, func(), error)
	Renew(context.Context, string, time.Duration) error
}

type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

type Config struct {
	NodeID             string
	Capabilities       playerclient.CapabilityReport
	EventEpoch         string
	PublisherKeyID     string
	PublisherPublicKey ed25519.PublicKey
	Features           []string
	PresentationMode   release.PresentationKind
	AcceptedMediaTypes []string
	Probation          time.Duration
	PollWait           time.Duration
	ReconnectMinimum   time.Duration
	ReconnectMaximum   time.Duration
	PartialGrace       time.Duration
	StreamTTL          time.Duration
	MaxEvents          int
	MaxEventBatch      int
	MaxConditions      int
	MaxMessageRunes    int
}
type Dependencies struct {
	Controller      Controller
	Journal         Journal
	CAS             CAS
	ScreenshotCache interface {
		Put(io.Reader, string) (localcas.Object, error)
		Open(string) (io.ReadCloser, error)
	}
	Capacity Capacity
	Streamer StreamSource
	Renderer renderer.Adapter
	Clock    Clock
}

type Snapshot struct {
	DesiredGenerationSeen uint64
	ObservedGeneration    uint64
	PresentationKind      release.PresentationKind
	RenderingDigest       string
	CandidateDigest       string
	HealthyDigest         string
	LastKnownGoodDigest   string
	CurrentContent        *playerclient.CurrentContent
	Safe                  bool
	ControllerAvailable   bool
}

type RuntimeError struct {
	Kind       error
	Generation uint64
	Operation  string
	Err        error
}

func (e *RuntimeError) Error() string {
	if e.Generation == 0 {
		return fmt.Sprintf("player runtime %s: %v", e.Operation, e.Kind)
	}
	return fmt.Sprintf("player runtime generation %d %s: %v", e.Generation, e.Operation, e.Kind)
}

func (e *RuntimeError) Unwrap() error {
	if e.Err != nil {
		return errors.Join(e.Kind, e.Err)
	}
	return e.Kind
}
