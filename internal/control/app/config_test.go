package app

import (
	"errors"
	"testing"
)

func TestLoadConfigRequiresEveryDurableAndSecretPath(t *testing.T) {
	values := validConfigEnvironment()
	for _, required := range []string{
		"CABLE_CONTROL_DATABASE_URL",
		"CABLE_CONTROL_TLS_CERT_PATH",
		"CABLE_CONTROL_TLS_KEY_PATH",
		"CABLE_CONTROL_PUBLISHER_KEY_PATH",
		"CABLE_CONTROL_CAS_PATH",
	} {
		t.Run(required, func(t *testing.T) {
			copy := cloneEnvironment(values)
			delete(copy, required)
			_, err := LoadConfig(mapLookup(copy))
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != required {
				t.Fatalf("error = %v, want ConfigError for %s", err, required)
			}
		})
	}
}

func TestLoadConfigRejectsOutOfBoundsValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{"zero database connections", "CABLE_CONTROL_DATABASE_MAX_CONNECTIONS", "0"},
		{"too many database connections", "CABLE_CONTROL_DATABASE_MAX_CONNECTIONS", "65"},
		{"idle exceeds open", "CABLE_CONTROL_DATABASE_MAX_IDLE", "17"},
		{"unbounded database wait", "CABLE_CONTROL_DATABASE_TIMEOUT", "31s"},
		{"short shutdown", "CABLE_CONTROL_SHUTDOWN_TIMEOUT", "500ms"},
		{"large headers", "CABLE_CONTROL_MAX_HEADER_BYTES", "1048577"},
		{"insecure public URL", "CABLE_CONTROL_PUBLIC_URL", "http://controller.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validConfigEnvironment()
			values[test.field] = test.value
			_, err := LoadConfig(mapLookup(values))
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != test.field {
				t.Fatalf("error = %v, want ConfigError for %s", err, test.field)
			}
		})
	}
}

func TestLoadConfigUsesBoundedDefaults(t *testing.T) {
	config, err := LoadConfig(mapLookup(validConfigEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if config.ListenAddress != ":8443" || config.MigrationsPath != "migrations" {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func validConfigEnvironment() map[string]string {
	return map[string]string{
		"CABLE_CONTROL_DATABASE_URL":       "postgres://cable@localhost/cable",
		"CABLE_CONTROL_TLS_CERT_PATH":      "/run/cable/server.crt",
		"CABLE_CONTROL_TLS_KEY_PATH":       "/run/cable/server.key",
		"CABLE_CONTROL_PUBLISHER_KEY_PATH": "/run/cable/publisher.key",
		"CABLE_CONTROL_CAS_PATH":           "/var/lib/cable/cas",
		"CABLE_CONTROL_PUBLIC_URL":         "https://controller.example",
	}
}

func cloneEnvironment(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
