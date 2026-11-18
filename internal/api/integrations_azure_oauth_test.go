package api

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
)

func TestAzureOAuthSafeErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		status      int
		code, retry string
	}{
		{"successful_response_backoff", &azuredevops.Error{Code: "rate_limited", HTTPStatus: 200, RetryAfter: 3 * time.Second}, 429, "AZURE_RATE_LIMITED", "3"},
		{"runtime_lease", &integrations.AzureRetry{After: 2 * time.Second}, 429, "AZURE_RETRY_LATER", "2"},
		{"revoked_refresh", &azuredevops.Error{Code: "invalid_grant", HTTPStatus: 400}, 409, "AZURE_REAUTHORIZE", ""},
		{"scope", integrations.ErrPersonalCapability, 403, "AZURE_SCOPE_NOT_ALLOWED", ""},
		{"self_mismatch", integrations.ErrPersonalIdentity, 422, "AZURE_IDENTITY_MISMATCH", ""},
		{"app_changed", integrations.ErrAzureAppChanged, 409, "AZURE_APP_CHANGED", ""},
		{"already_connected", integrations.ErrAzureAlreadyConnected, 409, "AZURE_ALREADY_CONNECTED", ""},
		{"provider_secret", &azuredevops.Error{Code: "opaque-provider-secret", HTTPStatus: 500}, 502, "AZURE_UPSTREAM_FAILED", ""},
		{"unknown_secret", errors.New("opaque-provider-secret"), 500, "INTERNAL_ERROR", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "http://localhost/fixture", nil)
			writeAzureError(w, r, tc.err)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("status=%d body=%s retry=%s", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
			}
			if strings.Contains(w.Body.String(), "opaque-provider-secret") || tc.name == "self_mismatch" && strings.Contains(w.Body.String(), "email") {
				t.Fatalf("unsafe/misleading public error %s", w.Body.String())
			}
		})
	}
}
