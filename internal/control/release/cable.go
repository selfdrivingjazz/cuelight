package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	controlcable "github.com/jmill/cable/internal/control/cable"
	canonical "github.com/jmill/cable/internal/release"
)

var ErrCableRevisionInvalid = errors.New("Cable revision is not publishable")

type CablePublishInput struct {
	RevisionID string `json:"revisionId"`
}

type CableRevisionRepository interface {
	Revision(context.Context, string) (controlcable.CableRevision, error)
}

type ClosureArtifactRepository interface {
	ReleaseArtifactByDigest(context.Context, string) (Artifact, error)
}

type CablePublisher struct {
	Releases               Repository
	Revisions              CableRevisionRepository
	Artifacts              ClosureArtifactRepository
	Signer                 Signer
	MinimumProtocolVersion string
	RequiredFeatures       []string
}

func (p CablePublisher) Publish(ctx context.Context, mutation Mutation, input CablePublishInput) (PublishResult, error) {
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return PublishResult{}, ErrInvalidMutation
	}
	if input.RevisionID == "" {
		return PublishResult{}, fmt.Errorf("%w: revisionId is required", ErrCableRevisionInvalid)
	}
	if p.Releases == nil || p.Revisions == nil || p.Artifacts == nil || p.Signer == nil || p.MinimumProtocolVersion == "" {
		return PublishResult{}, errors.New("Cable release publish service is not configured")
	}
	requestDigest, err := cableRequestDigest(input)
	if err != nil {
		return PublishResult{}, err
	}
	publication := Publication{ActorID: mutation.ActorID, IdempotencyKey: mutation.IdempotencyKey, RequestDigest: requestDigest}
	result, err := p.Releases.Publish(ctx, publication, func() (PreparedPublication, error) {
		return p.prepare(ctx, input.RevisionID)
	}, func(releaseID string, prepared PreparedPublication) (canonical.EnvelopeV1, error) {
		if releaseID == "" {
			return canonical.EnvelopeV1{}, fmt.Errorf("%w: empty release ID", ErrSignerContract)
		}
		envelope, signErr := p.Signer.Sign(ctx, releaseID, prepared.Payload)
		if signErr != nil {
			return canonical.EnvelopeV1{}, signErr
		}
		if validateErr := validateEnvelope(envelope, releaseID, prepared.Digest); validateErr != nil {
			return canonical.EnvelopeV1{}, validateErr
		}
		return envelope, nil
	})
	if err != nil {
		return PublishResult{}, err
	}
	if err := validateEnvelope(result.Envelope, result.Envelope.ID, result.Envelope.Digest); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (p CablePublisher) prepare(ctx context.Context, revisionID string) (PreparedPublication, error) {
	revision, err := p.Revisions.Revision(ctx, revisionID)
	if err != nil {
		return PreparedPublication{}, err
	}
	if revision.ID != revisionID || revision.Digest == "" || revision.ShellBuildDigest == "" || revision.Entrypoint == "" || revision.BridgeProtocol == "" || len(revision.Errors) != 0 {
		return PreparedPublication{}, fmt.Errorf("%w: revision identity, shell, bridge, or validation state is invalid", ErrCableRevisionInvalid)
	}
	closure := slices.Clone(revision.ArtifactDigests)
	slices.Sort(closure)
	closure = slices.Compact(closure)
	if !slices.Contains(closure, revision.Digest) || !slices.Contains(closure, revision.ShellBuildDigest) {
		return PreparedPublication{}, fmt.Errorf("%w: revision and shell artifacts must be in the closure", ErrCableRevisionInvalid)
	}
	artifacts := make([]canonical.Artifact, len(closure))
	for index, digest := range closure {
		artifact, lookupErr := p.Artifacts.ReleaseArtifactByDigest(ctx, digest)
		if lookupErr != nil {
			return PreparedPublication{}, lookupErr
		}
		if validateErr := validateArtifact(revisionID, artifact, Profile{}); validateErr != nil {
			return PreparedPublication{}, validateErr
		}
		artifacts[index] = canonical.Artifact{Digest: artifact.Digest, Size: artifact.Size, MediaType: artifact.MediaType}
	}
	features := slices.Clone(p.RequiredFeatures)
	features = append(features, "chromium")
	slices.Sort(features)
	features = slices.Compact(features)
	payload := canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: p.MinimumProtocolVersion,
		Presentation: canonical.Presentation{Kind: canonical.PresentationCable, Cable: &canonical.CablePresentation{
			ShellBuildDigest:       revision.ShellBuildDigest,
			RevisionArtifactDigest: revision.Digest,
			Entrypoint:             revision.Entrypoint,
			BridgeProtocol:         revision.BridgeProtocol,
			ArtifactClosure:        closure,
		}},
		Artifacts:        artifacts,
		RequiredFeatures: features,
	}
	digest, _, err := canonical.Digest(payload)
	if err != nil {
		return PreparedPublication{}, fmt.Errorf("build Cable release payload: %w", err)
	}
	return PreparedPublication{Payload: payload, Digest: digest}, nil
}

func cableRequestDigest(input CablePublishInput) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode Cable publish request: %w", err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}
