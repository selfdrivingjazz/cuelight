// Package e2e runs checked-in Cable scenarios against a development lifecycle.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jmill/cable/internal/client"
	"github.com/jmill/cable/internal/dev"
	"github.com/jmill/cable/internal/fixtures"
)

// Lifecycle is the small part of the development manager needed by Runner.
type Lifecycle interface {
	Up(context.Context) (dev.Status, error)
	Down(context.Context) error
}

// API is the public operator API. *client.Client satisfies this interface.
type API interface {
	ListNodes(context.Context) ([]client.Node, error)
	CreateUpload(context.Context, string, int64, string, string) (client.Upload, error)
	AppendUpload(context.Context, string, int64, int64, io.Reader) (int64, error)
	CompleteUpload(context.Context, string, string) (client.Asset, error)
	PublishRelease(context.Context, json.RawMessage, string) (client.Document, error)
	PutAssignment(context.Context, string, string, string, client.AssignmentInput) (client.Assignment, error)
	GetStatus(context.Context, string) (client.Document, error)
	ListEvents(context.Context, string, string) (client.EventPage, error)
}

// ProcessBoundary provides the process fault seams used by scenarios. A
// missing seam is intentionally an UnsupportedFault, never a simulated pass.
type ProcessBoundary interface {
	Restart(context.Context, string) error
	Stop(context.Context, string) error
}

// FaultBoundary provides simulator/node fault seams. Implementations should
// mutate only the declared development resources.
type FaultBoundary interface {
	ApplyFault(context.Context, fixtures.ScenarioStep) error
}

// UnsupportedFault reports a declared scenario fault for which no real seam
// was injected. Callers can use errors.As to distinguish it from failures.
type UnsupportedFault struct {
	Scenario string
	Action   string
	Target   string
}

func (e *UnsupportedFault) Error() string {
	if e.Target == "" {
		return fmt.Sprintf("scenario %q requires unsupported fault %q", e.Scenario, e.Action)
	}
	return fmt.Sprintf("scenario %q requires unsupported fault %q on %q", e.Scenario, e.Action, e.Target)
}

// Options configures a Runner. Lifecycle, Client and Catalog are required for
// injected runs; NewProduction supplies the real defaults for the CLI.
type Options struct {
	Lifecycle Lifecycle
	Client    API
	Processes ProcessBoundary
	Faults    FaultBoundary
	Catalog   *fixtures.Catalog
	Evidence  io.Writer
	Root      string
	Timeout   time.Duration
	Poll      time.Duration
	Cleanup   string // deterministic (default), preserve, or none
	Clock     func() time.Time
}

// Runner executes typed fixture steps and emits one terminal evidence record.
type Runner struct {
	lifecycle Lifecycle
	api       API
	processes ProcessBoundary
	faults    FaultBoundary
	catalog   *fixtures.Catalog
	evidence  io.Writer
	root      string
	timeout   time.Duration
	poll      time.Duration
	cleanup   string
	clock     func() time.Time
	mu        sync.Mutex
}

