package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/updates"
)

const downloadAttemptTimeout = 30 * time.Minute

// download fetches artifact into path, resuming from path+".partial" with a
// Range request, and installs the file only once its size and sha256 match the
// signed manifest. An interrupted transfer keeps its partial bytes.
func download(ctx context.Context, client *http.Client, source string, artifact updates.Artifact, path string) error {
	if complete, err := fileMatches(path, artifact); err != nil || complete {
		return err
	}
	partial := path + ".partial"
	file, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	offset, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	restart := func() error {
		hash.Reset()
		offset = 0
		if err := file.Truncate(0); err != nil {
			return err
		}
		_, err := file.Seek(0, io.SeekStart)
		return err
	}
	if offset > artifact.Size {
		if err := restart(); err != nil {
			return err
		}
	}
	if offset < artifact.Size {
		requestCtx, cancel := context.WithTimeout(ctx, downloadAttemptTimeout)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, source, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK:
			if err := restart(); err != nil {
				return err
			}
		case http.StatusPartialContent:
			if start, ok := contentRangeStart(response.Header.Get("Content-Range")); !ok || start != offset {
				return errors.New("server resumed at an unexpected offset")
			}
		case http.StatusRequestedRangeNotSatisfiable:
			_ = restart()
			return errors.New("server rejected the resume range")
		default:
			return fmt.Errorf("HTTP %d", response.StatusCode)
		}
		written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, artifact.Size-offset))
		offset += written
		if err != nil {
			return fmt.Errorf("interrupted after %d of %d bytes: %w", offset, artifact.Size, err)
		}
	}
	if offset != artifact.Size {
		return fmt.Errorf("received %d of %d bytes", offset, artifact.Size)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		_ = restart()
		return errors.New("sha256 does not match the signed manifest")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		return err
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		directory.Close()
	}
	return nil
}

// missingBytes is how much of artifact still has to be downloaded.
func missingBytes(path string, artifact updates.Artifact) (int64, error) {
	if info, err := os.Stat(path); err == nil && info.Size() == artifact.Size {
		return 0, nil
	}
	info, err := os.Stat(path + ".partial")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return artifact.Size, nil
		}
		return 0, err
	}
	return max(artifact.Size-info.Size(), 0), nil
}

// fileMatches reports whether path holds exactly artifact; a mismatching file
// is removed so it is downloaded again.
func fileMatches(path string, artifact updates.Artifact) (bool, error) {
	ok, err := verifyFile(path, artifact)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !ok {
		return false, os.Remove(path)
	}
	return true, nil
}

// verifyFile checks a file's size and sha256 against artifact.
func verifyFile(path string, artifact updates.Artifact) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return false, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == artifact.SHA256, nil
}

func contentRangeStart(value string) (int64, bool) {
	rest, ok := strings.CutPrefix(value, "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	return start, err == nil
}
