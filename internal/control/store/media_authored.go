package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/page"
)

var _ media.MediaRepository = (*Store)(nil)

const (
	authoredMediaCreate = "media.create"
	authoredMediaUpdate = "media.update"
)

func (s *Store) CreateMedia(ctx context.Context, mutation media.Mutation, value media.Media) (media.Media, error) {
	hash, err := jsonDigest(struct {
		AssetID, Title, Description, Artist, ArtworkAssetID string
		Tags                                                []string
		Playback                                            media.Playback
	}{value.AssetID, value.Title, value.Description, value.Artist, value.ArtworkAssetID, value.Tags, value.Playback})
	if err != nil {
		return media.Media{}, err
	}
	var result media.Media
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		responseID, replayed, err := s.mediaIdempotency(txCtx, tx, authoredMediaCreate, mutation, hash, func() (string, error) {
			if err := s.verifyMediaAssets(txCtx, tx, value.AssetID, value.ArtworkAssetID); err != nil {
				return "", err
			}
			_, err := tx.ExecContext(txCtx, `INSERT INTO media
				(id, asset_id, title, description, artist, tags, artwork_asset_id, fit, loop, muted, volume, orientation, version, created_by, updated_by)
				VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, NULLIF($7, '') , $8, $9, $10, $11, $12, 1, $13, $13)`,
				value.ID, value.AssetID, value.Title, value.Description, value.Artist, pgtype.FlatArray[string](value.Tags), value.ArtworkAssetID,
				value.Playback.Fit, value.Playback.Loop, value.Playback.Muted, value.Playback.Volume, value.Playback.Orientation, mutation.ActorID)
			if err != nil {
				return "", wrap("create authored media", err)
			}
			return value.ID, nil
		})
		if err != nil {
			return err
		}
		result, err = s.authoredMedia(txCtx, tx, responseID)
		if err != nil {
			return err
		}
		if !replayed {
			result.Version = 1
		}
		return nil
	})
	return result, err
}

func (s *Store) GetMedia(ctx context.Context, id string) (media.Media, error) {
	return s.authoredMedia(ctx, s.q(ctx), id)
}

func (s *Store) authoredMedia(ctx context.Context, q queryer, id string) (media.Media, error) {
	var value media.Media
	var description, artist, artwork sql.NullString
	var tags pgtype.FlatArray[string]
	err := q.QueryRowContext(ctx, `SELECT m.id::text, m.asset_id::text, m.title, m.description, m.artist,
		m.tags, m.artwork_asset_id::text, m.fit, m.loop, m.muted, m.volume, m.orientation, m.version,
		m.created_by::text, m.updated_by::text, m.created_at, m.updated_at,
		r.digest, r.size_bytes, r.media_type, r.state
		FROM media m JOIN asset_artifacts aa ON aa.asset_id = m.asset_id AND aa.role = 'original'
		JOIN artifacts r ON r.digest = aa.artifact_digest WHERE m.id = $1`, id).Scan(
		&value.ID, &value.AssetID, &value.Title, &description, &artist, &tags, &artwork, &value.Playback.Fit,
		&value.Playback.Loop, &value.Playback.Muted, &value.Playback.Volume, &value.Playback.Orientation,
		&value.Version, &value.CreatedBy, &value.UpdatedBy, &value.CreatedAt, &value.UpdatedAt,
		&value.Artifact.Digest, &value.Artifact.Size, &value.Artifact.MediaType, &value.Artifact.State)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, media.ErrNotFound
	}
	if err != nil {
		return media.Media{}, wrap("read authored media", err)
	}
	value.Description, value.Artist = description.String, artist.String
	value.ArtworkAssetID = artwork.String
	value.Tags = append([]string(nil), tags...)
	if value.Tags == nil {
		value.Tags = []string{}
	}
	return value, nil
}

