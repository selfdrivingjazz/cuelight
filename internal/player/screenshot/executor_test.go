package screenshot

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/selfdrivingjazz/cuelight/internal/control/preview"
	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

type casCache struct{ *localcas.Store }

func (c casCache) Put(r io.Reader, d string) (localcas.Object, error) { return c.Store.Put(r, d) }
func (c casCache) Open(d string) (io.ReadCloser, error)               { return c.Store.Open(d) }

type fakeJobs struct {
	job       preview.ScreenshotJob
	completed preview.Completion
	bytes     []byte
}

func (f *fakeJobs) Next(context.Context) (preview.ScreenshotJob, bool, error) {
	return f.job, f.job.ID != "" && f.job.Status == preview.StatusQueued, nil
}
func (f *fakeJobs) Claim(context.Context, string) (preview.ScreenshotJob, error) {
	f.job.Status = preview.StatusClaimed
	return f.job, nil
}
func (f *fakeJobs) Complete(_ context.Context, _ string, c preview.Completion) (preview.ScreenshotJob, error) {
	f.completed = c
	f.job.Status = c.Status
	return f.job, nil
}
func (f *fakeJobs) CompletePNG(_ context.Context, _ string, digest, mediaType string, _ int64, body io.Reader) (preview.ScreenshotJob, error) {
	f.bytes, _ = io.ReadAll(body)
	f.completed = preview.Completion{Status: preview.StatusCompleted, ArtifactDigest: digest, MediaType: mediaType}
	f.job.Status = preview.StatusCompleted
	return f.job, nil
}

type fakeCapture struct {
	bytes []byte
	err   error
}

func (f fakeCapture) Capture(context.Context) ([]byte, error) { return f.bytes, f.err }

func TestRunOnceStoresExactPNGAndCompletes(t *testing.T) {
	const pngData = "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\x0dIDAT\x08\xd7c\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB\x60\x82"
	cache, err := localcas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jobs := &fakeJobs{job: preview.ScreenshotJob{ID: "job", NodeID: "node", Status: preview.StatusQueued}}
	exec, err := New(Config{NodeID: "node", MaxBytes: 1 << 20}, jobs, fakeCapture{bytes: []byte(pngData)}, casCache{cache})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := exec.RunOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("run: ok=%v err=%v", ok, err)
	}
	if jobs.completed.Status != preview.StatusCompleted || string(jobs.bytes) != pngData {
		t.Fatalf("completion=%#v bytes=%d", jobs.completed, len(jobs.bytes))
	}
	if _, err := cache.Stat(jobs.completed.ArtifactDigest); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceUnsupportedIsTerminalWithoutImage(t *testing.T) {
	cache, err := localcas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jobs := &fakeJobs{job: preview.ScreenshotJob{ID: "job", NodeID: "node", Status: preview.StatusQueued}}
	exec, err := New(Config{NodeID: "node"}, jobs, fakeCapture{err: ErrUnsupported}, casCache{cache})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exec.RunOnce(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("run err=%v", err)
	}
}
