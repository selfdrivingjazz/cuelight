package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/auth"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

// ScopeMiddleware authenticates an operator and requires a token scope.
type ScopeMiddleware func(scope string, next http.Handler) http.Handler

// ArtifactView is one release artifact for operators.
type ArtifactView struct {
	updates.Artifact
	Available bool `json:"available"`
	// DownloadURL is the origin-relative public mirror path.
	DownloadURL string `json:"downloadUrl"`
}

// ReleaseView is an imported release for operators.
type ReleaseView struct {
	Version                string         `json:"version"`
	Channel                string         `json:"channel"`
	PublishedAt            time.Time      `json:"publishedAt"`
	ImportedAt             time.Time      `json:"importedAt"`
	Source                 string         `json:"source"`
	KeyID                  string         `json:"keyId"`
	MinimumProtocolVersion string         `json:"minimumProtocolVersion"`
	Artifacts              []ArtifactView `json:"artifacts"`
}

func releaseView(release Release) ReleaseView {
	view := ReleaseView{Version: release.Version, Channel: release.Channel, PublishedAt: release.PublishedAt, ImportedAt: release.ImportedAt, Source: release.Source, KeyID: release.KeyID, MinimumProtocolVersion: release.MinimumProtocolVersion, Artifacts: make([]ArtifactView, 0, len(release.Artifacts))}
	for _, artifact := range release.Artifacts {
		view.Artifacts = append(view.Artifacts, ArtifactView{Artifact: artifact.Artifact, Available: artifact.Ready, DownloadURL: downloadPath(release.Version, artifact.Name)})
	}
	return view
}

type overviewView struct {
	Control ControlStatus `json:"control"`
	Feed    FeedStatus    `json:"feed"`
	Latest  *ReleaseView  `json:"latest,omitempty"`
}

// OperatorOptions configures the operator handler.
type OperatorOptions struct {
	Authorize      ScopeMiddleware
	ReadScope      string
	WriteScope     string
	MaxUploadBytes int64
	MaxJSONBytes   int64
}

// NewOperatorHandler serves the operator API below /api/v1/software.
func NewOperatorHandler(service *Service, options OperatorOptions) http.Handler {
	h := &operatorHandler{service: service, options: options}
	read := func(fn http.HandlerFunc) http.Handler { return options.Authorize(options.ReadScope, fn) }
	write := func(fn http.HandlerFunc) http.Handler { return options.Authorize(options.WriteScope, fn) }
	mux := http.NewServeMux()
	mux.Handle("GET /software", read(h.overview))
	mux.Handle("GET /software/releases", read(h.listReleases))
	mux.Handle("POST /software/releases", write(h.importRelease))
	mux.Handle("GET /software/releases/{version}", read(h.getRelease))
	mux.Handle("POST /software/check", write(h.checkFeed))
	mux.Handle("GET /software/nodes", read(h.listNodes))
	mux.Handle("GET /software/nodes/{nodeId}", read(h.getNode))
	mux.Handle("PUT /software/nodes/{nodeId}/target", write(h.setTarget))
	mux.Handle("DELETE /software/nodes/{nodeId}/target", write(h.clearTarget))
	mux.Handle("POST /software/rollouts", write(h.rollout))
	return mux
}

type operatorHandler struct {
	service *Service
	options OperatorOptions
}

func (h *operatorHandler) overview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.service.Overview(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	view := overviewView{Control: overview.Control, Feed: overview.Feed}
	if overview.Latest != nil {
		latest := releaseView(*overview.Latest)
		view.Latest = &latest
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *operatorHandler) listReleases(w http.ResponseWriter, r *http.Request) {
	releases, err := h.service.Releases(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]ReleaseView, 0, len(releases))
	for _, release := range releases {
		items = append(items, releaseView(release))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *operatorHandler) getRelease(w http.ResponseWriter, r *http.Request) {
	release, err := h.service.Release(r.Context(), r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, releaseView(release))
}

// UploadResult is the response of an operator release upload.
type UploadResult struct {
	ImportResult
	// Ignored lists uploaded files the manifest does not name.
	Ignored []string `json:"ignored,omitempty"`
}

// importRelease streams a multipart/form-data upload: the first part is the
// signed envelope (cuelight-release.json), every later file part is an
// artifact named by its file name. Artifacts are hashed while they are written
// to CAS, so nothing is buffered in memory.
func (h *operatorHandler) importRelease(w http.ResponseWriter, r *http.Request) {
	if h.options.MaxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.options.MaxUploadBytes)
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, fmt.Errorf("%w: multipart/form-data body required", ErrInvalid))
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		writeError(w, fmt.Errorf("%w: the first part must be %s", ErrInvalid, updates.ManifestFileName))
		return
	}
	if part.FormName() != "envelope" && filepath.Base(part.FileName()) != updates.ManifestFileName {
		writeError(w, fmt.Errorf("%w: the first part must be %s", ErrInvalid, updates.ManifestFileName))
		return
	}
	document, err := io.ReadAll(io.LimitReader(part, maxEnvelopeBytes+1))
	if err != nil || len(document) > maxEnvelopeBytes {
		writeError(w, fmt.Errorf("%w: unreadable release envelope", ErrInvalid))
		return
	}
	importer, err := h.service.BeginImport(r.Context(), SourceUpload, document)
	if err != nil {
		writeError(w, err)
		return
	}
	result := UploadResult{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, uploadReadError(err))
			return
		}
		name := filepath.Base(part.FileName())
		if part.FileName() == "" {
			continue
		}
		if _, listed := findArtifact(importer.Artifacts(), name); !listed {
			result.Ignored = append(result.Ignored, name)
			continue
		}
		if err := importer.PutArtifact(r.Context(), name, part); err != nil && !errors.Is(err, ErrArtifactMismatch) {
			writeError(w, uploadReadError(err))
			return
		}
	}
	imported, err := importer.Finish()
	result.ImportResult = imported
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if imported.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func uploadReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return fmt.Errorf("%w: upload exceeds %d bytes", ErrInvalid, tooLarge.Limit)
	}
	return fmt.Errorf("%w: %v", ErrInvalid, err)
}

