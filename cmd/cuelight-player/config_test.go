package main

import (
	"errors"
	"testing"
	"time"
)

func TestLoadConfigValidatesRequiredAndLoopbackInputs(t *testing.T) {
	values := validEnvironment()
	config, err := loadConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.ControlURL != "https://control.example/api/v1" || config.LoopbackAddress != "127.0.0.1:0" || config.Probation != 20*time.Second || config.PresentationMode != "automatic" {
		t.Fatalf("config = %+v", config)
	}
	if len(config.Features) != 2 || len(config.AcceptedMediaTypes) != 1 {
		t.Fatalf("profile lists = %v %v", config.Features, config.AcceptedMediaTypes)
	}

	delete(values, "CUELIGHT_PLAYER_STATE_PATH")
	_, err = loadConfig(mapLookup(values))
	var configErr *configError
	if !errors.As(err, &configErr) || configErr.Field != "CUELIGHT_PLAYER_STATE_PATH" {
		t.Fatalf("missing state error = %v", err)
	}

	values = validEnvironment()
	values["CUELIGHT_PLAYER_LOOPBACK_ADDR"] = "0.0.0.0:8080"
	_, err = loadConfig(mapLookup(values))
	if !errors.As(err, &configErr) || configErr.Field != "CUELIGHT_PLAYER_LOOPBACK_ADDR" {
		t.Fatalf("non-loopback error = %v", err)
	}

	values = validEnvironment()
	values["CUELIGHT_PLAYER_PRESENTATION_MODE"] = "kiosk"
	_, err = loadConfig(mapLookup(values))
	if !errors.As(err, &configErr) || configErr.Field != "CUELIGHT_PLAYER_PRESENTATION_MODE" {
		t.Fatalf("presentation mode error = %v", err)
	}
}
func TestLoadConfigAcceptsAuthenticatedDesktopRendererWithoutMPV(t *testing.T) {
	values := validEnvironment()
	delete(values, "CUELIGHT_PLAYER_MPV_SOCKET")
	values["CUELIGHT_PLAYER_RENDERER_MODE"] = "desktop"
	values["CUELIGHT_PLAYER_DESKTOP_RENDERER_URL"] = "http://127.0.0.1:4179"
	values["CUELIGHT_PLAYER_DESKTOP_RENDERER_TOKEN"] = "renderer-secret"
	config, err := loadConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.RendererMode != "desktop" || config.MPVSocketPath != "" {
		t.Fatalf("desktop config = %+v", config)
	}
}

func TestLoadConfigRejectsUnsafeTiming(t *testing.T) {
	values := validEnvironment()
	values["CUELIGHT_PLAYER_STREAM_TTL"] = "10s"
	_, err := loadConfig(mapLookup(values))
	var configErr *configError
	if !errors.As(err, &configErr) || configErr.Field != "timing" {
		t.Fatalf("timing error = %v", err)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"CUELIGHT_PLAYER_CONTROL_URL":               "https://control.example/api/v1",
		"CUELIGHT_PLAYER_SERVER_CA_PATH":            "/run/cuelight/server-ca.pem",
		"CUELIGHT_PLAYER_NODE_CERT_PATH":            "/run/cuelight/node.pem",
		"CUELIGHT_PLAYER_NODE_KEY_PATH":             "/run/cuelight/node.key",
		"CUELIGHT_PLAYER_PUBLISHER_KEY_ID":          "publisher-1",
		"CUELIGHT_PLAYER_PUBLISHER_PUBLIC_KEY_PATH": "/run/cuelight/publisher.pub",
		"CUELIGHT_PLAYER_EVENT_EPOCH":               "11111111-1111-1111-1111-111111111111",
		"CUELIGHT_PLAYER_PROFILE_ID":                "profile-1",
		"CUELIGHT_PLAYER_RENDERER_VERSION":          "mpv-1",
		"CUELIGHT_PLAYER_STATE_PATH":                "/var/lib/cuelight/player.db",
		"CUELIGHT_PLAYER_CAS_PATH":                  "/var/lib/cuelight/cas",
		"CUELIGHT_PLAYER_MPV_SOCKET":                "/run/mpv/socket",
		"CUELIGHT_PLAYER_ACCEPTED_MEDIA_TYPES":      "video/mp4",
		"CUELIGHT_PLAYER_FEATURES":                  "loop, loop,rotation",
		"CUELIGHT_PLAYER_PROBATION":                 "20s",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}
