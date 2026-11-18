package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterMemberRoutesUsesV1Prefix(t *testing.T) {
	mux := http.NewServeMux()
	(&Handler{}).registerMemberRoutes(mux)

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/organizations/00000000-0000-0000-0000-000000000001/members"},
		{method: http.MethodPost, path: "/v1/organizations/00000000-0000-0000-0000-000000000001/members"},
		{method: http.MethodPatch, path: "/v1/organizations/00000000-0000-0000-0000-000000000001/members/00000000-0000-0000-0000-000000000002"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		if handler, pattern := mux.Handler(request); handler == nil || pattern == "" {
			t.Fatalf("route %s %s was not registered", test.method, test.path)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/00000000-0000-0000-0000-000000000001/members", nil)
	if _, pattern := mux.Handler(request); pattern != "" {
		t.Fatalf("unexpected /api/v1 member route pattern %q", pattern)
	}
}
