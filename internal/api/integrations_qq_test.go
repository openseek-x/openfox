package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/qq"
)

func TestQQAPIErrorRedaction(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid", integrations.ErrInvalidRequest, 400, "INVALID_REQUEST"},
		{"not found", integrations.ErrNotFound, 404, "NOT_FOUND"},
		{"forbidden", integrations.ErrForbidden, 403, "FORBIDDEN"},
		{"version", integrations.ErrVersionConflict, 409, "VERSION_CONFLICT"},
		{"reauth", integrations.ErrNeedsReauth, 409, "QQ_NEEDS_REAUTH"},
		{"configuration", integrations.ErrSecretsUnavailable, 503, "QQ_CONFIGURATION_UNAVAILABLE"},
		{"signature", qq.ErrSignature, 401, "QQ_WEBHOOK_UNAUTHORIZED"},
		{"token", qq.ErrAuthentication, 422, "QQ_VERIFICATION_FAILED"},
		{"unknown", qq.ErrUnknown, 502, "QQ_UNAVAILABLE"},
		{"unsafe diagnostic", errors.New("synthetic-private-secret https://secret.invalid/path"), 500, "INTERNAL_ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeQQError(w, httptest.NewRequest("POST", "/fixture", nil), tt.err)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("unexpected error %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "synthetic-private-secret") || strings.Contains(w.Body.String(), "secret.invalid") {
				t.Fatal("upstream diagnostic exposed")
			}
		})
	}
	for _, delay := range []time.Duration{1500 * time.Millisecond, 2147483648 * time.Second, time.Duration(1<<63 - 1)} {
		w := httptest.NewRecorder()
		writeQQError(w, httptest.NewRequest("POST", "/fixture", nil), &qq.Error{Kind: qq.ErrRateLimited, Reason: "synthetic-private-secret", RetryAfter: delay})
		seconds, err := strconv.ParseInt(w.Header().Get("Retry-After"), 10, 64)
		want := int64(delay / time.Second)
		if delay%time.Second != 0 {
			want++
		}
		if err != nil || w.Code != 429 || seconds != want || strings.Contains(w.Body.String(), "synthetic-private-secret") {
			t.Fatalf("shortened/unsafe retry: %v %d %s", delay, w.Code, w.Body.String())
		}
	}
}

func TestQQAPIWebhookEarlyBoundaries(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerQQRoutes(mux)
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, tt := range []struct {
		route, content string
		status         int
	}{{"invalid-route", "application/json", 404}, {token, "text/plain", 400}, {token, "application/json", 503}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/hooks/qq/"+tt.route, strings.NewReader(`{"secret":"synthetic-private-secret"}`))
		r.Header.Set("Content-Type", tt.content)
		r.Header.Set("X-Signature-Ed25519", "synthetic-signature")
		mux.ServeHTTP(w, r)
		if w.Code != tt.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("unexpected boundary: %d", w.Code)
		}
		for _, secret := range []string{token, "synthetic-private-secret", "synthetic-signature"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret in response")
			}
		}
	}
}
