package playerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jmilldotdev/cuelight/internal/player/client"
)

// NewHandler mounts only player routes. The caller can mount this handler at
// /api/v1, allowing operator and system routes to be composed independently.
func NewHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	if service == nil {
		service = &Service{}
	}
	mux.HandleFunc("PUT /player/capabilities", service.putCapabilities)
	mux.HandleFunc("GET /player/assignment", service.getAssignment)
	mux.HandleFunc("GET /player/releases/{releaseID}", service.getRelease)
	mux.HandleFunc("POST /player/reports", service.postReport)
	mux.HandleFunc("POST /player/events:append", service.appendEvents)
	mux.HandleFunc("HEAD /player/artifacts/{digest}/content", service.headArtifact)
	mux.HandleFunc("GET /player/artifacts/{digest}/content", service.getArtifact)
	return mux
}

func (s *Service) putCapabilities(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var report client.CapabilityReport
	if err := decodeBody(w, r, s.Limits.normalized().MaxBodyBytes, &report); err != nil {
		writeError(w, err)
		return
	}
	if err := s.PutCapabilities(r.Context(), nodeID, report); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) getAssignment(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wait := time.Duration(0)
	if raw := r.URL.Query().Get("waitSeconds"); raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds < 0 {
			writeError(w, ErrInvalidRequest)
			return
		}
		wait = time.Duration(seconds) * time.Second
	}
	result, err := s.GetAssignment(r.Context(), nodeID, r.Header.Get("If-None-Match"), wait)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	if result.NotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, result.Assignment)
}

func (s *Service) getRelease(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	envelope, err := s.FetchRelease(r.Context(), nodeID, r.PathValue("releaseID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope)
}

func (s *Service) postReport(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var report client.NodeReport
	if err := decodeBody(w, r, s.Limits.normalized().MaxBodyBytes, &report); err != nil {
		writeError(w, err)
		return
	}
	accepted, err := s.Report(r.Context(), nodeID, report)
	if err != nil {
		writeError(w, err)
		return
	}
	if accepted {
		w.WriteHeader(http.StatusAccepted)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Service) appendEvents(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var batch client.EventBatch
	if err := decodeBody(w, r, s.Limits.normalized().MaxBodyBytes, &batch); err != nil {
		writeError(w, err)
		return
	}
	acknowledgement, err := s.AppendEvents(r.Context(), nodeID, batch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acknowledgement)
}

func (s *Service) headArtifact(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	metadata, err := s.HeadArtifact(r.Context(), nodeID, r.PathValue("digest"))
	if err != nil {
		writeError(w, err)
		return
	}
	artifactHeaders(w, metadata)
	w.WriteHeader(http.StatusOK)
}

func (s *Service) getArtifact(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.nodeID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	digest := r.PathValue("digest")
	var requested *client.ByteRange
	if raw := r.Header.Get("Range"); raw != "" {
		requested, err = parseByteRange(raw)
		if err != nil {
			metadata, metadataErr := s.HeadArtifact(r.Context(), nodeID, digest)
			if metadataErr != nil {
				writeError(w, metadataErr)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", metadata.Size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	content, err := s.GetArtifact(r.Context(), nodeID, digest, requested, r.Header.Get("If-Range"))
	if err != nil {
		if errors.Is(err, ErrRangeUnsatisfiable) {
			metadata, metadataErr := s.HeadArtifact(r.Context(), nodeID, digest)
			if metadataErr == nil {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", metadata.Size))
			}
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		writeError(w, err)
		return
	}
	defer content.Body.Close()
	w.Header().Set("ETag", content.ETag)
	w.Header().Set("Accept-Ranges", content.AcceptRanges)
	w.Header().Set("Content-Type", "application/octet-stream")
	if content.Partial {
		w.Header().Set("Content-Range", content.ContentRange)
		w.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	_, _ = io.CopyN(w, content.Body, content.Size)
}

func artifactHeaders(w http.ResponseWriter, metadata client.ArtifactMetadata) {
	w.Header().Set("ETag", metadata.ETag)
	w.Header().Set("Accept-Ranges", metadata.AcceptRanges)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	if limit <= 0 {
		limit = 4 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: request body is required", ErrInvalidRequest)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge
		}
		return fmt.Errorf("%w: malformed JSON", ErrInvalidRequest)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: exactly one JSON value is required", ErrInvalidRequest)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := statusFor(err)
	title := http.StatusText(status)
	if title == "" {
		title = "Request failed"
	}
	problem := Problem{Type: "about:blank", Title: title, Status: status, Detail: err.Error()}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBodyTooLarge), errors.Is(err, ErrBatchTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrRangeUnsatisfiable):
		return http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, ErrWaitTooLong), errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrEpochMismatch), errors.Is(err, ErrEventConflict), errors.Is(err, ErrEventGap):
		return http.StatusBadRequest
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 499
	default:
		return http.StatusInternalServerError
	}
}
