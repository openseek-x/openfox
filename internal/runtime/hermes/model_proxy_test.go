package hermes

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/platform/safehttp"
	"openfox/internal/runtimepolicy"
)

func fixtureProxy(t *testing.T, h http.Handler, limit int) (*modelProxy, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewTLSServer(h)
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	client := safehttp.Client(safehttp.Options{RootCAs: roots, AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }})
	p, err := newModelProxy(context.Background(), runtimepolicy.HermesModelConfig{BaseURL: upstream.URL + "/api/v1", APIKey: "upstream-canary", Model: "fixed"}, limit, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, upstream
}

func TestModelProxyBindsTokenPathModelAndHeaders(t *testing.T) {
	var hits atomic.Int32
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer upstream-canary" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("upstream header/path escaped")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), 1024)
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/models", "", "", 403}, {"POST", "/chat/completions", "wrong", `{"model":"fixed"}`, 401},
		{"POST", "/chat/completions?other=1", p.token, `{"model":"fixed"}`, 403},
		{"POST", "/chat/completions", p.token, `{"model":"other"}`, 400},
		{"POST", "/chat/completions", p.token, `{"model":"fixed","model":"other"}`, 400},
		{"POST", "/chat/completions", p.token, `{"model":"fixed"} {}`, 400},
		{"POST", "/chat/completions", p.token, `{"model":"fixed","stream":false}`, 200},
	} {
		r, _ := http.NewRequest(tc.method, p.url+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Cookie", "private=1")
		r.Header.Set("X-Forwarded-Host", "other.invalid")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != tc.status {
			t.Errorf("%s: got %d want %d", tc.path, res.StatusCode, tc.status)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("unexpected outbound requests: %d", hits.Load())
	}
}

func TestModelProxyRejectsRedirectAndNeverUsesAmbientProxy(t *testing.T) {
	var sinkHits atomic.Int32
	sink := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sinkHits.Add(1) }))
	defer sink.Close()
	t.Setenv("HTTPS_PROXY", sink.URL)
	t.Setenv("NO_PROXY", "")
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", sink.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}), 1024)
	r, _ := http.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(`{"model":"fixed"}`))
	r.Header.Set("Authorization", "Bearer "+p.token)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if p.Err() == nil || sinkHits.Load() != 0 || res.Header.Get("Location") != "" {
		t.Fatal("redirect/proxy was allowed")
	}
}

func TestModelProxyStreamingCancelClosesTLS(t *testing.T) {
	closed := make(chan struct{})
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}), 1024)
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "POST", p.url+"/chat/completions", strings.NewReader(`{"model":"fixed"}`))
	r.Header.Set("Authorization", "Bearer "+p.token)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(res.Body, make([]byte, 13)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = res.Body.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancel did not close upstream TLS")
	}
}
