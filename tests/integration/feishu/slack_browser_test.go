package feishuint

// The browser uses actual OpenFox HTTP/session/CSRF/PG/worker code. Only Slack's
// fixed HTTPS origin is redirected to this owned TLS peer, with a verified CA.
import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/integrations"
)

type slackBrowserApp struct {
	ID, Client, Secret, Signing, User, Bot string
	Manifest                               json.RawMessage
}
type slackBrowserPeer struct {
	mu                        sync.Mutex
	key                       *rsa.PrivateKey
	team                      string
	apps                      map[string]slackBrowserApp // client ID
	nonces                    map[string]string          // synthetic code
	messages                  []map[string]any
	creates, updates, unfurls int
	unknownNext               bool
}

func slackBrowserJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (p *slackBrowserPeer) jwt(code string) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "browser"})
	access := sha256.Sum256([]byte("synthetic-oidc-access"))
	c, _ := json.Marshal(map[string]any{"iss": "https://slack.com", "aud": "123.456", "sub": "independent-subject", "nonce": p.nonces[code], "exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(), "at_hash": base64.RawURLEncoding.EncodeToString(access[:16]), "https://slack.com/team_id": p.team, "https://slack.com/user_id": "UHUMAN"})
	b := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(b))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	return b + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func (p *slackBrowserPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = r.ParseForm()
	clientID, _, _ := r.BasicAuth()
	a := p.apps[clientID]
	if a.ID == "" {
		a = p.apps[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer synthetic-access-")]
	}
	switch r.URL.Path {
	case "/api/oauth.v2.access":
		if a.ID == "" {
			http.Error(w, "unknown synthetic client", 400)
			return
		}
		scopes := []string{"chat:write", "im:write", "im:history", "channels:read", "groups:read", "im:read", "users:read", "links:read", "links:write"}
		if a.Client != "123.456" {
			scopes = []string{"chat:write", "im:write", "im:history", "channels:read", "groups:read", "im:read", "users:read", "app_mentions:read", "channels:history", "groups:history"}
		}
		slackBrowserJSON(w, map[string]any{"ok": true, "access_token": "synthetic-access-" + a.Client, "refresh_token": "synthetic-refresh-" + a.Client, "expires_in": 43200, "token_type": "bot", "scope": strings.Join(scopes, ","), "app_id": a.ID, "bot_user_id": a.User, "team": map[string]string{"id": p.team, "name": "Controlled Slack workspace"}, "authed_user": map[string]string{"id": "UHUMAN"}})
	case "/api/auth.test":
		slackBrowserJSON(w, map[string]any{"ok": true, "team_id": p.team, "user_id": a.User, "bot_id": a.Bot})
	case "/api/users.info":
		slackBrowserJSON(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": p.team, "name": "Controlled member", "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
	case "/api/openid.connect.token":
		slackBrowserJSON(w, map[string]any{"ok": true, "access_token": "synthetic-oidc-access", "token_type": "Bearer", "id_token": p.jwt(r.Form.Get("code"))})
	case "/.well-known/openid-configuration":
		slackBrowserJSON(w, map[string]any{"issuer": "https://slack.com", "authorization_endpoint": "https://slack.com/openid/connect/authorize", "token_endpoint": "https://slack.com/api/openid.connect.token", "userinfo_endpoint": "https://slack.com/api/openid.connect.userInfo", "jwks_uri": "https://slack.com/openid/connect/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/openid/connect/keys":
		slackBrowserJSON(w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "browser", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}})
	case "/api/openid.connect.userInfo":
		slackBrowserJSON(w, map[string]any{"ok": true, "sub": "independent-subject", "https://slack.com/team_id": p.team, "https://slack.com/user_id": "UHUMAN", "name": "Controlled member"})
	case "/api/tooling.tokens.rotate":
		slackBrowserJSON(w, map[string]any{"ok": true, "token": "synthetic-config-access", "refresh_token": "synthetic-next-refresh", "team_id": p.team, "user_id": "UHUMAN", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	case "/api/apps.manifest.create":
		p.creates++
		id := fmt.Sprintf("AD%d", p.creates)
		client := fmt.Sprintf("456.%d", p.creates)
		v := slackBrowserApp{ID: id, Client: client, Secret: "synthetic-dynamic-secret", Signing: "synthetic-dynamic-signing", User: "U" + id, Bot: "B" + id, Manifest: json.RawMessage(r.Form.Get("manifest"))}
		p.apps[client] = v
		slackBrowserJSON(w, map[string]any{"ok": true, "app_id": id, "credentials": map[string]string{"client_id": client, "client_secret": v.Secret, "signing_secret": v.Signing}})
	case "/api/apps.manifest.update":
		for key, v := range p.apps {
			if v.ID == r.Form.Get("app_id") {
				v.Manifest = json.RawMessage(r.Form.Get("manifest"))
				p.apps[key] = v
				p.updates++
				slackBrowserJSON(w, map[string]any{"ok": true, "app_id": v.ID})
				return
			}
		}
		slackBrowserJSON(w, map[string]any{"ok": false, "error": "app_not_found"})
	case "/api/apps.manifest.export":
		for _, v := range p.apps {
			if v.ID == r.URL.Query().Get("app_id") || v.ID == r.Form.Get("app_id") {
				slackBrowserJSON(w, map[string]any{"ok": true, "manifest": v.Manifest})
				return
			}
		}
		slackBrowserJSON(w, map[string]any{"ok": false, "error": "app_not_found"})
	case "/api/conversations.info":
		id := r.URL.Query().Get("channel")
		c := map[string]any{"id": id, "is_im": strings.HasPrefix(id, "D"), "is_mpim": false, "is_shared": false, "is_ext_shared": false, "is_archived": false}
		if strings.HasPrefix(id, "D") {
			c["user"] = "UHUMAN"
		} else {
			c["is_channel"] = true
			c["is_group"] = false
			c["is_member"] = true
			c["is_private"] = true
			c["name"] = "controlled-channel"
		}
		slackBrowserJSON(w, map[string]any{"ok": true, "channel": c})
	case "/api/conversations.members":
		slackBrowserJSON(w, map[string]any{"ok": true, "members": []string{"UHUMAN", a.User}, "response_metadata": map[string]string{"next_cursor": ""}})
	case "/api/conversations.open":
		slackBrowserJSON(w, map[string]any{"ok": true, "channel": map[string]any{"id": "D01", "is_im": true, "user": "UHUMAN", "is_mpim": false, "is_archived": false, "is_shared": false, "is_ext_shared": false}})
	case "/api/conversations.replies", "/api/conversations.history":
		items := []map[string]any{}
		for _, m := range p.messages {
			if m["channel"] == r.URL.Query().Get("channel") && (r.URL.Query().Get("ts") == "" || m["thread_ts"] == r.URL.Query().Get("ts")) {
				items = append(items, m)
			}
		}
		slackBrowserJSON(w, map[string]any{"ok": true, "messages": items, "has_more": false, "response_metadata": map[string]string{"next_cursor": ""}})
	case "/api/chat.postMessage":
		var m map[string]any
		if json.NewDecoder(r.Body).Decode(&m) != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		ts := fmt.Sprintf("%d.%06d", time.Now().Unix(), len(p.messages)+1)
		m["ts"] = ts
		m["type"] = "message"
		m["user"] = a.User
		m["bot_id"] = a.Bot
		m["app_id"] = a.ID
		m["team"] = p.team
		p.messages = append(p.messages, m)
		if p.unknownNext {
			p.unknownNext = false
			slackBrowserJSON(w, map[string]any{"ok": true})
			return
		}
		slackBrowserJSON(w, map[string]any{"ok": true, "channel": m["channel"], "ts": ts})
	case "/api/chat.unfurl":
		p.unfurls++
		slackBrowserJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "unexpected controlled Slack method", 404)
	}
}
func slackBrowserTransport(t *testing.T, p *slackBrowserPeer) http.RoundTripper {
	t.Helper()
	server := httptest.NewTLSServer(p)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "slack.com:443" {
			return nil, errors.New("external network forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

func TestSlackBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I15_BROWSER_FILE")
	if file == "" {
		t.Skip("owned browser harness not requested")
	}
	dsn, err := url.Parse(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil || !strings.HasPrefix(dsn.Path, "/openfox_i15_") {
		t.Fatal("exclusive I15 database required")
	}
	web := os.Getenv("OPENFOX_I15_WEB_URL")
	u, err := url.Parse(web)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Scheme != "http" {
		t.Fatal("owned loopback frontend required")
	}
	b := &botWorld{world: setup(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	b.ctx = ctx
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &slackBrowserPeer{key: key, team: "T" + b.org, apps: map[string]slackBrowserApp{}, nonces: map[string]string{}}
	p.apps["123.456"] = slackBrowserApp{ID: "A" + b.org, Client: "123.456", Secret: "synthetic-central-secret", Signing: "synthetic-central-signing", User: "UCENTRAL", Bot: "BCENTRAL"}
	transport := slackBrowserTransport(t, p)
	b.service = integrations.Service{Box: b.box, PublicURL: "https://openfox.example.test", SlackClientID: "123.456", SlackClientSecret: "synthetic-central-secret", SlackSigningSecret: "synthetic-central-signing", SlackTransport: transport}
	auth := identity.Service{Store: identity.PostgresStore{Pool: b.pool}, PasswordHasher: b.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, b.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: b.box, PublicURL: b.service.PublicURL, SlackClientID: b.service.SlackClientID, SlackClientSecret: b.service.SlackClientSecret, SlackSigningSecret: b.service.SlackSigningSecret, SlackTransport: transport})))
	defer server.Close()
	b.server = server
	b.owner = b.login(server.URL+"/api", "fs-owner-"+b.org[:8]+"@example.test", b.ownerID)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'Slack controlled computer',clock_timestamp())`, b.deviceID, "i15-browser-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	agentID := b.createAgent(b.owner, b.foxSpace, "Slack browser Agent", "SHARED")
	worker := &integrations.IMWorker{Pool: b.pool, Service: b.service, Instance: "i15-browser-" + b.org}
	postEvent := func(appID string, value any) (int, []byte, error) {
		secret := "synthetic-central-signing"
		if appID != "central" {
			secret = "synthetic-dynamic-signing"
		}
		body, _ := json.Marshal(value)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte("v0:" + ts + ":"))
		mac.Write(body)
		request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/integrations/slack/events/"+appID, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Slack-Request-Timestamp", ts)
		request.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		return response.StatusCode, raw, e
	}
	if status, _, e := postEvent("central", map[string]string{"type": "url_verification", "challenge": "controlled-challenge"}); e != nil || status != 200 {
		t.Fatal("central challenge", status, e)
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" && r.URL.Path != "/state" {
			http.Error(w, "method", 405)
			return
		}
		var input struct {
			SessionID string `json:"session_id"`
			Purpose   string `json:"purpose"`
			AppID     string `json:"app_id"`
			Channel   string `json:"channel"`
			Text      string `json:"text"`
			ThreadTS  string `json:"thread_ts"`
			Unknown   bool   `json:"unknown"`
		}
		if r.Method == "POST" && json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input) != nil {
			http.Error(w, "body", 400)
			return
		}
		switch r.URL.Path {
		case "/complete-oauth":
			status, raw := b.owner.do("GET", b.path("/integrations/slack/oauth/sessions/"+input.SessionID), nil)
			if status != 200 {
				http.Error(w, "session unavailable", status)
				return
			}
			var session integrations.SlackOAuthSession
			if json.Unmarshal([]byte(raw), &session) != nil || session.Purpose != input.Purpose {
				http.Error(w, "session shape", 400)
				return
			}
			a, e := url.Parse(session.AuthorizeURL)
			if e != nil || a.Host != "slack.com" {
				http.Error(w, "authorize URL", 400)
				return
			}
			p.mu.Lock()
			p.nonces[session.ID] = a.Query().Get("nonce")
			p.mu.Unlock()
			path := "/api/v1/integrations/slack/callback"
			if input.Purpose == "personal" {
				path = "/api/v1/integrations/slack/identity/callback"
			}
			request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path+"?"+url.Values{"state": {a.Query().Get("state")}, "code": {session.ID}}.Encode(), nil)
			response, e := (&http.Client{Jar: b.owner.http.Jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
			if e != nil {
				http.Error(w, "callback transport", 500)
				return
			}
			response.Body.Close()
			if response.StatusCode != 302 {
				http.Error(w, "callback status", 502)
				return
			}
			slackBrowserJSON(w, map[string]bool{"ok": true})
		case "/challenge":
			status, raw, e := postEvent(input.AppID, map[string]string{"type": "url_verification", "challenge": "controlled-dynamic-challenge"})
			if e != nil {
				http.Error(w, "challenge transport", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write(raw)
		case "/pump":
			if e := worker.RunOrganization(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			slackBrowserJSON(w, map[string]bool{"ok": true})
		case "/event":
			var app integrations.SlackApp
			var config integrations.SlackConfiguration
			status, raw := b.owner.do("GET", b.path("/integrations/slack/configuration"), nil)
			if status != 200 || json.Unmarshal([]byte(raw), &config) != nil {
				http.Error(w, "configuration", 500)
				return
			}
			for _, candidate := range config.Apps {
				if candidate.ID == input.AppID {
					app = candidate
				}
			}
			if app.ID == "" {
				http.Error(w, "app", 404)
				return
			}
			kind := "channel"
			if strings.HasPrefix(input.Channel, "D") {
				kind = "im"
			}
			ts := fmt.Sprintf("%d.%09d", time.Now().Unix(), time.Now().Nanosecond())
			ev := map[string]any{"type": "event_callback", "api_app_id": app.ExternalAppID, "team_id": app.TeamID, "event_id": "E" + b.newUUID(), "event_time": time.Now().Unix(), "event": map[string]any{"type": "message", "channel": input.Channel, "channel_type": kind, "user": "UHUMAN", "ts": ts, "thread_ts": input.ThreadTS, "text": input.Text}}
			route := app.ID
			if app.Kind == "CENTRAL" {
				route = "central"
			}
			status, _, e := postEvent(route, ev)
			if e != nil || status != 200 {
				http.Error(w, "event rejected", 500)
				return
			}
			if e = worker.RunOrganization(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			slackBrowserJSON(w, map[string]string{"ts": ts})
		case "/unknown-next":
			p.mu.Lock()
			p.unknownNext = input.Unknown
			p.mu.Unlock()
			slackBrowserJSON(w, map[string]bool{"ok": true})
		case "/state":
			var routes, inputs, events int
			b.scan(&routes, `SELECT count(*) FROM openfox.slack_routes WHERE organization_id=$1::uuid`, b.org)
			b.scan(&inputs, `SELECT count(*) FROM openfox.slack_input_authorizations WHERE organization_id=$1::uuid`, b.org)
			b.scan(&events, `SELECT count(*) FROM openfox.slack_events WHERE organization_id=$1::uuid AND state='DONE'`, b.org)
			p.mu.Lock()
			defer p.mu.Unlock()
			slackBrowserJSON(w, map[string]any{"routes": routes, "inputs": inputs, "events_done": events, "posts": p.messages, "creates": p.creates, "updates": p.updates, "unfurls": p.unfurls})
		default:
			http.NotFound(w, r)
		}
	}))
	defer control.Close()
	raw, _ := json.Marshal(map[string]string{"api_url": server.URL, "web_url": web, "fixture_url": control.URL, "organization_id": b.org, "space_id": b.foxSpace, "agent_id": agentID, "email": "fs-owner-" + b.org[:8] + "@example.test", "password": password})
	if e := os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness deadline")
		case <-ticker.C:
			if _, e := os.Stat(file + ".stop"); e == nil {
				return
			}
		}
	}
}
