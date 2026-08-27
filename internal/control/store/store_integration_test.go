package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/persistence"
	controlrelease "github.com/jmill/cable/internal/control/release"
	canonical "github.com/jmill/cable/internal/release"
)

const postgresTestDSN = "CABLE_POSTGRES_TEST_DSN"

var testSchemaSequence atomic.Uint64

func newPostgresTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv(postgresTestDSN)
	if dsn == "" {
		t.Skipf("%s is not configured", postgresTestDSN)
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cable_store_%d_%d", time.Now().UnixNano(), testSchemaSequence.Add(1))
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	if err := persistence.ApplyMigrations(ctx, db, os.DirFS("../../../migrations")); err != nil {
		db.Close()
		admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	repository, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return repository, db
}

func TestPostgresAdministratorIdempotencyConcurrencyReplayAndRollback(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000001"
	if err := repository.EnsureActor(ctx, actorID, "administrator"); err != nil {
		t.Fatal(err)
	}
	candidate := auth.IdempotencyRecord{
		ActorID:            actorID,
		Operation:          "test.operation",
		Key:                "same-key",
		RequestFingerprint: sha256.Sum256([]byte("same-request")),
		ExpiresAt:          time.Now().Add(time.Hour),
	}
	var calls atomic.Int32
	operation := func(context.Context) (auth.IdempotentResponse, error) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		return auth.IdempotentResponse{Status: 201, Body: []byte{0, 1, 2, 255}}, nil
	}
	start := make(chan struct{})
	results := make(chan auth.IdempotencyRecord, 2)
	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, _, err := repository.Execute(ctx, candidate, time.Now(), operation)
			results <- record
			errorsCh <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for record := range results {
		if record.ResponseStatus != 201 || string(record.ResponseBody) != string([]byte{0, 1, 2, 255}) {
			t.Fatalf("unexpected replay record: %#v", record)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("operation calls = %d, want 1", calls.Load())
	}

	failed := candidate
	failed.Key = "rollback-key"
	rollbackErr := errors.New("rollback")
	if _, _, err := repository.Execute(ctx, failed, time.Now(), func(txCtx context.Context) (auth.IdempotentResponse, error) {
		tokenHash := sha256.Sum256([]byte("temporary"))
		err := repository.Create(txCtx, auth.TokenRecord{
			ID: "00000000-0000-4000-8000-000000000002", ActorID: actorID,
			Prefix: "rollback", Hash: tokenHash, Scopes: []string{"test"}, CreatedAt: time.Now(),
		})
		if err != nil {
			return auth.IdempotentResponse{}, err
		}
		return auth.IdempotentResponse{}, rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v", err)
	}
	found, err := repository.FindByPrefix(ctx, "rollback")
	if err != nil || len(found) != 0 {
		t.Fatalf("rolled-back side effect = %#v, %v", found, err)
	}
}

func TestPostgresReleaseAssignmentConcurrencyAndCrossNodeIsolation(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000011"
	nodeOne := "00000000-0000-4000-8000-000000000012"
	nodeTwo := "00000000-0000-4000-8000-000000000013"
	assetID := "00000000-0000-4000-8000-000000000014"
	artifactDigest := fmt.Sprintf("%064x", 1)
	if err := repository.EnsureActor(ctx, actorID, "publisher"); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{nodeOne, nodeTwo} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO nodes (id, name, credential_state, event_epoch)
			VALUES ($1, $2, 'active', $3)`, nodeID, nodeID, nodeID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO artifacts (digest, size_bytes, media_type, state)
		VALUES ($1, 4, 'image/png', 'ready')`, artifactDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO assets (id, name) VALUES ($1, 'asset')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO asset_artifacts (asset_id, artifact_digest, role)
		VALUES ($1, $2, 'original')`, assetID, artifactDigest); err != nil {
		t.Fatal(err)
	}
	payload := canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: canonical.ProtocolV1,
		Playback:               canonical.PlaybackPayload{AssetID: assetID, Fit: "fit", AllowedDeliveryMode: []string{assignment.DeliveryStream}},
		Artifacts:              []canonical.Artifact{{Digest: artifactDigest, Size: 4, MediaType: "image/png"}},
	}
	releaseDigest, _, err := canonical.Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	publication := controlrelease.Publication{ActorID: actorID, IdempotencyKey: "publish", RequestDigest: fmt.Sprintf("%064x", 2)}
	published, err := repository.Publish(ctx, publication,
		func() (controlrelease.PreparedPublication, error) {
			return controlrelease.PreparedPublication{Payload: payload, Digest: releaseDigest}, nil
		},
		func(id string, prepared controlrelease.PreparedPublication) (canonical.EnvelopeV1, error) {
			return canonical.EnvelopeV1{ID: id, Digest: prepared.Digest, KeyID: "publisher", Signature: "AA==", Payload: prepared.Payload}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repository.Publish(ctx, publication,
		func() (controlrelease.PreparedPublication, error) {
			return controlrelease.PreparedPublication{}, errors.New("prepare called on replay")
		},
		func(string, controlrelease.PreparedPublication) (canonical.EnvelopeV1, error) {
			return canonical.EnvelopeV1{}, errors.New("create called on replay")
		})
	if err != nil || !replay.Replayed || replay.Envelope.ID != published.Envelope.ID {
		t.Fatalf("release replay = %#v, %v", replay, err)
	}

	service := assignment.Service{Assignments: repository}
	put := assignment.PutInput{
		ReleaseID: published.Envelope.ID, ReleaseDigest: published.Envelope.Digest,
		DeliveryMode: assignment.DeliveryStream, ExpectedGeneration: 0,
	}
	for _, nodeID := range []string{nodeOne, nodeTwo} {
		result, err := service.Put(ctx, nodeID, assignment.Mutation{ActorID: actorID, IdempotencyKey: "shared-across-nodes"}, put)
		if err != nil || !result.Created || result.Assignment.NodeID != nodeID {
			t.Fatalf("cross-node assignment %s = %#v, %v", nodeID, result, err)
		}
	}

	concurrent := put
	concurrent.ExpectedGeneration = 1
	var successes atomic.Int32
	var conflicts atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := service.Put(ctx, nodeOne, assignment.Mutation{ActorID: actorID, IdempotencyKey: fmt.Sprintf("concurrent-%d", index)}, concurrent)
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, assignment.ErrGenerationConflict) {
				conflicts.Add(1)
			}
		}(index)
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("assignment race successes=%d conflicts=%d", successes.Load(), conflicts.Load())
	}
}

func TestPostgresMigrationsRoundTrip(t *testing.T) {
	_, db := newPostgresTestStore(t)
	migrations, err := persistence.LoadMigrations(os.DirFS("../../../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(migrations) - 1; index >= 0; index-- {
		if _, err := tx.ExecContext(context.Background(), migrations[index].Down); err != nil {
			tx.Rollback()
			t.Fatalf("down migration %s: %v", migrations[index].Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var actors sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT to_regclass('actors')::text`).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if actors.Valid {
		t.Fatalf("actors table remains after down migrations: %s", actors.String)
	}
}
