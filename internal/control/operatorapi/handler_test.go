package operatorapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/pki"
	"github.com/jmill/cable/internal/control/release"
	playerclient "github.com/jmill/cable/internal/player/client"
	canonical "github.com/jmill/cable/internal/release"
)

type testAuth struct {
	scopes []string
	listed []auth.TokenMetadata
}

func (a testAuth) Authenticate(_ context.Context, secret string, required ...string) (auth.Actor, error) {
	if secret == "bad" {
		return auth.Actor{}, auth.ErrInvalidCredential
	}
	for _, want := range required {
		found := false
		for _, have := range a.scopes {
			if have == want {
				found = true
			}
		}
		if !found {
			return auth.Actor{}, auth.ErrInsufficientScope
		}
	}
	return auth.Actor{ID: "actor", TokenID: "token", Scopes: a.scopes}, nil
}
func (a testAuth) List(context.Context) ([]auth.TokenMetadata, error) { return a.listed, nil }
func (a testAuth) Revoke(context.Context, string) (auth.TokenMetadata, error) {
	return auth.TokenMetadata{}, nil
}

type testPKI struct {
	secret   pki.TokenSecret
	mutation pki.Mutation
}

func (p *testPKI) CreateEnrollmentToken(_ context.Context, m pki.Mutation) (pki.TokenSecret, error) {
	p.mutation = m
	return p.secret, nil
}
func (p *testPKI) CreateRecoveryToken(_ context.Context, m pki.Mutation, _ string) (pki.TokenSecret, error) {
	p.mutation = m
	return p.secret, nil
}

type testNodes struct {
	node     Node
	gotMatch string
}

func (n *testNodes) ListNodes(context.Context) ([]Node, error)     { return []Node{n.node}, nil }
func (n *testNodes) GetNode(context.Context, string) (Node, error) { return n.node, nil }
func (n *testNodes) UpdateNode(_ context.Context, _ string, name, match string) (Node, error) {
	n.gotMatch = match
	n.node.Name = name
	return n.node, nil
}
func (n *testNodes) GetNodeStatus(context.Context, string) (playerclient.NodeReport, error) {
	return playerclient.NodeReport{Sequence: 1}, nil
}
func (n *testNodes) ListNodeEvents(context.Context, string, string) ([]playerclient.NodeEvent, *string, error) {
	return nil, nil, nil
}

type testAssignments struct {
	value    assignment.Assignment
	result   assignment.PutResult
	mutation assignment.Mutation
	err      error
}

func (a *testAssignments) GetAssignment(context.Context, string) (assignment.Assignment, error) {
	return a.value, nil
}
func (a *testAssignments) PutAssignment(_ context.Context, _ string, m assignment.Mutation, _ assignment.PutInput) (assignment.PutResult, error) {
	a.mutation = m
	return a.result, a.err
}

type testMedia struct {
	upload media.Upload
	body   int64
}

func (m *testMedia) CreateUpload(context.Context, media.Mutation, media.CreateUploadRequest) (media.Upload, error) {
	return m.upload, nil
}
func (m *testMedia) Upload(context.Context, string) (media.Upload, error) { return m.upload, nil }
func (m *testMedia) AppendChunk(_ context.Context, _ media.Mutation, _ string, r media.AppendChunkRequest) (media.Upload, error) {
	b, err := io.ReadAll(r.Body)
	m.body = int64(len(b))
	return m.upload, err
}
func (m *testMedia) CompleteUpload(context.Context, media.Mutation, string) (media.Asset, error) {
	return media.Asset{ID: "asset"}, nil
}

type testAssets struct{}

func (testAssets) ListAssets(context.Context) ([]media.Asset, error) {
	return []media.Asset{{ID: "asset", Name: "x"}}, nil
}
func (testAssets) GetAsset(context.Context, string) (media.Asset, error) {
	return media.Asset{ID: "asset", Name: "x"}, nil
}

type testReleases struct{ err error }

func (r testReleases) Publish(context.Context, release.Mutation, release.PublishInput) (release.PublishResult, error) {
	return release.PublishResult{Envelope: canonicalEnvelope()}, r.err
}
func (r testReleases) GetRelease(context.Context, string) (canonical.EnvelopeV1, error) {
	return canonicalEnvelope(), r.err
}
func canonicalEnvelope() canonical.EnvelopeV1 {
	return canonical.EnvelopeV1{ID: "release", Digest: strings.Repeat("a", 64), KeyID: "key", Signature: "sig"}
}

