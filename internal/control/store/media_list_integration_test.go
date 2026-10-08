package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/page"
)

func seedListMedia(t *testing.T, repository *Store, rows []struct {
	title, mediaType string
	size             int64
}) {
	t.Helper()
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-0000000000aa"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, row := range rows {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		digest := strings.Repeat(fmt.Sprintf("%x", i+1), 64)[:64]
		created := base.Add(time.Duration(i) * time.Hour)
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO assets (id, name, created_at) VALUES ($1, $2, $3)`, []any{id, row.title, created}},
			{`INSERT INTO artifacts (digest, size_bytes, media_type, state) VALUES ($1, $2, $3, 'ready')`, []any{digest, row.size, row.mediaType}},
			{`INSERT INTO asset_artifacts (asset_id, artifact_digest, role) VALUES ($1, $2, 'original')`, []any{id, digest}},
			{`INSERT INTO media (id, asset_id, title, fit, loop, muted, volume, orientation, created_by, updated_by, created_at, updated_at)
				VALUES ($1, $1, $2, 'fit', true, true, 0, 0, $3, $3, $4, $4)`, []any{id, row.title, actorID, created}},
		} {
			if _, err := repository.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Every sort key must page through all rows exactly once in key order, with
// ties broken deterministically by ID across cursor boundaries.
func TestPostgresListMediaSortKeysPaginateInOrder(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	seedListMedia(t, repository, []struct {
		title, mediaType string
		size             int64
	}{
		{"delta.mp4", "video/mp4", 300},
		{"Alpha.png", "image/png", 100},
		{"charlie.mp3", "audio/mpeg", 300},
		{"bravo.png", "image/png", 200},
		{"echo.mp4", "video/mp4", 50},
	})
	cases := []struct {
		sort, order string
		want        []string
	}{
		{"", "asc", []string{"delta.mp4", "Alpha.png", "charlie.mp3", "bravo.png", "echo.mp4"}},
		{"createdAt", "desc", []string{"echo.mp4", "bravo.png", "charlie.mp3", "Alpha.png", "delta.mp4"}},
		{"title", "asc", []string{"Alpha.png", "bravo.png", "charlie.mp3", "delta.mp4", "echo.mp4"}},
		{"title", "desc", []string{"echo.mp4", "delta.mp4", "charlie.mp3", "bravo.png", "Alpha.png"}},
		{"size", "asc", []string{"echo.mp4", "Alpha.png", "bravo.png", "delta.mp4", "charlie.mp3"}},
		{"size", "desc", []string{"charlie.mp3", "delta.mp4", "bravo.png", "Alpha.png", "echo.mp4"}},
		{"type", "asc", []string{"charlie.mp3", "Alpha.png", "bravo.png", "delta.mp4", "echo.mp4"}},
	}
	for _, tc := range cases {
		t.Run(tc.sort+"_"+tc.order, func(t *testing.T) {
			var got []string
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > 5 {
					t.Fatal("pagination did not terminate")
				}
				result, err := repository.ListMedia(context.Background(), page.PageRequest{Limit: 2, Sort: tc.sort, Order: tc.order, Cursor: cursor}, media.MediaListFilter{})
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range result.Items {
					got = append(got, item.Title)
				}
				if result.Page.NextCursor == nil {
					break
				}
				cursor = *result.Page.NextCursor
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
	if _, err := repository.ListMedia(context.Background(), page.PageRequest{Limit: 2, Sort: "bogus"}, media.MediaListFilter{}); err != page.ErrSortInvalid {
		t.Fatalf("unknown sort error = %v", err)
	}
}

// Thumbnails are keyed by source digest: linking one surfaces it on list and
// get, drops the source from the backfill queue, and replacement overwrites.
func TestPostgresMediaThumbnailLinking(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	seedListMedia(t, repository, []struct {
		title, mediaType string
		size             int64
	}{{"poster.png", "image/png", 10}, {"clip.mp4", "video/mp4", 20}})
	ctx := context.Background()
	sources, err := repository.ListThumbnailSources(ctx, 10)
	if err != nil || len(sources) != 1 || sources[0].MediaType != "image/png" {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	thumb := media.Thumbnail{Digest: strings.Repeat("e", 64), MediaType: media.ThumbnailMediaType, Width: 64, Height: 48}
	if err := repository.PutMediaThumbnail(ctx, sources[0].Digest, thumb, 99); err != nil {
		t.Fatal(err)
	}
	replacement := media.Thumbnail{Digest: strings.Repeat("f", 64), MediaType: media.ThumbnailMediaType, Width: 32, Height: 24}
	if err := repository.PutMediaThumbnail(ctx, sources[0].Digest, replacement, 50); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetMedia(ctx, "00000000-0000-4000-8000-000000000001")
	if err != nil || got.Thumbnail == nil || *got.Thumbnail != replacement {
		t.Fatalf("get thumbnail = %#v, %v", got.Thumbnail, err)
	}
	list, err := repository.ListMedia(ctx, page.PageRequest{Limit: 10}, media.MediaListFilter{})
	if err != nil || list.Items[0].Thumbnail == nil || list.Items[1].Thumbnail != nil {
		t.Fatalf("list thumbnails = %#v, %v", list.Items, err)
	}
	if sources, err := repository.ListThumbnailSources(ctx, 10); err != nil || len(sources) != 0 {
		t.Fatalf("sources after link = %#v, %v", sources, err)
	}
}
