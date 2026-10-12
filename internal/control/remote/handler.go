package remote

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/protocol"
)

// Identity resolves the requesting node. Nil uses the mTLS node identity.
type Identity func(*http.Request) (string, error)

func NewHandler(service *Service, identity Identity) http.Handler {
	if identity == nil {
		identity = func(r *http.Request) (string, error) { return protocol.NodeIdentity(r, nil) }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /player/remote-sessions", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, err)
			return
		}
		var input struct {
			DesiredGeneration int64 `json:"desiredGeneration"`
		}
		if err := decode(w, r, &input); err != nil {
			writeProblem(w, http.StatusBadRequest, err)
			return
		}
		created, err := service.Create(r.Context(), nodeID, input.DesiredGeneration)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})
	mux.HandleFunc("GET /player/remote-sessions/{sessionId}/events", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, err)
			return
		}
		after, err := strconv.ParseInt(defaultValue(r.URL.Query().Get("after"), "0"), 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, ErrInvalid)
			return
		}
		seconds, err := strconv.Atoi(defaultValue(r.URL.Query().Get("waitSeconds"), "0"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, ErrInvalid)
			return
		}
		page, err := service.Poll(r.Context(), nodeID, r.PathValue("sessionId"), after, time.Duration(seconds)*time.Second)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	mux.HandleFunc("POST /remote/sessions/{sessionId}/events", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			writeProblem(w, http.StatusUnauthorized, ErrUnauthorized)
			return
		}
		var event Event
		if err := decode(w, r, &event); err != nil {
			writeProblem(w, http.StatusBadRequest, err)
			return
		}
		if err := service.Post(r.Context(), r.PathValue("sessionId"), token, event); err != nil {
			writeRemoteError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	return nil
}
func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func writeRemoteError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrExpired), errors.Is(err, ErrSessionMissing):
		status = http.StatusGone
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrDisabled):
		status = http.StatusForbidden
	case errors.Is(err, ErrRateLimited):
		status = http.StatusTooManyRequests
	}
	writeProblem(w, status, err)
}
func writeProblem(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"status": status, "detail": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
