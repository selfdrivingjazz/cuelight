package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jmill/cable/internal/control/playlist"
)

var _ playlist.Repository = (*Store)(nil)

// playlistItemInsert stores a file asset, or resolves a media ID to the file
// media's asset or the web media item itself. An unknown media ID inserts no
// row.
const playlistItemInsert = `INSERT INTO playlist_items(id,playlist_id,asset_id,media_id,position,duration_seconds,title,subtitle)
	SELECT $1, $2, COALESCE(NULLIF($3,'')::uuid, m.asset_id), CASE WHEN m.kind = 'web' THEN m.id END, $5, $6, $7, $8
	FROM (SELECT 1) one LEFT JOIN media m ON m.id = NULLIF($4,'')::uuid
	WHERE NULLIF($4,'') IS NULL OR m.id IS NOT NULL`

// playlistItemSelect reports every item's media ID, resolving file items to
// their file media row.
const playlistItemSelect = `SELECT i.id::text, i.playlist_id::text, COALESCE(i.asset_id::text,''), COALESCE(i.media_id::text, fm.id::text, ''),
	i.position, i.duration_seconds, i.title, i.subtitle
	FROM playlist_items i LEFT JOIN media fm ON fm.asset_id = i.asset_id
	WHERE i.playlist_id = $1 ORDER BY i.position, i.id`

func playlistTime(m playlist.Mutation) time.Time {
	if m.Now.IsZero() {
		return time.Now().UTC()
	}
	return m.Now.UTC()
}

func playlistError(operation string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows), postgresCode(err) == invalidTextRepresentation:
		return playlist.ErrNotFound
	default:
		return fmt.Errorf("PostgreSQL %s: %w", operation, err)
	}
}

func (s *Store) CreatePlaylist(ctx context.Context, mutation playlist.Mutation, input playlist.Input) (playlist.Playlist, error) {
	input, err := playlist.Normalize(input)
	if err != nil {
		return playlist.Playlist{}, err
	}
	if mutation.ActorID == "" {
		return playlist.Playlist{}, fmt.Errorf("%w: actor is required", playlist.ErrInvalidArgument)
	}
	playlistID, err := s.newUUID()
	if err != nil {
		return playlist.Playlist{}, err
	}
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `INSERT INTO playlists(id,name,description,version,created_by,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$5)`, playlistID, input.Name, input.Description, mutation.ActorID, playlistTime(mutation)); err != nil {
			if postgresCode(err) == "23505" {
				return playlist.ErrNameConflict
			}
			return playlistError("create playlist", err)
		}
		return s.insertPlaylistItems(txCtx, tx, playlistID, input.Items)
	})
	if err != nil {
		return playlist.Playlist{}, err
	}
	return s.Playlist(ctx, playlistID)
}

func (s *Store) insertPlaylistItems(ctx context.Context, tx *sql.Tx, playlistID string, items []playlist.ItemInput) error {
	for _, item := range items {
		itemID, err := s.newUUID()
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, playlistItemInsert, itemID, playlistID, item.AssetID, item.MediaID, item.Position, item.DurationSeconds, item.Title, item.Subtitle)
		if err != nil {
			switch postgresCode(err) {
			case "23514", "23503", invalidTextRepresentation:
				return fmt.Errorf("%w: item %d references unknown media", playlist.ErrInvalidArgument, item.Position)
			}
			return playlistError("insert playlist item", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("%w: item %d references unknown media", playlist.ErrInvalidArgument, item.Position)
		}
	}
	return nil
}

func (s *Store) Playlist(ctx context.Context, id string) (playlist.Playlist, error) {
	var value playlist.Playlist
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id::text,name,description,version,created_by::text,created_at,updated_at FROM playlists WHERE id=$1`, id).Scan(&value.ID, &value.Name, &value.Description, &value.Version, &value.CreatedBy, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return playlist.Playlist{}, playlistError("get playlist", err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, playlistItemSelect, id)
	if err != nil {
		return playlist.Playlist{}, playlistError("list playlist items", err)
	}
	defer rows.Close()
	value.Items = []playlist.Item{}
	for rows.Next() {
		var item playlist.Item
		if err := rows.Scan(&item.ID, &item.PlaylistID, &item.AssetID, &item.MediaID, &item.Position, &item.DurationSeconds, &item.Title, &item.Subtitle); err != nil {
			return playlist.Playlist{}, playlistError("scan playlist item", err)
		}
		value.Items = append(value.Items, item)
	}
	return value, playlistError("iterate playlist items", rows.Err())
}

func (s *Store) ListPlaylists(ctx context.Context, cursor string, limit int) (playlist.Page, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT id::text FROM playlists`
	args := []any{}
	if cursor != "" {
		query += ` WHERE id > $1`
		args = append(args, cursor)
	}
	query += ` ORDER BY id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		if postgresCode(err) == invalidTextRepresentation {
			return playlist.Page{}, fmt.Errorf("%w: invalid cursor", playlist.ErrInvalidArgument)
		}
		return playlist.Page{}, playlistError("list playlists", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return playlist.Page{}, playlistError("scan playlist", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return playlist.Page{}, playlistError("iterate playlists", err)
	}
	rows.Close()
	result := playlist.Page{Items: []playlist.Playlist{}}
	if len(ids) > limit {
		ids = ids[:limit]
		result.HasMore, result.NextCursor = true, ids[len(ids)-1]
	}
	for _, id := range ids {
		value, err := s.Playlist(ctx, id)
		if err != nil {
			return playlist.Page{}, err
		}
		result.Items = append(result.Items, value)
	}
	return result, nil
}

func (s *Store) UpdatePlaylist(ctx context.Context, mutation playlist.Mutation, id string, input playlist.Input) (playlist.Playlist, error) {
	input, err := playlist.Normalize(input)
	if err != nil {
		return playlist.Playlist{}, err
	}
	if mutation.ActorID == "" || mutation.ExpectedVersion <= 0 {
		return playlist.Playlist{}, fmt.Errorf("%w: actor and expected version are required", playlist.ErrInvalidArgument)
	}
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var version int64
		if err := tx.QueryRowContext(txCtx, `SELECT version FROM playlists WHERE id=$1 FOR UPDATE`, id).Scan(&version); err != nil {
			return playlistError("lock playlist", err)
		}
		if version != mutation.ExpectedVersion {
			return playlist.ErrConflict
		}
		if _, err := tx.ExecContext(txCtx, `UPDATE playlists SET name=$2,description=$3,version=version+1,updated_at=$4 WHERE id=$1`, id, input.Name, input.Description, playlistTime(mutation)); err != nil {
			if postgresCode(err) == "23505" {
				return playlist.ErrNameConflict
			}
			return playlistError("update playlist", err)
		}
		if _, err := tx.ExecContext(txCtx, `DELETE FROM playlist_items WHERE playlist_id=$1`, id); err != nil {
			return playlistError("replace playlist items", err)
		}
		return s.insertPlaylistItems(txCtx, tx, id, input.Items)
	})
	if err != nil {
		return playlist.Playlist{}, err
	}
	return s.Playlist(ctx, id)
}
