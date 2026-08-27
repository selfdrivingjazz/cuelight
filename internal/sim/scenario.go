package sim

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/player/runtime"
	playerstate "github.com/jmill/cable/internal/player/state"
	"github.com/jmill/cable/internal/release"
)

// ScenarioConfig runs a named case without a control-plane process. It still
// exercises the production reconciler, bbolt journal, local CAS and renderer.
type ScenarioConfig struct {
	Name          string
	Evidence      io.Writer
	Root          string
	OutputReady   bool
	CapacityBytes int64
	Data          []byte
}

// RunScenario emits exactly one terminal JSONL observation for the named case.
func RunScenario(ctx context.Context, config ScenarioConfig) error {
	if config.Name == "" {
		return errors.New("scenario name is required")
	}
	if config.Root == "" {
		config.Root, _ = os.MkdirTemp("", "cable-node-sim-")
		defer os.RemoveAll(config.Root)
	}
	if config.Data == nil {
		config.Data = []byte("simulated media")
	}
	if config.CapacityBytes == 0 {
		if config.Name == "insufficient-capacity" {
			config.CapacityBytes = int64(len(config.Data) - 1)
		} else {
			config.CapacityBytes = 1 << 30
		}
	}
	if !config.OutputReady {
		config.OutputReady = config.Name != "no-output-recovery"
	}
	names := map[string]bool{"enroll": true, "stream-cache": true, "candidate-failure": true, "offline-reconnect": true, "event-overflow": true, "insufficient-capacity": true, "no-output-recovery": true}
	if !names[config.Name] {
		return fmt.Errorf("unknown scenario %q", config.Name)
	}
	publisher, publisherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	digestBytes := sha256.Sum256(config.Data)
	digest := hex.EncodeToString(digestBytes[:])
	payload := release.PayloadV1{SchemaVersion: release.SchemaVersion, MinimumProtocolVersion: release.ProtocolV1, Playback: release.PlaybackPayload{AssetID: "asset-1", Fit: "fit", Loop: true, Volume: 50, AllowedDeliveryMode: []string{runtime.DeliveryStream, runtime.DeliveryCacheThenPlay}}, Artifacts: []release.Artifact{{Digest: digest, Size: int64(len(config.Data)), MediaType: "video/mp4"}}}
	envelope, err := release.Sign("release-1", "publisher-1", payload, publisherPrivate)
	if err != nil {
		return err
	}
	controller := &scenarioController{assignment: playerclient.Assignment{ID: "assignment-1", NodeID: "sim-node", ReleaseID: envelope.ID, ReleaseDigest: envelope.Digest, DeliveryMode: runtime.DeliveryStream, DesiredGeneration: 1}, envelope: envelope, data: config.Data, metadataSize: int64(len(config.Data))}
	if config.Name == "stream-cache" || config.Name == "insufficient-capacity" {
		controller.assignment.DeliveryMode = runtime.DeliveryCacheThenPlay
	}
	output := NewFakeOutput(config.OutputReady, config.CapacityBytes)
	rendererConfig := RendererConfig{}
	switch config.Name {
	case "candidate-failure":
		rendererConfig.FailHealth = true
	case "no-output-recovery":
		rendererConfig.FailStart = true
	}
	fakeRenderer := NewFakeRenderer(output, rendererConfig)
	root := filepath.Clean(config.Root)
	journal, err := playerstate.Open(filepath.Join(root, "state.db"))
	if err != nil {
		return err
	}
	defer journal.Close()
	cas, err := localcas.New(filepath.Join(root, "cas"))
	if err != nil {
		return err
	}
	runtimeConfig := runtime.Config{Capabilities: playerclient.CapabilityReport{Platform: "sim", Architecture: "amd64", ProtocolVersion: "1.0", RendererVersion: "sim", ProfileID: "sim", OutputReady: config.OutputReady, ClockReliable: true}, EventEpoch: "epoch-1", PublisherKeyID: "publisher-1", PublisherPublicKey: publisher, Probation: time.Millisecond, PollWait: 0, ReconnectMinimum: time.Millisecond, ReconnectMaximum: 10 * time.Millisecond, PartialGrace: time.Minute, StreamTTL: time.Second, MaxEvents: 2, MaxEventBatch: 1, MaxConditions: 8, MaxMessageRunes: 128}
	stream := scenarioStreamer{}
	reconciler, err := runtime.New(runtimeConfig, runtime.Dependencies{Controller: controller, Journal: journal, CAS: cas, Capacity: output, Streamer: stream, Renderer: fakeRenderer})
	if err != nil {
		return err
	}
	runErr := reconciler.Reconcile(ctx, controller.assignment)
	if config.Name == "insufficient-capacity" {
		if runErr == nil {
			return errors.New("insufficient-capacity did not fail")
		}
		if !errors.Is(runErr, runtime.ErrInsufficientCapacity) {
			return fmt.Errorf("insufficient-capacity: %w", runErr)
		}
	}
	if config.Name == "candidate-failure" && runErr == nil {
		return errors.New("candidate-failure did not fail")
	}
	if config.Name == "no-output-recovery" && runErr == nil {
		return errors.New("no-output-recovery did not fail")
	}
	if config.Name == "offline-reconnect" {
		controller.offline = true
		_, _ = controller.GetAssignment(ctx, "", 0)
		controller.offline = false
		runErr = reconciler.Reconcile(ctx, controller.assignment)
	}
	if config.Name == "enroll" {
		runErr = nil
	}
	evidence := Observation{Kind: config.Name, NodeID: "sim-node", EventEpoch: "epoch-1", Snapshot: reconciler.Snapshot()}
	if runErr != nil {
		evidence.Error = runErr.Error()
	}
	encoder := json.NewEncoder(io.Discard)
	if config.Evidence != nil {
		encoder = json.NewEncoder(config.Evidence)
	}
	return encoder.Encode(evidence)
}

