package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAServesFilesAndFallsBackToIndex(t *testing.T) {
	handler := spaHandler(ControllerWeb, fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
	})
	tests := []struct{ path, body, cache string }{
		{"/assets/app-1a2b.js", "console.log(1)", "public, max-age=31536000, immutable"},
		{"/screens/abc", "<html>app</html>", "no-cache"},
		{"/assets/missing.js", "<html>app</html>", "no-cache"},
		{"/../../etc/passwd", "<html>app</html>", "no-cache"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || response.Body.String() != test.body || response.Header().Get("Cache-Control") != test.cache {
			t.Errorf("%s: %d %q cache %q", test.path, response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
		}
	}
}

func TestSPAWithoutBuiltAssetsExplainsHowToBuild(t *testing.T) {
	handler := spaHandler(WebsiteNode, fstest.MapFS{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "pnpm build:web") {
		t.Fatalf("%d %q", response.Code, response.Body.String())
	}
}
