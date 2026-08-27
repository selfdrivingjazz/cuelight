package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmill/cable/internal/control/media"
)

var _ media.Repository = (*Store)(nil)

const (
	mediaCreateUpload   = "media.create-upload"
	mediaCompleteUpload = "media.complete-upload"
	mediaRegisterAsset  = "media.register-asset"
)

func (s *Store) CreateUpload(ctx context.Context, mutation media.Mutation, upload media.Upload) (media.Upload, error) {
	digest, err := jsonDigest(struct {
		Name           string
		DeclaredLength int64
		MediaType      string
	}{upload.Name, upload.DeclaredLength, upload.MediaType})
	if err != nil {
		return media.Upload{}, err
	}
	var result media.Upload
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		responseID, replayed, err := s.mediaIdempotency(txCtx, tx, mediaCreateUpload, mutation, digest, func() (string, error) {
			_, err := tx.ExecContext(txCtx, `
				INSERT INTO uploads
				  (id, name, declared_length, received_length, media_type, state, artifact_digest, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8)`,
				upload.ID, upload.Name, upload.DeclaredLength, upload.ReceivedLength,
				upload.MediaType, upload.State, upload.ArtifactDigest, upload.CreatedBy)
			if err != nil {
				return "", wrap("create upload", err)
			}
			return upload.ID, nil
		})
		if err != nil {
			return err
		}
		if replayed {
			result, err = s.upload(txCtx, tx, responseID, false)
			return err
		}
		result = upload
		return nil
	})
	return result, err
}

func (s *Store) Upload(ctx context.Context, id string) (media.Upload, error) {
	return s.upload(ctx, s.q(ctx), id, false)
}

func (s *Store) upload(ctx context.Context, q queryer, id string, lock bool) (media.Upload, error) {
	query := `
		SELECT id::text, name, declared_length, received_length, media_type, state,
		       COALESCE(artifact_digest, ''), created_by::text
		FROM uploads WHERE id = $1`
	if lock {
		query += " FOR UPDATE"
	}
	var upload media.Upload
	err := q.QueryRowContext(ctx, query, id).Scan(&upload.ID, &upload.Name, &upload.DeclaredLength,
		&upload.ReceivedLength, &upload.MediaType, &upload.State, &upload.ArtifactDigest, &upload.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Upload{}, media.ErrNotFound
	}
	return upload, wrap("read upload", err)
}

func (s *Store) UploadRange(ctx context.Context, uploadID string, start int64) (media.UploadRange, error) {
	var value media.UploadRange
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT start_offset, end_offset FROM upload_ranges
		WHERE upload_id = $1 AND start_offset = $2`, uploadID, start).Scan(&value.Start, &value.End)
	if errors.Is(err, sql.ErrNoRows) {
		return media.UploadRange{}, media.ErrNotFound
	}
	return value, wrap("read upload range", err)
}

func (s *Store) AdvanceUpload(ctx context.Context, id string, expected, end int64) (media.Upload, error) {
	var advanced media.Upload
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(txCtx, `
			UPDATE uploads SET received_length = $3, updated_at = now()
			WHERE id = $1 AND state = 'open' AND received_length = $2 AND declared_length >= $3`, id, expected, end)
		if err != nil {
			return wrap("advance upload", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return wrap("inspect upload advance", err)
		}
		if changed == 0 {
			return media.ErrConflict
		}
		_, err = tx.ExecContext(txCtx, `
			INSERT INTO upload_ranges (upload_id, start_offset, end_offset) VALUES ($1, $2, $3)`, id, expected, end)
		if postgresCode(err) == "23505" {
			return media.ErrConflict
		}
		if err != nil {
			return wrap("record upload range", err)
		}
		advanced, err = s.upload(txCtx, tx, id, false)
		return err
	})
	return advanced, err
}

func (s *Store) BeginFinalization(ctx context.Context, id string, expectedLength int64) (media.Upload, error) {
	var upload media.Upload
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(txCtx, `
			UPDATE uploads SET state = 'finalizing', updated_at = now()
			WHERE id = $1 AND state = 'open' AND received_length = $2 AND declared_length = $2`, id, expectedLength)
		if err != nil {
			return wrap("begin upload finalization", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return wrap("inspect upload finalization", err)
		}
		if changed == 0 {
			return media.ErrConflict
		}
		upload, err = s.upload(txCtx, tx, id, false)
		return err
	})
	return upload, err
}

func (s *Store) CompleteFinalization(ctx context.Context, mutation media.Mutation, uploadID string, artifact media.Artifact, asset media.Asset) (media.Asset, error) {
	digest, err := jsonDigest(struct {
		UploadID string
		Artifact media.Artifact
		Name     string
	}{uploadID, artifact, asset.Name})
	if err != nil {
		return media.Asset{}, err
	}
	var completed media.Asset
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		upload, err := s.upload(txCtx, tx, uploadID, true)
		if err != nil {
			return err
		}
		if upload.State == media.UploadComplete {
			completed, err = s.completedAsset(txCtx, tx, uploadID)
			return err
		}
		if upload.State != media.UploadFinalizing {
			return media.ErrConflict
		}
		responseID, replayed, err := s.mediaIdempotency(txCtx, tx, mediaCompleteUpload, mutation, digest, func() (string, error) {
			if err := s.upsertArtifact(txCtx, tx, artifact); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(txCtx, `INSERT INTO assets (id, name) VALUES ($1, $2)`, asset.ID, asset.Name); err != nil {
				return "", wrap("create completed asset", err)
			}
			if _, err := tx.ExecContext(txCtx, `
				INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, asset.ID, artifact.Digest); err != nil {
				return "", wrap("link completed asset artifact", err)
			}
			result, err := tx.ExecContext(txCtx, `
				UPDATE uploads
				SET state = 'complete', artifact_digest = $2, completed_asset_id = $3, failure_reason = NULL, updated_at = now()
				WHERE id = $1 AND state = 'finalizing'`, uploadID, artifact.Digest, asset.ID)
			if err != nil {
				return "", wrap("complete upload", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return "", wrap("inspect upload completion", err)
			}
			if changed != 1 {
				return "", media.ErrConflict
			}
			return asset.ID, nil
		})
		if err != nil {
			return err
		}
		if replayed {
			completed, err = s.asset(txCtx, tx, responseID)
			return err
		}
		asset.Artifact = artifact
		completed = asset
		return nil
	})
	return completed, err
}

