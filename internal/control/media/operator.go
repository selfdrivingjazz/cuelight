package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/auth"
	"github.com/jmilldotdev/cuelight/internal/control/page"
)

// MediaAuthorizer is intentionally small so callers can compose the handler
// beneath the controller's bearer-token middleware without coupling this
// package to the controller router.
type MediaAuthorizer func(auth.Actor, string, string, string) error

type OperatorDependencies struct {
	Service      *AuthoredService
	Content      ContentReader
	Thumbnails   *ThumbnailService
	Web          WebCapture
	Authorize    MediaAuthorizer
	MaxJSONBytes int64
}

// NewOperatorHandler mounts /media operations relative to the caller's API
// prefix. Authentication is composed by the caller and must put auth.Actor in
// the request context, as preview.NewOperatorHandler does.
func NewOperatorHandler(deps OperatorDependencies) http.Handler {
	mux := http.NewServeMux()
	h := &operatorHandler{deps: deps}
	mux.HandleFunc("POST /media", h.create)
	mux.HandleFunc("GET /media", h.list)
	mux.HandleFunc("GET /media/{mediaId}", h.get)
	mux.HandleFunc("PATCH /media/{mediaId}", h.update)
	mux.HandleFunc("DELETE /media/{mediaId}", h.delete)
	mux.HandleFunc("POST /media/{mediaId}/refresh", h.refresh)
	mux.HandleFunc("GET /media/{mediaId}/usage", h.usage)
	mux.HandleFunc("HEAD /media/{mediaId}/content", h.content)
	mux.HandleFunc("GET /media/{mediaId}/content", h.content)
	mux.HandleFunc("GET /media/{mediaId}/thumbnail", h.thumbnail)
	mux.HandleFunc("PUT /media/{mediaId}/thumbnail", h.putThumbnail)
	return mux
}

// NewMediaHandler is an explicit alias useful to composition code mounting
// multiple resource handlers.
func NewMediaHandler(deps OperatorDependencies) http.Handler { return NewOperatorHandler(deps) }

type operatorHandler struct{ deps OperatorDependencies }

