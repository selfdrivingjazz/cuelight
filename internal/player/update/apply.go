package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/updates"
)

// Appliance updater files.
const (
	RequestFileName = "request.json"
	ResultFileName  = "result.json"
	HealthFileName  = "health.json"

	StatusApplied = "applied"
	StatusFailed  = "failed"
)

// Layout locates an appliance install. Tests point it at a temp root.
type Layout struct {
	// OptDir holds versions/<v>/ and the current -> versions/<v> symlink.
	OptDir string
	// StateDir is the player's writable state (updates/, health.json).
	StateDir string
}

// DefaultLayout is the installed appliance layout.
func DefaultLayout() Layout {
	return Layout{OptDir: "/opt/cuelight", StateDir: "/var/lib/cuelight"}
}

func (l Layout) UpdatesDir() string  { return filepath.Join(l.StateDir, "updates") }
func (l Layout) HealthPath() string  { return filepath.Join(l.StateDir, HealthFileName) }
func (l Layout) VersionsDir() string { return filepath.Join(l.OptDir, "versions") }
func (l Layout) CurrentPath() string { return filepath.Join(l.OptDir, "current") }

// Request asks the root updater to apply a staged version.
type Request struct {
	Version string `json:"version"`
}

// Result is the root updater's outcome, reported by the player to control.
type Result struct {
	Version      string    `json:"version"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	RolledBackTo string    `json:"rolledBackTo,omitempty"`
	At           time.Time `json:"at"`
}

// Health is written by a player after a successful control round-trip.
type Health struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// WriteHealth atomically records that version reached control at at.
func WriteHealth(path, version string, at time.Time) error {
	document, err := json.Marshal(Health{Version: version, At: at.UTC()})
	if err != nil {
		return err
	}
	return atomicWrite(path, append(document, '\n'), 0o644)
}

// ErrNoRequest means there is nothing to apply.
var ErrNoRequest = errors.New("no software update request")

// Applier is the root side of appliance updates (`cuelight-player update
// apply`). It trusts nothing the unprivileged player staged: it re-verifies
// the envelope against compiled keys and the bundle against the manifest.
type Applier struct {
	Layout         Layout
	Trusted        map[string]ed25519.PublicKey
	RunningVersion string
	Arch           string
	// Units are restarted after switching versions.
	Units     []string
	Systemctl func(ctx context.Context, args ...string) error
	Now       func() time.Time
	Sleep     func(ctx context.Context, duration time.Duration) error
	// HealthTimeout bounds the wait for the new player's health marker.
	HealthTimeout time.Duration
	HealthPoll    time.Duration
	Logf          func(format string, args ...any)
}

// Apply consumes the pending request, installs it, and records the result.
// It returns ErrNoRequest when no request is pending.
func (a Applier) Apply(ctx context.Context) (Result, error) {
	if a.Logf == nil {
		a.Logf = func(string, ...any) {}
	}
	requestPath := filepath.Join(a.Layout.UpdatesDir(), RequestFileName)
	document, err := os.ReadFile(requestPath)
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, ErrNoRequest
	}
	if err != nil {
		return Result{}, err
	}
	// Consume first so a failing request cannot retrigger the path unit.
	if err := os.Remove(requestPath); err != nil {
		return Result{}, err
	}
	var request Request
	if err := json.Unmarshal(document, &request); err != nil {
		return Result{}, fmt.Errorf("decode update request: %w", err)
	}
	result := a.apply(ctx, request.Version)
	result.At = a.Now().UTC()
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if err := atomicWrite(filepath.Join(a.Layout.UpdatesDir(), ResultFileName), append(encoded, '\n'), 0o644); err != nil {
		return result, fmt.Errorf("record update result: %w", err)
	}
	if result.Status != StatusApplied {
		return result, errors.New(result.Error)
	}
	return result, nil
}

