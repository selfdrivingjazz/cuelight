// Package update installs vendor-signed Cuelight software releases on player
// nodes. Control only chooses a target version; the node fetches the signed
// release envelope, verifies it against the release keys compiled into this
// binary, and installs nothing a verified manifest does not name.
//
// Desktop nodes stage an electron-updater feed and hand it to the desktop app.
// Appliance nodes stage the player bundle and ask a root updater unit to
// apply it (see Applier).
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	playerruntime "github.com/jmilldotdev/cuelight/internal/player/runtime"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

// Update methods a player can report.
const (
	MethodNone      = "none"
	MethodDesktop   = "desktop"
	MethodAppliance = "appliance"
)

// Node event types reported to control.
const (
	EventFailed  = "software.update.failed"
	EventIgnored = "software.update.ignored"
	EventStaged  = "software.update.staged"
)

const (
	maxEnvelopeBytes = 1 << 20
	diskReserve      = 32 << 20
)

// Config configures a Manager.
type Config struct {
	Method         string
	RunningVersion string
	OS, Arch       string
	// Dir is the staging root; each version stages into Dir/<version>.
	Dir string
	// HTTP fetches control URLs (mTLS for players).
	HTTP *http.Client
	// ControlURL resolves origin-relative target URLs.
	ControlURL string
	Trusted    map[string]ed25519.PublicKey
	// Handoff passes a staged desktop feed to the desktop app.
	Handoff func(ctx context.Context, version, feedDir string) error
	// FreeBytes reports free space at a path; nil skips the disk check.
	FreeBytes    func(path string) (int64, error)
	RetryMinimum time.Duration
	RetryMaximum time.Duration
	// ResultPoll is how often an appliance looks for an updater result.
	ResultPoll time.Duration
	Logf       func(format string, args ...any)
}

// Manager stages at most one software update at a time. Offer records the
// newest target; Run processes it, retrying transient failures with backoff.
type Manager struct {
	config Config
	wake   chan struct{}

	mu     sync.Mutex
	wanted playerclient.SoftwareTarget
	events []playerruntime.ExternalEvent
}

// resolve makes a target URL absolute against the control URL; the result
// must be HTTPS.
func (m *Manager) resolve(raw string) (string, error) {
	reference, err := url.Parse(raw)
	if err != nil || raw == "" {
		return "", errors.New("software target URL is invalid")
	}
	if !reference.IsAbs() {
		base, err := url.Parse(m.config.ControlURL)
		if err != nil || m.config.ControlURL == "" {
			return "", errors.New("software target URL is relative and no control URL is configured")
		}
		reference = base.ResolveReference(reference)
	}
	if reference.Scheme != "https" || reference.Host == "" {
		return "", errors.New("software target URLs must be HTTPS")
	}
	return reference.String(), nil
}

// permanentError marks a failure that retrying the same target cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(format string, args ...any) error {
	return &permanentError{err: fmt.Errorf(format, args...)}
}

// errAlreadyFailed reports a target whose apply already failed and was reported.
var errAlreadyFailed = errors.New("software update previously failed")

// NewManager validates config.
func NewManager(config Config) (*Manager, error) {
	if config.Method != MethodDesktop && config.Method != MethodAppliance {
		return nil, fmt.Errorf("unsupported update method %q", config.Method)
	}
	if _, err := updates.ParseVersion(config.RunningVersion); err != nil {
		return nil, fmt.Errorf("running version: %w", err)
	}
	if config.Dir == "" || config.HTTP == nil || len(config.Trusted) == 0 || config.OS == "" || config.Arch == "" {
		return nil, errors.New("update staging directory, HTTP client, trusted keys, and platform are required")
	}
	if config.Method == MethodDesktop && config.Handoff == nil {
		return nil, errors.New("desktop updates need a handoff")
	}
	if config.RetryMinimum <= 0 {
		config.RetryMinimum = 5 * time.Second
	}
	if config.RetryMaximum < config.RetryMinimum {
		config.RetryMaximum = max(10*time.Minute, config.RetryMinimum)
	}
	if config.ResultPoll <= 0 {
		config.ResultPoll = 30 * time.Second
	}
	if config.Logf == nil {
		config.Logf = func(string, ...any) {}
	}
	return &Manager{config: config, wake: make(chan struct{}, 1)}, nil
}

