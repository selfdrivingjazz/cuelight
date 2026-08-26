package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

type Candidate struct {
	ReleaseDigest string
	Source        string
}

type Status struct {
	ReleaseDigest string
	Healthy       bool
	Path          string
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
	return nil
}

func (m *MPV) Start(ctx context.Context, candidate Candidate) error {
	if err := m.Prepare(ctx, candidate); err != nil {
		return err
	}
	if _, err := m.command(ctx, []any{"loadfile", candidate.Source, "replace"}); err != nil {
		return err
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
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: path == candidate.Source, Path: path}, nil
}

func (m *MPV) Commit(_ context.Context, candidate Candidate) error {
	current := m.current.Load()
	if current == nil || *current != candidate {
		return errors.New("candidate is not the current renderer state")
	}
	return nil
}

func (m *MPV) Stop(ctx context.Context) error {
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