func (s *Store) UpdateMedia(ctx context.Context, mutation media.Mutation, id string, expectedVersion int64, patch media.MediaPatch) (media.Media, error) {
	hash, err := jsonDigest(struct {
		ID      string
		Version int64
		Patch   media.MediaPatch
	}{id, expectedVersion, patch})
	if err != nil {
		return media.Media{}, err
	}
	var result media.Media
	err = s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		current, err := s.authoredMedia(txCtx, tx, id)
		if err != nil {
			return err
		}
		responseID, replayed, err := s.mediaIdempotency(txCtx, tx, authoredMediaUpdate, mutation, hash, func() (string, error) {
			if patch.ArtworkAssetID != nil {
				if err := s.verifyMediaAssets(txCtx, tx, current.AssetID, strings.TrimSpace(*patch.ArtworkAssetID)); err != nil {
					return "", err
				}
			}
			sets := make([]string, 0, 6)
			args := []any{id, expectedVersion}
			n := 3
			if patch.Title != nil {
				sets = append(sets, "title = $"+strconv.Itoa(n))
				args = append(args, *patch.Title)
				n++
			}
			if patch.Description != nil {
				sets = append(sets, "description = NULLIF($"+strconv.Itoa(n)+", '')")
				args = append(args, *patch.Description)
				n++
			}
			if patch.Artist != nil {
				sets = append(sets, "artist = NULLIF($"+strconv.Itoa(n)+", '')")
				args = append(args, *patch.Artist)
				n++
			}
			if patch.Tags != nil {
				sets = append(sets, "tags = $"+strconv.Itoa(n))
				args = append(args, pgtype.FlatArray[string](*patch.Tags))
				n++
			}
			if patch.ArtworkAssetID != nil {
				sets = append(sets, "artwork_asset_id = NULLIF($"+strconv.Itoa(n)+", '')")
				args = append(args, *patch.ArtworkAssetID)
				n++
			}
			if patch.Playback != nil {
				p := patch.Playback
				sets = append(sets, "fit = $"+strconv.Itoa(n), "loop = $"+strconv.Itoa(n+1), "muted = $"+strconv.Itoa(n+2), "volume = $"+strconv.Itoa(n+3), "orientation = $"+strconv.Itoa(n+4))
				args = append(args, p.Fit, p.Loop, p.Muted, p.Volume, p.Orientation)
				n += 5
			}
			if len(sets) == 0 {
				return current.ID, nil
			}
			sets = append(sets, "version = version + 1", "updated_by = $"+strconv.Itoa(n), "updated_at = now()")
			args = append(args, mutation.ActorID)
			query := "UPDATE media SET " + strings.Join(sets, ", ") + " WHERE id = $1 AND version = $2"
			changed, err := tx.ExecContext(txCtx, query, args...)
			if err != nil {
				return "", wrap("update authored media", err)
			}
			count, err := changed.RowsAffected()
			if err != nil {
				return "", wrap("inspect authored media update", err)
			}
			if count != 1 {
				return "", media.ErrConflict
			}
			return current.ID, nil
		})
		if err != nil {
			return err
		}
		result, err = s.authoredMedia(txCtx, tx, responseID)
		if replayed {
			return err
		}
		return err
	})
	return result, err
}

