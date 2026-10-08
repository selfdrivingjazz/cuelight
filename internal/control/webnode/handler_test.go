package webnode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type credentialOnlyRepository struct {
	Repository
	token string
}

func (r credentialOnlyRepository) BrowserCredential(_ context.Context, tokenHash []byte, _ time.Time) (Credential, bool, error) {
	if bytes.Equal(tokenHash, Hash(r.token)) {
		return Credential{ID: "c", NodeID: "node-1"}, true, nil
	}
	return Credential{}, false, nil
}

func TestIdentityAcceptsQueryTokenOnlyForArtifactContent(t *testing.T) {
	service := &Service{Repository: credentialOnlyRepository{token: "secret"}, NewID: func() (string, error) { return "id", nil }}
	cases := []struct {
		method, target, header string
		want                   string
	}{
		{http.MethodGet, "/player/assignment", "Bearer secret", "node-1"},
		{http.MethodGet, "/player/artifacts/abc/content?access_token=secret", "", "node-1"},
		{http.MethodHead, "/player/artifacts/abc/content?access_token=secret", "", "node-1"},
		{http.MethodGet, "/player/assignment?access_token=secret", "", ""},
		{http.MethodPost, "/player/artifacts/abc/content?access_token=secret", "", ""},
		{http.MethodGet, "/player/assignment", "Bearer wrong", ""},
	}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.target, nil)
		if test.header != "" {
			request.Header.Set("Authorization", test.header)
		}
		got, err := service.Identity(request)
		if got != test.want || (test.want == "") != (err != nil) {
			t.Errorf("%s %s = %q, %v; want %q", test.method, test.target, got, err, test.want)
		}
	}
}

func TestNormalizeCodeIgnoresCaseSpacesAndDashes(t *testing.T) {
	if got := NormalizeCode(" k7qm-4x pd "); got != "K7QM4XPD" {
		t.Fatalf("NormalizeCode = %q", got)
	}
}
