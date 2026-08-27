// Package sim provides a deterministic, non-interactive cable player node.
package sim

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/player/runtime"
	playerstate "github.com/jmill/cable/internal/player/state"
)

// Config controls a node simulator. ServerCAPEM is the provisioned control
// plane trust anchor; it is intentionally never generated implicitly.
type Config struct {
	ControlURL          string
	ServerCAPEM         []byte
	EnrollmentToken     string
	RecoveryToken       string
	NodeName            string
	StatePath           string
	CASPath             string
	Evidence            io.Writer
	Capabilities        playerclient.CapabilityReport
	Features            []string
	AcceptedMediaTypes  []string
	Probation           time.Duration
	PollWait            time.Duration
	ReconnectMinimum    time.Duration
	ReconnectMaximum    time.Duration
	PartialGrace        time.Duration
	StreamTTL           time.Duration
	MaxEvents           int
	MaxEventBatch       int
	MaxConditions       int
	MaxMessageRunes     int
	OutputReady         bool
	CapacityBytes       int64
	RendererFailPrepare bool
	RendererFailStart   bool
	RendererFailHealth  bool
}

// PersistentState is the only simulator state written to disk. Keeping the
// enrollment idempotency key durable permits safe replay after a lost response.
type PersistentState struct {
	PrivateKey               string `json:"privateKey"`
	PublicKey                string `json:"publicKey"`
	Certificate              string `json:"certificate"`
	NodeID                   string `json:"nodeId"`
	EventEpoch               string `json:"eventEpoch"`
	PublisherKeyID           string `json:"publisherKeyId"`
	PublisherPublicKey       string `json:"publisherPublicKey"`
	EnrollmentIdempotencyKey string `json:"enrollmentIdempotencyKey"`
	RecoveryIdempotencyKey   string `json:"recoveryIdempotencyKey"`
}

type Simulator struct {
	config   Config
	state    PersistentState
	client   *playerclient.Client
	journal  *playerstate.Store
	cas      *localcas.Store
	output   *FakeOutput
	renderer *FakeRenderer
	streamer *loopbackStreamer
	runtime  *runtime.Reconciler
	evidence *json.Encoder
	mu       sync.Mutex
}

func (c *Config) defaults() {
	if c.NodeName == "" {
		c.NodeName = "sim-node"
	}
	if c.Probation == 0 {
		c.Probation = 10 * time.Millisecond
	}
	if c.PollWait == 0 {
		c.PollWait = 0
	}
	if c.ReconnectMinimum == 0 {
		c.ReconnectMinimum = 10 * time.Millisecond
	}
	if c.ReconnectMaximum == 0 {
		c.ReconnectMaximum = 100 * time.Millisecond
	}
	if c.PartialGrace == 0 {
		c.PartialGrace = time.Minute
	}
	if c.StreamTTL == 0 {
		c.StreamTTL = time.Second
	}
	if c.MaxEvents == 0 {
		c.MaxEvents = 128
	}
	if c.MaxEventBatch == 0 {
		c.MaxEventBatch = 32
	}
	if c.MaxConditions == 0 {
		c.MaxConditions = 32
	}
	if c.MaxMessageRunes == 0 {
		c.MaxMessageRunes = 256
	}
	if c.CapacityBytes == 0 {
		c.CapacityBytes = 1 << 30
	}
	c.OutputReady = true
	c.Capabilities.OutputReady = true
	c.Capabilities.ClockReliable = true
	if c.Capabilities.Platform == "" {
		c.Capabilities.Platform = "sim"
	}
	if c.Capabilities.Architecture == "" {
		c.Capabilities.Architecture = "amd64"
	}
	if c.Capabilities.ProtocolVersion == "" {
		c.Capabilities.ProtocolVersion = "1.0"
	}
	if c.Capabilities.RendererVersion == "" {
		c.Capabilities.RendererVersion = "sim-renderer/1"
	}
	if c.Capabilities.ProfileID == "" {
		c.Capabilities.ProfileID = "sim"
	}
}