func findArtifact(artifacts []updates.Artifact, name string) (updates.Artifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Name == name {
			return artifact, true
		}
	}
	return updates.Artifact{}, false
}

func (h *operatorHandler) checkFeed(w http.ResponseWriter, r *http.Request) {
	if h.service.Feed.URL == "" {
		writeError(w, fmt.Errorf("%w: no update feed URL is configured", ErrInvalid))
		return
	}
	// The check outlives a dropped browser connection; its outcome, success or
	// failure, is recorded in the feed status returned here.
	ctx := context.WithoutCancel(r.Context())
	_, _ = h.service.CheckFeed(ctx)
	status, err := h.service.FeedStatus(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *operatorHandler) listNodes(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.service.NodeStatuses(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": statuses})
}

func (h *operatorHandler) getNode(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.NodeStatus(r.Context(), r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *operatorHandler) setTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if err := h.decode(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	status, err := h.service.SetTarget(r.Context(), actor.ID, r.PathValue("nodeId"), body.Version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *operatorHandler) clearTarget(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.ActorFromContext(r.Context())
	status, err := h.service.ClearTarget(r.Context(), actor.ID, r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *operatorHandler) rollout(w http.ResponseWriter, r *http.Request) {
	var request RolloutRequest
	if err := h.decode(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	result, err := h.service.Rollout(r.Context(), actor.ID, request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *operatorHandler) decode(w http.ResponseWriter, r *http.Request, target any) error {
	limit := h.options.MaxJSONBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed JSON body", ErrInvalid)
	}
	return nil
}

// NewPlayerHandler serves the mTLS player endpoints below /api/v1:
// GET /player/software/releases/{version} (signed envelope) and
// GET|HEAD /player/software/artifacts/{sha256} (Range supported).
func NewPlayerHandler(service *Service, identity func(*http.Request) (string, error)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /player/software/releases/{version}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := identity(r); err != nil {
			writeProblem(w, http.StatusUnauthorized, "player identity is not authorized")
			return
		}
		envelope, err := service.Envelope(r.Context(), r.PathValue("version"))
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(envelope)
	})
	mux.HandleFunc("GET /player/software/artifacts/{sha256}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := identity(r); err != nil {
			writeProblem(w, http.StatusUnauthorized, "player identity is not authorized")
			return
		}
		sha256 := r.PathValue("sha256")
		file, err := service.OpenArtifact(r.Context(), sha256)
		if err != nil {
			writeError(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		serveImmutable(w, r, sha256, sha256, file)
	})
	return mux
}

// NewPublicHandler serves GET|HEAD /updates/{version}/{name}: the
// unauthenticated mirror of imported, verified release artifacts (for
// electron-updater, the Android shell, and "Get the app" downloads).
func NewPublicHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /updates/{version}/{name}", func(w http.ResponseWriter, r *http.Request) {
		file, artifact, err := service.OpenNamedArtifact(r.Context(), r.PathValue("version"), r.PathValue("name"))
		if err != nil {
			writeError(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", contentType(artifact.Name))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
		serveImmutable(w, r, artifact.Name, artifact.SHA256, file)
	})
	return mux
}

func serveImmutable(w http.ResponseWriter, r *http.Request, name, sha256 string, content io.ReadSeeker) {
	w.Header().Set("ETag", `"`+sha256+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, content)
}

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".apk":
		return "application/vnd.android.package-archive"
	case ".yml", ".yaml":
		return "text/yaml; charset=utf-8"
	case ".dmg":
		return "application/x-apple-diskimage"
	case ".zip":
		return "application/zip"
	case ".gz":
		return "application/gzip"
	case ".deb":
		return "application/vnd.debian.binary-package"
	default:
		return "application/octet-stream"
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrConflict), errors.Is(err, ErrDowngrade):
		status = http.StatusConflict
	case errors.Is(err, ErrUnverified), errors.Is(err, ErrArtifactMismatch):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = 499
	}
	detail := err.Error()
	if status == http.StatusInternalServerError {
		slog.Default().Error("software API request failed", "error", err)
		detail = "internal error"
	}
	writeProblem(w, status, detail)
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	title := http.StatusText(status)
	if title == "" {
		title = "Request failed"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}
