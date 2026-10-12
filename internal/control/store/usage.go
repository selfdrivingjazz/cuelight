package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/control/page"
	"github.com/selfdrivingjazz/cuelight/internal/control/usage"
)

var _ usage.Repository = (*Store)(nil)

type usageRelation struct {
	sourceTable string
	query       string
}

// The relation SQL is deliberately static. Resource kind is selected from this
// table rather than interpolated from a request, while each relation is fetched
// in one bounded query (there is no per-reference lookup).
var usageRelations = map[usage.Kind]usageRelation{
	usage.KindMedia: {
		sourceTable: "media",
		query: `SELECT kind, id, name, href FROM (
			SELECT DISTINCT 'playlist'::text AS kind, p.id::text AS id, p.name,
				'/library/playlists/' || p.id::text AS href
			FROM media m
			JOIN playlist_items i ON i.asset_id = m.asset_id OR i.media_id = m.id
			JOIN playlists p ON p.id = i.playlist_id
			WHERE m.id = $1
		) refs
		WHERE ($2 = '' OR (kind, id) > ($2, $3))
		ORDER BY kind, id
		LIMIT $4`,
	},
	usage.KindPlaylist: {
		sourceTable: "playlists",
		query: `SELECT kind, id, name, href FROM (
			SELECT DISTINCT 'assignment'::text AS kind, a.id::text AS id, n.name,
				'/screens/' || a.node_id::text || '/assignment' AS href
			FROM assignments a
			JOIN nodes n ON n.id = a.node_id
			WHERE a.playlist_id = $1
		) refs
		WHERE ($2 = '' OR (kind, id) > ($2, $3))
		ORDER BY kind, id
		LIMIT $4`,
	},
	usage.KindRelease: {
		sourceTable: "releases",
		query: `SELECT kind, id, name, href FROM (
			SELECT DISTINCT 'assignment'::text AS kind, a.id::text AS id, n.name,
				'/screens/' || a.node_id::text || '/assignment' AS href
			FROM assignments a
			JOIN nodes n ON n.id = a.node_id
			WHERE a.release_id = $1
		) refs
		WHERE ($2 = '' OR (kind, id) > ($2, $3))
		ORDER BY kind, id
		LIMIT $4`,
	},
	usage.KindAssignment: {
		sourceTable: "assignments",
		query: `SELECT kind, id, name, href FROM (
			SELECT DISTINCT 'release'::text AS kind, r.id::text AS id, r.digest AS name,
				'/releases/' || r.id::text AS href
			FROM assignments a
			JOIN releases r ON r.id = a.release_id
			WHERE a.id = $1
		) refs
		WHERE ($2 = '' OR (kind, id) > ($2, $3))
		ORDER BY kind, id
		LIMIT $4`,
	},
}

func (s *Store) Usage(ctx context.Context, query usage.Query) (usage.Page, error) {
	query, err := query.Normalize()
	if err != nil {
		return usage.Page{}, err
	}
	relation, ok := usageRelations[query.Kind]
	if !ok {
		return usage.Page{}, fmt.Errorf("%w: %s", usage.ErrUnsupportedKind, query.Kind)
	}
	cursorKind, cursorID, err := usageCursor(query.Cursor, query)
	if err != nil {
		return usage.Page{}, err
	}

	// Check the source independently so an existing resource with no edges is
	// distinguishable from a missing resource, without loading any relations.
	var exists int
	if err := s.q(ctx).QueryRowContext(ctx,
		"SELECT 1 FROM "+relation.sourceTable+" WHERE id=$1", query.ResourceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return usage.Page{}, usage.ErrNotFound
		}
		return usage.Page{}, wrap("read usage resource", err)
	}

	rows, err := s.q(ctx).QueryContext(ctx, relation.query, query.ResourceID, cursorKind, cursorID, query.Limit+1)
	if err != nil {
		return usage.Page{}, wrap("read resource usage", err)
	}
	defer rows.Close()

	refs := make([]usage.Reference, 0, query.Limit)
	for rows.Next() {
		var kind, id, name, href string
		if err := rows.Scan(&kind, &id, &name, &href); err != nil {
			return usage.Page{}, wrap("scan resource usage", err)
		}
		refs = append(refs, usage.Reference{Kind: usage.Kind(kind), ID: id, Name: name, Href: href})
	}
	if err := rows.Err(); err != nil {
		return usage.Page{}, wrap("iterate resource usage", err)
	}

	result := usage.Page{ResourceID: query.ResourceID, References: refs}
	if len(refs) > query.Limit {
		result.HasMore = true
		last := refs[query.Limit-1]
		result.References = refs[:query.Limit]
		cursor, err := page.DefaultCodec().Encode(page.Position{
			Resource: usageCursorResource(query), Sort: "kind,id", Order: "asc",
			Value: string(last.Kind), ID: last.ID,
		})
		if err != nil {
			return usage.Page{}, wrap("encode usage cursor", err)
		}
		result.NextCursor = cursor
	}
	return result, nil
}

func usageCursorResource(query usage.Query) string {
	return "usage:" + string(query.Kind) + ":" + query.ResourceID
}

func usageCursor(raw string, query usage.Query) (kind, id string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", nil
	}
	position, err := page.DefaultCodec().Decode(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", page.ErrCursorInvalid, err)
	}
	if position.Resource != usageCursorResource(query) || position.Sort != "kind,id" || position.Order != "asc" || position.Value == "" || position.ID == "" {
		return "", "", page.ErrCursorInvalid
	}
	return position.Value, position.ID, nil
}

// ListUsage is an explicit alias useful to repository composition code that
// names relationship reads as list operations.
func (s *Store) ListUsage(ctx context.Context, query usage.Query) (usage.Page, error) {
	return s.Usage(ctx, query)
}
