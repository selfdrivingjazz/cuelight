package release

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/media"
	canonical "github.com/jmill/cable/internal/release"
)

type catalogFixture struct {
	media     map[string]TargetMedia
	playlists map[string][]PlaylistEntry
}

func (c catalogFixture) TargetMedia(_ context.Context, id string) (TargetMedia, error) {
	value, ok := c.media[id]
	if !ok {
		return TargetMedia{}, assignment.ErrTargetNotFound
	}
	return value, nil
}

func (c catalogFixture) TargetPlaylist(_ context.Context, id string) ([]PlaylistEntry, error) {
	value, ok := c.playlists[id]
	if !ok {
		return nil, assignment.ErrTargetNotFound
	}
	return value, nil
}

func targetFixture(t *testing.T, catalog catalogFixture) TargetPublisher {
	t.Helper()
	repository := newReleaseRepositoryFixture()
	service := publishFixture(t, repository, &artifactFixture{artifacts: []Artifact{readyArtifact()}}, compatibleProfile())
	return TargetPublisher{
		Media: service, Catalog: catalog, Releases: repository, Signer: service.Signer,
		MinimumProtocolVersion: canonical.ProtocolV1, PublicOrigin: "https://control.example/",
		Now: func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
	}
}

func TestTargetPublisherResolvesCableItemsToHostedGuide(t *testing.T) {
	publisher := targetFixture(t, catalogFixture{media: map[string]TargetMedia{
		"cable": {ID: "cable", Kind: media.KindWeb, DeploymentID: "dep-1"},
	}})
	envelope, err := publisher.PublishTarget(context.Background(), "actor", "key-1", "cable", "")
	if err != nil {
		t.Fatal(err)
	}
	web, ok := envelope.Payload.WebPresentation()
	if !ok || web.URL != "https://control.example/cable/?deployment=dep-1" {
		t.Fatalf("cable item release = %#v", envelope.Payload.Presentation)
	}
	publisher.PublicOrigin = ""
	if _, err := publisher.PublishTarget(context.Background(), "actor", "key-2", "cable", ""); err == nil {
		t.Fatal("cable item published without a public origin")
	}
}

func TestTargetPublisherLoopsPlaylistsAndRequiresWebFeatureForWebItems(t *testing.T) {
	artifact := readyArtifact()
	artifact.MediaType = "image/png"
	fileOnly := []PlaylistEntry{{ID: "a", DurationSeconds: 3600, AssetID: "asset-one", Artifact: artifact}}
	mixed := append(slices.Clone(fileOnly), PlaylistEntry{ID: "w", DurationSeconds: 1800, Web: &TargetMedia{Kind: media.KindWeb, URL: "https://example.com/board"}})
	publisher := targetFixture(t, catalogFixture{playlists: map[string][]PlaylistEntry{"files": fileOnly, "mixed": mixed, "empty": nil}})

	files, err := publisher.PublishTarget(context.Background(), "actor", "files-key", "", "files")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(files.Payload.RequiredFeatures, canonical.FeatureWeb) || len(files.Payload.Presentation.MediaSchedule.Items) != 24 {
		t.Fatalf("file playlist release = features %v items %d", files.Payload.RequiredFeatures, len(files.Payload.Presentation.MediaSchedule.Items))
	}

	envelope, err := publisher.PublishTarget(context.Background(), "actor", "mixed-key", "", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	schedule := envelope.Payload.Presentation.MediaSchedule
	if !slices.Contains(envelope.Payload.RequiredFeatures, canonical.FeatureWeb) || schedule.Window.StartsAt != "2026-10-04T12:00:00Z" || schedule.Window.EndsAt != "2026-10-05T12:00:00Z" {
		t.Fatalf("mixed playlist release = features %v window %#v", envelope.Payload.RequiredFeatures, schedule.Window)
	}
	if len(schedule.Items) != 32 || schedule.Items[1].Web == nil || schedule.Items[1].Web.URL != "https://example.com/board" || schedule.Items[2].ArtifactDigest != artifact.Digest {
		t.Fatalf("mixed playlist items = %#v", schedule.Items[:3])
	}
	if !envelope.Payload.AllowsDeliveryMode(DeliveryCacheThenPlay) {
		t.Fatal("playlist release must allow cache_then_play for its file items")
	}

	if _, err := publisher.PublishTarget(context.Background(), "actor", "empty-key", "", "empty"); err != ErrPlaylistEmpty {
		t.Fatalf("empty playlist error = %v", err)
	}
}
