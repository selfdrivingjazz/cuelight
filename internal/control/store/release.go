package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	controlrelease "github.com/jmilldotdev/cuelight/internal/control/release"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
)

var _ controlrelease.Repository = (*Store)(nil)
var _ controlrelease.ArtifactRepository = (*Store)(nil)

func (s *Store) ReleaseArtifactByDigest(ctx context.Context, digest string) (controlrelease.Artifact, error) {
	var artifact controlrelease.Artifact
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT digest, size_bytes, media_type, state
		FROM artifacts WHERE digest = $1`, digest).Scan(&artifact.Digest, &artifact.Size, &artifact.MediaType, &artifact.State)
	if err != nil {
		return controlrelease.Artifact{}, wrap("read release artifact", err)
	}
	return artifact, nil
}

func (s *Store) ArtifactsForAsset(ctx context.Context, assetID string) ([]controlrelease.Artifact, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT r.digest, r.size_bytes, r.media_type, r.state
		FROM asset_artifacts aa
		JOIN artifacts r ON r.digest = aa.artifact_digest
		WHERE aa.asset_id = $1
		ORDER BY aa.role, r.digest`, assetID)
	if err != nil {
		return nil, wrap("list release artifacts", err)
	}
	defer rows.Close()
	artifacts := make([]controlrelease.Artifact, 0)
	for rows.Next() {
		var artifact controlrelease.Artifact
		if err := rows.Scan(&artifact.Digest, &artifact.Size, &artifact.MediaType, &artifact.State); err != nil {
			return nil, wrap("scan release artifact", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate release artifacts", err)
	}
	return artifacts, nil
}

func (s *Store) Publish(
	ctx context.Context,
	publication controlrelease.Publication,
	prepare func() (controlrelease.PreparedPublication, error),
	create func(string, controlrelease.PreparedPublication) (canonical.EnvelopeV1, error),
) (controlrelease.PublishResult, error) {
	var result controlrelease.PublishResult
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		lockKey := fmt.Sprintf("%d:%s%s", len(publication.ActorID), publication.ActorID, publication.IdempotencyKey)
		if _, err := tx.ExecContext(txCtx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return wrap("lock release idempotency key", err)
		}
		var storedRequest, releaseID string
		err := tx.QueryRowContext(txCtx, `
			SELECT request_digest, release_id::text FROM release_publications
			WHERE actor_id = $1 AND key = $2 FOR UPDATE`, publication.ActorID, publication.IdempotencyKey).Scan(&storedRequest, &releaseID)
		if err == nil {
			if storedRequest != publication.RequestDigest {
				return controlrelease.ErrIdempotencyConflict
			}
			envelope, err := s.releaseEnvelope(txCtx, tx, releaseID)
			if err != nil {
				return err
			}
			result = controlrelease.PublishResult{Envelope: envelope, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return wrap("read release idempotency record", err)
		}

		prepared, err := prepare()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(txCtx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, prepared.Digest); err != nil {
			return wrap("lock release digest", err)
		}
		var existingID string
		err = tx.QueryRowContext(txCtx, `SELECT id::text FROM releases WHERE digest = $1`, prepared.Digest).Scan(&existingID)
		if err == nil {
			envelope, err := s.releaseEnvelope(txCtx, tx, existingID)
			if err != nil {
				return err
			}
			if err := recordPublication(txCtx, tx, publication, envelope); err != nil {
				return err
			}
			result = controlrelease.PublishResult{Envelope: envelope, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return wrap("deduplicate release digest", err)
		}

		newID, err := s.newUUID()
		if err != nil {
			return err
		}
		envelope, err := create(newID, prepared)
		if err != nil {
			return err
		}
		actualDigest, _, digestErr := canonical.Digest(envelope.Payload)
		if envelope.ID != newID || envelope.Digest != prepared.Digest || actualDigest != prepared.Digest || digestErr != nil {
			return controlrelease.ErrSignerContract
		}
		signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
		if err != nil {
			return fmt.Errorf("%w: invalid signature encoding", controlrelease.ErrSignerContract)
		}
		payload, err := json.Marshal(envelope.Payload)
		if err != nil {
			return fmt.Errorf("encode release payload: %w", err)
		}
		_, err = tx.ExecContext(txCtx, `
			INSERT INTO releases (id, digest, key_id, signature, payload, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)`, envelope.ID, envelope.Digest, envelope.KeyID, signature, payload, publication.ActorID)
		if err != nil {
			return wrap("insert release", err)
		}
		for _, artifact := range envelope.Payload.Artifacts {
			if _, err := tx.ExecContext(txCtx, `
				INSERT INTO release_artifacts (release_id, artifact_digest) VALUES ($1, $2)`, envelope.ID, artifact.Digest); err != nil {
				return wrap("link release artifact", err)
			}
		}
		if err := recordPublication(txCtx, tx, publication, envelope); err != nil {
			return err
		}
		result = controlrelease.PublishResult{Envelope: envelope}
		return nil
	})
	return result, err
}

func recordPublication(ctx context.Context, tx *sql.Tx, publication controlrelease.Publication, envelope canonical.EnvelopeV1) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO release_publications
		  (actor_id, key, request_digest, release_id, release_digest)
		VALUES ($1, $2, $3, $4, $5)`, publication.ActorID, publication.IdempotencyKey,
		publication.RequestDigest, envelope.ID, envelope.Digest)
	return wrap("record release publication", err)
}

func (s *Store) releaseEnvelope(ctx context.Context, q queryer, id string) (canonical.EnvelopeV1, error) {
	var envelope canonical.EnvelopeV1
	var signature, payload []byte
	err := q.QueryRowContext(ctx, `
		SELECT id::text, digest, key_id, signature, payload
		FROM releases WHERE id = $1`, id).Scan(&envelope.ID, &envelope.Digest, &envelope.KeyID, &signature, &payload)
	if err != nil {
		return canonical.EnvelopeV1{}, wrap("read release", err)
	}
	if err := json.Unmarshal(payload, &envelope.Payload); err != nil {
		return canonical.EnvelopeV1{}, fmt.Errorf("decode stored release %s: %w", id, err)
	}
	envelope.Signature = base64.StdEncoding.EncodeToString(signature)
	return envelope, nil
}
