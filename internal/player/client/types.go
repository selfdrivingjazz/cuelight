package client

import (
	"io"
	"time"

	"github.com/jmill/cable/internal/release"
)

type EnrollmentRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"publicKey"`
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
	Platform        string `json:"platform"`
	Architecture    string `json:"architecture"`
	ProtocolVersion string `json:"protocolVersion"`
	RendererVersion string `json:"rendererVersion"`
	ProfileID       string `json:"profileId"`
	FreeBytes       int64  `json:"freeBytes,omitempty"`
	OutputReady     bool   `json:"outputReady"`
	ClockReliable   bool   `json:"clockReliable"`
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
}

type AssignmentResult struct {
	Assignment  Assignment
	ETag        string
	NotModified bool
}

type ReleaseEnvelope = release.EnvelopeV1

type Condition struct {
	Type    string `json:"type"`
	Status  bool   `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}
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
