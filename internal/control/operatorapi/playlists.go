package operatorapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/control/auth"
	"github.com/selfdrivingjazz/cuelight/internal/control/playlist"
)

func playlistMutation(r *http.Request, key string, expected int64) playlist.Mutation {
	actor, _ := auth.ActorFromContext(r.Context())
	return playlist.Mutation{ActorID: actor.ID, IdempotencyKey: key, ExpectedVersion: expected, Now: time.Now().UTC()}
}

// playlistIfMatch parses the "v<version>" entity tag playlists are served with.
func playlistIfMatch(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if !strings.HasPrefix(value, "\"v") || !strings.HasSuffix(value, "\"") {
		return 0, ErrPrecondition
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, "\"v"), "\""), 10, 64)
	if err != nil || version <= 0 {
		return 0, ErrPrecondition
	}
	return version, nil
}

func (h *Handler) playlists() (playlist.Repository, error) {
	if h.deps.Playlists == nil {
		return nil, errors.New("playlist repository is not configured")
	}
	return h.deps.Playlists, nil
}

func (h *Handler) listPlaylists(w http.ResponseWriter, r *http.Request) {
	repository, err := h.playlists()
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := playlist.List(r.Context(), repository, r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	repository, err := h.playlists()
	if err != nil {
		writeError(w, err)
		return
	}
	var input playlist.Input
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := repository.CreatePlaylist(r.Context(), playlistMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", playlist.ETag(result.Version))
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) getPlaylist(w http.ResponseWriter, r *http.Request) {
	repository, err := h.playlists()
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repository.Playlist(r.Context(), r.PathValue("playlistId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", playlist.ETag(result.Version))
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) updatePlaylist(w http.ResponseWriter, r *http.Request) {
	version, err := playlistIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	repository, err := h.playlists()
	if err != nil {
		writeError(w, err)
		return
	}
	var input playlist.Input
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := repository.UpdatePlaylist(r.Context(), playlistMutation(r, "", version), r.PathValue("playlistId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", playlist.ETag(result.Version))
	writeJSON(w, http.StatusOK, result)
}
