package appaccess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/page"
	"github.com/jmill/cable/internal/control/playlist"
	"github.com/jmill/cable/internal/control/remote"
)

// Identity resolves the requesting node (mTLS or browser-node bearer).
type Identity func(*http.Request) (string, error)

// NewPlayerHandler serves POST /player/app-tokens for nodes.
func NewPlayerHandler(service *Service, identity Identity) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /player/app-tokens", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil || nodeID == "" {
			writeProblem(w, http.StatusUnauthorized, errors.New("node authentication required"))
			return
		}
		var input struct {
			MediaID string `json:"mediaId"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		token, err := service.Mint(r.Context(), nodeID, input.MediaID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, withAPIURL(token, r))
	})
	return mux
}

// withAPIURL fills the API base from the request when no public URL is
// configured.
func withAPIURL(token Token, r *http.Request) Token {
	if token.APIURL == "" {
		token.APIURL = "https://" + r.Host + "/api/v1"
	}
	return token
}

type RemoteSessions interface {
	AppSession(nodeID string) (remote.SessionCreated, error)
	Poll(ctx context.Context, nodeID, id string, after int64, wait time.Duration) (remote.EventPage, error)
}

type AppDependencies struct {
	Service *Service
	// Media serves read-only /media, /media/{id}, /media/{id}/content and
	// /media/{id}/thumbnail for the actor placed in the request context.
	Media     http.Handler
	Playlists playlist.Repository
	Remote    RemoteSessions
}

// AppContext tells a page which screen and media item it is running as.
type AppContext struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	MediaID  string `json:"mediaId"`
}

// AppRemote is the phone remote a page may show (for example as a QR code).
type AppRemote struct {
	URL       string    `json:"url"`
	ShowQR    bool      `json:"showQr"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type remoteEventPage struct {
	Events       []remote.Event `json:"events"`
	NextSequence int64          `json:"nextSequence"`
	Remote       *AppRemote     `json:"remote,omitempty"`
}

// MaxRemoteWait bounds a remote event long poll.
const MaxRemoteWait = 25 * time.Second

// NewAppHandler serves the read-only /app API for pages holding an app token.
// Every response allows any origin; credentials are bearer tokens only.
func NewAppHandler(deps AppDependencies) http.Handler {
	h := appHandler{deps}
	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /app/", preflight)
	mux.HandleFunc("GET /app/context", h.authorized(false, h.context))
	mux.HandleFunc("GET /app/media", h.authorized(false, h.media))
	mux.HandleFunc("GET /app/media/{mediaId}", h.authorized(false, h.media))
	mux.HandleFunc("HEAD /app/media/{mediaId}/content", h.authorized(true, h.media))
	mux.HandleFunc("GET /app/media/{mediaId}/content", h.authorized(true, h.media))
	mux.HandleFunc("GET /app/media/{mediaId}/thumbnail", h.authorized(true, h.media))
	mux.HandleFunc("GET /app/playlists", h.authorized(false, h.playlists))
	mux.HandleFunc("GET /app/playlists/{playlistId}", h.authorized(false, h.playlist))
	mux.HandleFunc("GET /app/remote/events", h.authorized(false, h.remoteEvents))
	mux.HandleFunc("POST /app/token:renew", h.authorized(false, h.renew))
	return WithCORS(mux)
}

// WithCORS lets any origin read responses of next. Requests carry bearer
// tokens, never cookies.
func WithCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Access-Control-Allow-Origin", "*")
		header.Set("Access-Control-Expose-Headers", "ETag, Content-Range, Accept-Ranges, Content-Length, Content-Type")
		next.ServeHTTP(w, r)
	})
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	header := w.Header()
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Authorization, Range, If-Range, If-None-Match, Content-Type")
	header.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

type appHandler struct{ deps AppDependencies }

func (h appHandler) authorized(allowQuery bool, next func(http.ResponseWriter, *http.Request, Grant)) http.HandlerFunc {
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
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next(w, r, grant)
	}
}

func (h appHandler) context(w http.ResponseWriter, _ *http.Request, grant Grant) {
	writeJSON(w, http.StatusOK, AppContext{NodeID: grant.NodeID, NodeName: grant.NodeName, MediaID: grant.MediaID})
}

