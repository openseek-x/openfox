package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/telegram"
)

func TestTelegramAPIErrorRedaction(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"reserved", integrations.ErrTelegramBotOccupied, 409, "TELEGRAM_BOT_OCCUPIED"},
		{"other webhook", integrations.ErrTelegramWebhookConflict, 409, "TELEGRAM_WEBHOOK_CONFLICT"},
		{"expired proof", integrations.ErrTelegramSessionState, 409, "TELEGRAM_SESSION_STATE"},
		{"unsupported", integrations.ErrTelegramUnsupported, 400, "TELEGRAM_UNSUPPORTED"},
		{"configuration", integrations.ErrSecretsUnavailable, 503, "TELEGRAM_CONFIGURATION_UNAVAILABLE"},
		{"signature", telegram.ErrSignature, 401, "TELEGRAM_WEBHOOK_UNAUTHORIZED"},
		{"token", telegram.ErrAuth, 422, "TELEGRAM_VERIFICATION_FAILED"},
		{"version", integrations.ErrVersionConflict, 409, "VERSION_CONFLICT"},
		{"unknown", telegram.ErrUnknown, 502, "TELEGRAM_UNAVAILABLE"},
		{"untrusted error", errors.New("synthetic-private-token https://secret.invalid/path"), 500, "INTERNAL_ERROR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/fixture", nil)
			writeTelegramError(w, r, c.err)
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.code) {
				t.Fatalf("unexpected fixed error %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "synthetic-private-token") || strings.Contains(w.Body.String(), "secret.invalid") {
				t.Fatal("error exposed upstream text")
			}
		})
	}
	w := httptest.NewRecorder()
	writeTelegramError(w, httptest.NewRequest("POST", "/fixture", nil), &telegram.Error{Kind: telegram.ErrRateLimited, Code: "synthetic-private-token", RetryAfter: 1500 * time.Millisecond})
	if w.Code != 429 || w.Header().Get("Retry-After") != "2" || strings.Contains(w.Body.String(), "synthetic-private-token") {
		t.Fatalf("unsafe rate error: %d", w.Code)
	}
}
func TestTelegramAPIWebhookEarlyBoundaries(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerTelegramRoutes(mux)
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		route, content string
		want           int
	}{{"not-a-route", "application/json", 404}, {token, "text/plain", 400}, {token, "application/json", 503}}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/hooks/telegram/"+c.route, strings.NewReader(`{"private":"synthetic-private-token"}`))
		r.Header.Set("Content-Type", c.content)
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "synthetic-header")
		mux.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("status %d wanted %d", w.Code, c.want)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("missing secret response policy")
		}
		for _, secret := range []string{token, "synthetic-private-token", "synthetic-header"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret in failure response")
			}
		}
	}
}
