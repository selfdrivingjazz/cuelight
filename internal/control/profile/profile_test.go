package profile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
)

type fakeFleet struct {
	nodes       map[string]bool
	current     map[string]assignment.Assignment
	profiles    map[string]Profile
	activations []Activation
	failNodes   map[string]error
	replays     map[string]assignment.Assignment
	sequence    int
}

func newFleet(nodes ...string) *fakeFleet {
	f := &fakeFleet{nodes: map[string]bool{}, current: map[string]assignment.Assignment{}, profiles: map[string]Profile{}, failNodes: map[string]error{}, replays: map[string]assignment.Assignment{}}
	for _, node := range nodes {
		f.nodes[node] = true
	}
	return f
}

func (f *fakeFleet) nextID(prefix string) string {
	f.sequence++
	return fmt.Sprintf("%s-%d", prefix, f.sequence)
}

func (f *fakeFleet) AssignTarget(_ context.Context, mutation assignment.Mutation, nodeID string, target assignment.Target) (assignment.PutResult, error) {
	if err := f.failNodes[nodeID]; err != nil {
		return assignment.PutResult{}, err
	}
	if replay, ok := f.replays[mutation.IdempotencyKey]; ok {
		return assignment.PutResult{Assignment: replay}, nil
	}
	if !f.nodes[nodeID] {
		return assignment.PutResult{}, errors.New("node not found")
	}
	mode := target.DeliveryMode
	if mode == "" {
		mode = assignment.DeliveryCacheThenPlay
	}
	previous := f.current[nodeID]
	next := assignment.Assignment{ID: f.nextID("assignment"), NodeID: nodeID, MediaID: target.MediaID, PlaylistID: target.PlaylistID, DeliveryMode: mode, RemoteInput: target.RemoteInput, ShowQR: target.ShowQR, DesiredGeneration: previous.DesiredGeneration + 1}
	f.current[nodeID] = next
	f.replays[mutation.IdempotencyKey] = next
	return assignment.PutResult{Assignment: next, Created: true}, nil
}

func (f *fakeFleet) ListProfiles(context.Context) ([]Profile, error) { return nil, nil }
func (f *fakeFleet) GetProfile(_ context.Context, id string) (Profile, error) {
	p, ok := f.profiles[id]
	if !ok {
		return Profile{}, ErrNotFound
	}
	screens := p.Screens[:0:0]
	for _, screen := range p.Screens {
		if f.nodeKnown(screen.NodeID) {
			screens = append(screens, screen)
		}
	}
	p.Screens = screens
	return p, nil
}

// nodeKnown mirrors ON DELETE CASCADE: deleted nodes (absent from the map)
// drop out of profile screens, revoked nodes (false) remain.
func (f *fakeFleet) nodeKnown(id string) bool { _, ok := f.nodes[id]; return ok }

func (f *fakeFleet) CreateProfile(_ context.Context, actorID string, input Input) (Profile, error) {
	p := Profile{ID: f.nextID("profile"), Name: input.Name, Description: input.Description, Screens: input.Screens, Version: 1, CreatedBy: actorID, UpdatedBy: actorID}
	f.profiles[p.ID] = p
	return p, nil
}
func (f *fakeFleet) UpdateProfile(context.Context, string, string, int64, Input) (Profile, error) {
	return Profile{}, errors.New("unused")
}
func (f *fakeFleet) DeleteProfile(context.Context, string) error { return errors.New("unused") }
func (f *fakeFleet) InsertActivation(_ context.Context, a Activation) (Activation, error) {
	a.ID = f.nextID("activation")
	a.ActivatedAt = time.Now()
	f.activations = append(f.activations, a)
	return a, nil
}
func (f *fakeFleet) LatestActivation(context.Context) (Activation, bool, error) {
	if len(f.activations) == 0 {
		return Activation{}, false, nil
	}
	return f.activations[len(f.activations)-1], true, nil
}
func (f *fakeFleet) ActiveNode(_ context.Context, id string) (bool, error) { return f.nodes[id], nil }
func (f *fakeFleet) CurrentAssignment(_ context.Context, id string) (assignment.Assignment, bool, error) {
	a, ok := f.current[id]
	return a, ok, nil
}

