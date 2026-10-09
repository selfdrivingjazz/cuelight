package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/media"
	"github.com/jmilldotdev/cuelight/internal/control/page"
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
				VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), ARRAY(SELECT jsonb_array_elements_text($6::jsonb)), NULLIF($7, '') , $8, $9, $10, $11, $12, 1, $13, $13)`,
				value.ID, value.AssetID, value.Title, value.Description, value.Artist, jsonStrings(value.Tags), value.ArtworkAssetID,
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

// authoredMediaSelect reads every media kind: file media join their asset's
// original artifact and poster; web media carry their own capture columns.
const authoredMediaSelect = `SELECT m.id::text, m.kind, COALESCE(m.asset_id::text, ''), COALESCE(m.url, ''),
	m.app_access, m.favicon_url, m.title, m.description, m.artist,
	array_to_json(m.tags), m.artwork_asset_id::text, m.fit, m.loop, m.muted, m.volume, m.orientation, m.version,
	m.created_by::text, m.updated_by::text, m.created_at, m.updated_at,
	COALESCE(r.digest, ''), COALESCE(r.size_bytes, 0), COALESCE(r.media_type, ''), COALESCE(r.state, ''),
	t.thumbnail_digest, t.width, t.height,
	m.thumbnail_digest, m.thumbnail_media_type, m.thumbnail_source, m.thumbnail_captured_at, m.thumbnail_error, m.thumbnail_attempted_at
	FROM media m
	LEFT JOIN asset_artifacts aa ON aa.asset_id = m.asset_id AND aa.role = 'original'
	LEFT JOIN artifacts r ON r.digest = aa.artifact_digest
	LEFT JOIN media_thumbnails t ON t.source_digest = r.digest`

// Web captures are rendered at the node viewport size.
const webThumbnailWidth, webThumbnailHeight = 1920, 1080

func scanAuthoredMedia(row rowScanner) (media.Media, error) {
	var value media.Media
	var description, artist, artwork sql.NullString
	var tags []byte
	var thumbDigest, webThumbDigest, webThumbType, webThumbSource sql.NullString
	var thumbWidth, thumbHeight sql.NullInt64
	var attempted, captured sql.NullTime
	var state string
	if err := row.Scan(&value.ID, &value.Kind, &value.AssetID, &value.URL, &value.AppAccess, &value.FaviconURL,
		&value.Title, &description, &artist, &tags, &artwork, &value.Playback.Fit,
		&value.Playback.Loop, &value.Playback.Muted, &value.Playback.Volume, &value.Playback.Orientation,
		&value.Version, &value.CreatedBy, &value.UpdatedBy, &value.CreatedAt, &value.UpdatedAt,
		&value.Artifact.Digest, &value.Artifact.Size, &value.Artifact.MediaType, &state,
		&thumbDigest, &thumbWidth, &thumbHeight, &webThumbDigest, &webThumbType, &webThumbSource, &captured, &value.ThumbnailError, &attempted); err != nil {
		return media.Media{}, err
	}
	value.Artifact.State = media.ArtifactState(state)
	value.Description, value.Artist = description.String, artist.String
	value.ArtworkAssetID = artwork.String
	if err := json.Unmarshal(tags, &value.Tags); err != nil {
		return media.Media{}, wrap("decode authored media tags", err)
	}
	if value.Tags == nil {
		value.Tags = []string{}
	}
	if value.Kind == media.KindWeb {
		value.Playback = media.Playback{}
		if webThumbDigest.Valid {
			at := captured.Time.UTC()
			value.Thumbnail = &media.Thumbnail{Digest: webThumbDigest.String, MediaType: webThumbType.String, Width: webThumbnailWidth, Height: webThumbnailHeight, Source: webThumbSource.String, CapturedAt: &at}
		}
		if attempted.Valid {
			at := attempted.Time.UTC()
			value.ThumbnailAttemptedAt = &at
		}
		return value, nil
	}
	value.Thumbnail = scannedThumbnail(thumbDigest, thumbWidth, thumbHeight)
	return value, nil
}

func (s *Store) authoredMedia(ctx context.Context, q queryer, id string) (media.Media, error) {
	if !webItemIDPattern.MatchString(id) {
		return media.Media{}, media.ErrNotFound
	}
	value, err := scanAuthoredMedia(q.QueryRowContext(ctx, authoredMediaSelect+` WHERE m.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, media.ErrNotFound
	}
	if err != nil {
		return media.Media{}, wrap("read authored media", err)
	}
	return value, nil
}

