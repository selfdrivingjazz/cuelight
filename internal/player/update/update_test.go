package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	playerruntime "github.com/jmilldotdev/cuelight/internal/player/runtime"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

type release struct {
	files    map[string][]byte
	manifest updates.Manifest
	envelope []byte
}

func newRelease(t *testing.T, version string, key ed25519.PrivateKey, files map[string][]byte, classify func(name string) updates.Artifact) release {
	t.Helper()
	manifest := updates.Manifest{Schema: updates.SchemaV1, Product: updates.Product, Version: version, Channel: updates.ChannelStable, PublishedAt: time.Now().UTC(), MinimumProtocolVersion: "1.0"}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		artifact := classify(name)
		sum := sha256.Sum256(files[name])
		artifact.Name, artifact.Size, artifact.SHA256 = name, int64(len(files[name])), hex.EncodeToString(sum[:])
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	envelope, err := updates.Sign(manifest, "test", key)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return release{files: files, manifest: manifest, envelope: document}
}

// fakeControl serves the player software endpoints and records requests.
type fakeControl struct {
	mu       sync.Mutex
	release  release
	requests []string
	server   *httptest.Server
}

func newFakeControl(t *testing.T, r release) *fakeControl {
	control := &fakeControl{release: r}
	control.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		control.mu.Lock()
		control.requests = append(control.requests, request.Method+" "+request.URL.Path+" "+request.Header.Get("Range"))
		control.mu.Unlock()
		if strings.HasPrefix(request.URL.Path, "/api/v1/player/software/releases/") {
			_, _ = w.Write(r.envelope)
			return
		}
		digest := strings.TrimPrefix(request.URL.Path, "/api/v1/player/software/artifacts/")
		for _, artifact := range r.manifest.Artifacts {
			if artifact.SHA256 == digest {
				http.ServeContent(w, request, artifact.Name, time.Time{}, bytes.NewReader(r.files[artifact.Name]))
				return
			}
		}
		http.NotFound(w, request)
	}))
	t.Cleanup(control.server.Close)
	return control
}

func (c *fakeControl) target(version string) *playerclient.SoftwareTarget {
	return &playerclient.SoftwareTarget{Version: version, ManifestURL: "/api/v1/player/software/releases/" + version, ArtifactsBaseURL: "/api/v1/player/software/artifacts/"}
}

func (c *fakeControl) artifactRequests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []string
	for _, request := range c.requests {
		if strings.Contains(request, "/artifacts/") {
			result = append(result, request)
		}
	}
	return result
}

func runManager(t *testing.T, config Config, target *playerclient.SoftwareTarget, until func([]playerruntime.ExternalEvent) bool) []playerruntime.ExternalEvent {
	t.Helper()
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = manager.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	manager.Offer(target)
	var events []playerruntime.ExternalEvent
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		events = append(events, manager.DrainEvents()...)
		if until(events) {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("manager did not reach the expected state; events=%v", events)
	return nil
}

func hasEvent(kind string) func([]playerruntime.ExternalEvent) bool {
	return func(events []playerruntime.ExternalEvent) bool {
		return slices.ContainsFunc(events, func(event playerruntime.ExternalEvent) bool { return event.Type == kind })
	}
}

func testKeys(t *testing.T) (ed25519.PrivateKey, map[string]ed25519.PublicKey) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private, map[string]ed25519.PublicKey{"test": public}
}

