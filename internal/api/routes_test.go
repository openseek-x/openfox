package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/identity"
	"openfox/internal/invitations"
)

func TestNewRegistersWP02WP03Routes(t *testing.T) {
	handler := New(identity.Service{}, nil, false)
	tests := []struct {
		name           string
		method         string
		path           string
		body           []byte
		wantStatusCode int
	}{
		{
			name:           "members",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/members",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "invitations",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/invites",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "device challenge",
			method:         http.MethodPost,
			path:           "/v1/device/auth/challenge",
			body:           []byte(`{"device_id":"00000000-0000-0000-0000-000000000011","key_id":"key-a"}`),
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "task statuses",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/task-statuses",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "tasks",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/spaces/00000000-0000-0000-0000-0000000000aa/tasks",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "agents",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/spaces/00000000-0000-0000-0000-0000000000aa/agents",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "conversations",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/conversations",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "conversation messages",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/conversations/00000000-0000-0000-0000-0000000000c1/messages",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "task comments",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/spaces/00000000-0000-0000-0000-0000000000aa/tasks/00000000-0000-0000-0000-0000000000c2/comments",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "notifications",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/notifications",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "runtime config",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/runtime-config",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "cleanup preview",
			method:         http.MethodPost,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/cleanup/previews",
			body:           []byte(`{"limit":10}`),
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "integrations",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/integrations",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "integration detail",
			method:         http.MethodGet,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/integrations/jira",
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name:           "integration disconnect",
			method:         http.MethodPost,
			path:           "/v1/organizations/00000000-0000-0000-0000-0000000000a1/integrations/connections/00000000-0000-0000-0000-0000000000c9/disconnect",
			body:           []byte(`{"version":1}`),
			wantStatusCode: http.StatusUnauthorized,
		},
		{name: "imports list", method: http.MethodGet, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports", wantStatusCode: http.StatusUnauthorized},
		{name: "imports upload", method: http.MethodPost, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/jira-csv", wantStatusCode: http.StatusUnauthorized},
		{name: "imports get", method: http.MethodGet, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9", wantStatusCode: http.StatusUnauthorized},
		{name: "imports config", method: http.MethodPut, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9/config", body: []byte(`{"config_version":1}`), wantStatusCode: http.StatusUnauthorized},
		{name: "imports start", method: http.MethodPost, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9/start", body: []byte(`{"config_version":1}`), wantStatusCode: http.StatusUnauthorized},
		{name: "imports cancel", method: http.MethodPost, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9/cancel", body: []byte(`{"version":1}`), wantStatusCode: http.StatusUnauthorized},
		{name: "imports rollback preview", method: http.MethodGet, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9/rollback", wantStatusCode: http.StatusUnauthorized},
		{name: "imports rollback", method: http.MethodPost, path: "/v1/organizations/00000000-0000-0000-0000-0000000000a1/imports/00000000-0000-0000-0000-0000000000c9/rollback", body: []byte(`{"version":1}`), wantStatusCode: http.StatusUnauthorized},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatusCode {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatusCode, response.Body.String())
			}
		})
	}
}

func TestInvitationResponseDoesNotSerializePlaintextToken(t *testing.T) {
	body, err := json.Marshal(invitationCreateResponse{
		ID:             "00000000-0000-0000-0000-000000000001",
		OrganizationID: "00000000-0000-0000-0000-0000000000a1",
		Email:          "invite@example.test",
		Role:           "MEMBER",
		Status:         invitations.StatusPending,
		IssuedAt:       time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
		ExpiresAt:      time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "token") {
		t.Fatalf("invitation response serialized token material: %s", body)
	}
}
