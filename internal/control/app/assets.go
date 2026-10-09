package app

import (
	"crypto/tls"
	"fmt"
	"os"
)

type StartupAssets struct {
	TLSCertificate tls.Certificate
}

type PathError struct {
	Kind   string
	Path   string
	Reason string
}

func (err *PathError) Error() string {
	return fmt.Sprintf("%s path %q: %s", err.Kind, err.Path, err.Reason)
}

func LoadStartupAssets(config Config) (StartupAssets, error) {
	if err := validatePublicFile("TLS certificate", config.TLSCertificatePath); err != nil {
		return StartupAssets{}, err
	}
	if err := validatePrivateFile("TLS private key", config.TLSPrivateKeyPath); err != nil {
		return StartupAssets{}, err
	}
	if err := validatePrivateFile("publisher private key", config.PublisherPrivateKeyPath); err != nil {
		return StartupAssets{}, err
	}
	if err := validateCASDirectory(config.CASPath); err != nil {
		return StartupAssets{}, err
	}
	certificate, err := tls.LoadX509KeyPair(config.TLSCertificatePath, config.TLSPrivateKeyPath)
	if err != nil {
		return StartupAssets{}, &PathError{Kind: "TLS certificate/key", Path: config.TLSCertificatePath, Reason: err.Error()}
	}
	return StartupAssets{TLSCertificate: certificate}, nil
}

func validatePublicFile(kind, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return &PathError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if !info.Mode().IsRegular() {
		return &PathError{Kind: kind, Path: path, Reason: "must be a regular file, not a symlink or special file"}
	}
	if info.Mode().Perm()&0o022 != 0 {
		return &PathError{Kind: kind, Path: path, Reason: "must not be writable by group or other users"}
	}
	file, err := os.Open(path)
	if err != nil {
		return &PathError{Kind: kind, Path: path, Reason: err.Error()}
	}
	return file.Close()
}

func validatePrivateFile(kind, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return &PathError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if !info.Mode().IsRegular() {
		return &PathError{Kind: kind, Path: path, Reason: "must be a regular file, not a symlink or special file"}
	}
	permissions := info.Mode().Perm()
	if permissions != 0o400 && permissions != 0o600 {
		return &PathError{Kind: kind, Path: path, Reason: "permissions must be 0400 or 0600"}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return &PathError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if len(content) == 0 {
		return &PathError{Kind: kind, Path: path, Reason: "must not be empty"}
	}
	return nil
}

func validateCASDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return &PathError{Kind: "CAS", Path: path, Reason: err.Error()}
	}
	if !info.IsDir() {
		return &PathError{Kind: "CAS", Path: path, Reason: "must be a directory, not a symlink or file"}
	}
	permissions := info.Mode().Perm()
	if permissions&0o700 != 0o700 {
		return &PathError{Kind: "CAS", Path: path, Reason: "must be readable, writable, and searchable by its owner"}
	}
	if permissions&0o022 != 0 {
		return &PathError{Kind: "CAS", Path: path, Reason: "must not be writable by group or other users"}
	}
	return nil
}
