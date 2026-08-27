package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddress       = ":8443"
	defaultMigrationsPath      = "migrations"
	defaultDatabaseTimeout     = 10 * time.Second
	defaultDatabaseConnections = 16
	defaultDatabaseIdle        = 4
	defaultConnectionLifetime  = 30 * time.Minute
	defaultReadHeaderTimeout   = 10 * time.Second
	defaultIdleTimeout         = 60 * time.Second
	defaultShutdownTimeout     = 15 * time.Second
	defaultMaxHeaderBytes      = 64 << 10
)

type Config struct {
	ListenAddress           string
	DatabaseURL             string
	MigrationsPath          string
	TLSCertificatePath      string
	TLSPrivateKeyPath       string
	PublisherPrivateKeyPath string
	CASPath                 string
	DatabaseTimeout         time.Duration
	DatabaseMaxConnections  int
	DatabaseMaxIdle         int
	DatabaseConnLifetime    time.Duration
	ReadHeaderTimeout       time.Duration
	IdleTimeout             time.Duration
	ShutdownTimeout         time.Duration
	MaxHeaderBytes          int
}

type ConfigError struct {
	Field  string
	Reason string
}

func (err *ConfigError) Error() string {
	return fmt.Sprintf("configuration %s: %s", err.Field, err.Reason)
}

