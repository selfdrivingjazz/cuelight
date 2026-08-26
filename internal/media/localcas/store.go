package localcas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DigestLength = 64

type Store struct {
	root string
}

type Object struct {
	Digest string
	Size   int64
	Path   string
}

type ReconcileResult struct {
	ExpiredPartials   []string
	OrphanDigests     []string
	MissingReferenced []string
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("CAS root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{absolute, filepath.Join(absolute, ".tmp"), filepath.Join(absolute, "sha256")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{root: absolute}, nil
}

func (s *Store) Put(reader io.Reader, expectedDigest string) (Object, error) {
	if expectedDigest != "" {
		if err := ValidateDigest(expectedDigest); err != nil {
			return Object{}, err
		}
	}
	temporary, err := os.CreateTemp(filepath.Join(s.root, ".tmp"), "upload-*.partial")
	if err != nil {
		return Object{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	hash := sha256.New()
	size, copyErr := io.CopyBuffer(io.MultiWriter(temporary, hash), reader, make([]byte, 128*1024))
	if copyErr != nil {
		temporary.Close()
		return Object{}, copyErr
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Object{}, err
	}
	if err := temporary.Close(); err != nil {
		return Object{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedDigest != "" && expectedDigest != digest {
		return Object{}, fmt.Errorf("digest mismatch: expected %s, observed %s", expectedDigest, digest)
	}
	final, err := s.path(digest)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return Object{}, err
	}
	if err := os.Link(temporaryName, final); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return Object{}, err
		}
		info, statErr := os.Stat(final)
		if statErr != nil {
			return Object{}, statErr
		}
		if info.Size() != size {
			return Object{}, errors.New("existing CAS object has conflicting size")
		}
	} else if err := syncDirectory(filepath.Dir(final)); err != nil {
		return Object{}, err
	}
	return Object{Digest: digest, Size: size, Path: final}, nil
}

func (s *Store) Stat(digest string) (Object, error) {
	path, err := s.path(digest)
	if err != nil {
		return Object{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Object{}, err
	}
	if !info.Mode().IsRegular() {
		return Object{}, errors.New("CAS object is not a regular file")
	}
	return Object{Digest: digest, Size: info.Size(), Path: path}, nil
}

func (s *Store) Open(digest string) (*os.File, error) {
	path, err := s.path(digest)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Store) OpenRange(digest string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, errors.New("range offset and length must be non-negative")
	}
	file, err := s.Open(digest)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if offset > info.Size() || length > info.Size()-offset {
		file.Close()
		return nil, errors.New("range exceeds object length")
	}
	return &sectionReadCloser{Reader: io.NewSectionReader(file, offset, length), closer: file}, nil
}

func (s *Store) Reconcile(referenced map[string]bool, partialGrace time.Duration, now time.Time) (ReconcileResult, error) {
	result := ReconcileResult{}
	partialEntries, err := os.ReadDir(filepath.Join(s.root, ".tmp"))
	if err != nil {
		return result, err
	}
	for _, entry := range partialEntries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		if now.Sub(info.ModTime()) >= partialGrace {
			result.ExpiredPartials = append(result.ExpiredPartials, filepath.Join(s.root, ".tmp", entry.Name()))
		}
	}
	seen := map[string]bool{}
	shaRoot := filepath.Join(s.root, "sha256")
	err = filepath.WalkDir(shaRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		digest := entry.Name()
		if err := ValidateDigest(digest); err != nil {
			return fmt.Errorf("invalid CAS entry %s: %w", path, err)
		}
		seen[digest] = true
		if !referenced[digest] {
			result.OrphanDigests = append(result.OrphanDigests, digest)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for digest := range referenced {
		if err := ValidateDigest(digest); err != nil {
			return result, err
		}
		if !seen[digest] {
			result.MissingReferenced = append(result.MissingReferenced, digest)
		}
	}
	return result, nil
}

func (s *Store) RemovePartial(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	temporaryRoot := filepath.Join(s.root, ".tmp") + string(os.PathSeparator)
	if !strings.HasPrefix(absolute, temporaryRoot) {
		return errors.New("partial path is outside CAS temporary root")
	}
	return os.Remove(absolute)
}

func (s *Store) RemoveOrphan(digest string) error {
	path, err := s.path(digest)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *Store) path(digest string) (string, error) {
	if err := ValidateDigest(digest); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "sha256", digest[:2], digest), nil
}

func ValidateDigest(digest string) error {
	if len(digest) != DigestLength || strings.ToLower(digest) != digest {
		return errors.New("digest must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("digest must be 64 lowercase hexadecimal characters")
	}
	return nil
}

type sectionReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *sectionReadCloser) Close() error { return r.closer.Close() }

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
