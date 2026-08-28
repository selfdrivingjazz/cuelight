package renderer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmill/cable/internal/release"
)

func TestHTTPBrowserLauncherLifecycle(t *testing.T) {
	alive := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer renderer-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/windows":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "window-one"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/windows/window-one" && alive:
			_ = json.NewEncoder(w).Encode(map[string]bool{"alive": true})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/windows/window-one" && alive:
			alive = false
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	launcher, err := NewHTTPBrowserLauncher(server.URL, "renderer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	session, err := launcher.Launch(context.Background(), "http://127.0.0.1:9999/player", "")
	if err != nil {
		t.Fatal(err)
	}
	if running, err := session.Alive(context.Background()); err != nil || !running {
		t.Fatalf("alive = %v, %v", running, err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if running, err := session.Alive(context.Background()); err != nil || running {
		t.Fatalf("alive after stop = %v, %v", running, err)
	}
}

func TestChromiumMediaCandidateUsesBrowserLauncherAndSignalsReady(t *testing.T) {
	mediaPath := t.TempDir() + "/image.png"
	if err := os.WriteFile(mediaPath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &capturingBrowserLauncher{target: make(chan string, 1), session: &capturingBrowserSession{alive: true}}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Kind: release.PresentationMedia, ReleaseDigest: strings.Repeat("a", 64), Source: mediaPath, MediaType: "image/png", Fit: "fill", Volume: 64}
	if err := adapter.Prepare(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() { startDone <- adapter.Start(context.Background(), candidate) }()
	var target string
	select {
	case target = <-launcher.target:
	case <-time.After(time.Second):
		t.Fatal("browser target was not launched")
	}
	if parsed, err := url.Parse(target); err != nil || parsed.Path != "/" {
		t.Fatalf("browser target = %q, want bridge root", target)
	}
	response, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 16<<10)
	count, _ := response.Body.Read(body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body[:count]), "/bridge/v1/media/current") {
		t.Fatalf("media page status=%d body=%s", response.StatusCode, body[:count])
	}
	readyRequest, _ := http.NewRequest(http.MethodPost, strings.TrimSuffix(target, "/index.html")+"/bridge/v1/ready", nil)
	readyResponse, err := http.DefaultClient.Do(readyRequest)
	if err != nil {
		t.Fatal(err)
	}
	readyResponse.Body.Close()
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Health(context.Background(), candidate)
	if err != nil || !status.Healthy {
		t.Fatalf("media health = %#v, %v", status, err)
	}
	if err := adapter.Commit(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Stop(context.Background()); err != nil || !launcher.session.stopped {
		t.Fatalf("stop = %v, session=%#v", err, launcher.session)
	}
}

type capturingBrowserLauncher struct {
	target  chan string
	session *capturingBrowserSession
}

func (l *capturingBrowserLauncher) Launch(_ context.Context, target, _ string) (BrowserSession, error) {
	l.target <- target
	return l.session, nil
}

type capturingBrowserSession struct {
	mu      sync.Mutex
	alive   bool
	stopped bool
}

func (s *capturingBrowserSession) Alive(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive, nil
}
func (s *capturingBrowserSession) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive, s.stopped = false, true
	return nil
}
