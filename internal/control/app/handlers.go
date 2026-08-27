package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

var ErrNotReady = errors.New("control plane is not ready")

type Probe func(context.Context) error

type Readiness struct {
	mutex  sync.RWMutex
	ready  bool
	reason error
	probes []Probe
}

func NewReadiness(probes ...Probe) *Readiness {
	return &Readiness{reason: ErrNotReady, probes: append([]Probe(nil), probes...)}
}

func (readiness *Readiness) SetReady() {
	readiness.mutex.Lock()
	readiness.ready = true
	readiness.reason = nil
	readiness.mutex.Unlock()
}

func (readiness *Readiness) SetNotReady(reason error) {
	if reason == nil {
		reason = ErrNotReady
	}
	readiness.mutex.Lock()
	readiness.ready = false
	readiness.reason = reason
	readiness.mutex.Unlock()
}

func (readiness *Readiness) Check(ctx context.Context) error {
	readiness.mutex.RLock()
	ready := readiness.ready
	reason := readiness.reason
	probes := append([]Probe(nil), readiness.probes...)
	readiness.mutex.RUnlock()
	if !ready {
		return errors.Join(ErrNotReady, reason)
	}
	for _, probe := range probes {
		if err := probe(ctx); err != nil {
			return fmt.Errorf("%w: dependency probe: %v", ErrNotReady, err)
		}
	}
	return nil
}

func NewHandler(version string, readiness *Readiness, product http.Handler) http.Handler {
	if readiness == nil {
		readiness = NewReadiness()
	}
	if product == nil {
		product = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writeStatus(writer, http.StatusOK, "ok", version)
	})
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, request *http.Request) {
		if err := readiness.Check(request.Context()); err != nil {
			writeStatus(writer, http.StatusServiceUnavailable, "unavailable", version)
			return
		}
		writeStatus(writer, http.StatusOK, "ready", version)
	})
	mux.HandleFunc("GET /version", func(writer http.ResponseWriter, _ *http.Request) {
		writeStatus(writer, http.StatusOK, "ok", version)
	})
	mux.Handle("/", product)
	return mux
}

type statusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func writeStatus(writer http.ResponseWriter, code int, status, version string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(code)
	_ = json.NewEncoder(writer).Encode(statusResponse{Status: status, Version: version})
}
