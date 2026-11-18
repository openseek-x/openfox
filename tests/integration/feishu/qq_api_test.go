package feishuint

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/qq"
)

func TestQQManagementHTTPAndSignedCommitAcknowledgement(t *testing.T) {
	f := newQQGateWorld(t)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.bot.qq.com" || r.URL.Path != "/app/getAppAccessToken" || r.Method != "POST" {
			t.Errorf("unexpected external request %s %s %s", r.Host, r.Method, r.URL.Path)
			http.Error(w, "unexpected", 400)
			return
		}
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil || in["clientSecret"] != "synthetic-secret" {
			t.Error("token input missing")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"controlled-api-token","expires_in":7200}`)
	}))
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.bot.qq.com:443" {
			return nil, errors.New("external network prohibited")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	auth := identity.Service{Store: identity.PostgresStore{Pool: f.pool}, PasswordHasher: f.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, f.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: f.box, PublicURL: "https://openfox.example.test", QQClient: &qq.Client{Transport: tr}})))
	t.Cleanup(server.Close)
	login := func(name, user string) *client {
		return f.login(server.URL+"/api", "fs-"+name+"-"+f.org[:8]+"@example.test", user)
	}
	owner, member, guest := login("owner", f.ownerID), login("member", f.memberID), login("guest", f.guestID)
	other := login("ownerb", f.ownerBID)
	base := f.path("/integrations/qq")
	var configuration integrations.QQConfiguration
	owner.json(200, "GET", base+"/configuration", nil, &configuration)
	if !configuration.Configured || configuration.QRConfigured || configuration.Bindings == nil || configuration.BindingSessions == nil {
		t.Fatal("configuration defaults", configuration)
	}
	guest.json(403, "GET", base+"/configuration", nil, nil)
	agent := f.createAgent(f.owner, f.foxSpace, "HTTP QQ Agent", "SHARED")
	create := integrations.QQCreateBotInput{AgentID: agent, Name: "QQ API fixture", IdempotencyKey: f.newUUID()}
	var bot, duplicate integrations.QQBot
	owner.json(201, "POST", base+"/bots", create, &bot)
	owner.json(201, "POST", base+"/bots", create, &duplicate)
	if bot.ID == "" || duplicate.ID != bot.ID {
		t.Fatal("create not idempotent")
	}
	botPath := base + "/bots/" + bot.ID
	member.json(403, "GET", botPath, nil, nil)
	other.json(404, "GET", "/v1/organizations/"+f.orgB+"/integrations/qq/bots/"+bot.ID, nil, nil)
	request, _ := http.NewRequest("POST", server.URL+"/api"+botPath+"/disconnect", strings.NewReader(`{"version":1}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := owner.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("missing CSRF=%d", response.StatusCode)
	}
	owner.json(503, "POST", botPath+"/qr", integrations.QQQRStartInput{Version: bot.Version, IdempotencyKey: f.newUUID()}, nil)
	appID := "http-" + f.newUUID()
	owner.json(200, "POST", botPath+"/credentials", integrations.QQConnectInput{Version: bot.Version, AppID: appID, AppSecret: "synthetic-secret"}, &bot)
	if bot.State != "WAITING_WEBHOOK" {
		t.Fatalf("credentials state %s", bot.State)
	}
	var hook struct {
		URL string `json:"url"`
	}
	owner.json(200, "POST", botPath+"/webhook", map[string]uint64{"version": bot.Version}, &hook)
	parsed, err := url.Parse(hook.URL)
	if err != nil {
		t.Fatal(err)
	}
	send := func(body any, valid bool) (int, string) {
		raw, _ := json.Marshal(body)
		ts := "1794691234"
		seed := "synthetic-secret"
		for len(seed) < 32 {
			seed += seed
		}
		key := ed25519.NewKeyFromSeed([]byte(seed[:32]))
		sig := hex.EncodeToString(ed25519.Sign(key, append([]byte(ts), raw...)))
		if !valid {
			sig = strings.Repeat("0", 128)
		}
		req, _ := http.NewRequest("POST", server.URL+parsed.Path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Bot-Appid", appID)
		req.Header.Set("X-Signature-Timestamp", ts)
		req.Header.Set("X-Signature-Ed25519", sig)
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		payload, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("secret callback cache headers")
		}
		return resp.StatusCode, strings.TrimSpace(string(payload))
	}
	challenge := map[string]any{"op": 13, "d": map[string]any{"plain_token": "controlled-challenge", "event_ts": "1794691234"}}
	if status, _ := send(challenge, false); status != 401 {
		t.Fatalf("invalid signature=%d", status)
	}
	if status, body := send(challenge, true); status != 200 || !strings.Contains(body, `"plain_token":"controlled-challenge"`) {
		t.Fatalf("challenge=%d %s", status, body)
	}
	message := func(id, text string) map[string]any {
		return map[string]any{"op": 0, "id": "event-" + id, "t": "C2C_MESSAGE_CREATE", "d": map[string]any{"id": id, "message_type": 0, "content": text, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": map[string]any{"user_openid": "api-private-subject", "bot": false}}}
	}
	event := message("activate", "controlled activation")
	if status, body := send(event, true); status != 200 || body != `{"op":12,"d":0}` {
		t.Fatalf("committed event=%d %s", status, body)
	}
	if status, body := send(event, true); status != 200 || body != `{"op":12,"d":0}` {
		t.Fatalf("duplicate event=%d %s", status, body)
	}
	if n := f.count(`SELECT count(*) FROM openfox.qq_message_receipts WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND event_id='event-activate'`, f.org, bot.ID); n != 1 {
		t.Fatal("duplicate receipt", n)
	}
	var detail integrations.QQBotDetail
	owner.json(200, "GET", botPath, nil, &detail)
	bot = detail.Bot
	if bot.State != "ACTIVE" || !bot.URLVerified || detail.Groups == nil || detail.Candidates == nil || detail.QRSessions == nil || detail.Replies == nil {
		t.Fatal("activation detail invalid", detail)
	}
	var binding integrations.QQBindingSession
	owner.json(201, "POST", botPath+"/binding-sessions", integrations.QQStartBindingInput{Version: bot.Version, Purpose: "PRIVATE_CODE", IdempotencyKey: f.newUUID()}, &binding)
	if binding.Code == "" {
		t.Fatal("missing one-time code")
	}
	code, bindingID := binding.Code, binding.ID
	binding = integrations.QQBindingSession{}
	owner.json(200, "GET", base+"/binding-sessions/"+bindingID, nil, &binding)
	if binding.Code != "" {
		t.Fatal("GET revealed code")
	}
	if status, body := send(message("binding", "/openfox-bind "+code), true); status != 200 || body != `{"op":12,"d":0}` {
		t.Fatalf("binding event=%d %s", status, body)
	}
	owner.json(200, "GET", base+"/binding-sessions/"+binding.ID, nil, &binding)
	if binding.Status != "OBSERVED" {
		t.Fatalf("identity observation %s", binding.Status)
	}
	owner.json(200, "POST", base+"/binding-sessions/"+binding.ID+"/confirm", integrations.QQConfirmBindingInput{Version: binding.Version}, &binding)
	if binding.Status != "CONFIRMED" || binding.BindingID == "" {
		t.Fatal("identity confirmation", binding)
	}
	// Exercise an actual PostgreSQL timeout after raw signature verification.
	lockedConn, e := pgx.Connect(f.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer lockedConn.Close(context.Background())
	locked, e := lockedConn.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer locked.Rollback(context.Background())
	if _, e = locked.Exec(f.ctx, `LOCK TABLE openfox.qq_message_receipts IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	retryEvent := message("retry", "controlled retry input")
	if status, body := send(retryEvent, true); status != 200 || body != `{"op":12,"d":1}` {
		t.Fatalf("retry ACK=%d %s", status, body)
	}
	if e = locked.Rollback(f.ctx); e != nil {
		t.Fatal(e)
	}
	if n := f.count(`SELECT count(*) FROM openfox.qq_message_receipts WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND event_id='event-retry'`, f.org, bot.ID); n != 0 {
		t.Fatal("failed transaction persisted receipt")
	}
	if status, body := send(retryEvent, true); status != 200 || body != `{"op":12,"d":0}` {
		t.Fatalf("retry success=%d %s", status, body)
	}
	owner.json(200, "GET", botPath, nil, &detail)
	bot = detail.Bot
	owner.json(409, "POST", botPath+"/disconnect", map[string]uint64{"version": bot.Version + 1}, nil)
	owner.json(200, "POST", botPath+"/disconnect", map[string]uint64{"version": bot.Version}, &bot)
	if status, _ := send(message("after-disconnect", "must not receive"), true); status != 404 {
		t.Fatalf("old route remained valid %d", status)
	}
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	for _, body := range f.log.bodies {
		if strings.Contains(body, "synthetic-secret") || strings.Contains(body, "controlled-api-token") {
			t.Fatal("secret leaked in API response")
		}
	}
}