func activatedFleet(t *testing.T) (*fakeFleet, Service, Profile) {
	t.Helper()
	fleet := newFleet("node-a", "node-b")
	service := Service{Repository: fleet, Assigner: fleet}
	ctx := context.Background()
	if _, err := fleet.AssignTarget(ctx, assignment.Mutation{ActorID: "actor", IdempotencyKey: "seed-b"}, "node-b", assignment.Target{MediaID: "media-b"}); err != nil {
		t.Fatal(err)
	}
	profile, err := service.Create(ctx, "actor", Input{Name: "Lobby", Screens: []Screen{{NodeID: "node-a", MediaID: "media-a", RemoteInput: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return fleet, service, profile
}

func TestActivateAssignsProfileScreensAndLeavesOtherNodesUntouched(t *testing.T) {
	fleet, service, profile := activatedFleet(t)
	ctx := context.Background()
	before := fleet.current["node-b"]

	activation, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activation.Results) != 1 || activation.Results[0].NodeID != "node-a" || activation.Results[0].AssignmentID == "" || activation.Results[0].Error != "" {
		t.Fatalf("results = %#v", activation.Results)
	}
	if got := fleet.current["node-a"]; got.MediaID != "media-a" || !got.RemoteInput || got.ID != activation.Results[0].AssignmentID {
		t.Fatalf("node-a assignment = %#v", got)
	}
	if after := fleet.current["node-b"]; after != before {
		t.Fatalf("node-b changed from %#v to %#v", before, after)
	}

	status, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ProfileID != profile.ID || status.ProfileName != "Lobby" || status.ActivatedAt == nil || status.Drifted || len(status.Drift) != 0 {
		t.Fatalf("status = %#v", status)
	}
}

func TestActiveReportsAssignmentChangedAfterReassignment(t *testing.T) {
	fleet, service, profile := activatedFleet(t)
	ctx := context.Background()
	if _, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fleet.AssignTarget(ctx, assignment.Mutation{ActorID: "actor", IdempotencyKey: "manual"}, "node-a", assignment.Target{MediaID: "media-other"}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Drifted || len(status.Drift) != 1 || status.Drift[0] != (Drift{NodeID: "node-a", Reason: DriftAssignmentChanged}) {
		t.Fatalf("status = %#v", status)
	}
}

func TestActiveReportsNodeMissingForRevokedAndDeletedNodes(t *testing.T) {
	for name, remove := range map[string]func(*fakeFleet){
		"revoked": func(f *fakeFleet) { f.nodes["node-a"] = false },
		"deleted": func(f *fakeFleet) { delete(f.nodes, "node-a") },
	} {
		t.Run(name, func(t *testing.T) {
			fleet, service, profile := activatedFleet(t)
			ctx := context.Background()
			if _, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID); err != nil {
				t.Fatal(err)
			}
			remove(fleet)
			status, err := service.Active(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !status.Drifted || len(status.Drift) != 1 || status.Drift[0] != (Drift{NodeID: "node-a", Reason: DriftNodeMissing}) {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestActivateReportsPartialFailurePerNode(t *testing.T) {
	fleet := newFleet("node-a", "node-b")
	fleet.failNodes["node-b"] = assignment.ErrTargetNotFound
	service := Service{Repository: fleet, Assigner: fleet}
	ctx := context.Background()
	profile, err := service.Create(ctx, "actor", Input{Name: "Wall", Screens: []Screen{{NodeID: "node-a", MediaID: "media-a"}, {NodeID: "node-b", PlaylistID: "playlist-b"}}})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activation.Results) != 2 {
		t.Fatalf("results = %#v", activation.Results)
	}
	if a := activation.Results[0]; a.NodeID != "node-a" || a.AssignmentID == "" || a.Error != "" {
		t.Fatalf("node-a result = %#v", a)
	}
	if b := activation.Results[1]; b.NodeID != "node-b" || b.AssignmentID != "" || b.Error == "" {
		t.Fatalf("node-b result = %#v", b)
	}
	status, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Drifted || len(status.Drift) != 1 || status.Drift[0] != (Drift{NodeID: "node-b", Reason: DriftUnassigned}) {
		t.Fatalf("status = %#v", status)
	}
}

func TestRetriedActivationReplaysAssignments(t *testing.T) {
	fleet, service, profile := activatedFleet(t)
	ctx := context.Background()
	first, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation := fleet.current["node-a"].DesiredGeneration
	second, err := service.Activate(ctx, "actor", "activation-key-0001", profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Results[0].AssignmentID != first.Results[0].AssignmentID || fleet.current["node-a"].DesiredGeneration != generation {
		t.Fatalf("retry created a new assignment: %#v vs %#v", first.Results, second.Results)
	}
}

func TestCreateRejectsInvalidScreens(t *testing.T) {
	service := Service{Repository: newFleet()}
	for name, screens := range map[string][]Screen{
		"no target":     {{NodeID: "n"}},
		"two targets":   {{NodeID: "n", MediaID: "m", PlaylistID: "p"}},
		"duplicate":     {{NodeID: "n", MediaID: "m"}, {NodeID: "n", MediaID: "m2"}},
		"bad delivery":  {{NodeID: "n", MediaID: "m", DeliveryMode: "fast"}},
		"array setting": {{NodeID: "n", MediaID: "m", Settings: []byte(`[]`)}},
	} {
		if _, err := service.Create(context.Background(), "actor", Input{Name: "x", Screens: screens}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
