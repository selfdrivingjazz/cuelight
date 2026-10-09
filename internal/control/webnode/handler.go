package webnode

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jmilldotdev/cuelight/internal/control/auth"
)

const maxJSONBytes = 16 << 10

// NewBrowserHandler serves the unauthenticated pairing routes and mounts
// player (a player API handler resolving identity via Service.Identity) at
// /player/. Mount it with the /browser-node prefix stripped.
func NewBrowserHandler(service *Service, player http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /claims", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserAgent string `json:"userAgent"`
		}
		if err := decodeOptionalJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		userAgent := body.UserAgent
		if userAgent == "" {
			userAgent = r.UserAgent()
		}
		started, err := service.StartClaim(r.Context(), userAgent)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, started)
	})
	mux.HandleFunc("GET /claims/{claimId}", func(w http.ResponseWriter, r *http.Request) {
		status, err := service.PollClaim(r.Context(), r.PathValue("claimId"), bearer(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("POST /credentials:renew", func(w http.ResponseWriter, r *http.Request) {
		issued, err := service.Renew(r.Context(), bearer(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, issued)
	})
	if player != nil {
		mux.Handle("/player/", player)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

// Identity resolves a browser credential for the player API. Media elements
// cannot set headers, so artifact content requests may carry the token as
// the access_token query parameter instead.
func (s *Service) Identity(r *http.Request) (string, error) {
	token := bearer(r)
	if token == "" && strings.HasSuffix(r.URL.Path, "/content") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		token = r.URL.Query().Get("access_token")
	}
	credential, err := s.Authenticate(r.Context(), token)
	if err != nil {
		return "", err
	}
	return credential.NodeID, nil
}

// OperatorAuth wraps a handler with operator authentication for scope.
type OperatorAuth func(scope string, next http.Handler) http.Handler

// NewOperatorHandler serves /browser-nodes routes for the operator API.
func NewOperatorHandler(service *Service, authorize OperatorAuth, enrollScope, readScope, revokeScope string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /browser-nodes/claims", authorize(enrollScope, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		actor, _ := auth.ActorFromContext(r.Context())
		node, err := service.RedeemClaim(r.Context(), actor.ID, body.Code, body.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, node)
	})))
	mux.Handle("GET /browser-nodes", authorize(readScope, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodes, err := service.ListNodes(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": nodes})
	})))
	mux.Handle("POST /browser-nodes/{nodeId}/revoke", authorize(revokeScope, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.RevokeNode(r.Context(), r.PathValue("nodeId")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	return mux
}

func bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return decodeJSON(w, r, target)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.Join(ErrInvalidRequest, errors.New("request body is required"))
		}
		return errors.Join(ErrInvalidRequest, errors.New("malformed JSON"))
	}
	return nil
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := statusFor(err)
	detail := err.Error()
	if status == http.StatusInternalServerError {
		slog.Error("browser node request failed", "error", err)
		detail = ""
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrClaimNotFound), errors.Is(err, ErrNodeNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrClaimConflict):
		return http.StatusConflict
	case errors.Is(err, ErrClaimGone):
		return http.StatusGone
	case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrCodeCollision):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
