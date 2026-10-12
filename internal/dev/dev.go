// Package dev owns the explicitly development-only Cuelight process lifecycle.
//
// The package deliberately keeps all credentials and generated state below a
// caller-declared development root. Status values contain paths and identities,
// never credential material.
package dev

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	operator "github.com/selfdrivingjazz/cuelight/internal/client"
	"github.com/selfdrivingjazz/cuelight/internal/control/pki"
)

const (
	stateVersion           = 1
	rootMarker             = "cuelight-development-root-v1"
	defaultPostgresImage   = "postgres:17-alpine"
	defaultControlBinary   = "cuelight-control"
	defaultSimulatorBinary = "cuelight-node-sim"
)

var (
	ErrNotRunning = errors.New("cuelight development lifecycle is not running")
	ErrUnsafeRoot = errors.New("development root is not declared or is unsafe")
	ErrNotOwned   = errors.New("resource is not owned by this development lifecycle")
)

type Config struct {
	Root             string
	ControlBinary    string
	SimulatorBinary  string
	PostgresImage    string
	PostgresUser     string
	PostgresPassword string
	PostgresDatabase string
	ControlAddress   string
	ReadinessTimeout time.Duration
	DisableSimulator bool
	ShutdownTimeout  time.Duration
}

func (c Config) normalized() (Config, error) {
	if strings.TrimSpace(c.Root) == "" {
		return Config{}, fmt.Errorf("root is required")
	}
	root, err := filepath.Abs(c.Root)
	if err != nil {
		return Config{}, fmt.Errorf("root: %w", err)
	}
	root, err = filepath.EvalSymlinks(filepath.Dir(root))
	if err == nil {
		root = filepath.Join(root, filepath.Base(c.Root))
	}
	if filepath.Clean(root) == string(filepath.Separator) || filepath.Clean(root) == "." {
		return Config{}, ErrUnsafeRoot
	}
	c.Root = filepath.Clean(root)
	if c.ControlBinary == "" {
		c.ControlBinary = defaultControlBinary
	}
	if c.SimulatorBinary == "" {
		c.SimulatorBinary = defaultSimulatorBinary
	}
	if c.PostgresImage == "" {
		c.PostgresImage = defaultPostgresImage
	}
	if c.PostgresUser == "" {
		c.PostgresUser = "cuelight"
	}
	if c.PostgresPassword == "" {
		c.PostgresPassword = "cuelight-development"
	}
	if c.PostgresDatabase == "" {
		c.PostgresDatabase = "cuelight"
	}
	if c.ReadinessTimeout <= 0 {
		c.ReadinessTimeout = 30 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 15 * time.Second
	}
	return c, nil
}

type ProcessSpec struct {
	Name string
	Args []string
	Env  []string
	Dir  string
}
type Process interface {
	PID() int
	Signal(os.Signal) error
	Wait() error
}
type ProcessState interface {
	Process
	Alive() bool
}
type ProcessStarter interface {
	Start(context.Context, ProcessSpec) (Process, error)
}
type CommandRunner interface {
	Run(context.Context, string, []string, []string, io.Writer, io.Writer) (int, error)
}

type ContainerSpec struct {
	Name, Image, User, Password, Database, Root string
	HostPort                                    int
}
type ContainerInfo struct {
	ID, Endpoint string
	Running      bool
	Owned        bool
}
type ContainerRuntime interface {
	Ensure(context.Context, ContainerSpec) (ContainerInfo, error)
	Stop(context.Context, ContainerInfo) error
}

type Probe func(context.Context, string, string) error

type Options struct {
	Processes     ProcessStarter
	Commands      CommandRunner
	Containers    ContainerRuntime
	Probe         Probe
	PostgresProbe func(context.Context, string) error
	Enrollment    func(context.Context, string, string) (string, error)
	Clock         func() time.Time
	Random        io.Reader
}

type Manager struct {
	config        Config
	processes     ProcessStarter
	commands      CommandRunner
	containers    ContainerRuntime
	probe         Probe
	postgresProbe func(context.Context, string) error
	enrollment    func(context.Context, string, string) (string, error)
	clock         func() time.Time
	random        io.Reader
	mu            sync.Mutex
	live          map[string]Process
}

