package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/webnode"
)

func TestPostgresBrowserNodePairingRenewalAndRevocation(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	ctx := context.Background()
	const actorID = "11111111-1111-4111-8111-111111111111"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	service := &webnode.Service{Repository: repository, NewID: repository.newUUID, Now: func() time.Time { return now },
		Config: webnode.Config{ClaimTTL: 10 * time.Minute, CredentialTTL: 48 * time.Hour, PairBaseURL: "https://console.example"}}

	started, err := service.StartClaim(ctx, "TV browser")
	if err != nil {
		t.Fatal(err)
	}
	if started.PairURL != "https://console.example/pair?code="+started.Code || started.DisplayCode[4] != '-' {
		t.Fatalf("claim = %+v", started)
	}
	if status, err := service.PollClaim(ctx, started.ClaimID, started.PollToken); err != nil || status.Status != "pending" {
		t.Fatalf("pending poll = %+v, %v", status, err)
	}
	if _, err := service.PollClaim(ctx, started.ClaimID, "wrong"); !errors.Is(err, webnode.ErrUnauthorized) {
		t.Fatalf("wrong poll token err = %v", err)
	}
	if _, err := service.PollClaim(ctx, "not-a-uuid", started.PollToken); !errors.Is(err, webnode.ErrUnauthorized) {
		t.Fatalf("malformed claim id err = %v", err)
	}
	if _, err := service.RedeemClaim(ctx, actorID, "ZZZZZZZZ", "Lobby"); !errors.Is(err, webnode.ErrClaimNotFound) {
		t.Fatalf("unknown code err = %v", err)
	}
	typed := started.DisplayCode[:4] + " " + started.DisplayCode[4:]
	node, err := service.RedeemClaim(ctx, actorID, typed, "Lobby TV")
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeClass != webnode.NodeClassBrowser || node.UserAgent != "TV browser" {
		t.Fatalf("node = %+v", node)
	}
	if _, err := service.RedeemClaim(ctx, actorID, started.Code, "Again"); !errors.Is(err, webnode.ErrClaimConflict) {
		t.Fatalf("second redeem err = %v", err)
	}
	operatorNode, err := NewOperatorAdapter(repository).GetNode(ctx, node.ID)
	if err != nil || operatorNode.NodeClass != webnode.NodeClassBrowser || operatorNode.CredentialState != "active" {
		t.Fatalf("operator node = %+v, %v", operatorNode, err)
	}

	claimed, err := service.PollClaim(ctx, started.ClaimID, started.PollToken)
	if err != nil || claimed.Status != "claimed" || claimed.Credential == nil || claimed.Credential.NodeID != node.ID || claimed.Credential.EventEpoch == "" {
		t.Fatalf("claimed poll = %+v, %v", claimed, err)
	}
	if _, err := service.PollClaim(ctx, started.ClaimID, started.PollToken); !errors.Is(err, webnode.ErrClaimGone) {
		t.Fatalf("credential redelivered: %v", err)
	}
	first := claimed.Credential.Token
	if credential, err := service.Authenticate(ctx, first); err != nil || credential.NodeID != node.ID {
		t.Fatalf("authenticate = %+v, %v", credential, err)
	}

	now = now.Add(time.Hour)
	renewed, err := service.Renew(ctx, first)
	if err != nil || renewed.Token == first || renewed.EventEpoch != claimed.Credential.EventEpoch {
		t.Fatalf("renew = %+v, %v", renewed, err)
	}
	if _, err := service.Authenticate(ctx, first); !errors.Is(err, webnode.ErrUnauthorized) {
		t.Fatalf("rotated token still valid: %v", err)
	}
	listed, err := service.ListNodes(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != node.ID || listed[0].CredentialExpiresAt == nil || !listed[0].CredentialExpiresAt.Equal(renewed.ExpiresAt) || listed[0].LastSeenAt == nil {
		t.Fatalf("list = %+v, %v", listed, err)
	}

	now = now.Add(49 * time.Hour)
	if _, err := service.Authenticate(ctx, renewed.Token); !errors.Is(err, webnode.ErrUnauthorized) {
		t.Fatalf("expired token accepted: %v", err)
	}
	if listed, _ := service.ListNodes(ctx); len(listed) != 1 || listed[0].CredentialState != "expired" {
		t.Fatalf("expired list = %+v", listed)
	}

	if err := service.RevokeNode(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if listed, _ := service.ListNodes(ctx); len(listed) != 0 {
		t.Fatalf("revoked node listed: %+v", listed)
	}
	if err := service.RevokeNode(ctx, "not-a-uuid"); !errors.Is(err, webnode.ErrNodeNotFound) {
		t.Fatalf("malformed revoke err = %v", err)
	}

	expired, err := service.StartClaim(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.RedeemClaim(ctx, actorID, expired.Code, "Late"); !errors.Is(err, webnode.ErrClaimNotFound) {
		t.Fatalf("expired claim redeemed: %v", err)
	}
	if _, err := service.PollClaim(ctx, expired.ClaimID, expired.PollToken); !errors.Is(err, webnode.ErrClaimGone) {
		t.Fatalf("expired poll err = %v", err)
	}
}

func TestPostgresBrowserCredentialRequiresBrowserNode(t *testing.T) {
	repository, _ := newPostgresTestStore(t)
	ctx := context.Background()
	native, err := repository.CreateNode(ctx, "native", "22222222-2222-4222-8222-222222222222", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RevokeBrowserNode(ctx, native.ID, time.Now().UTC()); !errors.Is(err, webnode.ErrNodeNotFound) {
		t.Fatalf("native node revoked through browser path: %v", err)
	}
}
