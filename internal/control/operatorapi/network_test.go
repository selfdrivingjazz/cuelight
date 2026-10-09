package operatorapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/jmilldotdev/cuelight/internal/control/pki"
)

func trustedHandler(t *testing.T, publicURL string, scopes []string, p *testPKI) http.Handler {
	t.Helper()
	trust, err := NewNetworkTrust([]netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}, publicURL)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Dependencies{Auth: testAuth{scopes: scopes}, PKI: p, Nodes: &testNodes{node: Node{ID: "n"}}, NetworkTrust: trust})
}

func peerRequest(h http.Handler, method, target, peer string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = peer
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestTrustedNetworkAdmitsTokenlessPeersOnly(t *testing.T) {
	p := &testPKI{secret: pki.TokenSecret{ID: "token", Secret: "s"}}
	h := trustedHandler(t, "", []string{ScopeNodeRead}, p)
	for _, test := range []struct {
		peer string
		want int
	}{
		{"100.101.102.103:41000", http.StatusOK},
		{"[fd7a:115c:a1e0:ab12::1]:41000", http.StatusOK},
		// IPv4 peers on a dual-stack listener arrive IPv4-mapped.
		{"[::ffff:100.64.0.9]:41000", http.StatusOK},
		{"[fe80::1%eth0]:41000", http.StatusUnauthorized},
		{"192.168.1.20:41000", http.StatusUnauthorized},
		{"[fd7a:115c:a1e1::1]:41000", http.StatusUnauthorized},
		{"[::ffff:192.168.1.20]:41000", http.StatusUnauthorized},
		{"garbage", http.StatusUnauthorized},
	} {
		if got := peerRequest(h, "GET", "/nodes", test.peer, nil).Code; got != test.want {
			t.Errorf("peer %s: status %d, want %d", test.peer, got, test.want)
		}
	}
	// Forwarding headers never stand in for the TCP peer.
	if got := peerRequest(h, "GET", "/nodes", "192.168.1.20:1", map[string]string{"X-Forwarded-For": "100.64.0.1", "X-Real-IP": "100.64.0.1"}).Code; got != http.StatusUnauthorized {
		t.Fatalf("X-Forwarded-For trusted: %d", got)
	}
	// The network actor holds every scope, including writes and security admin.
	w := peerRequest(h, "POST", "/enrollment-tokens", "100.64.0.1:1", map[string]string{"Idempotency-Key": "0123456789abcdef"})
	if w.Code != http.StatusCreated || p.mutation.ActorID != TrustedNetworkActorID {
		t.Fatalf("network write = %d %s actor %q", w.Code, w.Body.String(), p.mutation.ActorID)
	}
	// Disabled trust (zero value) admits nobody.
	open := NewHandler(Dependencies{Auth: testAuth{}, Nodes: &testNodes{}})
	if got := peerRequest(open, "GET", "/nodes", "127.0.0.1:1", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("zero-value trust admitted a peer: %d", got)
	}
}

func TestTrustedNetworkNeverUpgradesAPresentToken(t *testing.T) {
	h := trustedHandler(t, "", []string{ScopeNodeRead}, &testPKI{})
	peer := "100.64.0.1:1"
	if got := peerRequest(h, "GET", "/nodes", peer, map[string]string{"Authorization": "Bearer read-only"}).Code; got != http.StatusOK {
		t.Fatalf("read-only token read = %d", got)
	}
	headers := map[string]string{"Authorization": "Bearer read-only", "Idempotency-Key": "0123456789abcdef"}
	if got := peerRequest(h, "POST", "/enrollment-tokens", peer, headers).Code; got != http.StatusForbidden {
		t.Fatalf("read-only token write from trusted peer = %d, want 403", got)
	}
	for _, value := range []string{"Bearer bad", "Basic abc", ""} {
		if got := peerRequest(h, "GET", "/nodes", peer, map[string]string{"Authorization": value}).Code; got != http.StatusUnauthorized {
			t.Fatalf("Authorization %q from trusted peer = %d, want 401", value, got)
		}
	}
	// An Authorization header that is present but empty is still a credential
	// attempt, not an absent one.
	r := httptest.NewRequest("GET", "/nodes", nil)
	r.RemoteAddr = peer
	r.Header["Authorization"] = []string{""}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty Authorization from trusted peer = %d", w.Code)
	}
}

