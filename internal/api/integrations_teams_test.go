package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"openfox/internal/integrations"
	"openfox/internal/integrations/teams"
	"strings"
	"testing"
	"time"
)

func TestTeamsAPIErrorRedaction(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{integrations.ErrVersionConflict, 409, "VERSION_CONFLICT"}, {integrations.ErrForbidden, 403, "FORBIDDEN"}, {teams.ErrUnauthorized, 401, "TEAMS_UNAUTHORIZED"}, {teams.ErrChannelScope, 422, "TEAMS_UNSUPPORTED_SCOPE"}, {integrations.ErrSecretsUnavailable, 503, "TEAMS_CONFIGURATION_UNAVAILABLE"}, {errors.New("private-token private-response"), 500, "INTERNAL_ERROR"},
	} {
		w := httptest.NewRecorder()
		writeTeamsError(w, httptest.NewRequest("POST", "/fixture", nil), c.err)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.code) || strings.Contains(w.Body.String(), "private-") {
			t.Fatal("wrong/redaction", w.Code, w.Body.String())
		}
	}
	for _, valid := range []bool{true, false} {
		w := httptest.NewRecorder()
		writeTeamsError(w, httptest.NewRequest("POST", "/fixture", nil), &teams.Error{Code: "private-token", Status: 429, RetryAfter: 2147483648 * time.Second, RetryAfterValid: valid})
		want := ""
		if valid {
			want = "2147483648"
		}
		if w.Code != 429 || w.Header().Get("Retry-After") != want || strings.Contains(w.Body.String(), "private-token") {
			t.Fatal("retry wait changed", w.Header())
		}
	}
}
func TestTeamsAPIWebhookEarlyBoundaries(t *testing.T) {
	h := &Handler{}
	m := http.NewServeMux()
	h.registerTeamsRoutes(m)
	for _, c := range []struct {
		id, content, body string
		status            int
	}{
		{"bad", "application/json", "{}", 404}, {"11111111-1111-4111-8111-111111111111", "text/plain", "{}", 400}, {"11111111-1111-4111-8111-111111111111", "application/json", strings.Repeat("x", teams.MaxActivityBytes+1), 413}, {"11111111-1111-4111-8111-111111111111", "application/json", "{}", 503},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/hooks/teams/"+c.id+"/activities", strings.NewReader(c.body))
		r.Header.Set("Authorization", "Bearer private-token")
		r.Header.Set("Content-Type", c.content)
		m.ServeHTTP(w, r)
		if w.Code != c.status || strings.Contains(w.Body.String(), "private-token") {
			t.Fatal("unexpected error", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached")
		}
	}
}
