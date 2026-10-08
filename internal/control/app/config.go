package app

import (
	"fmt"
	"net"
	"net/url"
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
	defaultMaxBodyBytes        = 4 << 20
	defaultMaxUploadBytes      = 8 << 30
	defaultMaxChunkBytes       = 16 << 20
	defaultMaxLocalMediaBytes  = 8 << 30
	defaultMaxEvents           = 100
	defaultMaxWait             = 30 * time.Second
)

type Config struct {
	ListenAddress           string
	DatabaseURL             string
	MigrationsPath          string
	TLSCertificatePath      string
	TLSPrivateKeyPath       string
	PublisherPrivateKeyPath string
	PublisherKeyID          string
	CASPath                 string
	CAPath                  string
	CAKeyPath               string
	SealKeyPath             string
	TemporaryRoot           string
	MediaRoots              []string
	ProtocolVersion         string
	PlaybackProfile         string
	PublicURL               string
	// WebNodeDir is the built website-node app served at /node/; empty disables it.
	WebNodeDir string
	// CableWebDir is the built cable-web guide served at /cable/; empty disables it.
	CableWebDir string
	// WebCaptureBrowser is a headless Chromium binary used to capture web
	// library thumbnails; empty auto-detects one on PATH.
	WebCaptureBrowser      string
	DatabaseTimeout        time.Duration
	DatabaseMaxConnections int
	DatabaseMaxIdle        int
	DatabaseConnLifetime   time.Duration
	ReadHeaderTimeout      time.Duration
	IdleTimeout            time.Duration
	ShutdownTimeout        time.Duration
	MaxHeaderBytes         int
	MaxBodyBytes           int64
	MaxUploadBytes         int64
	MaxChunkBytes          int64
	MaxLocalMediaBytes     int64
	MaxEvents              int
	MaxWait                time.Duration
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
		PublisherKeyID:          env(lookup, "CABLE_CONTROL_PUBLISHER_KEY_ID"),
		CASPath:                 env(lookup, "CABLE_CONTROL_CAS_PATH"),
		CAPath:                  env(lookup, "CABLE_CONTROL_CA_PATH"),
		CAKeyPath:               env(lookup, "CABLE_CONTROL_CA_KEY_PATH"),
		SealKeyPath:             env(lookup, "CABLE_CONTROL_SEAL_KEY_PATH"),
		TemporaryRoot:           env(lookup, "CABLE_CONTROL_TEMP_ROOT"),
		ProtocolVersion:         envOrDefault(lookup, "CABLE_CONTROL_PROTOCOL_VERSION", "1.0"),
		PlaybackProfile:         envOrDefault(lookup, "CABLE_CONTROL_PLAYBACK_PROFILE", "default"),
		PublicURL:               env(lookup, "CABLE_CONTROL_PUBLIC_URL"),
		WebNodeDir:              env(lookup, "CABLE_CONTROL_WEB_NODE_DIR"),
		CableWebDir:             env(lookup, "CABLE_CONTROL_CABLE_WEB_DIR"),
		WebCaptureBrowser:       env(lookup, "CABLE_CONTROL_WEB_CAPTURE_BROWSER"),
		DatabaseTimeout:         defaultDatabaseTimeout,
		DatabaseMaxConnections:  defaultDatabaseConnections,
		DatabaseMaxIdle:         defaultDatabaseIdle,
		DatabaseConnLifetime:    defaultConnectionLifetime,
		ReadHeaderTimeout:       defaultReadHeaderTimeout,
		IdleTimeout:             defaultIdleTimeout,
		ShutdownTimeout:         defaultShutdownTimeout,
		MaxHeaderBytes:          defaultMaxHeaderBytes,
		MaxBodyBytes:            defaultMaxBodyBytes,
		MaxUploadBytes:          defaultMaxUploadBytes,
		MaxChunkBytes:           defaultMaxChunkBytes,
		MaxLocalMediaBytes:      defaultMaxLocalMediaBytes,
		MaxEvents:               defaultMaxEvents,
		MaxWait:                 defaultMaxWait,
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
	if config.MaxBodyBytes, err = int64Env(lookup, "CABLE_CONTROL_MAX_BODY_BYTES", config.MaxBodyBytes); err != nil {
		return Config{}, err
	}
	if config.MaxUploadBytes, err = int64Env(lookup, "CABLE_CONTROL_MAX_UPLOAD_BYTES", config.MaxUploadBytes); err != nil {
		return Config{}, err
	}
	if config.MaxChunkBytes, err = int64Env(lookup, "CABLE_CONTROL_MAX_CHUNK_BYTES", config.MaxChunkBytes); err != nil {
		return Config{}, err
	}
	if config.MaxLocalMediaBytes, err = int64Env(lookup, "CABLE_CONTROL_MAX_LOCAL_MEDIA_BYTES", config.MaxLocalMediaBytes); err != nil {
		return Config{}, err
	}
	if config.MaxEvents, err = intEnv(lookup, "CABLE_CONTROL_MAX_EVENTS", config.MaxEvents); err != nil {
		return Config{}, err
	}
	if config.MaxWait, err = durationEnv(lookup, "CABLE_CONTROL_MAX_WAIT", config.MaxWait); err != nil {
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
	if config.MaxBodyBytes < 1<<10 || config.MaxBodyBytes > 64<<20 {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_BODY_BYTES", Reason: "must be between 1024 and 67108864"}
	}
	if config.MaxUploadBytes <= 0 || config.MaxUploadBytes > 1<<40 {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_UPLOAD_BYTES", Reason: "must be between 1 and 1099511627776"}
	}
	if config.MaxChunkBytes <= 0 || config.MaxChunkBytes > config.MaxUploadBytes {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_CHUNK_BYTES", Reason: "must be positive and no greater than max upload bytes"}
	}
	if config.MaxLocalMediaBytes <= 0 || config.MaxLocalMediaBytes > config.MaxUploadBytes {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_LOCAL_MEDIA_BYTES", Reason: "must be positive and no greater than max upload bytes"}
	}
	if config.MaxEvents < 1 || config.MaxEvents > 10000 {
		return &ConfigError{Field: "CABLE_CONTROL_MAX_EVENTS", Reason: "must be between 1 and 10000"}
	}
	if err := boundedDuration("CABLE_CONTROL_MAX_WAIT", config.MaxWait, 0, 5*time.Minute); err != nil {
		return err
	}
	if strings.TrimSpace(config.ProtocolVersion) == "" || strings.TrimSpace(config.PlaybackProfile) == "" {
		return &ConfigError{Field: "CABLE_CONTROL_PROTOCOL_VERSION", Reason: "protocol version and playback profile are required"}
	}
	if config.PublicURL != "" {
		publicURL, err := url.Parse(config.PublicURL)
		if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" {
			return &ConfigError{Field: "CABLE_CONTROL_PUBLIC_URL", Reason: "must be an absolute HTTPS URL without credentials, query, or fragment"}
		}
	}
	return nil
}

func csvEnv(lookup func(string) (string, bool), name string) []string {
	raw := env(lookup, name)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
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
func int64Env(lookup func(string) (string, bool), name string, fallback int64) (int64, error) {
	value := env(lookup, name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
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
