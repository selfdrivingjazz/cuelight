package cableguide

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/cable"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/remote"
)

// Identity resolves the requesting node (mTLS or browser-node bearer).
type Identity func(*http.Request) (string, error)

// NewPlayerHandler serves POST /player/cable-guide-tokens for nodes.
func NewPlayerHandler(service *Service, identity Identity) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /player/cable-guide-tokens", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil || nodeID == "" {
			writeProblem(w, http.StatusUnauthorized, ErrUnauthorized)
			return
		}
		var input struct {
			DeploymentID string `json:"deploymentId"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		token, err := service.Mint(r.Context(), nodeID, input.DeploymentID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, token)
	})
	return mux
}

type Presentations interface {
	Build(context.Context, string) (cable.Presentation, error)
}

type RemoteSessions interface {
	Guide(nodeID string) (remote.SessionCreated, error)
	Poll(ctx context.Context, nodeID, id string, after int64, wait time.Duration) (remote.EventPage, error)
}

type GuideDependencies struct {
	Service       *Service
	Presentations Presentations
	Remote        RemoteSessions
	Artifacts     cable.ArtifactRepository
	Content       media.ContentReader
}

// NewGuideHandler serves /cable-guide/* for the hosted guide, authenticated
// with a guide bearer token.
func NewGuideHandler(deps GuideDependencies) http.Handler {
	h := guideHandler{deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cable-guide/presentation", h.authorized(false, h.presentation))
	mux.HandleFunc("GET /cable-guide/media/{assetId}", h.authorized(true, h.media))
	mux.HandleFunc("HEAD /cable-guide/media/{assetId}", h.authorized(true, h.media))
	mux.HandleFunc("GET /cable-guide/remote/events", h.authorized(false, h.remoteEvents))
	mux.HandleFunc("POST /cable-guide/token:renew", h.authorized(false, h.renew))
	return mux
}

type guideHandler struct{ deps GuideDependencies }

func (h guideHandler) authorized(allowQuery bool, next func(http.ResponseWriter, *http.Request, Grant)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if value := r.Header.Get("Authorization"); len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
			token = value[7:]
		} else if allowQuery {
			token = r.URL.Query().Get("access_token")
		}
		grant, err := h.deps.Service.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r, grant)
	}
}

func (h guideHandler) presentation(w http.ResponseWriter, r *http.Request, grant Grant) {
	presentation, err := h.deps.Presentations.Build(r.Context(), grant.DeploymentID)
	if err != nil {
		writeError(w, err)
		return
	}
	presentation.Remote = h.remote(grant.NodeID)
	writeJSON(w, http.StatusOK, presentation)
}

func (h guideHandler) remote(nodeID string) *cable.PresentationRemote {
	if h.deps.Remote == nil {
		return nil
	}
	session, err := h.deps.Remote.Guide(nodeID)
	if err != nil {
		return nil
	}
	return &cable.PresentationRemote{URL: session.RemoteURL, ShowQR: session.ShowQR, ExpiresAt: session.ExpiresAt}
}

func (h guideHandler) media(w http.ResponseWriter, r *http.Request, grant Grant) {
	assetID := r.PathValue("assetId")
	referenced, err := h.deps.Service.Repository.DeploymentReferencesAsset(r.Context(), grant.DeploymentID, assetID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !referenced {
		writeError(w, ErrNotFound)
		return
	}
	artifacts, err := h.deps.Artifacts.CableArtifactsForAsset(r.Context(), assetID)
	if err != nil {
		writeError(w, err)
		return
	}
	var artifact *cable.Artifact
	for i := range artifacts {
		if artifacts[i].State == "ready" {
			artifact = &artifacts[i]
			break
		}
	}
	if artifact == nil {
		writeError(w, ErrNotFound)
		return
	}
	object, err := h.deps.Content.Stat(r.Context(), artifact.Digest)
	if err != nil || object.Digest != artifact.Digest {
		writeError(w, ErrNotFound)
		return
	}
	reader, err := h.deps.Content.Open(r.Context(), artifact.Digest)
	if err != nil {
		writeError(w, ErrNotFound)
		return
	}
	defer reader.Close()
	// Content is immutable per digest, so the private cache may keep it.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("ETag", `"`+artifact.Digest+`"`)
	w.Header().Set("Content-Type", artifact.MediaType)
	http.ServeContent(w, r, "", time.Time{}, reader)
}

const maxRemoteWait = 30 * time.Second

type remoteEventsResponse struct {
	Events []remote.Event            `json:"events"`
	Next   int64                     `json:"next"`
	Remote *cable.PresentationRemote `json:"remote,omitempty"`
}

func (h guideHandler) remoteEvents(w http.ResponseWriter, r *http.Request, grant Grant) {
	query := r.URL.Query()
	after, err := strconv.ParseInt(defaultValue(query.Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(w, ErrInvalid)
		return
	}
	seconds, err := strconv.Atoi(defaultValue(query.Get("wait"), "0"))
	wait := time.Duration(seconds) * time.Second
	if err != nil || wait < 0 || wait > maxRemoteWait {
		writeError(w, ErrInvalid)
		return
	}
	response := remoteEventsResponse{Events: []remote.Event{}, Next: after}
	var session remote.SessionCreated
	sessionErr := remote.ErrSessionMissing
	if h.deps.Remote != nil {
		session, sessionErr = h.deps.Remote.Guide(grant.NodeID)
	}
	if sessionErr == nil {
		page, err := h.deps.Remote.Poll(r.Context(), grant.NodeID, session.ID, after, wait)
		switch {
		case err == nil:
			response.Events, response.Next = page.Events, page.NextSequence
		case errors.Is(err, remote.ErrSessionMissing), errors.Is(err, remote.ErrExpired):
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		default:
			writeError(w, err)
			return
		}
	} else if wait > 0 {
		// No active session: hold the poll so the guide does not spin.
		timer := time.NewTimer(wait)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	response.Remote = h.remote(grant.NodeID)
	writeJSON(w, http.StatusOK, response)
}

func (h guideHandler) renew(w http.ResponseWriter, r *http.Request, grant Grant) {
	token, err := h.deps.Service.Renew(r.Context(), grant)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeProblem(w, http.StatusBadRequest, err)
	case errors.Is(err, ErrUnauthorized):
		writeProblem(w, http.StatusUnauthorized, err)
	case errors.Is(err, ErrForbidden):
		writeProblem(w, http.StatusForbidden, err)
	case errors.Is(err, ErrNotFound), errors.Is(err, cable.ErrNotFound):
		writeProblem(w, http.StatusNotFound, err)
	default:
		writeProblem(w, http.StatusInternalServerError, errors.New("cable guide request failed"))
	}
}

func writeProblem(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