func TestTrustedNetworkCrossSiteGuard(t *testing.T) {
	peer := "100.64.0.1:1"
	withPublic := trustedHandler(t, "https://cuelight.example.ts.net:8470", []string{ScopeNodeRead}, &testPKI{})
	for _, test := range []struct {
		method  string
		headers map[string]string
		want    int
	}{
		{"GET", nil, http.StatusOK},
		{"GET", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://cuelight.example.ts.net:8470"}, http.StatusOK},
		{"GET", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"GET", map[string]string{"Origin": "HTTPS://Cuelight.Example.ts.net:8470"}, http.StatusOK},
		{"GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"GET", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"GET", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"GET", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"GET", map[string]string{"Origin": "http://cuelight.example.ts.net:8470"}, http.StatusForbidden},
		{"GET", map[string]string{"Origin": "https://cuelight.example.ts.net"}, http.StatusForbidden},
		{"DELETE", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"PATCH", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	} {
		w := peerRequest(withPublic, test.method, "/nodes/n", peer, test.headers)
		if w.Code != test.want {
			t.Errorf("%s %v: status %d, want %d", test.method, test.headers, w.Code, test.want)
		}
		if w.Code == http.StatusForbidden && w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%v: 403 without problem detail", test.headers)
		}
	}
	// A token-authenticated request is not subject to the guard: the token,
	// not ambient network position, is the credential.
	if got := peerRequest(withPublic, "GET", "/nodes", peer, map[string]string{"Authorization": "Bearer t", "Origin": "https://evil.example"}).Code; got != http.StatusOK {
		t.Fatalf("token request with foreign Origin = %d", got)
	}

	// Without a public URL the request's own scheme and Host are the origin.
	own := trustedHandler(t, "", nil, &testPKI{})
	if got := peerRequest(own, "GET", "https://100.64.0.2:8470/nodes", peer, map[string]string{"Origin": "https://100.64.0.2:8470"}).Code; got != http.StatusOK {
		t.Fatalf("own origin = %d", got)
	}
	if got := peerRequest(own, "GET", "https://100.64.0.2/nodes", peer, map[string]string{"Origin": "https://100.64.0.2"}).Code; got != http.StatusOK {
		t.Fatalf("own origin on default port = %d", got)
	}
	if got := peerRequest(own, "GET", "https://100.64.0.2:8470/nodes", peer, map[string]string{"Origin": "http://100.64.0.2:8470"}).Code; got != http.StatusForbidden {
		t.Fatalf("scheme mismatch = %d", got)
	}
}

func TestOperatorMeReportsAuthMethod(t *testing.T) {
	h := trustedHandler(t, "", []string{ScopeNodeRead}, &testPKI{})
	var me OperatorMe
	w := peerRequest(h, "GET", "/operator/me", "[::ffff:100.70.1.2]:1", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK {
		t.Fatalf("network me = %d %s", w.Code, w.Body.String())
	}
	if me.AuthMethod != AuthMethodNetwork || me.PeerAddress != "100.70.1.2" || me.ID != TrustedNetworkActorID || me.TokenID != "" || len(me.Scopes) != len(AllScopes) || !me.Capabilities[ScopeSecurityAdmin] {
		t.Fatalf("network me = %+v", me)
	}
	me = OperatorMe{}
	w = peerRequest(h, "GET", "/operator/me", "100.70.1.2:1", map[string]string{"Authorization": "Bearer t"})
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.AuthMethod != AuthMethodToken || me.PeerAddress != "" || me.TokenID != "token" || me.Capabilities[ScopeSecurityAdmin] {
		t.Fatalf("token me = %d %+v", w.Code, me)
	}
}

func TestOperatorAPINeverSendsCORSHeaders(t *testing.T) {
	h := trustedHandler(t, "", AllScopes, &testPKI{})
	for _, r := range []*http.Request{
		httptest.NewRequest("OPTIONS", "/nodes", nil),
		httptest.NewRequest("GET", "/nodes", nil),
	} {
		r.RemoteAddr = "100.64.0.1:1"
		r.Header.Set("Authorization", "Bearer t")
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Access-Control-Request-Method", "DELETE")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		for name := range w.Header() {
			if len(name) > 15 && http.CanonicalHeaderKey(name)[:15] == "Access-Control-" {
				t.Fatalf("%s %s sent %s", r.Method, r.URL, name)
			}
		}
	}
}
