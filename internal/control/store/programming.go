package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/jmill/cable/internal/control/cable"
)

var _ cable.ProgrammingRepository = (*Store)(nil)
var _ cable.ProgrammingDetailRepository = (*Store)(nil)
var _ cable.ProgrammingMutationRepository = (*Store)(nil)

func (s *Store) CreatePlaylist(ctx context.Context, mutation cable.Mutation, input cable.PlaylistInput) (cable.Playlist, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if mutation.ActorID == "" || input.Name == "" || len(input.Items) == 0 {
		return cable.Playlist{}, cable.ErrInvalidArgument
	}
	positions := make(map[int]bool, len(input.Items))
	for _, item := range input.Items {
		if (item.AssetID == "") == (item.MediaID == "") || item.Position <= 0 || item.DurationSeconds <= 0 || positions[item.Position] {
			return cable.Playlist{}, cable.ErrInvalidArgument
		}
		positions[item.Position] = true
	}
	playlistID, err := s.newUUID()
	if err != nil {
		return cable.Playlist{}, err
	}
	err = s.inTransaction(ctx, func(txCtx context.Context, _ *sql.Tx) error {
		if _, execErr := s.q(txCtx).ExecContext(txCtx, `INSERT INTO cable_playlists(id,name,description,version,created_by,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$5)`, playlistID, input.Name, input.Description, mutation.ActorID, ct(mutation)); execErr != nil {
			if pgConflict(execErr) {
				return cable.ErrConflict
			}
			return ce("create Cable playlist", execErr)
		}
		for _, item := range input.Items {
			itemID, idErr := s.newUUID()
			if idErr != nil {
				return idErr
			}
			if _, execErr := s.q(txCtx).ExecContext(txCtx, playlistItemInsert, itemID, playlistID, item.AssetID, item.MediaID, item.Position, item.DurationSeconds, strings.TrimSpace(item.Title), strings.TrimSpace(item.Subtitle)); execErr != nil {
				if pgConflict(execErr) {
					return cable.ErrConflict
				}
				if invalidPlaylistSource(execErr) {
					return cable.ErrInvalidArgument
				}
				return ce("create Cable playlist item", execErr)
			}
		}
		return nil
	})
	if err != nil {
		return cable.Playlist{}, err
	}
	return s.Playlist(ctx, playlistID)
}

func (s *Store) Playlist(ctx context.Context, id string) (cable.Playlist, error) {
	var value cable.Playlist
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id,name,description,version,created_by,created_at,updated_at FROM cable_playlists WHERE id=$1`, id).Scan(&value.ID, &value.Name, &value.Description, &value.Version, &value.CreatedBy, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return value, ce("get Cable playlist", err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id,playlist_id,COALESCE(asset_id::text,''),COALESCE(media_id::text,''),position,duration_seconds,title,subtitle FROM cable_playlist_items WHERE playlist_id=$1 ORDER BY position,id`, id)
	if err != nil {
		return value, ce("list Cable playlist items", err)
	}
	defer rows.Close()
	value.Items = []cable.PlaylistItem{}
	for rows.Next() {
		var item cable.PlaylistItem
		if err := rows.Scan(&item.ID, &item.PlaylistID, &item.AssetID, &item.MediaID, &item.Position, &item.DurationSeconds, &item.Title, &item.Subtitle); err != nil {
			return value, ce("scan Cable playlist item", err)
		}
		value.Items = append(value.Items, item)
	}
	return value, rows.Err()
}

