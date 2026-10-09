package main

import (
	"fmt"
	"net"
	"net/url"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	playerruntime "github.com/jmilldotdev/cuelight/internal/player/runtime"
	"github.com/jmilldotdev/cuelight/internal/release"
)

type playerConfig struct {
	ControlURL             string
	NodeID                 string
	ServerCAPath           string
	NodeCertificatePath    string
	NodePrivateKeyPath     string
	PublisherKeyID         string
	PublisherPublicKeyPath string
	EventEpoch             string
	ProfileID              string
	RendererVersion        string
	StatePath              string
	CASPath                string
	RendererMode           string
	DesktopRendererURL     string
	DesktopRendererToken   string
	MPVSocketPath          string
	ChromiumBinaryPath     string
	ChromiumRuntimeRoot    string
	ChromiumArgs           []string
	LoopbackAddress        string
	Features               []string
	AcceptedMediaTypes     []string
	PresentationMode       release.PresentationKind
	Probation              time.Duration
	PollWait               time.Duration
	ReconnectMinimum       time.Duration
	ReconnectMaximum       time.Duration
	PartialGrace           time.Duration
	StreamTTL              time.Duration
	MPVTimeout             time.Duration
	ChromiumTimeout        time.Duration
}

type configError struct{ Field, Reason string }

func (e *configError) Error() string { return fmt.Sprintf("configuration %s: %s", e.Field, e.Reason) }

func loadConfig(lookup func(string) (string, bool)) (playerConfig, error) {
	config := playerConfig{
		ControlURL: env(lookup, "CUELIGHT_PLAYER_CONTROL_URL"), NodeID: env(lookup, "CUELIGHT_PLAYER_NODE_ID"), ServerCAPath: env(lookup, "CUELIGHT_PLAYER_SERVER_CA_PATH"),
		NodeCertificatePath: env(lookup, "CUELIGHT_PLAYER_NODE_CERT_PATH"), NodePrivateKeyPath: env(lookup, "CUELIGHT_PLAYER_NODE_KEY_PATH"),
		PublisherKeyID: env(lookup, "CUELIGHT_PLAYER_PUBLISHER_KEY_ID"), PublisherPublicKeyPath: env(lookup, "CUELIGHT_PLAYER_PUBLISHER_PUBLIC_KEY_PATH"),
		EventEpoch: env(lookup, "CUELIGHT_PLAYER_EVENT_EPOCH"), ProfileID: env(lookup, "CUELIGHT_PLAYER_PROFILE_ID"), RendererVersion: env(lookup, "CUELIGHT_PLAYER_RENDERER_VERSION"),
		StatePath: env(lookup, "CUELIGHT_PLAYER_STATE_PATH"), CASPath: env(lookup, "CUELIGHT_PLAYER_CAS_PATH"), MPVSocketPath: env(lookup, "CUELIGHT_PLAYER_MPV_SOCKET"),
		RendererMode: envDefault(lookup, "CUELIGHT_PLAYER_RENDERER_MODE", "appliance"), DesktopRendererURL: env(lookup, "CUELIGHT_PLAYER_DESKTOP_RENDERER_URL"),
		DesktopRendererToken: env(lookup, "CUELIGHT_PLAYER_DESKTOP_RENDERER_TOKEN"), ChromiumBinaryPath: envDefault(lookup, "CUELIGHT_PLAYER_CHROMIUM_BINARY", "chromium"),
		ChromiumRuntimeRoot: envDefault(lookup, "CUELIGHT_PLAYER_CHROMIUM_RUNTIME_ROOT", "/var/lib/cuelight/chromium"),
		ChromiumArgs:        listEnv(lookup, "CUELIGHT_PLAYER_CHROMIUM_ARGS"), LoopbackAddress: envDefault(lookup, "CUELIGHT_PLAYER_LOOPBACK_ADDR", "127.0.0.1:0"),
		Features: listEnv(lookup, "CUELIGHT_PLAYER_FEATURES"), AcceptedMediaTypes: listEnv(lookup, "CUELIGHT_PLAYER_ACCEPTED_MEDIA_TYPES"),
		PresentationMode: release.PresentationKind(envDefault(lookup, "CUELIGHT_PLAYER_PRESENTATION_MODE", "automatic")),
		Probation:        30 * time.Second, PollWait: 30 * time.Second, ReconnectMinimum: time.Second, ReconnectMaximum: 30 * time.Second,
		PartialGrace: time.Hour, StreamTTL: 10 * time.Minute, MPVTimeout: 5 * time.Second, ChromiumTimeout: 15 * time.Second,
	}
	var err error
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{"CUELIGHT_PLAYER_PROBATION", &config.Probation}, {"CUELIGHT_PLAYER_POLL_WAIT", &config.PollWait},
		{"CUELIGHT_PLAYER_RECONNECT_MIN", &config.ReconnectMinimum}, {"CUELIGHT_PLAYER_RECONNECT_MAX", &config.ReconnectMaximum},
		{"CUELIGHT_PLAYER_PARTIAL_GRACE", &config.PartialGrace}, {"CUELIGHT_PLAYER_STREAM_TTL", &config.StreamTTL},
		{"CUELIGHT_PLAYER_MPV_TIMEOUT", &config.MPVTimeout}, {"CUELIGHT_PLAYER_CHROMIUM_TIMEOUT", &config.ChromiumTimeout},
	} {
		if *item.target, err = durationEnv(lookup, item.name, *item.target); err != nil {
			return playerConfig{}, err
		}
	}
	if err := config.validate(); err != nil {
		return playerConfig{}, err
	}
	return config, nil
}

