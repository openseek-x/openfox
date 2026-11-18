package feishuint

// This harness runs actual HTTP/session/CSRF, PG, Gateway, inbound and sender
// code. Only Discord's fixed HTTPS/WSS origins are redirected to a local TLS peer.
import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/api"
	"openfox/internal/conversations"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/discord"
	"openfox/internal/tenancy"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type discordBrowserPeer struct {
	mu       sync.Mutex
	bot      string
	events   chan map[string]any
	messages map[string]map[string]any
	posts    []map[string]any
	unknown  bool
}

func (p *discordBrowserPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		p.gateway(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/messages") {
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			w.WriteHeader(400)
			return
		}
		p.mu.Lock()
		p.posts = append(p.posts, v)
		unknown := p.unknown
		p.unknown = false
		n := len(p.posts)
		p.mu.Unlock()
		if unknown {
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				_ = c.Close()
			}
			return
		}
		send(map[string]any{"id": fmt.Sprint(9900000 + n), "channel_id": "7000002", "guild_id": "5000001", "author": map[string]any{"id": p.bot, "username": "Controlled Discord Bot", "bot": true}, "content": v["content"], "nonce": v["nonce"], "message_reference": v["message_reference"], "type": 19, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v10")
	switch path {
	case "/users/@me":
		send(map[string]any{"id": p.bot, "username": "Controlled Discord Bot", "bot": true})
	case "/applications/@me":
		send(map[string]any{"id": "18446744073709551615", "name": "Controlled Discord App", "bot": map[string]any{"id": p.bot, "username": "Controlled Discord Bot", "bot": true}, "bot_public": true, "bot_require_code_grant": false, "flags": 1 << 18})
	case "/gateway/bot":
		send(map[string]any{"url": "wss://gateway.discord.gg", "shards": 1, "session_start_limit": map[string]int{"total": 1000, "remaining": 999, "reset_after": 60000, "max_concurrency": 1}})
	case "/users/@me/guilds":
		send([]any{map[string]any{"id": "5000001", "name": "Controlled server", "owner": true, "permissions": "68624"}})
	case "/guilds/5000001/channels":
		send([]any{map[string]any{"id": "7000002", "guild_id": "5000001", "type": 0, "name": "controlled-channel"}})
	case "/channels/7000002":
		send(map[string]any{"id": "7000002", "guild_id": "5000001", "type": 0, "name": "controlled-channel"})
	case "/channels/7000001":
		send(map[string]any{"id": "7000001", "type": 1, "recipients": []any{map[string]any{"id": "6000001", "username": "Controlled Person"}}})
	default:
		if strings.Contains(path, "/permissions/member/") {
			send(map[string]any{"permissions": "68624"})
			return
		}
		if strings.HasPrefix(path, "/guilds/5000001/members/") {
			send(map[string]any{"user": map[string]any{"id": path[strings.LastIndex(path, "/")+1:], "username": "Controlled member"}, "roles": []string{}, "pending": false, "communication_disabled_until": nil})
			return
		}
		if strings.HasSuffix(path, "/messages") {
			send([]any{})
			return
		}
		if strings.Contains(path, "/messages/") {
			p.mu.Lock()
			m := p.messages[path[strings.LastIndex(path, "/")+1:]]
			p.mu.Unlock()
			if m != nil {
				send(m)
				return
			}
		}
		w.WriteHeader(404)
		send(map[string]any{"message": "Not found", "code": 10008})
	}
}
func (p *discordBrowserPeer) gateway(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, nil)
	if e != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	write := func(v any) error { raw, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, raw) }
	if write(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 2000}}) != nil {
		return
	}
	var seq int64 = 1
	for {
		_, raw, e := c.Read(ctx)
		if e != nil {
			return
		}
		var v struct {
			Op int `json:"op"`
		}
		_ = json.Unmarshal(raw, &v)
		if v.Op == 2 || v.Op == 6 {
			break
		}
		if v.Op == 1 {
			_ = write(map[string]any{"op": 11, "d": nil})
		}
	}
	if write(map[string]any{"op": 0, "s": seq, "t": "READY", "d": map[string]any{"v": 10, "user": map[string]any{"id": p.bot, "username": "Controlled Discord Bot", "bot": true}, "application": map[string]any{"id": "18446744073709551615"}, "session_id": "controlled-browser-session", "resume_gateway_url": "wss://gateway.discord.gg"}}) != nil {
		return
	}
	reads := make(chan int, 1)
	go func() {
		defer close(reads)
		for {
			_, raw, e := c.Read(ctx)
			if e != nil {
				return
			}
			var v struct {
				Op int `json:"op"`
			}
			if json.Unmarshal(raw, &v) == nil {
				select {
				case reads <- v.Op:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case op, ok := <-reads:
			if !ok {
				return
			}
			if op == 1 {
				if write(map[string]any{"op": 11, "d": nil}) != nil {
					return
				}
			}
		case d := <-p.events:
			seq++
			if write(map[string]any{"op": 0, "s": seq, "t": "MESSAGE_CREATE", "d": d}) != nil {
				return
			}
		}
	}
}
func newDiscordHTTPWorld(t *testing.T) (*botWorld, *discordBrowserPeer, *integrations.LongConnManager, *integrations.IMWorker) {
	b := &botWorld{world: setup(t)}
	p := &discordBrowserPeer{bot: fmt.Sprint(time.Now().UnixNano()), events: make(chan map[string]any, 8), messages: map[string]map[string]any{}}
	remote := httptest.NewTLSServer(p)
	t.Cleanup(remote.Close)
	roots := x509.NewCertPool()
	roots.AddCert(remote.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "discord.com:443" && address != "gateway.discord.gg:443" {
			return nil, errors.New("external network forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	dc := discord.NewClient(tr)
	b.service = integrations.Service{Box: b.box, Discord: dc, PublicURL: "http://127.0.0.1:4187"}
	auth := identity.Service{Store: identity.PostgresStore{Pool: b.pool}, PasswordHasher: b.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, b.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: b.box, PublicURL: b.service.PublicURL, DiscordClient: dc})))
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	b.server = server
	b.owner = b.login(server.URL+"/api", "fs-owner-"+b.org[:8]+"@example.test", b.ownerID)
	b.member = b.login(server.URL+"/api", "fs-member-"+b.org[:8]+"@example.test", b.memberID)
	b.guest = b.login(server.URL+"/api", "fs-guest-"+b.org[:8]+"@example.test", b.guestID)
	b.ownerB = b.login(server.URL+"/api", "fs-ownerb-"+b.org[:8]+"@example.test", b.ownerBID)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'Discord controlled computer',clock_timestamp())`, b.deviceID, "i17-browser-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	mgr := &integrations.LongConnManager{Pool: b.pool, Service: b.service, Instance: "i17-browser-" + b.org, ScanInterval: 200 * time.Millisecond, RenewInterval: time.Second}
	worker := &integrations.IMWorker{Pool: b.pool, Service: b.service, Instance: "i17-browser-worker"}
	return b, p, mgr, worker
}
func (p *discordBrowserPeer) emit(text string, dm bool) string {
	id := fmt.Sprint(time.Now().UnixNano())
	channel := "7000002"
	if dm {
		channel = "7000001"
	}
	m := map[string]any{"id": id, "channel_id": channel, "author": map[string]any{"id": "6000001", "username": "Controlled Person"}, "content": text, "type": 0, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "mentions": []any{}}
	if !dm {
		m["guild_id"] = "5000001"
		m["mentions"] = []any{map[string]any{"id": p.bot, "username": "Controlled Discord Bot", "bot": true}}
	}
	p.mu.Lock()
	p.messages[id] = m
	p.mu.Unlock()
	p.events <- m
	return id
}
func TestDiscordBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I17_BROWSER_FILE")
	if file == "" {
		t.Skip("owned browser harness not requested")
	}
	dsn, e := url.Parse(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e != nil || !strings.HasPrefix(dsn.Path, "/openfox_i17_") {
		t.Fatal("exclusive I17 database required")
	}
	b, p, mgr, worker := newDiscordHTTPWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	b.ctx = ctx
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	defer func() { cancel(); <-done }()
	agent := b.createAgent(b.owner, b.foxSpace, "Discord browser Agent", "SHARED")
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" && r.URL.Path != "/state" {
			http.Error(w, "method", 405)
			return
		}
		var in struct {
			Text    string `json:"text"`
			DM      bool   `json:"dm"`
			Unknown bool   `json:"unknown"`
		}
		if r.Method == "POST" && json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
			http.Error(w, "body", 400)
			return
		}
		send := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/event":
			id := p.emit(in.Text, in.DM)
			send(map[string]string{"id": id})
		case "/pump":
			if e := worker.ProcessDiscordInbound(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			if e := worker.ProcessDiscordReplies(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			send(map[string]bool{"ok": true})
		case "/reject":
			p.mu.Lock()
			p.unknown = in.Unknown
			p.mu.Unlock()
			// Controlled dispatch rejection, using the same explicit-input helper as
			// rejectInputs. This fixture never fabricates a model or terminal Run.
			e := b.pool.RunForOrganization(ctx, b.org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
				return tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.ownerID, AuthorizationVersion: 1}, nil
			}), func(ctx context.Context, tx pgx.Tx) error {
				var ci, conv string
				if e := tx.QueryRow(ctx, `UPDATE openfox.conversation_inputs SET status='REJECTED',assigned_at=clock_timestamp() WHERE id=(SELECT id FROM openfox.conversation_inputs WHERE organization_id=openfox_private.current_organization_id() AND status='PENDING' ORDER BY created_at LIMIT 1) RETURNING id::text,conversation_id::text`).Scan(&ci, &conv); e != nil {
					return e
				}
				var seq int
				if e := tx.QueryRow(ctx, `UPDATE openfox.conversations SET next_message_seq=next_message_seq+1 WHERE id=$1::uuid RETURNING next_message_seq-1`, conv).Scan(&seq); e != nil {
					return e
				}
				message := b.newUUID()
				if _, e := tx.Exec(ctx, `INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'SYSTEM','Controlled dispatch rejection')`, message, conv, seq); e != nil {
					return e
				}
				return conversations.RecordDiscordRejectedInputs(ctx, tx, conv, message, []string{ci})
			})
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			if e := worker.ProcessDiscordReplies(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			send(map[string]bool{"ok": true})
		case "/state":
			var receipts, inputs, identities, channels, doneCount, unknown int
			b.scan(&receipts, `SELECT count(*) FROM openfox.discord_message_receipts WHERE organization_id=$1::uuid`, b.org)
			b.scan(&inputs, `SELECT count(*) FROM openfox.discord_inputs WHERE organization_id=$1::uuid AND source_kind='REMOTE'`, b.org)
			b.scan(&identities, `SELECT count(*) FROM openfox.discord_identities WHERE organization_id=$1::uuid AND active`, b.org)
			b.scan(&channels, `SELECT count(*) FROM openfox.discord_channel_epochs WHERE organization_id=$1::uuid AND enabled`, b.org)
			b.scan(&doneCount, `SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND status='DONE'`, b.org)
			b.scan(&unknown, `SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND last_error='delivery_unknown'`, b.org)
			p.mu.Lock()
			defer p.mu.Unlock()
			send(map[string]any{"receipts": receipts, "inputs": inputs, "identities": identities, "channels": channels, "delivered": doneCount, "unknown": unknown, "posts": p.posts})
		default:
			http.NotFound(w, r)
		}
	}))
	defer control.Close()
	raw, _ := json.Marshal(map[string]string{"api_url": b.server.URL, "web_url": "http://127.0.0.1:4187", "fixture_url": control.URL, "organization_id": b.org, "agent_id": agent, "space_id": b.foxSpace, "email": "fs-owner-" + b.org[:8] + "@example.test", "password": password, "remote_bot_id": p.bot})
	if e := os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness deadline")
		case <-tick.C:
			if _, e := os.Stat(file + ".stop"); e == nil {
				return
			}
		}
	}
}
