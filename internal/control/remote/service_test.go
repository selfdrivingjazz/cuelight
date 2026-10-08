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
	// A second controller whose clock is behind still gets through; the
	// server, not the client, orders the session's events.
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: 1, Type: "key", Key: "Enter"}); err != nil {
		t.Fatal(err)
	}
	base := now.UnixMicro()
	page, err := service.Poll(context.Background(), "node-one", created.ID, 0, 0)
	if err != nil || len(page.Events) != 2 || page.Events[0].Key != "ArrowDown" || page.Events[0].Sequence != base+1 || page.Events[1].Sequence != base+2 || page.NextSequence != base+2 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	// A renewed session keeps sequences monotonic, so a consumer holding the
	// old cursor still receives the new session's events.
	now = now.Add(time.Second)
	renewed, err := service.Create(context.Background(), "node-one", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Post(context.Background(), renewed.ID, renewed.Token, Event{Sequence: 1, Type: "key", Key: "Enter"}); err != nil {
		t.Fatal(err)
	}
	renewedPage, err := service.Poll(context.Background(), "node-one", renewed.ID, page.NextSequence, 0)
	if err != nil || len(renewedPage.Events) != 1 || renewedPage.Events[0].Sequence <= page.NextSequence || renewedPage.NextSequence != now.UnixMicro()+1 {
		t.Fatalf("renewed page=%#v err=%v", renewedPage, err)
	}
	created = renewed
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
	for _, key := range []string{"ChannelUp", "ChannelDown", "Guide", "Info", "ChannelNumber:24", "Delete", "Home", "End", "PageUp", "PageDown", "Space", "BrowserBack", "BrowserForward", "BrowserReload"} {
		if err := validateEvent(Event{Sequence: 1, Type: "key", Key: key}); err != nil {
			t.Fatalf("rejected remote key %q: %v", key, err)
		}
	}
	for _, key := range []string{"ChannelNumber:", "ChannelNumber:0", "ChannelNumber:1000"} {
		if err := validateEvent(Event{Sequence: 1, Type: "key", Key: key}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid remote key %q", key)
		}
	}
}

func TestSessionRateLimitsAndBoundsBufferedEvents(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	service := &Service{Policies: policies{Policy{Enabled: true}}, PublicURL: "https://control.example", TTL: time.Hour, Now: func() time.Time { return now }, Random: bytes.NewReader(make([]byte, 96))}
	created, err := service.Create(context.Background(), "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= maxEventsPerMinute; sequence++ {
		if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: sequence, Type: "pointer", X: 0.5, Y: 0.5, State: "move"}); err != nil {
			t.Fatalf("event %d: %v", sequence, err)
		}
	}
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: maxEventsPerMinute + 1, Type: "key", Key: "Enter"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over limit = %v", err)
	}
	page, err := service.Poll(context.Background(), "node", created.ID, 0, 0)
	if err != nil || len(page.Events) != maxBufferedEvents || page.Events[0].Sequence != now.UnixMicro()+maxEventsPerMinute-maxBufferedEvents+1 {
		t.Fatalf("buffered %d events, err=%v", len(page.Events), err)
	}
	now = now.Add(time.Minute)
	if err := service.Post(context.Background(), created.ID, created.Token, Event{Sequence: maxEventsPerMinute + 1, Type: "key", Key: "Enter"}); err != nil {
		t.Fatalf("after window = %v", err)
	}
}

func TestLinkMintsAdditionalSessionTokens(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	service := &Service{Policies: policies{Policy{Enabled: true}}, PublicURL: "https://control.example", TTL: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := service.Link(ctx, "node"); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("no session = %v", err)
	}
	created, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Link(ctx, "other"); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("other node = %v", err)
	}
	links := make([]SessionCreated, 0, maxSessionTokens)
	for range maxSessionTokens {
		link, err := service.Link(ctx, "node")
		if err != nil {
			t.Fatal(err)
		}
		if link.ID != created.ID || link.Token == "" || link.Token == created.Token || link.RemoteURL == created.RemoteURL {
			t.Fatalf("link = %#v", link)
		}
		links = append(links, link)
	}
	sequence := int64(0)
	post := func(token string) error {
		sequence++
		return service.Post(ctx, created.ID, token, Event{Sequence: sequence, Type: "key", Key: "Enter"})
	}
	if err := post(links[0].Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("evicted minted token = %v", err)
	}
	sequence--
	for _, link := range links[1:] {
		if err := post(link.Token); err != nil {
			t.Fatalf("minted token: %v", err)
		}
	}
	if err := post(created.Token); err != nil {
		t.Fatalf("node token: %v", err)
	}
	if err := post("wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token = %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := service.Link(ctx, "node"); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("expired session = %v", err)
	}
}

func TestGuideLinkIsStablePerSessionAndSurvivesLinkEviction(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	service := &Service{Policies: policies{Policy{Enabled: true, ShowQR: true}}, PublicURL: "https://control.example", TTL: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := service.Guide("node"); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("no session = %v", err)
	}
	created, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	guide, err := service.Guide("node")
	if err != nil || guide.ID != created.ID || guide.Token == created.Token || !guide.ShowQR || !guide.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("guide = %#v, %v", guide, err)
	}
	if again, _ := service.Guide("node"); again.RemoteURL != guide.RemoteURL {
		t.Fatalf("guide URL changed within a session: %s vs %s", again.RemoteURL, guide.RemoteURL)
	}
	for range maxSessionTokens + 1 {
		if _, err := service.Link(ctx, "node"); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Post(ctx, created.ID, guide.Token, Event{Sequence: 1, Type: "key", Key: "Enter"}); err != nil {
		t.Fatalf("guide token after link eviction: %v", err)
	}
	renewed, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := service.Guide("node"); err != nil || next.ID != renewed.ID || next.Token == guide.Token {
		t.Fatalf("renewed guide = %#v, %v", next, err)
	}
}

func TestLinkRequiresEnabledPolicy(t *testing.T) {
	repository := &togglePolicies{enabled: true}
	service := &Service{Policies: repository, PublicURL: "https://control.example"}
	if _, err := service.Create(context.Background(), "node", 1); err != nil {
		t.Fatal(err)
	}
	repository.enabled = false
	if _, err := service.Link(context.Background(), "node"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled = %v", err)
	}
}

type togglePolicies struct{ enabled bool }

func (p *togglePolicies) RemotePolicy(context.Context, string, int64) (Policy, error) {
	return Policy{Enabled: p.enabled}, nil
}
