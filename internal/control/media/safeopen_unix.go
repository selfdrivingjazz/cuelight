//go:build linux || darwin

package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
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

	current, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open allowlisted root: %w", err)
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if openErr != nil {
			return nil, fmt.Errorf("open source directory component: %w", openErr)
		}
		current = next
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	unix.Close(current)
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
	if file == nil {
		unix.Close(fd)
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
	connection, err := f.file.SyscallConn()
	if err != nil {
		return SourceVersion{}, err
	}
	var stat unix.Stat_t
	var statErr error
	if err := connection.Control(func(fd uintptr) { statErr = unix.Fstat(int(fd), &stat) }); err != nil {
		return SourceVersion{}, err
	}
	if statErr != nil {
		return SourceVersion{}, fmt.Errorf("source stat: %w", statErr)
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
