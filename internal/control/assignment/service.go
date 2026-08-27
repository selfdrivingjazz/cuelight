package assignment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	canonical "github.com/jmill/cable/internal/release"
)

const (
	DeliveryStream        = "stream"
	DeliveryCacheThenPlay = "cache_then_play"

	RoleDesired   = "desired"
	RoleCandidate = "candidate"
	RoleRendering = "rendering"
	RoleHealthy   = "healthy"
	RoleLKG       = "lkg"
)

var (
	ErrInvalidMutation     = errors.New("actor and idempotency key are required")
	ErrInvalidAssignment   = errors.New("invalid assignment")
	ErrReleaseNotFound     = errors.New("release not found")
	ErrReleaseMismatch     = errors.New("release ID and digest do not identify the same release")
	ErrDeliveryMode        = errors.New("delivery mode is not allowed by the release")
	ErrGenerationConflict  = errors.New("expected generation does not match current generation")
	ErrGenerationExhausted = errors.New("desired generation is exhausted")
	ErrIdempotencyConflict = errors.New("idempotency key was already used with a different request")
)

type Mutation struct {
	ActorID        string
	IdempotencyKey string
}

type PutInput struct {
	ReleaseID          string
	ReleaseDigest      string
	DeliveryMode       string
	ExpectedGeneration int64
}

type Assignment struct {
	ID                 string
	NodeID             string
	ReleaseID          string
	ReleaseDigest      string
	DeliveryMode       string
	ExpectedGeneration int64
	DesiredGeneration  int64
	CreatedBy          string
}

type PutResult struct {
	Assignment Assignment
	Created    bool
}

// Observation is the latest accepted node report snapshot. Repositories must
// only expose a report after enforcing event-epoch and sequence freshness.
type Observation struct {
	EventEpoch                 string
	Sequence                   int64
	DesiredGenerationSeen      int64
	ObservedGeneration         int64
	CandidateReleaseDigest     string
	RenderingReleaseDigest     string
	HealthyReleaseDigest       string
	LastKnownGoodReleaseDigest string
}

type Authorization struct {
	Role          string
	ReleaseID     string
	ReleaseDigest string
}

type IdempotencyRecord struct {
	RequestDigest string
	Assignment    Assignment
}

// Repository runs fn in a transaction scoped to nodeID.
type Repository interface {
	Transact(context.Context, string, func(Transaction) error) error
}

// Transaction is deliberately expressed in domain operations: assignment
// history remains append-only while the five authorization role slots are
// replaced atomically with the desired generation change.
type Transaction interface {
	CurrentGeneration() (int64, error)
	CurrentAssignment() (Assignment, bool, error)
	ReleaseByID(string) (canonical.EnvelopeV1, bool, error)
	AssignedRelease(releaseDigest string, atMostGeneration int64) (Assignment, bool, error)
	LatestObservation() (Observation, bool, error)
	Idempotency(actorID, key string) (IdempotencyRecord, bool, error)
	NewAssignmentID() (string, error)
	InsertAssignment(Assignment) error
	ReplaceAuthorizations([]Authorization) error
	PutIdempotency(actorID, key string, record IdempotencyRecord) error
}

type Service struct {
	Assignments Repository
}

func (s Service) Put(ctx context.Context, nodeID string, mutation Mutation, input PutInput) (PutResult, error) {
	if s.Assignments == nil {
		return PutResult{}, errors.New("assignment service is not configured")
	}
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return PutResult{}, ErrInvalidMutation
	}
	if err := validateInput(nodeID, input); err != nil {
		return PutResult{}, err
	}
	requestDigest, err := RequestDigest(nodeID, input)
	if err != nil {
		return PutResult{}, err
	}

	var result PutResult
	err = s.Assignments.Transact(ctx, nodeID, func(tx Transaction) error {
		record, found, err := tx.Idempotency(mutation.ActorID, mutation.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if record.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			result = PutResult{Assignment: record.Assignment, Created: false}
			return nil
		}

		currentGeneration, err := tx.CurrentGeneration()
		if err != nil {
			return err
		}
		if input.ExpectedGeneration != currentGeneration {
			return &GenerationConflictError{Expected: input.ExpectedGeneration, Actual: currentGeneration}
		}
		if currentGeneration == math.MaxInt64 {
			return ErrGenerationExhausted
		}

		envelope, found, err := tx.ReleaseByID(input.ReleaseID)
		if err != nil {
			return err
		}
		if !found {
			return ErrReleaseNotFound
		}
		if err := validateReleaseBinding(envelope, input.ReleaseID, input.ReleaseDigest); err != nil {
			return err
		}
		if !envelope.Payload.AllowsDeliveryMode(input.DeliveryMode) {
			return fmt.Errorf("%w: %q", ErrDeliveryMode, input.DeliveryMode)
		}

		assignmentID, err := tx.NewAssignmentID()
		if err != nil {
			return err
		}
		if assignmentID == "" {
			return fmt.Errorf("%w: repository returned an empty assignment ID", ErrInvalidAssignment)
		}
		created := Assignment{
			ID:                 assignmentID,
			NodeID:             nodeID,
			ReleaseID:          input.ReleaseID,
			ReleaseDigest:      input.ReleaseDigest,
			DeliveryMode:       input.DeliveryMode,
			ExpectedGeneration: input.ExpectedGeneration,
			DesiredGeneration:  currentGeneration + 1,
			CreatedBy:          mutation.ActorID,
		}
		if err := tx.InsertAssignment(created); err != nil {
			return err
		}
		if err := recompute(tx, created); err != nil {
			return err
		}
		if err := tx.PutIdempotency(mutation.ActorID, mutation.IdempotencyKey, IdempotencyRecord{
			RequestDigest: requestDigest,
			Assignment:    created,
		}); err != nil {
			return err
		}
		result = PutResult{Assignment: created, Created: true}
		return nil
	})
	if err != nil {
		return PutResult{}, err
	}
	return result, nil
}

