package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/control/operatorapi"
	"github.com/jmilldotdev/cuelight/internal/control/profile"
)

func TestPostgresProfileActivationOverHTTP(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-0000000000d1"
	nodeA := "00000000-0000-4000-8000-0000000000d2"
	nodeB := "00000000-0000-4000-8000-0000000000d3"
	pageA := "00000000-0000-4000-8000-0000000000d4"
	pageB := "00000000-0000-4000-8000-0000000000d5"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{nodeA, nodeB} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, $2, 'active', $3)`, node, node, node); err != nil {
			t.Fatal(err)
		}
	}
	for id, url := range map[string]string{pageA: "https://a.example.test/", pageB: "https://b.example.test/"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO media (id, kind, url, title, version, created_by, updated_by) VALUES ($1, 'web', $2, $2, 1, $3, $3)`, id, url, actorID); err != nil {
			t.Fatal(err)
		}
	}
	assigner := assignment.Service{Assignments: repository, Releases: targetTestPublisher(t, repository)}
	handler := operatorapi.NewHandler(operatorapi.Dependencies{
		Auth:     targetTestAuth{actorID: actorID},
		Profiles: profile.Service{Repository: ProfileRepository{Store: repository}, Assigner: assigner},
	})
	call := func(method, path, key, body string, out any) int {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if out != nil {
			if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
				t.Fatalf("%s %s: %v: %s", method, path, err, recorder.Body.String())
			}
		}
		return recorder.Code
	}
	assign := func(node, media, key string) assignment.Assignment {
		t.Helper()
		result, err := assigner.AssignTarget(ctx, assignment.Mutation{ActorID: actorID, IdempotencyKey: key}, node, assignment.Target{MediaID: media})
		if err != nil {
			t.Fatal(err)
		}
		return result.Assignment
	}
	untouched := assign(nodeB, pageB, "seed-node-b")

	var created operatorapi.Profile
	if code := call(http.MethodPost, "/profiles", "", `{"name":"Lobby","screens":[{"nodeId":"`+nodeA+`","mediaId":"`+pageA+`","remoteInput":true,"showQr":false}]}`, &created); code != http.StatusCreated {
		t.Fatalf("create = %d %#v", code, created)
	}
	var activation operatorapi.ProfileActivation
	if code := call(http.MethodPost, "/profiles/"+created.ID+"/activate", "activate-lobby-0001", "", &activation); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	if len(activation.Results) != 1 || activation.Results[0].NodeID != nodeA || activation.Results[0].AssignmentID == "" || activation.Results[0].Error != "" {
		t.Fatalf("activation = %#v", activation)
	}
	currentA, _, err := repository.CurrentAssignment(ctx, nodeA)
	if err != nil || currentA.MediaID != pageA || !currentA.RemoteInput || currentA.ID != activation.Results[0].AssignmentID {
		t.Fatalf("node A assignment = %#v %v", currentA, err)
	}
	currentB, _, err := repository.CurrentAssignment(ctx, nodeB)
	if err != nil || currentB.ID != untouched.ID || currentB.DesiredGeneration != untouched.DesiredGeneration {
		t.Fatalf("node B changed: %#v -> %#v %v", untouched, currentB, err)
	}

	var active operatorapi.ActiveProfile
	if code := call(http.MethodGet, "/profiles/active", "", "", &active); code != http.StatusOK || active.ProfileID != created.ID || active.Drifted || len(active.Drift) != 0 {
		t.Fatalf("active after activation = %d %#v", code, active)
	}
	var fetched operatorapi.Profile
	if call(http.MethodGet, "/profiles/"+created.ID, "", "", &fetched); !fetched.Active || fetched.LastActivatedAt == nil {
		t.Fatalf("profile after activation = %#v", fetched)
	}

	assign(nodeA, pageB, "manual-node-a")
	active = operatorapi.ActiveProfile{}
	if code := call(http.MethodGet, "/profiles/active", "", "", &active); code != http.StatusOK || !active.Drifted || len(active.Drift) != 1 || active.Drift[0] != (operatorapi.ProfileDrift{NodeID: nodeA, Reason: profile.DriftAssignmentChanged}) {
		t.Fatalf("active after reassignment = %d %#v", code, active)
	}
}

