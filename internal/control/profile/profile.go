// Package profile manages saved sets of per-screen assignments that operators
// activate at once. Activation assigns every profile screen through the
// assignment service; nodes outside the profile are never touched.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
)

var (
	ErrNotFound        = errors.New("profile not found")
	ErrInvalid         = errors.New("invalid profile")
	ErrNameConflict    = errors.New("profile name already exists")
	ErrVersionConflict = errors.New("profile version does not match")
)

// Drift reasons reported by Active.
const (
	DriftAssignmentChanged = "assignment_changed"
	DriftUnassigned        = "unassigned"
	DriftNodeMissing       = "node_missing"
)

// Screen is one node's saved assignment. Exactly one of MediaID and
// PlaylistID is set. Settings is reserved for per-screen settings that are
// not assignment options; it is always a JSON object.
type Screen struct {
	NodeID       string
	MediaID      string
	PlaylistID   string
	DeliveryMode string
	RemoteInput  bool
	ShowQR       bool
	Settings     json.RawMessage
}

type Profile struct {
	ID              string
	Name            string
	Description     string
	Version         int64
	Screens         []Screen
	CreatedBy       string
	UpdatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastActivatedAt *time.Time
	Active          bool
}

type Input struct {
	Name        string
	Description string
	Screens     []Screen
}

