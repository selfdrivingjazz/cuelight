package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
	controlplayerapi "github.com/jmill/cable/internal/control/playerapi"
	controlrelease "github.com/jmill/cable/internal/control/release"
	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
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

	mediaID := "00000000-0000-4000-8000-000000000015"
	if _, err := db.ExecContext(ctx, `INSERT INTO media (id, asset_id, title, created_by, updated_by) VALUES ($1, $2, 'asset', $3, $3)`, mediaID, assetID, actorID); err != nil {
		t.Fatal(err)
	}
	service := assignment.Service{Assignments: repository, Releases: targetTestPublisher(t, repository)}
	zero := int64(0)
	put := assignment.Target{MediaID: mediaID, DeliveryMode: assignment.DeliveryStream, ExpectedGeneration: &zero}
	for _, nodeID := range []string{nodeOne, nodeTwo} {
		result, err := service.AssignTarget(ctx, assignment.Mutation{ActorID: actorID, IdempotencyKey: "shared-across-nodes"}, nodeID, put)
		if err != nil || !result.Created || result.Assignment.NodeID != nodeID {
			t.Fatalf("cross-node assignment %s = %#v, %v", nodeID, result, err)
		}
	}

	one := int64(1)
	concurrent := put
	concurrent.ExpectedGeneration = &one
	var successes atomic.Int32
	var conflicts atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := service.AssignTarget(ctx, assignment.Mutation{ActorID: actorID, IdempotencyKey: fmt.Sprintf("concurrent-%d", index)}, nodeOne, concurrent)
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

type fixedReleasePublisher struct{ envelope canonical.EnvelopeV1 }

func (p fixedReleasePublisher) PublishTarget(context.Context, string, string, string, string) (canonical.EnvelopeV1, error) {
	return p.envelope, nil
}

