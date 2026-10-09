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
		MinimumProtocolVersion: canonical.ProtocolV1,
		Now:                    func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
	}
}

func TestTargetPublisherMarksAppAccessWebMedia(t *testing.T) {
	publisher := targetFixture(t, catalogFixture{media: map[string]TargetMedia{
		"page": {ID: "page", Kind: media.KindWeb, URL: "https://example.com/board"},
		"app":  {ID: "app", Kind: media.KindWeb, URL: "https://apps.example/guide", AppAccess: true},
	}})
	plain, err := publisher.PublishTarget(context.Background(), "actor", "key-1", "page", "")
	if err != nil {
		t.Fatal(err)
	}
	if web, ok := plain.Payload.WebPresentation(); !ok || web.URL != "https://example.com/board" || web.AppAccess || web.MediaID != "" {
		t.Fatalf("plain web release = %#v", plain.Payload.Presentation)
	}
	app, err := publisher.PublishTarget(context.Background(), "actor", "key-2", "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if web, ok := app.Payload.WebPresentation(); !ok || web.URL != "https://apps.example/guide" || !web.AppAccess || web.MediaID != "app" {
		t.Fatalf("app web release = %#v", app.Payload.Presentation)
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
