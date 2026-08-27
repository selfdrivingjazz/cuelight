package composition

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPKIRouteHandlerClaimsProvisioningPaths(t *testing.T) {
	handler := newPKIHandler(nil, 1024)
	for _, path := range []string{"/player/enroll", "/player/recover", "/player/credentials:rotate", "/player/credentials/rotate"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if recorder.Code == http.StatusNotFound {
				t.Fatalf("provisioning route %s was not claimed", path)
			}
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want configured-dependency failure", recorder.Code)
			}
		})
	}
}
