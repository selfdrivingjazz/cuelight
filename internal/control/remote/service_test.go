package remote

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// policies reports a fixed policy for whatever generation is asked about.
type policies struct{ policy Policy }

func (p policies) RemotePolicy(_ context.Context, _ string, since int64) (Policy, int64, error) {
	return p.policy, since, nil
}

// history models assignment history: remote[i] is generation i+1's remote
// input and the last entry is current.
type history struct{ remote []bool }

func (h *history) RemotePolicy(_ context.Context, _ string, since int64) (Policy, int64, error) {
	current := int64(len(h.remote))
	if since < 1 || since > current {
		return Policy{}, 0, ErrDisabled
	}
	enabled := true
	for _, on := range h.remote[since-1:] {
		enabled = enabled && on
	}
	return Policy{Enabled: enabled, ShowQR: enabled}, current, nil
}

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
	// A session renewed after expiry keeps sequences monotonic, so a consumer
	// holding the old cursor still receives the new session's events.
	now = now.Add(11 * time.Minute)
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
	links := make([]SessionCreated, 0, maxSessionTokens+1)
	for range maxSessionTokens + 1 {
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

func TestAppSessionLinkIsStablePerSessionAndSurvivesLinkEviction(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	service := &Service{Policies: policies{Policy{Enabled: true, ShowQR: true}}, PublicURL: "https://control.example", TTL: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := service.AppSession("node"); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("no session = %v", err)
	}
	created, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.AppSession("node")
	if err != nil || app.ID != created.ID || app.Token == created.Token || !app.ShowQR || !app.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("app = %#v, %v", app, err)
	}
	if again, _ := service.AppSession("node"); again.RemoteURL != app.RemoteURL {
		t.Fatalf("app URL changed within a session: %s vs %s", again.RemoteURL, app.RemoteURL)
	}
	for range maxSessionTokens + 1 {
		if _, err := service.Link(ctx, "node"); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Post(ctx, created.ID, app.Token, Event{Sequence: 1, Type: "key", Key: "Enter"}); err != nil {
		t.Fatalf("app token after link eviction: %v", err)
	}
	rebound, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := service.AppSession("node"); err != nil || next.ID != created.ID || rebound.ID != created.ID || next.Token != app.Token {
		t.Fatalf("rebound app = %#v, %v", next, err)
	}
	now = now.Add(2 * time.Hour)
	renewed, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := service.AppSession("node"); err != nil || next.ID != renewed.ID || next.ID == created.ID || next.Token == app.Token {
		t.Fatalf("renewed app = %#v, %v", next, err)
	}
}

func TestSessionSurvivesReassignmentsWhileRemoteStaysOn(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	policies := &history{remote: []bool{true}}
	service := &Service{Policies: policies, PublicURL: "https://control.example", TTL: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	first, err := service.Create(ctx, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.Link(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	post := func(id, token string) error {
		return service.Post(ctx, id, token, Event{Sequence: 1, Type: "key", Key: "Enter"})
	}
	// Reassigned to content that never creates a session (a video).
	policies.remote = append(policies.remote, true)
	if err := post(first.ID, first.Token); err != nil {
		t.Fatalf("scanned token after reassignment: %v", err)
	}
	// Reassigned to a web page: the player's new session is the same one.
	policies.remote = append(policies.remote, true)
	second, err := service.Create(ctx, "node", 3)
	if err != nil || second.ID != first.ID || second.Token == first.Token {
		t.Fatalf("rebound = %#v, %v", second, err)
	}
	if page, err := service.Poll(ctx, "node", second.ID, 0, 0); err != nil || len(page.Events) != 0 {
		t.Fatalf("rebound session replays earlier input: %#v, %v", page, err)
	}
	for _, token := range []string{first.Token, link.Token, second.Token} {
		if err := post(second.ID, token); err != nil {
			t.Fatalf("token after rebind: %v", err)
		}
	}
	// Turning remote input off ends every phone, and turning it back on
	// does not revive them.
	policies.remote = append(policies.remote, false)
	if err := post(second.ID, second.Token); !errors.Is(err, ErrDisabled) {
		t.Fatalf("remote off = %v", err)
	}
	if _, err := service.Link(ctx, "node"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("link with remote off = %v", err)
	}
	if _, err := service.Create(ctx, "node", 4); !errors.Is(err, ErrDisabled) {
		t.Fatalf("create with remote off = %v", err)
	}
	policies.remote = append(policies.remote, true)
	if err := post(second.ID, second.Token); !errors.Is(err, ErrDisabled) {
		t.Fatalf("remote back on revived an old phone: %v", err)
	}
	fresh, err := service.Create(ctx, "node", 5)
	if err != nil || fresh.ID == first.ID {
		t.Fatalf("fresh = %#v, %v", fresh, err)
	}
	if err := post(first.ID, first.Token); !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("replaced session = %v", err)
	}
	if err := post(fresh.ID, fresh.Token); err != nil {
		t.Fatal(err)
	}
	if app, err := service.AppSession("node"); err != nil || app.ID != fresh.ID {
		t.Fatalf("app session = %#v, %v", app, err)
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

func (p *togglePolicies) RemotePolicy(_ context.Context, _ string, since int64) (Policy, int64, error) {
	return Policy{Enabled: p.enabled}, since, nil
}