func desktopRelease(t *testing.T, key ed25519.PrivateKey, version string) release {
	feed := "version: " + version + "\nfiles:\n  - url: Cuelight-" + version + "-mac-universal.zip\n    sha512: abc\n    size: 3\n  - url: 'Cuelight-" + version + "-mac-universal.dmg'\n    sha512: def\npath: Cuelight-" + version + "-mac-universal.zip\nsha512: abc\nreleaseDate: '2026-10-08T00:00:00.000Z'\n"
	return newRelease(t, version, key, map[string][]byte{
		"latest-mac.yml": []byte(feed),
		"Cuelight-" + version + "-mac-universal.zip":          []byte("zip-bytes"),
		"Cuelight-" + version + "-mac-universal.zip.blockmap": []byte("zip-blockmap"),
		"Cuelight-" + version + "-mac-universal.dmg":          []byte("dmg-bytes"),
		"Cuelight-" + version + "-win-x64.exe":                []byte("exe-bytes"),
		"latest.yml":                                          []byte("path: Cuelight-" + version + "-win-x64.exe\n"),
	}, func(name string) updates.Artifact {
		switch {
		case name == "latest-mac.yml":
			return updates.Artifact{Component: updates.ComponentDesktop, OS: "darwin", Arch: "any", Format: "yml"}
		case name == "latest.yml":
			return updates.Artifact{Component: updates.ComponentDesktop, OS: "windows", Arch: "any", Format: "yml"}
		case strings.Contains(name, "-win-"):
			return updates.Artifact{Component: updates.ComponentDesktop, OS: "windows", Arch: "amd64", Format: "exe"}
		}
		format := strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(name, ".blockmap")), ".")
		if strings.HasSuffix(name, ".blockmap") {
			format += ".blockmap"
		}
		return updates.Artifact{Component: updates.ComponentDesktop, OS: "darwin", Arch: "universal", Format: format}
	})
}

func TestFeedFilesFollowUpdaterMetadataAndIncludeBlockmaps(t *testing.T) {
	key, _ := testKeys(t)
	r := desktopRelease(t, key, "1.2.0")
	feed, err := FeedArtifact(r.manifest, "darwin", "arm64")
	if err != nil || feed.Name != "latest-mac.yml" {
		t.Fatalf("feed=%v err=%v", feed.Name, err)
	}
	files, err := FeedFiles(r.manifest, r.files["latest-mac.yml"])
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name)
	}
	want := []string{"Cuelight-1.2.0-mac-universal.zip", "Cuelight-1.2.0-mac-universal.zip.blockmap", "Cuelight-1.2.0-mac-universal.dmg"}
	if !slices.Equal(names, want) {
		t.Fatalf("files=%v want %v", names, want)
	}
	if _, err := FeedFiles(r.manifest, []byte("files:\n  - url: Unsigned-1.2.0.zip\npath: Unsigned-1.2.0.zip\n")); err == nil {
		t.Fatal("a feed referencing a file outside the signed manifest must be refused")
	}
	if _, err := FeedFiles(r.manifest, []byte("path: https://evil.example/Cuelight.zip\n")); err == nil {
		t.Fatal("a feed referencing a URL must be refused")
	}
}

func TestDesktopUpdateStagesVerifiedFeedAndHandsItOff(t *testing.T) {
	key, trusted := testKeys(t)
	r := desktopRelease(t, key, "1.2.0")
	control := newFakeControl(t, r)
	var handed []string
	config := Config{
		Method: MethodDesktop, RunningVersion: "1.1.0", OS: "darwin", Arch: "arm64", Dir: t.TempDir(), HTTP: control.server.Client(), ControlURL: control.server.URL + "/api/v1", Trusted: trusted,
		Handoff: func(_ context.Context, version, feedDir string) error {
			handed = append(handed, version, feedDir)
			return nil
		},
	}
	runManager(t, config, control.target("1.2.0"), hasEvent(EventStaged))
	feedDir := filepath.Join(config.Dir, "1.2.0")
	if len(handed) != 2 || handed[0] != "1.2.0" || handed[1] != feedDir {
		t.Fatalf("handoff=%v", handed)
	}
	entries, err := os.ReadDir(feedDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		content, _ := os.ReadFile(filepath.Join(feedDir, entry.Name()))
		if !bytes.Equal(content, r.files[entry.Name()]) {
			t.Fatalf("%s content differs from the release", entry.Name())
		}
	}
	want := []string{"Cuelight-1.2.0-mac-universal.dmg", "Cuelight-1.2.0-mac-universal.zip", "Cuelight-1.2.0-mac-universal.zip.blockmap", "latest-mac.yml"}
	if !slices.Equal(names, want) {
		t.Fatalf("feed dir=%v want %v", names, want)
	}
}

