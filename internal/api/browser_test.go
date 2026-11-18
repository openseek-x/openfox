package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/identity"
)

func TestBrowserRoutesRequireAuthentication(t *testing.T) {
	h := New(identity.Service{}, nil, false)
	for _, route := range []struct{ method, path string }{
		{"POST", "/v1/organizations/o/conversations/c/browser-bindings"},
		{"DELETE", "/v1/organizations/o/conversations/c/browser-bindings/b"},
		{"POST", "/v1/device/organizations/o/browser-bindings/b/claim"},
		{"POST", "/v1/device/organizations/o/browser-bindings/b/heartbeat"},
		{"POST", "/v1/device/organizations/o/runs/r/browser-authorize"},
	} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d", route.method, route.path, response.Code)
		}
	}
}