func (s *Store) ListMedia(ctx context.Context, request page.PageRequest, filter media.MediaListFilter) (page.Response[media.Media], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[media.Media]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" && sortKey != "title" {
		return page.Response[media.Media]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var pos page.Position
	var previous *string
	if request.Cursor != "" {
		pos, err = codec.Decode(request.Cursor)
		if err != nil || pos.Resource != "media" || pos.Sort != sortKey || pos.Order != request.Order {
			return page.Response[media.Media]{}, page.ErrCursorInvalid
		}
		previousValue := pos.Previous
		previous = &previousValue
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 5)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		clauses = append(clauses, "(LOWER(m.title) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(COALESCE(m.artist,'')) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(m.id::text) LIKE $"+strconv.Itoa(len(args))+")")
	}
	if filter.Playable != nil && *filter.Playable {
		clauses = append(clauses, "r.state = 'ready' AND (r.media_type LIKE 'audio/%' OR r.media_type LIKE 'video/%' OR r.media_type LIKE 'image/%')")
	}
	if filter.Kind != "" {
		args = append(args, filter.Kind+"/%")
		clauses = append(clauses, "r.media_type LIKE $"+strconv.Itoa(len(args)))
	}
	if len(filter.AssetIDs) > 0 {
		args = append(args, pgtype.FlatArray[string](filter.AssetIDs))
		clauses = append(clauses, "m.asset_id = ANY($"+strconv.Itoa(len(args))+"::uuid[])")
	}
	if request.Cursor != "" {
		op := ">"
		if request.Order == "desc" {
			op = "<"
		}
		args = append(args, pos.Value, pos.ID)
		if sortKey == "title" {
			clauses = append(clauses, "(m.title, m.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		} else {
			parsed, e := time.Parse(time.RFC3339Nano, pos.Value)
			if e != nil {
				return page.Response[media.Media]{}, page.ErrCursorInvalid
			}
			args[len(args)-2] = parsed
			clauses = append(clauses, "(m.created_at, m.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		}
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := "m.created_at " + order + ", m.id " + order
	if sortKey == "title" {
		orderBy = "m.title " + order + ", m.id " + order
	}
	args = append(args, request.Limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, "SELECT m.id::text, m.asset_id::text, m.title, m.description, m.artist, m.tags, m.artwork_asset_id::text, m.fit, m.loop, m.muted, m.volume, m.orientation, m.version, m.created_by::text, m.updated_by::text, m.created_at, m.updated_at, r.digest, r.size_bytes, r.media_type, r.state FROM media m JOIN asset_artifacts aa ON aa.asset_id=m.asset_id AND aa.role='original' JOIN artifacts r ON r.digest=aa.artifact_digest WHERE "+strings.Join(clauses, " AND ")+" ORDER BY "+orderBy+" LIMIT $"+strconv.Itoa(len(args)), args...)
	items := make([]media.Media, 0, request.Limit)
	var lastValue, lastID string
	hasMore := false
	for rows.Next() {
		var item media.Media
		var desc, artist, artwork sql.NullString
		var tags pgtype.FlatArray[string]
		var created time.Time
		err := rows.Scan(&item.ID, &item.AssetID, &item.Title, &desc, &artist, &tags, &artwork, &item.Playback.Fit, &item.Playback.Loop, &item.Playback.Muted, &item.Playback.Volume, &item.Playback.Orientation, &item.Version, &item.CreatedBy, &item.UpdatedBy, &created, &item.UpdatedAt, &item.Artifact.Digest, &item.Artifact.Size, &item.Artifact.MediaType, &item.Artifact.State)
		if err != nil {
			return page.Response[media.Media]{}, wrap("scan authored media", err)
		}
		item.Description, item.Artist, item.ArtworkAssetID = desc.String, artist.String, artwork.String
		item.Tags = append([]string(nil), tags...)
		if len(items) < request.Limit {
			item.CreatedAt = created
			items = append(items, item)
			lastID = item.ID
			if sortKey == "title" {
				lastValue = item.Title
			} else {
				lastValue = created.UTC().Format(time.RFC3339Nano)
			}
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[media.Media]{}, wrap("iterate authored media", err)
	}
	var next *string
	if hasMore {
		v, e := codec.Encode(page.Position{Resource: "media", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if e != nil {
			return page.Response[media.Media]{}, e
		}
		next = &v
	}
	return page.NewResponse(items, next, previous, hasMore), nil
}

func (s *Store) GetMediaUsage(ctx context.Context, id string) (media.MediaUsage, error) {
	var result media.MediaUsage
	err := s.q(ctx).QueryRowContext(ctx, `SELECT m.id::text, 0::bigint, 0::bigint FROM media m WHERE m.id=$1`, id).Scan(&result.MediaID, &result.ReleaseCount, &result.ScheduleCount)
	if errors.Is(err, sql.ErrNoRows) {
		return media.MediaUsage{}, media.ErrNotFound
	}
	if err != nil {
		return media.MediaUsage{}, wrap("read authored media usage", err)
	}
	return result, nil
}

func (s *Store) verifyMediaAssets(ctx context.Context, tx *sql.Tx, assetID, artworkID string) error {
	for field, id := range map[string]string{"assetId": assetID, "artworkAssetId": artworkID} {
		if id == "" {
			continue
		}
		var digest string
		var state media.ArtifactState
		err := tx.QueryRowContext(ctx, `SELECT r.digest,r.state FROM assets a JOIN asset_artifacts aa ON aa.asset_id=a.id AND aa.role='original' JOIN artifacts r ON r.digest=aa.artifact_digest WHERE a.id=$1`, id).Scan(&digest, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", media.ErrNotFound, field)
		}
		if err != nil {
			return wrap("verify media asset", err)
		}
		if digest == "" {
			return fmt.Errorf("%w: empty artifact", media.ErrInvalidArgument)
		}
	}
	return nil
}
