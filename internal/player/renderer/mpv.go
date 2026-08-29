package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/jmill/cable/internal/release"
)

type RemoteDescriptor struct {
	SessionID string
	URL       string
	ShowQR    bool
	ExpiresAt time.Time
}

type Candidate struct {
	Kind                release.PresentationKind
	ReleaseDigest       string
	Source              string
	MediaType           string
	Fit                 string
	Loop                bool
	Muted               bool
	Volume              int
	ImageDurationSecond int
	Orientation         int

	// Cable candidates are materialized by the Chromium adapter. Keeping the
	// immutable closure on the candidate lets the composite renderer dispatch
	// without carrying presentation-specific side channels.
	ShellBuildDigest       string
	RevisionArtifactDigest string
	Entrypoint             string
	BridgeProtocol         string
	ArtifactClosure        []string
	ShellPath              string
	RevisionPath           string
	ArtifactPaths          map[string]string
	Remote                 *RemoteDescriptor
}

func sameCandidate(a, b Candidate) bool { return reflect.DeepEqual(a, b) }

type Status struct {
	ReleaseDigest string
	Healthy       bool
	Path          string
	Candidate     Candidate
}

type Adapter interface {
	Prepare(context.Context, Candidate) error
	Start(context.Context, Candidate) error
	Health(context.Context, Candidate) (Status, error)
	Commit(context.Context, Candidate) error
	Stop(context.Context) error
	Current(context.Context) (Status, error)
}

type MPV struct {
	SocketPath string
	Timeout    time.Duration
	requestID  atomic.Uint64
	current    atomic.Pointer[Candidate]
}

type mpvResponse struct {
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	RequestID uint64          `json:"request_id"`
}

func (m *MPV) Prepare(_ context.Context, candidate Candidate) error {
	if candidate.ReleaseDigest == "" || candidate.Source == "" {
		return errors.New("release digest and source are required")
	}
	if candidate.Fit != "fit" && candidate.Fit != "fill" {
		return errors.New("fit must be fit or fill")
	}
	if candidate.Volume < 0 || candidate.Volume > 100 || candidate.ImageDurationSecond < 0 {
		return errors.New("volume and image duration are invalid")
	}
	switch candidate.Orientation {
	case 0, 90, 180, 270:
	default:
		return errors.New("orientation must be 0, 90, 180, or 270")
	}
	return nil
}

func (m *MPV) Start(ctx context.Context, candidate Candidate) error {
	if err := m.Prepare(ctx, candidate); err != nil {
		return err
	}
	if _, err := m.command(ctx, []any{"loadfile", candidate.Source, "replace"}); err != nil {
		return err
	}
	for _, property := range candidateProperties(candidate) {
		if _, err := m.command(ctx, []any{"set_property", property.name, property.value}); err != nil {
			return fmt.Errorf("set mpv property %s: %w", property.name, err)
		}
	}
	copy := candidate
	m.current.Store(&copy)
	return nil
}

func (m *MPV) Health(ctx context.Context, candidate Candidate) (Status, error) {
	response, err := m.command(ctx, []any{"get_property", "path"})
	if err != nil {
		return Status{}, err
	}
	var path string
	if err := json.Unmarshal(response.Data, &path); err != nil {
		return Status{}, fmt.Errorf("decode mpv path: %w", err)
	}
	healthy := path == candidate.Source
	for _, property := range candidateProperties(candidate) {
		response, err := m.command(ctx, []any{"get_property", property.name})
		if err != nil {
			return Status{}, err
		}
		if !sameJSONValue(response.Data, property.value) {
			healthy = false
		}
	}
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: healthy, Path: path, Candidate: candidate}, nil
}

func (m *MPV) Commit(_ context.Context, candidate Candidate) error {
	current := m.current.Load()
	if current == nil || !sameCandidate(*current, candidate) {
		return errors.New("candidate is not the current renderer state")
	}
	return nil
}

func (m *MPV) Stop(ctx context.Context) error {
	if m.current.Load() == nil {
		return nil
	}
	if _, err := m.command(ctx, []any{"stop"}); err != nil {
		return err
	}
	m.current.Store(nil)
	return nil
}

func (m *MPV) Current(ctx context.Context) (Status, error) {
	current := m.current.Load()
	if current == nil {
		return Status{}, nil
	}
	return m.Health(ctx, *current)
}

type candidateProperty struct {
	name  string
	value any
}

func candidateProperties(candidate Candidate) []candidateProperty {
	panscan := 0
	if candidate.Fit == "fill" {
		panscan = 1
	}
	loop := "no"
	if candidate.Loop {
		loop = "inf"
	}
	return []candidateProperty{
		{name: "panscan", value: panscan},
		{name: "loop-file", value: loop},
		{name: "mute", value: candidate.Muted},
		{name: "volume", value: candidate.Volume},
		{name: "image-display-duration", value: candidate.ImageDurationSecond},
		{name: "video-rotate", value: candidate.Orientation},
	}
}

func sameJSONValue(encoded json.RawMessage, expected any) bool {
	var actual any
	if json.Unmarshal(encoded, &actual) != nil {
		return false
	}
	normalized, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	var normalizedExpected any
	if json.Unmarshal(normalized, &normalizedExpected) != nil {
		return false
	}
	return fmt.Sprint(actual) == fmt.Sprint(normalizedExpected)
}

func (m *MPV) command(ctx context.Context, command []any) (mpvResponse, error) {
	if m.SocketPath == "" {
		return mpvResponse{}, errors.New("mpv socket path is required")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", m.SocketPath)
	if err != nil {
		return mpvResponse{}, fmt.Errorf("connect mpv IPC: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return mpvResponse{}, err
	}
	requestID := m.requestID.Add(1)
	request := struct {
		Command   []any  `json:"command"`
		RequestID uint64 `json:"request_id"`
	}{Command: command, RequestID: requestID}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return mpvResponse{}, fmt.Errorf("write mpv IPC: %w", err)
	}
	var response mpvResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return mpvResponse{}, fmt.Errorf("read mpv IPC: %w", err)
	}
	if response.RequestID != requestID {
		return mpvResponse{}, errors.New("mpv response request ID mismatch")
	}
	if response.Error != "success" {
		return mpvResponse{}, fmt.Errorf("mpv command failed: %s", response.Error)
	}
	return response, nil
}
