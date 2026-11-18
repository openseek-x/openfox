package slack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var _ API = (*Client)(nil)
var testNow = time.Unix(1800000000, 0).UTC()
var testApp = AppCredentials{ClientID: "123.456", ClientSecret: "synthetic-client-secret"}

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "slack.com" {
			t.Error("wrong fixed host")
			http.Error(w, "denied", 403)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "slack.com:443" {
			return nil, errors.New("external network forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &Client{Transport: tr, Now: func() time.Time { return testNow }}, srv
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func jsonReply(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, s)
}
func goodManifest() Manifest {
	return Manifest{DisplayInformation: DisplayInformation{Name: "OpenFox 测试"}, Features: ManifestFeatures{BotUser: ManifestBot{DisplayName: "OpenFox"}, AppHome: ManifestHome{MessagesTabEnabled: true}}, OAuthConfig: ManifestOAuth{RedirectURLs: []string{"https://openfox.example.test/callback"}, Scopes: ManifestScopes{Bot: []string{"chat:write", "im:write"}}}, Settings: ManifestSettings{TokenRotationEnabled: true}}
}
func TestOAuthAndManifestProtocols(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RawQuery != "" {
			t.Error("credential API used query")
		}
		r.ParseForm()
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			id, s, ok := r.BasicAuth()
			if !ok || id != testApp.ClientID || s != testApp.ClientSecret || r.Form.Get("client_secret") != "" {
				t.Error("confidential client Basic auth missing")
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				reply(w, map[string]any{"ok": true, "access_token": "next-access", "refresh_token": "next-refresh", "expires_in": 43200, "token_type": "bot", "scope": "im:write,chat:write"})
			} else {
				if r.Form.Get("code") != "code" || r.Form.Get("redirect_uri") != "https://openfox.example.test/callback" {
					t.Error("bad code exchange")
				}
				reply(w, map[string]any{"ok": true, "access_token": "bot-access", "refresh_token": "bot-refresh", "expires_in": 43200, "token_type": "bot", "scope": "chat:write,im:write", "app_id": "A1", "bot_user_id": "W1", "team": map[string]any{"id": "T1", "name": "Workspace"}, "authed_user": map[string]any{"id": "W2"}, "is_enterprise_install": false})
			}
		case "/api/auth.test":
			if r.Header.Get("Authorization") != "Bearer bot-access" {
				t.Error("missing bearer")
			}
			reply(w, map[string]any{"ok": true, "team_id": "T1", "user_id": "W1", "bot_id": "B1"})
		case "/api/tooling.tokens.rotate":
			if r.Form.Get("refresh_token") != "config-refresh" || r.Header.Get("Authorization") != "" {
				t.Error("bad config rotate")
			}
			reply(w, map[string]any{"ok": true, "token": "config-access", "refresh_token": "next-config-refresh", "team_id": "T1", "user_id": "W2", "iat": testNow.Unix(), "exp": testNow.Add(12 * time.Hour).Unix()})
		case "/api/apps.manifest.create":
			if r.Header.Get("Authorization") != "Bearer config-access" || r.Form.Get("manifest") == "" {
				t.Error("bad create")
			}
			reply(w, map[string]any{"ok": true, "app_id": "A2", "credentials": map[string]string{"client_id": "234.567", "client_secret": "new-secret", "signing_secret": "new-signing"}, "oauth_authorize_url": "https://attacker.example/ignore-this-field"})
		case "/api/apps.manifest.update":
			if r.Form.Get("app_id") != "A2" {
				t.Error("bad update app")
			}
			reply(w, map[string]any{"ok": true, "app_id": "A2"})
		case "/api/apps.manifest.export":
			reply(w, map[string]any{"ok": true, "manifest": goodManifest()})
		default:
			t.Error("unexpected method", r.URL.Path)
			http.Error(w, "bad", 400)
		}
	})
	ctx := context.Background()
	install, e := client.ExchangeOAuth(ctx, testApp, "code", "https://openfox.example.test/callback")
	if e != nil || install.AppID != "A1" || install.InstallerUserID != "W2" || install.ExpiresAt != testNow.Add(12*time.Hour) {
		t.Fatal("install", e)
	}
	grant, e := client.RefreshOAuth(ctx, testApp, "old-refresh")
	if e != nil || grant.TeamID != "" || grant.AppID != "" || grant.AccessToken != "next-access" {
		t.Fatal("refresh invented identity", e)
	}
	auth, e := client.AuthTest(ctx, install.AccessToken)
	if e != nil || auth.UserID != install.BotUserID || auth.TeamID != install.TeamID {
		t.Fatal("auth identity", e)
	}
	config, e := client.RotateConfiguration(ctx, "config-refresh")
	if e != nil || config.UserID != "W2" || config.TeamID != "T1" {
		t.Fatal("config proof", e)
	}
	app, e := client.CreateManifest(ctx, config.Token, goodManifest())
	if e != nil || app.AppID != "A2" {
		t.Fatal("create", e)
	}
	if e = client.UpdateManifest(ctx, config.Token, app.AppID, goodManifest()); e != nil {
		t.Fatal(e)
	}
	exported, e := client.ExportManifest(ctx, config.Token, app.AppID)
	if e != nil || !jsonObject(exported) {
		t.Fatal(e)
	}
	if calls.Load() != 7 {
		t.Fatal("unexpected retries", calls.Load())
	}
	for _, v := range []any{testApp, install, grant, config, app} {
		s := fmt.Sprintf("%v %+v %#v", v, v, v)
		if strings.Contains(s, "synthetic") || strings.Contains(s, "bot-access") || strings.Contains(s, "new-secret") {
			t.Fatal("formatter leaked secret")
		}
	}
}
func TestAuthURLSeparation(t *testing.T) {
	u, e := AuthorizeURL("123.456", "https://openfox.example.test/callback", "0123456789abcdef", "T1", []string{"chat:write"})
	if e != nil {
		t.Fatal(e)
	}
	p, _ := url.Parse(u)
	if p.Host != "slack.com" || p.Path != "/oauth/v2/authorize" || p.Query().Has("user_scope") {
		t.Fatal("bot authorization")
	}
	u, e = OIDCAuthorizeURL("123.456", "https://openfox.example.test/identity", "0123456789abcdef", "0123456789abcdef", "T1")
	if e != nil {
		t.Fatal(e)
	}
	p, _ = url.Parse(u)
	if p.Query().Get("scope") != "openid profile" || p.Path != "/openid/connect/authorize" {
		t.Fatal("OIDC scopes")
	}
	for _, redirect := range []string{"http://example.test/cb", "https://u:p@example.test/cb", "https://example.test/cb#x", "https://example.test/cb?host=x"} {
		if _, e = AuthorizeURL("123", redirect, "0123456789abcdef", "", []string{"chat:write"}); e == nil {
			t.Fatal("invalid redirect accepted")
		}
	}
}
func TestSlackErrorsAndUnknownWrites(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
		retry  bool
	}{
		{"rate", 429, `{"ok":false,"error":"ratelimited"}`, ErrRateLimited, true}, {"auth", 200, `{"ok":false,"error":"invalid_auth"}`, ErrAuth, false}, {"scope", 200, `{"ok":false,"error":"missing_scope"}`, ErrForbidden, false}, {"explicit400", 400, `{"ok":false,"error":"invalid_arguments"}`, ErrInvalid, false}, {"http401", 401, `{}`, ErrAuth, false}, {"fatal", 200, `{"ok":false,"error":"fatal_error"}`, ErrUnknown, false}, {"internal", 200, `{"ok":false,"error":"internal_error"}`, ErrUnknown, false}, {"unknowncode", 200, `{"ok":false,"error":"synthetic-secret-do-not-echo"}`, ErrUnknown, false}, {"server", 500, `{}`, ErrUnknown, false}, {"unexpected201", 201, `{"ok":true,"channel":"C1","ts":"1.000001"}`, ErrUnknown, false}, {"missingok", 200, `{"channel":"C1"}`, ErrUnknown, false}, {"null", 200, `null`, ErrUnknown, false}, {"duplicate", 200, `{"ok":false,"ok":true}`, ErrUnknown, false}, {"badreceipt", 200, `{"ok":true,"channel":"C2","ts":"1.000001"}`, ErrUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, e := c.PostMessage(context.Background(), "synthetic-token", PostMessageInput{Channel: "C1", Text: "hello"})
			if !errors.Is(e, tc.want) {
				t.Fatalf("got %v want %v", e, tc.want)
			}
			if strings.Contains(e.Error(), "synthetic") {
				t.Fatal("error leaked input")
			}
			if tc.retry {
				var x *Error
				if !errors.As(e, &x) || x.RetryAfter != time.Minute {
					t.Fatal("retry header")
				}
			}
			if count.Load() != 1 {
				t.Fatal("mutation retried")
			}
		})
	}
}
func TestTLSRedirectCancellationAndBounds(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		var count atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.Header().Set("Location", "https://other.example/secret")
			w.WriteHeader(302)
		})
		_, e := c.AuthTest(context.Background(), "token")
		if e == nil || count.Load() != 1 {
			t.Fatal("followed redirect")
		}
	})
	for _, mode := range []string{"unknown-ca", "wrong-hostname"} {
		t.Run(mode, func(t *testing.T) {
			var count atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); reply(w, map[string]any{"ok": true}) })
			tr := c.Transport.(*http.Transport)
			if mode == "unknown-ca" {
				tr.TLSClientConfig.RootCAs = x509.NewCertPool()
			} else {
				tr.TLSClientConfig.ServerName = "wrong.example"
			}
			_, e := c.AuthTest(context.Background(), "token")
			if e == nil || count.Load() != 0 {
				t.Fatal("TLS validation disabled")
			}
		})
	}
	t.Run("before-cancel", func(t *testing.T) {
		var count atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, e := c.PostMessage(ctx, "token", PostMessageInput{Channel: "C1", Text: "text"})
		if !errors.Is(e, context.Canceled) || count.Load() != 0 {
			t.Fatal("canceled call sent")
		}
	})
	t.Run("sent-timeout", func(t *testing.T) {
		var count atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			count.Add(1)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, e := c.PostMessage(ctx, "token", PostMessageInput{Channel: "C1", Text: "text"})
		if !errors.Is(e, ErrUnknown) || count.Load() != 1 || time.Since(started) > time.Second {
			t.Fatal("sent timeout semantics", e)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"ok":true,"padding":"`+strings.Repeat("x", MaxResponseBytes)+`"}`)
		})
		_, e := c.AuthTest(context.Background(), "token")
		if !errors.Is(e, ErrIncomplete) {
			t.Fatal("body not bounded", e)
		}
	})
	t.Run("disconnect-after-send", func(t *testing.T) {
		var count atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			count.Add(1)
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		})
		_, e := c.PostMessage(context.Background(), "token", PostMessageInput{Channel: "C1", Text: "text"})
		if !errors.Is(e, ErrUnknown) || count.Load() != 1 {
			t.Fatal("unknown replay", e)
		}
	})
}

func TestMutationCredentialAndManifestBoundaries(t *testing.T) {
	cases := []struct {
		name, path, body string
		call             func(*Client) error
		want             error
	}{
		{"oauth-missing-refresh", "oauth.v2.access", `{"ok":true,"access_token":"access","expires_in":43200,"token_type":"bot","scope":"chat:write"}`, func(c *Client) error { _, e := c.RefreshOAuth(context.Background(), testApp, "refresh"); return e }, ErrUnknown},
		{"oauth-missing-installer", "oauth.v2.access", `{"ok":true,"access_token":"access","refresh_token":"refresh","expires_in":43200,"token_type":"bot","scope":"chat:write","team":{"id":"T1"},"app_id":"A1","bot_user_id":"W1"}`, func(c *Client) error {
			_, e := c.ExchangeOAuth(context.Background(), testApp, "code", "https://openfox.example.test/cb")
			return e
		}, ErrUnknown},
		{"oauth-user-token", "oauth.v2.access", `{"ok":true,"access_token":"access","refresh_token":"refresh","expires_in":43200,"token_type":"bot","authed_user":{"access_token":"unrequested-user-access"}}`, func(c *Client) error { _, e := c.RefreshOAuth(context.Background(), testApp, "refresh"); return e }, ErrUnsupported},
		{"oauth-enterprise", "oauth.v2.access", `{"ok":true,"access_token":"access","refresh_token":"refresh","expires_in":43200,"token_type":"bot","is_enterprise_install":true}`, func(c *Client) error { _, e := c.RefreshOAuth(context.Background(), testApp, "refresh"); return e }, ErrUnsupported},
		{"config-missing-owner", "tooling.tokens.rotate", fmt.Sprintf(`{"ok":true,"token":"access","refresh_token":"refresh","team_id":"T1","iat":%d,"exp":%d}`, testNow.Unix(), testNow.Add(time.Hour).Unix()), func(c *Client) error { _, e := c.RotateConfiguration(context.Background(), "refresh"); return e }, ErrUnknown},
		{"create-lost-secret", "apps.manifest.create", `{"ok":true,"app_id":"A1","credentials":{"client_id":"123","signing_secret":"signing"}}`, func(c *Client) error {
			_, e := c.CreateManifest(context.Background(), "token", goodManifest())
			return e
		}, ErrUnknown},
		{"update-wrong-app", "apps.manifest.update", `{"ok":true,"app_id":"A2"}`, func(c *Client) error { return c.UpdateManifest(context.Background(), "token", "A1", goodManifest()) }, ErrUnknown},
		{"export-null", "apps.manifest.export", `{"ok":true,"manifest":null}`, func(c *Client) error { _, e := c.ExportManifest(context.Background(), "token", "A1"); return e }, ErrIncomplete},
		{"dm-wrong-user", "conversations.open", `{"ok":true,"channel":{"id":"D1","is_im":true,"user":"W2"}}`, func(c *Client) error { _, e := c.OpenDM(context.Background(), "token", "W1"); return e }, ErrUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/"+tc.path {
					t.Error("wrong path")
				}
				jsonReply(w, tc.body)
			})
			if e := tc.call(c); !errors.Is(e, tc.want) {
				t.Fatal("wrong boundary", e)
			}
			if calls.Load() != 1 {
				t.Fatal("mutation repeated")
			}
		})
	}
	t.Run("manifest-local-validation", func(t *testing.T) {
		var calls atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
		for _, change := range []func(*Manifest){func(m *Manifest) { m.DisplayInformation.Name = strings.Repeat("界", 36) }, func(m *Manifest) { m.Settings.SocketModeEnabled = true }, func(m *Manifest) { m.Settings.OrgDeployEnabled = true }, func(m *Manifest) { m.Settings.TokenRotationEnabled = false }, func(m *Manifest) { m.OAuthConfig.RedirectURLs = []string{"http://localhost/callback"} }, func(m *Manifest) {
			m.Settings.EventSubscriptions = &ManifestEvents{RequestURL: "https://example.test/events?token=secret", BotEvents: []string{"app_mention"}}
		}} {
			m := goodManifest()
			change(&m)
			if _, e := c.CreateManifest(context.Background(), "token", m); !errors.Is(e, ErrInvalid) {
				t.Fatal("unsafe manifest")
			}
		}
		if calls.Load() != 0 {
			t.Fatal("invalid manifest sent")
		}
	})
}

func TestRateLimitDelayIsNotShortened(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		delay       time.Duration
		invalid     bool
	}{
		{"long seconds", "900000", 900000 * time.Second, false},
		{"missing fallback", "", time.Second, false},
		{"duration overflow", "9223372037", 0, true},
		{"integer overflow", "999999999999999999999999999", 0, true},
		{"HTTP date unsupported", "Wed, 21 Oct 2015 07:28:00 GMT", 0, true},
		{"negative", "-1", 0, true}, {"zero", "0", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.value != "" {
					w.Header().Set("Retry-After", tc.value)
				}
				w.WriteHeader(429)
				io.WriteString(w, `{"ok":false,"error":"ratelimited"}`)
			})
			_, e := client.PostMessage(context.Background(), "synthetic", PostMessageInput{Channel: "C1", Text: "hi"})
			var api *Error
			if !errors.As(e, &api) {
				t.Fatal(e)
			}
			if tc.invalid {
				if !errors.Is(e, ErrInvalid) || api.Code != "retry_after_invalid" || api.RetryAfter != 0 {
					t.Fatalf("unsafe retry %v delay=%v", e, api.RetryAfter)
				}
			} else if !errors.Is(e, ErrRateLimited) || api.RetryAfter != tc.delay {
				t.Fatalf("delay %v want%v", api.RetryAfter, tc.delay)
			}
			if calls.Load() != 1 {
				t.Fatal("retried")
			}
		})
	}
}
