package renderer

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/release"
)

func TestChromiumLoadsBuiltCableShellAndSignalsReady(t *testing.T) {
	binary := os.Getenv("CABLE_CHROMIUM_BINARY")
	shellDirectory := os.Getenv("CABLE_CHROMIUM_SHELL_DIR")
	if binary == "" || shellDirectory == "" {
		t.Skip("set CABLE_CHROMIUM_BINARY and CABLE_CHROMIUM_SHELL_DIR for the real Chromium gate")
	}
	runtimeRoot := t.TempDir()
	shellPath := filepath.Join(runtimeRoot, "shell.tar")
	archiveDirectory(t, shellDirectory, shellPath)
	shellDigest := fileDigest(t, shellPath)
	revisionDocument := map[string]any{
		"schema": "cable.revision.v1", "shellBuildDigest": shellDigest, "entrypoint": "index.html",
		"bridgeProtocol": "1", "generatedAt": "2026-08-27T17:00:00Z", "theme": "nebula",
		"timezone": "UTC", "channels": []any{}, "artifactDigests": []string{shellDigest},
	}
	revisionBytes, err := json.Marshal(revisionDocument)
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest := bytesDigest(revisionBytes)
	revisionPath := filepath.Join(runtimeRoot, "revision.json")
	if err := os.WriteFile(revisionPath, revisionBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		Kind: release.PresentationCable, ReleaseDigest: strings.Repeat("d", 64), ShellBuildDigest: shellDigest,
		RevisionArtifactDigest: revisionDigest, Entrypoint: "index.html", BridgeProtocol: "1",
		ArtifactClosure: []string{shellDigest, revisionDigest}, ShellPath: shellPath, RevisionPath: revisionPath,
		ArtifactPaths: map[string]string{shellDigest: shellPath, revisionDigest: revisionPath},
	}
	adapter, err := NewChromium(ChromiumConfig{
		BinaryPath: binary, RuntimeRoot: runtimeRoot, Timeout: 20 * time.Second, Output: os.Stderr,
		ExtraArgs: []string{"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--enable-logging=stderr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := adapter.Prepare(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(context.Background())
	status, err := adapter.Health(ctx, candidate)
	if err != nil || !status.Healthy || status.ReleaseDigest != candidate.ReleaseDigest {
		t.Fatalf("Chromium status = %#v, %v", status, err)
	}
	if err := adapter.Commit(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	current, err := adapter.Current(ctx)
	if err != nil || !current.Healthy || current.Candidate.Kind != release.PresentationCable {
		t.Fatalf("current Chromium = %#v, %v", current, err)
	}
}

func archiveDirectory(t *testing.T, root, destination string) {
	t.Helper()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(output)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name, err = filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(header.Name)
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archive, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func bytesDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
