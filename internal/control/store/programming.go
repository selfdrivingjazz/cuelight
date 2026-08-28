package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/cable"
)

var _ cable.ProgrammingRepository = (*Store)(nil)

func (s *Store) CreatePlaylist(ctx context.Context, mutation cable.Mutation, input cable.PlaylistInput) (cable.Playlist, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if mutation.ActorID == "" || input.Name == "" || len(input.Items) == 0 {
		return cable.Playlist{}, cable.ErrInvalidArgument
	}
	positions := make(map[int]bool, len(input.Items))
	for _, item := range input.Items {
		if item.AssetID == "" || item.Position <= 0 || item.DurationSeconds <= 0 || positions[item.Position] {
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
			if _, execErr := s.q(txCtx).ExecContext(txCtx, `INSERT INTO cable_playlist_items(id,playlist_id,asset_id,position,duration_seconds,title,subtitle) VALUES($1,$2,$3,$4,$5,$6,$7)`, itemID, playlistID, item.AssetID, item.Position, item.DurationSeconds, strings.TrimSpace(item.Title), strings.TrimSpace(item.Subtitle)); execErr != nil {
				if pgConflict(execErr) {
					return cable.ErrConflict
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
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id,playlist_id,asset_id,position,duration_seconds,title,subtitle FROM cable_playlist_items WHERE playlist_id=$1 ORDER BY position,id`, id)
	if err != nil {
		return value, ce("list Cable playlist items", err)
	}
	defer rows.Close()
	value.Items = []cable.PlaylistItem{}
	for rows.Next() {
		var item cable.PlaylistItem
		if err := rows.Scan(&item.ID, &item.PlaylistID, &item.AssetID, &item.Position, &item.DurationSeconds, &item.Title, &item.Subtitle); err != nil {
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
	if mutation.ActorID == "" || input.ChannelID == "" || input.PlaylistID == "" || input.StartsAt.IsZero() {
		return cable.Block{}, cable.ErrInvalidArgument
	}
	playlist, err := s.Playlist(ctx, input.PlaylistID)
	if err != nil {
		return cable.Block{}, err
	}
	if input.Title == "" {
		input.Title = playlist.Name
	}
	endsAt := input.StartsAt.UTC()
	for _, item := range playlist.Items {
		endsAt = endsAt.Add(time.Duration(item.DurationSeconds) * time.Second)
	}
	blockID, err := s.newUUID()
	if err != nil {
		return cable.Block{}, err
	}
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var overlap bool
		if queryErr := tx.QueryRowContext(txCtx, `SELECT EXISTS(SELECT 1 FROM cable_blocks WHERE channel_id=$1 AND starts_at<$3 AND ends_at>$2)`, input.ChannelID, input.StartsAt.UTC(), endsAt).Scan(&overlap); queryErr != nil {
			return ce("check Cable block overlap", queryErr)
		}
		if overlap {
			return cable.ErrOverlap
		}
		if _, execErr := tx.ExecContext(txCtx, `INSERT INTO cable_blocks(id,channel_id,playlist_id,title,starts_at,ends_at,version,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8)`, blockID, input.ChannelID, input.PlaylistID, input.Title, input.StartsAt.UTC(), endsAt, mutation.ActorID, ct(mutation)); execErr != nil {
			return ce("create Cable block", execErr)
		}
		cursor := input.StartsAt.UTC()
		bound := s.transactionStore(tx)
		for _, item := range playlist.Items {
			itemEnd := cursor.Add(time.Duration(item.DurationSeconds) * time.Second)
			if _, createErr := bound.CreateProgramEntry(txCtx, mutation, cable.ProgramInput{ChannelID: input.ChannelID, AssetID: item.AssetID, StartsAt: cursor, EndsAt: itemEnd, Title: item.Title, Subtitle: item.Subtitle, Fit: "fill", PlacardPolicy: "start", AvailabilityPolicy: "fail", Volume: 1}); createErr != nil {
				return createErr
			}
			cursor = itemEnd
		}
		return nil
	})
	if err != nil {
		return cable.Block{}, err
	}
	return s.block(ctx, blockID)
}

func (s *Store) block(ctx context.Context, id string) (cable.Block, error) {
	var value cable.Block
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id,channel_id,playlist_id,title,starts_at,ends_at,version,created_by,created_at FROM cable_blocks WHERE id=$1`, id).Scan(&value.ID, &value.ChannelID, &value.PlaylistID, &value.Title, &value.StartsAt, &value.EndsAt, &value.Version, &value.CreatedBy, &value.CreatedAt)
	return value, ce("get Cable block", err)
}

func (s *Store) ListBlocks(ctx context.Context, channelID, cursor string, limit int) (cable.BlockPage, error) {
	limit = cl(limit)
	query := `SELECT id,channel_id,playlist_id,title,starts_at,ends_at,version,created_by,created_at FROM cable_blocks WHERE channel_id=$1`
	args := []any{channelID}
	if cursor != "" {
		query += ` AND id>$2`
		args = append(args, cursor)
	}
	query += ` ORDER BY id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return cable.BlockPage{}, ce("list Cable blocks", err)
	}
	defer rows.Close()
	result := cable.BlockPage{Items: []cable.Block{}}
	for rows.Next() {
		var value cable.Block
		if err := rows.Scan(&value.ID, &value.ChannelID, &value.PlaylistID, &value.Title, &value.StartsAt, &value.EndsAt, &value.Version, &value.CreatedBy, &value.CreatedAt); err != nil {
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

func (s *Store) CreateProfile(ctx context.Context, mutation cable.Mutation, input cable.ProfileInput) (cable.Profile, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if mutation.ActorID == "" || input.Name == "" || input.DeploymentID == "" || input.ShellBuildID == "" {
		return cable.Profile{}, cable.ErrInvalidArgument
	}
	id, err := s.newUUID()
	if err != nil {
		return cable.Profile{}, err
	}
	var releaseID any
	if input.ReleaseID != "" {
		releaseID = input.ReleaseID
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_profiles(id,name,description,deployment_id,shell_build_id,release_id,version,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$8)`, id, input.Name, input.Description, input.DeploymentID, input.ShellBuildID, releaseID, mutation.ActorID, ct(mutation))
	if err != nil {
		if pgConflict(err) {
			return cable.Profile{}, cable.ErrConflict
		}
		return cable.Profile{}, ce("create Cable profile", err)
	}
	return s.Profile(ctx, id)
}

func (s *Store) Profile(ctx context.Context, id string) (cable.Profile, error) {
	var value cable.Profile
	var releaseID sql.NullString
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id,name,description,deployment_id,shell_build_id,release_id,version,created_by,created_at,updated_at FROM cable_profiles WHERE id=$1`, id).Scan(&value.ID, &value.Name, &value.Description, &value.DeploymentID, &value.ShellBuildID, &releaseID, &value.Version, &value.CreatedBy, &value.CreatedAt, &value.UpdatedAt)
	if releaseID.Valid {
		value.ReleaseID = releaseID.String
	}
	return value, ce("get Cable profile", err)
}

func (s *Store) ListProfiles(ctx context.Context, cursor string, limit int) (cable.ProfilePage, error) {
	limit = cl(limit)
	query := `SELECT id,name,description,deployment_id,shell_build_id,release_id,version,created_by,created_at,updated_at FROM cable_profiles`
	args := []any{}
	if cursor != "" {
		query += ` WHERE id>$1`
		args = append(args, cursor)
	}
	query += ` ORDER BY id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return cable.ProfilePage{}, ce("list Cable profiles", err)
	}
	defer rows.Close()
	result := cable.ProfilePage{Items: []cable.Profile{}}
	for rows.Next() {
		var value cable.Profile
		var releaseID sql.NullString
		if err := rows.Scan(&value.ID, &value.Name, &value.Description, &value.DeploymentID, &value.ShellBuildID, &releaseID, &value.Version, &value.CreatedBy, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return cable.ProfilePage{}, err
		}
		if releaseID.Valid {
			value.ReleaseID = releaseID.String
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