// Offer records the target control currently wants; nil leaves the last one.
func (m *Manager) Offer(target *playerclient.SoftwareTarget) {
	if target == nil {
		return
	}
	m.mu.Lock()
	m.wanted = *target
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// DrainEvents returns and clears pending node events.
func (m *Manager) DrainEvents() []playerruntime.ExternalEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.events
	m.events = nil
	return events
}

func (m *Manager) emit(kind string, data map[string]any) {
	m.mu.Lock()
	m.events = append(m.events, playerruntime.ExternalEvent{Type: kind, Data: data})
	m.mu.Unlock()
}

func (m *Manager) target() playerclient.SoftwareTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wanted
}

// Run processes offered targets until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	var resultTicks <-chan time.Time
	if m.config.Method == MethodAppliance {
		ticker := time.NewTicker(m.config.ResultPoll)
		defer ticker.Stop()
		resultTicks = ticker.C
		m.collectApplyResult()
	}
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	var handled, attempting, lastFailure string
	retryPending := false
	backoff := m.config.RetryMinimum
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.wake:
		case <-retry.C:
			retryPending = false
		case <-resultTicks:
			m.collectApplyResult()
			continue
		}
		target := m.target()
		if target.Version == "" || target.Version == handled {
			continue
		}
		if target.Version != attempting {
			attempting, lastFailure, backoff = target.Version, "", m.config.RetryMinimum
			retry.Stop()
			retryPending = false
		} else if retryPending {
			continue
		}
		err := m.process(ctx, target)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var permanentErr *permanentError
		switch {
		case err == nil || errors.Is(err, errAlreadyFailed):
			handled = target.Version
		case errors.As(err, &permanentErr):
			handled = target.Version
			m.config.Logf("software update %s refused: %v", target.Version, err)
			m.emit(EventFailed, map[string]any{"version": target.Version, "error": err.Error()})
		default:
			m.config.Logf("software update %s failed, retrying in %s: %v", target.Version, backoff, err)
			if err.Error() != lastFailure {
				lastFailure = err.Error()
				m.emit(EventFailed, map[string]any{"version": target.Version, "error": err.Error()})
			}
			retry.Reset(backoff)
			retryPending = true
			backoff = min(backoff*2, m.config.RetryMaximum)
		}
	}
}

func (m *Manager) process(ctx context.Context, target playerclient.SoftwareTarget) error {
	if _, err := updates.ParseVersion(target.Version); err != nil {
		return permanent("target version: %w", err)
	}
	manifestURL, err := m.resolve(target.ManifestURL)
	if err != nil {
		return &permanentError{err: err}
	}
	artifactsURL, err := m.resolve(target.ArtifactsBaseURL)
	if err != nil {
		return &permanentError{err: err}
	}
	target.ManifestURL, target.ArtifactsBaseURL = manifestURL, artifactsURL
	comparison, err := updates.CompareVersions(target.Version, m.config.RunningVersion)
	if err != nil {
		return permanent("compare versions: %w", err)
	}
	if comparison <= 0 {
		if comparison < 0 {
			m.config.Logf("ignoring software target %s: not newer than running %s", target.Version, m.config.RunningVersion)
			m.emit(EventIgnored, map[string]any{"version": target.Version, "running": m.config.RunningVersion})
		}
		return nil
	}
	if m.config.Method == MethodAppliance {
		if _, err := os.Stat(failedRecordPath(m.config.Dir, target.Version)); err == nil {
			return errAlreadyFailed
		}
	}
	document, err := m.fetchEnvelope(ctx, target.ManifestURL)
	if err != nil {
		return err
	}
	envelope, err := updates.ParseEnvelope(document)
	if err != nil {
		return permanent("verify release manifest: %w", err)
	}
	manifest, err := envelope.Verify(m.config.Trusted)
	if err != nil {
		return permanent("verify release manifest: %w", err)
	}
	if manifest.Version != target.Version {
		return permanent("signed manifest is version %s, not target %s", manifest.Version, target.Version)
	}
	dir := filepath.Join(m.config.Dir, target.Version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	switch m.config.Method {
	case MethodDesktop:
		if err := m.stageDesktop(ctx, target, manifest, dir); err != nil {
			return err
		}
	case MethodAppliance:
		if err := m.stageAppliance(ctx, target, manifest, document, dir); err != nil {
			return err
		}
	}
	m.config.Logf("software update %s staged in %s", target.Version, dir)
	m.emit(EventStaged, map[string]any{"version": target.Version})
	m.pruneStaging(target.Version)
	return nil
}

func (m *Manager) stageDesktop(ctx context.Context, target playerclient.SoftwareTarget, manifest updates.Manifest, dir string) error {
	feed, err := FeedArtifact(manifest, m.config.OS, m.config.Arch)
	if err != nil {
		return &permanentError{err: err}
	}
	if err := m.fetchAll(ctx, target.ArtifactsBaseURL, dir, []updates.Artifact{feed}); err != nil {
		return err
	}
	document, err := os.ReadFile(filepath.Join(dir, feed.Name))
	if err != nil {
		return err
	}
	files, err := FeedFiles(manifest, document)
	if err != nil {
		return &permanentError{err: err}
	}
	if err := m.fetchAll(ctx, target.ArtifactsBaseURL, dir, files); err != nil {
		return err
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := m.config.Handoff(ctx, target.Version, absolute); err != nil {
		return fmt.Errorf("hand update to desktop app: %w", err)
	}
	return nil
}

func (m *Manager) stageAppliance(ctx context.Context, target playerclient.SoftwareTarget, manifest updates.Manifest, envelope []byte, dir string) error {
	artifact, ok := manifest.Find(updates.ComponentPlayer, "linux", m.config.Arch, "tar.gz")
	if !ok {
		return permanent("release %s has no player bundle for linux/%s", manifest.Version, m.config.Arch)
	}
	if err := m.fetchAll(ctx, target.ArtifactsBaseURL, dir, []updates.Artifact{artifact}); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dir, updates.ManifestFileName), envelope, 0o644); err != nil {
		return err
	}
	request, err := json.Marshal(Request{Version: target.Version})
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.config.Dir, RequestFileName), append(request, '\n'), 0o644)
}