func TestPostgresProfileRepositoryLifecycle(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-0000000000a1"
	nodeA := "00000000-0000-4000-8000-0000000000b1"
	nodeB := "00000000-0000-4000-8000-0000000000b2"
	mediaID := "00000000-0000-4000-8000-0000000000c1"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{nodeA, nodeB} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, $2, 'active', $3)`, node, node, node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO media (id, kind, url, title, version, created_by, updated_by) VALUES ($1, 'web', 'https://example.test/', 'Example', 1, $2, $2)`, mediaID, actorID); err != nil {
		t.Fatal(err)
	}
	profiles := ProfileRepository{Store: repository}
	service := profile.Service{Repository: profiles}

	created, err := service.Create(ctx, actorID, profile.Input{Name: "Lobby", Screens: []profile.Screen{
		{NodeID: nodeA, MediaID: mediaID, DeliveryMode: "stream", RemoteInput: true, Settings: []byte(`{"volume":3}`)},
		{NodeID: nodeB, MediaID: mediaID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || len(created.Screens) != 2 || created.Active || created.LastActivatedAt != nil {
		t.Fatalf("created = %#v", created)
	}
	if _, err := service.Create(ctx, actorID, profile.Input{Name: "Lobby"}); !errors.Is(err, profile.ErrNameConflict) {
		t.Fatalf("duplicate name err = %v", err)
	}
	if _, err := service.Create(ctx, actorID, profile.Input{Name: "Broken", Screens: []profile.Screen{{NodeID: nodeA, MediaID: "00000000-0000-4000-8000-0000000000ff"}}}); !errors.Is(err, profile.ErrInvalid) {
		t.Fatalf("unknown media err = %v", err)
	}

	if _, err := service.Update(ctx, actorID, created.ID, 7, profile.Input{Name: "Lobby"}); !errors.Is(err, profile.ErrVersionConflict) {
		t.Fatalf("stale version err = %v", err)
	}
	updated, err := service.Update(ctx, actorID, created.ID, 1, profile.Input{Name: "Lobby wall", Screens: []profile.Screen{{NodeID: nodeA, MediaID: mediaID, Settings: []byte(`{"volume":3}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != "Lobby wall" || len(updated.Screens) != 1 || string(updated.Screens[0].Settings) != `{"volume": 3}` {
		t.Fatalf("updated = %#v", updated)
	}

	other, err := service.Create(ctx, actorID, profile.Input{Name: "Other", Screens: []profile.Screen{{NodeID: nodeB, MediaID: mediaID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.InsertActivation(ctx, profile.Activation{ProfileID: other.ID, ActivatedBy: actorID, Results: []profile.Result{{NodeID: nodeB, Error: "boom"}}}); err != nil {
		t.Fatal(err)
	}
	activation, err := profiles.InsertActivation(ctx, profile.Activation{ProfileID: updated.ID, ActivatedBy: actorID, Results: []profile.Result{{NodeID: nodeA}}})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := profiles.LatestActivation(ctx)
	if err != nil || !found || latest.ID != activation.ID || len(latest.Results) != 1 || latest.Results[0].NodeID != nodeA {
		t.Fatalf("latest = %#v %v %v", latest, found, err)
	}
	list, err := service.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %#v %v", list, err)
	}
	for _, item := range list {
		if item.LastActivatedAt == nil || item.Active != (item.ID == updated.ID) {
			t.Fatalf("list item = %#v", item)
		}
	}

	if _, err := db.ExecContext(ctx, `UPDATE nodes SET credential_state = 'revoked' WHERE id = $1`, nodeB); err != nil {
		t.Fatal(err)
	}
	if active, err := profiles.ActiveNode(ctx, nodeB); err != nil || active {
		t.Fatalf("revoked node active = %v %v", active, err)
	}
	if active, err := profiles.ActiveNode(ctx, nodeA); err != nil || !active {
		t.Fatalf("node active = %v %v", active, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM nodes WHERE id = $1`, nodeA); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := service.Get(ctx, updated.ID)
	if err != nil || len(afterDelete.Screens) != 0 {
		t.Fatalf("deleting a node must drop its screens: %#v %v", afterDelete, err)
	}
	status, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Drifted || len(status.Drift) != 1 || status.Drift[0] != (profile.Drift{NodeID: nodeA, Reason: profile.DriftNodeMissing}) {
		t.Fatalf("status = %#v", status)
	}

	if err := service.Delete(ctx, updated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, updated.ID); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("deleted get err = %v", err)
	}
	if err := service.Delete(ctx, updated.ID); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}
