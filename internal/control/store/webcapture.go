package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/control/media"
	"github.com/selfdrivingjazz/cuelight/internal/control/webitem"
	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

// WebCaptureRepository persists web media thumbnails in the shared artifact CAS.
type WebCaptureRepository struct {
	*Store
	CAS *localcas.Store
}

var _ webitem.Repository = (*WebCaptureRepository)(nil)

var webItemIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *WebCaptureRepository) WebCaptureTarget(ctx context.Context, id string) (webitem.Item, error) {
	if !webItemIDPattern.MatchString(id) {
		return webitem.Item{}, media.ErrNotFound
	}
	var item webitem.Item
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id::text, url FROM media WHERE id = $1 AND kind = 'web' AND url IS NOT NULL`, id).Scan(&item.ID, &item.URL)
	if errors.Is(err, sql.ErrNoRows) {
		return webitem.Item{}, media.ErrNotFound
	}
	return item, wrap("read web capture target", err)
}

func (s *WebCaptureRepository) SetWebMetadata(ctx context.Context, id, title, faviconURL string) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE media SET favicon_url = $3,
		title = CASE WHEN $2 <> '' AND title = url THEN $2 ELSE title END,
		version = CASE WHEN $2 <> '' AND title = url AND $2 <> title THEN version + 1 ELSE version END,
		updated_at = now()
		WHERE id = $1 AND kind = 'web'`, id, title, faviconURL)
	return wrap("update web media metadata", err)
}

func (s *WebCaptureRepository) StoreWebThumbnail(ctx context.Context, id string, png []byte, source string, capturedAt time.Time) error {
	if s.CAS == nil || len(png) == 0 {
		return errors.New("web thumbnail store is not configured")
	}
	sum := sha256.Sum256(png)
	digest := hex.EncodeToString(sum[:])
	if _, err := s.CAS.Put(bytes.NewReader(png), digest); err != nil {
		return wrap("store web thumbnail bytes", err)
	}
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `
			INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, $2, 'image/png', 'ready')
			ON CONFLICT (digest) DO UPDATE SET state = 'ready'`, digest, len(png)); err != nil {
			return wrap("register web thumbnail artifact", err)
		}
		_, err := tx.ExecContext(txCtx, `
			UPDATE media SET thumbnail_digest = $2, thumbnail_media_type = 'image/png', thumbnail_source = $3,
			  thumbnail_captured_at = $4, thumbnail_attempted_at = $4, thumbnail_error = '', updated_at = now()
			WHERE id = $1 AND kind = 'web' AND (thumbnail_captured_at IS NULL OR thumbnail_captured_at <= $4)`,
			id, digest, source, capturedAt.UTC())
		return wrap("set web media thumbnail", err)
	})
}

func (s *WebCaptureRepository) RecordWebCaptureFailure(ctx context.Context, id, reason string, at time.Time) error {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE media SET thumbnail_error = $2, thumbnail_attempted_at = $3 WHERE id = $1 AND kind = 'web'`, id, reason, at.UTC())
	return wrap("record web capture failure", err)
}

// AdoptNodeScreenshots takes, per web media item, the newest completed
// screenshot from a node whose current assignment (made before the
// screenshot) targets that item, when it is newer than the item's thumbnail.
// Hosted items have no server capture, so node screenshots are their only
// thumbnails.
func (s *WebCaptureRepository) AdoptNodeScreenshots(ctx context.Context) (int, error) {
	result, err := s.q(ctx).ExecContext(ctx, `
		WITH current_assignment AS (
			SELECT DISTINCT ON (node_id) node_id, media_id, created_at
			FROM assignments ORDER BY node_id, desired_generation DESC
		), candidate AS (
			SELECT DISTINCT ON (m.id) m.id AS item_id, j.result_artifact_digest AS digest,
			       j.result_media_type AS media_type, j.completion_at
			FROM media m
			JOIN current_assignment a ON a.media_id = m.id
			JOIN screenshot_jobs j ON j.node_id = a.node_id AND j.status = 'completed'
			  AND j.completion_at > a.created_at + interval '5 seconds'
			WHERE m.kind = 'web' AND (m.thumbnail_captured_at IS NULL OR j.completion_at > m.thumbnail_captured_at)
			ORDER BY m.id, j.completion_at DESC
		)
		UPDATE media m SET thumbnail_digest = c.digest, thumbnail_media_type = c.media_type,
		  thumbnail_source = 'node', thumbnail_captured_at = c.completion_at, thumbnail_error = '', updated_at = now()
		FROM candidate c WHERE m.id = c.item_id`)
	if err != nil {
		return 0, wrap("adopt node screenshots", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *WebCaptureRepository) WebDueForCapture(ctx context.Context, before time.Time, limit int) ([]webitem.Item, error) {
	// Failed attempts back off for the same interval as successful ones,
	// except a never-captured item retries after 10 minutes.
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id::text, url FROM media
		WHERE kind = 'web' AND url IS NOT NULL
		  AND (thumbnail_captured_at IS NULL OR thumbnail_captured_at < $1 OR thumbnail_attempted_at IS NULL)
		  AND (thumbnail_attempted_at IS NULL
		       OR (thumbnail_captured_at IS NULL AND thumbnail_attempted_at < now() - interval '10 minutes')
		       OR thumbnail_attempted_at < $1)
		ORDER BY thumbnail_captured_at NULLS FIRST, created_at LIMIT $2`, before.UTC(), limit)
	if err != nil {
		return nil, wrap("list web media due for capture", err)
	}
	defer rows.Close()
	var items []webitem.Item
	for rows.Next() {
		var item webitem.Item
		if err := rows.Scan(&item.ID, &item.URL); err != nil {
			return nil, wrap("scan web capture target", err)
		}
		items = append(items, item)
	}
	return items, wrap("iterate web media due for capture", rows.Err())
}
