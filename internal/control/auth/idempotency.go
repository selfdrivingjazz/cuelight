package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrIdempotencyKeyRequired = errors.New("idempotency key required")
	ErrIdempotencyConflict    = errors.New("idempotency key reused with different request")
)

type IdempotencyRecord struct {
	ActorID            string
	Operation          string
	Key                string
	RequestFingerprint [sha256.Size]byte
	ResponseStatus     int
	ResponseBody       []byte
	ExpiresAt          time.Time
}

// IdempotencyRepository atomically serializes work under
// (actor, operation, key). Execute must:
//   - return an unexpired record without invoking operation;
//   - replace an expired record and invoke operation exactly once;
//   - commit the response record and operation's side effects atomically; and
//   - leave no record or claim when operation fails or its transaction aborts.
//
// The context passed to operation must carry the repository transaction so
// other persistence adapters can participate in the same commit.
type IdempotencyRepository interface {
	Execute(context.Context, IdempotencyRecord, time.Time, IdempotentOperation) (record IdempotencyRecord, executed bool, err error)
}

type IdempotentResponse struct {
	Status int
	Body   []byte
}

type IdempotentOperation func(context.Context) (IdempotentResponse, error)

type IdempotencyService struct {
	repository IdempotencyRepository
	clock      Clock
	ttl        time.Duration
}

func NewIdempotencyService(repository IdempotencyRepository, clock Clock, ttl time.Duration) *IdempotencyService {
	return &IdempotencyService{repository: repository, clock: clock, ttl: ttl}
}

type idempotencyContextKey struct{}

func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyContextKey{}, key)
}

func IdempotencyKeyFromContext(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(idempotencyContextKey{}).(string)
	return key, ok && key != ""
}

// Execute either runs operation once and atomically records its response, or
// replays the response already stored for the actor, operation name, and key.
// A live record with another request fingerprint is a deterministic conflict.
func (s *IdempotencyService) Execute(ctx context.Context, operation string, request []byte, execute IdempotentOperation) (IdempotentResponse, bool, error) {
	actor, key, err := mutationIdentity(ctx, operation)
	if err != nil {
		return IdempotentResponse{}, false, err
	}
	if s.ttl <= 0 {
		return IdempotentResponse{}, false, fmt.Errorf("%w: idempotency TTL must be positive", ErrInvalidArgument)
	}
	if execute == nil {
		return IdempotentResponse{}, false, fmt.Errorf("%w: idempotent operation is required", ErrInvalidArgument)
	}

	now := s.clock.Now()
	candidate := IdempotencyRecord{
		ActorID:            actor.ID,
		Operation:          operation,
		Key:                key,
		RequestFingerprint: RequestFingerprint(request),
		ExpiresAt:          now.Add(s.ttl),
	}
	record, executed, err := s.repository.Execute(ctx, candidate, now, func(transactionContext context.Context) (IdempotentResponse, error) {
		response, err := execute(transactionContext)
		if err != nil {
			return IdempotentResponse{}, err
		}
		if response.Status < 100 || response.Status > 599 {
			return IdempotentResponse{}, fmt.Errorf("%w: response status must be between 100 and 599", ErrInvalidArgument)
		}
		response.Body = slices.Clone(response.Body)
		return response, nil
	})
	if err != nil {
		return IdempotentResponse{}, false, fmt.Errorf("execute idempotent operation: %w", err)
	}
	if !sameFingerprint(record.RequestFingerprint, candidate.RequestFingerprint) {
		return IdempotentResponse{}, false, ErrIdempotencyConflict
	}
	return responseFromRecord(record), !executed, nil
}

func RequestFingerprint(request []byte) [sha256.Size]byte {
	return sha256.Sum256(request)
}

func mutationIdentity(ctx context.Context, operation string) (Actor, string, error) {
	actor, ok := ActorFromContext(ctx)
	if !ok {
		return Actor{}, "", ErrActorRequired
	}
	if operation == "" {
		return Actor{}, "", fmt.Errorf("%w: operation is required", ErrInvalidArgument)
	}
	key, ok := IdempotencyKeyFromContext(ctx)
	if !ok {
		return Actor{}, "", ErrIdempotencyKeyRequired
	}
	return actor, key, nil
}

func sameFingerprint(left, right [sha256.Size]byte) bool {
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

func responseFromRecord(record IdempotencyRecord) IdempotentResponse {
	return IdempotentResponse{Status: record.ResponseStatus, Body: slices.Clone(record.ResponseBody)}
}

func cloneIdempotencyRecord(record IdempotencyRecord) IdempotencyRecord {
	record.ResponseBody = slices.Clone(record.ResponseBody)
	return record
}
