package client

import (
	"io"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/preview"
	"github.com/jmilldotdev/cuelight/internal/release"
)

type ScreenshotJob = preview.ScreenshotJob

type EnrollmentRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"publicKey"`
	Name      string `json:"name,omitempty"`
}

type ClientCredential struct {
	Certificate string    `json:"certificate"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type Enrollment struct {
	ClientCredential
	NodeID             string `json:"nodeId"`
	EventEpoch         string `json:"eventEpoch"`
	PublisherKeyID     string `json:"publisherKeyId"`
	PublisherPublicKey string `json:"publisherPublicKey"`
}

type CapabilityReport struct {
	Platform           string   `json:"platform"`
	Architecture       string   `json:"architecture"`
	ProtocolVersion    string   `json:"protocolVersion"`
	RendererVersion    string   `json:"rendererVersion"`
	ProfileID          string   `json:"profileId"`
	Features           []string `json:"features,omitempty"`
	AcceptedMediaTypes []string `json:"acceptedMediaTypes,omitempty"`
	FreeBytes          int64    `json:"freeBytes,omitempty"`
	OutputReady        bool     `json:"outputReady"`
	ClockReliable      bool     `json:"clockReliable"`
	// SoftwareVersion is the semver of the software the node runs.
	SoftwareVersion string `json:"softwareVersion,omitempty"`
	// Channel is the release channel the node follows (stable or beta).
	Channel string `json:"channel,omitempty"`
	// UpdateMethod is how the node installs software updates:
	// desktop, appliance, browser, android, or none.
	UpdateMethod string `json:"updateMethod,omitempty"`
	// OutputStandby reports a connected node whose shared local display is
	// currently showing another Cuelight network.
	OutputStandby bool `json:"outputStandby,omitempty"`
}

type Assignment struct {
	ID                 string                   `json:"id"`
	NodeID             string                   `json:"nodeId"`
	ReleaseID          string                   `json:"releaseId"`
	ReleaseDigest      string                   `json:"releaseDigest"`
	PresentationKind   release.PresentationKind `json:"presentationKind"`
	DeliveryMode       string                   `json:"deliveryMode"`
	ExpectedGeneration int64                    `json:"expectedGeneration"`
	DesiredGeneration  int64                    `json:"desiredGeneration"`
	RemoteInput        bool                     `json:"remoteInput"`
	ShowQR             bool                     `json:"showQr"`
	// Software is the vendor-signed version control wants the node to run;
	// absent means stay put.
	Software *SoftwareTarget `json:"software,omitempty"`
}

// SoftwareTarget points a node at a signed software release mirrored by control.
type SoftwareTarget struct {
	Version string `json:"version"`
	// ManifestURL serves the signed release envelope (mTLS for players).
	ManifestURL string `json:"manifestUrl"`
	// ArtifactsBaseURL is joined with an artifact's sha256 to download it.
	ArtifactsBaseURL string `json:"artifactsBaseUrl"`
	// APKURL and APKSHA256 are set for browser nodes in the Android shell.
	APKURL    string `json:"apkUrl,omitempty"`
	APKSHA256 string `json:"apkSha256,omitempty"`
}

type AssignmentResult struct {
	Assignment  Assignment
	ETag        string
	NotModified bool
}
type RemoteSession struct {
	ID        string    `json:"sessionId"`
	Token     string    `json:"token,omitempty"`
	RemoteURL string    `json:"remoteUrl"`
	ShowQR    bool      `json:"showQr"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type RemoteEvent struct {
	Sequence int64   `json:"sequence"`
	Type     string  `json:"type"`
	Key      string  `json:"key,omitempty"`
	Text     string  `json:"text,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	DeltaX   float64 `json:"deltaX,omitempty"`
	DeltaY   float64 `json:"deltaY,omitempty"`
	Button   string  `json:"button,omitempty"`
	State    string  `json:"state,omitempty"`
}
type RemoteEventPage struct {
	Events       []RemoteEvent `json:"events"`
	NextSequence int64         `json:"nextSequence"`
}

type ReleaseEnvelope = release.EnvelopeV1

type Condition struct {
	Type    string `json:"type"`
	Status  bool   `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// CurrentContent is the content the node renderer is actually presenting.
// It is intentionally independent of assignment intent; an off-air or stale
// observation may still identify the release that supplied the renderer.
type CurrentContent struct {
	ReleaseDigest    string                   `json:"releaseDigest"`
	PresentationKind release.PresentationKind `json:"presentationKind"`
	State            string                   `json:"state"`
	ItemID           string                   `json:"itemId,omitempty"`
	MediaID          string                   `json:"mediaId,omitempty"`
	AssetID          string                   `json:"assetId,omitempty"`
	StartedAt        *time.Time               `json:"startedAt,omitempty"`
	TransitionedAt   time.Time                `json:"transitionedAt"`
	PositionMs       *int64                   `json:"positionMs,omitempty"`
	Width            *int32                   `json:"width,omitempty"`
	Height           *int32                   `json:"height,omitempty"`
}

const (
	ContentStatePlaying = "playing"
	ContentStatePaused  = "paused"
	ContentStateOffAir  = "off_air"
	ContentStateStale   = "stale"
)

type NodeReport struct {
	EventEpoch                 string                   `json:"eventEpoch"`
	Sequence                   int64                    `json:"sequence"`
	DesiredGenerationSeen      int64                    `json:"desiredGenerationSeen"`
	ObservedGeneration         int64                    `json:"observedGeneration"`
	PresentationKind           release.PresentationKind `json:"presentationKind"`
	RenderingReleaseDigest     *string                  `json:"renderingReleaseDigest"`
	CandidateReleaseDigest     *string                  `json:"candidateReleaseDigest"`
	HealthyReleaseDigest       *string                  `json:"healthyReleaseDigest"`
	LastKnownGoodReleaseDigest *string                  `json:"lastKnownGoodReleaseDigest"`
	CurrentContent             *CurrentContent          `json:"currentContent,omitempty"`
	ReportedAt                 *time.Time               `json:"reportedAt,omitempty"`
	Conditions                 []Condition              `json:"conditions"`
}

type NodeEvent struct {
	ID            string         `json:"id"`
	Epoch         string         `json:"epoch"`
	Sequence      int64          `json:"sequence"`
	Type          string         `json:"type"`
	OccurredAt    time.Time      `json:"occurredAt"`
	CoversThrough int64          `json:"coversThrough,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

type EventBatch struct {
	Epoch  string      `json:"epoch"`
	Events []NodeEvent `json:"events"`
}

type EventAcknowledgement struct {
	Epoch               string `json:"epoch"`
	AcknowledgedThrough int64  `json:"acknowledgedThrough"`
}

type ArtifactMetadata struct {
	Size         int64
	ETag         string
	AcceptRanges string
}

type ByteRange struct {
	Start int64
	End   *int64
}

type ArtifactContent struct {
	Body         io.ReadCloser
	Size         int64
	ETag         string
	AcceptRanges string
	ContentRange string
	Partial      bool
}

// AppToken authorises an app-access web page to call the /app API, which is
// served at APIURL, for one node and media item.
type AppToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	APIURL    string    `json:"apiUrl"`
}
