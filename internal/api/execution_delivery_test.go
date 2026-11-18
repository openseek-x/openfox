package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/identity"
)

func TestNewRegistersWP12UserAndDeviceRoutes(t *testing.T) {
	handler := New(identity.Service{}, nil, false)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/organizations/00000000-0000-0000-0000-0000000000a1/runs/00000000-0000-0000-0000-0000000000d1"},
		{http.MethodGet, "/v1/organizations/00000000-0000-0000-0000-0000000000a1/runs/00000000-0000-0000-0000-0000000000d1/events"},
		{http.MethodGet, "/v1/organizations/00000000-0000-0000-0000-0000000000a1/events"},
		{http.MethodPost, "/v1/device/organizations/00000000-0000-0000-0000-0000000000a1/runs/00000000-0000-0000-0000-0000000000d1/events"},
	}
	for _, test := range cases {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
			}
		})
	}
}