type Result struct {
	NodeID       string `json:"nodeId"`
	AssignmentID string `json:"assignmentId,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Activation struct {
	ID          string
	ProfileID   string
	ActivatedBy string
	ActivatedAt time.Time
	Results     []Result
}

type Drift struct {
	NodeID string
	Reason string
}

// ActiveStatus describes the most recently activated profile across the
// fleet and whether any of its screens no longer match.
type ActiveStatus struct {
	ProfileID   string
	ProfileName string
	ActivatedAt *time.Time
	Drifted     bool
	Drift       []Drift
}

// Repository persists profiles and activations. Get/Update/Delete return
// ErrNotFound for unknown IDs; Create/Update return ErrNameConflict for a
// duplicate name; Update returns ErrVersionConflict for a stale version.
// LastActivatedAt/Active on returned profiles are filled by the repository.
type Repository interface {
	ListProfiles(context.Context) ([]Profile, error)
	GetProfile(context.Context, string) (Profile, error)
	CreateProfile(ctx context.Context, actorID string, input Input) (Profile, error)
	UpdateProfile(ctx context.Context, actorID, id string, expectedVersion int64, input Input) (Profile, error)
	DeleteProfile(context.Context, string) error
	InsertActivation(context.Context, Activation) (Activation, error)
	LatestActivation(context.Context) (Activation, bool, error)
	// ActiveNode reports whether the node exists and is still registered.
	ActiveNode(ctx context.Context, nodeID string) (bool, error)
	CurrentAssignment(ctx context.Context, nodeID string) (assignment.Assignment, bool, error)
}

type Assigner interface {
	AssignTarget(context.Context, assignment.Mutation, string, assignment.Target) (assignment.PutResult, error)
}

type Service struct {
	Repository Repository
	Assigner   Assigner
}

func (s Service) List(ctx context.Context) ([]Profile, error) {
	return s.Repository.ListProfiles(ctx)
}

func (s Service) Get(ctx context.Context, id string) (Profile, error) {
	return s.Repository.GetProfile(ctx, id)
}

func (s Service) Create(ctx context.Context, actorID string, input Input) (Profile, error) {
	input, err := normalize(input)
	if err != nil {
		return Profile{}, err
	}
	return s.Repository.CreateProfile(ctx, actorID, input)
}

func (s Service) Update(ctx context.Context, actorID, id string, expectedVersion int64, input Input) (Profile, error) {
	input, err := normalize(input)
	if err != nil {
		return Profile{}, err
	}
	return s.Repository.UpdateProfile(ctx, actorID, id, expectedVersion, input)
}

func (s Service) Delete(ctx context.Context, id string) error {
	return s.Repository.DeleteProfile(ctx, id)
}

// Activate assigns every screen of the profile. Each node is assigned
// independently against its current generation; a failure on one node is
// reported in that node's result and does not stop the others. The
// idempotency key is scoped per node so a retried activation replays
// assignments that already succeeded instead of creating new generations.
func (s Service) Activate(ctx context.Context, actorID, idempotencyKey, id string) (Activation, error) {
	if actorID == "" || idempotencyKey == "" {
		return Activation{}, assignment.ErrInvalidMutation
	}
	profile, err := s.Repository.GetProfile(ctx, id)
	if err != nil {
		return Activation{}, err
	}
	results := make([]Result, 0, len(profile.Screens))
	for _, screen := range profile.Screens {
		result := Result{NodeID: screen.NodeID}
		put, err := s.Assigner.AssignTarget(ctx,
			assignment.Mutation{ActorID: actorID, IdempotencyKey: idempotencyKey + ":" + profile.ID + ":" + screen.NodeID},
			screen.NodeID,
			assignment.Target{
				MediaID:      screen.MediaID,
				PlaylistID:   screen.PlaylistID,
				DeliveryMode: screen.DeliveryMode,
				RemoteInput:  &screen.RemoteInput,
				ShowQR:       &screen.ShowQR,
			})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Activation{}, ctxErr
			}
			result.Error = err.Error()
		} else {
			result.AssignmentID = put.Assignment.ID
		}
		results = append(results, result)
	}
	return s.Repository.InsertActivation(ctx, Activation{ProfileID: profile.ID, ActivatedBy: actorID, Results: results})
}

// Active reports the last activated profile and its drift. Drift covers the
// profile's current screens plus nodes that were part of the activation but
// have since been removed (their screens cascade away with the node).
func (s Service) Active(ctx context.Context) (ActiveStatus, error) {
	activation, found, err := s.Repository.LatestActivation(ctx)
	if err != nil || !found {
		return ActiveStatus{Drift: []Drift{}}, err
	}
	profile, err := s.Repository.GetProfile(ctx, activation.ProfileID)
	if err != nil {
		return ActiveStatus{}, err
	}
	at := activation.ActivatedAt
	status := ActiveStatus{ProfileID: profile.ID, ProfileName: profile.Name, ActivatedAt: &at, Drift: []Drift{}}
	seen := make(map[string]bool, len(profile.Screens))
	for _, screen := range profile.Screens {
		seen[screen.NodeID] = true
		reason, err := s.screenDrift(ctx, screen)
		if err != nil {
			return ActiveStatus{}, err
		}
		if reason != "" {
			status.Drift = append(status.Drift, Drift{NodeID: screen.NodeID, Reason: reason})
		}
	}
	for _, result := range activation.Results {
		if seen[result.NodeID] {
			continue
		}
		seen[result.NodeID] = true
		active, err := s.Repository.ActiveNode(ctx, result.NodeID)
		if err != nil {
			return ActiveStatus{}, err
		}
		if !active {
			status.Drift = append(status.Drift, Drift{NodeID: result.NodeID, Reason: DriftNodeMissing})
		}
	}
	status.Drifted = len(status.Drift) > 0
	return status, nil
}

func (s Service) screenDrift(ctx context.Context, screen Screen) (string, error) {
	active, err := s.Repository.ActiveNode(ctx, screen.NodeID)
	if err != nil {
		return "", err
	}
	if !active {
		return DriftNodeMissing, nil
	}
	current, found, err := s.Repository.CurrentAssignment(ctx, screen.NodeID)
	if err != nil {
		return "", err
	}
	if !found {
		return DriftUnassigned, nil
	}
	if !Matches(screen, current) {
		return DriftAssignmentChanged, nil
	}
	return "", nil
}

// Matches reports whether an assignment carries the screen's target and
// options. An empty screen delivery mode accepts the server-chosen default.
func Matches(screen Screen, current assignment.Assignment) bool {
	return current.MediaID == screen.MediaID &&
		current.PlaylistID == screen.PlaylistID &&
		(screen.DeliveryMode == "" || current.DeliveryMode == screen.DeliveryMode) &&
		current.RemoteInput == screen.RemoteInput &&
		current.ShowQR == screen.ShowQR
}

func normalize(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 200 {
		return Input{}, fmt.Errorf("%w: name must be 1-200 characters", ErrInvalid)
	}
	if len(input.Description) > 2000 {
		return Input{}, fmt.Errorf("%w: description must be at most 2000 characters", ErrInvalid)
	}
	nodes := make(map[string]bool, len(input.Screens))
	screens := make([]Screen, 0, len(input.Screens))
	for _, screen := range input.Screens {
		screen.NodeID = strings.TrimSpace(screen.NodeID)
		if screen.NodeID == "" {
			return Input{}, fmt.Errorf("%w: screen nodeId is required", ErrInvalid)
		}
		if nodes[screen.NodeID] {
			return Input{}, fmt.Errorf("%w: node %s appears more than once", ErrInvalid, screen.NodeID)
		}
		nodes[screen.NodeID] = true
		if (screen.MediaID == "") == (screen.PlaylistID == "") {
			return Input{}, fmt.Errorf("%w: screen %s needs exactly one of mediaId and playlistId", ErrInvalid, screen.NodeID)
		}
		switch screen.DeliveryMode {
		case "", assignment.DeliveryStream, assignment.DeliveryCacheThenPlay:
		default:
			return Input{}, fmt.Errorf("%w: screen %s has unknown delivery mode %q", ErrInvalid, screen.NodeID, screen.DeliveryMode)
		}
		if len(screen.Settings) == 0 {
			screen.Settings = json.RawMessage(`{}`)
		} else {
			var settings map[string]any
			if err := json.Unmarshal(screen.Settings, &settings); err != nil || settings == nil {
				return Input{}, fmt.Errorf("%w: screen %s settings must be a JSON object", ErrInvalid, screen.NodeID)
			}
		}
		screens = append(screens, screen)
	}
	input.Screens = screens
	return input, nil
}