func (h *operatorHandler) actor(r *http.Request, action, resourceID string) (auth.Actor, error) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || strings.TrimSpace(actor.ID) == "" {
		return auth.Actor{}, ErrUnauthorized
	}
	if h.deps.Authorize != nil {
		if err := h.deps.Authorize(actor, action, "media", resourceID); err != nil {
			return auth.Actor{}, err
		}
	}
	return actor, nil
}
func (h *operatorHandler) create(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_write", "")
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	var input MediaInput
	if err := decodeMediaJSON(w, r, h.deps.MaxJSONBytes, &input); err != nil {
		writeMediaError(w, err)
		return
	}
	mutation := Mutation{ActorID: actor.ID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}
	if input.Kind == KindWeb {
		value, created, err := h.deps.Service.CreateWeb(r.Context(), mutation, input)
		if err != nil {
			writeMediaError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		if (created || value.Thumbnail == nil) && h.deps.Web != nil {
			h.deps.Web.Capture(value.ID, value.URL)
		}
		setMediaETag(w, value)
		writeMediaJSON(w, status, value)
		return
	}
	value, err := h.deps.Service.Create(r.Context(), mutation, input)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if value.Thumbnail == nil && h.deps.Thumbnails != nil {
		// Decodable images get their poster at ingest so the library never
		// has to download the original to render a card.
		if thumbnail, err := h.deps.Thumbnails.Generate(r.Context(), value.Artifact); err == nil {
			value.Thumbnail = &thumbnail
		}
	}
	writeMediaJSON(w, http.StatusCreated, value)
}
func (h *operatorHandler) delete(w http.ResponseWriter, r *http.Request) {
	if _, err := h.actor(r, "media_write", r.PathValue("mediaId")); err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	if err := h.deps.Service.DeleteWeb(r.Context(), r.PathValue("mediaId")); err != nil {
		writeMediaError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *operatorHandler) refresh(w http.ResponseWriter, r *http.Request) {
	if _, err := h.actor(r, "media_write", r.PathValue("mediaId")); err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil || h.deps.Web == nil {
		writeMediaError(w, ErrCaptureDisabled)
		return
	}
	current, err := h.deps.Service.Get(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if current.Kind != KindWeb {
		writeMediaError(w, ErrWrongKind)
		return
	}
	if err := h.deps.Web.Refresh(r.Context(), current.ID); err != nil {
		writeMediaError(w, err)
		return
	}
	value, err := h.deps.Service.Get(r.Context(), current.ID)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	setMediaETag(w, value)
	writeMediaJSON(w, http.StatusOK, value)
}
func (h *operatorHandler) list(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_read", "")
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	request, err := page.Parse(r.URL.Query())
	if err != nil {
		writeMediaError(w, err)
		return
	}
	filter := MediaListFilter{}
	if raw := strings.TrimSpace(r.URL.Query().Get("playable")); raw != "" {
		v, e := strconv.ParseBool(raw)
		if e != nil {
			writeMediaError(w, &ValidationError{Field: "playable", Reason: "must be true or false"})
			return
		}
		filter.Playable = &v
	}
	switch kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind {
	case "", KindFile, KindWeb:
		filter.Kind = kind
	default:
		writeMediaError(w, &ValidationError{Field: "kind", Reason: "must be file or web"})
		return
	}
	if mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType")); mediaType != "" {
		if mediaType != "image" && mediaType != "video" && mediaType != "audio" {
			writeMediaError(w, &ValidationError{Field: "mediaType", Reason: "must be image, video, or audio"})
			return
		}
		filter.MediaType = mediaType
	}
	for _, assetID := range r.URL.Query()["assetId"] {
		assetID = strings.TrimSpace(assetID)
		if !validUUID(assetID) || len(filter.AssetIDs) >= 100 {
			writeMediaError(w, &ValidationError{Field: "assetId", Reason: "must contain at most 100 non-empty IDs"})
			return
		}
		filter.AssetIDs = append(filter.AssetIDs, assetID)
	}
	_ = actor
	result, err := h.deps.Service.List(r.Context(), request, filter)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeMediaJSON(w, http.StatusOK, result)
}
func (h *operatorHandler) get(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_read", r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	_ = actor
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	value, err := h.deps.Service.Get(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	setMediaETag(w, value)
	writeMediaJSON(w, http.StatusOK, value)
}
func (h *operatorHandler) update(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_write", r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	version, err := parseMediaMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	var patch MediaPatch
	if err := decodeMediaJSON(w, r, h.deps.MaxJSONBytes, &patch); err != nil {
		writeMediaError(w, err)
		return
	}
	value, err := h.deps.Service.Update(r.Context(), Mutation{ActorID: actor.ID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}, r.PathValue("mediaId"), version, patch)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	setMediaETag(w, value)
	writeMediaJSON(w, http.StatusOK, value)
}
func (h *operatorHandler) usage(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_read", r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	_ = actor
	if h.deps.Service == nil {
		writeMediaError(w, errors.New("media service is not configured"))
		return
	}
	value, err := h.deps.Service.Usage(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeMediaJSON(w, http.StatusOK, value)
}
func (h *operatorHandler) content(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, "media_read", r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	_ = actor
	if h.deps.Service == nil || h.deps.Content == nil {
		writeMediaError(w, errors.New("media content service is not configured"))
		return
	}
	value, err := h.deps.Service.Get(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if !value.Playable() {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	object, err := h.deps.Content.Stat(r.Context(), value.Artifact.Digest)
	if err != nil {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	if object.Digest != value.Artifact.Digest || object.Size != value.Artifact.Size {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	reader, err := h.deps.Content.Open(r.Context(), value.Artifact.Digest)
	if err != nil {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	defer reader.Close()
	etag := `"` + value.Artifact.Digest + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", value.Artifact.MediaType)
	serveRequest := r
	if raw := strings.TrimSpace(r.Header.Get("If-Range")); raw != "" && !mediaIfRangeMatches(raw, etag, value.UpdatedAt) {
		clone := r.Clone(r.Context())
		clone.Header = clone.Header.Clone()
		clone.Header.Del("Range")
		serveRequest = clone
	}
	http.ServeContent(w, serveRequest, value.Title, value.UpdatedAt, reader)
}

func (h *operatorHandler) thumbnail(w http.ResponseWriter, r *http.Request) {
	if _, err := h.actor(r, "media_read", r.PathValue("mediaId")); err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil || h.deps.Content == nil {
		writeMediaError(w, errors.New("media content service is not configured"))
		return
	}
	value, err := h.deps.Service.Get(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if value.Thumbnail == nil {
		writeMediaError(w, ErrNotFound)
		return
	}
	etag := `"` + value.Thumbnail.Digest + `"`
	w.Header().Set("ETag", etag)
	// A versioned URL (?v=digest) names immutable bytes; the bare URL must
	// revalidate because a new poster may replace the old one.
	if r.URL.Query().Get("v") == value.Thumbnail.Digest {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	reader, err := h.deps.Content.Open(r.Context(), value.Thumbnail.Digest)
	if err != nil {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", value.Thumbnail.MediaType)
	http.ServeContent(w, r, "", time.Time{}, reader)
}

func (h *operatorHandler) putThumbnail(w http.ResponseWriter, r *http.Request) {
	if _, err := h.actor(r, "media_write", r.PathValue("mediaId")); err != nil {
		writeMediaError(w, err)
		return
	}
	if h.deps.Service == nil || h.deps.Thumbnails == nil {
		writeMediaError(w, errors.New("media thumbnail service is not configured"))
		return
	}
	switch strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) {
	case "image/png", "image/jpeg":
	default:
		writeMediaError(w, &ValidationError{Field: "Content-Type", Reason: "must be image/png or image/jpeg"})
		return
	}
	value, err := h.deps.Service.Get(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if !value.Playable() {
		writeMediaError(w, ErrArtifactUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ThumbnailMaxUploadBytes))
	if err != nil {
		writeMediaError(w, &ValidationError{Field: "body", Reason: "thumbnail must be at most 8 MiB"})
		return
	}
	thumbnail, err := h.deps.Thumbnails.Store(r.Context(), value.Artifact.Digest, body)
	if errors.Is(err, ErrThumbnailUnsupported) {
		writeMediaError(w, &ValidationError{Field: "body", Reason: "must be a decodable PNG or JPEG image"})
		return
	}
	if err != nil {
		writeMediaError(w, err)
		return
	}
	value.Thumbnail = &thumbnail
	writeMediaJSON(w, http.StatusOK, value)
}

func mediaIfRangeMatches(value, etag string, updated time.Time) bool {
	if value == etag {
		return true
	}
	when, err := http.ParseTime(value)
	return err == nil && !updated.IsZero() && !updated.After(when)
}
func parseMediaMatch(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("%w: If-Match is required", ErrPrecondition)
	}
	raw = strings.Trim(raw, `"`)
	if strings.HasPrefix(raw, "media:") {
		parts := strings.Split(raw, ":")
		raw = parts[len(parts)-1]
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: invalid If-Match", ErrPrecondition)
	}
	return n, nil
}
func decodeMediaJSON(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	if limit <= 0 {
		limit = 4 << 20
	}
	if r.Body == nil {
		return fmt.Errorf("%w: request body is required", ErrInvalidArgument)
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("%w: body too large", ErrInvalidArgument)
		}
		return fmt.Errorf("%w: malformed JSON", ErrInvalidArgument)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: exactly one JSON value is required", ErrInvalidArgument)
	}
	return nil
}
func setMediaETag(w http.ResponseWriter, v Media) {
	w.Header().Set("ETag", fmt.Sprintf(`"media:%s:%d"`, v.ID, v.Version))
}
func writeMediaJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type mediaProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeMediaError(w http.ResponseWriter, err error) {
	status := mediaStatus(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mediaProblem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: err.Error()})
}
func mediaStatus(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPrecondition), errors.Is(err, ErrConflict):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, page.ErrCursorInvalid), errors.Is(err, page.ErrLimitInvalid), errors.Is(err, page.ErrSortInvalid), errors.Is(err, page.ErrOrderInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrArtifactUnavailable), errors.Is(err, ErrWrongKind), errors.Is(err, ErrInUse):
		return http.StatusConflict
	case errors.Is(err, ErrCaptureDisabled):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