// media delegates to the library's media handler under /media as a
// read-only actor scoped to the app grant.
func (h appHandler) media(w http.ResponseWriter, r *http.Request, grant Grant) {
	if h.deps.Media == nil {
		writeError(w, errors.New("media handler is not configured"))
		return
	}
	forwarded := r.Clone(auth.WithActor(r.Context(), auth.Actor{ID: "app:" + grant.NodeID + ":" + grant.MediaID, Scopes: []string{"media_read"}}))
	forwarded.URL = cloneURL(r.URL)
	forwarded.URL.Path = strings.TrimPrefix(r.URL.Path, "/app")
	forwarded.URL.RawPath = ""
	query := forwarded.URL.Query()
	query.Del("access_token")
	forwarded.URL.RawQuery = query.Encode()
	forwarded.RequestURI = forwarded.URL.RequestURI()
	// The media handler decides caching for immutable content.
	w.Header().Del("Cache-Control")
	h.deps.Media.ServeHTTP(w, forwarded)
}

func cloneURL(value *url.URL) *url.URL {
	clone := *value
	return &clone
}

func (h appHandler) playlists(w http.ResponseWriter, r *http.Request, _ Grant) {
	result, err := playlist.List(r.Context(), h.deps.Playlists, r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h appHandler) playlist(w http.ResponseWriter, r *http.Request, _ Grant) {
	value, err := h.deps.Playlists.Playlist(r.Context(), r.PathValue("playlistId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", playlist.ETag(value.Version))
	writeJSON(w, http.StatusOK, value)
}

func (h appHandler) remote(nodeID string) *AppRemote {
	if h.deps.Remote == nil {
		return nil
	}
	session, err := h.deps.Remote.AppSession(nodeID)
	if err != nil {
		return nil
	}
	return &AppRemote{URL: session.RemoteURL, ShowQR: session.ShowQR, ExpiresAt: session.ExpiresAt}
}

func (h appHandler) remoteEvents(w http.ResponseWriter, r *http.Request, grant Grant) {
	query := r.URL.Query()
	after, err := strconv.ParseInt(defaultValue(query.Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(w, ErrInvalid)
		return
	}
	seconds, err := strconv.Atoi(defaultValue(query.Get("waitSeconds"), "0"))
	wait := time.Duration(seconds) * time.Second
	if err != nil || wait < 0 || wait > MaxRemoteWait {
		writeError(w, ErrInvalid)
		return
	}
	response := remoteEventPage{Events: []remote.Event{}, NextSequence: after}
	var session remote.SessionCreated
	sessionErr := remote.ErrSessionMissing
	if h.deps.Remote != nil {
		session, sessionErr = h.deps.Remote.AppSession(grant.NodeID)
	}
	if sessionErr == nil {
		events, err := h.deps.Remote.Poll(r.Context(), grant.NodeID, session.ID, after, wait)
		switch {
		case err == nil:
			response.Events, response.NextSequence = events.Events, events.NextSequence
		case errors.Is(err, remote.ErrSessionMissing), errors.Is(err, remote.ErrExpired):
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		default:
			writeError(w, err)
			return
		}
	} else if wait > 0 {
		// No active session: hold the poll so the page does not spin.
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

func (h appHandler) renew(w http.ResponseWriter, r *http.Request, grant Grant) {
	token, err := h.deps.Service.Renew(r.Context(), grant)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, withAPIURL(token, r))
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, playlist.ErrInvalidArgument), errors.Is(err, page.ErrCursorInvalid), errors.Is(err, page.ErrLimitInvalid), errors.Is(err, page.ErrOrderInvalid):
		writeProblem(w, http.StatusBadRequest, err)
	case errors.Is(err, ErrUnauthorized):
		writeProblem(w, http.StatusUnauthorized, err)
	case errors.Is(err, ErrForbidden):
		writeProblem(w, http.StatusForbidden, err)
	case errors.Is(err, ErrNotFound), errors.Is(err, playlist.ErrNotFound):
		writeProblem(w, http.StatusNotFound, err)
	default:
		writeProblem(w, http.StatusInternalServerError, errors.New("app request failed"))
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
