package cableguide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/cable"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/remote"
)

type memoryRepository struct {
	tokens   map[string]Grant
	assigned map[string]string
	assets   map[string]map[string]bool
}

func (m *memoryRepository) CreateCableGuideToken(_ context.Context, hash []byte, grant Grant, _ time.Time) error {
	m.tokens[string(hash)] = grant
	return nil
}
func (m *memoryRepository) CableGuideGrant(_ context.Context, hash []byte, now time.Time) (Grant, error) {
	grant, ok := m.tokens[string(hash)]
	if !ok || !grant.ExpiresAt.After(now) {
		return Grant{}, ErrUnauthorized
	}
	return grant, nil
}
func (m *memoryRepository) NodeCableDeployment(_ context.Context, nodeID string, _ time.Time) (string, error) {
	return m.assigned[nodeID], nil
}
func (m *memoryRepository) DeploymentReferencesAsset(_ context.Context, deploymentID, assetID string) (bool, error) {
	return m.assets[deploymentID][assetID], nil
}

type presentationsByDeployment struct{}

func (presentationsByDeployment) Build(_ context.Context, deploymentID string) (cable.Presentation, error) {
	return cable.Presentation{Revision: cable.PresentationRevision{ID: deploymentID}, Theme: "nebula"}, nil
}

type nodeRemotes struct{ events map[string][]remote.Event }

func (r nodeRemotes) Guide(nodeID string) (remote.SessionCreated, error) {
	if _, ok := r.events[nodeID]; !ok {
		return remote.SessionCreated{}, remote.ErrSessionMissing
	}
	return remote.SessionCreated{ID: "session-" + nodeID, RemoteURL: "https://control.test/remote/session-" + nodeID, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (r nodeRemotes) Poll(_ context.Context, nodeID, id string, after int64, _ time.Duration) (remote.EventPage, error) {
	if id != "session-"+nodeID {
		return remote.EventPage{}, remote.ErrSessionMissing
	}
	return remote.EventPage{Events: r.events[nodeID], NextSequence: after + int64(len(r.events[nodeID]))}, nil
}

type artifacts struct{}

func (artifacts) CableArtifactsForAsset(_ context.Context, assetID string) ([]cable.Artifact, error) {
	return []cable.Artifact{{AssetID: assetID, Digest: "digest-" + assetID, MediaType: "video/mp4", State: "ready", Size: 10}}, nil
}

type content struct{}

func (content) Stat(_ context.Context, digest string) (media.ContentObject, error) {
	return media.ContentObject{Digest: digest, Size: 10}, nil
}
func (content) Open(context.Context, string) (media.ContentHandle, error) {
	return readSeekCloser{bytes.NewReader([]byte("0123456789"))}, nil
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

type fixture struct {
	repository *memoryRepository
	service    *Service
	guide      http.Handler
	player     http.Handler
}

func newFixture() fixture {
	repository := &memoryRepository{
		tokens:   map[string]Grant{},
		assigned: map[string]string{"node-a": "deployment-a", "node-b": "deployment-b"},
		assets:   map[string]map[string]bool{"deployment-a": {"asset-a": true}, "deployment-b": {"asset-b": true}},
	}
	service := &Service{Repository: repository}
	return fixture{
		repository: repository,
		service:    service,
		guide: NewGuideHandler(GuideDependencies{Service: service, Presentations: presentationsByDeployment{}, Remote: nodeRemotes{events: map[string][]remote.Event{
			"node-a": {{Sequence: 1, Type: "key", Key: "ArrowUp"}},
			"node-b": {{Sequence: 1, Type: "key", Key: "Enter"}},
		}}, Artifacts: artifacts{}, Content: content{}}),
		player: NewPlayerHandler(service, func(r *http.Request) (string, error) {
			if node := r.Header.Get("X-Node"); node != "" {
				return node, nil
			}
			return "", errors.New("no identity")
		}),
	}
}

func (f fixture) mint(t *testing.T, node, deployment string) (int, Token) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/player/cable-guide-tokens", strings.NewReader(`{"deploymentId":"`+deployment+`"}`))
	request.Header.Set("X-Node", node)
	recorder := httptest.NewRecorder()
	f.player.ServeHTTP(recorder, request)
	var token Token
	_ = json.Unmarshal(recorder.Body.Bytes(), &token)
	return recorder.Code, token
}

func (f fixture) get(token, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.guide.ServeHTTP(recorder, request)
	return recorder
}

func TestGuideTokenRequiresAssignmentToDeploymentCableItem(t *testing.T) {
	f := newFixture()
	if code, _ := f.mint(t, "node-a", "deployment-b"); code != http.StatusForbidden {
		t.Fatalf("mint for unassigned deployment status=%d", code)
	}
	if code, _ := f.mint(t, "node-c", "deployment-a"); code != http.StatusForbidden {
		t.Fatalf("mint for node without Cable assignment status=%d", code)
	}
	request := httptest.NewRequest(http.MethodPost, "/player/cable-guide-tokens", strings.NewReader(`{"deploymentId":"deployment-a"}`))
	recorder := httptest.NewRecorder()
	f.player.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("mint without node identity status=%d", recorder.Code)
	}
	code, token := f.mint(t, "node-a", "deployment-a")
	if code != http.StatusCreated || token.Token == "" || !token.ExpiresAt.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("mint status=%d token=%+v", code, token)
	}
}

