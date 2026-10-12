package usage

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/control/auth"
	"github.com/selfdrivingjazz/cuelight/internal/control/page"
)

type Handler struct {
	Service *Service
}

// NewHandler mounts the usage route below an API root. Authentication is
// intentionally supplied by the composing operator middleware; the actor is
// read from auth context and authorization remains in Service before SQL.
func NewHandler(service *Service) http.Handler {
	return NewHandlerWithDependencies(Dependencies{Service: service})
}

type Dependencies struct {
	Service *Service
}

func NewHandlerWithDependencies(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	h := &Handler{Service: deps.Service}
	mux.HandleFunc("GET /usage/{kind}/{id}", h.get)
	return mux
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil {
		writeError(w, http.StatusInternalServerError, errors.New("usage service is required"))
		return
	}
	kind, err := ParseKind(r.PathValue("kind"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request, err := page.Parse(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Sort != "" {
		writeError(w, http.StatusBadRequest, page.ErrSortInvalid)
		return
	}
	if request.Order != "asc" {
		writeError(w, http.StatusBadRequest, page.ErrOrderInvalid)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	result, err := h.Service.Usage(r.Context(), Query{
		ActorID: actor.ID, Kind: kind, ResourceID: strings.TrimSpace(r.PathValue("id")),
		Cursor: request.Cursor, Limit: request.Limit,
	})
	if err != nil {
		writeUsageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeUsageError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrUnsupportedKind), errors.Is(err, page.ErrLimitInvalid), errors.Is(err, page.ErrCursorInvalid), errors.Is(err, page.ErrCursorTampered):
		status = http.StatusBadRequest
	case errors.Is(err, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	}
	writeError(w, status, err)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
