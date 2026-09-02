package preview

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/protocol"
)

// NewOperatorHandler mounts operator screenshot creation and inspection routes.
// Authentication is intentionally composed by the caller; it must place an
// auth.Actor in the request context (the normal operator middleware does so).
func NewOperatorHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	if service == nil {
		service = &Service{}
	}
	mux.HandleFunc("POST /screenshots", service.createHTTP)
	mux.HandleFunc("GET /screenshots/{jobId}", service.getHTTP)
	mux.HandleFunc("GET /screenshots/{jobId}/content", service.getContentHTTP)
	return mux
}

// NodeIdentity resolves the authenticated mTLS node identity. It is injectable
// for tests and for servers that already performed certificate validation.
type NodeIdentity func(*http.Request) (string, error)

// NewPlayerHandler mounts the node claim and completion protocol. The default
// identity resolver requires the same node certificate used by playerapi.
func NewPlayerHandler(service *Service, identity NodeIdentity) http.Handler {
	mux := http.NewServeMux()
	if service == nil {
		service = &Service{}
	}
	if identity == nil {
		identity = func(r *http.Request) (string, error) { return protocol.NodeIdentity(r, nil) }
	}
	mux.HandleFunc("GET /player/screenshots/next", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil || strings.TrimSpace(nodeID) == "" {
			writeError(w, fmt.Errorf("%w: %v", ErrUnauthorized, err))
			return
		}
		job, ok, err := service.Next(r.Context(), nodeID)
		if err != nil {
			writeError(w, err)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /player/screenshots/{jobId}/claim", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil || strings.TrimSpace(nodeID) == "" {
			writeError(w, fmt.Errorf("%w: %v", ErrUnauthorized, err))
			return
		}
		job, err := service.Claim(r.Context(), nodeID, r.PathValue("jobId"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /player/screenshots/{jobId}/complete", func(w http.ResponseWriter, r *http.Request) {
		nodeID, err := identity(r)
		if err != nil || strings.TrimSpace(nodeID) == "" {
			writeError(w, fmt.Errorf("%w: %v", ErrUnauthorized, err))
			return
		}
		var job ScreenshotJob
		if r.Header.Get("Content-Type") == "image/png" || strings.HasPrefix(r.Header.Get("Content-Type"), "image/png;") {
			size := r.ContentLength
			if size < 0 {
				writeError(w, fmt.Errorf("%w: Content-Length is required", ErrInvalidArgument))
				return
			}
			job, err = service.CompleteUpload(r.Context(), nodeID, r.PathValue("jobId"), r.Header.Get("X-Artifact-Digest"), "image/png", size, io.LimitReader(r.Body, 16<<20+1))
		} else {
			var completion Completion
			if err := decodeJSON(r, &completion); err != nil {
				writeError(w, err)
				return
			}
			job, err = service.Complete(r.Context(), nodeID, r.PathValue("jobId"), completion)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	return mux
}

func actorID(r *http.Request) (string, error) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || strings.TrimSpace(actor.ID) == "" {
		return "", ErrUnauthorized
	}
	return actor.ID, nil
}

func (s *Service) createHTTP(w http.ResponseWriter, r *http.Request) {
	actor, err := actorID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var wire struct {
		NodeID     string          `json:"nodeId"`
		Expected   ExpectedPreview `json:"expected"`
		TTLSeconds int64           `json:"ttlSeconds"`
	}
	if err := decodeJSON(r, &wire); err != nil {
		writeError(w, err)
		return
	}
	if wire.TTLSeconds < 0 {
		writeError(w, fmt.Errorf("%w: ttlSeconds must not be negative", ErrInvalidArgument))
		return
	}
	input := CreateInput{NodeID: wire.NodeID, Expected: wire.Expected}
	if wire.TTLSeconds > 0 {
		input.TTL = time.Duration(wire.TTLSeconds) * time.Second
	}
	result, err := s.Create(r.Context(), Mutation{ActorID: actor, IdempotencyKey: r.Header.Get("Idempotency-Key")}, input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result.Job)
}

func (s *Service) getHTTP(w http.ResponseWriter, r *http.Request) {
	actor, err := actorID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	job, err := s.Get(r.Context(), actor, r.PathValue("jobId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Service) getContentHTTP(w http.ResponseWriter, r *http.Request) {
	actor, err := actorID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	content, err := s.Content(r.Context(), actor, r.PathValue("jobId"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer content.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", content.MediaType)
	w.Header().Set("ETag", content.ETag)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", content.Size))
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, content.Body, content.Size)
}
func decodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: request body is required", ErrInvalidArgument)
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrInvalidArgument, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: request body must contain one JSON value", ErrInvalidArgument)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeError(w http.ResponseWriter, err error) {
	status := statusFor(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: err.Error()})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrConflict), errors.Is(err, ErrTerminal), errors.Is(err, ErrNotClaimed):
		return http.StatusConflict
	case errors.Is(err, ErrExpired):
		return http.StatusGone
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrArtifactUnavailable), errors.Is(err, ErrArtifactMismatch):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