func request(h http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func withHeader(h http.Handler, method, path, token string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOperatorAuthAndScopeAndSecretBoundary(t *testing.T) {
	secret := pki.TokenSecret{ID: "token", Secret: "one-time", ExpiresAt: time.Unix(1, 0).UTC()}
	authn := testAuth{scopes: []string{ScopeNodeEnroll}, listed: []auth.TokenMetadata{{ID: "token", Prefix: "cable_admin_x", Scopes: []string{ScopeNodeEnroll}}}}
	h := NewHandler(Dependencies{Auth: authn, PKI: &testPKI{secret: secret}})
	if got := request(h, "POST", "/enrollment-tokens", "", nil).Code; got != 401 {
		t.Fatalf("unauthenticated status=%d", got)
	}
	if got := request(h, "GET", "/admin-tokens", "tok", nil).Code; got != 403 {
		t.Fatalf("wrong scope status=%d", got)
	}
	w := withHeader(h, "POST", "/enrollment-tokens", "tok", nil, map[string]string{"Idempotency-Key": "0123456789abcdef"})
	if w.Code != 201 || !strings.Contains(w.Body.String(), "one-time") {
		t.Fatalf("create token response=%d %s", w.Code, w.Body.String())
	}
	// A list response is metadata-only and cannot expose the one-time secret.
	w = withHeader(NewHandler(Dependencies{Auth: testAuth{scopes: []string{ScopeSecurityAdmin}, listed: authn.listed}}), "GET", "/admin-tokens", "tok", nil, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "one-time") {
		t.Fatalf("secret crossed list boundary: %s", w.Body.String())
	}
}

func TestOperatorIfMatchUploadAndMalformedJSON(t *testing.T) {
	nodes := &testNodes{node: Node{ID: "n", Name: "old", DesiredGeneration: 2, ObservedGeneration: 1}}
	med := &testMedia{upload: media.Upload{ID: "u", DeclaredLength: 3, ReceivedLength: 0, State: media.UploadOpen}}
	h := NewHandler(Dependencies{Auth: testAuth{scopes: []string{ScopeNodeWrite, ScopeMediaWrite}}, Nodes: nodes, Media: med, MaxJSONBytes: 1 << 20, MaxChunkBytes: 2})
	if w := request(h, "PATCH", "/nodes/n", "tok", strings.NewReader(`{"name":"new"}`)); w.Code != 412 {
		t.Fatalf("missing If-Match=%d", w.Code)
	}
	w := withHeader(h, "PATCH", "/nodes/n", "tok", strings.NewReader(`{"name":"new"}`), map[string]string{"If-Match": "\"n:2:1\""})
	if w.Code != 200 || nodes.gotMatch != "\"n:2:1\"" {
		t.Fatalf("update=%d match=%q", w.Code, nodes.gotMatch)
	}
	w = withHeader(h, "PATCH", "/nodes/n", "tok", strings.NewReader(`{"unknown":1}`), map[string]string{"If-Match": "x"})
	if w.Code != 400 {
		t.Fatalf("unknown field=%d", w.Code)
	}
	w = withHeader(h, "PATCH", "/uploads/u", "tok", strings.NewReader("abc"), map[string]string{"Upload-Offset": "0"})
	if w.Code != 413 {
		t.Fatalf("chunk limit=%d", w.Code)
	}
	w = withHeader(h, "PATCH", "/uploads/u", "tok", strings.NewReader("a"), map[string]string{"Upload-Offset": "0"})
	if w.Code != 204 || med.body != 1 {
		t.Fatalf("chunk=%d body=%d", w.Code, med.body)
	}
}

func TestOperatorAssignmentAndReleaseMappings(t *testing.T) {
	as := &testAssignments{value: assignment.Assignment{ID: "a", NodeID: "n", DesiredGeneration: 3}, result: assignment.PutResult{Assignment: assignment.Assignment{ID: "a", NodeID: "n", DesiredGeneration: 4}, Created: true}}
	h := NewHandler(Dependencies{Auth: testAuth{scopes: []string{ScopeAssignmentRead, ScopeAssignmentWrite, ScopeReleaseRead, ScopeReleaseWrite}}, Assignments: as, Releases: testReleases{}})
	w := request(h, "GET", "/nodes/n/assignment", "tok", nil)
	if w.Code != 200 || w.Header().Get("ETag") != "\"generation:3\"" {
		t.Fatalf("assignment=%d etag=%q", w.Code, w.Header().Get("ETag"))
	}
	w = withHeader(h, "PUT", "/nodes/n/assignment", "tok", strings.NewReader(`{"releaseId":"r","releaseDigest":"`+strings.Repeat("a", 64)+`","deliveryMode":"stream","expectedGeneration":3}`), map[string]string{"If-Match": "\"generation:3\"", "Idempotency-Key": "0123456789abcdef"})
	if w.Code != 201 {
		t.Fatalf("assignment put=%d", w.Code)
	}
	as.err = assignment.ErrGenerationConflict
	w = withHeader(h, "PUT", "/nodes/n/assignment", "tok", strings.NewReader(`{}`), map[string]string{"If-Match": "x", "Idempotency-Key": "0123456789abcdef"})
	if w.Code != 412 {
		t.Fatalf("generation conflict=%d", w.Code)
	}
	w = withHeader(NewHandler(Dependencies{Auth: testAuth{scopes: []string{ScopeReleaseWrite}}, Releases: testReleases{err: release.ErrIdempotencyConflict}}), "POST", "/releases/publish", "tok", strings.NewReader(`{}`), map[string]string{"Idempotency-Key": "0123456789abcdef"})
	if w.Code != 409 {
		t.Fatalf("replay conflict=%d", w.Code)
	}
	w = withHeader(h, "POST", "/releases/publish", "tok", strings.NewReader(`{"playback":{"assetId":"a","fit":"fit","loop":false,"muted":false,"volume":1,"orientation":0,"allowedDeliveryModes":["stream"]}}`), map[string]string{"Idempotency-Key": "0123456789abcdef"})
	if w.Code != 201 {
		t.Fatalf("release publish=%d", w.Code)
	}
	if statusFor(assignment.ErrIdempotencyConflict) != 409 {
		t.Fatal("idempotency conflict mapping changed")
	}
}
