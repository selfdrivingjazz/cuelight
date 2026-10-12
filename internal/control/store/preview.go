package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/selfdrivingjazz/cuelight/internal/control/preview"
	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
	"io"
	"strings"
	"time"
)

type PreviewRepository struct {
	*Store
	CAS *localcas.Store
}

func NewPreviewRepository(s *Store) *PreviewRepository {
	if s == nil {
		return nil
	}
	return &PreviewRepository{Store: s}
}

var _ preview.Repository = (*PreviewRepository)(nil)
var _ preview.ArtifactCatalog = (*PreviewRepository)(nil)

// Create atomically writes the screenshot job, idempotency record, and audit
// event. The idempotency row is intentionally not a database FK: it is claimed
// before the job is written, allowing concurrent requests to serialize on the
// natural (actor,key) unique index without creating duplicate jobs.
func (s *PreviewRepository) Create(ctx context.Context, req preview.CreateRequest) (preview.ScreenshotJob, bool, error) {
	var result preview.ScreenshotJob
	var replayed bool
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		claim, err := tx.ExecContext(txCtx, `
			INSERT INTO screenshot_idempotency_records
			  (actor_id, key, request_digest, job_id, expires_at)
			VALUES ($1::uuid, $2, $3, $4::uuid, $5)
			ON CONFLICT (actor_id, key) DO NOTHING`,
			req.ActorID, req.IdempotencyKey, req.RequestDigest, req.Job.ID,
			req.IdempotencyExpiresAt.UTC())
		if err != nil {
			return wrap("claim screenshot idempotency key", err)
		}
		claimed, err := claim.RowsAffected()
		if err != nil {
			return wrap("inspect screenshot idempotency claim", err)
		}
		var digest, jobID string
		var expires time.Time
		err = tx.QueryRowContext(txCtx, `
			SELECT request_digest, job_id::text, expires_at
			FROM screenshot_idempotency_records
			WHERE actor_id = $1::uuid AND key = $2
			FOR UPDATE`, req.ActorID, req.IdempotencyKey).Scan(&digest, &jobID, &expires)
		if err != nil {
			return wrap("lock screenshot idempotency key", err)
		}
		if claimed == 0 {
			if expires.After(req.Job.RequestedAt) {
				if digest != req.RequestDigest {
					return preview.ErrIdempotencyConflict
				}
				result, err = s.screenshotJob(txCtx, tx, jobID, false)
				if err != nil {
					return err
				}
				replayed = true
				return nil
			}
			if _, err := tx.ExecContext(txCtx, `DELETE FROM screenshot_idempotency_records WHERE actor_id = $1::uuid AND key = $2`, req.ActorID, req.IdempotencyKey); err != nil {
				return wrap("discard expired screenshot idempotency key", err)
			}
			claim, err = tx.ExecContext(txCtx, `
				INSERT INTO screenshot_idempotency_records
				  (actor_id, key, request_digest, job_id, expires_at)
				VALUES ($1::uuid, $2, $3, $4::uuid, $5)`,
				req.ActorID, req.IdempotencyKey, req.RequestDigest, req.Job.ID, req.IdempotencyExpiresAt.UTC())
			if err != nil {
				return wrap("replace expired screenshot idempotency key", err)
			}
		}
		if claimed == 0 {
			return errors.New("screenshot idempotency key was not claimed")
		}
		body, err := json.Marshal(req.Job.Expected)
		if err != nil {
			return fmt.Errorf("encode expected screenshot preview: %w", err)
		}
		_, err = tx.ExecContext(txCtx, `
			INSERT INTO screenshot_jobs
			  (id, node_id, expected, requested_at, expires_at, status,
			   retention_policy, retention_until, artifact_retention_until, created_by)
			VALUES ($1::uuid, $2::uuid, $3::jsonb, $4, $5, $6, $7, $8, $9, $10::uuid)`,
			req.Job.ID, req.Job.NodeID, body, req.Job.RequestedAt.UTC(), req.Job.ExpiresAt.UTC(),
			req.Job.Status, req.Job.RetentionPolicy, req.Job.RetentionUntil.UTC(), req.Job.ArtifactRetentionUntil.UTC(), req.ActorID)
		if err != nil {
			if postgresCode(err) == "23505" {
				return fmt.Errorf("%w: duplicate job id", preview.ErrConflict)
			}
			return wrap("create screenshot job", err)
		}
		if err := s.insertScreenshotAudit(txCtx, tx, req.Audit, req.Job); err != nil {
			return err
		}
		result = req.Job
		return nil
	})
	return result, replayed, err
}

