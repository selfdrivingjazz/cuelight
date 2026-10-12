package release

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	canonical "github.com/selfdrivingjazz/cuelight/internal/release"
)

type artifactFixture struct {
	artifacts []Artifact
	err       error
	calls     int
}

func (f *artifactFixture) ArtifactsForAsset(context.Context, string) ([]Artifact, error) {
	f.calls++
	return f.artifacts, f.err
}

type profileFixture struct {
	profile Profile
}

func (f profileFixture) ProfileForPlayback(context.Context, canonical.PlaybackPayload) (Profile, error) {
	return f.profile, nil
}

type publicationRecord struct {
	requestDigest string
	envelope      canonical.EnvelopeV1
}

type releaseRepositoryFixture struct {
	next       int
	byRequest  map[string]publicationRecord
	byDigest   map[string]canonical.EnvelopeV1
	createCall int
}

func newReleaseRepositoryFixture() *releaseRepositoryFixture {
	return &releaseRepositoryFixture{
		byRequest: make(map[string]publicationRecord),
		byDigest:  make(map[string]canonical.EnvelopeV1),
	}
}

func (r *releaseRepositoryFixture) Publish(
	_ context.Context,
	publication Publication,
	prepare func() (PreparedPublication, error),
	create func(string, PreparedPublication) (canonical.EnvelopeV1, error),
) (PublishResult, error) {
	key := publication.ActorID + "\x00" + publication.IdempotencyKey
	if record, found := r.byRequest[key]; found {
		if record.requestDigest != publication.RequestDigest {
			return PublishResult{}, ErrIdempotencyConflict
		}
		return PublishResult{Envelope: record.envelope, Replayed: true}, nil
	}
	prepared, err := prepare()
	if err != nil {
		return PublishResult{}, err
	}
	if envelope, found := r.byDigest[prepared.Digest]; found {
		r.byRequest[key] = publicationRecord{requestDigest: publication.RequestDigest, envelope: envelope}
		return PublishResult{Envelope: envelope, Replayed: true}, nil
	}
	r.next++
	r.createCall++
	envelope, err := create(fmt.Sprintf("release-%d", r.next), prepared)
	if err != nil {
		return PublishResult{}, err
	}
	r.byDigest[prepared.Digest] = envelope
	r.byRequest[key] = publicationRecord{requestDigest: publication.RequestDigest, envelope: envelope}
	return PublishResult{Envelope: envelope}, nil
}

func TestPublishDeterministicRepublishAndReplay(t *testing.T) {
	repository := newReleaseRepositoryFixture()
	artifacts := &artifactFixture{artifacts: []Artifact{readyArtifact()}}
	service := publishFixture(t, repository, artifacts, compatibleProfile())
	input := PublishInput{Playback: playbackFixture()}

	first, err := service.Publish(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "request-one"}, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Publish(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "request-two"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Envelope, second.Envelope) || !second.Replayed || repository.createCall != 1 {
		t.Fatalf("republish was not deterministically deduplicated: %#v %#v", first, second)
	}

	artifacts.artifacts[0].State = ArtifactQuarantined
	replayed, err := service.Publish(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: "request-one"}, input)
	if err != nil {
		t.Fatalf("recorded replay consulted mutable artifact state: %v", err)
	}
	if !reflect.DeepEqual(replayed.Envelope, first.Envelope) || artifacts.calls != 2 {
		t.Fatalf("unexpected replay: %#v, artifact calls %d", replayed, artifacts.calls)
	}
}

func TestPublishRejectsUnavailableAndIncompatibleArtifacts(t *testing.T) {
	tests := []struct {
		name      string
		artifacts []Artifact
		profile   Profile
		want      error
	}{
		{name: "missing catalog entry", profile: compatibleProfile(), want: ErrArtifactMissing},
		{name: "missing artifact", artifacts: []Artifact{{Digest: strings.Repeat("a", 64), State: ArtifactMissing}}, profile: compatibleProfile(), want: ErrArtifactMissing},
		{name: "quarantined artifact", artifacts: []Artifact{{Digest: strings.Repeat("a", 64), State: ArtifactQuarantined}}, profile: compatibleProfile(), want: ErrArtifactQuarantined},
		{name: "incompatible media type", artifacts: []Artifact{readyArtifact()}, profile: Profile{MinimumProtocolVersion: canonical.ProtocolV1, AcceptedMediaTypes: []string{"image/png"}}, want: ErrArtifactIncompatible},
		{name: "incompatible delivery mode", artifacts: []Artifact{readyArtifact()}, profile: Profile{MinimumProtocolVersion: canonical.ProtocolV1, AcceptedMediaTypes: []string{"video/mp4"}, SupportedDeliveryModes: []string{DeliveryCacheThenPlay}}, want: ErrArtifactIncompatible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := publishFixture(t, newReleaseRepositoryFixture(), &artifactFixture{artifacts: test.artifacts}, test.profile)
			_, err := service.Publish(context.Background(), Mutation{ActorID: "actor-one", IdempotencyKey: test.name}, PublishInput{Playback: playbackFixture()})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestPublishSameKeyDifferentBodyConflicts(t *testing.T) {
	repository := newReleaseRepositoryFixture()
	service := publishFixture(t, repository, &artifactFixture{artifacts: []Artifact{readyArtifact()}}, compatibleProfile())
	mutation := Mutation{ActorID: "actor-one", IdempotencyKey: "request-one"}
	input := PublishInput{Playback: playbackFixture()}
	if _, err := service.Publish(context.Background(), mutation, input); err != nil {
		t.Fatal(err)
	}
	input.Playback.Volume++
	if _, err := service.Publish(context.Background(), mutation, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v, want idempotency conflict", err)
	}
}

func publishFixture(t *testing.T, repository Repository, artifacts ArtifactRepository, profile Profile) Service {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	signer, err := NewEd25519Signer("publisher-one", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return Service{
		Releases:  repository,
		Artifacts: artifacts,
		Profiles:  profileFixture{profile: profile},
		Signer:    signer,
	}
}

func playbackFixture() canonical.PlaybackPayload {
	return canonical.PlaybackPayload{
		AssetID:             "asset-one",
		Fit:                 "fit",
		Loop:                true,
		Muted:               false,
		Volume:              80,
		Orientation:         0,
		AllowedDeliveryMode: []string{DeliveryStream},
	}
}

func readyArtifact() Artifact {
	return Artifact{
		Digest:    strings.Repeat("a", 64),
		Size:      1024,
		MediaType: "video/mp4",
		State:     ArtifactReady,
	}
}

func compatibleProfile() Profile {
	return Profile{
		MinimumProtocolVersion: canonical.ProtocolV1,
		RequiredFeatures:       []string{"mpv"},
		AcceptedMediaTypes:     []string{"video/mp4"},
		SupportedDeliveryModes: []string{DeliveryStream, DeliveryCacheThenPlay},
	}
}