func (c playerConfig) validate() error {
	required := []struct{ name, value string }{
		{"CUELIGHT_PLAYER_CONTROL_URL", c.ControlURL}, {"CUELIGHT_PLAYER_SERVER_CA_PATH", c.ServerCAPath},
		{"CUELIGHT_PLAYER_NODE_CERT_PATH", c.NodeCertificatePath}, {"CUELIGHT_PLAYER_NODE_KEY_PATH", c.NodePrivateKeyPath},
		{"CUELIGHT_PLAYER_PUBLISHER_KEY_ID", c.PublisherKeyID}, {"CUELIGHT_PLAYER_PUBLISHER_PUBLIC_KEY_PATH", c.PublisherPublicKeyPath},
		{"CUELIGHT_PLAYER_EVENT_EPOCH", c.EventEpoch}, {"CUELIGHT_PLAYER_PROFILE_ID", c.ProfileID}, {"CUELIGHT_PLAYER_RENDERER_VERSION", c.RendererVersion},
		{"CUELIGHT_PLAYER_STATE_PATH", c.StatePath}, {"CUELIGHT_PLAYER_CAS_PATH", c.CASPath},
	}
	for _, field := range required {
		if field.value == "" {
			return &configError{field.name, "is required"}
		}
	}
	if c.RendererMode != "appliance" && c.RendererMode != "desktop" && c.RendererMode != "mpv-only" {
		return &configError{"CUELIGHT_PLAYER_RENDERER_MODE", "must be appliance, desktop, or mpv-only"}
	}
	if (c.RendererMode == "appliance" || c.RendererMode == "mpv-only") && c.MPVSocketPath == "" {
		return &configError{"CUELIGHT_PLAYER_MPV_SOCKET", "is required in appliance or mpv-only mode"}
	}
	if c.RendererMode == "desktop" && (c.DesktopRendererURL == "" || c.DesktopRendererToken == "") {
		return &configError{"desktop renderer", "URL and token are required in desktop mode"}
	}
	parsed, err := url.Parse(c.ControlURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return &configError{"CUELIGHT_PLAYER_CONTROL_URL", "must be an absolute HTTPS URL without credentials, query, or fragment"}
	}
	host, _, err := net.SplitHostPort(c.LoopbackAddress)
	if err != nil {
		return &configError{"CUELIGHT_PLAYER_LOOPBACK_ADDR", "must be an IP host:port address"}
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return &configError{"CUELIGHT_PLAYER_LOOPBACK_ADDR", "must bind a loopback IP address"}
	}
	if len(c.AcceptedMediaTypes) == 0 {
		return &configError{"CUELIGHT_PLAYER_ACCEPTED_MEDIA_TYPES", "must contain at least one media type"}
	}
	if c.PresentationMode != "automatic" && c.PresentationMode != release.PresentationMedia && c.PresentationMode != release.PresentationMediaSchedule && c.PresentationMode != release.PresentationWeb {
		return &configError{"CUELIGHT_PLAYER_PRESENTATION_MODE", "must be automatic, media, media-schedule, or web"}
	}
	if c.Probation <= 0 || c.PollWait < 0 || c.ReconnectMinimum <= 0 || c.ReconnectMaximum < c.ReconnectMinimum || c.PartialGrace < 0 || c.StreamTTL <= c.Probation || c.MPVTimeout <= 0 || c.ChromiumTimeout <= 0 {
		return &configError{"timing", "durations must be positive, reconnect max must be at least min, and stream TTL must exceed probation"}
	}
	if c.NodeCertificatePath == c.NodePrivateKeyPath {
		return &configError{"CUELIGHT_PLAYER_NODE_KEY_PATH", "must differ from the certificate path"}
	}
	return nil
}

func (c playerConfig) runtimeConfig(publisherKey []byte) playerruntime.Config {
	return playerruntime.Config{
		NodeID: c.NodeID, Capabilities: capabilityReport(c), EventEpoch: c.EventEpoch, PublisherKeyID: c.PublisherKeyID, PublisherPublicKey: publisherKey,
		Features: c.Features, AcceptedMediaTypes: c.AcceptedMediaTypes, Probation: c.Probation, PollWait: c.PollWait,
		ReconnectMinimum: c.ReconnectMinimum, ReconnectMaximum: c.ReconnectMaximum, PartialGrace: c.PartialGrace, StreamTTL: c.StreamTTL,
		PresentationMode: c.runtimePresentationMode(),
		MaxEvents:        256, MaxEventBatch: 32, MaxConditions: 32, MaxMessageRunes: 256,
	}
}
func (c playerConfig) runtimePresentationMode() release.PresentationKind {
	if c.PresentationMode == "automatic" {
		return ""
	}
	return c.PresentationMode
}

func capabilityReport(c playerConfig) playerclient.CapabilityReport {
	return playerclient.CapabilityReport{Platform: goruntime.GOOS, Architecture: goruntime.GOARCH, ProtocolVersion: release.ProtocolV1, RendererVersion: c.RendererVersion, ProfileID: c.ProfileID, Features: slices.Clone(c.Features), AcceptedMediaTypes: slices.Clone(c.AcceptedMediaTypes), OutputReady: true, ClockReliable: true}
}

func env(lookup func(string) (string, bool), name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}
func envDefault(lookup func(string) (string, bool), name, fallback string) string {
	if value := env(lookup, name); value != "" {
		return value
	}
	return fallback
}
func listEnv(lookup func(string) (string, bool), name string) []string {
	values := strings.Split(env(lookup, name), ",")
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
func durationEnv(lookup func(string) (string, bool), name string, fallback time.Duration) (time.Duration, error) {
	value := env(lookup, name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, &configError{name, "must be a Go duration such as 10s or 5m"}
	}
	return parsed, nil
}
