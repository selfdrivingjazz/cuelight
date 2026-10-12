package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/selfdrivingjazz/cuelight/internal/control/assignment"
	"github.com/selfdrivingjazz/cuelight/internal/control/auth"
	"github.com/selfdrivingjazz/cuelight/internal/control/media"
	"github.com/selfdrivingjazz/cuelight/internal/control/operatorapi"
	"github.com/selfdrivingjazz/cuelight/internal/control/persistence"
	controlrelease "github.com/selfdrivingjazz/cuelight/internal/control/release"
	"github.com/selfdrivingjazz/cuelight/internal/control/remote"
	canonical "github.com/selfdrivingjazz/cuelight/internal/release"
)

type targetTestAuth struct{ actorID string }

func (a targetTestAuth) Authenticate(context.Context, string, ...string) (auth.Actor, error) {
	return auth.Actor{ID: a.actorID, Scopes: []string{operatorapi.ScopeAssignmentRead, operatorapi.ScopeAssignmentWrite}}, nil
}
func (targetTestAuth) List(context.Context) ([]auth.TokenMetadata, error) { return nil, nil }
func (targetTestAuth) Revoke(context.Context, string) (auth.TokenMetadata, error) {
	return auth.TokenMetadata{}, nil
}

type targetTestProfiles struct{}

func (targetTestProfiles) ProfileForPlayback(context.Context, canonical.PlaybackPayload) (controlrelease.Profile, error) {
	return controlrelease.Profile{MinimumProtocolVersion: canonical.ProtocolV1}, nil
}

func targetTestPublisher(t *testing.T, repository *Store) controlrelease.TargetPublisher {
	t.Helper()
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := controlrelease.NewEd25519Signer("test-publisher", private)
	if err != nil {
		t.Fatal(err)
	}
	return controlrelease.TargetPublisher{
		Media:   controlrelease.Service{Releases: repository, Artifacts: repository, Profiles: targetTestProfiles{}, Signer: signer},
		Catalog: repository, Releases: repository, Signer: signer,
		MinimumProtocolVersion: canonical.ProtocolV1,
	}
}

