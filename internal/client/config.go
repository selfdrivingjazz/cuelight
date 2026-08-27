package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrInsecureConfig = errors.New("operator config permissions must be 0600")

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func DefaultConfigPath() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		return filepath.Join(root, "cable", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "cable", "config.json"), nil
}

func LoadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("read operator config: %w", err)
	}
	if info.Mode().Perm() != 0o600 {
		return Config{}, fmt.Errorf("%w: %s has mode %04o", ErrInsecureConfig, path, info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read operator config: %w", err)
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode operator config: %w", err)
	}
	if strings.TrimSpace(config.Server) == "" || strings.TrimSpace(config.Token) == "" {
		return Config{}, errors.New("operator config requires server and token")
	}
	return config, nil
}

func SaveConfig(path string, config Config) error {
	if strings.TrimSpace(config.Server) == "" || strings.TrimSpace(config.Token) == "" {
		return errors.New("operator config requires server and token")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create operator config directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return fmt.Errorf("create operator config: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure operator config: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		file.Close()
		return fmt.Errorf("write operator config: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync operator config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close operator config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace operator config: %w", err)
	}
	return nil
}
