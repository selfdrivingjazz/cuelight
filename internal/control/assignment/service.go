package assignment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	canonical "github.com/jmilldotdev/cuelight/internal/release"
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
	// ErrTargetNotFound reports an unknown media item or playlist.
	ErrTargetNotFound = errors.New("assignment target not found")
)

type Mutation struct {
	ActorID        string
	IdempotencyKey string
}

// Target is what an operator assigns: exactly one media item or playlist.
// An empty DeliveryMode selects cache_then_play when the published release
// allows it and stream otherwise. A nil ExpectedGeneration assigns over
// whatever generation is current. A nil RemoteInput or ShowQR keeps the
// node's current assignment value (false without one); turning remote input
// off also turns off an inherited QR code.
type Target struct {
	MediaID            string
	PlaylistID         string
	DeliveryMode       string
	RemoteInput        *bool
	ShowQR             *bool
	ExpectedGeneration *int64
}

// Publisher publishes, or reuses by digest, the signed release presenting a
// target. Exactly one of mediaID and playlistID is set. The idempotency key
// scopes replay of the publication itself.
type Publisher interface {
	PublishTarget(ctx context.Context, actorID, idempotencyKey, mediaID, playlistID string) (canonical.EnvelopeV1, error)
}

type Assignment struct {
	ID                 string
	NodeID             string
	MediaID            string
	PlaylistID         string
	ReleaseID          string
	ReleaseDigest      string
	DeliveryMode       string
	ExpectedGeneration int64
	DesiredGeneration  int64
	RemoteInput        bool
	ShowQR             bool
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
	Releases    Publisher
}

// AssignTarget publishes or reuses the release for target and makes it the
// node's desired assignment.
func (s Service) AssignTarget(ctx context.Context, mutation Mutation, nodeID string, target Target) (PutResult, error) {
	if s.Assignments == nil || s.Releases == nil {
		return PutResult{}, errors.New("assignment service is not configured")
	}
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return PutResult{}, ErrInvalidMutation
	}
	if err := validateTarget(nodeID, target); err != nil {
		return PutResult{}, err
	}
	requestDigest, err := RequestDigest(nodeID, target)
	if err != nil {
		return PutResult{}, err
	}
	envelope, err := s.Releases.PublishTarget(ctx, mutation.ActorID, "assignment:"+nodeID+":"+mutation.IdempotencyKey, target.MediaID, target.PlaylistID)
	if err != nil {
		return PutResult{}, err
	}
	mode := target.DeliveryMode
	if mode == "" {
		mode = DeliveryStream
		if envelope.Payload.AllowsDeliveryMode(DeliveryCacheThenPlay) {
			mode = DeliveryCacheThenPlay
		}
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
		expected := currentGeneration
		if target.ExpectedGeneration != nil {
			expected = *target.ExpectedGeneration
		}
		if expected != currentGeneration {
			return &GenerationConflictError{Expected: expected, Actual: currentGeneration}
		}
		if currentGeneration == math.MaxInt64 {
			return ErrGenerationExhausted
		}

		stored, found, err := tx.ReleaseByID(envelope.ID)
		if err != nil {
			return err
		}
		if !found {
			return ErrReleaseNotFound
		}
		if err := validateReleaseBinding(stored, envelope.ID, envelope.Digest); err != nil {
			return err
		}
		if !stored.Payload.AllowsDeliveryMode(mode) {
			return fmt.Errorf("%w: %q", ErrDeliveryMode, mode)
		}

		remoteInput, showQR, err := remoteSettings(tx, target)
		if err != nil {
			return err
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
			MediaID:            target.MediaID,
			PlaylistID:         target.PlaylistID,
			ReleaseID:          envelope.ID,
			ReleaseDigest:      envelope.Digest,
			DeliveryMode:       mode,
			ExpectedGeneration: expected,
			DesiredGeneration:  currentGeneration + 1,
			RemoteInput:        remoteInput,
			ShowQR:             showQR,
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

func RequestDigest(nodeID string, target Target) (string, error) {
	body, err := json.Marshal(struct {
		NodeID string
		Target
	}{NodeID: nodeID, Target: target})
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

func validateTarget(nodeID string, target Target) error {
	if nodeID == "" || (target.ExpectedGeneration != nil && *target.ExpectedGeneration < 0) {
		return ErrInvalidAssignment
	}
	if (target.MediaID == "") == (target.PlaylistID == "") {
		return fmt.Errorf("%w: exactly one of mediaId and playlistId is required", ErrInvalidAssignment)
	}
	if target.DeliveryMode != "" && target.DeliveryMode != DeliveryStream && target.DeliveryMode != DeliveryCacheThenPlay {
		return fmt.Errorf("%w: %q", ErrDeliveryMode, target.DeliveryMode)
	}
	// An inherited remote input setting is checked in remoteSettings.
	if target.ShowQR != nil && *target.ShowQR && target.RemoteInput != nil && !*target.RemoteInput {
		return fmt.Errorf("%w: QR requires remote input", ErrInvalidAssignment)
	}
	return nil
}

// remoteSettings resolves the target's remote input and QR settings, keeping
// the node's current values for omitted fields.
func remoteSettings(tx Transaction, target Target) (remoteInput, showQR bool, err error) {
	if target.RemoteInput == nil || target.ShowQR == nil {
		current, found, err := tx.CurrentAssignment()
		if err != nil {
			return false, false, err
		}
		if found {
			remoteInput, showQR = current.RemoteInput, current.ShowQR
		}
	}
	if target.RemoteInput != nil {
		remoteInput = *target.RemoteInput
	}
	if target.ShowQR != nil {
		showQR = *target.ShowQR
	}
	if showQR && !remoteInput {
		if target.ShowQR != nil {
			return false, false, fmt.Errorf("%w: QR requires remote input", ErrInvalidAssignment)
		}
		showQR = false
	}
	return remoteInput, showQR, nil
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
