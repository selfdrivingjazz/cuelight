package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryIdempotencyRepository struct {
	mu      sync.Mutex
	records map[idempotencyRecordKey]IdempotencyRecord
}

type idempotencyRecordKey struct {
	actorID   string
	operation string
	key       string
}

func newMemoryIdempotencyRepository() *memoryIdempotencyRepository {
	return &memoryIdempotencyRepository{records: make(map[idempotencyRecordKey]IdempotencyRecord)}
}

func (repository *memoryIdempotencyRepository) Execute(
	ctx context.Context,
	candidate IdempotencyRecord,
	now time.Time,
	operation IdempotentOperation,
) (IdempotencyRecord, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	key := idempotencyRecordKey{actorID: candidate.ActorID, operation: candidate.Operation, key: candidate.Key}
	if existing, ok := repository.records[key]; ok && existing.ExpiresAt.After(now) {
		return cloneIdempotencyRecord(existing), false, nil
	}
	response, err := operation(ctx)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	candidate.ResponseStatus = response.Status
	candidate.ResponseBody = append([]byte(nil), response.Body...)
	repository.records[key] = cloneIdempotencyRecord(candidate)
	return cloneIdempotencyRecord(candidate), true, nil
}

func TestIdempotentResponseReplay(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	repository := newMemoryIdempotencyRepository()
	service := NewIdempotencyService(repository, fixedClock{now: now}, 24*time.Hour)
	ctx := mutationContext("actor-1", "request-key")
	request := []byte(`{"tokenId":"token-1"}`)
	original := IdempotentResponse{Status: 204, Body: []byte(`{"revoked":true}`)}
	calls := 0

	response, replay, err := service.Execute(ctx, "revokeAdminToken", request, func(context.Context) (IdempotentResponse, error) {
		calls++
		return original, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay || response.Status != original.Status || string(response.Body) != string(original.Body) {
		t.Fatalf("first Execute = (%+v, replay %v)", response, replay)
	}
	original.Body[0] = 'x'
	response.Body[0] = 'x'

	response, replay, err = service.Execute(ctx, "revokeAdminToken", request, func(context.Context) (IdempotentResponse, error) {
		calls++
		return IdempotentResponse{Status: 500}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay || response.Status != 204 || string(response.Body) != `{"revoked":true}` {
		t.Fatalf("replayed Execute = (%+v, replay %v)", response, replay)
	}
	if calls != 1 {
		t.Fatalf("operation called %d times, want 1", calls)
	}
}

func TestConcurrentDuplicateExecutesSideEffectOnce(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := NewIdempotencyService(newMemoryIdempotencyRepository(), fixedClock{now: now}, time.Hour)
	ctx := mutationContext("actor-1", "request-key")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan executeResult, 2)
	var calls atomic.Int32
	operation := func(context.Context) (IdempotentResponse, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return IdempotentResponse{Status: 201, Body: []byte("created")}, nil
	}

	go func() {
		response, replay, err := service.Execute(ctx, "create", []byte("same"), operation)
		results <- executeResult{response: response, replay: replay, err: err}
	}()
	<-entered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		response, replay, err := service.Execute(ctx, "create", []byte("same"), operation)
		results <- executeResult{response: response, replay: replay, err: err}
	}()
	<-secondStarted
	close(release)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent errors = (%v, %v)", first.err, second.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("operation called %d times, want 1", calls.Load())
	}
	if first.replay == second.replay {
		t.Fatalf("replay flags = (%v, %v), want one execution and one replay", first.replay, second.replay)
	}
	if string(first.response.Body) != "created" || string(second.response.Body) != "created" {
		t.Fatalf("responses = (%q, %q), want identical replay", first.response.Body, second.response.Body)
	}
}

func TestIdempotencyKeyReuseWithDifferentRequestConflictsWithoutExecuting(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := NewIdempotencyService(newMemoryIdempotencyRepository(), fixedClock{now: now}, time.Hour)
	ctx := mutationContext("actor-1", "request-key")

	if _, _, err := service.Execute(ctx, "revokeAdminToken", []byte(`{"tokenId":"token-1"}`), staticResponse(204, nil)); err != nil {
		t.Fatal(err)
	}
	called := false
	_, replay, err := service.Execute(ctx, "revokeAdminToken", []byte(`{"tokenId":"token-2"}`), func(context.Context) (IdempotentResponse, error) {
		called = true
		return IdempotentResponse{Status: 204}, nil
	})
	if !errors.Is(err, ErrIdempotencyConflict) || replay {
		t.Fatalf("Execute = (replay %v, error %v), want conflict", replay, err)
	}
	if called {
		t.Fatal("conflicting operation was executed")
	}
}

func TestFailedOperationAbandonsClaimForRetry(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := NewIdempotencyService(newMemoryIdempotencyRepository(), fixedClock{now: now}, time.Hour)
	ctx := mutationContext("actor-1", "request-key")
	operationError := errors.New("operation failed")
	calls := 0

	_, replay, err := service.Execute(ctx, "create", []byte("request"), func(context.Context) (IdempotentResponse, error) {
		calls++
		return IdempotentResponse{}, operationError
	})
	if !errors.Is(err, operationError) || replay {
		t.Fatalf("failed Execute = (replay %v, error %v)", replay, err)
	}
	response, replay, err := service.Execute(ctx, "create", []byte("request"), func(context.Context) (IdempotentResponse, error) {
		calls++
		return IdempotentResponse{Status: 201, Body: []byte("created")}, nil
	})
	if err != nil || replay || response.Status != 201 {
		t.Fatalf("retry Execute = (%+v, replay %v, error %v)", response, replay, err)
	}
	if calls != 2 {
		t.Fatalf("operation called %d times, want failed attempt plus retry", calls)
	}
}

func TestExpiredIdempotencyRecordCanBeReplaced(t *testing.T) {
	start := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	service := NewIdempotencyService(newMemoryIdempotencyRepository(), clock, time.Hour)
	ctx := mutationContext("actor-1", "request-key")

	if _, _, err := service.Execute(ctx, "operation", []byte("old"), staticResponse(200, []byte("old"))); err != nil {
		t.Fatal(err)
	}
	clock.now = start.Add(time.Hour)
	response, replay, err := service.Execute(ctx, "operation", []byte("new"), staticResponse(201, []byte("new")))
	if err != nil || replay || response.Status != 201 || string(response.Body) != "new" {
		t.Fatalf("replacement Execute = (%+v, replay %v, error %v)", response, replay, err)
	}
}

func TestIdempotencyKeyIsScopedByActorAndOperation(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := NewIdempotencyService(newMemoryIdempotencyRepository(), fixedClock{now: now}, time.Hour)
	request := []byte(`{"value":1}`)

	for _, test := range []struct {
		actorID   string
		operation string
	}{
		{actorID: "actor-1", operation: "operation-a"},
		{actorID: "actor-2", operation: "operation-a"},
		{actorID: "actor-1", operation: "operation-b"},
	} {
		if _, replay, err := service.Execute(mutationContext(test.actorID, "same-key"), test.operation, request, staticResponse(201, nil)); err != nil || replay {
			t.Fatalf("%s/%s Execute = (replay %v, error %v)", test.actorID, test.operation, replay, err)
		}
	}
}

type executeResult struct {
	response IdempotentResponse
	replay   bool
	err      error
}

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

func mutationContext(actorID, key string) context.Context {
	ctx := WithActor(context.Background(), Actor{ID: actorID})
	return WithIdempotencyKey(ctx, key)
}

func staticResponse(status int, body []byte) IdempotentOperation {
	return func(context.Context) (IdempotentResponse, error) {
		return IdempotentResponse{Status: status, Body: body}, nil
	}
}