func New(config Config) (*Simulator, error) {
	config.defaults()
	if !strings.HasPrefix(config.ControlURL, "https://") {
		return nil, errors.New("control URL must be HTTPS")
	}
	if len(config.ServerCAPEM) == 0 {
		return nil, errors.New("provisioned server CA is required")
	}
	if config.StatePath == "" {
		return nil, errors.New("state path is required")
	}
	if config.CASPath == "" {
		config.CASPath = config.StatePath + ".cas"
	}
	if err := os.MkdirAll(filepath.Dir(config.StatePath), 0o700); err != nil {
		return nil, err
	}
	state, err := loadState(config.StatePath)
	if err != nil {
		return nil, err
	}
	s := &Simulator{config: config, state: state, output: NewFakeOutput(config.OutputReady, config.CapacityBytes)}
	s.renderer = NewFakeRenderer(s.output, RendererConfig{FailPrepare: config.RendererFailPrepare, FailStart: config.RendererFailStart, FailHealth: config.RendererFailHealth})
	s.evidence = json.NewEncoder(io.Discard)
	if config.Evidence != nil {
		s.evidence = json.NewEncoder(config.Evidence)
	}
	s.journal, err = playerstate.Open(config.StatePath + ".db")
	if err != nil {
		return nil, fmt.Errorf("open simulator journal: %w", err)
	}
	s.cas, err = localcas.New(config.CASPath)
	if err != nil {
		s.journal.Close()
		return nil, fmt.Errorf("open simulator CAS: %w", err)
	}
	if err := s.configureClient(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func loadState(path string) (PersistentState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PersistentState{}, nil
	}
	if err != nil {
		return PersistentState{}, err
	}
	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return PersistentState{}, fmt.Errorf("decode simulator state: %w", err)
	}
	return state, nil
}

func (s *Simulator) persist() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp := s.config.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.config.StatePath); err != nil {
		return err
	}
	return nil
}

func (s *Simulator) configureClient() error {
	var certificate *tls.Certificate
	if s.state.Certificate != "" && s.state.PrivateKey != "" {
		cert, err := tls.X509KeyPair([]byte(s.state.Certificate), []byte(s.state.PrivateKey))
		if err != nil {
			return fmt.Errorf("load persisted node credential: %w", err)
		}
		certificate = &cert
	}
	client, err := playerclient.New(playerclient.Config{BaseURL: s.config.ControlURL, ServerCAPEM: s.config.ServerCAPEM, NodeCertificate: certificate})
	if err != nil {
		return err
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	s.client = client
	return nil
}

func (s *Simulator) ensureKey() (ed25519.PrivateKey, error) {
	if s.state.PrivateKey != "" {
		block, _ := pem.Decode([]byte(s.state.PrivateKey))
		if block == nil {
			return nil, errors.New("persisted private key is not PEM")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		private, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("persisted private key is not Ed25519")
		}
		return private, nil
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	s.state.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	s.state.PublicKey = base64.StdEncoding.EncodeToString(public)
	if s.state.EnrollmentIdempotencyKey == "" {
		s.state.EnrollmentIdempotencyKey = randomKey()
	}
	if err := s.persist(); err != nil {
		return nil, err
	}
	return private, nil
}

func randomKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Simulator) Enroll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.NodeID != "" {
		return s.buildRuntime()
	}
	if _, err := s.ensureKey(); err != nil {
		return err
	}
	enrollment, err := s.client.Enroll(ctx, playerclient.EnrollmentRequest{Token: s.config.EnrollmentToken, PublicKey: s.state.PublicKey}, s.state.EnrollmentIdempotencyKey)
	if err != nil {
		return err
	}
	s.state.Certificate, s.state.NodeID, s.state.EventEpoch = enrollment.Certificate, enrollment.NodeID, enrollment.EventEpoch
	s.state.PublisherKeyID, s.state.PublisherPublicKey = enrollment.PublisherKeyID, enrollment.PublisherPublicKey
	if err := s.persist(); err != nil {
		return err
	}
	if err := s.configureClient(); err != nil {
		return err
	}
	return s.buildRuntime()
}

func (s *Simulator) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.NodeID == "" {
		return errors.New("cannot recover before enrollment")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	s.state.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	s.state.PublicKey = base64.StdEncoding.EncodeToString(public)
	if s.state.RecoveryIdempotencyKey == "" {
		s.state.RecoveryIdempotencyKey = randomKey()
	}
	if err := s.persist(); err != nil {
		return err
	}
	enrollment, err := s.client.Recover(ctx, playerclient.EnrollmentRequest{Token: s.config.RecoveryToken, PublicKey: s.state.PublicKey}, s.state.RecoveryIdempotencyKey)
	if err != nil {
		return err
	}
	s.state.Certificate, s.state.EventEpoch = enrollment.Certificate, enrollment.EventEpoch
	if enrollment.NodeID != "" {
		s.state.NodeID = enrollment.NodeID
	}
	if enrollment.PublisherKeyID != "" {
		s.state.PublisherKeyID = enrollment.PublisherKeyID
	}
	if enrollment.PublisherPublicKey != "" {
		s.state.PublisherPublicKey = enrollment.PublisherPublicKey
	}
	if err := s.persist(); err != nil {
		return err
	}
	if err := s.configureClient(); err != nil {
		return err
	}
	return s.buildRuntime()
}

