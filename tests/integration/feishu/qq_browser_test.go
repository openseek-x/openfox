package feishuint

// Real browser HTTP/PG/worker harness. The fixed official SDK executes in its
// native Node process; the test-only Go overlay restricts it to this TLS peer.
import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/qq"
	"openfox/internal/tenancy"
)

type qqBrowserPeer struct {
	mu             sync.Mutex
	key            []byte
	scanned        bool
	creates, polls int
	appID          string
	posts          []map[string]any
}

func qqBrowserJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (p *qqBrowserPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if r.Host == "q.qq.com" {
		var in map[string]string
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			http.Error(w, "body", 400)
			return
		}
		switch r.URL.Path {
		case "/lite/create_bind_task":
			key, e := base64.StdEncoding.DecodeString(in["key"])
			if e != nil || len(key) != 32 {
				http.Error(w, "key", 400)
				return
			}
			p.key = key
			p.creates++
			qqBrowserJSON(w, map[string]any{"retcode": 0, "data": map[string]any{"task_id": "owned-browser-task"}})
		case "/lite/poll_bind_result":
			p.polls++
			if !p.scanned {
				qqBrowserJSON(w, map[string]any{"retcode": 0, "data": map[string]any{"status": 1}})
				return
			}
			block, e := aes.NewCipher(p.key)
			if e != nil {
				http.Error(w, "key", 400)
				return
			}
			gcm, _ := cipher.NewGCM(block)
			nonce := make([]byte, gcm.NonceSize())
			_, _ = rand.Read(nonce)
			encrypted := append(nonce, gcm.Seal(nil, nonce, []byte("owned-browser-secret"), nil)...)
			qqBrowserJSON(w, map[string]any{"retcode": 0, "data": map[string]any{"status": 2, "bot_appid": p.appID, "bot_encrypt_secret": base64.StdEncoding.EncodeToString(encrypted), "user_openid": "unused-sdk-subject"}})
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Host != "api.bot.qq.com" {
		http.Error(w, "origin", 400)
		return
	}
	switch r.URL.Path {
	case "/app/getAppAccessToken":
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil || in["appId"] != p.appID || in["clientSecret"] != "owned-browser-secret" {
			http.Error(w, "credential", 400)
			return
		}
		qqBrowserJSON(w, map[string]any{"access_token": "owned-browser-access", "expires_in": 7200})
	case "/v2/users/owned-private-subject/messages":
		if r.Header.Get("Authorization") != "QQBot owned-browser-access" {
			http.Error(w, "token", 401)
			return
		}
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "body", 400)
			return
		}
		p.posts = append(p.posts, in)
		qqBrowserJSON(w, map[string]any{"id": "owned-browser-reply", "timestamp": time.Now().UTC().Format(time.RFC3339)})
	default:
		http.NotFound(w, r)
	}
}

func TestQQBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I14_BROWSER_FILE")
	if file == "" {
		t.Skip("requires exclusive QQ browser harness")
	}
	dsn, e := url.Parse(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e != nil || !strings.HasPrefix(dsn.Path, "/openfox_i14_") {
		t.Fatal("exclusive I14 DB required")
	}
	web := os.Getenv("OPENFOX_I14_WEB_URL")
	u, e := url.Parse(web)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback web URL required")
	}
	nativeFile := os.Getenv("OPENFOX_I14_NATIVE_TLS")
	if nativeFile == "" {
		t.Fatal("controlled native overlay required")
	}
	b := &botWorld{world: setup(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	b.ctx = ctx
	p := &qqBrowserPeer{appID: "browser-" + b.org}
	peer := httptest.NewTLSServer(p)
	defer peer.Close()
	_, port, _ := net.SplitHostPort(peer.Listener.Addr().String())
	raw, _ := json.Marshal(map[string]string{"port": port, "ca": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}))})
	if e = os.WriteFile(nativeFile, raw, 0600); e != nil {
		t.Fatal(e)
	}
	defer os.Remove(nativeFile)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.bot.qq.com:443" {
			return nil, errors.New("external network prohibited")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, peer.Listener.Addr().String())
	}}
	defer tr.CloseIdleConnections()
	b.service = integrations.Service{Box: b.box, PublicURL: "https://openfox.example.test", QQ: &qq.Client{Transport: tr}, QQConnectorNodePath: os.Getenv("OPENFOX_TEST_QQ_CONNECTOR_NODE"), QQConnectorRoot: os.Getenv("OPENFOX_TEST_QQ_CONNECTOR_ROOT")}
	auth := identity.Service{Store: identity.PostgresStore{Pool: b.pool}, PasswordHasher: b.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, b.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: b.box, PublicURL: b.service.PublicURL, QQClient: b.service.QQ, QQConnectorNodePath: b.service.QQConnectorNodePath, QQConnectorRoot: b.service.QQConnectorRoot})))
	defer server.Close()
	b.server = server
	b.owner = b.login(server.URL+"/api", "fs-owner-"+b.org[:8]+"@example.test", b.ownerID)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'QQ controlled computer',clock_timestamp())`, b.deviceID, "i14-browser-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	agent := b.createAgent(b.owner, b.foxSpace, "QQ browser Agent", "SHARED")
	manager := integrations.NewQQSessionManager()
	defer func() {
		if e := manager.Close(); e != nil {
			t.Error(e)
		}
	}()
	worker := &integrations.IMWorker{Pool: b.pool, Service: b.service, Instance: "i14-browser-" + b.org, QQSessions: manager}
	send := func(bot string, value any) (int, []byte, error) {
		var current integrations.QQBotDetail
		code, body := b.owner.do("GET", b.path("/integrations/qq/bots/"+bot), nil)
		if code != 200 || json.Unmarshal([]byte(body), &current) != nil {
			return 0, nil, errors.New("bot detail")
		}
		var callback struct {
			URL string `json:"url"`
		}
		code, body = b.owner.do("POST", b.path("/integrations/qq/bots/"+bot+"/webhook"), map[string]uint64{"version": current.Bot.Version})
		if code != 200 || json.Unmarshal([]byte(body), &callback) != nil {
			return 0, nil, errors.New("callback unavailable")
		}
		u, e := url.Parse(callback.URL)
		if e != nil {
			return 0, nil, e
		}
		raw, _ := json.Marshal(value)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		seed := strings.Repeat("owned-browser-secret", 2)[:32]
		sig := ed25519.Sign(ed25519.NewKeyFromSeed([]byte(seed)), append([]byte(ts), raw...))
		req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+u.Path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Bot-Appid", p.appID)
		req.Header.Set("X-Signature-Timestamp", ts)
		req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
		resp, e := server.Client().Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer resp.Body.Close()
		bodyBytes, e := io.ReadAll(resp.Body)
		return resp.StatusCode, bodyBytes, e
	}
	actorTx := func(fn func(context.Context, pgx.Tx) error) error {
		return b.pool.RunForOrganization(ctx, b.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.ownerID, AuthorizationVersion: 1}, nil
		}), fn)
	}
	var controlMu sync.Mutex
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlMu.Lock()
		defer controlMu.Unlock()
		if r.Method != "POST" && r.URL.Path != "/proof" {
			http.Error(w, "method", 405)
			return
		}
		var in struct{ BotID, Text, ID, Scope string }
		if r.Method == "POST" && json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
			http.Error(w, "body", 400)
			return
		}
		switch r.URL.Path {
		case "/scan":
			p.mu.Lock()
			p.scanned = true
			p.mu.Unlock()
			qqBrowserJSON(w, map[string]bool{"ok": true})
		case "/pump":
			if e := worker.RunOrganization(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			qqBrowserJSON(w, map[string]bool{"ok": true})
		case "/challenge", "/event":
			var v any
			if r.URL.Path == "/challenge" {
				v = map[string]any{"op": 13, "d": map[string]string{"plain_token": "owned-challenge", "event_ts": strconv.FormatInt(time.Now().Unix(), 10)}}
			} else {
				data := map[string]any{"id": in.ID, "message_type": 0, "content": in.Text, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": map[string]any{"user_openid": "owned-private-subject", "bot": false}}
				kind := "C2C_MESSAGE_CREATE"
				if in.Scope == "GROUP" {
					kind = "GROUP_AT_MESSAGE_CREATE"
					data["group_openid"] = "owned-group"
					data["author"] = map[string]any{"member_openid": "owned-group-subject", "bot": false}
					data["member"] = map[string]any{"roles": []string{"2"}}
				}
				v = map[string]any{"op": 0, "id": "event-" + in.ID, "t": kind, "d": data}
			}
			code, body, e := send(in.BotID, v)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write(body)
		case "/finish-run":
			if e := worker.RunOrganization(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			store, _ := dispatch.NewStore(dispatch.DefaultConfig())
			var run, conv string
			e := actorTx(func(ctx context.Context, tx pgx.Tx) error {
				made, rejected, e := store.MaterializeInputs(ctx, tx, b.org, 50)
				if e != nil {
					return e
				}
				if made != 1 || rejected != 0 {
					return fmt.Errorf("materialize %d/%d", made, rejected)
				}
				if e = tx.QueryRow(ctx, `SELECT id::text,conversation_id::text FROM openfox.runs WHERE organization_id=$1::uuid AND actor_id=$2::uuid AND state='QUEUED'`, b.org, b.ownerID).Scan(&run, &conv); e != nil {
					return e
				}
				// Controlled execution completion only: no paid model is invoked. Actual
				// MaterializeInputs and FinalizeConversation preserve Run/source linkage.
				if _, e = tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',started_at=now(),finished_at=now() WHERE id=$1::uuid`, run); e != nil {
					return e
				}
				_, e = dispatch.FinalizeConversation(ctx, tx, execution.Run{ID: run, OrganizationID: b.org, ConversationID: conv, AgentID: agent, SpaceID: b.foxSpace, ActorID: b.ownerID, State: execution.StateSucceeded}, dispatch.AuthorAgent, "Controlled QQ browser terminal reply", false)
				return e
			})
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			if e = worker.RunOrganization(ctx, b.org); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			qqBrowserJSON(w, map[string]bool{"ok": true})
		case "/proof":
			var routes, inputs, done int
			b.scan(&routes, `SELECT count(*) FROM openfox.qq_routes WHERE organization_id=$1::uuid`, b.org)
			b.scan(&inputs, `SELECT count(*) FROM openfox.qq_inputs WHERE organization_id=$1::uuid AND source='REMOTE'`, b.org)
			b.scan(&done, `SELECT count(*) FROM openfox.qq_replies WHERE organization_id=$1::uuid AND state='DONE'`, b.org)
			p.mu.Lock()
			defer p.mu.Unlock()
			qqBrowserJSON(w, map[string]any{"creates": p.creates, "polls": p.polls, "posts": p.posts, "routes": routes, "inputs": inputs, "replies_done": done})
		default:
			http.NotFound(w, r)
		}
	}))
	defer control.Close()
	raw, _ = json.Marshal(map[string]string{"api_url": server.URL, "web_url": web, "fixture_url": control.URL, "organization_id": b.org, "space_id": b.foxSpace, "agent_id": agent, "email": "fs-owner-" + b.org[:8] + "@example.test", "password": password})
	if !filepath.IsAbs(file) {
		t.Fatal("absolute fixture path required")
	}
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser deadline")
		case <-ticker.C:
			if _, e := os.Stat(file + ".stop"); e == nil {
				return
			}
		}
	}
}