func (a Applier) apply(ctx context.Context, version string) Result {
	failed := func(format string, args ...any) Result {
		message := fmt.Sprintf(format, args...)
		a.Logf("update %s failed: %s", version, message)
		return Result{Version: version, Status: StatusFailed, Error: message}
	}
	if _, err := updates.ParseVersion(version); err != nil {
		return failed("invalid version: %v", err)
	}
	comparison, err := updates.CompareVersions(version, a.RunningVersion)
	if err != nil {
		return failed("compare versions: %v", err)
	}
	if comparison <= 0 {
		return failed("version is not newer than running %s", a.RunningVersion)
	}
	staged := filepath.Join(a.Layout.UpdatesDir(), version)
	document, err := os.ReadFile(filepath.Join(staged, updates.ManifestFileName))
	if err != nil {
		return failed("read release manifest: %v", err)
	}
	envelope, err := updates.ParseEnvelope(document)
	if err != nil {
		return failed("verify release manifest: %v", err)
	}
	manifest, err := envelope.Verify(a.Trusted)
	if err != nil {
		return failed("verify release manifest: %v", err)
	}
	if manifest.Version != version {
		return failed("signed manifest is version %s", manifest.Version)
	}
	artifact, ok := manifest.Find(updates.ComponentPlayer, "linux", a.Arch, "tar.gz")
	if !ok {
		return failed("release has no player bundle for linux/%s", a.Arch)
	}
	bundle := filepath.Join(staged, artifact.Name)
	if ok, err := verifyFile(bundle, artifact); err != nil || !ok {
		return failed("player bundle does not match the signed manifest")
	}
	if err := os.MkdirAll(a.Layout.VersionsDir(), 0o755); err != nil {
		return failed("create versions directory: %v", err)
	}
	if err := a.unpack(bundle, version); err != nil {
		return failed("unpack bundle: %v", err)
	}
	previous, err := os.Readlink(a.Layout.CurrentPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failed("read current version: %v", err)
	}
	switchedAt := a.Now()
	if err := a.point(filepath.Join("versions", version)); err != nil {
		return failed("switch to %s: %v", version, err)
	}
	a.Logf("switched %s to %s", a.Layout.CurrentPath(), version)
	problem := a.restart(ctx)
	if problem == nil {
		problem = a.awaitHealthy(ctx, version, switchedAt)
	}
	if problem == nil {
		a.prune(version, previous)
		a.Logf("update %s healthy", version)
		return Result{Version: version, Status: StatusApplied}
	}
	if previous == "" {
		return failed("%v; no previous version to roll back to", problem)
	}
	if err := a.point(previous); err != nil {
		return failed("%v; roll back to %s: %v", problem, previous, err)
	}
	result := failed("rolled back: %v", problem)
	result.RolledBackTo = filepath.Base(previous)
	if err := a.restart(context.WithoutCancel(ctx)); err != nil {
		result.Error += fmt.Sprintf("; restart after rollback: %v", err)
	}
	return result
}

func (a Applier) restart(ctx context.Context) error {
	if len(a.Units) == 0 {
		return nil
	}
	if err := a.Systemctl(ctx, append([]string{"restart"}, a.Units...)...); err != nil {
		return fmt.Errorf("restart %s: %w", strings.Join(a.Units, " "), err)
	}
	return nil
}

// awaitHealthy waits for a health marker from version written after switchedAt.
func (a Applier) awaitHealthy(ctx context.Context, version string, switchedAt time.Time) error {
	deadline := switchedAt.Add(a.HealthTimeout)
	for {
		if document, err := os.ReadFile(a.Layout.HealthPath()); err == nil {
			var health Health
			if json.Unmarshal(document, &health) == nil && health.Version == version && !health.At.Before(switchedAt) {
				return nil
			}
		}
		if !a.Now().Before(deadline) {
			return fmt.Errorf("version %s did not report a successful control round-trip within %s", version, a.HealthTimeout)
		}
		if err := a.Sleep(ctx, a.HealthPoll); err != nil {
			return err
		}
	}
}

// point atomically replaces the current symlink with one to target.
func (a Applier) point(target string) error {
	temporary := a.Layout.CurrentPath() + ".next"
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, a.Layout.CurrentPath()); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// unpack extracts the bundle into versions/<version>, replacing any earlier
// partial install of the same version. Only regular files and directories
// with relative, non-escaping names are accepted.
func (a Applier) unpack(bundle, version string) error {
	destination := filepath.Join(a.Layout.VersionsDir(), version)
	if current, err := filepath.EvalSymlinks(a.Layout.CurrentPath()); err == nil && current == destination {
		return errors.New("version is already current")
	}
	temporary, err := os.MkdirTemp(a.Layout.VersionsDir(), "."+version+".")
	if err != nil {
		return err
	}
	if err := extract(bundle, temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	if info, err := os.Stat(filepath.Join(temporary, "cuelight-player")); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		_ = os.RemoveAll(temporary)
		return errors.New("bundle has no executable cuelight-player")
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	if err := os.RemoveAll(destination); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	return os.Rename(temporary, destination)
}

func extract(bundle, destination string) error {
	file, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("bundle entry %q escapes the install directory", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, archive)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("bundle entry %q is not a regular file or directory", header.Name)
		}
	}
}

// prune keeps only the new version and the one it replaced.
func (a Applier) prune(version, previous string) {
	entries, err := os.ReadDir(a.Layout.VersionsDir())
	if err != nil {
		return
	}
	keep := map[string]bool{version: true, filepath.Base(previous): true}
	for _, entry := range entries {
		if entry.IsDir() && !keep[entry.Name()] && !strings.HasPrefix(entry.Name(), ".") {
			_ = os.RemoveAll(filepath.Join(a.Layout.VersionsDir(), entry.Name()))
		}
	}
}
