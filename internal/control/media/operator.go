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

	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/page"
)

// MediaAuthorizer is intentionally small so callers can compose the handler
// beneath the controller's bearer-token middleware without coupling this
// package to the controller router.
type MediaAuthorizer func(auth.Actor, string, string, string) error

type OperatorDependencies struct {
	Service      *AuthoredService
	Content      ContentReader
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
	mux.HandleFunc("GET /media/{mediaId}/usage", h.usage)
	mux.HandleFunc("HEAD /media/{mediaId}/content", h.content)
	mux.HandleFunc("GET /media/{mediaId}/content", h.content)
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
	value, err := h.deps.Service.Create(r.Context(), Mutation{ActorID: actor.ID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}, input)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeMediaJSON(w, http.StatusCreated, value)
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
	case errors.Is(err, ErrArtifactUnavailable):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
