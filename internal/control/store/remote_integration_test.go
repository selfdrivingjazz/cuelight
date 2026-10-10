package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jmilldotdev/cuelight/internal/control/appaccess"
	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/control/media"
	"github.com/jmilldotdev/cuelight/internal/control/operatorapi"
	"github.com/jmilldotdev/cuelight/internal/control/remote"
)

func TestPostgresRemoteSessionIsRestoredAfterControlRestart(t *testing.T) {
	repository, db := newPostgresTestStore(t)
	ctx := context.Background()
	actorID := "00000000-0000-4000-8000-000000000311"
	nodeID := "00000000-0000-4000-8000-000000000312"
	if err := repository.EnsureActor(ctx, actorID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, credential_state, event_epoch) VALUES ($1, 'screen', 'active', $1)`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO node_credentials (node_id, serial, not_after) VALUES ($1, 'serial', now() + interval '1 day')`, nodeID); err != nil {
		t.Fatal(err)
	}
	guide, _, err := repository.CreateWebMedia(ctx, media.Mutation{ActorID: actorID}, media.Media{ID: "00000000-0000-4000-8000-000000000313", URL: "https://apps.example/guide", Title: "Guide", AppAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	operator := operatorapi.NewHandler(operatorapi.Dependencies{
		Auth:     targetTestAuth{actorID: actorID},
		Assigner: assignment.Service{Assignments: repository, Releases: targetTestPublisher(t, repository)},
	})
	put := func(key, body string) operatorapi.Assignment {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/nodes/"+nodeID+"/assignment", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("If-Match", `"generation:0"`)
		request.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		operator.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d %s", body, recorder.Code, recorder.Body.String())
		}
		var result operatorapi.Assignment
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	assigned := put("restored-remote-1-0123456789", `{"mediaId":"`+guide.ID+`","expectedGeneration":0,"remoteInput":true,"showQr":true}`)
	apps := &appaccess.Service{Repository: repository}
	appToken, err := apps.Mint(ctx, nodeID, guide.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Before the restart the guide shows the player's session.
	before := &remote.Service{Policies: repository, PublicURL: "https://control.example"}
	if _, err := before.Create(ctx, nodeID, assigned.DesiredGeneration); err != nil {
		t.Fatal(err)
	}
	stale, err := before.AppSession(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}

	// The restarted control plane starts without sessions.
	sessions := &remote.Service{Policies: repository, PublicURL: "https://control.example"}
	app := appaccess.NewAppHandler(appaccess.AppDependencies{Service: apps, Remote: sessions})
	phone := remote.NewHandler(sessions, nil)
	type eventPage struct {
		Events       []remote.Event       `json:"events"`
		NextSequence int64                `json:"nextSequence"`
		Remote       *appaccess.AppRemote `json:"remote"`
	}
	poll := func(after int64) eventPage {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app/remote/events?after="+strconv.FormatInt(after, 10), nil)
		request.Header.Set("Authorization", "Bearer "+appToken.Token)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("app poll = %d %s", recorder.Code, recorder.Body.String())
		}
		var page eventPage
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	press := func(sessionID, token string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/remote/sessions/"+sessionID+"/events", strings.NewReader(`{"sequence":1,"type":"key","key":"Enter"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		phone.ServeHTTP(recorder, request)
		return recorder.Code
	}

	first := poll(0)
	if first.Remote == nil || !first.Remote.ShowQR || first.Remote.URL == stale.RemoteURL {
		t.Fatalf("guide remote after restart = %#v; want a new session's QR", first.Remote)
	}
	qr, err := url.Parse(first.Remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := strings.TrimPrefix(qr.Path, "/remote/")
	qrToken := strings.TrimPrefix(qr.Fragment, "token=")
	link, err := sessions.Link(ctx, nodeID)
	if err != nil || link.ID != sessionID {
		t.Fatalf("remote-link after restart = %#v, %v; want session %s", link, err, sessionID)
	}
	if code := press(link.ID, link.Token); code != http.StatusAccepted {
		t.Fatalf("operator link press = %d", code)
	}
	if page := poll(first.NextSequence); len(page.Events) != 1 || page.Events[0].Key != "Enter" || page.Remote == nil || page.Remote.URL != first.Remote.URL {
		t.Fatalf("guide poll after press = %#v", page)
	}

	// The player renewing its session keeps the guide's QR working.
	renewed, err := sessions.Create(ctx, nodeID, assigned.DesiredGeneration)
	if err != nil || renewed.ID != sessionID {
		t.Fatalf("player session = %#v, %v; want the restored session", renewed, err)
	}
	if code := press(sessionID, qrToken); code != http.StatusAccepted {
		t.Fatalf("phone that scanned the guide after the player renewed = %d", code)
	}
	if page := poll(0); len(page.Events) != 1 || page.Remote == nil || page.Remote.URL != first.Remote.URL {
		t.Fatalf("guide poll after the player renewed = %#v", page)
	}

	// With remote input off nothing is restored.
	put("restored-remote-2-0123456789", `{"mediaId":"`+guide.ID+`","expectedGeneration":1,"remoteInput":false}`)
	restarted := &remote.Service{Policies: repository, PublicURL: "https://control.example"}
	if _, err := restarted.Link(ctx, nodeID); !errors.Is(err, remote.ErrSessionMissing) {
		t.Fatalf("remote-link with remote off = %v", err)
	}
	if _, err := restarted.AppSession(ctx, nodeID); !errors.Is(err, remote.ErrSessionMissing) {
		t.Fatalf("app session with remote off = %v", err)
	}
	if _, err := restarted.Link(ctx, "not-a-node"); !errors.Is(err, remote.ErrSessionMissing) {
		t.Fatalf("remote-link for an unknown node = %v", err)
	}
}