func (s *Store) ListPlaylists(ctx context.Context, cursor string, limit int) (cable.PlaylistPage, error) {
	limit = cl(limit)
	query := `SELECT id FROM cable_playlists`
	args := []any{}
	if cursor != "" {
		query += ` WHERE id>$1`
		args = append(args, cursor)
	}
	query += ` ORDER BY id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return cable.PlaylistPage{}, ce("list Cable playlists", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return cable.PlaylistPage{}, err
		}
		ids = append(ids, id)
	}
	page := cable.PlaylistPage{Items: []cable.Playlist{}}
	if len(ids) > limit {
		page.Page.HasMore = true
		ids = ids[:limit]
		page.Page.NextCursor = ids[len(ids)-1]
	}
	for _, id := range ids {
		item, err := s.Playlist(ctx, id)
		if err != nil {
			return cable.PlaylistPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

func (s *Store) CreateBlock(ctx context.Context, mutation cable.Mutation, input cable.BlockInput) (cable.Block, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.PlaylistID = strings.TrimSpace(input.PlaylistID)
	input.AssetID = strings.TrimSpace(input.AssetID)
	if mutation.ActorID == "" {
		return cable.Block{}, cable.ErrInvalidArgument
	}
	if err := cable.ValidateBlock(input); err != nil {
		return cable.Block{}, err
	}
	var playlist cable.Playlist
	var err error
	if input.PlaylistID != "" {
		playlist, err = s.Playlist(ctx, input.PlaylistID)
		if err != nil {
			return cable.Block{}, err
		}
		if len(playlist.Items) == 0 {
			return cable.Block{}, &cable.ValidationError{Field: "playlistId", Reason: "playlist must contain at least one item"}
		}
		if input.Title == "" {
			input.Title = playlist.Name
		}
	}
	if input.Title == "" {
		input.Title = "Cable media"
	}
	blockID, err := s.newUUID()
	if err != nil {
		return cable.Block{}, err
	}
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var overlap bool
		if queryErr := tx.QueryRowContext(txCtx, `SELECT EXISTS(SELECT 1 FROM cable_blocks WHERE channel_id=$1 AND starts_at<$3 AND ends_at>$2)`, input.ChannelID, input.StartsAt.UTC(), input.EndsAt.UTC()).Scan(&overlap); queryErr != nil {
			return ce("check Cable block overlap", queryErr)
		}
		if overlap {
			return cable.ErrOverlap
		}
		if _, execErr := tx.ExecContext(txCtx, `INSERT INTO cable_blocks(id,channel_id,playlist_id,asset_id,title,starts_at,ends_at,version,created_by,created_at) VALUES($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,$6,$7,1,$8,$9)`, blockID, input.ChannelID, input.PlaylistID, input.AssetID, input.Title, input.StartsAt.UTC(), input.EndsAt.UTC(), mutation.ActorID, ct(mutation)); execErr != nil {
			if pgConflict(execErr) || postgresCode(execErr) == "23P01" {
				return cable.ErrOverlap
			}
			return ce("create Cable block", execErr)
		}
		return nil
	})
	if err != nil {
		return cable.Block{}, err
	}
	return s.Block(ctx, blockID)
}

func (s *Store) Block(ctx context.Context, id string) (cable.Block, error) {
	var value cable.Block
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id,channel_id,COALESCE(playlist_id::text,''),COALESCE(asset_id::text,''),title,starts_at,ends_at,version,created_by,created_at FROM cable_blocks WHERE id=$1`, id).Scan(&value.ID, &value.ChannelID, &value.PlaylistID, &value.AssetID, &value.Title, &value.StartsAt, &value.EndsAt, &value.Version, &value.CreatedBy, &value.CreatedAt)
	return value, ce("get Cable block", err)
}

