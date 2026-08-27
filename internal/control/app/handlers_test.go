package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessHandlerTransitionsAndFailsOnProbe(t *testing.T) {
	probeErr := error(nil)
	readiness := NewReadiness(func(_ context.Context) error { return probeErr })
	handler := NewHandler("test-version", readiness, nil)

	assertStatusCode(t, handler, "/ready", http.StatusServiceUnavailable)
	readiness.SetReady()
	assertStatusCode(t, handler, "/ready", http.StatusOK)
	probeErr = errors.New("database unavailable")
	assertStatusCode(t, handler, "/ready", http.StatusServiceUnavailable)
	probeErr = nil
	readiness.SetNotReady(errors.New("shutting down"))
	assertStatusCode(t, handler, "/ready", http.StatusServiceUnavailable)
}

func TestHealthVersionAndInjectedHandler(t *testing.T) {
	readiness := NewReadiness()
	product := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/example" {
			t.Fatalf("unexpected product path %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := NewHandler("v1.2.3", readiness, product)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\",\"version\":\"v1.2.3\"}\n" {
		t.Fatalf("unexpected health response: %d %q", response.Code, response.Body.String())
	}
	assertStatusCode(t, handler, "/version", http.StatusOK)
	assertStatusCode(t, handler, "/api/v1/example", http.StatusNoContent)
}

func assertStatusCode(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != want {
		t.Fatalf("GET %s status = %d, want %d; body = %q", path, response.Code, want, response.Body.String())
	}
}
