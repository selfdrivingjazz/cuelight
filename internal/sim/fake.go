package sim

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/jmill/cable/internal/player/renderer"
	"github.com/jmill/cable/internal/player/runtime"
	"github.com/jmill/cable/internal/release"
)

// FakeOutput models the physical output and disk budget independently, so
// scenarios can toggle readiness/capacity without replacing runtime wiring.
type FakeOutput struct {
	mu       sync.RWMutex
	ready    bool
	capacity int64
}

func NewFakeOutput(ready bool, capacity int64) *FakeOutput {
	return &FakeOutput{ready: ready, capacity: capacity}
}
func (o *FakeOutput) SetReady(ready bool)     { o.mu.Lock(); o.ready = ready; o.mu.Unlock() }
func (o *FakeOutput) Ready() bool             { o.mu.RLock(); defer o.mu.RUnlock(); return o.ready }
func (o *FakeOutput) SetCapacity(bytes int64) { o.mu.Lock(); o.capacity = bytes; o.mu.Unlock() }
func (o *FakeOutput) AvailableBytes(context.Context) (int64, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.capacity, nil
}

// RendererConfig injects failures at exact lifecycle boundaries.
type RendererConfig struct{ FailPrepare, FailStart, FailHealth bool }

type FakeRenderer struct {
	output                                         *FakeOutput
	config                                         RendererConfig
	mu                                             sync.Mutex
	current                                        renderer.Candidate
	running                                        bool
	prepares, starts, healthChecks, commits, stops int
}

func NewFakeRenderer(output *FakeOutput, config RendererConfig) *FakeRenderer {
	return &FakeRenderer{output: output, config: config}
}
func (r *FakeRenderer) Prepare(_ context.Context, c renderer.Candidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prepares++
	if r.config.FailPrepare {
		return errors.New("fake renderer prepare failure")
	}
	return validateCandidate(c)
}
func (r *FakeRenderer) Start(_ context.Context, c renderer.Candidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.config.FailStart {
		return errors.New("fake renderer start failure")
	}
	if r.output != nil && !r.output.Ready() {
		return errors.New("fake output is not ready")
	}
	if err := validateCandidate(c); err != nil {
		return err
	}
	r.current, r.running = c, true
	return nil
}
func (r *FakeRenderer) Health(_ context.Context, c renderer.Candidate) (renderer.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthChecks++
	if r.config.FailHealth {
		return renderer.Status{}, errors.New("fake renderer probation failure")
	}
	healthy := r.running && reflect.DeepEqual(r.current, c) && (r.output == nil || r.output.Ready())
	return renderer.Status{ReleaseDigest: c.ReleaseDigest, Healthy: healthy, Path: c.Source, Candidate: c}, nil
}
func (r *FakeRenderer) Commit(_ context.Context, c renderer.Candidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits++
	if !r.running || !reflect.DeepEqual(r.current, c) {
		return errors.New("fake candidate is not current")
	}
	return nil
}
func (r *FakeRenderer) Stop(_ context.Context) error {
	r.mu.Lock()
	r.stops++
	r.running = false
	r.current = renderer.Candidate{}
	r.mu.Unlock()
	return nil
}
func (r *FakeRenderer) Current(ctx context.Context) (renderer.Status, error) {
	r.mu.Lock()
	c, running := r.current, r.running
	r.mu.Unlock()
	if !running {
		return renderer.Status{}, nil
	}
	return r.Health(ctx, c)
}
func (r *FakeRenderer) Counts() (prepare, start, health, commit, stop int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prepares, r.starts, r.healthChecks, r.commits, r.stops
}
func (r *FakeRenderer) SetFailures(config RendererConfig) {
	r.mu.Lock()
	r.config = config
	r.mu.Unlock()
}
func validateCandidate(candidate renderer.Candidate) error {
	if candidate.ReleaseDigest == "" {
		return errors.New("fake candidate release digest is required")
	}
	if candidate.Kind == release.PresentationMediaSchedule {
		if len(candidate.Schedule) == 0 {
			return errors.New("fake schedule candidate items are required")
		}
		for _, item := range candidate.Schedule {
			if item.Source == "" && item.WebURL == "" {
				return errors.New("fake schedule item needs a source or web URL")
			}
		}
		return nil
	}
	if candidate.Kind == release.PresentationWeb {
		if candidate.WebURL == "" {
			return errors.New("fake web candidate URL is required")
		}
		return nil
	}
	if candidate.Source == "" {
		return errors.New("fake media candidate source is required")
	}
	if candidate.Fit != "fit" && candidate.Fit != "fill" {
		return errors.New("fake media candidate fit is invalid")
	}
	return nil
}

type loopbackStreamer struct {
	proxy    *renderer.Proxy
	server   *http.Server
	listener net.Listener
	base     string
}

func newLoopbackStreamer(controlURL string, caPEM []byte, certPEM, keyPEM string) (*loopbackStreamer, error) {
	httpClient, err := tlsHTTPClient(caPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	proxy, err := renderer.NewProxy(controlURL, httpClient)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 2 * time.Second}
	stream := &loopbackStreamer{proxy: proxy, server: server, listener: listener, base: "http://" + listener.Addr().String()}
	go func() { _ = server.Serve(listener) }()
	return stream, nil
}
func (s *loopbackStreamer) Open(_ context.Context, digest string, ttl time.Duration) (string, func(), error) {
	path, err := s.proxy.Grant(digest, ttl)
	if err != nil {
		return "", nil, err
	}
	return s.base + path, func() { s.proxy.Revoke(path) }, nil
}
func (s *loopbackStreamer) Renew(_ context.Context, source string, ttl time.Duration) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return err
	}
	return s.proxy.Renew(parsed.Path, ttl)
}
func (s *loopbackStreamer) Close() {
	if s.server != nil {
		_ = s.server.Close()
	}
}

var _ runtime.StreamSource = (*loopbackStreamer)(nil)
