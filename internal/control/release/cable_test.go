package release

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	controlcable "github.com/jmill/cable/internal/control/cable"
	canonical "github.com/jmill/cable/internal/release"
)

type cableRevisionFixture struct {
	revision controlcable.CableRevision
}

func (f cableRevisionFixture) Revision(context.Context, string) (controlcable.CableRevision, error) {
	return f.revision, nil
}

type closureArtifactFixture map[string]Artifact

func (f closureArtifactFixture) ReleaseArtifactByDigest(_ context.Context, digest string) (Artifact, error) {
	artifact, ok := f[digest]
	if !ok {
		return Artifact{}, errors.New("artifact not found")
	}
	return artifact, nil
}

func TestCablePublisherBuildsSignedExactClosure(t *testing.T) {
	shell := strings.Repeat("a", 64)
	revisionDigest := strings.Repeat("b", 64)
	media := strings.Repeat("c", 64)
	revision := controlcable.CableRevision{
		ID: revisionDigest, Digest: revisionDigest, ShellBuildDigest: shell,
		Entrypoint: "index.html", BridgeProtocol: "1", ArtifactDigests: []string{media, shell, revisionDigest},
	}
	artifacts := closureArtifactFixture{
		shell:          {Digest: shell, Size: 4096, MediaType: "application/vnd.cable.shell+tar", State: ArtifactReady},
		revisionDigest: {Digest: revisionDigest, Size: 1024, MediaType: "application/vnd.cable.revision+json", State: ArtifactReady},
		media:          {Digest: media, Size: 2048, MediaType: "video/mp4", State: ArtifactReady},
	}
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	signer, err := NewEd25519Signer("publisher-one", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	repository := newReleaseRepositoryFixture()
	publisher := CablePublisher{Releases: repository, Revisions: cableRevisionFixture{revision: revision}, Artifacts: artifacts, Signer: signer, MinimumProtocolVersion: canonical.ProtocolV1}
	mutation := Mutation{ActorID: "actor-one", IdempotencyKey: "publish-cable-one"}
	result, err := publisher.Publish(context.Background(), mutation, CablePublishInput{RevisionID: revisionDigest})
	if err != nil {
		t.Fatal(err)
	}
	presentation := result.Envelope.Payload.Presentation
	if presentation.Kind != canonical.PresentationCable || presentation.Cable == nil {
		t.Fatalf("presentation = %#v", presentation)
	}
	if !result.Envelope.Payload.AllowsDeliveryMode(DeliveryCacheThenPlay) || result.Envelope.Payload.AllowsDeliveryMode(DeliveryStream) {
		t.Fatalf("Cable delivery modes are unsafe: %#v", result.Envelope.Payload)
	}
	if len(presentation.Cable.ArtifactClosure) != 3 || presentation.Cable.ArtifactClosure[0] != shell || presentation.Cable.ArtifactClosure[1] != revisionDigest || presentation.Cable.ArtifactClosure[2] != media {
		t.Fatalf("closure = %#v", presentation.Cable.ArtifactClosure)
	}
	replayed, err := publisher.Publish(context.Background(), mutation, CablePublishInput{RevisionID: revisionDigest})
	if err != nil || !replayed.Replayed || replayed.Envelope.Digest != result.Envelope.Digest {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
}

func TestCablePublisherRejectsIncompleteRevisionClosure(t *testing.T) {
	shell := strings.Repeat("a", 64)
	revisionDigest := strings.Repeat("b", 64)
	publisher := CablePublisher{
		Releases: newReleaseRepositoryFixture(),
		Revisions: cableRevisionFixture{revision: controlcable.CableRevision{
			ID: revisionDigest, Digest: revisionDigest, ShellBuildDigest: shell,
			Entrypoint: "index.html", BridgeProtocol: "1", ArtifactDigests: []string{shell},
		}},
		Artifacts: closureArtifactFixture{}, Signer: signerFixture(t), MinimumProtocolVersion: canonical.ProtocolV1,
	}
	_, err := publisher.Publish(context.Background(), Mutation{ActorID: "actor", IdempotencyKey: "missing-revision"}, CablePublishInput{RevisionID: revisionDigest})
	if !errors.Is(err, ErrCableRevisionInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func signerFixture(t *testing.T) Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	signer, err := NewEd25519Signer("publisher-one", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
