//go:build linux

package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type OSSafeOpener struct{}

func (OSSafeOpener) Open(root, relativePath string) (SafeFile, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("safe-open root must be absolute")
	}
	if filepath.IsAbs(relativePath) {
		return nil, errors.New("safe-open path must be relative")
	}
	clean := filepath.Clean(relativePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("safe-open path escapes root")
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("safe-open path contains an invalid component")
		}
	}

	current, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open allowlisted root: %w", err)
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := syscall.Openat(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(current)
		if openErr != nil {
			return nil, fmt.Errorf("open source directory component: %w", openErr)
		}
		current = next
	}
	fd, err := syscall.Openat(current, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	syscall.Close(current)
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
	if file == nil {
		syscall.Close(fd)
		return nil, errors.New("construct source file handle")
	}
	source := &osSafeFile{file: file}
	version, err := source.Version()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !version.Mode.IsRegular() {
		file.Close()
		return nil, fmt.Errorf("source mode %s: %w", version.Mode, ErrUnsafeSource)
	}
	return source, nil
}

type osSafeFile struct {
	file *os.File
}

func (f *osSafeFile) Read(buffer []byte) (int, error) { return f.file.Read(buffer) }
func (f *osSafeFile) Close() error                    { return f.file.Close() }

func (f *osSafeFile) Version() (SourceVersion, error) {
	info, err := f.file.Stat()
	if err != nil {
		return SourceVersion{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return SourceVersion{}, errors.New("source stat lacks Unix identity")
	}
	return SourceVersion{
		Size:            info.Size(),
		Mode:            fs.FileMode(info.Mode()),
		Device:          uint64(stat.Dev),
		Inode:           stat.Ino,
		ModTimeUnixNano: info.ModTime().UnixNano(),
		ChangeUnixNano:  stat.Ctim.Nano(),
	}, nil
}
