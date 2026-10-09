package app

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadConfigRequiresEveryDurableAndSecretPath(t *testing.T) {
	values := validConfigEnvironment()
	for _, required := range []string{
		"CUELIGHT_CONTROL_DATABASE_URL",
		"CUELIGHT_CONTROL_TLS_CERT_PATH",
		"CUELIGHT_CONTROL_TLS_KEY_PATH",
		"CUELIGHT_CONTROL_PUBLISHER_KEY_PATH",
		"CUELIGHT_CONTROL_CAS_PATH",
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
		{"zero database connections", "CUELIGHT_CONTROL_DATABASE_MAX_CONNECTIONS", "0"},
		{"too many database connections", "CUELIGHT_CONTROL_DATABASE_MAX_CONNECTIONS", "65"},
		{"idle exceeds open", "CUELIGHT_CONTROL_DATABASE_MAX_IDLE", "17"},
		{"unbounded database wait", "CUELIGHT_CONTROL_DATABASE_TIMEOUT", "31s"},
		{"short shutdown", "CUELIGHT_CONTROL_SHUTDOWN_TIMEOUT", "500ms"},
		{"large headers", "CUELIGHT_CONTROL_MAX_HEADER_BYTES", "1048577"},
		{"insecure public URL", "CUELIGHT_CONTROL_PUBLIC_URL", "http://controller.example"},
		{"bare trusted address", "CUELIGHT_CONTROL_TRUSTED_NETWORKS", "10.0.0.0/8,192.168.1.10"},
		{"malformed trusted prefix", "CUELIGHT_CONTROL_TRUSTED_NETWORKS", "10.0.0.0/33"},
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
	if config.ListenAddress != ":8443" {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadConfigParsesTrustedNetworks(t *testing.T) {
	values := validConfigEnvironment()
	values["CUELIGHT_CONTROL_TRUSTED_NETWORKS"] = " 100.64.0.0/10, fd7a:115c:a1e0::/48,10.1.2.3/8 "
	config, err := LoadConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, prefix := range config.TrustedNetworks {
		got = append(got, prefix.String())
	}
	if want := "100.64.0.0/10 fd7a:115c:a1e0::/48 10.0.0.0/8"; strings.Join(got, " ") != want {
		t.Fatalf("trusted networks = %v, want %s", got, want)
	}
	if defaults, _ := LoadConfig(mapLookup(validConfigEnvironment())); len(defaults.TrustedNetworks) != 0 {
		t.Fatalf("trusted networks enabled by default: %v", defaults.TrustedNetworks)
	}
}

func validConfigEnvironment() map[string]string {
	return map[string]string{
		"CUELIGHT_CONTROL_DATABASE_URL":       "postgres://cuelight@localhost/cuelight",
		"CUELIGHT_CONTROL_TLS_CERT_PATH":      "/run/cuelight/server.crt",
		"CUELIGHT_CONTROL_TLS_KEY_PATH":       "/run/cuelight/server.key",
		"CUELIGHT_CONTROL_PUBLISHER_KEY_PATH": "/run/cuelight/publisher.key",
		"CUELIGHT_CONTROL_CAS_PATH":           "/var/lib/cuelight/cas",
		"CUELIGHT_CONTROL_PUBLIC_URL":         "https://controller.example",
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
