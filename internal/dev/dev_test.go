package dev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeProcess struct {
	mu    sync.Mutex
	pid   int
	alive bool
	done  chan struct{}
}

func (p *fakeProcess) PID() int    { return p.pid }
func (p *fakeProcess) Alive() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.alive }
func (p *fakeProcess) Signal(signal os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if (signal == syscall.SIGTERM || signal == syscall.SIGKILL) && p.alive {
		p.alive = false
		close(p.done)
	}
	return nil
}
func (p *fakeProcess) Wait() error { <-p.done; return nil }

type fakeProcesses struct{ started []*fakeProcess }

func (p *fakeProcesses) Start(context.Context, ProcessSpec) (Process, error) {
	result := &fakeProcess{pid: 100 + len(p.started), alive: true, done: make(chan struct{})}
	p.started = append(p.started, result)
	return result, nil
}

type fakeCommands struct{ calls int }

func (c *fakeCommands) Run(_ context.Context, _ string, _ []string, _ []string, stdout, _ io.Writer) (int, error) {
	c.calls++
	_, err := io.WriteString(stdout, `{"secret":"admin-secret"}`)
	return 0, err
}

type fakeContainers struct{ stopped int }

func (c *fakeContainers) Ensure(context.Context, ContainerSpec) (ContainerInfo, error) {
	return ContainerInfo{ID: "container-1", Endpoint: "postgres://cable:cable-development@127.0.0.1:5433/cable", Owned: true}, nil
}
func (c *fakeContainers) Stop(context.Context, ContainerInfo) error { c.stopped++; return nil }

func testManager(t *testing.T, root string, processes *fakeProcesses, commands *fakeCommands, containers *fakeContainers) *Manager {
	t.Helper()
	manager, err := New(Config{Root: root, ReadinessTimeout: time.Second, ShutdownTimeout: time.Second}, Options{
		Processes: processes, Commands: commands, Containers: containers,
		Probe:         func(context.Context, string, string) error { return nil },
		PostgresProbe: func(context.Context, string) error { return nil },
		Enrollment:    func(context.Context, string, string) (string, error) { return "enrollment-secret", nil },
		Random:        bytes.NewReader(bytes.Repeat([]byte{7}, 8192)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func securedTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUpDownIdempotentWithFakeBoundaries(t *testing.T) {
	processes, commands, containers := &fakeProcesses{}, &fakeCommands{}, &fakeContainers{}
	manager := testManager(t, securedTempDir(t), processes, commands, containers)
	first, err := manager.Up(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Up(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Endpoint != second.Endpoint || len(processes.started) != 2 || commands.calls != 1 {
		t.Fatalf("up was not idempotent: first=%#v second=%#v processes=%d commands=%d", first, second, len(processes.started), commands.calls)
	}
	if err := manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if containers.stopped != 1 {
		t.Fatalf("containers stopped=%d", containers.stopped)
	}
	if err := manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRedactsSecrets(t *testing.T) {
	manager, err := New(Config{Root: securedTempDir(t)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureRoot(); err != nil {
		t.Fatal(err)
	}
	r := receipt{Version: stateVersion, Marker: rootMarker, Root: manager.config.Root, State: "ready", Endpoint: "https://127.0.0.1:8443", UpdatedAt: manager.clock(), Postgres: componentReceipt{Kind: "container", ID: "container", Endpoint: "postgres://user:super-secret@127.0.0.1:5433/cable", Owned: true}}
	if err := manager.writeReceipt(r); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), "super-secret") || strings.Contains(string(data), "admin-secret") {
		t.Fatalf("status leaked secret: %s", data)
	}
	if strings.Contains(status.Postgres.Endpoint, "super-secret") || strings.Contains(status.Postgres.Endpoint, "@") {
		t.Fatalf("endpoint not redacted: %q", status.Postgres.Endpoint)
	}
}

func TestResetRefusesUndeclaredRoot(t *testing.T) {
	manager, err := New(Config{Root: securedTempDir(t)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(context.Background()); err == nil {
		t.Fatal("reset accepted undeclared root")
	}
}
