package cliclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "ofx_pat_client-test-token-0123456789"

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	client, err := New(server.URL, testToken, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second,
		CheckRedirect: client.HTTP.CheckRedirect}
	client.Sleep = func(time.Duration) {}
	return client, server
}

func dropConnection(w http.ResponseWriter) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	_ = conn.Close()
}

func TestDoSendsBearerAndParsesErrors(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("User-Agent") != "openfox-cli/9.9.9" || r.URL.Path != "/api/v1/x" {
			t.Errorf("headers = %v path = %s", r.Header, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		// 服务端异常回显了令牌：必须被抹掉。
		_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"bad ` + testToken + `","request_id":"rid-7"}}`))
	})
	_, err := client.Do(context.Background(), Request{Method: "GET", Path: "/v1/x"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 409 || api.Code != "CONFLICT" || api.RequestID != "rid-7" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(api.Message, testToken) {
		t.Fatalf("token in error: %v", err)
	}
	// 非 JSON 错误体回退到状态文本。
	client2, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "<html>", 502) })
	_, err = client2.Do(context.Background(), Request{Method: "GET", Path: "/v1/x"})
	if !errors.As(err, &api) || api.Status != 502 || api.Message == "" {
		t.Fatalf("err = %#v", err)
	}
}

func TestRetryOnlyForIdempotentRequestsAndOnlyOnce(t *testing.T) {
	var calls atomic.Int32
	var keys []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		dropConnection(w)
	})
	var network *NetworkError
	// 带幂等标记：共 2 次（原请求 + 1 次重试），键相同。
	_, err := client.Do(context.Background(), Request{Method: "POST", Path: "/v1/x", Body: map[string]string{"a": "b"}, IdempotencyKey: "key-12345678", Idempotent: true})
	if !errors.As(err, &network) || calls.Load() != 2 || keys[0] != "key-12345678" || keys[1] != keys[0] {
		t.Fatalf("idempotent: err = %v calls = %d keys = %v", err, calls.Load(), keys)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in network error: %v", err)
	}
	// 无幂等标记：只有 1 次。
	calls.Store(0)
	_, err = client.Do(context.Background(), Request{Method: "PATCH", Path: "/v1/x", Body: map[string]string{}})
	if !errors.As(err, &network) || calls.Load() != 1 {
		t.Fatalf("non-idempotent: err = %v calls = %d", err, calls.Load())
	}
	// HTTP 错误状态（即使 5xx）不触发重试。
	var statusCalls atomic.Int32
	client2, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { statusCalls.Add(1); w.WriteHeader(503) })
	_, err = client2.Do(context.Background(), Request{Method: "POST", Path: "/v1/x", IdempotencyKey: "key-12345678", Idempotent: true})
	var api *APIError
	if !errors.As(err, &api) || statusCalls.Load() != 1 {
		t.Fatalf("503: err = %v calls = %d", err, statusCalls.Load())
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	t.Cleanup(other.Close)
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
	_, err := client.Do(context.Background(), Request{Method: "GET", Path: "/v1/x"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 302 || leaked.Load() {
		t.Fatalf("err = %v leaked = %v", err, leaked.Load())
	}
}

func TestOverallTimeoutIsBounded(t *testing.T) {
	if ConnectTimeout != 10*time.Second || RequestTimeout != 30*time.Second {
		t.Fatalf("timeouts = %v %v", ConnectTimeout, RequestTimeout)
	}
	if DefaultHTTPClient().Timeout != RequestTimeout {
		t.Fatal("default client has no overall timeout")
	}
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { time.Sleep(300 * time.Millisecond) })
	client.HTTP.Timeout = 50 * time.Millisecond
	var network *NetworkError
	if _, err := client.Do(context.Background(), Request{Method: "GET", Path: "/v1/x"}); !errors.As(err, &network) {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeServerAndInsecureRemote(t *testing.T) {
	good := map[string]string{"https://a.example/": "https://a.example", " http://127.0.0.1:8080 ": "http://127.0.0.1:8080", "https://a.example/sub/": "https://a.example/sub"}
	for in, want := range good {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Fatalf("%q -> %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://a", "a.example", "https://u:p@a.example", "https://a.example?x=1", "https://a.example#f", "https://"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for url, want := range map[string]bool{"http://example.com": true, "http://10.0.0.5:80": true, "https://example.com": false,
		"http://localhost:8080": false, "http://127.0.0.1": false, "http://[::1]:8080": false} {
		if InsecureRemote(url) != want {
			t.Fatalf("InsecureRemote(%q) = %v", url, !want)
		}
	}
}
