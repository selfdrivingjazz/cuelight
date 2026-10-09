package localcas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestPutConvergesAndReadsRange(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("cuelight"), 1<<18)
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	var wait sync.WaitGroup
	objects := make(chan Object, 2)
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			object, err := store.Put(bytes.NewReader(content), digest)
			objects <- object
			errors <- err
		}()
	}
	wait.Wait()
	close(objects)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for object := range objects {
		if object.Digest != digest || object.Size != int64(len(content)) {
			t.Fatalf("unexpected object: %#v", object)
		}
	}

	reader, err := store.OpenRange(digest, 7, 13)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	part, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(part, content[7:20]) {
		t.Fatalf("unexpected range %q", part)
	}
}

func TestPutRejectsDigestMismatch(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(bytes.NewReader([]byte("wrong")), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestReconcileClassifiesCrashStates(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Put(bytes.NewReader([]byte("orphan")), "")
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, ".tmp", "crashed.partial")
	if err := os.WriteFile(partial, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(partial, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	missing := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result, err := store.Reconcile(map[string]bool{missing: true}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(result.ExpiredPartials, partial) {
		t.Fatalf("partial not classified: %#v", result)
	}
	if !slices.Contains(result.OrphanDigests, object.Digest) {
		t.Fatalf("orphan not classified: %#v", result)
	}
	if !slices.Contains(result.MissingReferenced, missing) {
		t.Fatalf("missing object not classified: %#v", result)
	}
}

func TestDigestPathRejectsTraversal(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"../etc/passwd", "ABC", "aaaa/../bbbb"} {
		if _, err := store.Open(digest); err == nil {
			t.Fatalf("unsafe digest accepted: %q", digest)
		}
	}
}
