//go:build linux

package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jmill/cable/internal/media/localcas"
)

func TestLocalRegistrationPolicyDisabledAndUnallowed(t *testing.T) {
	repository := newFakeRepository()
	store := &fakeCAS{objects: make(map[string][]byte)}
	root := t.TempDir()
	opener := &trackingOpener{}
	service, err := NewRegistrationService(repository, store, fakeProbe{certified: true, mediaType: "video/mp4"}, opener, StreamCopier{}, sequenceIDs(), LocalRegistrationPolicy{Enabled: false, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	mutation := Mutation{ActorID: "actor-1", IdempotencyKey: "register"}
	if _, err := service.Register(context.Background(), mutation, filepath.Join(root, "clip.mp4")); !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("disabled registration error = %v", err)
	}
	if opener.called {
		t.Fatal("disabled policy attempted to open source")
	}

	service, err = NewRegistrationService(repository, store, fakeProbe{certified: true, mediaType: "video/mp4"}, opener, StreamCopier{}, sequenceIDs(), LocalRegistrationPolicy{Enabled: true, AllowlistedRoots: []string{root}, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(outside, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), mutation, outside); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("unallowed path error = %v", err)
	}
	if opener.called {
		t.Fatal("unallowed path reached safe opener")
	}
}

func TestLocalRegistrationRejectsSymlinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.mp4")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.mp4")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "media.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	service := newRegistrationService(t, root, StreamCopier{})
	mutation := Mutation{ActorID: "actor-1", IdempotencyKey: "register"}
	for _, path := range []string{link, fifo} {
		if _, err := service.Register(context.Background(), mutation, path); !errors.Is(err, ErrUnsafeSource) {
			t.Errorf("unsafe path %q error = %v", path, err)
		}
	}
}

func TestLocalRegistrationRejectsSourceChangedDuringCopy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	copier := mutatingCopier{
		path: path,
		next: StreamCopier{},
	}
	service := newRegistrationService(t, root, copier)
	_, err := service.Register(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "register"}, path)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source error = %v", err)
	}
}
func TestLocalRegistrationGrowthPastLimitDoesNotCommitCAS(t *testing.T) {
	root := t.TempDir()
	repository := newFakeRepository()
	store := &fakeCAS{objects: make(map[string][]byte)}
	source := &growingSafeFile{
		Reader:  bytes.NewReader([]byte("12345")),
		version: SourceVersion{Size: 4, Mode: 0o600},
	}
	service, err := NewRegistrationService(
		repository,
		store,
		fakeProbe{certified: true, mediaType: "video/mp4"},
		fixedOpener{source: source},
		StreamCopier{},
		sequenceIDs(),
		LocalRegistrationPolicy{Enabled: true, AllowlistedRoots: []string{root}, MaxBytes: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Register(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "register"}, filepath.Join(root, "growing.mp4"))
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("growing source error = %v", err)
	}
	if store.puts != 0 || len(store.objects) != 0 {
		t.Fatalf("oversized source committed CAS object: puts=%d objects=%d", store.puts, len(store.objects))
	}
	if len(repository.assets) != 0 {
		t.Fatalf("oversized source created %d assets", len(repository.assets))
	}
}

func TestLocalRegistrationStoresCertifiedRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("certified content"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newRegistrationService(t, root, StreamCopier{})
	asset, err := service.Register(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "register"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != "clip.mp4" || asset.Artifact.MediaType != "video/mp4" || asset.Artifact.State != ArtifactReady {
		t.Fatalf("unexpected registered asset: %#v", asset)
	}
}

func newRegistrationService(t *testing.T, root string, copier SourceCopier) *RegistrationService {
	t.Helper()
	service, err := NewRegistrationService(
		newFakeRepository(),
		&fakeCAS{objects: make(map[string][]byte)},
		fakeProbe{certified: true, mediaType: "video/mp4"},
		OSSafeOpener{},
		copier,
		sequenceIDs(),
		LocalRegistrationPolicy{Enabled: true, AllowlistedRoots: []string{root}, MaxBytes: 1024},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type trackingOpener struct {
	called bool
}

func (o *trackingOpener) Open(string, string) (SafeFile, error) {
	o.called = true
	return nil, errors.New("unexpected open")
}

type mutatingCopier struct {
	path string
	next SourceCopier
}

func (c mutatingCopier) Copy(ctx context.Context, store CAS, source io.Reader, maxBytes int64) (localcas.Object, error) {
	object, err := c.next.Copy(ctx, store, source, maxBytes)
	if err != nil {
		return localcas.Object{}, err
	}
	if err := os.WriteFile(c.path, []byte("changed to a different length"), 0o600); err != nil {
		return localcas.Object{}, err
	}
	return object, nil
}

type fixedOpener struct {
	source SafeFile
}

func (o fixedOpener) Open(string, string) (SafeFile, error) {
	return o.source, nil
}

type growingSafeFile struct {
	*bytes.Reader
	version SourceVersion
}

func (f *growingSafeFile) Close() error { return nil }

func (f *growingSafeFile) Version() (SourceVersion, error) {
	return f.version, nil
}
