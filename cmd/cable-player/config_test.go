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
	if config.ControlURL != "https://control.example/api/v1" || config.LoopbackAddress != "127.0.0.1:0" || config.Probation != 20*time.Second {
		t.Fatalf("config = %+v", config)
	}
	if len(config.Features) != 2 || len(config.AcceptedMediaTypes) != 1 {
		t.Fatalf("profile lists = %v %v", config.Features, config.AcceptedMediaTypes)
	}

	delete(values, "CABLE_PLAYER_STATE_PATH")
	_, err = loadConfig(mapLookup(values))
	var configErr *configError
	if !errors.As(err, &configErr) || configErr.Field != "CABLE_PLAYER_STATE_PATH" {
		t.Fatalf("missing state error = %v", err)
	}

	values = validEnvironment()
	values["CABLE_PLAYER_LOOPBACK_ADDR"] = "0.0.0.0:8080"
	_, err = loadConfig(mapLookup(values))
	if !errors.As(err, &configErr) || configErr.Field != "CABLE_PLAYER_LOOPBACK_ADDR" {
		t.Fatalf("non-loopback error = %v", err)
	}
}
func TestLoadConfigAcceptsAuthenticatedDesktopRendererWithoutMPV(t *testing.T) {
	values := validEnvironment()
	delete(values, "CABLE_PLAYER_MPV_SOCKET")
	values["CABLE_PLAYER_RENDERER_MODE"] = "desktop"
	values["CABLE_PLAYER_DESKTOP_RENDERER_URL"] = "http://127.0.0.1:4179"
	values["CABLE_PLAYER_DESKTOP_RENDERER_TOKEN"] = "renderer-secret"
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
	values["CABLE_PLAYER_STREAM_TTL"] = "10s"
	_, err := loadConfig(mapLookup(values))
	var configErr *configError
	if !errors.As(err, &configErr) || configErr.Field != "timing" {
		t.Fatalf("timing error = %v", err)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"CABLE_PLAYER_CONTROL_URL":               "https://control.example/api/v1",
		"CABLE_PLAYER_SERVER_CA_PATH":            "/run/cable/server-ca.pem",
		"CABLE_PLAYER_NODE_CERT_PATH":            "/run/cable/node.pem",
		"CABLE_PLAYER_NODE_KEY_PATH":             "/run/cable/node.key",
		"CABLE_PLAYER_PUBLISHER_KEY_ID":          "publisher-1",
		"CABLE_PLAYER_PUBLISHER_PUBLIC_KEY_PATH": "/run/cable/publisher.pub",
		"CABLE_PLAYER_EVENT_EPOCH":               "11111111-1111-1111-1111-111111111111",
		"CABLE_PLAYER_PROFILE_ID":                "profile-1",
		"CABLE_PLAYER_RENDERER_VERSION":          "mpv-1",
		"CABLE_PLAYER_STATE_PATH":                "/var/lib/cable/player.db",
		"CABLE_PLAYER_CAS_PATH":                  "/var/lib/cable/cas",
		"CABLE_PLAYER_MPV_SOCKET":                "/run/mpv/socket",
		"CABLE_PLAYER_ACCEPTED_MEDIA_TYPES":      "video/mp4",
		"CABLE_PLAYER_FEATURES":                  "loop, loop,rotation",
		"CABLE_PLAYER_PROBATION":                 "20s",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}
