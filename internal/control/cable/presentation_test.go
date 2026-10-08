package cable

import (
	"context"
	"testing"
	"time"
)

type liveRepository struct {
	Repository
	ProgrammingRepository
	deployment CableDeployment
	channels   []Channel
	blocks     map[string][]Block
	playlists  map[string]Playlist
	artifacts  map[string][]Artifact
}

func (r *liveRepository) Deployment(context.Context, string) (CableDeployment, error) {
	return r.deployment, nil
}
func (r *liveRepository) ListChannels(context.Context, string, string, int) (ChannelPage, error) {
	return ChannelPage{Items: append([]Channel(nil), r.channels...)}, nil
}
func (r *liveRepository) ListBlocks(_ context.Context, channelID, _ string, _ int) (BlockPage, error) {
	return BlockPage{Items: r.blocks[channelID]}, nil
}
func (r *liveRepository) Playlist(_ context.Context, id string) (Playlist, error) {
	playlist, ok := r.playlists[id]
	if !ok {
		return Playlist{}, ErrNotFound
	}
	return playlist, nil
}
func (r *liveRepository) CableArtifactsForAsset(_ context.Context, assetID string) ([]Artifact, error) {
	return r.artifacts[assetID], nil
}

func TestPresentationReflectsLiveAuthoredState(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 10, 0, 0, time.UTC)
	repository := &liveRepository{
		deployment: CableDeployment{ID: "dep", Version: 1, Theme: "gallery", Timezone: "America/New_York", GuideDensity: "hour", GuideHorizonSeconds: 3 * 3600},
		channels:   []Channel{{ID: "ch-2", Number: 2, Name: "Two", Enabled: true}, {ID: "ch-1", Number: 1, Name: "One", Callsign: "ONE", Enabled: true, Standby: true}},
		blocks: map[string][]Block{
			"ch-1": {
				{ID: "past", ChannelID: "ch-1", AssetID: "clip", Title: "Long gone", StartsAt: now.Add(-5 * time.Hour), EndsAt: now.Add(-4 * time.Hour)},
				{ID: "loop", ChannelID: "ch-1", PlaylistID: "pl", Title: "Loop", StartsAt: now.Add(-10 * time.Minute), EndsAt: now.Add(20 * time.Minute)},
			},
		},
		playlists: map[string]Playlist{"pl": {ID: "pl", Items: []PlaylistItem{
			{ID: "b", AssetID: "still", Position: 2, DurationSeconds: 600, Title: "Still"},
			{ID: "a", AssetID: "clip", Position: 1, DurationSeconds: 600, Title: "Clip"},
			{ID: "web", Position: 3, DurationSeconds: 600},
			{ID: "audio", AssetID: "song", Position: 4, DurationSeconds: 600},
		}}},
		artifacts: map[string][]Artifact{
			"clip":  {{AssetID: "clip", Digest: "d1", MediaType: "video/mp4", State: "ready"}},
			"still": {{AssetID: "still", Digest: "d2", MediaType: "image/png", State: "ready"}},
			"song":  {{AssetID: "song", Digest: "d3", MediaType: "audio/mpeg", State: "ready"}},
		},
	}
	builder := PresentationBuilder{Repository: repository, Programming: repository, Artifacts: repository, Now: func() time.Time { return now }}
	first, err := builder.Build(context.Background(), "dep")
	if err != nil {
		t.Fatal(err)
	}
	if first.Theme != "gallery" || first.Revision.Timezone != "America/New_York" || first.Revision.GuideDensity != "hour" || first.Bridge.State != "online" {
		t.Fatalf("presentation metadata = %+v", first)
	}
	if len(first.Channels) != 2 || first.Channels[0].ID != "ch-1" || first.Channels[0].StandbyMessage == "" {
		t.Fatalf("channels = %+v", first.Channels)
	}
	if len(first.Blocks) != 1 || first.Blocks[0].ID != "loop" || first.Blocks[0].Source != "playlist" {
		t.Fatalf("blocks outside the window must be dropped: %+v", first.Blocks)
	}
	// The 30-minute block plays clip then still then (unplayable) web and
	// audio items; only guide-playable media appear, in playlist order.
	if len(first.Occurrences) != 2 {
		t.Fatalf("occurrences = %+v", first.Occurrences)
	}
	clip, still := first.Occurrences[0], first.Occurrences[1]
	if clip.ItemID != "a" || clip.Media.Kind != "video" || clip.Media.Reference != GuideMediaPath+"clip" || !clip.StartsAt.Equal(now.Add(-10*time.Minute)) || !clip.EndsAt.Equal(now) {
		t.Fatalf("clip = %+v", clip)
	}
	if still.ItemID != "b" || still.Media.Kind != "image" || still.Media.Mime != "image/png" || !still.EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("still = %+v", still)
	}

	// Authored edits show up on the next request with a new revision digest.
	repository.channels[1].Name = "Renamed"
	repository.deployment.Version = 2
	second, err := builder.Build(context.Background(), "dep")
	if err != nil {
		t.Fatal(err)
	}
	if second.Channels[0].Name != "Renamed" || second.Revision.Digest == first.Revision.Digest {
		t.Fatalf("live edit not reflected: %+v / %s", second.Channels[0], second.Revision.Digest)
	}
	third, err := builder.Build(context.Background(), "dep")
	if err != nil || third.Revision.Digest != second.Revision.Digest {
		t.Fatalf("unchanged state must keep its digest: %s vs %s (%v)", third.Revision.Digest, second.Revision.Digest, err)
	}
}
