package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/client"
	"github.com/selfdrivingjazz/cuelight/internal/dev"
	"github.com/selfdrivingjazz/cuelight/internal/fixtures"
)

type fakeLifecycle struct{ ups, downs int }

func (f *fakeLifecycle) Up(context.Context) (dev.Status, error) {
	f.ups++
	return dev.Status{State: "ready", Root: "/tmp/root", Endpoint: "https://127.0.0.1:1"}, nil
}
func (f *fakeLifecycle) Down(context.Context) error { f.downs++; return nil }

type fakeAPI struct {
	statuses []client.Document
	nodes    []client.Node
}

func (f *fakeAPI) ListNodes(context.Context) ([]client.Node, error) { return f.nodes, nil }
func (f *fakeAPI) CreateUpload(context.Context, string, int64, string, string) (client.Upload, error) {
	return client.Upload{ID: "u", Offset: 0}, nil
}
func (f *fakeAPI) AppendUpload(_ context.Context, _ string, _ int64, length int64, reader io.Reader) (int64, error) {
	_, _ = io.ReadAll(reader)
	return length, nil
}
func (f *fakeAPI) CompleteUpload(context.Context, string, string) (client.Asset, error) {
	return client.Asset{ID: "asset"}, nil
}
func (f *fakeAPI) PublishRelease(context.Context, json.RawMessage, string) (client.Document, error) {
	return client.Document{"id": "release", "digest": "digest"}, nil
}
func (f *fakeAPI) CreateMedia(context.Context, any, string) (client.Document, error) {
	return client.Document{"id": "media"}, nil
}
func (f *fakeAPI) MediaForAsset(context.Context, string) (client.Document, bool, error) {
	return nil, false, nil
}
func (f *fakeAPI) PutAssignment(context.Context, string, string, string, client.AssignmentInput) (client.Assignment, error) {
	return client.Assignment{AssignmentInput: client.AssignmentInput{MediaID: "media"}, ReleaseID: "release", ReleaseDigest: "digest", DesiredGeneration: 1}, nil
}
func (f *fakeAPI) GetStatus(context.Context, string) (client.Document, error) {
	if len(f.statuses) == 0 {
		return client.Document{"condition": "active"}, nil
	}
	status := f.statuses[0]
	f.statuses = f.statuses[1:]
	return status, nil
}
func (f *fakeAPI) ListEvents(context.Context, string, string) (client.EventPage, error) {
	return client.EventPage{}, nil
}

func TestRunnerUnsupportedFaultIsTypedAndCleansUp(t *testing.T) {
	catalog, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	life := &fakeLifecycle{}
	var output bytes.Buffer
	runner, err := New(Options{Lifecycle: life, Client: &fakeAPI{nodes: []client.Node{{ID: "node", CredentialState: "enrolled"}}}, Catalog: catalog, Evidence: &output, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), "safe")
	var unsupported *UnsupportedFault
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedFault", err)
	}
	if life.downs != 1 {
		t.Fatalf("cleanup calls = %d, want 1", life.downs)
	}
	var evidence Evidence
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Cleanup != "deterministic" || evidence.Scenario != "safe" {
		t.Fatalf("bad evidence: %+v", evidence)
	}
}

func TestRunnerCancellationStillEmitsEvidence(t *testing.T) {
	catalog, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	life := &fakeLifecycle{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	runner, err := New(Options{Lifecycle: life, Client: &fakeAPI{nodes: []client.Node{{ID: "node"}}}, Catalog: catalog, Evidence: &output})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, "happy-stream-cache")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("evidence lines = %d, want 1", bytes.Count(output.Bytes(), []byte("\n")))
	}
}
