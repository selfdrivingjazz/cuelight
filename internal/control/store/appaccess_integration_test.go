package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/appaccess"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/playlist"
)

func TestPostgresAppTokensFollowAssignmentAndAppAccess(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000031"
	nodeID := "00000000-0000-4000-8000-000000000032"
	otherNodeID := "00000000-0000-4000-8000-000000000033"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{nodeID, otherNodeID} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, $2, 'active', $3)`, id, "screen "+id[len(id)-2:], id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO node_credentials (node_id, serial, not_after) VALUES ($1, 'serial', now() + interval '1 day')`, id); err != nil {
			t.Fatal(err)
		}
	}
	app, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000034", URL: "https://apps.example/guide", Title: "Guide", AppAccess: true})
	if err != nil || !app.AppAccess {
		t.Fatalf("app media = %#v, %v", app, err)
	}
	page, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000035", URL: "https://example.com/board", Title: "Board"})
	if err != nil || page.AppAccess {
		t.Fatalf("plain media = %#v, %v", page, err)
	}

	service := &appaccess.Service{Repository: repository}
	if _, err := service.Mint(ctx, nodeID, app.ID); !errors.Is(err, appaccess.ErrForbidden) {
		t.Fatalf("unassigned mint error = %v", err)
	}
	releaseDigest := strings.Repeat("e", 64)
	releaseID := "00000000-0000-4000-8000-000000000036"
	if _, err := db.ExecContext(ctx, `INSERT INTO releases (id, digest, key_id, signature, payload, created_by) VALUES ($1, $2, 'k', '\x00', '{}', $3)`, releaseID, releaseDigest, actorID); err != nil {
		t.Fatal(err)
	}
	assign := func(node string, generation int, mediaID string) {
		t.Helper()
		id := fmt.Sprintf("00000000-0000-4000-8001-%012d", generation*10+len(node)%7+int(node[len(node)-1]))
		if _, err := db.ExecContext(ctx, `INSERT INTO assignments (id, node_id, desired_generation, release_id, release_digest, delivery_mode, created_by, media_id) VALUES ($1, $2, $3, $4, $5, 'stream', $6, $7)`, id, node, generation, releaseID, releaseDigest, actorID, sql.NullString{String: mediaID, Valid: mediaID != ""}); err != nil {
			t.Fatal(err)
		}
	}
	assign(otherNodeID, 1, page.ID)
	if _, err := service.Mint(ctx, otherNodeID, page.ID); !errors.Is(err, appaccess.ErrForbidden) {
		t.Fatalf("plain web media mint error = %v", err)
	}
	assign(nodeID, 1, app.ID)
	if _, err := service.Mint(ctx, otherNodeID, app.ID); !errors.Is(err, appaccess.ErrForbidden) {
		t.Fatalf("other node mint error = %v", err)
	}
	token, err := service.Mint(ctx, nodeID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.Authenticate(ctx, token.Token)
	if err != nil || grant.NodeID != nodeID || grant.MediaID != app.ID || grant.NodeName != "screen 32" {
		t.Fatalf("grant = %#v, %v", grant, err)
	}

	// Turning app access off invalidates the token; turning it on restores it.
	setAppAccess := func(enabled bool) {
		t.Helper()
		current, err := repository.GetMedia(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.UpdateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: fmt.Sprintf("app-access-%t-%d", enabled, current.Version)}, app.ID, current.Version, media.MediaPatch{AppAccess: &enabled}); err != nil {
			t.Fatal(err)
		}
	}
	setAppAccess(false)
	if _, err := service.Authenticate(ctx, token.Token); !errors.Is(err, appaccess.ErrUnauthorized) {
		t.Fatalf("app access disabled token error = %v", err)
	}
	setAppAccess(true)
	if _, err := service.Authenticate(ctx, token.Token); err != nil {
		t.Fatalf("app access re-enabled token error = %v", err)
	}

	// Reassigning away from the item invalidates the token.
	assign(nodeID, 2, page.ID)
	if _, err := service.Authenticate(ctx, token.Token); !errors.Is(err, appaccess.ErrUnauthorized) {
		t.Fatalf("reassigned token error = %v", err)
	}
	assign(nodeID, 3, app.ID)
	token, err = service.Mint(ctx, nodeID, app.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Unregistering (revoking every credential) deletes the node's tokens.
	if _, err := repository.RevokeNodeCredentials(ctx, nodeID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, token.Token); !errors.Is(err, appaccess.ErrUnauthorized) {
		t.Fatalf("revoked token error = %v", err)
	}
	if _, err := service.Mint(ctx, nodeID, app.ID); !errors.Is(err, appaccess.ErrForbidden) {
		t.Fatalf("revoked node mint error = %v", err)
	}

	// File media cannot opt in to app access.
	enabled := true
	fileMediaID := insertFileMedia(t, db, actorID)
	if _, err := repository.UpdateMedia(ctx, media.Mutation{ActorID: actorID, IdempotencyKey: "file-app-access"}, fileMediaID, 1, media.MediaPatch{AppAccess: &enabled}); !errors.Is(err, media.ErrInvalidArgument) {
		t.Fatalf("file media app access error = %v", err)
	}
}