func (s *Store) CompletedAsset(ctx context.Context, uploadID string) (media.Asset, error) {
	return s.completedAsset(ctx, s.q(ctx), uploadID)
}

func (s *Store) completedAsset(ctx context.Context, q queryer, uploadID string) (media.Asset, error) {
	var assetID sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT completed_asset_id::text FROM uploads
		WHERE id = $1 AND state = 'complete'`, uploadID).Scan(&assetID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !assetID.Valid {
		return media.Asset{}, media.ErrNotFound
	}
	if err != nil {
		return media.Asset{}, wrap("find completed upload asset", err)
	}
	return s.asset(ctx, q, assetID.String)
}

func (s *Store) FailUpload(ctx context.Context, id, reason string) error {
	result, err := s.q(ctx).ExecContext(ctx, `
		UPDATE uploads SET state = 'failed', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND state <> 'complete'`, id, reason)
	if err != nil {
		return wrap("fail upload", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrap("inspect failed upload", err)
	}
	if changed == 0 {
		return media.ErrNotFound
	}
	return nil
}

func (s *Store) RegisterAsset(ctx context.Context, mutation media.Mutation, asset media.Asset) (media.Asset, error) {
	digest, err := jsonDigest(struct {
		Name     string
		Artifact media.Artifact
	}{asset.Name, asset.Artifact})
	if err != nil {
		return media.Asset{}, err
	}
	var registered media.Asset
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		responseID, replayed, err := s.mediaIdempotency(txCtx, tx, mediaRegisterAsset, mutation, digest, func() (string, error) {
			if err := s.upsertArtifact(txCtx, tx, asset.Artifact); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(txCtx, `INSERT INTO assets (id, name) VALUES ($1, $2)`, asset.ID, asset.Name); err != nil {
				return "", wrap("register asset", err)
			}
			if _, err := tx.ExecContext(txCtx, `
				INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, asset.ID, asset.Artifact.Digest); err != nil {
				return "", wrap("link registered asset", err)
			}
			return asset.ID, nil
		})
		if err != nil {
			return err
		}
		if replayed {
			registered, err = s.asset(txCtx, tx, responseID)
			return err
		}
		registered = asset
		return nil
	})
	return registered, err
}