func (s *PreviewRepository) Get(ctx context.Context, id string) (preview.ScreenshotJob, bool, error) {
	job, err := s.screenshotJob(ctx, s.q(ctx), id, false)
	if errors.Is(err, preview.ErrNotFound) {
		return preview.ScreenshotJob{}, false, nil
	}
	return job, err == nil, err
}

func (s *PreviewRepository) screenshotJob(ctx context.Context, q queryer, id string, lock bool) (preview.ScreenshotJob, error) {
	query := `SELECT id::text, node_id::text, expected, requested_at, expires_at,
		claimed_at, completion_at, status, COALESCE(result_artifact_digest, ''),
		COALESCE(result_media_type, ''), COALESCE(error_reason, ''), retention_policy,
		retention_until, artifact_retention_until FROM screenshot_jobs WHERE id = $1::uuid`
	if lock {
		query += " FOR UPDATE"
	}
	var job preview.ScreenshotJob
	var expected []byte
	var claimedAt, completionAt sql.NullTime
	if err := q.QueryRowContext(ctx, query, id).Scan(&job.ID, &job.NodeID, &expected,
		&job.RequestedAt, &job.ExpiresAt, &claimedAt, &completionAt, &job.Status,
		&job.ResultArtifactDigest, &job.ResultMediaType, &job.ErrorReason,
		&job.RetentionPolicy, &job.RetentionUntil, &job.ArtifactRetentionUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return preview.ScreenshotJob{}, preview.ErrNotFound
		}
		return preview.ScreenshotJob{}, wrap("read screenshot job", err)
	}
	if err := json.Unmarshal(expected, &job.Expected); err != nil {
		return preview.ScreenshotJob{}, fmt.Errorf("decode screenshot expected preview: %w", err)
	}
	if claimedAt.Valid {
		v := claimedAt.Time.UTC()
		job.ClaimedAt = &v
	}
	if completionAt.Valid {
		v := completionAt.Time.UTC()
		job.CompletionAt = &v
	}
	return job, nil
}

func (s *PreviewRepository) Claim(ctx context.Context, nodeID, id string, now time.Time, audit preview.AuditEvent) (preview.ScreenshotJob, error) {
	var result preview.ScreenshotJob
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		job, err := s.screenshotJob(txCtx, tx, id, true)
		if err != nil {
			return err
		}
		if job.NodeID != nodeID {
			return preview.ErrForbidden
		}
		if job.Status == preview.StatusExpired {
			return preview.ErrExpired
		}
		if job.Status == preview.StatusCompleted || job.Status == preview.StatusFailed || job.Status == preview.StatusUnsupported {
			return preview.ErrTerminal
		}
		if !now.Before(job.ExpiresAt) {
			job.Status = preview.StatusExpired
			job.ErrorReason = "job TTL elapsed"
			job.CompletionAt = ptrTime(now)
			audit.Action = "screenshot.expired"
			audit.Detail = job.ErrorReason
			if err := s.expireLocked(txCtx, tx, job, audit); err != nil {
				return err
			}
			return preview.ErrExpired
		}
		if job.Status != preview.StatusQueued {
			return preview.ErrConflict
		}
		_, err = tx.ExecContext(txCtx, `UPDATE screenshot_jobs SET status = 'claimed', claimed_at = $2, updated_at = $2 WHERE id = $1::uuid`, id, now.UTC())
		if err != nil {
			return wrap("claim screenshot job", err)
		}
		job.Status = preview.StatusClaimed
		job.ClaimedAt = ptrTime(now)
		if err := s.insertScreenshotAudit(txCtx, tx, audit, job); err != nil {
			return err
		}
		result = job
		return nil
	})
	return result, err
}

