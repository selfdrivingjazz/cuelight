package renderer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"sync"

	playerclient "github.com/selfdrivingjazz/cuelight/internal/player/client"
	"github.com/selfdrivingjazz/cuelight/internal/player/screenshot"
)

type BrowserLauncher interface {
	Launch(context.Context, string, string) (BrowserSession, error)
}

type BrowserSession interface {
	Alive(context.Context) (bool, error)
	Stop(context.Context) error
}

// WebLaunch describes a live website window. Unlike bridge launches, the URL
// is an arbitrary http(s) site and the window accepts injected remote input.
type WebLaunch struct {
	URL    string
	Remote *RemoteDescriptor
}

// WebBrowserLauncher is optional: only launchers that can present arbitrary
// websites and inject input (the desktop renderer) support web presentations.
type WebBrowserLauncher interface {
	LaunchWeb(context.Context, WebLaunch) (BrowserSession, error)
}

// BrowserInputSession forwards remote input events into a browser window.
type BrowserInputSession interface {
	SendInput(context.Context, []playerclient.RemoteEvent) error
}

// BrowserRemoteSession refreshes a window's remote QR overlay after the remote
// session was renewed.
type BrowserRemoteSession interface {
	UpdateRemote(context.Context, RemoteDescriptor) error
}

func (s *httpBrowserSession) UpdateRemote(ctx context.Context, remote RemoteDescriptor) error {
	response, err := s.launcher.request(ctx, http.MethodPost, path.Join("/v1/windows", url.PathEscape(s.id), "remote"), map[string]any{"url": remote.URL, "showQr": remote.ShowQR})
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("desktop renderer remote update returned HTTP %d", response.StatusCode)
	}
	return nil
}

// BrowserScreenshotSession is optional so existing lightweight launchers remain
// valid; production Chromium/desktop sessions implement it.
type BrowserScreenshotSession interface {
	Screenshot(context.Context) ([]byte, error)
}

type ExecBrowserLauncher struct {
	BinaryPath string
	ExtraArgs  []string
	Output     io.Writer
}

func (l ExecBrowserLauncher) Launch(_ context.Context, targetURL, profilePath string) (BrowserSession, error) {
	if strings.TrimSpace(l.BinaryPath) == "" {
		return nil, errors.New("Chromium binary is required")
	}
	args := []string{"--kiosk", "--app=" + targetURL, "--no-first-run", "--no-default-browser-check", "--user-data-dir=" + profilePath}
	args = append(args, l.ExtraArgs...)
	args = append(args, targetURL)
	command := exec.Command(l.BinaryPath, args...)
	command.Env = os.Environ()
	command.Stdout = l.Output
	command.Stderr = l.Output
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("launch Chromium: %w", err)
	}
	done := make(chan struct{})
	session := &execBrowserSession{command: command, done: done}
	go func() {
		_ = command.Wait()
		close(done)
	}()
	return session, nil
}

type execBrowserSession struct {
	command *exec.Cmd
	done    <-chan struct{}
	once    sync.Once
}

func (s *execBrowserSession) Alive(context.Context) (bool, error) {
	select {
	case <-s.done:
		return false, nil
	default:
		return true, nil
	}
}

func (s *execBrowserSession) Stop(context.Context) error {
	var result error
	s.once.Do(func() {
		if s.command != nil && s.command.Process != nil {
			result = s.command.Process.Kill()
		}
	})
	return result
}

type HTTPBrowserLauncher struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func NewHTTPBrowserLauncher(baseURL, token string, client *http.Client) (*HTTPBrowserLauncher, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("desktop renderer URL must be an absolute loopback HTTP URL")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("desktop renderer URL must use a loopback IP address")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("desktop renderer token is required")
	}
	if client == nil {
		client = &http.Client{Transport: http.DefaultTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &HTTPBrowserLauncher{BaseURL: parsed.String(), Token: token, Client: client}, nil
}

func (l *HTTPBrowserLauncher) Launch(ctx context.Context, targetURL, _ string) (BrowserSession, error) {
	return l.createWindow(ctx, map[string]string{"url": targetURL})
}

func (l *HTTPBrowserLauncher) LaunchWeb(ctx context.Context, launch WebLaunch) (BrowserSession, error) {
	type remote struct {
		URL    string `json:"url"`
		ShowQR bool   `json:"showQr"`
	}
	body := struct {
		URL    string  `json:"url"`
		Kind   string  `json:"kind"`
		Remote *remote `json:"remote,omitempty"`
	}{URL: launch.URL, Kind: "web"}
	if launch.Remote != nil && launch.Remote.URL != "" {
		body.Remote = &remote{URL: launch.Remote.URL, ShowQR: launch.Remote.ShowQR}
	}
	return l.createWindow(ctx, body)
}

func (l *HTTPBrowserLauncher) createWindow(ctx context.Context, body any) (BrowserSession, error) {
	response, err := l.request(ctx, http.MethodPost, "/v1/windows", body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("desktop renderer create returned HTTP %d", response.StatusCode)
	}
	var value struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&value); err != nil || strings.TrimSpace(value.ID) == "" {
		return nil, errors.New("desktop renderer returned an invalid window identity")
	}
	return &httpBrowserSession{launcher: l, id: value.ID}, nil
}

func (l *HTTPBrowserLauncher) request(ctx context.Context, method, endpoint string, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, l.BaseURL+endpoint, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+l.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return l.Client.Do(request)
}

type httpBrowserSession struct {
	launcher *HTTPBrowserLauncher
	id       string
	once     sync.Once
}

func (s *httpBrowserSession) Alive(ctx context.Context) (bool, error) {
	response, err := s.launcher.request(ctx, http.MethodGet, path.Join("/v1/windows", url.PathEscape(s.id)), nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("desktop renderer health returned HTTP %d", response.StatusCode)
	}
	var value struct {
		Alive bool `json:"alive"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&value); err != nil {
		return false, err
	}
	return value.Alive, nil
}

func (s *httpBrowserSession) Screenshot(ctx context.Context) ([]byte, error) {
	response, err := s.launcher.request(ctx, http.MethodGet, path.Join("/v1/windows", url.PathEscape(s.id), "screenshot"), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, screenshot.ErrUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("desktop renderer screenshot returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, screenshot.DefaultMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > screenshot.DefaultMaxBytes {
		return nil, screenshot.ErrCaptureTooLarge
	}
	return data, nil
}

// maxInputBatch matches the desktop renderer's per-request event limit.
const maxInputBatch = 256

func (s *httpBrowserSession) SendInput(ctx context.Context, events []playerclient.RemoteEvent) error {
	for len(events) > 0 {
		batch := events[:min(len(events), maxInputBatch)]
		events = events[len(batch):]
		response, err := s.launcher.request(ctx, http.MethodPost, path.Join("/v1/windows", url.PathEscape(s.id), "input"), map[string]any{"events": batch})
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("desktop renderer input returned HTTP %d", response.StatusCode)
		}
	}
	return nil
}

func (s *httpBrowserSession) Stop(ctx context.Context) error {
	var result error
	s.once.Do(func() {
		response, err := s.launcher.request(ctx, http.MethodDelete, path.Join("/v1/windows", url.PathEscape(s.id)), nil)
		if err != nil {
			result = err
			return
		}
		defer response.Body.Close()
		if !slices.Contains([]int{http.StatusNoContent, http.StatusNotFound}, response.StatusCode) {
			result = fmt.Errorf("desktop renderer stop returned HTTP %d", response.StatusCode)
		}
	})
	return result
}
