package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/page"
)

type authoredRepo struct {
	value       Media
	createCalls int
	updateCalls int
}

func (r *authoredRepo) CreateMedia(_ context.Context, _ Mutation, v Media) (Media, error) {
	r.createCalls++
	v.Artifact = Artifact{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 11, MediaType: "video/mp4", State: ArtifactReady}
	r.value = v
	return v, nil
}
func (r *authoredRepo) ListMedia(context.Context, page.PageRequest, MediaListFilter) (page.Response[Media], error) {
	return page.NewResponse([]Media{r.value}, nil, nil, false), nil
}
func (r *authoredRepo) GetMedia(context.Context, string) (Media, error) {
	if r.value.ID == "" {
		return Media{}, ErrNotFound
	}
	return r.value, nil
}
func (r *authoredRepo) UpdateMedia(_ context.Context, _ Mutation, _ string, version int64, _ MediaPatch) (Media, error) {
	if version != r.value.Version {
		return Media{}, ErrConflict
	}
	r.updateCalls++
	r.value.Version++
	return r.value, nil
}
func (r *authoredRepo) GetMediaUsage(context.Context, string) (MediaUsage, error) {
	return MediaUsage{}, nil
}
func (r *authoredRepo) CreateWebMedia(_ context.Context, _ Mutation, v Media) (Media, bool, error) {
	r.createCalls++
	r.value = v
	return v, true, nil
}
func (r *authoredRepo) DeleteWebMedia(context.Context, string) error {
	r.value = Media{}
	return nil
}

type testContent struct{ data []byte }
type testHandle struct{ *bytes.Reader }

func (h testHandle) Close() error { return nil }
func (c testContent) Stat(context.Context, string) (ContentObject, error) {
	return ContentObject{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: int64(len(c.data))}, nil
}
func (c testContent) Open(context.Context, string) (ContentHandle, error) {
	return testHandle{bytes.NewReader(c.data)}, nil
}

func testAuthRequest(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	return r.WithContext(auth.WithActor(r.Context(), auth.Actor{ID: "actor-1"}))
}
func TestAuthoredServiceRejectsInvalidPlaybackAndPlayableRejectsUnsupportedType(t *testing.T) {
	repo := &authoredRepo{}
	service, _ := NewAuthoredService(repo, func() string { return "media-1" })
	_, err := service.Create(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "0123456789abcdef"}, MediaInput{AssetID: "11111111-1111-1111-1111-111111111111", Title: "bad", Playback: Playback{Fit: "stretch", Volume: 1}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid fit error=%v", err)
	}
	value := Media{Artifact: Artifact{State: ArtifactReady, Size: 1, MediaType: "application/pdf"}}
	if value.Playable() {
		t.Fatal("application/pdf must not be playable")
	}
}
func TestOperatorMediaRejectsCrossScope(t *testing.T) {
	repo := &authoredRepo{value: Media{ID: "media-1", Version: 1, Artifact: Artifact{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 3, MediaType: "video/mp4", State: ArtifactReady}}}
	service, _ := NewAuthoredService(repo, func() string { return "unused" })
	h := NewOperatorHandler(OperatorDependencies{Service: service, Authorize: func(_ auth.Actor, action, resource, id string) error { return ErrForbidden }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, testAuthRequest(http.MethodGet, "/media/media-1", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-scope status=%d", w.Code)
	}
}
func TestOperatorMediaRangeAndIfRange(t *testing.T) {
	repo := &authoredRepo{value: Media{ID: "media-1", Title: "clip", Version: 2, UpdatedAt: time.Now().UTC(), Artifact: Artifact{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 11, MediaType: "video/mp4", State: ArtifactReady}}}
	service, _ := NewAuthoredService(repo, func() string { return "unused" })
	h := NewOperatorHandler(OperatorDependencies{Service: service, Content: testContent{data: []byte("hello world")}})
	r := testAuthRequest(http.MethodGet, "/media/media-1/content", nil)
	r.Header.Set("Range", "bytes=0-4")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "hello" {
		t.Fatalf("range status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Header().Get("ETag") != `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"` {
		t.Fatalf("etag=%q", w.Header().Get("ETag"))
	}
	r = testAuthRequest(http.MethodGet, "/media/media-1/content", nil)
	r.Header.Set("Range", "bytes=0-4")
	r.Header.Set("If-Range", `"stale"`)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "hello world" {
		t.Fatalf("if-range status=%d body=%q", w.Code, w.Body.String())
	}
}
func TestAuthoredMutationsReplayAndConflict(t *testing.T) {
	repo := &authoredRepo{}
	service, _ := NewAuthoredService(repo, func() string { return "media-1" })
	m := Mutation{ActorID: "actor-1", IdempotencyKey: "0123456789abcdef"}
	input := MediaInput{AssetID: "11111111-1111-1111-1111-111111111111", Title: "title", Playback: Playback{Fit: "fit", Volume: 50}}
	if _, err := service.Create(context.Background(), m, input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), m, input); err != nil {
		t.Fatal(err)
	} // durable replay is repository-owned
	if repo.createCalls != 2 {
		t.Fatalf("repository receives replay contract calls=%d", repo.createCalls)
	}
	_, err := service.Update(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "abcdefghijklmnop"}, "media-1", 99, MediaPatch{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
}