// CreateWebMedia inserts a web page or returns the library item that already
// shows the URL.
func (s *Store) CreateWebMedia(ctx context.Context, mutation media.Mutation, value media.Media) (media.Media, bool, error) {
	var result media.Media
	var created bool
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(txCtx, `INSERT INTO media (id, kind, url, app_access, title, description, tags, version, created_by, updated_by)
			VALUES ($1, 'web', $2, $3, $4, NULLIF($5, ''), ARRAY(SELECT jsonb_array_elements_text($6::jsonb)), 1, $7, $7)
			ON CONFLICT (url) WHERE url IS NOT NULL DO NOTHING RETURNING id::text`,
			value.ID, value.URL, value.AppAccess, value.Title, value.Description, jsonStrings(value.Tags), mutation.ActorID).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRowContext(txCtx, `SELECT id::text FROM media WHERE url = $1`, value.URL).Scan(&id); err != nil {
				return wrap("read existing web media", err)
			}
		case err != nil:
			return wrap("create web media", err)
		default:
			created = true
		}
		result, err = s.authoredMedia(txCtx, tx, id)
		return err
	})
	return result, created, err
}

// DeleteWebMedia removes an operator-added web page. Playlist items that
// still reference it block deletion.
func (s *Store) DeleteWebMedia(ctx context.Context, id string) error {
	if !webItemIDPattern.MatchString(id) {
		return media.ErrNotFound
	}
	result, err := s.q(ctx).ExecContext(ctx, `DELETE FROM media WHERE id = $1 AND kind = 'web'`, id)
	if postgresCode(err) == "23503" {
		return media.ErrInUse
	}
	if err != nil {
		return wrap("delete web media", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return media.ErrNotFound
	}
	return nil
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
		if current.Kind == media.KindWeb && (patch.Playback != nil || patch.ArtworkAssetID != nil || patch.Artist != nil) {
			return fmt.Errorf("%w: web media support title, description, tags, and appAccess only", media.ErrWrongKind)
		}
		if current.Kind != media.KindWeb && patch.AppAccess != nil {
			return &media.ValidationError{Field: "appAccess", Reason: "applies to web media only"}
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
				sets = append(sets, "tags = ARRAY(SELECT jsonb_array_elements_text($"+strconv.Itoa(n)+"::jsonb))")
				args = append(args, jsonStrings(*patch.Tags))
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
			if patch.AppAccess != nil {
				sets = append(sets, "app_access = $"+strconv.Itoa(n))
				args = append(args, *patch.AppAccess)
				n++
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
	sortColumn, ok := mediaSortColumns[sortKey]
	if !ok {
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
		clauses = append(clauses, "(LOWER(m.title) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(COALESCE(m.artist,'')) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(COALESCE(m.url,'')) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(m.id::text) LIKE $"+strconv.Itoa(len(args))+")")
	}
	if filter.Playable != nil && *filter.Playable {
		clauses = append(clauses, "(m.kind = 'web' OR (r.state = 'ready' AND (r.media_type LIKE 'audio/%' OR r.media_type LIKE 'video/%' OR r.media_type LIKE 'image/%')))")
	}
	if filter.Kind != "" {
		args = append(args, filter.Kind)
		clauses = append(clauses, "m.kind = $"+strconv.Itoa(len(args)))
	}
	if filter.MediaType != "" {
		args = append(args, filter.MediaType+"/%")
		clauses = append(clauses, "r.media_type LIKE $"+strconv.Itoa(len(args)))
	}
	if len(filter.AssetIDs) > 0 {
		args = append(args, jsonStrings(filter.AssetIDs))
		clauses = append(clauses, "m.asset_id = ANY(ARRAY(SELECT jsonb_array_elements_text($"+strconv.Itoa(len(args))+"::jsonb)::uuid))")
	}
	if request.Cursor != "" {
		op := ">"
		if request.Order == "desc" {
			op = "<"
		}
		var cursorValue any = pos.Value
		switch sortKey {
		case "createdAt":
			parsed, e := time.Parse(time.RFC3339Nano, pos.Value)
			if e != nil {
				return page.Response[media.Media]{}, page.ErrCursorInvalid
			}
			cursorValue = parsed
		case "size":
			parsed, e := strconv.ParseInt(pos.Value, 10, 64)
			if e != nil {
				return page.Response[media.Media]{}, page.ErrCursorInvalid
			}
			cursorValue = parsed
		}
		args = append(args, cursorValue, pos.ID)
		clauses = append(clauses, "("+sortColumn+", m.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := sortColumn + " " + order + ", m.id::text " + order
	args = append(args, request.Limit+1)
	rows, err := s.q(ctx).QueryContext(ctx, authoredMediaSelect+" WHERE "+strings.Join(clauses, " AND ")+" ORDER BY "+orderBy+" LIMIT $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return page.Response[media.Media]{}, wrap("list authored media", err)
	}
	defer rows.Close()
	items := make([]media.Media, 0, request.Limit)
	var lastValue, lastID string
	hasMore := false
	for rows.Next() {
		item, err := scanAuthoredMedia(rows)
		if err != nil {
			return page.Response[media.Media]{}, wrap("scan authored media", err)
		}
		created := item.CreatedAt
		if len(items) < request.Limit {
			item.CreatedAt = created
			items = append(items, item)
			lastID = item.ID
			switch sortKey {
			case "title":
				lastValue = strings.ToLower(item.Title)
			case "size":
				lastValue = strconv.FormatInt(item.Artifact.Size, 10)
			case "type":
				lastValue = item.Artifact.MediaType
			default:
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
func jsonStrings(values []string) string {
	if values == nil {
		return "[]"
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

// mediaSortColumns maps public sort keys to keyset columns. Every key is
// paired with m.id as a tiebreaker so cursors stay stable.
var mediaSortColumns = map[string]string{
	"createdAt": "m.created_at",
	"title":     "LOWER(m.title)",
	"size":      "COALESCE(r.size_bytes, 0)",
	"type":      "COALESCE(r.media_type, '')",
}

func scannedThumbnail(digest sql.NullString, width, height sql.NullInt64) *media.Thumbnail {
	if !digest.Valid {
		return nil
	}
	return &media.Thumbnail{Digest: digest.String, MediaType: media.ThumbnailMediaType, Width: int(width.Int64), Height: int(height.Int64)}
}

func (s *Store) PutMediaThumbnail(ctx context.Context, sourceDigest string, thumbnail media.Thumbnail, size int64) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, $2, $3, 'ready') ON CONFLICT (digest) DO NOTHING`, thumbnail.Digest, size, thumbnail.MediaType); err != nil {
			return wrap("register thumbnail artifact", err)
		}
		_, err := tx.ExecContext(txCtx, `INSERT INTO media_thumbnails (source_digest, thumbnail_digest, width, height) VALUES ($1, $2, $3, $4)
			ON CONFLICT (source_digest) DO UPDATE SET thumbnail_digest = EXCLUDED.thumbnail_digest, width = EXCLUDED.width, height = EXCLUDED.height, created_at = now()`,
			sourceDigest, thumbnail.Digest, thumbnail.Width, thumbnail.Height)
		return wrap("link media thumbnail", err)
	})
}

func (s *Store) ListThumbnailSources(ctx context.Context, limit int) ([]media.Artifact, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT DISTINCT r.digest, r.size_bytes, r.media_type, r.state FROM media m
		JOIN asset_artifacts aa ON aa.asset_id = m.asset_id AND aa.role = 'original'
		JOIN artifacts r ON r.digest = aa.artifact_digest
		LEFT JOIN media_thumbnails t ON t.source_digest = r.digest
		WHERE t.source_digest IS NULL AND r.state = 'ready' AND r.media_type LIKE 'image/%' LIMIT $1`, limit)
	if err != nil {
		return nil, wrap("list thumbnail sources", err)
	}
	defer rows.Close()
	var out []media.Artifact
	for rows.Next() {
		var artifact media.Artifact
		if err := rows.Scan(&artifact.Digest, &artifact.Size, &artifact.MediaType, &artifact.State); err != nil {
			return nil, wrap("scan thumbnail source", err)
		}
		out = append(out, artifact)
	}
	return out, wrap("iterate thumbnail sources", rows.Err())
}

var _ media.ThumbnailRepository = (*Store)(nil)

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
