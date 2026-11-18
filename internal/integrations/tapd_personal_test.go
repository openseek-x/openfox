package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTAPDPersonalFourReads(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.URL.Path)
		if r.Method != "GET" || r.Host != "api.tapd.cn" {
			t.Errorf("unexpected target %s %s", r.Method, r.Host)
		}
		if len(calls) <= 2 {
			if r.Header.Get("Authorization") != "Bearer fake-user-token" {
				t.Error("expected user token")
			}
		} else {
			u, p, ok := r.BasicAuth()
			if !ok || u != "api-user" || p != "fake-parent-password" {
				t.Error("expected parent Basic")
			}
		}
		switch r.URL.Path {
		case "/users/info":
			_, _ = w.Write([]byte(`{"status":1,"data":{"id":"6081","nick":"ExactNick","name":"Test","enabled":"1","status_id":"1"}}`))
		case "/workspaces/get_workspace_info":
			if r.URL.Query().Get("workspace_id") != "10104801" {
				t.Error("wrong project")
			}
			_, _ = w.Write([]byte(`{"status":1,"data":{"Workspace":{"id":"10104801","company_id":123333,"status":"normal"}}}`))
		case "/workspaces/users":
			want := url.Values{"workspace_id": {"10104801"}, "user": {"ExactNick"}, "fields": {"user,email"}}
			if r.URL.Query().Encode() != want.Encode() {
				t.Errorf("unexpected member query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"status":1,"data":[{"UserWorkspace":{"user":"ExactNick","email":" MEMBER@EXAMPLE.COM "}}]}`))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	g := tapdTestGrant(srv)
	verified, err := VerifyTAPDPersonal(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.verified || verified.identityID != "6081" || verified.nick != "ExactNick" || len(calls) != 4 {
		t.Fatalf("unexpected proof or calls: %v %v", verified.verified, calls)
	}
}

func tapdTestGrant(srv *httptest.Server) TAPDPersonalGrant {
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return TAPDPersonalGrant{projectID: "10104801", companyID: "123333", email: "member@example.com", token: "fake-user-token", parentAuth: ProviderTokenInput{Account: "api-user", Secret: "fake-parent-password"}, transport: transport}
}

func TestTAPDPersonalVerificationFailsClosed(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		want             error
	}{
		{"inactive", "/users/info", `{"status":1,"data":{"id":"1","nick":"n","enabled":"0","status_id":"1"}}`, 200, ErrTAPDPersonalAuth},
		{"departed", "/users/info", `{"status":1,"data":{"id":"1","nick":"n","enabled":"1","status_id":"2"}}`, 200, ErrTAPDPersonalAuth},
		{"multiple nickname", "/users/info", `{"status":1,"data":{"id":"1","nick":"n,other","enabled":"1","status_id":"1"}}`, 200, ErrTAPDPersonalAuth},
		{"query operator", "/users/info", `{"status":1,"data":{"id":"1","nick":"LIKE<n>","enabled":"1","status_id":"1"}}`, 200, ErrTAPDPersonalAuth},
		{"wrong company", "/workspaces/get_workspace_info", `{"status":1,"data":{"Workspace":{"id":"10104801","company_id":999,"status":"normal"}}}`, 200, ErrTAPDPersonalPermission},
		{"closed project", "/workspaces/get_workspace_info", `{"status":1,"data":{"Workspace":{"id":"10104801","company_id":123333,"status":"closed"}}}`, 200, ErrTAPDPersonalPermission},
		{"many members", "/workspaces/users", `{"status":1,"data":[{"UserWorkspace":{"user":"n","email":"member@example.com"}},{"UserWorkspace":{"user":"other","email":"other@example.com"}}]}`, 200, ErrTAPDPersonalPermission},
		{"wrong nickname case", "/workspaces/users", `{"status":1,"data":[{"UserWorkspace":{"user":"N","email":"member@example.com"}}]}`, 200, ErrTAPDPersonalPermission},
		{"wrong email", "/workspaces/users", `{"status":1,"data":[{"UserWorkspace":{"user":"n","email":"other@example.com"}}]}`, 200, ErrTAPDPersonalEmail},
		{"empty email", "/workspaces/users", `{"status":1,"data":[{"UserWorkspace":{"user":"n","email":""}}]}`, 200, ErrTAPDPersonalEmail},
		{"unauthorized", "/users/info", "", 401, ErrTAPDPersonalAuth},
		{"forbidden", "/workspaces/get_workspace_info", "", 403, ErrTAPDPersonalPermission},
		{"rate limited", "/users/info", "", 429, ErrTAPDPersonalRate},
		{"redirect", "/users/info", "", 302, ErrUpstreamUnavailable},
		{"null", "/users/info", `{"status":1,"data":null}`, 200, ErrUpstreamUnavailable},
		{"invalid scalar", "/users/info", `{"status":1,"data":{"id":1.2}}`, 200, ErrUpstreamUnavailable},
		{"unsuccessful envelope", "/users/info", `{"status":0,"data":{"id":"1"},"info":"fake-user-token"}`, 200, ErrTAPDPersonalAuth},
		{"malformed", "/users/info", `{"status":1`, 200, ErrUpstreamUnavailable},
		{"large", "/users/info", strings.Repeat("x", (1<<20)+1), 200, ErrUpstreamUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Error("write attempted")
				}
				if r.URL.Path == tt.path {
					if tt.status == 302 {
						w.Header().Set("Location", "https://must-not-follow.example/")
					}
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				if r.URL.Path == "/users/info" {
					_, _ = w.Write([]byte(`{"status":1,"data":{"id":"1","nick":"n","enabled":"1","status_id":"1"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"status":1,"data":{"Workspace":{"id":"10104801","company_id":"123333","status":"normal"}}}`))
			}))
			defer srv.Close()
			g, err := VerifyTAPDPersonal(context.Background(), tapdTestGrant(srv))
			if !errors.Is(err, tt.want) || g.verified {
				t.Fatalf("got %v verified=%v want %v", err, g.verified, tt.want)
			}
			if strings.Contains(err.Error(), "fake-user-token") {
				t.Fatal("credential leaked")
			}
			if calls > 4 {
				t.Fatalf("retried or followed redirect: %d", calls)
			}
		})
	}
}

func TestTAPDPersonalScalarsAndNickname(t *testing.T) {
	for _, raw := range []string{`"123"`, `123`} {
		var n tapdScalar
		if err := json.Unmarshal([]byte(raw), &n); err != nil || n != "123" {
			t.Fatalf("%s %s %v", raw, n, err)
		}
	}
	for _, raw := range []string{`true`, `null`, `1.2`, `1e3`, `{}`} {
		var n tapdScalar
		if json.Unmarshal([]byte(raw), &n) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, nick := range []string{"", ",", "a;b", "a|b", "LIKE<x>", " x", "x\n"} {
		if tapdNickValid(nick) {
			t.Fatalf("accepted %q", nick)
		}
	}
	for _, nick := range []string{"ExactNick", "a.b@c", "中文昵称"} {
		if !tapdNickValid(nick) {
			t.Fatalf("rejected %q", nick)
		}
	}
}

func TestTAPDPersonalDeadlineStopsWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	grant, err := VerifyTAPDPersonal(ctx, tapdTestGrant(srv))
	if !errors.Is(err, ErrUpstreamUnavailable) || grant.verified || calls.Load() > 1 {
		t.Fatalf("deadline: err=%v verified=%v calls=%d", err, grant.verified, calls.Load())
	}
}
