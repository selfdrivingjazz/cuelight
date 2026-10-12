package store

import (
	"context"
	"testing"

	"github.com/selfdrivingjazz/cuelight/internal/control/media"
)

// Creating or editing file media writes artwork_asset_id (uuid) from a string
// that may be empty; both paths must store a real asset id or NULL.
func TestPostgresAuthoredMediaArtworkRoundTrips(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-0000000000aa"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	videoID := "00000000-0000-4000-8000-000000000001"
	posterID := "00000000-0000-4000-8000-000000000002"
	secondID := "00000000-0000-4000-8000-000000000003"
	for _, asset := range []struct{ id, digest, mediaType string }{
		{videoID, "1111111111111111111111111111111111111111111111111111111111111111", "image/jpeg"},
		{posterID, "2222222222222222222222222222222222222222222222222222222222222222", "image/png"},
		{secondID, "3333333333333333333333333333333333333333333333333333333333333333", "image/jpeg"},
	} {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO assets (id, name) VALUES ($1, 'asset')`, []any{asset.id}},
			{`INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, 10, $2, 'ready')`, []any{asset.digest, asset.mediaType}},
			{`INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, []any{asset.id, asset.digest}},
		} {
			if _, err := repository.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	playback := media.Playback{Fit: "fit", Loop: true, Muted: true}

	plain, err := repository.CreateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: "create-plain"},
		media.Media{ID: "00000000-0000-4000-8000-000000000010", AssetID: videoID, Title: "Summertime Sadness.jpg", Playback: playback})
	if err != nil {
		t.Fatalf("create without artwork: %v", err)
	}
	if plain.ArtworkAssetID != "" {
		t.Fatalf("artwork = %q, want none", plain.ArtworkAssetID)
	}

	withArt, err := repository.CreateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: "create-art"},
		media.Media{ID: "00000000-0000-4000-8000-000000000011", AssetID: secondID, Title: "With artwork", ArtworkAssetID: posterID, Playback: playback})
	if err != nil {
		t.Fatalf("create with artwork: %v", err)
	}
	if withArt.ArtworkAssetID != posterID {
		t.Fatalf("artwork = %q, want %q", withArt.ArtworkAssetID, posterID)
	}

	updated, err := repository.UpdateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: "set-art"}, plain.ID, plain.Version,
		media.MediaPatch{ArtworkAssetID: &posterID})
	if err != nil {
		t.Fatalf("set artwork: %v", err)
	}
	if updated.ArtworkAssetID != posterID {
		t.Fatalf("artwork after set = %q", updated.ArtworkAssetID)
	}
	clear := ""
	cleared, err := repository.UpdateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: "clear-art"}, plain.ID, updated.Version,
		media.MediaPatch{ArtworkAssetID: &clear})
	if err != nil {
		t.Fatalf("clear artwork: %v", err)
	}
	if cleared.ArtworkAssetID != "" {
		t.Fatalf("artwork after clear = %q", cleared.ArtworkAssetID)
	}
}