func TestPostgresAssignmentByMediaIDPublishesAndReusesReleaseKind(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000101"
	nodeID := "00000000-0000-4000-8000-000000000102"
	assetID := "00000000-0000-4000-8000-000000000103"
	fileMediaID := "00000000-0000-4000-8000-000000000104"
	artifactDigest := fmt.Sprintf("%064x", 7)
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, 'screen', 'active', $1)`, []any{nodeID}},
		{`INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, 4, 'image/png', 'ready')`, []any{artifactDigest}},
		{`INSERT INTO assets (id, name) VALUES ($1, 'poster')`, []any{assetID}},
		{`INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, []any{assetID, artifactDigest}},
		{`INSERT INTO media (id, asset_id, title, created_by, updated_by) VALUES ($1, $2, 'Poster', $3, $3)`, []any{fileMediaID, assetID, actorID}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	web, created, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000105", URL: "https://example.com/board", Title: "Board"})
	if err != nil || !created || web.Kind != media.KindWeb {
		t.Fatalf("create web media = %#v, %v, %v", web, created, err)
	}

	handler := operatorapi.NewHandler(operatorapi.Dependencies{
		Auth:     targetTestAuth{actorID: actorID},
		Assigner: assignment.Service{Assignments: repository, Releases: targetTestPublisher(t, repository)},
	})
	put := func(key, body string) operatorapi.Assignment {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/nodes/"+nodeID+"/assignment", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("If-Match", `"generation:0"`)
		request.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d %s", body, recorder.Code, recorder.Body.String())
		}
		var result operatorapi.Assignment
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	releaseKind := func(id string) canonical.PayloadV1 {
		t.Helper()
		envelope, err := repository.releaseEnvelope(ctx, repository.q(ctx), id)
		if err != nil {
			t.Fatal(err)
		}
		return envelope.Payload
	}

	file := put("assign-file-1-key-0123456789", `{"mediaId":"`+fileMediaID+`","expectedGeneration":0,"remoteInput":false,"showQr":false}`)
	if file.MediaID != fileMediaID || file.DeliveryMode != assignment.DeliveryCacheThenPlay {
		t.Fatalf("file assignment = %#v", file)
	}
	if payload := releaseKind(file.ReleaseID); payload.Presentation.Kind != canonical.PresentationMedia || payload.Presentation.Media.Playback.AssetID != assetID {
		t.Fatalf("file media release = %#v", payload.Presentation)
	}

	page := put("assign-web-1-key-0123456789", `{"mediaId":"`+web.ID+`","expectedGeneration":1,"remoteInput":true,"showQr":true}`)
	if page.MediaID != web.ID || page.DeliveryMode != assignment.DeliveryStream || !page.RemoteInput || page.ReleaseID == file.ReleaseID {
		t.Fatalf("web assignment = %#v", page)
	}
	if payload := releaseKind(page.ReleaseID); payload.Presentation.Kind != canonical.PresentationWeb || payload.Presentation.Web.URL != "https://example.com/board" {
		t.Fatalf("web media release = %#v", payload.Presentation)
	}

	again := put("assign-file-2-key-0123456789", `{"mediaId":"`+fileMediaID+`","expectedGeneration":2,"remoteInput":false,"showQr":false}`)
	if again.ReleaseID != file.ReleaseID || again.DesiredGeneration != 3 {
		t.Fatalf("reassigning the same media must reuse its release: first=%#v again=%#v", file, again)
	}
	var releases int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM releases`).Scan(&releases); err != nil || releases != 2 {
		t.Fatalf("releases = %d, %v; want one per media kind", releases, err)
	}
	current, found, err := repository.CurrentAssignment(ctx, nodeID)
	if err != nil || !found || current.MediaID != fileMediaID {
		t.Fatalf("current assignment = %#v, %v, %v", current, found, err)
	}
}

func TestPostgresUnifiedMediaMigrationConvertsWebItems(t *testing.T) {
	dsn := os.Getenv(postgresTestDSN)
	if dsn == "" {
		t.Skipf("%s is not configured", postgresTestDSN)
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cuelight_media_migration_%d_%d", time.Now().UnixNano(), testSchemaSequence.Add(1))
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		db.Close()
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	all := os.DirFS("../../../migrations")
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0012" {
			continue
		}
		body, err := fs.ReadFile(all, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		before[entry.Name()] = &fstest.MapFile{Data: body}
	}
	if err := persistence.ApplyMigrations(ctx, db, before); err != nil {
		t.Fatal(err)
	}

	actorID := "00000000-0000-4000-8000-000000000201"
	nodeID := "00000000-0000-4000-8000-000000000202"
	namedItem := "00000000-0000-4000-8000-000000000203"
	untitledItem := "00000000-0000-4000-8000-000000000204"
	releaseID := "00000000-0000-4000-8000-000000000205"
	payload := canonical.PayloadV1{
		SchemaVersion: canonical.SchemaVersion, MinimumProtocolVersion: canonical.ProtocolV1,
		Presentation:     canonical.Presentation{Kind: canonical.PresentationWeb, Web: &canonical.WebPresentation{URL: "https://example.com/untitled"}},
		RequiredFeatures: []string{"chromium"},
	}
	digest, canonicalPayload, err := canonical.Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO actors (id, name) VALUES ($1, 'operator')`, []any{actorID}},
		{`INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, 'screen', 'active', $1)`, []any{nodeID}},
		{`INSERT INTO web_items (id, url, title, favicon_url, created_by) VALUES ($1, 'https://example.com/named', 'Named page', 'https://example.com/favicon.ico', $2)`, []any{namedItem, actorID}},
		{`INSERT INTO web_items (id, url, created_by) VALUES ($1, 'https://example.com/untitled', $2)`, []any{untitledItem, actorID}},
		{`INSERT INTO releases (id, digest, payload, key_id, signature, created_by) VALUES ($1, $2, $3, 'test', decode('AA==', 'base64'), $4)`, []any{releaseID, digest, string(canonicalPayload), actorID}},
		{`INSERT INTO assignments (id, node_id, desired_generation, release_id, release_digest, delivery_mode, created_by)
			VALUES ($1, $2, 1, $3, $4, 'stream', $5)`, []any{"00000000-0000-4000-8000-000000000206", nodeID, releaseID, digest, actorID}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.query, err)
		}
	}
	if err := persistence.ApplyMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}

	repository, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	named, err := repository.GetMedia(ctx, namedItem)
	if err != nil || named.Kind != media.KindWeb || named.URL != "https://example.com/named" || named.Title != "Named page" || named.FaviconURL != "https://example.com/favicon.ico" || named.AssetID != "" {
		t.Fatalf("named web item = %#v, %v", named, err)
	}
	untitled, err := repository.GetMedia(ctx, untitledItem)
	if err != nil || untitled.Kind != media.KindWeb || untitled.Title != "https://example.com/untitled" {
		t.Fatalf("untitled web item = %#v, %v", untitled, err)
	}
	var mediaID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT media_id::text FROM assignments WHERE node_id = $1`, nodeID).Scan(&mediaID); err != nil || mediaID.String != untitledItem {
		t.Fatalf("assignment media_id = %v, %v; want backfilled web item", mediaID, err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'web_items'`, schema).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("web_items tables = %d, %v; want dropped", tables, err)
	}
}