func TestGuideTokenIsScopedToItsDeploymentAndNode(t *testing.T) {
	f := newFixture()
	_, token := f.mint(t, "node-a", "deployment-a")

	response := f.get(token.Token, "/cable-guide/presentation")
	var presentation cable.Presentation
	if err := json.Unmarshal(response.Body.Bytes(), &presentation); err != nil || response.Code != http.StatusOK || presentation.Revision.ID != "deployment-a" {
		t.Fatalf("presentation status=%d body=%s", response.Code, response.Body.String())
	}
	if presentation.Remote == nil || !strings.HasSuffix(presentation.Remote.URL, "session-node-a") {
		t.Fatalf("presentation remote = %+v", presentation.Remote)
	}

	if response := f.get(token.Token, "/cable-guide/media/asset-b"); response.Code != http.StatusNotFound {
		t.Fatalf("other deployment media status=%d", response.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/cable-guide/media/asset-a?access_token="+token.Token, nil)
	request.Header.Set("Range", "bytes=2-4")
	recorder := httptest.NewRecorder()
	f.guide.ServeHTTP(recorder, request)
	if body, _ := io.ReadAll(recorder.Body); recorder.Code != http.StatusPartialContent || string(body) != "234" || recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("ranged media status=%d body=%q", recorder.Code, body)
	}

	events := f.get(token.Token, "/cable-guide/remote/events?after=0&wait=0")
	var page struct {
		Events []remote.Event            `json:"events"`
		Next   int64                     `json:"next"`
		Remote *cable.PresentationRemote `json:"remote"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &page); err != nil || len(page.Events) != 1 || page.Events[0].Key != "ArrowUp" || page.Next != 1 || page.Remote == nil {
		t.Fatalf("events status=%d body=%s", events.Code, events.Body.String())
	}

	// The presentation endpoint never accepts the query-string token.
	if response := f.get("", "/cable-guide/presentation?access_token="+token.Token); response.Code != http.StatusUnauthorized {
		t.Fatalf("query token on presentation status=%d", response.Code)
	}
}

func TestGuideTokenStopsWorkingWhenNodeLosesAssignmentAndRenews(t *testing.T) {
	f := newFixture()
	_, token := f.mint(t, "node-a", "deployment-a")
	request := httptest.NewRequest(http.MethodPost, "/cable-guide/token:renew", nil)
	request.Header.Set("Authorization", "Bearer "+token.Token)
	recorder := httptest.NewRecorder()
	f.guide.ServeHTTP(recorder, request)
	var renewed Token
	if err := json.Unmarshal(recorder.Body.Bytes(), &renewed); err != nil || recorder.Code != http.StatusOK || renewed.Token == "" || renewed.Token == token.Token {
		t.Fatalf("renew status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	f.repository.assigned["node-a"] = ""
	for _, value := range []string{token.Token, renewed.Token} {
		if response := f.get(value, "/cable-guide/presentation"); response.Code != http.StatusUnauthorized {
			t.Fatalf("unassigned node presentation status=%d", response.Code)
		}
	}
	if response := f.get("not-a-token", "/cable-guide/presentation"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status=%d", response.Code)
	}
}
