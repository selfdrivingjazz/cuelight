package remote

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type policies struct{ policy Policy }

func (p policies) RemotePolicy(context.Context, string, int64) (Policy, error) { return p.policy, nil }

func TestSessionAuthenticatesOrdersAndScopesEvents(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	service := &Service{Policies: policies{Policy{Enabled: true, ShowQR: true}}, PublicURL: "https://control.example", TTL: 10 * time.Minute, Now: func() time.Time { return now }, Random: bytes.NewReader(make([]byte, 96))}
	created, err := service.Create(context.Background(), "node-one", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !created.ShowQR || created.Token == "" || created.RemoteURL == "" {
		t.Fatalf("session = %#v", created)
	}
	if err := service.Post(context.Background(), created.ID, "wrong", Event{Sequence: 1, Type: "key", Key: "ArrowDown"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token = %v", err)
	}
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: 1, Type: "key", Key: "ArrowDown"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: 1, Type: "key", Key: "Enter"}); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay = %v", err)
	}
	page, err := service.Poll(context.Background(), "node-one", created.ID, 0, 0)
	if err != nil || len(page.Events) != 1 || page.NextSequence != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if _, err := service.Poll(context.Background(), "node-two", created.ID, 0, 0); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("wrong node = %v", err)
	}
	now = now.Add(11 * time.Minute)
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: 2, Type: "key", Key: "Enter"}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired = %v", err)
	}
}

func TestSessionRequiresEnabledPolicyAndBoundedEvents(t *testing.T) {
	service := &Service{Policies: policies{Policy{}}, PublicURL: "https://control.example", Random: bytes.NewReader(make([]byte, 96))}
	if _, err := service.Create(context.Background(), "node", 1); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled = %v", err)
	}
	for _, event := range []Event{{Sequence: 1, Type: "key", Key: "F12"}, {Sequence: 1, Type: "text"}, {Sequence: 1, Type: "pointer", X: 2, State: "move"}} {
		if !errors.Is(validateEvent(event), ErrInvalid) {
			t.Fatalf("accepted %#v", event)
		}
	}
}
