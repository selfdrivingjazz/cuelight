package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/cable"
	"github.com/jmill/cable/internal/control/cableguide"
)

func TestPostgresHostedCableMediaAndGuideTokens(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000031"
	nodeID := "00000000-0000-4000-8000-000000000032"
	otherNodeID := "00000000-0000-4000-8000-000000000033"
	assetID := "00000000-0000-4000-8000-000000000034"
	if err := repository.EnsureActor(ctx, actorID, "Cable author"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{nodeID, otherNodeID} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, $2, 'active', $3)`, id, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO node_credentials (node_id, serial, not_after) VALUES ($1, 'serial', now() + interval '1 day')`, id); err != nil {
			t.Fatal(err)
		}
	}
	mutation := cable.Mutation{ActorID: actorID, IdempotencyKey: "cable-authoring", Now: time.Now().UTC()}
	alpha, err := repository.CreateDeployment(ctx, mutation, cable.DeploymentInput{Name: "Alpha", Timezone: "UTC", Theme: "gallery"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := repository.CreateDeployment(ctx, mutation, cable.DeploymentInput{Name: "Beta", Timezone: "UTC", Theme: "noir"})
	if err != nil {
		t.Fatal(err)
	}
	cableMedia := func(deploymentID string) (id, title string) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT id::text, title FROM media WHERE deployment_id = $1 AND kind = 'web' AND url IS NULL`, deploymentID).Scan(&id, &title); err != nil {
			t.Fatalf("Cable media for %s: %v", deploymentID, err)
		}
		return id, title
	}
	alphaMedia, title := cableMedia(alpha.ID)
	if title != "Cable · Alpha" {
		t.Fatalf("title = %q", title)
	}
	if _, err := repository.UpdateDeployment(ctx, cable.Mutation{ActorID: actorID, ExpectedVersion: alpha.Version}, alpha.ID, cable.DeploymentInput{Name: "Alpha Prime", Timezone: "UTC", Theme: "gallery"}); err != nil {
		t.Fatal(err)
	}
	if renamedID, title := cableMedia(alpha.ID); renamedID != alphaMedia || title != "Cable · Alpha Prime" {
		t.Fatalf("renamed media = %s %q", renamedID, title)
	}

	guide := &cableguide.Service{Repository: repository}
	if _, err := guide.Mint(ctx, nodeID, alpha.ID); !errors.Is(err, cableguide.ErrForbidden) {
		t.Fatalf("unassigned mint error = %v", err)
	}
	releaseDigest := strings.Repeat("e", 64)
	releaseID := "00000000-0000-4000-8000-000000000035"
	if _, err := db.ExecContext(ctx, `INSERT INTO releases (id, digest, key_id, signature, payload, created_by) VALUES ($1, $2, 'k', '\x00', '{}', $3)`, releaseID, releaseDigest, actorID); err != nil {
		t.Fatal(err)
	}
	assign := func(node string, generation int, mediaID sql.NullString) {
		t.Helper()
		id := fmt.Sprintf("00000000-0000-4000-8001-%012d", generation*10+len(node)%7)
		if _, err := db.ExecContext(ctx, `INSERT INTO assignments (id, node_id, desired_generation, release_id, release_digest, delivery_mode, created_by, media_id) VALUES ($1, $2, $3, $4, $5, 'stream', $6, $7)`, id, node, generation, releaseID, releaseDigest, actorID, mediaID); err != nil {
			t.Fatal(err)
		}
	}
	assign(nodeID, 1, sql.NullString{String: alphaMedia, Valid: true})
	if _, err := guide.Mint(ctx, nodeID, beta.ID); !errors.Is(err, cableguide.ErrForbidden) {
		t.Fatalf("other deployment mint error = %v", err)
	}
	if _, err := guide.Mint(ctx, otherNodeID, alpha.ID); !errors.Is(err, cableguide.ErrForbidden) {
		t.Fatalf("other node mint error = %v", err)
	}
	token, err := guide.Mint(ctx, nodeID, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := guide.Authenticate(ctx, token.Token)
	if err != nil || grant.NodeID != nodeID || grant.DeploymentID != alpha.ID {
		t.Fatalf("grant = %#v, %v", grant, err)
	}

	// Asset scoping: only assets the deployment schedules are readable.
	if _, err := db.ExecContext(ctx, `INSERT INTO assets (id, name) VALUES ($1, 'programme')`, assetID); err != nil {
		t.Fatal(err)
	}
	channel, err := repository.CreateChannel(ctx, mutation, cable.ChannelInput{DeploymentID: alpha.ID, Number: 1, Name: "One", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for deploymentID, want := range map[string]bool{alpha.ID: false, beta.ID: false} {
		if got, err := repository.DeploymentReferencesAsset(ctx, deploymentID, assetID); err != nil || got != want {
			t.Fatalf("before block %s referenced = %v, %v", deploymentID, got, err)
		}
	}
	start := time.Now().UTC().Truncate(time.Hour)
	if _, err := repository.CreateBlock(ctx, mutation, cable.BlockInput{ChannelID: channel.ID, AssetID: assetID, Title: "Block", StartsAt: start, EndsAt: start.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for deploymentID, want := range map[string]bool{alpha.ID: true, beta.ID: false} {
		if got, err := repository.DeploymentReferencesAsset(ctx, deploymentID, assetID); err != nil || got != want {
			t.Fatalf("%s referenced = %v, %v; want %v", deploymentID, got, err, want)
		}
	}
	if got, err := repository.DeploymentReferencesAsset(ctx, alpha.ID, "not-a-uuid"); err != nil || got {
		t.Fatalf("malformed asset = %v, %v", got, err)
	}

	// Reassigning away from the Cable item invalidates the token.
	assign(nodeID, 2, sql.NullString{})
	if _, err := guide.Authenticate(ctx, token.Token); !errors.Is(err, cableguide.ErrUnauthorized) {
		t.Fatalf("reassigned token error = %v", err)
	}
	assign(nodeID, 3, sql.NullString{String: alphaMedia, Valid: true})
	token, err = guide.Mint(ctx, nodeID, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Unregistering (revoking every credential) deletes the node's tokens.
	if _, err := repository.RevokeNodeCredentials(ctx, nodeID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := guide.Authenticate(ctx, token.Token); !errors.Is(err, cableguide.ErrUnauthorized) {
		t.Fatalf("revoked token error = %v", err)
	}
	if _, err := guide.Mint(ctx, nodeID, alpha.ID); !errors.Is(err, cableguide.ErrForbidden) {
		t.Fatalf("revoked node mint error = %v", err)
	}

	// Deleting a deployment cascades to its Cable media item.
	if err := repository.DeleteDeployment(ctx, cable.Mutation{ActorID: actorID, ExpectedVersion: beta.Version}, beta.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM media WHERE deployment_id = $1`, beta.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("beta media remaining = %d, %v", remaining, err)
	}
}
