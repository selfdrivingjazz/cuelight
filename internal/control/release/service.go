package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	canonical "github.com/jmilldotdev/cuelight/internal/release"
)

const (
	ArtifactReady       = "ready"
	ArtifactQuarantined = "quarantined"
	ArtifactMissing     = "missing"

	DeliveryStream        = "stream"
	DeliveryCacheThenPlay = "cache_then_play"
)

var (
	ErrArtifactMissing      = errors.New("release artifact is missing")
	ErrArtifactQuarantined  = errors.New("release artifact is quarantined")
	ErrArtifactIncompatible = errors.New("release artifact is incompatible with the playback profile")
	ErrIdempotencyConflict  = errors.New("idempotency key was already used with a different request")
	ErrInvalidMutation      = errors.New("actor and idempotency key are required")
	ErrSignerContract       = errors.New("signer returned an invalid release envelope")
)

// Mutation identifies the actor and idempotency scope of a publish operation.
type Mutation struct {
	ActorID        string
	IdempotencyKey string
}

type PublishInput struct {
	Playback canonical.PlaybackPayload
}

// Artifact is the catalog view needed to construct an immutable release.
type Artifact struct {
	Digest    string
	Size      int64
	MediaType string
	State     string
}

// Profile supplies the release protocol and renderer requirements selected for
// a playback request. Empty compatibility lists mean that the profile does not
// further restrict that dimension.
type Profile struct {
	MinimumProtocolVersion string
	RequiredFeatures       []string
	AcceptedMediaTypes     []string
	SupportedDeliveryModes []string
}

type ArtifactRepository interface {
	ArtifactsForAsset(context.Context, string) ([]Artifact, error)
}

type ProfileRepository interface {
	ProfileForPlayback(context.Context, canonical.PlaybackPayload) (Profile, error)
}

type Signer interface {
	Sign(context.Context, string, canonical.PayloadV1) (canonical.EnvelopeV1, error)
}

// Publication is passed to Repository.Publish so request replay can be
// decided atomically before mutable catalog and profile state is consulted.
type Publication struct {
	ActorID        string
	IdempotencyKey string
	RequestDigest  string
}

type PreparedPublication struct {
	Payload canonical.PayloadV1
	Digest  string
}

type PublishResult struct {
	Envelope canonical.EnvelopeV1
	Replayed bool
}

// Repository owns the transaction containing idempotency lookup, digest
// deduplication, release ID allocation, insertion, and idempotency recording.
// It first resolves an idempotency replay, then calls prepare at most once. If
// the prepared digest already exists it records and returns that release
// without calling create. Otherwise it calls create at most once with the new
// release ID. A request-hash mismatch must return ErrIdempotencyConflict.
type Repository interface {
	Publish(
		context.Context,
		Publication,
		func() (PreparedPublication, error),
		func(releaseID string, prepared PreparedPublication) (canonical.EnvelopeV1, error),
	) (PublishResult, error)
}

type Service struct {
	Releases  Repository
	Artifacts ArtifactRepository
	Profiles  ProfileRepository
	Signer    Signer
}

func (s Service) Publish(ctx context.Context, mutation Mutation, input PublishInput) (PublishResult, error) {
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return PublishResult{}, ErrInvalidMutation
	}
	if s.Releases == nil || s.Artifacts == nil || s.Profiles == nil || s.Signer == nil {
		return PublishResult{}, errors.New("release publish service is not configured")
	}

	requestDigest, err := RequestDigest(input)
	if err != nil {
		return PublishResult{}, err
	}
	publication := Publication{
		ActorID:        mutation.ActorID,
		IdempotencyKey: mutation.IdempotencyKey,
		RequestDigest:  requestDigest,
	}
	return signAndPublish(ctx, s.Releases, s.Signer, publication, func() (PreparedPublication, error) {
		return s.prepare(ctx, input)
	})
}

