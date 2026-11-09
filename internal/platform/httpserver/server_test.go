package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/platform/config"
)

func testConfig() config.Config {
	return config.Config{
		Environment: config.EnvironmentDevelopment,
		Server:      config.ServerConfig{Listen: "127.0.0.1:8080", PublicURL: "http://127.0.0.1:8080"},
		Database:    config.DatabaseConfig{PoolMax: 20, StatementTimeout: 3 * time.Second, LockTimeout: time.Second},
		Scheduler:   config.SchedulerConfig{OfferTTL: 15 * time.Second},
	}
}

func TestHandlerReturnsLivenessAndRequestID(t *testing.T) {
	server := New(testConfig(), "test", slog.Default(), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/live", nil)
	request.Header.Set("X-Request-ID", "test-request")

	server.httpServer.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("X-Request-ID") != "test-request" {
		t.Fatalf("request ID = %q", recorder.Header().Get("X-Request-ID"))
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("body = %q", body)
	}
}

func TestHandlerHidesReadinessFailureDetails(t *testing.T) {
	server := New(testConfig(), "test", nil, func(context.Context) error {
		return io.ErrUnexpectedEOF
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)

	server.httpServer.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, io.ErrUnexpectedEOF.Error()) || !strings.Contains(body, `"code":"NOT_READY"`) {
		t.Fatalf("body = %q", body)
	}
}

func TestMountAPIAlongsideExactRootMetadata(t *testing.T) {
	server := New(testConfig(), "test", nil, nil)
	server.Mount("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/api/v1/organizations", http.StatusNoContent},
		{http.MethodPost, "/api/v1/auth/login", http.StatusNoContent},
		{http.MethodPatch, "/api/v1/tasks/example", http.StatusNoContent},
		{http.MethodGet, "/missing", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.status {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, recorder.Code, test.status)
		}
	}
}
