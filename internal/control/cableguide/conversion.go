package cableguide

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmill/cable/internal/control/assignment"
)

// LegacyAssignment is a node whose current assignment was a compiled Cable
// release when migration 0013 ran.
type LegacyAssignment struct {
	NodeID            string
	MediaID           string
	DesiredGeneration int64
	RemoteInput       bool
	ShowQR            bool
	ActorID           string
}

type ConversionRepository interface {
	PendingCableConversions(context.Context) ([]LegacyAssignment, error)
	CompleteCableConversion(ctx context.Context, nodeID string) error
}

type Assigner interface {
	AssignTarget(context.Context, assignment.Mutation, string, assignment.Target) (assignment.PutResult, error)
}

// ConvertLegacyAssignments re-assigns every recorded legacy Cable node to its
// deployment's Cable media item. A row is cleared once converted, or when the
// node has since moved to another generation or the target is gone; other
// failures keep the row for the next start.
func ConvertLegacyAssignments(ctx context.Context, repository ConversionRepository, assigner Assigner) error {
	pending, err := repository.PendingCableConversions(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, legacy := range pending {
		generation := legacy.DesiredGeneration
		_, err := assigner.AssignTarget(ctx, assignment.Mutation{ActorID: legacy.ActorID, IdempotencyKey: "hosted-cable-conversion"}, legacy.NodeID, assignment.Target{
			MediaID: legacy.MediaID, RemoteInput: legacy.RemoteInput, ShowQR: legacy.ShowQR, ExpectedGeneration: &generation,
		})
		var conflict *assignment.GenerationConflictError
		if err != nil && !errors.As(err, &conflict) && !errors.Is(err, assignment.ErrTargetNotFound) {
			failures = append(failures, fmt.Errorf("convert Cable assignment for node %s: %w", legacy.NodeID, err))
			continue
		}
		if err := repository.CompleteCableConversion(ctx, legacy.NodeID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
