package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newAPIServer serves handler at /api/v1, where a bare server origin points.
func newAPIServer(handler http.Handler) *httptest.Server {
	return httptest.NewServer(http.StripPrefix("/api/v1", handler))
}

func TestRequestMappingAndProblemDecoding(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.EscapedPath() != "/admin-tokens/token%2Fid/revoke" {
			http.Error(response, "wrong request", http.StatusInternalServerError)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer operator-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "0123456789abcdef" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		response.Header().Set("Content-Type", "application/problem+json")
		response.WriteHeader(http.StatusConflict)
		fmt.Fprint(response, `{"type":"https://cuelight.test/problems/revoked","title":"Already revoked","status":409,"detail":"token cannot be changed","correlationId":"corr-1","retryable":false}`)
	}))
	defer server.Close()

	api, err := New(server.URL, "operator-secret", server.Client(), AllowInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	err = api.RevokeToken(context.Background(), "token/id", "0123456789abcdef")
	var problem *Problem
	if !errors.As(err, &problem) {
		t.Fatalf("error = %T %v, want *Problem", err, err)
	}
	if problem.Status != http.StatusConflict || problem.CorrelationID != "corr-1" || problem.Extensions["retryable"] != false {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestClientPreservesServerPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/nodes" {
			t.Errorf("path = %q, want /api/v1/nodes", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		fmt.Fprint(response, `{"items":[]}`)
	}))
	defer server.Close()

	api, err := New(server.URL+"/api/v1", "operator-secret", server.Client(), AllowInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ListNodes(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientRequiresHTTPSUnlessLoopbackIsExplicit(t *testing.T) {
	if _, err := New("http://127.0.0.1:8080", "operator-secret", nil); err == nil {
		t.Fatal("plain HTTP loopback was accepted without explicit option")
	}
	if _, err := New("http://control.example", "operator-secret", nil, AllowInsecureLoopback()); err == nil {
		t.Fatal("plain HTTP non-loopback was accepted with loopback option")
	}
	if _, err := New("http://localhost:8080", "operator-secret", nil, AllowInsecureLoopback()); err != nil {
		t.Fatalf("explicit HTTP loopback rejected: %v", err)
	}
}

func TestClientBlocksHTTPSDowngradeRedirect(t *testing.T) {
	var downgradeRequests atomic.Int32
	plainServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		downgradeRequests.Add(1)
		response.Header().Set("Content-Type", "application/json")
		fmt.Fprint(response, `{"items":[]}`)
	}))
	defer plainServer.Close()
	secureServer := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, plainServer.URL+"/nodes", http.StatusFound)
	}))
	defer secureServer.Close()

	api, err := New(secureServer.URL, "operator-secret", secureServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ListNodes(context.Background()); err == nil {
		t.Fatal("HTTPS downgrade redirect was accepted")
	}
	if got := downgradeRequests.Load(); got != 0 {
		t.Fatalf("bearer request reached plain HTTP server %d times", got)
	}
}

func TestUploadAssetStreamsContentAcrossWorkflow(t *testing.T) {
	const content = "a streamed media payload"
	requests := 0
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/uploads" {
				t.Fatalf("create request = %s %s", request.Method, request.URL.Path)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			fmt.Fprintf(response, `{"id":"upload-1","length":%d,"offset":0,"state":"open"}`, len(content))
		case 2:
			if request.Method != http.MethodPatch || request.URL.Path != "/uploads/upload-1" {
				t.Fatalf("append request = %s %s", request.Method, request.URL.Path)
			}
			if request.Header.Get("Content-Type") != "application/offset+octet-stream" || request.Header.Get("Upload-Offset") != "0" {
				t.Fatalf("append headers = %#v", request.Header)
			}
			if request.ContentLength != int64(len(content)) {
				t.Fatalf("Content-Length = %d", request.ContentLength)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != content {
				t.Fatalf("body = %q", body)
			}
			response.Header().Set("Upload-Offset", fmt.Sprint(len(content)))
			response.WriteHeader(http.StatusNoContent)
		case 3:
			if request.Method != http.MethodPost || request.URL.Path != "/uploads/upload-1/complete" {
				t.Fatalf("complete request = %s %s", request.Method, request.URL.Path)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			fmt.Fprint(response, `{"id":"asset-1","name":"clip.mp4","artifact":{"digest":"sha256:abc","size":24,"mediaType":"video/mp4"}}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	api, err := New(server.URL, "operator-secret", server.Client(), AllowInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := api.UploadAsset(context.Background(), "clip.mp4", "video/mp4", int64(len(content)), strings.NewReader(content), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || asset.ID != "asset-1" || asset.Artifact.Digest != "sha256:abc" {
		t.Fatalf("requests = %d, asset = %#v", requests, asset)
	}
}
