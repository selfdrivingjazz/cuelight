package media

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmilldotdev/cuelight/internal/media/localcas"
)

type thumbnailRepo struct{ media *authoredRepo }

func (r thumbnailRepo) PutMediaThumbnail(_ context.Context, source string, thumbnail Thumbnail, _ int64) error {
	if source == r.media.value.Artifact.Digest {
		r.media.value.Thumbnail = &thumbnail
	}
	return nil
}
func (r thumbnailRepo) ListThumbnailSources(context.Context, int) ([]Artifact, error) {
	return nil, nil
}

// An operator-captured poster is validated, downscaled, persisted, and then
// served immutable only under its digest-versioned URL.
func TestOperatorThumbnailUploadAndCaching(t *testing.T) {
	store, err := localcas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &authoredRepo{value: Media{ID: "media-1", Title: "clip", Version: 1, Artifact: Artifact{Digest: strings.Repeat("a", 64), Size: 11, MediaType: "video/mp4", State: ArtifactReady}}}
	service, _ := NewAuthoredService(repo, func() string { return "unused" })
	content := NewLocalCASContent(store)
	h := NewOperatorHandler(OperatorDependencies{Service: service, Content: content, Thumbnails: &ThumbnailService{Repository: thumbnailRepo{media: repo}, CAS: store, Content: content}})
	put := func(contentType string, body []byte) *httptest.ResponseRecorder {
		r := testAuthRequest(http.MethodPut, "/media/media-1/thumbnail", bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := put("text/plain", []byte("x")); w.Code != http.StatusBadRequest {
		t.Fatalf("non-image content type status=%d", w.Code)
	}
	if w := put("image/png", []byte("not a png")); w.Code != http.StatusBadRequest {
		t.Fatalf("undecodable body status=%d", w.Code)
	}
	var frame bytes.Buffer
	if err := png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 1280, 720))); err != nil {
		t.Fatal(err)
	}
	w := put("image/png", frame.Bytes())
	if w.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
	}
	var updated Media
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Thumbnail == nil || updated.Thumbnail.Width != 640 || updated.Thumbnail.Height != 360 || updated.Thumbnail.MediaType != "image/jpeg" {
		t.Fatalf("thumbnail=%+v", updated.Thumbnail)
	}
	digest := updated.Thumbnail.Digest

	get := func(path string, etag string) *httptest.ResponseRecorder {
		r := testAuthRequest(http.MethodGet, path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	versioned := get("/media/media-1/thumbnail?v="+digest, "")
	if versioned.Code != http.StatusOK || versioned.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(versioned.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("versioned status=%d headers=%v", versioned.Code, versioned.Header())
	}
	if _, _, err := image.Decode(versioned.Body); err != nil {
		t.Fatalf("served thumbnail is not an image: %v", err)
	}
	bare := get("/media/media-1/thumbnail", "")
	if bare.Code != http.StatusOK || strings.Contains(bare.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("bare URL must revalidate: status=%d cache=%q", bare.Code, bare.Header().Get("Cache-Control"))
	}
	if notModified := get("/media/media-1/thumbnail", `"`+digest+`"`); notModified.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match status=%d", notModified.Code)
	}
}