func (s *Store) mediaIdempotency(ctx context.Context, tx *sql.Tx, operation string, mutation media.Mutation, requestHash string, execute func() (string, error)) (string, bool, error) {
	claim, err := tx.ExecContext(ctx, `
		INSERT INTO media_idempotency_records (operation, actor_id, key, request_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (operation, actor_id, key) DO NOTHING`, operation, mutation.ActorID, mutation.IdempotencyKey, requestHash)
	if err != nil {
		return "", false, wrap("claim media idempotency key", err)
	}
	claimed, err := claim.RowsAffected()
	if err != nil {
		return "", false, wrap("inspect media idempotency claim", err)
	}
	var storedHash string
	var responseID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT request_hash, response_id::text FROM media_idempotency_records
		WHERE operation = $1 AND actor_id = $2 AND key = $3 FOR UPDATE`, operation, mutation.ActorID, mutation.IdempotencyKey).Scan(&storedHash, &responseID)
	if err != nil {
		return "", false, wrap("lock media idempotency key", err)
	}
	if claimed == 0 {
		if storedHash != requestHash || !responseID.Valid {
			return "", false, media.ErrConflict
		}
		return responseID.String, true, nil
	}
	id, err := execute()
	if err != nil {
		return "", false, err
	}
	if id == "" {
		return "", false, errors.New("media repository produced an empty replay ID")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE media_idempotency_records SET response_id = $4
		WHERE operation = $1 AND actor_id = $2 AND key = $3`, operation, mutation.ActorID, mutation.IdempotencyKey, id)
	if err != nil {
		return "", false, wrap("record media idempotency response", err)
	}
	return id, false, nil
}

func (s *Store) upsertArtifact(ctx context.Context, tx *sql.Tx, artifact media.Artifact) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts (digest, size_bytes, media_type, state)
		VALUES ($1, $2, $3, $4) ON CONFLICT (digest) DO NOTHING`,
		artifact.Digest, artifact.Size, artifact.MediaType, artifact.State)
	if err != nil {
		return wrap("upsert artifact", err)
	}
	var size int64
	var mediaType string
	err = tx.QueryRowContext(ctx, `SELECT size_bytes, media_type FROM artifacts WHERE digest = $1 FOR UPDATE`, artifact.Digest).Scan(&size, &mediaType)
	if err != nil {
		return wrap("verify artifact binding", err)
	}
	if size != artifact.Size || mediaType != artifact.MediaType {
		return fmt.Errorf("%w: digest %s", ErrArtifactBinding, artifact.Digest)
	}
	_, err = tx.ExecContext(ctx, `UPDATE artifacts SET state = $2 WHERE digest = $1`, artifact.Digest, artifact.State)
	return wrap("update artifact state", err)
}

func (s *Store) asset(ctx context.Context, q queryer, id string) (media.Asset, error) {
	var asset media.Asset
	err := q.QueryRowContext(ctx, `
		SELECT a.id::text, a.name, r.digest, r.size_bytes, r.media_type, r.state
		FROM assets a
		JOIN asset_artifacts aa ON aa.asset_id = a.id AND aa.role = 'original'
		JOIN artifacts r ON r.digest = aa.artifact_digest
		WHERE a.id = $1`, id).Scan(&asset.ID, &asset.Name, &asset.Artifact.Digest,
		&asset.Artifact.Size, &asset.Artifact.MediaType, &asset.Artifact.State)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Asset{}, media.ErrNotFound
	}
	return asset, wrap("read asset", err)
}

func jsonDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode idempotency request: %w", err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// IncompleteUploads returns durable upload state needed by startup crash reconciliation.
func (s *Store) IncompleteUploads(ctx context.Context) ([]media.Upload, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT id::text, name, declared_length, received_length, media_type, state,
		       COALESCE(artifact_digest, ''), created_by::text
		FROM uploads WHERE state IN ('open', 'finalizing', 'failed') ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap("list incomplete uploads", err)
	}
	defer rows.Close()
	result := make([]media.Upload, 0)
	for rows.Next() {
		var upload media.Upload
		if err := rows.Scan(&upload.ID, &upload.Name, &upload.DeclaredLength, &upload.ReceivedLength,
			&upload.MediaType, &upload.State, &upload.ArtifactDigest, &upload.CreatedBy); err != nil {
			return nil, wrap("scan incomplete upload", err)
		}
		result = append(result, upload)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate incomplete uploads", err)
	}
	return result, nil
}

func (s *Store) SetArtifactState(ctx context.Context, digest string, state media.ArtifactState) error {
	result, err := s.q(ctx).ExecContext(ctx, `UPDATE artifacts SET state = $2 WHERE digest = $1`, digest, state)
	if err != nil {
		return wrap("set artifact reconciliation state", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrap("inspect artifact state update", err)
	}
	if changed == 0 {
		return media.ErrNotFound
	}
	return nil
}
