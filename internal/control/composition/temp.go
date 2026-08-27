package composition

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmill/cable/internal/control/media"
)

type temporaryStore struct{ root string }

func newTemporaryStore(root string) (*temporaryStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("temporary root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &temporaryStore{root: filepath.Clean(root)}, nil
}
func (s *temporaryStore) path(id string) string { return filepath.Join(s.root, id+".partial") }
func (s *temporaryStore) WriteChunk(ctx context.Context, id string, offset, length int64, body io.Reader) (media.ChunkWrite, error) {
	if err := ctx.Err(); err != nil {
		return media.ChunkWrite{}, err
	}
	if id == "" || offset < 0 || length <= 0 {
		return media.ChunkWrite{}, errors.New("invalid temporary chunk")
	}
	file, err := os.OpenFile(s.path(id), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return media.ChunkWrite{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return media.ChunkWrite{}, err
	}
	if offset > info.Size() {
		return media.ChunkWrite{}, media.ErrOffsetMismatch
	}
	if offset < info.Size() {
		if length > info.Size()-offset {
			return media.ChunkWrite{}, media.ErrOffsetMismatch
		}
		existing := make([]byte, length)
		if _, err := file.ReadAt(existing, offset); err != nil {
			return media.ChunkWrite{}, err
		}
		incoming := make([]byte, length)
		if _, err := io.ReadFull(body, incoming); err != nil {
			return media.ChunkWrite{}, err
		}
		var extra [1]byte
		if n, _ := body.Read(extra[:]); n != 0 {
			return media.ChunkWrite{}, errors.New("chunk body is longer than declared length")
		}
		if string(existing) != string(incoming) {
			return media.ChunkWrite{}, media.ErrChunkConflict
		}
		return media.ChunkWrite{Disposition: media.ChunkIdentical, Size: info.Size()}, nil
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return media.ChunkWrite{}, err
	}
	written, err := io.CopyN(file, body, length)
	if err != nil || written != length {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return media.ChunkWrite{}, err
	}
	var extra [1]byte
	if n, _ := body.Read(extra[:]); n != 0 {
		return media.ChunkWrite{}, errors.New("chunk body is longer than declared length")
	}
	return media.ChunkWrite{Disposition: media.ChunkAppended, Size: offset + written}, nil
}
func (s *temporaryStore) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(s.path(id))
}
func (s *temporaryStore) Remove(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
