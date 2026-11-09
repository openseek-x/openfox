package wp02_wp03

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

// CSRFTestHooks exposes a CSRF value from the adapter's cookie jar without
// making the value part of the test source.
type CSRFTestHooks interface {
	CSRFToken(context.Context, AuthSpec) (string, error)
}

func newHarness(t *testing.T) (context.Context, HTTPHarness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if HTTPFactory == nil {
		if os.Getenv("OPENFOX_WP02_WP03_REQUIRE_HTTP") == "1" {
			t.Fatal("WP-02/WP-03 HTTP adapter is not wired; set HTTPFactory in a same-package test file")
		}
		t.Skip("WP-02/WP-03 HTTP adapter is not wired; baseline compiled but did not execute")
	}
	harness, err := HTTPFactory(ctx)
	if err != nil {
		t.Fatalf("create WP-02/WP-03 HTTP adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := harness.Close(); err != nil {
			t.Errorf("close WP-02/WP-03 HTTP adapter: %v", err)
		}
	})
	return ctx, harness
}

func requireInvitationHooks(t *testing.T, harness HTTPHarness) InvitationTestHooks {
	t.Helper()
	hooks, ok := harness.(InvitationTestHooks)
	if !ok {
		t.Skip("WP-02/WP-03 adapter has no invitation test delivery/expiry hook")
	}
	return hooks
}

func requireDeviceHooks(t *testing.T, harness HTTPHarness) DeviceTestHooks {
	t.Helper()
	hooks, ok := harness.(DeviceTestHooks)
	if !ok {
		t.Skip("WP-02/WP-03 adapter has no device challenge/signing hook")
	}
	return hooks
}

func requireCSRFHooks(t *testing.T, harness HTTPHarness) CSRFTestHooks {
	t.Helper()
	hooks, ok := harness.(CSRFTestHooks)
	if !ok {
		t.Skip("WP-02/WP-03 adapter has no CSRF cookie hook")
	}
	return hooks
}

func userAuth(principal FixturePrincipal) AuthSpec {
	return AuthSpec{Kind: AuthUserCookie, Principal: principal}
}

func userBearerAuth(principal FixturePrincipal) AuthSpec {
	return AuthSpec{Kind: AuthUserBearer, Principal: principal}
}

func deviceAuth(principal FixturePrincipal) AuthSpec {
	return AuthSpec{Kind: AuthDeviceBearer, Principal: principal}
}

func route(harness HTTPHarness, path string) string {
	prefix := strings.TrimRight(harness.RoutePrefix(), "/")
	if prefix == "" {
		prefix = "/api/v1"
	}
	return strings.TrimRight(harness.BaseURL(), "/") + prefix + "/" + strings.TrimLeft(path, "/")
}

func requestJSON(t *testing.T, client *http.Client, method, url string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode JSON request: %v", err)
		}
		payload = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, payload)
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("HTTP %s %s: %v", method, url, err)
	}
	return response
}

func responseBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return body
}

func assertStatus(t *testing.T, response *http.Response, expected ...int) []byte {
	t.Helper()
	body := responseBody(t, response)
	for _, status := range expected {
		if response.StatusCode == status {
			return body
		}
	}
	t.Fatalf("HTTP status = %d, want one of %v; body=%s", response.StatusCode, expected, body)
	return nil
}

func assertError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	body := responseBody(t, response)
	if response.StatusCode != status {
		t.Fatalf("error status = %d, want %d; body=%s", response.StatusCode, status, body)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error body is not JSON: %v; body=%s", err, body)
	}
	if envelope.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body=%s", envelope.Error.Code, code, body)
	}
	if envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("error body omitted message or request_id: %s", body)
	}
	if got := response.Header.Get("X-Request-ID"); got != envelope.Error.RequestID {
		t.Fatalf("X-Request-ID = %q, body request_id = %q", got, envelope.Error.RequestID)
	}
	message := strings.ToLower(envelope.Error.Message)
	for _, forbidden := range []string{"password", "private key", "sqlstate", "select ", "insert ", "update ", "delete "} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error message contains internal detail %q: %q", forbidden, envelope.Error.Message)
		}
	}
}

func stringField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, body)
	}
	value, ok := object[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		t.Fatalf("response field %q is missing or not a non-empty string: %s", field, body)
	}
	return value
}

func numberField(t *testing.T, body []byte, field string) float64 {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, body)
	}
	value, ok := object[field].(float64)
	if !ok {
		t.Fatalf("response field %q is missing or not a number: %s", field, body)
	}
	return value
}

func assertNoJSONKey(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, body)
	}
	for _, key := range forbidden {
		if containsJSONKey(value, strings.ToLower(key)) {
			t.Fatalf("response contains forbidden plaintext field %q: %s", key, body)
		}
	}
}

func containsJSONKey(value any, wanted string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.ToLower(key) == wanted || strings.Contains(strings.ToLower(key), wanted) {
				return true
			}
			if containsJSONKey(child, wanted) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsJSONKey(child, wanted) {
				return true
			}
		}
	}
	return false
}

func addCSRF(t *testing.T, ctx context.Context, harness HTTPHarness, auth AuthSpec, headers map[string]string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		result[key] = value
	}
	token, err := requireCSRFHooks(t, harness).CSRFToken(ctx, auth)
	if err != nil {
		t.Fatalf("get CSRF test value: %v", err)
	}
	result["X-CSRF-Token"] = token
	return result
}

func concurrentResponses(t *testing.T, calls ...func() *http.Response) []*http.Response {
	t.Helper()
	responses := make([]*http.Response, len(calls))
	done := make(chan struct{}, len(calls))
	for index, call := range calls {
		index, call := index, call
		go func() {
			responses[index] = call()
			done <- struct{}{}
		}()
	}
	for range calls {
		<-done
	}
	return responses
}

func countStatuses(responses []*http.Response, wanted ...int) int {
	count := 0
	for _, response := range responses {
		for _, status := range wanted {
			if response.StatusCode == status {
				count++
				break
			}
		}
	}
	return count
}

func requireID(t *testing.T, body []byte, fields ...string) string {
	t.Helper()
	for _, field := range fields {
		var object map[string]any
		if err := json.Unmarshal(body, &object); err == nil {
			if value, ok := object[field].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	t.Fatalf("response did not contain one of %v: %s", fields, body)
	return ""
}

func requireStatusIn(t *testing.T, response *http.Response, allowed ...int) []byte {
	t.Helper()
	if len(allowed) == 0 {
		t.Fatal("requireStatusIn called without allowed statuses")
	}
	return assertStatus(t, response, allowed...)
}

func describeAuth(auth AuthSpec) string {
	return fmt.Sprintf("auth=%s principal=%s org=%s", auth.Kind, auth.Principal.ID, auth.Principal.OrganizationID)
}