func (m *Manager) fetchAll(ctx context.Context, baseURL, dir string, artifacts []updates.Artifact) error {
	var needed int64
	for _, artifact := range artifacts {
		missing, err := missingBytes(filepath.Join(dir, artifact.Name), artifact)
		if err != nil {
			return err
		}
		needed += missing
	}
	if needed > 0 && m.config.FreeBytes != nil {
		free, err := m.config.FreeBytes(dir)
		if err == nil && free < needed+diskReserve {
			return fmt.Errorf("insufficient disk space: need %d bytes, %d free", needed+diskReserve, free)
		}
	}
	for _, artifact := range artifacts {
		if err := download(ctx, m.config.HTTP, artifactURL(baseURL, artifact.SHA256), artifact, filepath.Join(dir, artifact.Name)); err != nil {
			return fmt.Errorf("download %s: %w", artifact.Name, err)
		}
	}
	return nil
}

func (m *Manager) fetchEnvelope(ctx context.Context, manifestURL string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, &permanentError{err: err}
	}
	request.Header.Set("Accept", "application/json")
	response, err := m.config.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch release manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch release manifest: HTTP %d", response.StatusCode)
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, maxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch release manifest: %w", err)
	}
	if len(document) > maxEnvelopeBytes {
		return nil, permanent("release manifest exceeds %d bytes", maxEnvelopeBytes)
	}
	return document, nil
}

// pruneStaging removes staged versions other than keep.
func (m *Manager) pruneStaging(keep string) {
	entries, err := os.ReadDir(m.config.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep {
			continue
		}
		if _, err := updates.ParseVersion(entry.Name()); err == nil {
			_ = os.RemoveAll(filepath.Join(m.config.Dir, entry.Name()))
		}
	}
}

// collectApplyResult reports the root updater's last result once. A failed
// version is remembered so the node does not request it again.
func (m *Manager) collectApplyResult() {
	path := filepath.Join(m.config.Dir, ResultFileName)
	document, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var result Result
	if err := json.Unmarshal(document, &result); err != nil || result.Version == "" {
		_ = os.Remove(path)
		return
	}
	if result.Status != StatusFailed {
		_ = os.Remove(path)
		return
	}
	if strings.ContainsAny(result.Version, `/\`) || os.Rename(path, failedRecordPath(m.config.Dir, result.Version)) != nil {
		_ = os.Remove(path)
	}
	m.config.Logf("software update %s failed to apply: %s", result.Version, result.Error)
	m.emit(EventFailed, map[string]any{"version": result.Version, "error": result.Error})
}

func failedRecordPath(dir, version string) string {
	return filepath.Join(dir, "failed-"+version+".json")
}

func artifactURL(base, digest string) string {
	return strings.TrimRight(base, "/") + "/" + digest
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