func insertFileMedia(t *testing.T, db *sql.DB, actorID string) string {
	t.Helper()
	ctx := context.Background()
	assetID := "00000000-0000-4000-8000-000000000041"
	mediaID := "00000000-0000-4000-8000-000000000042"
	digest := fmt.Sprintf("%064x", 41)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, 4, 'image/png', 'ready')`, []any{digest}},
		{`INSERT INTO assets (id, name) VALUES ($1, 'poster')`, []any{assetID}},
		{`INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, []any{assetID, digest}},
		{`INSERT INTO media (id, asset_id, title, created_by, updated_by) VALUES ($1, $2, 'Poster', $3, $3)`, []any{mediaID, assetID, actorID}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return mediaID
}

func TestPostgresPlaylistItemsResolveMediaReferences(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000051"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	fileMediaID := insertFileMedia(t, db, actorID)
	web, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000052", URL: "https://example.com/board", Title: "Board"})
	if err != nil {
		t.Fatal(err)
	}
	mutation := playlist.Mutation{ActorID: actorID}
	created, err := repository.CreatePlaylist(ctx, mutation, playlist.Input{Name: " Lobby ", Items: []playlist.ItemInput{
		{MediaID: web.ID, Position: 2, DurationSeconds: 30},
		{MediaID: fileMediaID, Position: 1, DurationSeconds: 10, Title: "Poster"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Lobby" || len(created.Items) != 2 {
		t.Fatalf("playlist = %#v", created)
	}
	file, page := created.Items[0], created.Items[1]
	if file.MediaID != fileMediaID || file.AssetID != "00000000-0000-4000-8000-000000000041" || file.Title != "Poster" {
		t.Fatalf("file item = %#v", file)
	}
	if page.MediaID != web.ID || page.AssetID != "" || page.DurationSeconds != 30 {
		t.Fatalf("web item = %#v", page)
	}
	entries, err := repository.TargetPlaylist(ctx, created.ID)
	if err != nil || len(entries) != 2 || entries[0].AssetID == "" || entries[1].Web == nil || entries[1].Web.URL != web.URL {
		t.Fatalf("target playlist = %#v, %v", entries, err)
	}

	// Re-submitting by asset ID replaces items; unknown media is rejected.
	updated, err := repository.UpdatePlaylist(ctx, playlist.Mutation{ActorID: actorID, ExpectedVersion: created.Version}, created.ID, playlist.Input{Name: "Lobby", Items: []playlist.ItemInput{{AssetID: file.AssetID, Position: 1, DurationSeconds: 5}}})
	if err != nil || updated.Version != 2 || len(updated.Items) != 1 || updated.Items[0].MediaID != fileMediaID {
		t.Fatalf("updated = %#v, %v", updated, err)
	}
	if _, err := repository.UpdatePlaylist(ctx, playlist.Mutation{ActorID: actorID, ExpectedVersion: created.Version}, created.ID, playlist.Input{Name: "Lobby", Items: []playlist.ItemInput{{AssetID: file.AssetID, Position: 1, DurationSeconds: 5}}}); !errors.Is(err, playlist.ErrConflict) {
		t.Fatalf("stale version error = %v", err)
	}
	if _, err := repository.UpdatePlaylist(ctx, playlist.Mutation{ActorID: actorID, ExpectedVersion: 2}, created.ID, playlist.Input{Name: "Lobby", Items: []playlist.ItemInput{{MediaID: "00000000-0000-4000-8000-0000000000ff", Position: 1, DurationSeconds: 5}}}); !errors.Is(err, playlist.ErrInvalidArgument) {
		t.Fatalf("unknown media error = %v", err)
	}
	if _, err := repository.CreatePlaylist(ctx, mutation, playlist.Input{Name: "Lobby", Items: []playlist.ItemInput{{MediaID: web.ID, Position: 1, DurationSeconds: 5}}}); !errors.Is(err, playlist.ErrNameConflict) {
		t.Fatalf("duplicate name error = %v", err)
	}
	if _, err := repository.Playlist(ctx, "00000000-0000-4000-8000-0000000000fe"); !errors.Is(err, playlist.ErrNotFound) {
		t.Fatalf("missing playlist error = %v", err)
	}
}