func LoadConfig(lookup func(string) (string, bool)) (Config, error) {
	config := Config{
		ListenAddress:           envOrDefault(lookup, "CABLE_CONTROL_LISTEN_ADDR", defaultListenAddress),
		DatabaseURL:             env(lookup, "CABLE_CONTROL_DATABASE_URL"),
		MigrationsPath:          envOrDefault(lookup, "CABLE_CONTROL_MIGRATIONS_PATH", defaultMigrationsPath),
		TLSCertificatePath:      env(lookup, "CABLE_CONTROL_TLS_CERT_PATH"),
		TLSPrivateKeyPath:       env(lookup, "CABLE_CONTROL_TLS_KEY_PATH"),
		PublisherPrivateKeyPath: env(lookup, "CABLE_CONTROL_PUBLISHER_KEY_PATH"),
		CASPath:                 env(lookup, "CABLE_CONTROL_CAS_PATH"),
		DatabaseTimeout:         defaultDatabaseTimeout,
		DatabaseMaxConnections:  defaultDatabaseConnections,
		DatabaseMaxIdle:         defaultDatabaseIdle,
		DatabaseConnLifetime:    defaultConnectionLifetime,
		ReadHeaderTimeout:       defaultReadHeaderTimeout,
		IdleTimeout:             defaultIdleTimeout,
		ShutdownTimeout:         defaultShutdownTimeout,
		MaxHeaderBytes:          defaultMaxHeaderBytes,
	}
	var err error
	if config.DatabaseTimeout, err = durationEnv(lookup, "CABLE_CONTROL_DATABASE_TIMEOUT", config.DatabaseTimeout); err != nil {
		return Config{}, err
	}
	if config.DatabaseMaxConnections, err = intEnv(lookup, "CABLE_CONTROL_DATABASE_MAX_CONNECTIONS", config.DatabaseMaxConnections); err != nil {
		return Config{}, err
	}
	if config.DatabaseMaxIdle, err = intEnv(lookup, "CABLE_CONTROL_DATABASE_MAX_IDLE", config.DatabaseMaxIdle); err != nil {
		return Config{}, err
	}
	if config.DatabaseConnLifetime, err = durationEnv(lookup, "CABLE_CONTROL_DATABASE_CONN_LIFETIME", config.DatabaseConnLifetime); err != nil {
		return Config{}, err
	}
	if config.ReadHeaderTimeout, err = durationEnv(lookup, "CABLE_CONTROL_READ_HEADER_TIMEOUT", config.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if config.IdleTimeout, err = durationEnv(lookup, "CABLE_CONTROL_IDLE_TIMEOUT", config.IdleTimeout); err != nil {
		return Config{}, err
	}
	if config.ShutdownTimeout, err = durationEnv(lookup, "CABLE_CONTROL_SHUTDOWN_TIMEOUT", config.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if config.MaxHeaderBytes, err = intEnv(lookup, "CABLE_CONTROL_MAX_HEADER_BYTES", config.MaxHeaderBytes); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	required := []struct {
		name  string
		value string
	}{
		{"CABLE_CONTROL_DATABASE_URL", config.DatabaseURL},
		{"CABLE_CONTROL_MIGRATIONS_PATH", config.MigrationsPath},
		{"CABLE_CONTROL_TLS_CERT_PATH", config.TLSCertificatePath},
		{"CABLE_CONTROL_TLS_KEY_PATH", config.TLSPrivateKeyPath},
		{"CABLE_CONTROL_PUBLISHER_KEY_PATH", config.PublisherPrivateKeyPath},
		{"CABLE_CONTROL_CAS_PATH", config.CASPath},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return &ConfigError{Field: field.name, Reason: "is required"}
		}
	}
	if config.TLSCertificatePath == config.TLSPrivateKeyPath {
		return &ConfigError{Field: "CABLE_CONTROL_TLS_KEY_PATH", Reason: "must differ from the certificate path"}
	}
	if config.PublisherPrivateKeyPath == config.TLSPrivateKeyPath || config.PublisherPrivateKeyPath == config.TLSCertificatePath {
		return &ConfigError{Field: "CABLE_CONTROL_PUBLISHER_KEY_PATH", Reason: "must differ from the TLS certificate and private key paths"}
	}
	if _, _, err := net.SplitHostPort(config.ListenAddress); err != nil {
		return &ConfigError{Field: "CABLE_CONTROL_LISTEN_ADDR", Reason: "must be a host:port address"}
	}
	if err := boundedDuration("CABLE_CONTROL_DATABASE_TIMEOUT", config.DatabaseTimeout, time.Second, 30*time.Second); err != nil {
		return err
	}
	if config.DatabaseMaxConnections < 1 || config.DatabaseMaxConnections > 64 {
		return &ConfigError{Field: "CABLE_CONTROL_DATABASE_MAX_CONNECTIONS", Reason: "must be between 1 and 64"}
	}
	if config.DatabaseMaxIdle < 0 || config.DatabaseMaxIdle > config.DatabaseMaxConnections {
		return &ConfigError{Field: "CABLE_CONTROL_DATABASE_MAX_IDLE", Reason: "must be between 0 and database max connections"}
	}
	if err := boundedDuration("CABLE_CONTROL_DATABASE_CONN_LIFETIME", config.DatabaseConnLifetime, time.Minute, 24*time.Hour); err != nil {
		return err
	}
	if err := boundedDuration("CABLE_CONTROL_READ_HEADER_TIMEOUT", config.ReadHeaderTimeout, time.Second, 30*time.Second); err != nil {
		return err
	}
	if err := boundedDuration("CABLE_CONTROL_IDLE_TIMEOUT", config.IdleTimeout, 5*time.Second, 5*time.Minute); err != nil {
		return err
	}
	if err := boundedDuration("CABLE_CONTROL_SHUTDOWN_TIMEOUT", config.ShutdownTimeout, time.Second, time.Minute); err != nil {
		return err
	}
	if config.MaxHeaderBytes < 4<<10 || config.MaxHeaderBytes > 1<<20 {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_HEADER_BYTES", Reason: "must be between 4096 and 1048576"}
	}
	return nil
}

func env(lookup func(string) (string, bool), name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}

func envOrDefault(lookup func(string) (string, bool), name, fallback string) string {
	if value := env(lookup, name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(lookup func(string) (string, bool), name string, fallback time.Duration) (time.Duration, error) {
	value := env(lookup, name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, &ConfigError{Field: name, Reason: "must be a duration such as 10s or 5m"}
	}
	return parsed, nil
}

func intEnv(lookup func(string) (string, bool), name string, fallback int) (int, error) {
	value := env(lookup, name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, &ConfigError{Field: name, Reason: "must be an integer"}
	}
	return parsed, nil
}

func boundedDuration(field string, value, minimum, maximum time.Duration) error {
	if value < minimum || value > maximum {
		return &ConfigError{Field: field, Reason: fmt.Sprintf("must be between %s and %s", minimum, maximum)}
	}
	return nil
}