type scenarioStreamer struct{}

func (scenarioStreamer) Open(context.Context, string, time.Duration) (string, func(), error) {
	return "http://127.0.0.1:41873/media/scenario", func() {}, nil
}
func (scenarioStreamer) Renew(context.Context, string, time.Duration) error { return nil }

type scenarioController struct {
	assignment   playerclient.Assignment
	envelope     release.EnvelopeV1
	data         []byte
	metadataSize int64
	offline      bool
}

func (c *scenarioController) check() error {
	if c.offline {
		return errors.New("scenario controller offline")
	}
	return nil
}
func (c *scenarioController) PutCapabilities(context.Context, playerclient.CapabilityReport, string) error {
	return c.check()
}
func (c *scenarioController) GetAssignment(context.Context, string, time.Duration) (playerclient.AssignmentResult, error) {
	if err := c.check(); err != nil {
		return playerclient.AssignmentResult{}, err
	}
	return playerclient.AssignmentResult{Assignment: c.assignment, ETag: `"scenario-1"`}, nil
}
func (c *scenarioController) FetchRelease(context.Context, string) (playerclient.ReleaseEnvelope, error) {
	if err := c.check(); err != nil {
		return release.EnvelopeV1{}, err
	}
	return c.envelope, nil
}
func (c *scenarioController) Report(context.Context, playerclient.NodeReport, string) (bool, error) {
	return false, c.check()
}
func (c *scenarioController) AppendEvents(context.Context, playerclient.EventBatch, string) (playerclient.EventAcknowledgement, error) {
	if err := c.check(); err != nil {
		return playerclient.EventAcknowledgement{}, err
	}
	return playerclient.EventAcknowledgement{Epoch: "epoch-1", AcknowledgedThrough: 100}, nil
}
func (c *scenarioController) HeadArtifact(context.Context, string) (playerclient.ArtifactMetadata, error) {
	if err := c.check(); err != nil {
		return playerclient.ArtifactMetadata{}, err
	}
	return playerclient.ArtifactMetadata{Size: c.metadataSize, ETag: `"scenario"`, AcceptRanges: "bytes"}, nil
}
func (c *scenarioController) GetArtifact(context.Context, string, *playerclient.ByteRange, string) (playerclient.ArtifactContent, error) {
	if err := c.check(); err != nil {
		return playerclient.ArtifactContent{}, err
	}
	return playerclient.ArtifactContent{Body: io.NopCloser(bytes.NewReader(c.data)), Size: int64(len(c.data)), ETag: `"scenario"`, AcceptRanges: "bytes"}, nil
}

var _ runtime.Controller = (*scenarioController)(nil)
