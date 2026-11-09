package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/integrations/gitapi"
)

func TestPersonalTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"valid", `{"active":true,"revoked":false,"scopes":["api"],"expires_at":"2099-01-01"}`, 200, nil},
		{"no expiry", `{"active":true,"scopes":["api"],"expires_at":null}`, 200, nil},
		{"read only", `{"active":true,"scopes":["read_api"]}`, 200, gitapi.ErrTokenRevoked},
		{"revoked", `{"active":true,"revoked":true,"scopes":["api"]}`, 200, gitapi.ErrTokenRevoked},
		{"expired", `{"active":true,"scopes":["api"],"expires_at":"2000-01-01"}`, 200, gitapi.ErrTokenRevoked},
		{"invalid date", `{"active":true,"scopes":["api"],"expires_at":"invalid"}`, 200, gitapi.ErrRequestFailed},
		{"malformed", `{`, 200, gitapi.ErrRequestFailed},
		{"denied", `{}`, 403, gitapi.ErrTokenRevoked},
		{"unavailable", `{}`, 503, gitapi.ErrRequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/personal_access_tokens/self" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("unexpected validation request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := New(srv.URL, "", "", "", Options{AllowLocal: true})
			_, err := client.PersonalToken(context.Background(), "test-token")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestPersonalTokenProjectPermissions(t *testing.T) {
	permissions := `{"project_access":{"access_level":30}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/projects" {
			if r.URL.Query().Get("membership") != "true" || r.URL.Query().Get("min_access_level") != "40" {
				t.Error("missing membership filter")
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"path":"app","path_with_namespace":"group/app","permissions":` + permissions + `}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "", "", "", Options{AllowLocal: true})
	c.PersonalTokenAuth = true
	if _, _, err := c.ListRepos(context.Background(), "test", true, "user", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRepo(context.Background(), "test", "group", "app"); !errors.Is(err, gitapi.ErrNotFound) {
		t.Fatalf("developer accepted: %v", err)
	}
	permissions = `{"group_access":{"access_level":40}}`
	if _, err := c.GetRepo(context.Background(), "test", "group", "app"); err != nil {
		t.Fatal(err)
	}
}
