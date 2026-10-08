package cableguide

import (
	"context"
	"errors"
	"testing"

	"github.com/jmill/cable/internal/control/assignment"
)

type conversions struct {
	pending   []LegacyAssignment
	completed []string
}

func (c *conversions) PendingCableConversions(context.Context) ([]LegacyAssignment, error) {
	return c.pending, nil
}
func (c *conversions) CompleteCableConversion(_ context.Context, nodeID string) error {
	c.completed = append(c.completed, nodeID)
	return nil
}

type assignerFunc func(string, assignment.Target) error

func (f assignerFunc) AssignTarget(_ context.Context, _ assignment.Mutation, nodeID string, target assignment.Target) (assignment.PutResult, error) {
	return assignment.PutResult{}, f(nodeID, target)
}

func TestConvertLegacyAssignmentsKeepsOnlyRetryableFailures(t *testing.T) {
	repository := &conversions{pending: []LegacyAssignment{
		{NodeID: "converted", MediaID: "cable-a", DesiredGeneration: 3, RemoteInput: true, ShowQR: true, ActorID: "actor"},
		{NodeID: "moved-on", MediaID: "cable-a", DesiredGeneration: 1, ActorID: "actor"},
		{NodeID: "transient", MediaID: "cable-b", DesiredGeneration: 2, ActorID: "actor"},
	}}
	var assigned assignment.Target
	err := ConvertLegacyAssignments(context.Background(), repository, assignerFunc(func(nodeID string, target assignment.Target) error {
		switch nodeID {
		case "converted":
			assigned = target
			return nil
		case "moved-on":
			return &assignment.GenerationConflictError{Expected: 1, Actual: 5}
		default:
			return errors.New("database unavailable")
		}
	}))
	if err == nil {
		t.Fatal("transient failure was not reported")
	}
	if assigned.MediaID != "cable-a" || !assigned.RemoteInput || !assigned.ShowQR || assigned.ExpectedGeneration == nil || *assigned.ExpectedGeneration != 3 {
		t.Fatalf("target = %+v", assigned)
	}
	if len(repository.completed) != 2 || repository.completed[0] != "converted" || repository.completed[1] != "moved-on" {
		t.Fatalf("completed = %v", repository.completed)
	}
}