func (s Service) prepare(ctx context.Context, input PublishInput) (PreparedPublication, error) {
	artifacts, err := s.Artifacts.ArtifactsForAsset(ctx, input.Playback.AssetID)
	if err != nil {
		return PreparedPublication{}, err
	}
	if len(artifacts) == 0 {
		return PreparedPublication{}, &ArtifactError{AssetID: input.Playback.AssetID, State: ArtifactMissing, Err: ErrArtifactMissing}
	}
	profile, err := s.Profiles.ProfileForPlayback(ctx, input.Playback)
	if err != nil {
		return PreparedPublication{}, err
	}

	canonicalArtifacts := make([]canonical.Artifact, len(artifacts))
	for index, artifact := range artifacts {
		if err := validateArtifact(input.Playback.AssetID, artifact, profile); err != nil {
			return PreparedPublication{}, err
		}
		canonicalArtifacts[index] = canonical.Artifact{
			Digest:    artifact.Digest,
			Size:      artifact.Size,
			MediaType: artifact.MediaType,
		}
	}
	if err := validateDeliveryModes(input.Playback.AllowedDeliveryMode, profile.SupportedDeliveryModes); err != nil {
		return PreparedPublication{}, err
	}

	payload := canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: profile.MinimumProtocolVersion,
		Presentation: canonical.Presentation{
			Kind:  canonical.PresentationMedia,
			Media: &canonical.MediaPresentation{Playback: input.Playback},
		},
		Artifacts:        canonicalArtifacts,
		RequiredFeatures: slices.Clone(profile.RequiredFeatures),
	}
	digest, _, err := canonical.Digest(payload)
	if err != nil {
		return PreparedPublication{}, fmt.Errorf("build release payload: %w", err)
	}
	return PreparedPublication{Payload: payload, Digest: digest}, nil
}

// RequestDigest hashes only the normalized API request body. Repository or
// profile state changes therefore cannot silently turn a replay into a
// different request.
func RequestDigest(input PublishInput) (string, error) {
	playback := input.Playback
	playback.AllowedDeliveryMode = slices.Clone(playback.AllowedDeliveryMode)
	slices.Sort(playback.AllowedDeliveryMode)
	playback.AllowedDeliveryMode = slices.Compact(playback.AllowedDeliveryMode)
	body, err := json.Marshal(PublishInput{Playback: playback})
	if err != nil {
		return "", fmt.Errorf("encode publish request: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

type ArtifactError struct {
	AssetID string
	Digest  string
	State   string
	Err     error
}

func (e *ArtifactError) Error() string {
	if e.Digest == "" {
		return fmt.Sprintf("asset %q: %v", e.AssetID, e.Err)
	}
	return fmt.Sprintf("asset %q artifact %q (%s): %v", e.AssetID, e.Digest, e.State, e.Err)
}

func (e *ArtifactError) Unwrap() error { return e.Err }

func validateArtifact(assetID string, artifact Artifact, profile Profile) error {
	switch artifact.State {
	case ArtifactReady:
	case ArtifactMissing:
		return &ArtifactError{AssetID: assetID, Digest: artifact.Digest, State: artifact.State, Err: ErrArtifactMissing}
	case ArtifactQuarantined:
		return &ArtifactError{AssetID: assetID, Digest: artifact.Digest, State: artifact.State, Err: ErrArtifactQuarantined}
	default:
		return &ArtifactError{AssetID: assetID, Digest: artifact.Digest, State: artifact.State, Err: ErrArtifactMissing}
	}
	if len(profile.AcceptedMediaTypes) != 0 && !slices.Contains(profile.AcceptedMediaTypes, artifact.MediaType) {
		return &ArtifactError{AssetID: assetID, Digest: artifact.Digest, State: artifact.State, Err: ErrArtifactIncompatible}
	}
	return nil
}

func validateDeliveryModes(requested, supported []string) error {
	if len(supported) == 0 {
		return nil
	}
	for _, mode := range requested {
		if !slices.Contains(supported, mode) {
			return fmt.Errorf("%w: delivery mode %q", ErrArtifactIncompatible, mode)
		}
	}
	return nil
}

func validateEnvelope(envelope canonical.EnvelopeV1, expectedID, expectedDigest string) error {
	if envelope.ID == "" || envelope.ID != expectedID || envelope.KeyID == "" || envelope.Signature == "" {
		return ErrSignerContract
	}
	digest, _, err := canonical.Digest(envelope.Payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignerContract, err)
	}
	if envelope.Digest != expectedDigest || digest != expectedDigest {
		return ErrSignerContract
	}
	return nil
}

// Ed25519Signer adapts the canonical release signer to the publish service.
type Ed25519Signer struct {
	keyID      string
	privateKey ed25519.PrivateKey
}

func NewEd25519Signer(keyID string, privateKey ed25519.PrivateKey) (*Ed25519Signer, error) {
	if keyID == "" || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("valid publisher key ID and Ed25519 private key are required")
	}
	return &Ed25519Signer{keyID: keyID, privateKey: slices.Clone(privateKey)}, nil
}

func (s *Ed25519Signer) Sign(_ context.Context, releaseID string, payload canonical.PayloadV1) (canonical.EnvelopeV1, error) {
	return canonical.Sign(releaseID, s.keyID, payload, s.privateKey)
}