func New(config Config, options Options) (*Manager, error) {
	var err error
	config, err = config.normalized()
	if err != nil {
		return nil, err
	}
	m := &Manager{config: config, processes: options.Processes, commands: options.Commands, containers: options.Containers, probe: options.Probe, postgresProbe: options.PostgresProbe, enrollment: options.Enrollment, clock: options.Clock, random: options.Random, live: make(map[string]Process)}
	if m.processes == nil {
		m.processes = execStarter{}
	}
	if m.commands == nil {
		m.commands = execRunner{}
	}
	if m.containers == nil {
		m.containers = dockerRuntime{runner: m.commands}
	}
	if m.probe == nil {
		m.probe = probeReady
	}
	if m.postgresProbe == nil {
		m.postgresProbe = probePostgres
	}
	if m.enrollment == nil {
		m.enrollment = m.createEnrollment
	}
	if m.clock == nil {
		m.clock = time.Now
	}
	if m.random == nil {
		m.random = rand.Reader
	}
	return m, nil
}

// Up creates development assets and starts PostgreSQL, control, and the node
// simulator. Repeated calls reuse a healthy receipt and are idempotent.
func (m *Manager) Up(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureRoot(); err != nil {
		return Status{}, err
	}
	if receipt, err := m.readReceipt(); err == nil {
		status := m.statusFromReceipt(receipt)
		if status.State == "ready" || status.State == "degraded" {
			return status, nil
		}
		m.cleanupReceiptLocked(ctx, receipt)
	}
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if err := m.ensureAssets(); err != nil {
		return Status{}, err
	}
	port, err := choosePort(m.config.ControlAddress)
	if err != nil {
		return Status{}, err
	}
	container, err := m.containers.Ensure(ctx, ContainerSpec{Name: "cuelight-dev-postgres", Image: m.config.PostgresImage, User: m.config.PostgresUser, Password: m.config.PostgresPassword, Database: m.config.PostgresDatabase, Root: m.config.Root, HostPort: port + 1})
	if err != nil {
		return Status{}, fmt.Errorf("start PostgreSQL: %w", err)
	}
	if !container.Owned {
		return Status{}, fmt.Errorf("start PostgreSQL: %w", ErrNotOwned)
	}
	if container.Endpoint == "" {
		container.Endpoint = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", m.config.PostgresUser, m.config.PostgresPassword, port+1, m.config.PostgresDatabase)
	}
	postgresCtx, cancelPostgres := context.WithTimeout(ctx, m.config.ReadinessTimeout)
	err = m.postgresProbe(postgresCtx, container.Endpoint)
	cancelPostgres()
	if err != nil {
		_ = m.containers.Stop(context.Background(), container)
		return Status{}, fmt.Errorf("PostgreSQL readiness: %w", err)
	}
	r := receipt{Version: stateVersion, Marker: rootMarker, Root: m.config.Root, UpdatedAt: m.clock().UTC(), Endpoint: "https://127.0.0.1:" + strconv.Itoa(port), Paths: m.paths()}
	r.Postgres = componentReceipt{Kind: "container", ID: container.ID, Endpoint: container.Endpoint, Owned: true}
	writeErr := func(err error) (Status, error) { m.cleanupReceiptLocked(context.Background(), r); return Status{}, err }
	controlEnv := m.controlEnv(port, container.Endpoint)
	control, err := m.processes.Start(ctx, ProcessSpec{Name: m.config.ControlBinary, Args: []string{"serve"}, Env: controlEnv, Dir: m.config.Root})
	if err != nil {
		return writeErr(fmt.Errorf("start control: %w", err))
	}
	m.live["control"] = control
	r.Control = componentReceipt{Kind: "process", PID: control.PID(), PIDStart: pidStartToken(control.PID()), Endpoint: r.Endpoint, Owned: true}
	if err := m.writeReceipt(r); err != nil {
		return writeErr(err)
	}
	if err := m.waitReady(ctx, r.Endpoint); err != nil {
		return writeErr(fmt.Errorf("control readiness: %w", err))
	}
	adminPath := m.path("admin-token")
	adminBytes, readAdminErr := os.ReadFile(adminPath)
	admin := strings.TrimSpace(string(adminBytes))
	if readAdminErr != nil || admin == "" {
		admin, err = m.bootstrapAdmin(ctx, controlEnv)
		if err != nil {
			return writeErr(err)
		}
		if err := writeSecret(adminPath, []byte(admin)); err != nil {
			return writeErr(err)
		}
	}
	if err := operator.SaveConfig(m.path("operator.json"), operator.Config{Server: r.Endpoint + "/api/v1", Token: admin, CAPath: m.path("trust", "ca.pem")}); err != nil {
		return writeErr(err)
	}
	if !m.config.DisableSimulator {
		enrollment := ""
		if _, stateErr := os.Stat(m.path("state", "node.state.json")); errors.Is(stateErr, os.ErrNotExist) {
			enrollment, err = m.enrollment(ctx, r.Endpoint, admin)
			if err != nil {
				return writeErr(err)
			}
		}
		simulatorArgs := []string{"-control-url", r.Endpoint + "/api/v1", "-server-ca", m.path("trust", "ca.pem"), "-state", m.path("state", "node.state.json")}
		if enrollment != "" {
			simulatorArgs = append(simulatorArgs, "-enrollment-token", enrollment)
		}
		sim, err := m.processes.Start(ctx, ProcessSpec{Name: m.config.SimulatorBinary, Args: simulatorArgs, Env: nil, Dir: m.config.Root})
		if err != nil {
			return writeErr(fmt.Errorf("start simulator: %w", err))
		}
		m.live["simulator"] = sim
		r.Simulator = componentReceipt{Kind: "process", PID: sim.PID(), PIDStart: pidStartToken(sim.PID()), Owned: true}
	}
	r.State = "ready"
	r.UpdatedAt = m.clock().UTC()
	if err := m.writeReceipt(r); err != nil {
		return writeErr(err)
	}
	return m.statusFromReceipt(r), nil
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureRoot(); err != nil {
		return Status{}, err
	}
	r, err := m.readReceipt()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{Version: stateVersion, Development: true, Root: m.config.Root, State: "down", UpdatedAt: m.clock().UTC()}, nil
		}
		return Status{}, err
	}
	status := m.statusFromReceipt(r)
	if status.State == "ready" && !m.componentAlive(r.Control) {
		status.State = "stale"
		status.Warnings = append(status.Warnings, "control process is no longer running")
	} else if status.State == "ready" && (status.Control.State != "running" || status.Simulator.State != "running" || status.Postgres.State != "running") {
		status.State = "degraded"
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	return status, nil
}

