package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/auth"
)

var _ auth.TokenRepository = (*Store)(nil)
var _ auth.IdempotencyRepository = (*Store)(nil)

func (s *Store) Create(ctx context.Context, record auth.TokenRecord) error {
	_, err := s.q(ctx).ExecContext(ctx, `
		INSERT INTO admin_tokens (id, actor_id, prefix, token_hash, scopes, revoked_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.ID, record.ActorID, record.Prefix, record.Hash[:], record.Scopes, record.RevokedAt, record.CreatedAt)
	if postgresCode(err) == "23505" {
		return auth.ErrAlreadyExists
	}
	return wrap("create administrator token", err)
}

func (s *Store) FindByPrefix(ctx context.Context, prefix string) ([]auth.TokenRecord, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT id::text, actor_id::text, prefix, token_hash, array_to_json(scopes), created_at, revoked_at
		FROM admin_tokens WHERE prefix = $1 ORDER BY id`, prefix)
	if err != nil {
		return nil, wrap("find administrator tokens", err)
	}
	defer rows.Close()
	return scanAdminTokens(rows)
}

func (s *Store) List(ctx context.Context) ([]auth.TokenRecord, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT id::text, actor_id::text, prefix, token_hash, array_to_json(scopes), created_at, revoked_at
		FROM admin_tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap("list administrator tokens", err)
	}
	defer rows.Close()
	return scanAdminTokens(rows)
}

func (s *Store) Revoke(ctx context.Context, id string, at time.Time) (auth.TokenRecord, error) {
	row := s.q(ctx).QueryRowContext(ctx, `
		UPDATE admin_tokens SET revoked_at = COALESCE(revoked_at, $2)
		WHERE id = $1
		RETURNING id::text, actor_id::text, prefix, token_hash, array_to_json(scopes), created_at, revoked_at`, id, at)
	record, err := scanAdminToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.TokenRecord{}, auth.ErrNotFound
	}
	return record, wrap("revoke administrator token", err)
}

type rowScanner interface {
	Scan(...any) error
}

func scanAdminToken(row rowScanner) (auth.TokenRecord, error) {
	var record auth.TokenRecord
	var hash []byte
	var scopesJSON []byte
	if err := row.Scan(&record.ID, &record.ActorID, &record.Prefix, &hash, &scopesJSON, &record.CreatedAt, &record.RevokedAt); err != nil {
		return auth.TokenRecord{}, err
	}
	if len(hash) != sha256.Size {
		return auth.TokenRecord{}, fmt.Errorf("administrator token %s has invalid hash length %d", record.ID, len(hash))
	}
	if err := json.Unmarshal(scopesJSON, &record.Scopes); err != nil {
		return auth.TokenRecord{}, fmt.Errorf("administrator token %s has invalid scopes: %w", record.ID, err)
	}
	copy(record.Hash[:], hash)
	return record, nil
}

func scanAdminTokens(rows *sql.Rows) ([]auth.TokenRecord, error) {
	records := make([]auth.TokenRecord, 0)
	for rows.Next() {
		record, err := scanAdminToken(rows)
		if err != nil {
			return nil, wrap("scan administrator token", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate administrator tokens", err)
	}
	return records, nil
}

func (s *Store) Execute(
	ctx context.Context,
	candidate auth.IdempotencyRecord,
	now time.Time,
	operation auth.IdempotentOperation,
) (record auth.IdempotencyRecord, executed bool, resultErr error) {
	resultErr = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		claim, err := tx.ExecContext(txCtx, `
			INSERT INTO idempotency_records
			  (actor_id, operation, key, request_hash, response_status, response_body, expires_at)
			VALUES ($1, $2, $3, $4, 0, $5, $6)
			ON CONFLICT (actor_id, operation, key) DO NOTHING`,
			candidate.ActorID, candidate.Operation, candidate.Key, candidate.RequestFingerprint[:], []byte{}, candidate.ExpiresAt)
		if err != nil {
			return wrap("claim administrator idempotency key", err)
		}
		claimed, err := claim.RowsAffected()
		if err != nil {
			return wrap("inspect administrator idempotency claim", err)
		}

		var stored auth.IdempotencyRecord
		var fingerprint []byte
		err = tx.QueryRowContext(txCtx, `
			SELECT actor_id::text, operation, key, request_hash, response_status, response_body, expires_at
			FROM idempotency_records
			WHERE actor_id = $1 AND operation = $2 AND key = $3
			FOR UPDATE`, candidate.ActorID, candidate.Operation, candidate.Key).Scan(
			&stored.ActorID, &stored.Operation, &stored.Key, &fingerprint,
			&stored.ResponseStatus, &stored.ResponseBody, &stored.ExpiresAt)
		if err != nil {
			return wrap("lock administrator idempotency key", err)
		}
		if len(fingerprint) != sha256.Size {
			return fmt.Errorf("stored administrator request hash has length %d", len(fingerprint))
		}
		copy(stored.RequestFingerprint[:], fingerprint)
		if claimed == 0 && stored.ExpiresAt.After(now) {
			record = stored
			executed = false
			return nil
		}

		_, err = tx.ExecContext(txCtx, `
			UPDATE idempotency_records
			SET request_hash = $4, response_status = 0, response_body = $5, expires_at = $6
			WHERE actor_id = $1 AND operation = $2 AND key = $3`,
			candidate.ActorID, candidate.Operation, candidate.Key, candidate.RequestFingerprint[:], []byte{}, candidate.ExpiresAt)
		if err != nil {
			return wrap("replace expired administrator idempotency key", err)
		}
		response, err := operation(txCtx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(txCtx, `
			UPDATE idempotency_records SET response_status = $4, response_body = $5
			WHERE actor_id = $1 AND operation = $2 AND key = $3`,
			candidate.ActorID, candidate.Operation, candidate.Key, response.Status, response.Body)
		if err != nil {
			return wrap("record administrator idempotency response", err)
		}
		candidate.ResponseStatus = response.Status
		candidate.ResponseBody = append([]byte(nil), response.Body...)
		record = candidate
		executed = true
		return nil
	})
	if resultErr != nil {
		return auth.IdempotencyRecord{}, false, resultErr
	}
	return record, executed, nil
}
