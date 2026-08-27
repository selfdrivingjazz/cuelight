package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateKeyPathFailsClosedOnBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher.key")
	if err := os.WriteFile(path, []byte("private"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateFile("publisher private key", path); err == nil {
		t.Fatal("expected group-readable private key to fail")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateFile("publisher private key", path); err != nil {
		t.Fatalf("secure private key rejected: %v", err)
	}
}

func TestStartupPathsRejectSymlinksAndWritableCAS(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.key")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateFile("TLS private key", link); err == nil {
		t.Fatal("expected private-key symlink to fail")
	}

	cas := filepath.Join(root, "cas")
	if err := os.Mkdir(cas, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cas, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateCASDirectory(cas); err == nil {
		t.Fatal("expected broadly writable CAS to fail")
	}
}

func TestMissingStartupPathReturnsTypedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.crt")
	err := validatePublicFile("TLS certificate", path)
	var pathErr *PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatalf("error = %v, want PathError for %s", err, path)
	}
}
