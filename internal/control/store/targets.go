package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	controlrelease "github.com/jmilldotdev/cuelight/internal/control/release"
)

var _ controlrelease.TargetCatalog = (*Store)(nil)

// TargetMedia reads the assignable view of one media item.
func (s *Store) TargetMedia(ctx context.Context, id string) (controlrelease.TargetMedia, error) {
	if !webItemIDPattern.MatchString(id) {
		return controlrelease.TargetMedia{}, assignment.ErrTargetNotFound
	}
	var target controlrelease.TargetMedia
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id::text, kind, COALESCE(asset_id::text, ''), COALESCE(url, ''),
		app_access, fit, loop, muted, volume, orientation FROM media WHERE id = $1`, id).Scan(
		&target.ID, &target.Kind, &target.AssetID, &target.URL, &target.AppAccess,
		&target.Playback.Fit, &target.Playback.Loop, &target.Playback.Muted, &target.Playback.Volume, &target.Playback.Orientation)
	if errors.Is(err, sql.ErrNoRows) {
		return controlrelease.TargetMedia{}, assignment.ErrTargetNotFound
	}
	return target, wrap("read assignment media target", err)
}

// TargetPlaylist reads a playlist's items in order with each file item's
// original artifact.
func (s *Store) TargetPlaylist(ctx context.Context, id string) ([]controlrelease.PlaylistEntry, error) {
	if !webItemIDPattern.MatchString(id) {
		return nil, assignment.ErrTargetNotFound
	}
	var found bool
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM playlists WHERE id = $1)`, id).Scan(&found); err != nil {
		return nil, wrap("read assignment playlist target", err)
	}
	if !found {
		return nil, assignment.ErrTargetNotFound
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT i.id::text, i.duration_seconds, COALESCE(i.asset_id::text, ''),
		COALESCE(r.digest, ''), COALESCE(r.size_bytes, 0), COALESCE(r.media_type, ''), COALESCE(r.state, ''),
		COALESCE(m.id::text, ''), COALESCE(m.url, '')
		FROM playlist_items i
		LEFT JOIN asset_artifacts aa ON aa.asset_id = i.asset_id AND aa.role = 'original'
		LEFT JOIN artifacts r ON r.digest = aa.artifact_digest
		LEFT JOIN media m ON m.id = i.media_id
		WHERE i.playlist_id = $1 ORDER BY i.position, i.id`, id)
	if err != nil {
		return nil, wrap("list assignment playlist items", err)
	}
	defer rows.Close()
	var entries []controlrelease.PlaylistEntry
	for rows.Next() {
		var entry controlrelease.PlaylistEntry
		var web controlrelease.TargetMedia
		if err := rows.Scan(&entry.ID, &entry.DurationSeconds, &entry.AssetID,
			&entry.Artifact.Digest, &entry.Artifact.Size, &entry.Artifact.MediaType, &entry.Artifact.State,
			&web.ID, &web.URL); err != nil {
			return nil, wrap("scan assignment playlist item", err)
		}
		if web.ID != "" {
			web.Kind = "web"
			entry.Web = &web
		}
		entries = append(entries, entry)
	}
	return entries, wrap("iterate assignment playlist items", rows.Err())
}