func (s *PreviewRepository) Complete(ctx context.Context, nodeID, id string, now time.Time, completion preview.Completion, audit preview.AuditEvent) (preview.ScreenshotJob, error) {
	var result preview.ScreenshotJob
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		job, err := s.screenshotJob(txCtx, tx, id, true)
		if err != nil {
			return err
		}
		if job.NodeID != nodeID {
			return preview.ErrForbidden
		}
		if job.Status == preview.StatusExpired {
			return preview.ErrExpired
		}
		if job.Status == preview.StatusCompleted || job.Status == preview.StatusFailed || job.Status == preview.StatusUnsupported {
			return preview.ErrTerminal
		}
		if job.Status != preview.StatusClaimed {
			return preview.ErrNotClaimed
		}
		if !now.Before(job.ExpiresAt) {
			job.Status = preview.StatusExpired
			job.ErrorReason = "job TTL elapsed"
			job.CompletionAt = ptrTime(now)
			audit.Action = "screenshot.expired"
			audit.Detail = job.ErrorReason
			if err := s.expireLocked(txCtx, tx, job, audit); err != nil {
				return err
			}
		}
		if completion.Status == preview.StatusCompleted {
			var ready bool
			if err := tx.QueryRowContext(txCtx, `SELECT EXISTS (SELECT 1 FROM artifacts WHERE digest = $1 AND media_type = $2 AND state = 'ready')`, completion.ArtifactDigest, completion.MediaType).Scan(&ready); err != nil {
				return wrap("verify screenshot artifact", err)
			}
			if !ready {
				return preview.ErrArtifactUnavailable
			}
		}
		_, err = tx.ExecContext(txCtx, `UPDATE screenshot_jobs SET status = $2, completion_at = $3, result_artifact_digest = NULLIF($4, ''), result_media_type = NULLIF($5, ''), error_reason = NULLIF($6, ''), updated_at = $3 WHERE id = $1::uuid`, id, completion.Status, now.UTC(), completion.ArtifactDigest, completion.MediaType, completion.ErrorReason)
		if err != nil {
			return wrap("complete screenshot job", err)
		}
		job.Status = completion.Status
		job.CompletionAt = ptrTime(now)
		job.ResultArtifactDigest = completion.ArtifactDigest
		job.ResultMediaType = completion.MediaType
		job.ErrorReason = completion.ErrorReason
		if err := s.insertScreenshotAudit(txCtx, tx, audit, job); err != nil {
			return err
		}
		result = job
		return nil
	})
	return result, err
}

func (s *PreviewRepository) Expire(ctx context.Context, id string, now time.Time, audit preview.AuditEvent) (preview.ScreenshotJob, bool, error) {
	var result preview.ScreenshotJob
	var changed bool
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		job, err := s.screenshotJob(txCtx, tx, id, true)
		if errors.Is(err, preview.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.Status == preview.StatusCompleted || job.Status == preview.StatusFailed || job.Status == preview.StatusUnsupported || job.Status == preview.StatusExpired || now.Before(job.ExpiresAt) {
			result = job
			return nil
		}
		job.Status = preview.StatusExpired
		job.ErrorReason = "job TTL elapsed"
		job.CompletionAt = ptrTime(now)
		if err := s.expireLocked(txCtx, tx, job, audit); err != nil {
			return err
		}
		result, changed = job, true
		return nil
	})
	return result, changed, err
}

func (s *PreviewRepository) expireLocked(ctx context.Context, tx *sql.Tx, job preview.ScreenshotJob, audit preview.AuditEvent) error {
	_, err := tx.ExecContext(ctx, `UPDATE screenshot_jobs SET status = 'expired', completion_at = $2, error_reason = $3, updated_at = $2 WHERE id = $1::uuid`, job.ID, job.CompletionAt.UTC(), job.ErrorReason)
	if err != nil {
		return wrap("expire screenshot job", err)
	}
	return s.insertScreenshotAudit(ctx, tx, audit, job)
}