func TestPostgresPlayerAPIRepositoryContracts(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000021"
	nodeOne := "00000000-0000-4000-8000-000000000022"
	nodeTwo := "00000000-0000-4000-8000-000000000023"
	assetID := "00000000-0000-4000-8000-000000000024"
	artifactDigest := fmt.Sprintf("%064x", 3)
	if err := repository.EnsureActor(ctx, actorID, "player-api-test"); err != nil {
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
		VALUES ($1, 6, 'text/plain', 'ready')`, artifactDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO assets (id, name) VALUES ($1, 'player-asset')`, assetID); err != nil {
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
		Artifacts:              []canonical.Artifact{{Digest: artifactDigest, Size: 6, MediaType: "text/plain"}},
	}
	releaseDigest, _, err := canonical.Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.Publish(ctx, controlrelease.Publication{ActorID: actorID, IdempotencyKey: "player-publish", RequestDigest: fmt.Sprintf("%064x", 4)},
		func() (controlrelease.PreparedPublication, error) {
			return controlrelease.PreparedPublication{Payload: payload, Digest: releaseDigest}, nil
		},
		func(id string, prepared controlrelease.PreparedPublication) (canonical.EnvelopeV1, error) {
			return canonical.EnvelopeV1{ID: id, Digest: prepared.Digest, KeyID: "player", Signature: "AA==", Payload: prepared.Payload}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// The player contracts need a raw release (text/plain artifact), so the
	// target publisher is replaced by one that returns it.
	rawMediaID := "00000000-0000-4000-8000-000000000025"
	if _, err := db.ExecContext(ctx, `INSERT INTO media (id, asset_id, title, created_by, updated_by) VALUES ($1, $2, 'raw', $3, $3)`, rawMediaID, assetID, actorID); err != nil {
		t.Fatal(err)
	}
	assignmentService := assignment.Service{Assignments: repository, Releases: fixedReleasePublisher{envelope: published.Envelope}}
	zero, one := int64(0), int64(1)
	if _, err := assignmentService.AssignTarget(ctx, assignment.Mutation{ActorID: actorID, IdempotencyKey: "player-assignment-1"}, nodeOne, assignment.Target{MediaID: rawMediaID, DeliveryMode: assignment.DeliveryStream, ExpectedGeneration: &zero}); err != nil {
		t.Fatal(err)
	}

	report := playerclient.CapabilityReport{Platform: "linux", Architecture: "amd64", ProtocolVersion: "1", RendererVersion: "1", ProfileID: "default"}
	if err := repository.PutCapabilities(ctx, nodeOne, report); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.QueryRowContext(ctx, `SELECT report FROM node_capability_reports WHERE node_id = $1`, nodeOne).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatal("capability report was not persisted")
	}
	if _, ok, err := repository.CurrentAssignment(ctx, nodeTwo); err != nil || ok {
		t.Fatalf("cross-node current assignment = found=%v err=%v", ok, err)
	}
	current, ok, err := repository.CurrentAssignment(ctx, nodeOne)
	if err != nil || !ok || current.ReleaseID != published.Envelope.ID {
		t.Fatalf("current assignment = %#v found=%v err=%v", current, ok, err)
	}
	if _, ok, err := repository.AssignedRelease(ctx, nodeTwo, published.Envelope.ID); err != nil || ok {
		t.Fatalf("cross-node release assignment = found=%v err=%v", ok, err)
	}
	if _, ok, err := repository.AssignedRelease(ctx, nodeOne, published.Envelope.ID); err != nil || !ok {
		t.Fatalf("assigned release = found=%v err=%v", ok, err)
	}
	envelope, err := repository.LoadRelease(ctx, published.Envelope.ID)
	if err != nil || envelope.Digest != published.Envelope.Digest {
		t.Fatalf("loaded release = %#v err=%v", envelope, err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	waitDone := make(chan error, 1)
	go func() { waitDone <- repository.WaitForAssignment(cancelCtx, nodeOne, 1, time.Second) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled assignment wait = %v", err)
	}
	if _, err := assignmentService.AssignTarget(ctx, assignment.Mutation{ActorID: actorID, IdempotencyKey: "player-assignment-2"}, nodeOne, assignment.Target{
		MediaID: rawMediaID, DeliveryMode: assignment.DeliveryStream, ExpectedGeneration: &one,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.WaitForAssignment(ctx, nodeOne, 1, time.Second); err != nil {
		t.Fatalf("assignment wait after update = %v", err)
	}

	candidate := published.Envelope.Digest
	accepted, err := repository.RecordReport(ctx, nodeOne, playerclient.NodeReport{
		EventEpoch: nodeOne, Sequence: 1, DesiredGenerationSeen: 2, ObservedGeneration: 2,
		PresentationKind: canonical.PresentationWeb, CandidateReleaseDigest: &candidate,
		Conditions: []playerclient.Condition{{Type: "PlaybackHealthy", Status: true, Reason: "CandidateCommitted"}},
	})
	if err != nil || !accepted {
		t.Fatalf("fresh report accepted=%v err=%v", accepted, err)
	}
	storedReport, err := NewOperatorAdapter(repository).GetNodeStatus(ctx, nodeOne)
	if err != nil || storedReport.PresentationKind != canonical.PresentationWeb || len(storedReport.Conditions) != 1 || storedReport.Conditions[0].Reason != "CandidateCommitted" {
		t.Fatalf("stored wire report = %#v, %v", storedReport, err)
	}
	stale := playerclient.NodeReport{EventEpoch: nodeOne, Sequence: 1, DesiredGenerationSeen: 2, ObservedGeneration: 0}
	accepted, err = repository.RecordReport(ctx, nodeOne, stale)
	if err != nil || accepted {
		t.Fatalf("stale report accepted=%v err=%v", accepted, err)
	}
	var observed int64
	if err := db.QueryRowContext(ctx, `SELECT observed_generation FROM nodes WHERE id = $1`, nodeOne).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 2 {
		t.Fatalf("stale report changed observed generation to %d", observed)
	}
	if authorized, err := repository.AuthorizeArtifact(ctx, nodeOne, artifactDigest); err != nil || !authorized {
		t.Fatalf("authorized artifact = %v err=%v", authorized, err)
	}
	if authorized, err := repository.AuthorizeArtifact(ctx, nodeTwo, artifactDigest); err != nil || authorized {
		t.Fatalf("cross-node artifact authorization = %v err=%v", authorized, err)
	}

	event := func(id string, sequence int64) playerclient.NodeEvent {
		return playerclient.NodeEvent{ID: id, Epoch: nodeOne, Sequence: sequence, Type: "test"}
	}
	ack, err := repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeOne, Events: []playerclient.NodeEvent{event("00000000-0000-4000-8000-000000000031", 1)}})
	if err != nil || ack.AcknowledgedThrough != 1 {
		t.Fatalf("first event ack=%#v err=%v", ack, err)
	}
	if duplicate, err := repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeOne, Events: []playerclient.NodeEvent{event("00000000-0000-4000-8000-000000000031", 1)}}); err != nil || duplicate.AcknowledgedThrough != 1 {
		t.Fatalf("duplicate event ack=%#v err=%v", duplicate, err)
	}
	if _, err := repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeOne, Events: []playerclient.NodeEvent{event("00000000-0000-4000-8000-000000000033", 3)}}); !errors.Is(err, controlplayerapi.ErrEventGap) {
		t.Fatalf("event gap err=%v", err)
	}
	ack, err = repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeOne, Events: []playerclient.NodeEvent{event("00000000-0000-4000-8000-000000000032", 2)}})
	if err != nil || ack.AcknowledgedThrough != 2 {
		t.Fatalf("second event ack=%#v err=%v", ack, err)
	}
	conflict := event("00000000-0000-4000-8000-000000000031", 1)
	conflict.Type = "different"
	if _, err := repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeOne, Events: []playerclient.NodeEvent{conflict}}); !errors.Is(err, controlplayerapi.ErrEventConflict) {
		t.Fatalf("event conflict err=%v", err)
	}
	if _, err := repository.AppendEvents(ctx, nodeOne, playerclient.EventBatch{Epoch: nodeTwo, Events: []playerclient.NodeEvent{event("00000000-0000-4000-8000-000000000034", 3)}}); !errors.Is(err, controlplayerapi.ErrEpochMismatch) {
		t.Fatalf("event epoch err=%v", err)
	}

	cas, err := localcas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	object, err := cas.Put(bytes.NewReader([]byte("player")), "")
	if err != nil {
		t.Fatal(err)
	}
	reader := NewLocalCASReader(cas)
	stat, err := reader.Stat(ctx, object.Digest)
	if err != nil || stat.Size != 6 {
		t.Fatalf("CAS stat = %#v err=%v", stat, err)
	}
	body, err := reader.Open(ctx, object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(content) != "player" {
		t.Fatalf("CAS content = %q err=%v", content, err)
	}
}