func (s *Simulator) buildRuntime() error {
	if s.runtime != nil {
		return nil
	}
	publisher, err := base64.StdEncoding.DecodeString(s.state.PublisherPublicKey)
	if err != nil {
		return fmt.Errorf("decode publisher key: %w", err)
	}
	proxy, err := newLoopbackStreamer(s.config.ControlURL, s.config.ServerCAPEM, s.state.Certificate, s.state.PrivateKey)
	if err != nil {
		return err
	}
	s.streamer = proxy
	config := runtime.Config{Capabilities: s.config.Capabilities, EventEpoch: s.state.EventEpoch, PublisherKeyID: s.state.PublisherKeyID, PublisherPublicKey: publisher, Features: s.config.Features, AcceptedMediaTypes: s.config.AcceptedMediaTypes, Probation: s.config.Probation, PollWait: s.config.PollWait, ReconnectMinimum: s.config.ReconnectMinimum, ReconnectMaximum: s.config.ReconnectMaximum, PartialGrace: s.config.PartialGrace, StreamTTL: s.config.StreamTTL, MaxEvents: s.config.MaxEvents, MaxEventBatch: s.config.MaxEventBatch, MaxConditions: s.config.MaxConditions, MaxMessageRunes: s.config.MaxMessageRunes}
	r, err := runtime.New(config, runtime.Dependencies{Controller: s.client, Journal: s.journal, CAS: s.cas, Capacity: s.output, Streamer: s.streamer, Renderer: s.renderer})
	if err != nil {
		s.streamer.Close()
		s.streamer = nil
		return err
	}
	s.runtime = r
	return nil
}

func (s *Simulator) Run(ctx context.Context) error {
	if s.state.NodeID == "" {
		if s.config.EnrollmentToken == "" {
			return errors.New("enrollment token is required")
		}
		if err := s.Enroll(ctx); err != nil {
			return err
		}
	} else if err := s.buildRuntime(); err != nil {
		return err
	}
	return s.runtime.Run(ctx)
}

func (s *Simulator) Reconcile(ctx context.Context, assignment playerclient.Assignment) error {
	if s.runtime == nil {
		return errors.New("simulator is not enrolled")
	}
	err := s.runtime.Reconcile(ctx, assignment)
	s.Observe("reconcile", err)
	return err
}

func (s *Simulator) Runtime() *runtime.Reconciler { return s.runtime }
func (s *Simulator) Client() *playerclient.Client { return s.client }
func (s *Simulator) State() PersistentState       { s.mu.Lock(); defer s.mu.Unlock(); return s.state }
func (s *Simulator) Output() *FakeOutput          { return s.output }
func (s *Simulator) Renderer() *FakeRenderer      { return s.renderer }

func (s *Simulator) Observe(kind string, runErr error) {
	observation := Observation{Kind: kind, NodeID: s.state.NodeID, EventEpoch: s.state.EventEpoch}
	if s.runtime != nil {
		observation.Snapshot = s.runtime.Snapshot()
	}
	if runErr != nil {
		observation.Error = runErr.Error()
	}
	_ = s.evidence.Encode(observation)
}

func (s *Simulator) Close() error {
	if s.streamer != nil {
		s.streamer.Close()
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	if s.journal != nil {
		return s.journal.Close()
	}
	return nil
}

// Observation is deliberately JSONL-friendly and contains no unstable pointer
// or implementation fields. encoding/json sorts object keys for stable output.
type Observation struct {
	Kind       string           `json:"kind"`
	NodeID     string           `json:"nodeId,omitempty"`
	EventEpoch string           `json:"eventEpoch,omitempty"`
	Snapshot   runtime.Snapshot `json:"snapshot"`
	Error      string           `json:"error,omitempty"`
}

// tlsHTTPClient is used only by the loopback proxy; player.Client intentionally
// keeps its transport private while this transport uses identical trust rules.
func tlsHTTPClient(caPEM []byte, certPEM, keyPEM string) (*http.Client, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid server CA")
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{cert}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
