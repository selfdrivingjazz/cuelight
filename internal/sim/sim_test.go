package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/selfdrivingjazz/cuelight/internal/player/runtime"
)

func TestFakeOutputReadinessAndCapacityAreInjectable(t *testing.T) {
	output := NewFakeOutput(false, 17)
	if output.Ready() {
		t.Fatal("new fake output unexpectedly ready")
	}
	free, err := output.AvailableBytes(context.Background())
	if err != nil || free != 17 {
		t.Fatalf("capacity=%d err=%v", free, err)
	}
	output.SetReady(true)
	output.SetCapacity(42)
	if !output.Ready() {
		t.Fatal("output did not become ready")
	}
	free, _ = output.AvailableBytes(context.Background())
	if free != 42 {
		t.Fatalf("capacity=%d", free)
	}
}

func TestScenariosEmitStableSingleJSONObservation(t *testing.T) {
	for _, name := range []string{"enroll", "stream-cache", "candidate-failure", "offline-reconnect", "event-overflow", "insufficient-capacity", "no-output-recovery"} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := RunScenario(context.Background(), ScenarioConfig{Name: name, Evidence: &output}); err != nil {
				t.Fatal(err)
			}
			if strings.Count(strings.TrimSpace(output.String()), "\n") != 0 {
				t.Fatal("scenario emitted more than one JSONL record")
			}
			var observation Observation
			if err := json.Unmarshal(output.Bytes(), &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Kind != name || observation.NodeID != "sim-node" || observation.EventEpoch != "epoch-1" {
				t.Fatalf("observation=%+v", observation)
			}
			if name == "insufficient-capacity" && !observation.Snapshot.Safe {
				t.Fatal("capacity failure did not enter safe mode")
			}
			if name == "candidate-failure" && !errors.Is(errors.New(observation.Error), runtime.ErrSafeMode) && observation.Error == "" {
				t.Fatal("candidate failure lacks evidence")
			}
		})
	}
}
