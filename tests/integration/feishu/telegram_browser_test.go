package feishuint

// Explicitly requested local harness: real authenticated API, tenant PostgreSQL,
// Telegram protocol TLS, inbound worker and dispatch. Only the model result is
// supplied synthetically; no external Telegram account or model is contacted.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	"openfox/internal/integrations/telegram"
	"openfox/internal/tenancy"
)

const telegramBrowserToken = "12345:synthetic_browser_token"
const telegramBrowserRemoteBot int64 = 900001
const telegramBrowserPerson int64 = 987654
const telegramBrowserPrivateChat int64 = 777888
const telegramBrowserGroup int64 = -100200300

type telegramBrowserTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (v telegramBrowserTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" {
		return nil, errors.New("fixture rejected non-Telegram origin")
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Host = v.target.Host
	u.Scheme = v.target.Scheme
	c.URL = &u
	c.Host = v.target.Host
	return v.inner.RoundTrip(c)
}

type telegramBrowserPost struct {
	Chat  int64 `json:"chat_id"`
	Reply int64 `json:"reply_to_message_id"`
	Link  bool  `json:"has_openfox_link"`
}
type telegramBrowserPeer struct {
	mu            sync.Mutex
	hook, secret  string
	sets, deletes int
	posts         []telegramBrowserPost
	unknownNext   bool
}

func telegramBrowserJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (p *telegramBrowserPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/bot"+telegramBrowserToken+"/") {
		w.WriteHeader(401)
		return
	}
	var in struct {
		URL    string `json:"url"`
		Secret string `json:"secret_token"`
		Chat   int64  `json:"chat_id"`
		Text   string `json:"text"`
		Drop   bool   `json:"drop_pending_updates"`
		Reply  struct {
			ID int64 `json:"message_id"`
		} `json:"reply_parameters"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		w.WriteHeader(400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out any
	switch strings.TrimPrefix(r.URL.Path, "/bot"+telegramBrowserToken+"/") {
	case "getMe":
		out = map[string]any{"id": telegramBrowserRemoteBot, "is_bot": true, "first_name": "Controlled Telegram Bot", "username": "Current_Bot"}
	case "getWebhookInfo":
		out = map[string]any{"url": p.hook, "has_custom_certificate": false, "pending_update_count": 0}
	case "setWebhook":
		if in.Drop {
			w.WriteHeader(400)
			return
		}
		p.sets++
		p.hook, p.secret = in.URL, in.Secret
		out = true
	case "deleteWebhook":
		if in.Drop {
			w.WriteHeader(400)
			return
		}
		p.deletes++
		p.hook = ""
		out = true
	case "getChat":
		out = map[string]any{"id": in.Chat, "type": "supergroup", "title": "Controlled Telegram Group"}
	case "getChatMember":
		out = map[string]any{"status": "member", "user": map[string]any{"id": telegramBrowserRemoteBot, "is_bot": true, "first_name": "Controlled Telegram Bot"}}
	case "sendMessage":
		p.posts = append(p.posts, telegramBrowserPost{in.Chat, in.Reply.ID, strings.Contains(in.Text, "https://openfox.example.test/o/")})
		if p.unknownNext {
			p.unknownNext = false
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				_ = c.Close()
			}
			return
		}
		kind := "private"
		if in.Chat < 0 {
			kind = "supergroup"
		}
		chat := map[string]any{"id": in.Chat, "type": kind}
		out = map[string]any{"message_id": 8000 + len(p.posts), "date": time.Now().Unix(), "chat": chat, "from": map[string]any{"id": telegramBrowserRemoteBot, "is_bot": true, "first_name": "Controlled Telegram Bot"}, "text": in.Text, "reply_to_message": map[string]any{"message_id": in.Reply.ID, "date": time.Now().Unix(), "chat": chat, "from": map[string]any{"id": telegramBrowserPerson, "is_bot": false, "first_name": "Controlled Person"}, "text": "controlled source"}}
	default:
		w.WriteHeader(400)
		return
	}
	telegramBrowserJSON(w, map[string]any{"ok": true, "result": out})
}
func telegramBrowserTx(b *botWorld, ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	selectOrg := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.ownerID, AuthorizationVersion: 1}, nil
	})
	return b.pool.RunForOrganization(ctx, b.org, tenancy.SourceUserRequest, selectOrg, fn)
}
func TestTelegramBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I18_BROWSER_FILE")
	if file == "" {
		t.Skip("owned browser harness not requested")
	}
	dsn, e := url.Parse(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e != nil || !strings.HasPrefix(strings.TrimPrefix(dsn.Path, "/"), "i18_") {
		t.Fatal("exclusive i18_ database required")
	}
	web := os.Getenv("OPENFOX_I18_WEB_URL")
	u, e := url.Parse(web)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback frontend required")
	}
	b := &botWorld{world: setup(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	b.ctx = ctx
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	peer := &telegramBrowserPeer{posts: []telegramBrowserPost{}}
	tls := httptest.NewTLSServer(peer)
	defer tls.Close()
	target, _ := url.Parse(tls.URL)
	tg := telegram.NewClient(telegramBrowserTransport{target, tls.Client().Transport})
	b.service = integrations.Service{Box: b.box, PublicURL: "https://openfox.example.test", Telegram: tg}
	auth := identity.Service{Store: identity.PostgresStore{Pool: b.pool}, PasswordHasher: b.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, b.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: b.box, PublicURL: b.service.PublicURL, TelegramClient: tg})))
	defer server.Close()
	b.server = server
	b.owner = b.login(server.URL+"/api", "fs-owner-"+b.org[:8]+"@example.test", b.ownerID)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at,capabilities) VALUES($1::uuid,$2,4,$3::uuid,'Telegram controlled computer',clock_timestamp(),'{"runtimes":{"codex":{"version":"controlled","login_status":"LOGGED_IN","runnable":true}}}'::jsonb)`, b.deviceID, "i18-browser-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	agentID := b.createAgent(b.owner, b.foxSpace, "Telegram browser Agent", "SHARED")
	worker := &integrations.IMWorker{Pool: b.pool, Service: b.service, Instance: "i18-browser-" + b.org}
	var eventMu sync.Mutex
	var updateID int64 = 100
	event := func(kind, code string) (int, map[string]any, error) {
		eventMu.Lock()
		defer eventMu.Unlock()
		updateID++
		peer.mu.Lock()
		hook, secret := peer.hook, peer.secret
		peer.mu.Unlock()
		target, e := url.Parse(hook)
		if e != nil || target.Host != "openfox.example.test" {
			return 0, nil, errors.New("webhook not configured")
		}
		chatID := telegramBrowserPrivateChat
		chatType := "private"
		text := "controlled private request"
		var entities []map[string]any
		switch kind {
		case "start":
			text = "/start bind_" + code
			entities = []map[string]any{{"type": "bot_command", "offset": 0, "length": 6}}
		case "group":
			chatID = telegramBrowserGroup
			chatType = "supergroup"
			text = "/openfox_connect@Current_Bot"
			entities = []map[string]any{{"type": "bot_command", "offset": 0, "length": len(text)}}
		case "mention", "topic":
			chatID = telegramBrowserGroup
			chatType = "supergroup"
			text = "@Current_Bot controlled group request"
			entities = []map[string]any{{"type": "mention", "offset": 0, "length": 12}}
		case "background":
			chatID = telegramBrowserGroup
			chatType = "supergroup"
			text = "controlled ordinary group context"
		case "private":
		default:
			return 0, nil, errors.New("invalid event kind")
		}
		msg := map[string]any{"message_id": updateID, "date": time.Now().Unix(), "from": map[string]any{"id": telegramBrowserPerson, "is_bot": false, "first_name": "Controlled Person"}, "chat": map[string]any{"id": chatID, "type": chatType, "title": "Controlled Telegram Group"}, "text": text}
		if entities != nil {
			msg["entities"] = entities
		}
		if kind == "topic" {
			msg["message_thread_id"] = 7
			msg["is_topic_message"] = true
		}
		raw, _ := json.Marshal(map[string]any{"update_id": updateID, "message": msg})
		request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+target.Path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		response, e := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		var receipt map[string]any
		e = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&receipt)
		return response.StatusCode, receipt, e
	}
	finish := func() error {
		return telegramBrowserTx(b, ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, _, e := (dispatch.Store{}).MaterializeInputs(ctx, tx, b.org, 10)
			if e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `SELECT DISTINCT r.id::text,r.space_id::text,r.conversation_id::text,r.agent_id::text,r.actor_id::text FROM openfox.runs r JOIN openfox.conversation_inputs ci ON ci.organization_id=r.organization_id AND ci.run_id=r.id JOIN openfox.telegram_inputs ti ON ti.organization_id=ci.organization_id AND ti.message_id=ci.message_id WHERE r.organization_id=openfox_private.current_organization_id() AND r.state='QUEUED' ORDER BY 1 LIMIT 10`)
			if e != nil {
				return e
			}
			var runs []execution.Run
			for rows.Next() {
				var run execution.Run
				if e = rows.Scan(&run.ID, &run.SpaceID, &run.ConversationID, &run.AgentID, &run.ActorID); e != nil {
					rows.Close()
					return e
				}
				run.OrganizationID = b.org
				run.State = execution.StateSucceeded
				runs = append(runs, run)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, run := range runs {
				if _, e = tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, run.ID); e != nil {
					return e
				}
				if _, e = dispatch.FinalizeConversation(ctx, tx, run, dispatch.AuthorAgent, "Controlled browser result; no model was called.", false); e != nil {
					return e
				}
			}
			return nil
		})
	}
	state := func() map[string]any {
		values := map[string]any{}
		for key, query := range map[string]string{"identities": "SELECT count(*) FROM openfox.telegram_identities WHERE organization_id=$1::uuid AND active", "groups": "SELECT count(*) FROM openfox.telegram_group_epochs WHERE organization_id=$1::uuid AND enabled", "inputs": "SELECT count(*) FROM openfox.telegram_inputs WHERE organization_id=$1::uuid", "background": "SELECT count(*) FROM openfox.telegram_background_messages WHERE organization_id=$1::uuid", "replies_done": "SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND status='DONE'", "replies_unknown": "SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND last_error='delivery_unknown'"} {
			var count int
			b.scan(&count, query, b.org)
			values[key] = count
		}
		peer.mu.Lock()
		values["set_calls"], values["delete_calls"], values["send_calls"] = peer.sets, peer.deletes, len(peer.posts)
		values["posts"] = append([]telegramBrowserPost{}, peer.posts...)
		peer.mu.Unlock()
		return values
	}
	var controlMu sync.Mutex
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlMu.Lock()
		defer controlMu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/state" {
			telegramBrowserJSON(w, state())
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Code    string `json:"code"`
			Kind    string `json:"kind"`
			Unknown bool   `json:"unknown"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		var err error
		switch r.URL.Path {
		case "/pump":
			err = worker.RunOrganization(ctx, b.org)
		case "/complete":
			err = worker.RunOrganization(ctx, b.org)
			if err == nil {
				err = finish()
			}
			if err == nil {
				err = worker.RunOrganization(ctx, b.org)
			}
		case "/event":
			status, out, e := event(in.Kind, in.Code)
			if e != nil {
				http.Error(w, "controlled event failed", 500)
				return
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(out)
			return
		case "/unknown-next":
			peer.mu.Lock()
			peer.unknownNext = in.Unknown
			peer.mu.Unlock()
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			t.Logf("controlled step failed: %T", err)
			http.Error(w, "controlled step failed", 500)
			return
		}
		telegramBrowserJSON(w, map[string]bool{"ok": true})
	}))
	defer control.Close()
	raw, _ := json.Marshal(map[string]string{"api_url": server.URL, "web_url": web, "fixture_url": control.URL, "organization_id": b.org, "space_id": b.foxSpace, "agent_id": agentID, "device_id": b.deviceID, "grant_id": b.grantID, "email": "fs-owner-" + b.org[:8] + "@example.test", "password": password, "token": telegramBrowserToken})
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness deadline")
		case <-ticker.C:
			if _, e = os.Stat(file + ".stop"); e == nil {
				controlMu.Lock()
				proof := state()
				controlMu.Unlock()
				proofRaw, _ := json.MarshalIndent(proof, "", "  ")
				if e = os.WriteFile(file+".proof.json", proofRaw, 0600); e != nil {
					t.Fatal(e)
				}
				return
			}
		}
	}
}