func (m *Manager) Down(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureRoot(); err != nil {
		return err
	}
	r, err := m.readReceipt()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	m.cleanupReceiptLocked(ctx, r)
	return os.Remove(m.receiptPath())
}

// Reset stops owned resources and removes only the generated state below the
// declared root. It refuses absent markers, root mismatch, and symlinked paths.
func (m *Manager) Reset(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDeclaredRoot(); err != nil {
		return err
	}
	if r, err := m.readReceipt(); err == nil {
		m.cleanupReceiptLocked(ctx, r)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.reclaimPostgresRoot(ctx); err != nil {
		return err
	}
	for _, path := range []string{m.path("trust"), m.path("cas"), m.path("state"), m.path("postgres"), m.path("admin-token"), m.path("operator.json"), m.path("logs"), m.receiptPath()} {
		if err := removeWithinRoot(m.config.Root, path); err != nil {
			return err
		}
	}
	return nil
}

// Top-level helpers make the package convenient for a later public CLI.
func Up(ctx context.Context, config Config, options Options) (Status, error) {
	m, e := New(config, options)
	if e != nil {
		return Status{}, e
	}
	return m.Up(ctx)
}
func StatusOf(ctx context.Context, config Config, options Options) (Status, error) {
	m, e := New(config, options)
	if e != nil {
		return Status{}, e
	}
	return m.Status(ctx)
}
func Down(ctx context.Context, config Config, options Options) error {
	m, e := New(config, options)
	if e != nil {
		return e
	}
	return m.Down(ctx)
}
func Reset(ctx context.Context, config Config, options Options) error {
	m, e := New(config, options)
	if e != nil {
		return e
	}
	return m.Reset(ctx)
}

type Status struct {
	Version     int               `json:"version"`
	Development bool              `json:"development"`
	Root        string            `json:"root"`
	State       string            `json:"state"`
	Endpoint    string            `json:"endpoint,omitempty"`
	Postgres    ComponentStatus   `json:"postgres"`
	Control     ComponentStatus   `json:"control"`
	Simulator   ComponentStatus   `json:"simulator"`
	Paths       map[string]string `json:"paths,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}
type ComponentStatus struct {
	State       string `json:"state"`
	PID         int    `json:"pid,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}
type componentReceipt struct {
	Kind     string `json:"kind"`
	PID      int    `json:"pid,omitempty"`
	PIDStart string `json:"pidStart,omitempty"`
	ID       string `json:"id,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Owned    bool   `json:"owned"`
}
type receipt struct {
	Version   int               `json:"version"`
	Marker    string            `json:"marker"`
	Root      string            `json:"root"`
	State     string            `json:"state"`
	Endpoint  string            `json:"endpoint"`
	Paths     map[string]string `json:"paths"`
	Postgres  componentReceipt  `json:"postgres"`
	Control   componentReceipt  `json:"control"`
	Simulator componentReceipt  `json:"simulator"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

func (m *Manager) path(parts ...string) string {
	return filepath.Join(append([]string{m.config.Root}, parts...)...)
}
func (m *Manager) receiptPath() string { return m.path("lifecycle.json") }
func (m *Manager) paths() map[string]string {
	return map[string]string{
		"ca":             m.path("trust", "ca.pem"),
		"operatorConfig": m.path("operator.json"),
		"logs":           m.path("logs"),
	}
}
func (m *Manager) ensureRoot() error {
	if err := os.MkdirAll(m.config.Root, 0700); err != nil {
		return err
	}
	marker := m.path(".development")
	info, statErr := os.Lstat(marker)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := os.WriteFile(marker, []byte(rootMarker+"\n"), 0600); err != nil {
			return err
		}
		info, statErr = os.Lstat(marker)
	}
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeRoot
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != rootMarker {
		return ErrUnsafeRoot
	}
	info, err = os.Lstat(m.config.Root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&0o077 != 0 {
		return fmt.Errorf("%w: root permissions must be 0700", ErrUnsafeRoot)
	}
	return nil
}
func (m *Manager) ensureDeclaredRoot() error {
	info, err := os.Lstat(m.config.Root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeRoot, err)
	}
	if !info.IsDir() || info.Mode()&0o077 != 0 {
		return fmt.Errorf("%w: root permissions must be 0700", ErrUnsafeRoot)
	}
	markerInfo, err := os.Lstat(m.path(".development"))
	if err != nil || markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return ErrUnsafeRoot
	}
	data, err := os.ReadFile(m.path(".development"))
	if err != nil || strings.TrimSpace(string(data)) != rootMarker {
		return ErrUnsafeRoot
	}
	return nil
}
func (m *Manager) readReceipt() (receipt, error) {
	var r receipt
	data, err := os.ReadFile(m.receiptPath())
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("read lifecycle receipt: %w", err)
	}
	if r.Version != stateVersion || r.Marker != rootMarker || filepath.Clean(r.Root) != m.config.Root {
		return r, ErrUnsafeRoot
	}
	return r, nil
}
func (m *Manager) writeReceipt(r receipt) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(m.receiptPath(), append(data, '\n'), 0600)
}
func (m *Manager) statusFromReceipt(r receipt) Status {
	s := Status{Version: stateVersion, Development: true, Root: m.config.Root, State: r.State, Endpoint: r.Endpoint, Paths: r.Paths, UpdatedAt: r.UpdatedAt}
	s.Postgres = m.componentStatus(r.Postgres)
	s.Control = m.componentStatus(r.Control)
	s.Simulator = m.componentStatus(r.Simulator)
	if s.State == "" {
		s.State = "starting"
	}
	return s
}
func (m *Manager) componentStatus(c componentReceipt) ComponentStatus {
	state := "stopped"
	if c.Owned && (c.PID == 0 || m.componentAlive(c)) {
		state = "running"
	}
	endpoint := c.Endpoint
	if c.Kind == "container" {
		endpoint = redactEndpoint(endpoint)
	}
	return ComponentStatus{State: state, PID: c.PID, ContainerID: c.ID, Endpoint: endpoint}
}
func (m *Manager) reclaimPostgresRoot(ctx context.Context) error {
	postgresRoot := m.path("postgres")
	if _, err := os.Stat(postgresRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	code, err := m.commands.Run(ctx, "docker", []string{"run", "--rm", "--entrypoint", "chown", "-v", postgresRoot + ":/data", m.config.PostgresImage, "-R", owner, "/data"}, nil, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("reclaim PostgreSQL development data: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("reclaim PostgreSQL development data exited %d", code)
	}
	return nil
}

func redactEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}
func (m *Manager) liveByPID(pid int) Process {
	for _, p := range m.live {
		if p.PID() == pid {
			return p
		}
	}
	return nil
}
func (m *Manager) componentAlive(c componentReceipt) bool {
	if c.PID == 0 {
		return c.ID != ""
	}
	if p := m.liveByPID(c.PID); p != nil {
		if state, ok := p.(ProcessState); ok {
			return state.Alive()
		}
		return true
	}
	return c.PIDStart != "" && pidStartToken(c.PID) == c.PIDStart
}
func (m *Manager) cleanupReceiptLocked(ctx context.Context, r receipt) {
	deadline := m.config.ShutdownTimeout
	stopCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for name, c := range map[string]componentReceipt{"simulator": r.Simulator, "control": r.Control} {
		if !c.Owned {
			continue
		}
		if p := m.live[name]; p != nil {
			_ = p.Signal(syscall.SIGTERM)
			select {
			case <-stopCtx.Done():
				_ = p.Signal(syscall.SIGKILL)
			case <-waitProcess(p):
			}
			delete(m.live, name)
			continue
		}
		// A new Manager has no in-memory handle. The start-time receipt keeps
		// stale cleanup from terminating an unrelated recycled PID.
		if c.PID > 1 && c.PIDStart != "" && pidStartToken(c.PID) == c.PIDStart {
			_ = syscall.Kill(c.PID, syscall.SIGTERM)
		}
	}
	if r.Postgres.Owned && r.Postgres.ID != "" {
		_ = m.containers.Stop(stopCtx, ContainerInfo{ID: r.Postgres.ID, Endpoint: r.Postgres.Endpoint, Owned: true})
	}
}
func waitProcess(p Process) <-chan struct{} {
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	return done
}

func (m *Manager) ensureAssets() error {
	dirs := []string{m.path("trust"), m.path("cas"), m.path("state"), m.path("state", "tmp")}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	caPath := m.path("trust", "ca.pem")
	if _, err := os.Stat(caPath); err == nil {
		return nil
	}
	clock := pkiClock{now: m.clock}
	authority, err := pki.BootstrapAuthority(context.Background(), clock, pki.CryptoSerialSource{Reader: m.random}, pki.AuthorityBootstrapOptions{
		CommonName: "Cuelight development CA (NON-PRODUCTION)",
		Lifetime:   365 * 24 * time.Hour, Backdate: time.Minute, Random: m.random,
	})
	if err != nil {
		return err
	}
	server, err := authority.Authority.BootstrapServer(context.Background(), clock, pki.CryptoSerialSource{Reader: m.random}, []string{"127.0.0.1", "localhost"})
	if err != nil {
		return err
	}
	_, publisherPrivate, err := ed25519.GenerateKey(m.random)
	if err != nil {
		return err
	}
	publisherDER, err := x509.MarshalPKCS8PrivateKey(publisherPrivate)
	if err != nil {
		return err
	}
	seal := make([]byte, 32)
	if _, err := io.ReadFull(m.random, seal); err != nil {
		return err
	}
	files := map[string][]byte{
		caPath:                               []byte(authority.Certificate),
		m.path("trust", "ca-key.pem"):        []byte(authority.PrivateKey),
		m.path("trust", "server.pem"):        []byte(server.Certificate),
		m.path("trust", "server-key.pem"):    []byte(server.PrivateKey),
		m.path("trust", "publisher-key.pem"): pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: publisherDER}),
		m.path("trust", "seal.key"):          seal,
	}
	for path, data := range files {
		if err := atomicWrite(path, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

type pkiClock struct{ now func() time.Time }

func (c pkiClock) Now() time.Time { return c.now() }
func (m *Manager) controlEnv(port int, database string) []string {
	return []string{"CUELIGHT_CONTROL_LISTEN_ADDR=127.0.0.1:" + strconv.Itoa(port), "CUELIGHT_CONTROL_DATABASE_URL=" + database, "CUELIGHT_CONTROL_TLS_CERT_PATH=" + m.path("trust", "server.pem"), "CUELIGHT_CONTROL_TLS_KEY_PATH=" + m.path("trust", "server-key.pem"), "CUELIGHT_CONTROL_PUBLISHER_KEY_PATH=" + m.path("trust", "publisher-key.pem"), "CUELIGHT_CONTROL_PUBLISHER_KEY_ID=dev-publisher", "CUELIGHT_CONTROL_CAS_PATH=" + m.path("cas"), "CUELIGHT_CONTROL_CA_PATH=" + m.path("trust", "ca.pem"), "CUELIGHT_CONTROL_CA_KEY_PATH=" + m.path("trust", "ca-key.pem"), "CUELIGHT_CONTROL_SEAL_KEY_PATH=" + m.path("trust", "seal.key"), "CUELIGHT_CONTROL_TEMP_ROOT=" + m.path("state", "tmp")}
}
func (m *Manager) waitReady(ctx context.Context, endpoint string) error {
	probeCtx, cancel := context.WithTimeout(ctx, m.config.ReadinessTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := m.probe(probeCtx, endpoint, m.path("trust", "ca.pem")); err == nil {
			return nil
		}
		select {
		case <-probeCtx.Done():
			return probeCtx.Err()
		case <-ticker.C:
		}
	}
}
func (m *Manager) bootstrapAdmin(ctx context.Context, env []string) (string, error) {
	var out strings.Builder
	scopes := "security_admin,node_enroll,node_read,node_write,assignment_read,assignment_write,media_write,local_media_read,media_read,release_write,release_read"
	code, err := m.commands.Run(ctx, m.config.ControlBinary, []string{"admin", "token", "create", "--actor-name", "cuelight development", "--scopes", scopes}, env, &out, io.Discard)
	if err != nil {
		return "", fmt.Errorf("bootstrap admin token: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("bootstrap admin token exited with status %d", code)
	}
	var token struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(out.String()), &token); err != nil || token.Secret == "" {
		return "", fmt.Errorf("bootstrap admin token: invalid response")
	}
	return token.Secret, nil
}
func (m *Manager) createEnrollment(ctx context.Context, endpoint, admin string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/v1/enrollment-tokens", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Idempotency-Key", "cuelight-dev-enrollment")
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: mustCA(m.path("trust", "ca.pem"))}}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", fmt.Errorf("create enrollment token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create enrollment token: HTTP %s", resp.Status)
	}
	var token struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil || token.Secret == "" {
		return "", fmt.Errorf("create enrollment token: invalid response")
	}
	return token.Secret, nil
}
func mustCA(path string) *x509.CertPool {
	pool := x509.NewCertPool()
	data, err := os.ReadFile(path)
	if err == nil {
		pool.AppendCertsFromPEM(data)
	}
	return pool
}
func writeSecret(path string, data []byte) error { return atomicWrite(path, append(data, '\n'), 0600) }
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func removeWithinRoot(root, path string) error {
	root, _ = filepath.Abs(root)
	path, _ = filepath.Abs(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		return ErrUnsafeRoot
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: refusing symlink %s", ErrUnsafeRoot, path)
	}
	return os.RemoveAll(path)
}
func choosePort(address string) (int, error) {
	if address != "" {
		if _, p, e := net.SplitHostPort(address); e == nil {
			n, e := strconv.Atoi(p)
			if e == nil && n > 0 {
				return n, nil
			}
		}
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func pidStartToken(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) > 21 {
		return fields[21]
	}
	return ""
}

func probeReady(ctx context.Context, endpoint, caPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/ready", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: mustCA(caPath)}}}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func probePostgres(ctx context.Context, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	host := parsed.Host
	if host == "" {
		return errors.New("PostgreSQL endpoint has no host")
	}
	dialer := net.Dialer{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := dialer.DialContext(ctx, "tcp", host)
		if err == nil {
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			request := make([]byte, 8)
			binary.BigEndian.PutUint32(request[0:4], 8)
			binary.BigEndian.PutUint32(request[4:8], 80877103) // PostgreSQL SSLRequest
			_, writeErr := connection.Write(request)
			response := []byte{0}
			_, readErr := io.ReadFull(connection, response)
			closeErr := connection.Close()
			if writeErr == nil && readErr == nil && closeErr == nil && (response[0] == 'S' || response[0] == 'N') {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), err
	}
	return -1, err
}

type execStarter struct{}

func (execStarter) Start(_ context.Context, s ProcessSpec) (Process, error) {
	cmd := exec.Command(s.Name, s.Args...)
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.Dir = s.Dir
	logDirectory := filepath.Join(s.Dir, "logs")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(logDirectory, filepath.Base(s.Name)+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	if err := logFile.Close(); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	return execProcess{cmd}, nil
}

type execProcess struct{ cmd *exec.Cmd }

func (p execProcess) PID() int                 { return p.cmd.Process.Pid }
func (p execProcess) Signal(s os.Signal) error { return p.cmd.Process.Signal(s) }
func (p execProcess) Wait() error              { return p.cmd.Wait() }

type dockerRuntime struct{ runner CommandRunner }

func (d dockerRuntime) Ensure(ctx context.Context, s ContainerSpec) (ContainerInfo, error) {
	port := s.HostPort
	if port == 0 {
		port = 5433
	}
	postgresRoot := filepath.Join(s.Root, "postgres")
	if err := os.MkdirAll(postgresRoot, 0o700); err != nil {
		return ContainerInfo{}, err
	}
	var out strings.Builder
	args := []string{"run", "-d", "--rm", "--name", s.Name, "--label", "com.cuelight.dev.root=" + s.Root, "-v", postgresRoot + ":/var/lib/postgresql/data", "-e", "POSTGRES_USER=" + s.User, "-e", "POSTGRES_PASSWORD=" + s.Password, "-e", "POSTGRES_DB=" + s.Database, "-p", fmt.Sprintf("127.0.0.1:%d:5432", port), s.Image}
	code, err := d.runner.Run(ctx, "docker", args, nil, &out, io.Discard)
	if err != nil || code != 0 {
		return ContainerInfo{}, fmt.Errorf("docker run: %w (exit %d)", err, code)
	}
	id := strings.TrimSpace(out.String())
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		var readyOutput strings.Builder
		readyCode, readyErr := d.runner.Run(readyCtx, "docker", []string{"exec", id, "pg_isready", "-U", s.User, "-d", s.Database}, nil, &readyOutput, io.Discard)
		if readyErr == nil && readyCode == 0 {
			break
		}
		select {
		case <-readyCtx.Done():
			_, _ = d.runner.Run(context.Background(), "docker", []string{"rm", "-f", id}, nil, io.Discard, io.Discard)
			return ContainerInfo{}, fmt.Errorf("PostgreSQL container readiness: %w", readyCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ContainerInfo{ID: id, Endpoint: fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", s.User, s.Password, port, s.Database), Running: true, Owned: true}, nil
}
func (d dockerRuntime) Stop(ctx context.Context, c ContainerInfo) error {
	if !c.Owned || c.ID == "" {
		return ErrNotOwned
	}
	code, err := d.runner.Run(ctx, "docker", []string{"rm", "-f", c.ID}, nil, io.Discard, io.Discard)
	if code != 0 && err != nil {
		return err
	}
	return nil
}