func TestUntrustedManifestStopsUpdateBeforeDownloading(t *testing.T) {
	_, trusted := testKeys(t)
	otherKey, _ := testKeys(t)
	r := desktopRelease(t, otherKey, "1.2.0")
	control := newFakeControl(t, r)
	handedOff := false
	config := Config{
		Method: MethodDesktop, RunningVersion: "1.1.0", OS: "darwin", Arch: "arm64", Dir: t.TempDir(), HTTP: control.server.Client(), ControlURL: control.server.URL, Trusted: trusted,
		Handoff: func(context.Context, string, string) error { handedOff = true; return nil },
	}
	events := runManager(t, config, control.target("1.2.0"), hasEvent(EventFailed))
	if handedOff || len(control.artifactRequests()) != 0 {
		t.Fatalf("unverified release was acted on: handoff=%v artifact requests=%v", handedOff, control.artifactRequests())
	}
	if !strings.Contains(events[0].Data["error"].(string), "signature is invalid") || events[0].Data["version"] != "1.2.0" {
		t.Fatalf("event=%v", events[0])
	}
}

func TestOlderTargetIsIgnoredAndReported(t *testing.T) {
	key, trusted := testKeys(t)
	control := newFakeControl(t, desktopRelease(t, key, "1.0.0"))
	config := Config{
		Method: MethodDesktop, RunningVersion: "1.1.0", OS: "darwin", Arch: "arm64", Dir: t.TempDir(), HTTP: control.server.Client(), ControlURL: control.server.URL, Trusted: trusted,
		Handoff: func(context.Context, string, string) error { t.Error("downgrade handed off"); return nil },
	}
	events := runManager(t, config, control.target("1.0.0"), hasEvent(EventIgnored))
	if events[0].Data["version"] != "1.0.0" || events[0].Data["running"] != "1.1.0" {
		t.Fatalf("event=%v", events[0])
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if len(control.requests) != 0 {
		t.Fatalf("downgrade fetched %v", control.requests)
	}
}

func TestDownloadResumesPartialFile(t *testing.T) {
	key, _ := testKeys(t)
	content := bytes.Repeat([]byte("cuelight"), 4096)
	r := newRelease(t, "1.2.0", key, map[string][]byte{"bundle.tar.gz": content}, func(string) updates.Artifact {
		return updates.Artifact{Component: updates.ComponentPlayer, OS: "linux", Arch: "amd64", Format: "tar.gz"}
	})
	control := newFakeControl(t, r)
	artifact := r.manifest.Artifacts[0]
	path := filepath.Join(t.TempDir(), artifact.Name)
	if err := os.WriteFile(path+".partial", content[:1000], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download(context.Background(), control.server.Client(), control.server.URL+"/api/v1/player/software/artifacts/"+artifact.SHA256, artifact, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, content) {
		t.Fatal("resumed file differs")
	}
	if requests := control.artifactRequests(); len(requests) != 1 || !strings.HasSuffix(requests[0], "bytes=1000-") {
		t.Fatalf("requests=%v", requests)
	}
	if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Fatal("partial file left behind")
	}
}

func bundleTarball(t *testing.T, marker string) []byte {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, entry := range []struct {
		name string
		mode int64
		body string
	}{{"cuelight-player", 0o755, "#!/bin/sh\necho " + marker + "\n"}, {"cuelight-minimal-node", 0o755, "minimal"}, {"systemd/cuelight-updater.path", 0o644, "[Path]"}} {
		if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = archive.Write([]byte(entry.body))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// stagedAppliance prepares an installed 1.0.0 and a staged, requested 1.1.0.
func stagedAppliance(t *testing.T, key ed25519.PrivateKey) Layout {
	layout := Layout{OptDir: filepath.Join(t.TempDir(), "opt"), StateDir: filepath.Join(t.TempDir(), "state")}
	old := filepath.Join(layout.VersionsDir(), "1.0.0")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("versions/1.0.0", layout.CurrentPath()); err != nil {
		t.Fatal(err)
	}
	name := "cuelight-player_1.1.0_linux_amd64.tar.gz"
	r := newRelease(t, "1.1.0", key, map[string][]byte{name: bundleTarball(t, "1.1.0")}, func(string) updates.Artifact {
		return updates.Artifact{Component: updates.ComponentPlayer, OS: "linux", Arch: "amd64", Format: "tar.gz"}
	})
	staged := filepath.Join(layout.UpdatesDir(), "1.1.0")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		filepath.Join(staged, name):                         r.files[name],
		filepath.Join(staged, updates.ManifestFileName):     r.envelope,
		filepath.Join(layout.UpdatesDir(), RequestFileName): []byte(`{"version":"1.1.0"}`),
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return layout
}

func TestApplySwitchesVersionAndRollsBackWithoutHealthMarker(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(map[bool]string{true: "healthy", false: "no health marker"}[healthy], func(t *testing.T) {
			key, trusted := testKeys(t)
			layout := stagedAppliance(t, key)
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			var calls []string
			applier := Applier{
				Layout: layout, Trusted: trusted, RunningVersion: "1.0.0", Arch: "amd64", Units: []string{"cuelight-minimal-node.service"},
				Systemctl: func(_ context.Context, args ...string) error {
					target, _ := os.Readlink(layout.CurrentPath())
					calls = append(calls, strings.Join(args, " ")+" -> "+target)
					if healthy && target == "versions/1.1.0" {
						return WriteHealth(layout.HealthPath(), "1.1.0", now.Add(time.Second))
					}
					return nil
				},
				Now:           func() time.Time { return now },
				Sleep:         func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil },
				HealthTimeout: time.Minute, HealthPoll: 5 * time.Second,
			}
			// A marker from the old version, or from before the switch, never counts.
			if err := WriteHealth(layout.HealthPath(), "1.0.0", now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			result, err := applier.Apply(context.Background())
			current, _ := os.Readlink(layout.CurrentPath())
			recorded, _ := os.ReadFile(filepath.Join(layout.UpdatesDir(), ResultFileName))
			var stored Result
			_ = json.Unmarshal(recorded, &stored)
			if _, statErr := os.Stat(filepath.Join(layout.UpdatesDir(), RequestFileName)); !os.IsNotExist(statErr) {
				t.Fatal("request was not consumed")
			}
			if healthy {
				if err != nil || current != "versions/1.1.0" || stored.Status != StatusApplied || len(calls) != 1 {
					t.Fatalf("err=%v current=%s result=%+v calls=%v", err, current, stored, calls)
				}
				if script, _ := os.ReadFile(filepath.Join(layout.VersionsDir(), "1.1.0", "cuelight-player")); !strings.Contains(string(script), "1.1.0") {
					t.Fatal("bundle not unpacked into versions/1.1.0")
				}
				return
			}
			want := []string{"restart cuelight-minimal-node.service -> versions/1.1.0", "restart cuelight-minimal-node.service -> versions/1.0.0"}
			if err == nil || current != "versions/1.0.0" || !slices.Equal(calls, want) {
				t.Fatalf("err=%v current=%s calls=%v", err, current, calls)
			}
			if stored.Status != StatusFailed || stored.RolledBackTo != "1.0.0" || !strings.HasPrefix(stored.Error, "rolled back:") || result.Version != "1.1.0" {
				t.Fatalf("result=%+v", stored)
			}
		})
	}
}

func TestApplyRefusesTamperedBundle(t *testing.T) {
	key, trusted := testKeys(t)
	layout := stagedAppliance(t, key)
	bundle := filepath.Join(layout.UpdatesDir(), "1.1.0", "cuelight-player_1.1.0_linux_amd64.tar.gz")
	if err := os.WriteFile(bundle, bundleTarball(t, "evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	applier := Applier{Layout: layout, Trusted: trusted, RunningVersion: "1.0.0", Arch: "amd64", Now: time.Now,
		Systemctl: func(context.Context, ...string) error { t.Fatal("tampered bundle restarted units"); return nil }}
	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("tampered bundle applied")
	}
	if current, _ := os.Readlink(layout.CurrentPath()); current != "versions/1.0.0" {
		t.Fatalf("current=%s", current)
	}
}
