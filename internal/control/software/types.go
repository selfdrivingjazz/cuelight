// Package software mirrors vendor-signed Cuelight software releases on the
// control plane and tracks which version every node runs and should run.
//
// Control never produces code: every release envelope is verified against the
// vendor keys compiled into this binary (updates.TrustedKeys) before it is
// stored, and every artifact is copied into control CAS only when its sha256
// and size match the signed manifest. Nodes verify the envelope again
// themselves, so control chooses among vendor-signed versions but cannot
// supply them.
package software

import (
	"context"
	"errors"
	"time"

	"github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

// Import sources.
const (
	SourceFeed   = "feed"
	SourceCLI    = "cli"
	SourceUpload = "upload"
)

// Update methods reported by nodes in their capability report.
const (
	MethodDesktop   = "desktop"
	MethodAppliance = "appliance"
	MethodBrowser   = "browser"
	MethodAndroid   = "android"
	MethodNone      = "none"
)

// Node software statuses.
const (
	StatusCurrent     = "current"
	StatusOutdated    = "outdated"
	StatusUpdating    = "updating"
	StatusFailed      = "failed"
	StatusUnknown     = "unknown"
	StatusUnsupported = "unsupported"
)

// Node event types the player reports about software updates.
const (
	EventUpdateFailed  = "software.update.failed"
	EventUpdateIgnored = "software.update.ignored"
)

// Artifact import outcomes.
const (
	ArtifactImported = "imported"
	ArtifactPresent  = "present"
	ArtifactMissing  = "missing"
	ArtifactRejected = "rejected"
)

var (
	ErrNotFound = errors.New("software resource not found")
	ErrInvalid  = errors.New("invalid software request")
	// ErrUnverified rejects envelopes that do not verify against the
	// compiled-in vendor keys.
	ErrUnverified = errors.New("release is not signed by a trusted Cuelight key")
	// ErrConflict rejects a second, different manifest for an imported version.
	ErrConflict = errors.New("release version already imported with a different manifest")
	// ErrArtifactMismatch rejects artifact bytes that do not match the manifest.
	ErrArtifactMismatch = errors.New("artifact does not match the signed manifest")
	// ErrDowngrade rejects a target below the version a node runs.
	ErrDowngrade = errors.New("target version is below the node's running version")
)

// Release is one imported, verified release.
type Release struct {
	Version                string
	Channel                string
	PublishedAt            time.Time
	MinimumProtocolVersion string
	KeyID                  string
	// Envelope is the exact signed envelope document.
	Envelope   []byte
	Source     string
	ImportedAt time.Time
	Artifacts  []Artifact
}

// Artifact is a manifest artifact and whether its bytes are in control CAS.
type Artifact struct {
	updates.Artifact
	Ready bool
}

// Target is the version an operator asked a node to run.
type Target struct {
	Version     string
	RequestedBy string
	RequestedAt time.Time
}

// Failure is the newest software.update.failed event of a node.
type Failure struct {
	Version string
	Error   string
	At      time.Time
}

// NodeRecord is what control knows about one node's software.
type NodeRecord struct {
	ID              string
	Name            string
	NodeClass       string
	CredentialState string
	// Report is the node's latest capability report; nil when it never sent one.
	Report  *client.CapabilityReport
	Target  *Target
	Failure *Failure
}

// FeedState is the persisted outcome of release feed polling.
type FeedState struct {
	URL           string
	LastCheckedAt *time.Time
	LastSuccessAt *time.Time
	LastError     string
	LastVersion   string
}

// Repository persists releases, targets, and feed state.
type Repository interface {
	// PutSoftwareRelease stores a verified release and its artifacts (not
	// ready). It reports false when the identical manifest was already
	// imported and ErrConflict when the version exists with other bytes.
	PutSoftwareRelease(context.Context, Release) (bool, error)
	MarkSoftwareArtifactReady(ctx context.Context, version, name string) error
	ListSoftwareReleases(context.Context) ([]Release, error)
	GetSoftwareRelease(ctx context.Context, version string) (Release, error)
	// SoftwareArtifactReady reports whether any imported release has a ready
	// artifact with this sha256.
	SoftwareArtifactReady(ctx context.Context, sha256 string) (bool, error)
	SoftwareArtifact(ctx context.Context, version, name string) (Artifact, error)
	ListSoftwareNodes(context.Context) ([]NodeRecord, error)
	SoftwareNode(ctx context.Context, nodeID string) (NodeRecord, error)
	SetSoftwareTarget(ctx context.Context, nodeID, version, actorID string) error
	ClearSoftwareTarget(ctx context.Context, nodeID, actorID string) error
	// PlayerSoftwareTarget returns the node's target version ("" for none),
	// its software generation, and its reported update method.
	PlayerSoftwareTarget(ctx context.Context, nodeID string) (version string, generation int64, updateMethod string, err error)
	SoftwareFeedState(context.Context) (FeedState, error)
	PutSoftwareFeedState(context.Context, FeedState) error
}
