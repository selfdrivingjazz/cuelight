package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/pki"
	canonical "github.com/jmill/cable/internal/release"
)

var _ assignment.Repository = (*Store)(nil)
var _ assignment.Transaction = (*assignmentTransaction)(nil)

func (s *Store) Transact(ctx context.Context, nodeID string, fn func(assignment.Transaction) error) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var lockedID string
		err := tx.QueryRowContext(txCtx, `SELECT id::text FROM nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&lockedID)
		if errors.Is(err, sql.ErrNoRows) {
			return pki.ErrNodeNotFound
		}
		if err != nil {
			return wrap("lock assignment node", err)
		}
		return fn(&assignmentTransaction{store: s, ctx: txCtx, tx: tx, nodeID: lockedID})
	})
}

type assignmentTransaction struct {
	store  *Store
	ctx    context.Context
	tx     *sql.Tx
	nodeID string
}

func (t *assignmentTransaction) CurrentGeneration() (int64, error) {
	var generation int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT desired_generation FROM nodes WHERE id = $1`, t.nodeID).Scan(&generation)
	return generation, wrap("read desired generation", err)
}

func (t *assignmentTransaction) CurrentAssignment() (assignment.Assignment, bool, error) {
	return t.assignment(`
		SELECT id::text, node_id::text, release_id::text, release_digest, delivery_mode,
		       desired_generation - 1, desired_generation, created_by::text
		FROM assignments WHERE node_id = $1
		ORDER BY desired_generation DESC LIMIT 1`, t.nodeID)
}

func (t *assignmentTransaction) ReleaseByID(id string) (canonical.EnvelopeV1, bool, error) {
	envelope, err := t.store.releaseEnvelope(t.ctx, t.tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return canonical.EnvelopeV1{}, false, nil
	}
	if err != nil {
		var count int
		checkErr := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM releases WHERE id = $1`, id).Scan(&count)
		if checkErr == nil && count == 0 {
			return canonical.EnvelopeV1{}, false, nil
		}
		return canonical.EnvelopeV1{}, false, err
	}
	return envelope, true, nil
}

func (t *assignmentTransaction) AssignedRelease(releaseDigest string, atMostGeneration int64) (assignment.Assignment, bool, error) {
	return t.assignment(`
		SELECT id::text, node_id::text, release_id::text, release_digest, delivery_mode,
		       desired_generation - 1, desired_generation, created_by::text
		FROM assignments
		WHERE node_id = $1 AND release_digest = $2 AND desired_generation <= $3
		ORDER BY desired_generation DESC LIMIT 1`, t.nodeID, releaseDigest, atMostGeneration)
}

func (t *assignmentTransaction) LatestObservation() (assignment.Observation, bool, error) {
	var report []byte
	err := t.tx.QueryRowContext(t.ctx, `
		SELECT report FROM node_report_snapshots WHERE node_id = $1`, t.nodeID).Scan(&report)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment.Observation{}, false, nil
	}
	if err != nil {
		return assignment.Observation{}, false, wrap("read latest node observation", err)
	}
	var observation assignment.Observation
	if err := json.Unmarshal(report, &observation); err != nil {
		return assignment.Observation{}, false, wrap("decode latest node observation", err)
	}
	return observation, true, nil
}

func (t *assignmentTransaction) Idempotency(actorID, key string) (assignment.IdempotencyRecord, bool, error) {
	var record assignment.IdempotencyRecord
	err := t.tx.QueryRowContext(t.ctx, `
		SELECT i.request_digest,
		       a.id::text, a.node_id::text, a.release_id::text, a.release_digest, a.delivery_mode,
		       a.desired_generation - 1, a.desired_generation, a.created_by::text
		FROM assignment_idempotency_records i
		JOIN assignments a ON a.id = i.assignment_id
		WHERE i.node_id = $1 AND i.actor_id = $2 AND i.key = $3`, t.nodeID, actorID, key).Scan(
		&record.RequestDigest, &record.Assignment.ID, &record.Assignment.NodeID,
		&record.Assignment.ReleaseID, &record.Assignment.ReleaseDigest, &record.Assignment.DeliveryMode,
		&record.Assignment.ExpectedGeneration, &record.Assignment.DesiredGeneration, &record.Assignment.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment.IdempotencyRecord{}, false, nil
	}
	return record, err == nil, wrap("read assignment idempotency record", err)
}

func (t *assignmentTransaction) NewAssignmentID() (string, error) {
	return t.store.newUUID()
}

func (t *assignmentTransaction) InsertAssignment(value assignment.Assignment) error {
	if value.NodeID != t.nodeID {
		return pki.ErrWrongNode
	}
	_, err := t.tx.ExecContext(t.ctx, `
		INSERT INTO assignments
		  (id, node_id, desired_generation, release_id, release_digest, delivery_mode, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, value.ID, value.NodeID, value.DesiredGeneration,
		value.ReleaseID, value.ReleaseDigest, value.DeliveryMode, value.CreatedBy)
	if postgresCode(err) == "23505" {
		return assignment.ErrGenerationConflict
	}
	if err != nil {
		return wrap("insert assignment history", err)
	}
	result, err := t.tx.ExecContext(t.ctx, `
		UPDATE nodes SET desired_generation = $2
		WHERE id = $1 AND desired_generation = $3`, t.nodeID, value.DesiredGeneration, value.ExpectedGeneration)
	if err != nil {
		return wrap("advance desired generation", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrap("inspect desired generation advance", err)
	}
	if changed != 1 {
		return assignment.ErrGenerationConflict
	}
	return nil
}

func (t *assignmentTransaction) ReplaceAuthorizations(authorizations []assignment.Authorization) error {
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM node_release_authorizations WHERE node_id = $1`, t.nodeID); err != nil {
		return wrap("replace node release authorizations", err)
	}
	for _, authorization := range authorizations {
		_, err := t.tx.ExecContext(t.ctx, `
			INSERT INTO node_release_authorizations
			  (node_id, role, release_id, release_digest, updated_at)
			VALUES ($1, $2, $3, $4, $5)`, t.nodeID, authorization.Role,
			authorization.ReleaseID, authorization.ReleaseDigest, time.Now().UTC())
		if err != nil {
			return wrap("insert node release authorization", err)
		}
	}
	return nil
}

func (t *assignmentTransaction) PutIdempotency(actorID, key string, record assignment.IdempotencyRecord) error {
	if record.Assignment.NodeID != t.nodeID {
		return pki.ErrWrongNode
	}
	_, err := t.tx.ExecContext(t.ctx, `
		INSERT INTO assignment_idempotency_records
		  (node_id, actor_id, key, request_digest, assignment_id)
		VALUES ($1, $2, $3, $4, $5)`, t.nodeID, actorID, key, record.RequestDigest, record.Assignment.ID)
	if postgresCode(err) == "23505" {
		return assignment.ErrIdempotencyConflict
	}
	return wrap("record assignment idempotency response", err)
}

func (t *assignmentTransaction) assignment(query string, arguments ...any) (assignment.Assignment, bool, error) {
	var value assignment.Assignment
	err := t.tx.QueryRowContext(t.ctx, query, arguments...).Scan(&value.ID, &value.NodeID,
		&value.ReleaseID, &value.ReleaseDigest, &value.DeliveryMode, &value.ExpectedGeneration,
		&value.DesiredGeneration, &value.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment.Assignment{}, false, nil
	}
	return value, err == nil, wrap("read assignment history", err)
}

// RecordObservation accepts only the current event epoch and a strictly newer
// sequence, so stale reports can never influence authorization replacement.
func (s *Store) RecordObservation(ctx context.Context, nodeID string, observation assignment.Observation) (bool, error) {
	accepted := false
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var epoch string
		err := tx.QueryRowContext(txCtx, `SELECT event_epoch::text FROM nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&epoch)
		if errors.Is(err, sql.ErrNoRows) {
			return pki.ErrNodeNotFound
		}
		if err != nil {
			return wrap("lock observation node", err)
		}
		if epoch != observation.EventEpoch {
			return ErrEventEpochMismatch
		}
		var sequence int64
		err = tx.QueryRowContext(txCtx, `SELECT sequence FROM node_report_snapshots WHERE node_id = $1 FOR UPDATE`, nodeID).Scan(&sequence)
		if err == nil && observation.Sequence <= sequence {
			return ErrStaleObservation
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return wrap("read observation sequence", err)
		}
		report, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(txCtx, `
			INSERT INTO node_report_snapshots (node_id, event_epoch, sequence, report, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (node_id) DO UPDATE
			SET event_epoch = EXCLUDED.event_epoch, sequence = EXCLUDED.sequence,
			    report = EXCLUDED.report, updated_at = EXCLUDED.updated_at`,
			nodeID, observation.EventEpoch, observation.Sequence, report)
		if err != nil {
			return wrap("record node observation", err)
		}
		_, err = tx.ExecContext(txCtx, `UPDATE nodes SET observed_generation = $2 WHERE id = $1`, nodeID, observation.ObservedGeneration)
		if err != nil {
			return wrap("update observed generation", err)
		}
		accepted = true
		return nil
	})
	return accepted, err
}