func TestPostgresReassignmentKeepsRemoteSession(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000301"
	nodeID := "00000000-0000-4000-8000-000000000302"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, 'screen', 'active', $1)`, nodeID); err != nil {
		t.Fatal(err)
	}
	board, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000303", URL: "https://example.com/board", Title: "Board"})
	if err != nil {
		t.Fatal(err)
	}
	menu, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000304", URL: "https://example.com/menu", Title: "Menu"})
	if err != nil {
		t.Fatal(err)
	}
	operator := operatorapi.NewHandler(operatorapi.Dependencies{
		Auth:     targetTestAuth{actorID: actorID},
		Assigner: assignment.Service{Assignments: repository, Releases: targetTestPublisher(t, repository)},
	})
	put := func(key, body string) operatorapi.Assignment {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/nodes/"+nodeID+"/assignment", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("If-Match", `"generation:0"`)
		request.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		operator.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d %s", body, recorder.Code, recorder.Body.String())
		}
		var result operatorapi.Assignment
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	sessions := &remote.Service{Policies: repository, PublicURL: "https://control.example"}
	phone := remote.NewHandler(sessions, nil)
	press := func(sessionID, token string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/remote/sessions/"+sessionID+"/events", strings.NewReader(`{"sequence":1,"type":"key","key":"Enter"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		phone.ServeHTTP(recorder, request)
		return recorder.Code
	}

	first := put("sticky-remote-1-0123456789", `{"mediaId":"`+board.ID+`","expectedGeneration":0,"remoteInput":true,"showQr":true}`)
	scanned, err := sessions.Create(ctx, nodeID, first.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	link, err := sessions.Link(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}

	second := put("sticky-remote-2-0123456789", `{"mediaId":"`+menu.ID+`","expectedGeneration":1}`)
	if !second.RemoteInput || !second.ShowQR {
		t.Fatalf("reassignment without remote fields = %#v; want remote settings kept", second)
	}
	if code := press(scanned.ID, scanned.Token); code != http.StatusAccepted {
		t.Fatalf("scanned phone after reassignment = %d", code)
	}
	renewed, err := sessions.Create(ctx, nodeID, second.DesiredGeneration)
	if err != nil || renewed.ID != scanned.ID || !renewed.ShowQR {
		t.Fatalf("player session after reassignment = %#v, %v; want the scanned session", renewed, err)
	}
	for _, token := range []string{scanned.Token, link.Token, renewed.Token} {
		if code := press(scanned.ID, token); code != http.StatusAccepted {
			t.Fatalf("phone after the player renewed its session = %d", code)
		}
	}

	third := put("sticky-remote-3-0123456789", `{"mediaId":"`+board.ID+`","expectedGeneration":2,"remoteInput":false}`)
	if third.RemoteInput || third.ShowQR {
		t.Fatalf("explicit remoteInput false = %#v; want remote and QR off", third)
	}
	if code := press(scanned.ID, renewed.Token); code != http.StatusForbidden {
		t.Fatalf("phone after remote turned off = %d", code)
	}
	if _, err := sessions.Create(ctx, nodeID, third.DesiredGeneration); !errors.Is(err, remote.ErrDisabled) {
		t.Fatalf("session with remote off = %v", err)
	}
}