// RecomputeAuthorizations rebuilds, rather than appends to, the authorization
// role set after a newer node report has become the repository's latest
// accepted snapshot.
func (s Service) RecomputeAuthorizations(ctx context.Context, nodeID string) error {
	if s.Assignments == nil {
		return errors.New("assignment service is not configured")
	}
	if nodeID == "" {
		return ErrInvalidAssignment
	}
	return s.Assignments.Transact(ctx, nodeID, func(tx Transaction) error {
		current, found, err := tx.CurrentAssignment()
		if err != nil {
			return err
		}
		if !found {
			return tx.ReplaceAuthorizations(nil)
		}
		return recompute(tx, current)
	})
}

func RequestDigest(nodeID string, input PutInput) (string, error) {
	body, err := json.Marshal(struct {
		NodeID string
		PutInput
	}{NodeID: nodeID, PutInput: input})
	if err != nil {
		return "", fmt.Errorf("encode assignment request: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

type GenerationConflictError struct {
	Expected int64
	Actual   int64
}

func (e *GenerationConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, current %d", ErrGenerationConflict, e.Expected, e.Actual)
}

func (e *GenerationConflictError) Unwrap() error { return ErrGenerationConflict }

func validateInput(nodeID string, input PutInput) error {
	if nodeID == "" || input.ReleaseID == "" || input.ExpectedGeneration < 0 {
		return ErrInvalidAssignment
	}
	if err := canonical.ValidateDigest(input.ReleaseDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAssignment, err)
	}
	if input.DeliveryMode != DeliveryStream && input.DeliveryMode != DeliveryCacheThenPlay {
		return fmt.Errorf("%w: %q", ErrDeliveryMode, input.DeliveryMode)
	}
	return nil
}

func validateReleaseBinding(envelope canonical.EnvelopeV1, releaseID, releaseDigest string) error {
	if envelope.ID != releaseID || envelope.Digest != releaseDigest {
		return ErrReleaseMismatch
	}
	actualDigest, _, err := canonical.Digest(envelope.Payload)
	if err != nil {
		return fmt.Errorf("%w: invalid stored payload: %v", ErrReleaseMismatch, err)
	}
	if actualDigest != releaseDigest {
		return ErrReleaseMismatch
	}
	return nil
}

func recompute(tx Transaction, desired Assignment) error {
	authorizations := make([]Authorization, 0, 5)
	authorizations = append(authorizations, authorization(RoleDesired, desired))

	observation, found, err := tx.LatestObservation()
	if err != nil {
		return err
	}
	if !found {
		return tx.ReplaceAuthorizations(authorizations)
	}

	observedLimit := minGeneration(observation.ObservedGeneration, desired.DesiredGeneration)
	candidateLimit := observation.DesiredGenerationSeen
	if observation.ObservedGeneration > candidateLimit {
		candidateLimit = observation.ObservedGeneration
	}
	candidateLimit = minGeneration(candidateLimit, desired.DesiredGeneration)

	roles := []struct {
		role   string
		digest string
		limit  int64
	}{
		{role: RoleCandidate, digest: observation.CandidateReleaseDigest, limit: candidateLimit},
		{role: RoleRendering, digest: observation.RenderingReleaseDigest, limit: observedLimit},
		{role: RoleHealthy, digest: observation.HealthyReleaseDigest, limit: observedLimit},
		{role: RoleLKG, digest: observation.LastKnownGoodReleaseDigest, limit: observedLimit},
	}
	for _, reported := range roles {
		if reported.digest == "" || reported.limit <= 0 {
			continue
		}
		if canonical.ValidateDigest(reported.digest) != nil {
			continue
		}
		assigned, found, err := tx.AssignedRelease(reported.digest, reported.limit)
		if err != nil {
			return err
		}
		if !found || assigned.DesiredGeneration <= 0 || assigned.DesiredGeneration > reported.limit {
			continue
		}
		authorizations = append(authorizations, authorization(reported.role, assigned))
	}
	return tx.ReplaceAuthorizations(authorizations)
}

func authorization(role string, assigned Assignment) Authorization {
	return Authorization{Role: role, ReleaseID: assigned.ReleaseID, ReleaseDigest: assigned.ReleaseDigest}
}

func minGeneration(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
