package playerapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/player/client"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
)

var (
	ErrUnauthorized       = errors.New("player identity is not authorized")
	ErrForbidden          = errors.New("player is not authorized for this resource")
	ErrNotFound           = errors.New("player resource not found")
	ErrInvalidRequest     = errors.New("invalid player request")
	ErrBodyTooLarge       = errors.New("player request body is too large")
	ErrBatchTooLarge      = errors.New("player event batch is too large")
	ErrWaitTooLong        = errors.New("assignment wait is too long")
	ErrEpochMismatch      = errors.New("event epoch does not match current epoch")
	ErrEventConflict      = errors.New("event conflicts with an existing event")
	ErrEventGap           = errors.New("event sequence has a gap")
	ErrRangeUnsatisfiable = errors.New("artifact range is unsatisfiable")
)

type Repository interface {
	PutCapabilities(context.Context, string, client.CapabilityReport) error
	CurrentAssignment(context.Context, string) (assignment.Assignment, bool, error)
	// WaitForAssignment returns once the node's assignment generation exceeds
	// generation or its software generation exceeds softwareGeneration.
	WaitForAssignment(ctx context.Context, nodeID string, generation, softwareGeneration int64, wait time.Duration) error
	AssignedRelease(context.Context, string, string) (assignment.Assignment, bool, error)
	AuthorizeArtifact(context.Context, string, string) (bool, error)
	RecordReport(context.Context, string, client.NodeReport) (bool, error)
	AppendEvents(context.Context, string, client.EventBatch) (client.EventAcknowledgement, error)
}

// ReleaseLoader loads an immutable release envelope after Repository has
// authorized the requesting node for that release.
type ReleaseLoader interface {
	LoadRelease(context.Context, string) (canonical.EnvelopeV1, error)
}

// SoftwareResolver supplies Assignment.software and the node's software
// generation, which changes whenever its target is set or cleared.
type SoftwareResolver interface {
	PlayerTarget(ctx context.Context, nodeID string) (*client.SoftwareTarget, int64, error)
}

type ArtifactStat struct {
	Digest string
	Size   int64
}

// CASReader is deliberately read-only. The control plane never accepts an
// artifact body from a player.
type CASReader interface {
	Stat(context.Context, string) (ArtifactStat, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type Identity func(*http.Request) (string, error)

// RevocationChecker is used by the default identity resolver. A custom
// Identity may be supplied for tests or a server that already verified mTLS.
type RevocationChecker interface {
	Revoked(nodeID, serial string) bool
}

type Limits struct {
	MaxBodyBytes int64
	MaxEvents    int
	MaxWait      time.Duration
}

func (l Limits) normalized() Limits {
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = 4 << 20
	}
	if l.MaxEvents <= 0 {
		l.MaxEvents = 100
	}
	if l.MaxWait <= 0 {
		l.MaxWait = 30 * time.Second
	}
	return l
}

type Service struct {
	Repository    Repository
	ReleaseLoader ReleaseLoader
	CASReader     CASReader
	// Software is optional; without it assignments never carry software.
	Software          SoftwareResolver
	Identity          Identity
	RevocationChecker RevocationChecker
	Limits            Limits
}

func (s *Service) configured() error {
	if s == nil || s.Repository == nil || s.ReleaseLoader == nil || s.CASReader == nil {
		return errors.New("player API is not configured")
	}
	return nil
}

type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}