func (s *Store) ListBlocks(ctx context.Context, channelID, cursor string, limit int) (cable.BlockPage, error) {
	limit = cl(limit)
	query := `SELECT id,channel_id,COALESCE(playlist_id::text,''),COALESCE(asset_id::text,''),title,starts_at,ends_at,version,created_by,created_at FROM cable_blocks WHERE channel_id=$1`
	args := []any{channelID}
	if cursor != "" {
		query += ` AND (starts_at,id) > (SELECT starts_at,id FROM cable_blocks WHERE id=$2)`
		args = append(args, cursor)
	}
	query += ` ORDER BY starts_at,id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return cable.BlockPage{}, ce("list Cable blocks", err)
	}
	defer rows.Close()
	result := cable.BlockPage{Items: []cable.Block{}}
	for rows.Next() {
		var value cable.Block
		if err := rows.Scan(&value.ID, &value.ChannelID, &value.PlaylistID, &value.AssetID, &value.Title, &value.StartsAt, &value.EndsAt, &value.Version, &value.CreatedBy, &value.CreatedAt); err != nil {
			return cable.BlockPage{}, err
		}
		result.Items = append(result.Items, value)
	}
	if len(result.Items) > limit {
		result.Page.HasMore = true
		result.Items = result.Items[:limit]
		result.Page.NextCursor = result.Items[len(result.Items)-1].ID
	}
	return result, rows.Err()
}
// playlistItemInsert stores a file asset or a web media item. A media ID that
// is not web media resolves to NULL and trips the exactly-one-source check.
const playlistItemInsert = `INSERT INTO cable_playlist_items(id,playlist_id,asset_id,media_id,position,duration_seconds,title,subtitle)
	VALUES($1,$2,NULLIF($3,'')::uuid,(SELECT m.id FROM media m WHERE m.id = NULLIF($4,'')::uuid AND m.kind = 'web'),$5,$6,$7,$8)`

func invalidPlaylistSource(err error) bool {
	code := postgresCode(err)
	return code == "23514" || code == "23503" || code == "22P02"
}

func validatePlaylistInput(input cable.PlaylistInput) error {
	if strings.TrimSpace(input.Name) == "" || len(input.Items) == 0 {
		return cable.ErrInvalidArgument
	}
	positions := make(map[int]bool, len(input.Items))
	for _, item := range input.Items {
		if (item.AssetID == "") == (item.MediaID == "") || item.Position <= 0 || item.DurationSeconds <= 0 || positions[item.Position] {
			return cable.ErrInvalidArgument
		}
		positions[item.Position] = true
	}
	return nil
}

func (s *Store) UpdatePlaylist(ctx context.Context, mutation cable.Mutation, id string, input cable.PlaylistInput) (cable.Playlist, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if mutation.ActorID == "" || mutation.ExpectedVersion <= 0 {
		return cable.Playlist{}, cable.ErrInvalidArgument
	}
	if err := validatePlaylistInput(input); err != nil {
		return cable.Playlist{}, err
	}
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(txCtx, `UPDATE cable_playlists SET name=$2,description=$3,version=version+1,updated_at=$4 WHERE id=$1 AND version=$5`, id, input.Name, input.Description, ct(mutation), mutation.ExpectedVersion)
		if err != nil {
			if pgConflict(err) {
				return cable.ErrConflict
			}
			return ce("update Cable playlist", err)
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return cable.ErrConflict
		}
		if _, err := tx.ExecContext(txCtx, `DELETE FROM cable_playlist_items WHERE playlist_id=$1`, id); err != nil {
			return ce("replace Cable playlist items", err)
		}
		for _, item := range input.Items {
			itemID, err := s.newUUID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(txCtx, playlistItemInsert, itemID, id, item.AssetID, item.MediaID, item.Position, item.DurationSeconds, strings.TrimSpace(item.Title), strings.TrimSpace(item.Subtitle)); err != nil {
				if pgConflict(err) {
					return cable.ErrConflict
				}
				if invalidPlaylistSource(err) {
					return cable.ErrInvalidArgument
				}
				return ce("replace Cable playlist item", err)
			}
		}
		return nil
	})
	if err != nil {
		return cable.Playlist{}, err
	}
	return s.Playlist(ctx, id)
}

func (s *Store) UpdateBlock(ctx context.Context, mutation cable.Mutation, id string, input cable.BlockInput) (cable.Block, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.PlaylistID = strings.TrimSpace(input.PlaylistID)
	input.AssetID = strings.TrimSpace(input.AssetID)
	if mutation.ActorID == "" || mutation.ExpectedVersion <= 0 {
		return cable.Block{}, cable.ErrInvalidArgument
	}
	if input.ChannelID == "" {
		current, err := s.Block(ctx, id)
		if err != nil {
			return cable.Block{}, err
		}
		input.ChannelID = current.ChannelID
	}
	if err := cable.ValidateBlock(input); err != nil {
		return cable.Block{}, err
	}
	if input.PlaylistID != "" {
		playlist, err := s.Playlist(ctx, input.PlaylistID)
		if err != nil {
			return cable.Block{}, err
		}
		if len(playlist.Items) == 0 {
			return cable.Block{}, &cable.ValidationError{Field: "playlistId", Reason: "playlist must contain at least one item"}
		}
		if input.Title == "" {
			input.Title = playlist.Name
		}
	}
	if input.Title == "" {
		input.Title = "Cable media"
	}
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var overlap bool
		if err := tx.QueryRowContext(txCtx, `SELECT EXISTS(SELECT 1 FROM cable_blocks WHERE channel_id=$1 AND id<>$2 AND starts_at<$4 AND ends_at>$3)`, input.ChannelID, id, input.StartsAt.UTC(), input.EndsAt.UTC()).Scan(&overlap); err != nil {
			return ce("check Cable block overlap", err)
		}
		if overlap {
			return cable.ErrOverlap
		}
		result, err := tx.ExecContext(txCtx, `UPDATE cable_blocks SET channel_id=$2,playlist_id=NULLIF($3,'')::uuid,asset_id=NULLIF($4,'')::uuid,title=$5,starts_at=$6,ends_at=$7,version=version+1 WHERE id=$1 AND version=$8`, id, input.ChannelID, input.PlaylistID, input.AssetID, input.Title, input.StartsAt.UTC(), input.EndsAt.UTC(), mutation.ExpectedVersion)
		if err != nil {
			if pgConflict(err) || postgresCode(err) == "23P01" {
				return cable.ErrOverlap
			}
			return ce("update Cable block", err)
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return cable.ErrConflict
		}
		return nil
	})
	if err != nil {
		return cable.Block{}, err
	}
	return s.Block(ctx, id)
}
