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
)

type BrowserLauncher interface {
	Launch(context.Context, string, string) (BrowserSession, error)
}

type BrowserSession interface {
	Alive(context.Context) (bool, error)
	Stop(context.Context) error
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
	response, err := l.request(ctx, http.MethodPost, "/v1/windows", map[string]string{"url": targetURL})
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
