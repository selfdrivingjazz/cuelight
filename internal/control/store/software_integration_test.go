package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	controlplayerapi "github.com/jmilldotdev/cuelight/internal/control/playerapi"
	"github.com/jmilldotdev/cuelight/internal/control/software"
	"github.com/jmilldotdev/cuelight/internal/media/localcas"
	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

type softwareFixture struct {
	repository *Store
	service    *software.Service
	key        ed25519.PrivateKey
	actorID    string
	nodeID     string
}

func newSoftwareFixture(t *testing.T) softwareFixture {
	t.Helper()
	repository, db := newPostgresTestStore(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cas, err := localcas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := softwareFixture{
		repository: repository,
		service:    &software.Service{Repository: repository, Blobs: cas, Trusted: map[string]ed25519.PublicKey{"test-key": public}, PublicURL: "https://control.example", ControlVersion: "0.3.0"},
		key:        private, actorID: "00000000-0000-4000-8000-000000000061", nodeID: "00000000-0000-4000-8000-000000000062",
	}
	ctx := context.Background()
	if err := repository.EnsureActor(ctx, fixture.actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, 'mac', 'active', $1)`, fixture.nodeID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// writeRelease writes a signed release directory whose files hold contents.
func (f softwareFixture) writeRelease(t *testing.T, version string, contents map[string]string, key ed25519.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()
	targets := map[string][4]string{
		"latest-mac.yml": {updates.ComponentDesktop, "darwin", "any", "yml"},
		"Cuelight-" + version + "-mac-universal.zip": {updates.ComponentDesktop, "darwin", "universal", "zip"},
		"Cuelight-" + version + "-android.apk":       {updates.ComponentAndroid, "android", "any", "apk"},
	}
	manifest := updates.Manifest{Schema: updates.SchemaV1, Product: updates.Product, Version: version, Channel: updates.ChannelStable, PublishedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), MinimumProtocolVersion: "1.0"}
	for name, target := range targets {
		body := contents[name]
		if body == "" {
			body = name + " bytes"
		}
		digest := sha256.Sum256([]byte(body))
		manifest.Artifacts = append(manifest.Artifacts, updates.Artifact{Component: target[0], OS: target[1], Arch: target[2], Format: target[3], Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])})
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := updates.Sign(manifest, "test-key", key)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, updates.ManifestFileName), document, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f softwareFixture) report(t *testing.T, version, method string) {
	t.Helper()
	report := playerclient.CapabilityReport{Platform: "darwin", Architecture: "arm64", ProtocolVersion: "1.0", RendererVersion: "1", ProfileID: "default", SoftwareVersion: version, UpdateMethod: method}
	if err := f.repository.PutCapabilities(context.Background(), f.nodeID, report); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSoftwareImportIsIdempotentAndVerified(t *testing.T) {
	f := newSoftwareFixture(t)
	ctx := context.Background()
	dir := f.writeRelease(t, "0.3.0", nil, f.key)

	first, err := f.service.ImportDir(ctx, software.SourceCLI, dir)
	if err != nil || !first.Created {
		t.Fatalf("first import = %#v, %v", first, err)
	}
	for _, artifact := range first.Artifacts {
		if artifact.State != software.ArtifactImported {
			t.Fatalf("first import artifact %#v", artifact)
		}
	}
	again, err := f.service.ImportDir(ctx, software.SourceUpload, dir)
	if err != nil || again.Created {
		t.Fatalf("second import = %#v, %v", again, err)
	}
	for _, artifact := range again.Artifacts {
		if artifact.State != software.ArtifactPresent {
			t.Fatalf("second import artifact %#v", artifact)
		}
	}
	releases, err := f.service.Releases(ctx)
	if err != nil || len(releases) != 1 || releases[0].Source != software.SourceCLI {
		t.Fatalf("releases = %#v, %v", releases, err)
	}

	// The same version with different signed bytes is refused.
	other := f.writeRelease(t, "0.3.0", map[string]string{"latest-mac.yml": "other yml"}, f.key)
	if _, err := f.service.ImportDir(ctx, software.SourceCLI, other); !errors.Is(err, software.ErrConflict) {
		t.Fatalf("conflicting import error = %v", err)
	}

	// A release signed by an untrusted key is never stored.
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	unsigned := f.writeRelease(t, "0.4.0", nil, stranger)
	if _, err := f.service.ImportDir(ctx, software.SourceCLI, unsigned); !errors.Is(err, software.ErrUnverified) {
		t.Fatalf("untrusted import error = %v", err)
	}
	if _, err := f.service.Release(ctx, "0.4.0"); !errors.Is(err, software.ErrNotFound) {
		t.Fatalf("untrusted release stored: %v", err)
	}

	// Bytes that differ from the signed sha256 are rejected; the rest import,
	// and the right bytes import later.
	tampered := f.writeRelease(t, "0.5.0", nil, f.key)
	apk := filepath.Join(tampered, "Cuelight-0.5.0-android.apk")
	original, err := os.ReadFile(apk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apk, append([]byte("X"), original[1:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ImportDir(ctx, software.SourceCLI, tampered)
	if !errors.Is(err, software.ErrArtifactMismatch) {
		t.Fatalf("tampered import error = %v", err)
	}
	states := map[string]string{}
	for _, artifact := range result.Artifacts {
		states[artifact.Name] = artifact.State
	}
	if states["Cuelight-0.5.0-android.apk"] != software.ArtifactRejected || states["latest-mac.yml"] != software.ArtifactImported {
		t.Fatalf("tampered states = %#v", states)
	}
	if _, _, err := f.service.OpenNamedArtifact(ctx, "0.5.0", "Cuelight-0.5.0-android.apk"); !errors.Is(err, software.ErrNotFound) {
		t.Fatalf("rejected artifact served: %v", err)
	}
	if err := os.WriteFile(apk, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := f.service.ImportDir(ctx, software.SourceCLI, tampered); err != nil || result.Created {
		t.Fatalf("repaired import = %#v, %v", result, err)
	}
	file, _, err := f.service.OpenNamedArtifact(ctx, "0.5.0", "Cuelight-0.5.0-android.apk")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func TestPostgresSoftwareTargetNeverBelowRunning(t *testing.T) {
	f := newSoftwareFixture(t)
	ctx := context.Background()
	for _, version := range []string{"0.2.0", "0.4.0"} {
		if _, err := f.service.ImportDir(ctx, software.SourceCLI, f.writeRelease(t, version, nil, f.key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.SetTarget(ctx, f.actorID, f.nodeID, "0.4.0"); !errors.Is(err, software.ErrInvalid) {
		t.Fatalf("target without a reported version = %v", err)
	}
	f.report(t, "0.3.0", software.MethodDesktop)
	status, err := f.service.NodeStatus(ctx, f.nodeID)
	if err != nil || status.Status != software.StatusOutdated || status.LatestAvailable != "0.4.0" {
		t.Fatalf("status = %#v, %v", status, err)
	}
	if _, err := f.service.SetTarget(ctx, f.actorID, f.nodeID, "0.2.0"); !errors.Is(err, software.ErrDowngrade) {
		t.Fatalf("downgrade target = %v", err)
	}
	rollout, err := f.service.Rollout(ctx, f.actorID, software.RolloutRequest{Version: "0.2.0", NodeIDs: []string{f.nodeID}})
	if err != nil || len(rollout.Updated) != 0 || len(rollout.Skipped) != 1 {
		t.Fatalf("downgrade rollout = %#v, %v", rollout, err)
	}
	if status, err := f.service.NodeStatus(ctx, f.nodeID); err != nil || status.Target != "" {
		t.Fatalf("downgrade left a target: %#v, %v", status, err)
	}
	rollout, err = f.service.Rollout(ctx, f.actorID, software.RolloutRequest{Version: "0.4.0", AllEligible: true})
	if err != nil || len(rollout.Updated) != 1 || rollout.Updated[0].Status != software.StatusUpdating || rollout.Updated[0].Target != "0.4.0" {
		t.Fatalf("rollout = %#v, %v", rollout, err)
	}
	f.report(t, "0.4.0", software.MethodDesktop)
	if status, err := f.service.NodeStatus(ctx, f.nodeID); err != nil || status.Status != software.StatusCurrent {
		t.Fatalf("after update status = %#v, %v", status, err)
	}
}

func TestPostgresSoftwareTargetChangesAssignmentETag(t *testing.T) {
	f := newSoftwareFixture(t)
	ctx := context.Background()
	for _, version := range []string{"0.4.0", "0.5.0"} {
		if _, err := f.service.ImportDir(ctx, software.SourceCLI, f.writeRelease(t, version, nil, f.key)); err != nil {
			t.Fatal(err)
		}
	}
	f.report(t, "0.3.0", software.MethodAndroid)
	player, err := controlplayerapi.NewService(f.repository, f.repository, NewLocalCASReader(nil), controlplayerapi.Limits{MaxWait: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	player.Software = f.service
	if _, err := player.GetAssignment(ctx, f.nodeID, "", 0); !errors.Is(err, controlplayerapi.ErrNotFound) {
		t.Fatalf("no assignment, no target = %v", err)
	}
	if _, err := f.service.SetTarget(ctx, f.actorID, f.nodeID, "0.4.0"); err != nil {
		t.Fatal(err)
	}
	first, err := player.GetAssignment(ctx, f.nodeID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	software040 := first.Assignment.Software
	if software040 == nil || software040.Version != "0.4.0" || software040.APKURL != "https://control.example/updates/0.4.0/Cuelight-0.4.0-android.apk" || len(software040.APKSHA256) != 64 || first.Assignment.ReleaseID != "" {
		t.Fatalf("software-only assignment = %#v", first.Assignment)
	}
	if again, err := player.GetAssignment(ctx, f.nodeID, first.ETag, 0); err != nil || !again.NotModified {
		t.Fatalf("unchanged poll = %#v, %v", again, err)
	}

	// A long poll on the current ETag wakes when the target changes.
	type polled struct {
		Result playerclient.AssignmentResult
		Err    error
	}
	done := make(chan polled, 1)
	go func() {
		result, err := player.GetAssignment(ctx, f.nodeID, first.ETag, 8*time.Second)
		done <- polled{Result: result, Err: err}
	}()
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	if _, err := f.service.SetTarget(ctx, f.actorID, f.nodeID, "0.5.0"); err != nil {
		t.Fatal(err)
	}
	woke := <-done
	if woke.Err != nil || woke.Result.NotModified || woke.Result.ETag == first.ETag || woke.Result.Assignment.Software.Version != "0.5.0" || time.Since(started) > 2*time.Second {
		t.Fatalf("long poll = %#v after %s", woke, time.Since(started))
	}
	if _, err := f.service.ClearTarget(ctx, f.actorID, f.nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := player.GetAssignment(ctx, f.nodeID, woke.Result.ETag, 0); !errors.Is(err, controlplayerapi.ErrNotFound) {
		t.Fatalf("cleared target poll = %v", err)
	}
}