func (s *Store) AssignmentHistory(ctx context.Context, nodeID string) ([]assignment.Assignment, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT id::text, node_id::text, release_id::text, release_digest, delivery_mode,
		       desired_generation - 1, desired_generation, created_by::text
		FROM assignments WHERE node_id = $1 ORDER BY desired_generation`, nodeID)
	if err != nil {
		return nil, wrap("list assignment history", err)
	}
	defer rows.Close()
	result := make([]assignment.Assignment, 0)
	for rows.Next() {
		var value assignment.Assignment
		if err := rows.Scan(&value.ID, &value.NodeID, &value.ReleaseID, &value.ReleaseDigest,
			&value.DeliveryMode, &value.ExpectedGeneration, &value.DesiredGeneration, &value.CreatedBy); err != nil {
			return nil, wrap("scan assignment history", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate assignment history", err)
	}
	return result, nil
}

func (s *Store) Authorizations(ctx context.Context, nodeID string) ([]assignment.Authorization, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT role, release_id::text, release_digest
		FROM node_release_authorizations WHERE node_id = $1 ORDER BY role`, nodeID)
	if err != nil {
		return nil, wrap("list node release authorizations", err)
	}
	defer rows.Close()
	result := make([]assignment.Authorization, 0, 5)
	for rows.Next() {
		var authorization assignment.Authorization
		if err := rows.Scan(&authorization.Role, &authorization.ReleaseID, &authorization.ReleaseDigest); err != nil {
			return nil, wrap("scan node release authorization", err)
		}
		result = append(result, authorization)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate node release authorizations", err)
	}
	return result, nil
}