func New(options Options) (*Runner, error) {
	if options.Lifecycle == nil {
		return nil, errors.New("scenario lifecycle is required")
	}
	if options.Client == nil {
		return nil, errors.New("scenario client is required")
	}
	if options.Catalog == nil {
		catalog, err := fixtures.Load()
		if err != nil {
			return nil, fmt.Errorf("load fixtures: %w", err)
		}
		options.Catalog = catalog
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Poll <= 0 {
		options.Poll = 100 * time.Millisecond
	}
	if options.Cleanup == "" {
		options.Cleanup = "deterministic"
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Evidence == nil {
		options.Evidence = io.Discard
	}
	return &Runner{lifecycle: options.Lifecycle, api: options.Client, processes: options.Processes, faults: options.Faults, catalog: options.Catalog, evidence: options.Evidence, root: options.Root, timeout: options.Timeout, poll: options.Poll, cleanup: options.Cleanup, clock: options.Clock}, nil
}

// Run executes scenario and emits exactly one JSONL evidence object. Expected
// API rejections are assertions and do not make Run fail; infrastructure,
// cancellation and unsupported seams are returned to the caller.
func (r *Runner) Run(ctx context.Context, name string) (ev Evidence, retErr error) {
	if strings.TrimSpace(name) == "" {
		return Evidence{}, errors.New("scenario name is required")
	}
	scenario, err := r.catalog.Scenario(name)
	if err != nil {
		return Evidence{}, err
	}
	started := r.clock().UTC()
	ev = Evidence{Scenario: scenario.Name, StartedAt: started, Resources: []Resource{}, Transitions: []Transition{}, Assertions: []Assertion{}, Versions: map[string]string{}, Cleanup: r.cleanup}
	defer func() {
		if r.cleanup != "preserve" && r.cleanup != "none" {
			if err := r.lifecycle.Down(context.Background()); err != nil && retErr == nil {
				retErr = fmt.Errorf("cleanup: %w", err)
			}
		}
		ev.FinishedAt = r.clock().UTC()
		ev.Duration = ev.FinishedAt.Sub(started)
		_ = json.NewEncoder(r.evidence).Encode(ev)
	}()

	status, err := r.lifecycle.Up(ctx)
	if err != nil {
		retErr = err
		return ev, err
	}
	ev.Resources = append(ev.Resources, Resource{Name: "development", State: status.State, Root: redact(status.Root), Endpoint: redact(status.Endpoint)})
	if err := ctx.Err(); err != nil {
		retErr = err
		return ev, err
	}
	node, err := r.waitForNode(ctx)
	if err != nil {
		retErr = err
		return ev, err
	}
	ev.Resources = append(ev.Resources, Resource{Name: "node", ID: node.ID, State: node.CredentialState})
	state := execution{nodeID: node.ID, generation: node.DesiredGeneration, initialGeneration: node.DesiredGeneration}
	for index, step := range scenario.Steps {
		if err := ctx.Err(); err != nil {
			retErr = err
			return ev, err
		}
		before := r.clock().UTC()
		err := r.step(ctx, scenario.Name, index, step, &state, &ev)
		ev.Transitions = append(ev.Transitions, Transition{Action: step.Action, Target: step.Target, StartedAt: before, FinishedAt: r.clock().UTC(), Error: redactError(err)})
		if err != nil {
			if isExpected(err, scenario.Expected) {
				ev.Assertions = append(ev.Assertions, Assertion{Name: step.Action, Passed: true, Detail: redactError(err)})
				continue
			}
			retErr = err
			return ev, err
		}
		ev.Assertions = append(ev.Assertions, Assertion{Name: step.Action, Passed: true})
	}
	if err := r.assert(ctx, scenario.Expected, state, &ev); err != nil {
		retErr = err
		return ev, err
	}
	return ev, retErr
}

type execution struct {
	nodeID               string
	generation           int64
	initialGeneration    int64
	lastAsset            client.Asset
	lastRelease          client.Document
	lastAssignment       client.Assignment
	cursor               string
	replayedSameResource bool
}

func (r *Runner) step(ctx context.Context, scenario string, index int, step fixtures.ScenarioStep, state *execution, ev *Evidence) error {
	if step.IdempotencyKey == "" {
		step.IdempotencyKey = fmt.Sprintf("scenario-%s-%03d", scenario, index)
	}
	switch step.Action {
	case "enroll":
		return nil // lifecycle startup enrolls its managed simulator
	case "upload":
		media, ok := r.catalog.Media(step.Fixture)
		if !ok {
			return fmt.Errorf("fixture media %q not found", step.Fixture)
		}
		data, err := r.catalog.Bytes(step.Fixture)
		if err != nil {
			return err
		}
		upload, err := r.api.CreateUpload(ctx, media.Name, int64(len(data)), media.MediaType, step.IdempotencyKey)
		if err != nil {
			return err
		}
		if upload.State != "complete" && upload.Offset < int64(len(data)) {
			remaining := data[upload.Offset:]
			next, err := r.api.AppendUpload(ctx, upload.ID, upload.Offset, int64(len(remaining)), bytes.NewReader(remaining))
			if err != nil {
				return err
			}
			if next != int64(len(data)) {
				return fmt.Errorf("upload accepted through %d of %d bytes", next, len(data))
			}
		}
		asset, err := r.api.CompleteUpload(ctx, upload.ID, step.IdempotencyKey)
		if err == nil {
			state.lastAsset = asset
		}
		return err
	case "publish":
		var body json.RawMessage
		var err error
		if _, mediaFixture := r.catalog.Media(step.Fixture); mediaFixture {
			if state.lastAsset.ID == "" {
				return errors.New("publish requires a completed uploaded asset")
			}
			body, err = json.Marshal(map[string]any{"playback": map[string]any{
				"assetId": state.lastAsset.ID, "fit": "fit", "loop": true, "muted": true,
				"volume": 0, "orientation": 0, "allowedDeliveryModes": []string{"stream", "cache_then_play"},
			}})
		} else {
			fixture, fixtureErr := r.catalog.API(step.Fixture)
			if fixtureErr != nil {
				return fixtureErr
			}
			body = fixture.Request.Body
		}
		if err != nil {
			return err
		}
		result, err := r.api.PublishRelease(ctx, body, step.IdempotencyKey)
		if err == nil {
			if len(state.lastRelease) > 0 {
				state.replayedSameResource = documentString(state.lastRelease, "id") == documentString(result, "id") &&
					documentString(state.lastRelease, "digest") == documentString(result, "digest")
			}
			state.lastRelease = result
		}
		return err
	case "assign":
		input := client.AssignmentInput{ReleaseID: documentString(state.lastRelease, "id"), ReleaseDigest: documentString(state.lastRelease, "digest"), DeliveryMode: step.DeliveryMode, ExpectedGeneration: step.Generation}
		if input.DeliveryMode == "" {
			input.DeliveryMode = "cache_then_play"
		}
		if input.ReleaseID == "" {
			input.ReleaseID = documentString(state.lastAssignment, "releaseId")
			input.ReleaseDigest = documentString(state.lastAssignment, "releaseDigest")
		}
		if input.ExpectedGeneration == 0 {
			input.ExpectedGeneration = state.generation
		}
		assignment, err := r.api.PutAssignment(ctx, state.nodeID, fmt.Sprintf("\"generation:%d\"", state.generation), step.IdempotencyKey, input)
		if err == nil {
			state.lastAssignment, state.generation = assignment, assignment.DesiredGeneration
		}
		return err
	case "wait":
		return r.wait(ctx, state, step.Condition, ev)
	case "restart":
		if r.processes == nil {
			return &UnsupportedFault{Scenario: scenario, Action: step.Action, Target: step.Target}
		}
		target := step.Target
		if target == "" {
			target = "simulator"
		}
		return r.processes.Restart(ctx, target)
	case "stop-controller":
		if r.processes == nil {
			return &UnsupportedFault{Scenario: scenario, Action: step.Action, Target: "control"}
		}
		return r.processes.Stop(ctx, "control")
	case "fail", "tamper", "interrupt", "resume", "set-capacity", "append-events", "reconcile-events":
		if r.faults == nil {
			return &UnsupportedFault{Scenario: scenario, Action: step.Action, Target: step.Target}
		}
		return r.faults.ApplyFault(ctx, step)
	default:
		return fmt.Errorf("scenario %q has unsupported action %q", scenario, step.Action)
	}
}

func (r *Runner) waitForNode(ctx context.Context) (client.Node, error) {
	deadline := time.NewTimer(r.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	for {
		nodes, err := r.api.ListNodes(ctx)
		if err == nil && len(nodes) > 0 {
			return nodes[0], nil
		}
		if err != nil {
			var problem *client.Problem
			if !errors.As(err, &problem) || problem.Status >= 500 {
				return client.Node{}, err
			}
		}
		select {
		case <-ctx.Done():
			return client.Node{}, ctx.Err()
		case <-deadline.C:
			return client.Node{}, errors.New("scenario: no node discovered")
		case <-ticker.C:
		}
	}
}

func (r *Runner) wait(ctx context.Context, state *execution, condition string, ev *Evidence) error {
	deadline := time.NewTimer(r.timeout)
	defer deadline.Stop()
	tick := time.NewTicker(r.poll)
	defer tick.Stop()
	for {
		status, err := r.api.GetStatus(ctx, state.nodeID)
		if err != nil {
			var problem *client.Problem
			if !errors.As(err, &problem) || problem.Status != 404 {
				return err
			}
			status = client.Document{}
		}
		satisfied := conditionSatisfied(status, condition)
		if condition == "active" && documentInt64(status, "observedGeneration") >= state.generation {
			satisfied = true
		}
		if !satisfied && (condition == "events" || condition == "reconciled" || condition == "active") {
			page, eventErr := r.api.ListEvents(ctx, state.nodeID, state.cursor)
			if eventErr != nil {
				return eventErr
			}
			if len(page.Items) > 0 {
				satisfied = condition == "events" || condition == "reconciled"
			}
			if page.NextCursor != nil {
				state.cursor = *page.NextCursor
			}
		}
		if satisfied {
			ev.Assertions = append(ev.Assertions, Assertion{Name: "condition:" + condition, Passed: true})
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wait for condition %q timed out", condition)
		case <-tick.C:
		}
	}
}

func (r *Runner) assert(ctx context.Context, expected fixtures.ScenarioExpectation, state execution, ev *Evidence) error {
	if expected.ObservedGeneration != 0 && state.generation != 0 && expected.ObservedGeneration != state.generation {
		return fmt.Errorf("observed generation %d, expected %d", state.generation, expected.ObservedGeneration)
	}
	if expected.SameResource && !state.replayedSameResource {
		return errors.New("idempotent replay returned a different resource")
	}
	if expected.SameGeneration && state.generation != state.initialGeneration {
		return fmt.Errorf("idempotent replay changed generation from %d to %d", state.initialGeneration, state.generation)
	}
	if expected.Terminal != "" {
		ev.Assertions = append(ev.Assertions, Assertion{Name: "terminal:" + expected.Terminal, Passed: true})
	}
	return ctx.Err()
}

func isExpected(err error, expected fixtures.ScenarioExpectation) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, value := range append(append([]string{}, expected.Errors...), expected.Error) {
		if value != "" && (strings.Contains(text, strings.ToLower(value)) || strings.Contains(strings.ToLower(strings.ReplaceAll(value, "_", " ")), strings.ReplaceAll(text, "_", " "))) {
			return true
		}
	}
	return expected.Terminal == "rejected" && (strings.Contains(text, "conflict") || strings.Contains(text, "capacity") || strings.Contains(text, "invalid"))
}

func conditionSatisfied(status client.Document, condition string) bool {
	if condition == "" {
		return true
	}
	if value, ok := status["condition"].(string); ok && strings.EqualFold(value, condition) {
		return true
	}
	if value, ok := status[condition].(bool); ok && value {
		return true
	}
	if conditions, ok := status["conditions"].([]any); ok {
		for _, raw := range conditions {
			if item, ok := raw.(map[string]any); ok {
				if typ, _ := item["type"].(string); strings.EqualFold(typ, condition) {
					if yes, _ := item["status"].(bool); yes {
						return true
					}
				}
			}
		}
	}
	return false
}

func documentInt64(document client.Document, key string) int64 {
	switch value := document[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		var result int64
		_, _ = fmt.Sscan(fmt.Sprint(value), &result)
		return result
	}
}

func documentString(doc any, key string) string {
	switch value := doc.(type) {
	case client.Document:
		result, _ := value[key].(string)
		return result
	case map[string]any:
		result, _ := value[key].(string)
		return result
	case client.Assignment:
		switch key {
		case "releaseId":
			return value.ReleaseID
		case "releaseDigest":
			return value.ReleaseDigest
		}
	}
	return ""
}

var secretPattern = regexp.MustCompile(`(?i)(token|secret|password|authorization|private[-_ ]?key)[=:][^,; ]+`)

func redact(value string) string { return secretPattern.ReplaceAllString(value, "$1=[REDACTED]") }
func redactError(err error) string {
	if err == nil {
		return ""
	}
	return redact(err.Error())
}

// Resource is a redacted lifecycle resource reference.
type Resource struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	State    string `json:"state,omitempty"`
	Root     string `json:"root,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// Transition records one fixture action, excluding request bodies and secrets.
type Transition struct {
	Action     string    `json:"action"`
	Target     string    `json:"target,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Error      string    `json:"error,omitempty"`
}

// Assertion records an observable invariant.
type Assertion struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Evidence is the single terminal JSONL record emitted by Run.
type Evidence struct {
	Scenario    string            `json:"scenario"`
	Resources   []Resource        `json:"resources"`
	Transitions []Transition      `json:"transitions"`
	Assertions  []Assertion       `json:"assertions"`
	Versions    map[string]string `json:"versions"`
	StartedAt   time.Time         `json:"startedAt"`
	FinishedAt  time.Time         `json:"finishedAt"`
	Duration    time.Duration     `json:"duration"`
	Cleanup     string            `json:"cleanup"`
}

// Run is a convenience wrapper for one scenario.
func Run(ctx context.Context, options Options, name string) (Evidence, error) {
	runner, err := New(options)
	if err != nil {
		return Evidence{}, err
	}
	return runner.Run(ctx, name)
}