func (s *PreviewRepository) insertScreenshotAudit(ctx context.Context, tx *sql.Tx, event preview.AuditEvent, job preview.ScreenshotJob) error {
	result, err := json.Marshal(struct {
		JobID          string         `json:"jobId"`
		Status         preview.Status `json:"status"`
		ArtifactDigest string         `json:"artifactDigest,omitempty"`
		MediaType      string         `json:"mediaType,omitempty"`
		ErrorReason    string         `json:"errorReason,omitempty"`
	}{JobID: job.ID, Status: job.Status, ArtifactDigest: job.ResultArtifactDigest, MediaType: job.ResultMediaType, ErrorReason: job.ErrorReason})
	if err != nil {
		return fmt.Errorf("encode screenshot audit result: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events
		  (id, actor_id, node_id, action, reason, result, correlation_id, created_at)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, NULLIF($5, ''), $6::jsonb, $7, $8)`,
		normalizeUUID(event.ID), event.ActorID, event.NodeID, event.Action, event.Detail, result, event.ID, event.At.UTC())
	if err != nil {
		return wrap("record screenshot audit", err)
	}
	return nil
}
func (s *PreviewRepository) Next(ctx context.Context, nodeID string, now time.Time) (preview.ScreenshotJob, bool, error) {
	var id string
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT id::text FROM screenshot_jobs
		WHERE node_id = $1::uuid AND status = 'queued' AND expires_at > $2
		ORDER BY requested_at, id LIMIT 1`, nodeID, now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return preview.ScreenshotJob{}, false, nil
	}
	if err != nil {
		return preview.ScreenshotJob{}, false, wrap("discover screenshot job", err)
	}
	job, err := s.screenshotJob(ctx, s.q(ctx), id, false)
	return job, err == nil, err
}

func (s *PreviewRepository) StoreScreenshotArtifact(ctx context.Context, digest, mediaType string, size int64, body io.Reader) error {
	if s.CAS == nil || body == nil || size <= 0 || mediaType != "image/png" {
		return errors.New("screenshot artifact sink is not configured")
	}
	object, err := s.CAS.Put(body, digest)
	if err != nil {
		return wrap("store screenshot bytes", err)
	}
	if object.Size != size || object.Digest != digest {
		return preview.ErrArtifactMismatch
	}
	_, err = s.q(ctx).ExecContext(ctx, `
		INSERT INTO artifacts (digest, size_bytes, media_type, state)
		VALUES ($1, $2, $3, 'ready')
		ON CONFLICT (digest) DO UPDATE SET
		  size_bytes = EXCLUDED.size_bytes, media_type = EXCLUDED.media_type, state = 'ready'
		WHERE artifacts.size_bytes = EXCLUDED.size_bytes AND artifacts.media_type = EXCLUDED.media_type`,
		digest, size, mediaType)
	if err != nil {
		return wrap("register screenshot artifact", err)
	}
	return nil
}

func (s *PreviewRepository) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	if s.CAS == nil {
		return nil, errors.New("screenshot artifact reader is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.CAS.Open(digest)
}

func (s *PreviewRepository) Stat(ctx context.Context, digest string) (preview.ArtifactStat, error) {
	if s.CAS == nil {
		return preview.ArtifactStat{}, errors.New("screenshot artifact reader is not configured")
	}
	if err := ctx.Err(); err != nil {
		return preview.ArtifactStat{}, err
	}
	object, err := s.CAS.Stat(digest)
	if err != nil {
		return preview.ArtifactStat{}, err
	}
	return preview.ArtifactStat{Digest: object.Digest, Size: object.Size}, nil
}

func (s *PreviewRepository) LookupArtifact(ctx context.Context, digest string) (preview.ArtifactRecord, error) {
	var result preview.ArtifactRecord
	err := s.q(ctx).QueryRowContext(ctx, `SELECT digest, media_type, state FROM artifacts WHERE digest = $1`, digest).Scan(&result.Digest, &result.MediaType, &result.State)
	if errors.Is(err, sql.ErrNoRows) {
		return preview.ArtifactRecord{}, preview.ErrArtifactUnavailable
	}
	if err != nil {
		return preview.ArtifactRecord{}, wrap("read screenshot artifact", err)
	}
	return result, nil
}

// RetentionCleanup describes rows removed from the screenshot-job table and
// result objects that are no longer referenced by retained jobs. The caller
// owns deletion from the byte CAS after this transaction commits.
type RetentionCleanup struct {
	JobIDs          []string
	ArtifactDigests []string
}

// CleanupScreenshotRetention deterministically removes at most limit jobs
// whose retention has elapsed, oldest retention deadline first. Artifact
// digests are returned only when no screenshot job still references them and
// their artifact retention deadline has elapsed.
func (s *PreviewRepository) CleanupScreenshotRetention(ctx context.Context, now time.Time, limit int) (RetentionCleanup, error) {
	if limit <= 0 {
		limit = 100
	}
	var result RetentionCleanup
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(txCtx, `SELECT id::text, COALESCE(result_artifact_digest, ''), artifact_retention_until FROM screenshot_jobs WHERE retention_until <= $1 ORDER BY retention_until, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now.UTC(), limit)
		if err != nil {
			return wrap("select screenshot retention", err)
		}
		type candidate struct {
			id, digest        string
			artifactRetention time.Time
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.id, &value.digest, &value.artifactRetention); err != nil {
				rows.Close()
				return wrap("scan screenshot retention", err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return wrap("close screenshot retention", err)
		}
		if err := rows.Err(); err != nil {
			return wrap("iterate screenshot retention", err)
		}
		for _, value := range candidates {
			if _, err := tx.ExecContext(txCtx, `DELETE FROM screenshot_idempotency_records WHERE job_id = $1::uuid`, value.id); err != nil {
				return wrap("delete screenshot idempotency retention", err)
			}
			if _, err := tx.ExecContext(txCtx, `DELETE FROM screenshot_jobs WHERE id = $1::uuid`, value.id); err != nil {
				return wrap("delete screenshot job retention", err)
			}
			result.JobIDs = append(result.JobIDs, value.id)
		}
		seen := make(map[string]struct{})
		for _, value := range candidates {
			if value.digest == "" || !value.artifactRetention.After(now) {
				continue
			}
			// An artifact whose own retention has not elapsed remains available
			// to a CAS janitor even though this job row is gone.
			seen[value.digest] = struct{}{}
		}
		for _, value := range candidates {
			if value.digest == "" || value.artifactRetention.After(now) {
				continue
			}
			if _, ok := seen[value.digest]; ok {
				continue
			}
			var referenced bool
			if err := tx.QueryRowContext(txCtx, `SELECT EXISTS (SELECT 1 FROM screenshot_jobs WHERE result_artifact_digest = $1) OR EXISTS (SELECT 1 FROM web_items WHERE thumbnail_digest = $1)`, value.digest).Scan(&referenced); err != nil {
				return wrap("check screenshot artifact references", err)
			}
			if !referenced {
				result.ArtifactDigests = append(result.ArtifactDigests, value.digest)
				seen[value.digest] = struct{}{}
			}
		}
		return nil
	})
	return result, err
}

func ptrTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func normalizeUUID(value string) string {
	if len(value) == 36 && strings.Count(value, "-") == 4 {
		compact := strings.ReplaceAll(value, "-", "")
		if len(compact) == 32 {
			value = compact
		}
	}
	compact := strings.ReplaceAll(value, "-", "")
	validHex := len(compact) >= 32
	if validHex {
		for _, c := range compact[:32] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				validHex = false
				break
			}
		}
	}
	if !validHex {
		sum := sha256.Sum256([]byte(value))
		compact = hex.EncodeToString(sum[:])
	}
	compact = strings.ToLower(compact[:32])
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}
