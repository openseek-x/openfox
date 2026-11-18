package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type tgFixture struct {
	t                                    *testing.T
	dsn, org, user, space, device, grant string
	actor                                collaboration.Actor
	service                              Service
	remote                               int64
	mu                                   sync.Mutex
	hook, secret                         string
	setCalls, deleteCalls                int
	loseSet                              bool
	sendCalls                            int
	loseSend                             bool
	rateSend                             int
	sendErrorStatus                      int
	sentChat, sentReply                  int64
	sentText                             string
	sendEntered                          chan struct{}
	sendRelease                          chan struct{}
}
type tgTestTransport struct {
	url   *url.URL
	inner http.RoundTripper
}

func (t tgTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" {
		return nil, errors.New("fixture rejects origin")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.url.Scheme
	u.Host = t.url.Host
	copy.URL = &u
	return t.inner.RoundTrip(copy)
}
func newTGFixture(t *testing.T, db bool) *tgFixture {
	t.Helper()
	f := &tgFixture{t: t, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t), device: refsTestID(t)}
	hash := sha256.Sum256([]byte(f.org))
	for _, v := range hash[:6] {
		f.remote = f.remote*256 + int64(v)
	}
	f.remote++
	box, err := secrets.New(bytes.Repeat([]byte{38}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/bot12345:synthetic_token/") {
			t.Error("unexpected fixture request")
			w.WriteHeader(400)
			return
		}
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid json")
		}
		method := strings.TrimPrefix(r.URL.Path, "/bot12345:synthetic_token/")
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any
		switch method {
		case "getMe":
			result = map[string]any{"id": f.remote, "is_bot": true, "first_name": "Fixture", "username": "Current_Bot"}
		case "getWebhookInfo":
			result = map[string]any{"url": f.hook, "has_custom_certificate": false, "pending_update_count": 0}
		case "setWebhook":
			f.setCalls++
			f.hook, _ = input["url"].(string)
			f.secret, _ = input["secret_token"].(string)
			if input["drop_pending_updates"] != false {
				t.Error("must preserve pending")
			}
			if f.loseSet {
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
				return
			}
			result = true
		case "deleteWebhook":
			f.deleteCalls++
			f.hook = ""
			result = true
		case "getChat":
			result = map[string]any{"id": input["chat_id"], "type": "supergroup", "title": "Fixture group"}
		case "getChatMember":
			result = map[string]any{"status": "member", "user": map[string]any{"id": f.remote, "is_bot": true, "first_name": "Fixture"}}
		case "sendMessage":
			f.sendCalls++
			f.sentChat = int64(input["chat_id"].(float64))
			f.sentReply = int64(input["reply_parameters"].(map[string]any)["message_id"].(float64))
			f.sentText = input["text"].(string)
			if f.sendEntered != nil {
				entered, release := f.sendEntered, f.sendRelease
				f.sendEntered = nil
				f.mu.Unlock()
				close(entered)
				<-release
				f.mu.Lock()
			}
			if _, exists := input["parse_mode"]; exists {
				t.Error("plain text must not set parse_mode")
			}
			if f.sendErrorStatus != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.sendErrorStatus)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": f.sendErrorStatus})
				return
			}
			if f.loseSend {
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
				return
			}
			if f.rateSend > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 429, "parameters": map[string]any{"retry_after": f.rateSend}})
				return
			}
			kind := "private"
			if f.sentChat < 0 {
				kind = "supergroup"
			}
			chat := map[string]any{"id": f.sentChat, "type": kind}
			result = map[string]any{"message_id": 8000 + f.sendCalls, "date": time.Now().Unix(), "chat": chat, "from": map[string]any{"id": f.remote, "is_bot": true, "first_name": "Fixture"}, "text": f.sentText, "reply_to_message": map[string]any{"message_id": f.sentReply, "date": time.Now().Unix(), "chat": chat, "from": map[string]any{"id": 987654, "is_bot": false, "first_name": "Person"}, "text": "source"}}
		default:
			t.Errorf("unexpected method %s", method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test", Telegram: telegram.NewClient(tgTestTransport{u, server.Client().Transport})}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	if !db {
		return f
	}
	f.dsn = os.Getenv("OPENFOX_I18_TEST_DATABASE_URL")
	if f.dsn == "" {
		t.Skip("requires exclusive I18 PostgreSQL database")
	}
	c, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var name string
	if err = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || !strings.HasPrefix(name, "i18_") {
		t.Fatal("requires isolated i18_ database")
	}
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, []any{f.user, f.user + "@example.test"}},
		{`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Telegram fixture')`, []any{f.org, f.org}},
		{`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, []any{f.org}},
		{`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, []any{f.org, f.user}},
		{`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES($1::uuid,$2::uuid,'TGTEST','Telegram','PRIVATE')`, []any{f.org, f.space}},
		{`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, []any{f.org, f.space, f.user}},
		{`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'Telegram fixture',clock_timestamp())`, []any{f.device, "telegram-" + f.org, f.user}},
		{`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, []any{f.org, f.device}},
	}
	for _, q := range seed {
		if _, err = c.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.QueryRow(context.Background(), `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, f.org, f.device).Scan(&f.grant); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *tgFixture) run(fn func(context.Context, pgx.Tx) error) error {
	tenant, err := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: f.user, AuthorizationVersion: f.actor.AuthorizationVersion}, tenancy.SourceUserRequest)
	if err != nil {
		return err
	}
	ctx := tenancy.WithTenantContext(context.Background(), tenant)
	c, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,$3,'user_request')`, f.org, f.user, f.actor.AuthorizationVersion); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (f *tgFixture) must(fn func(context.Context, pgx.Tx) error) {
	f.t.Helper()
	if err := f.run(fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *tgFixture) input() TelegramConnectInput {
	return TelegramConnectInput{OperationKey: refsTestID(f.t), Kind: BotKindTaskAssistant, SpaceID: f.space, Token: "12345:synthetic_token", Runtime: &FeishuAssistantRuntime{ComputerID: f.device, GrantID: f.grant, Runtime: "codex", Model: "default"}}
}
func (f *tgFixture) connect() TelegramBot {
	f.t.Helper()
	var plan TelegramConnectTarget
	var p TelegramPreparation
	var b TelegramBot
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, f.input())
		if e != nil {
			return fmt.Errorf("prepare: %w", e)
		}
		return nil
	})
	proof, err := f.service.VerifyTelegramConnection(context.Background(), plan)
	if err != nil {
		f.t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, plan, proof)
		if e != nil {
			return fmt.Errorf("save: %w", e)
		}
		return nil
	})
	f.mu.Lock()
	sets := f.setCalls
	f.mu.Unlock()
	if sets != 0 {
		f.t.Fatal("prepare performed a vendor write")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ConfirmTelegramPreparation(ctx, tx, f.org, f.actor, p.ID, TelegramConfirmInput{Version: p.Version, RemoteBotID: p.RemoteBotID.String()})
		if e != nil {
			return fmt.Errorf("confirm: %w", e)
		}
		return nil
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		again, e := f.service.ConfirmTelegramPreparation(ctx, tx, f.org, f.actor, p.ID, TelegramConfirmInput{Version: p.Version, RemoteBotID: p.RemoteBotID.String()})
		if e == nil && again.ID != b.ID {
			f.t.Fatal("confirm replay changed bot")
		}
		return e
	})
	return b
}
func (f *tgFixture) control(b TelegramBot) TelegramBot {
	f.t.Helper()
	var claim *TelegramControlClaim
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		claim, e = f.service.ClaimTelegramControl(ctx, tx, f.org, b.ID)
		return e
	})
	if claim == nil {
		f.t.Fatal("no control claim")
	}
	result := f.service.RunTelegramControl(context.Background(), *claim)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.FinishTelegramControl(ctx, tx, f.org, *claim, result)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	return b
}
func (f *tgFixture) receive(b TelegramBot, id, chat, msg, user int64, text string, entities []any) (TelegramReceiveResult, error) {
	f.t.Helper()
	var coordinates telegramWebhookCoordinates
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadTelegramControl(ctx, tx, b.ID, false)
		if e != nil {
			return e
		}
		coordinates, e = f.service.telegramCoordinates(f.org, r)
		return e
	})
	typ := "private"
	if chat < 0 {
		typ = "supergroup"
	}
	m := map[string]any{"message_id": msg, "date": time.Now().Unix(), "chat": map[string]any{"id": chat, "type": typ, "title": "Fixture group"}, "from": map[string]any{"id": user, "is_bot": false, "first_name": "Person"}, "text": text}
	if entities != nil {
		m["entities"] = entities
	}
	raw, _ := json.Marshal(map[string]any{"update_id": id, "message": m})
	var out TelegramReceiveResult
	err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ReceiveTelegram(ctx, tx, TelegramWebhookLookup{f.org, b.ID}, coordinates.Route, http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{coordinates.Secret}}, raw)
		return e
	})
	return out, err
}
func (f *tgFixture) bind(b TelegramBot, updateID int64) (TelegramIdentity, string) {
	f.t.Helper()
	var session TelegramBindingSession
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.IssueTelegramBinding(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	u, _ := url.Parse(session.StartURL)
	text := "/start " + u.Query().Get("start")
	out, err := f.receive(b, updateID, 987654, updateID, 987654, text, []any{map[string]any{"type": "bot_command", "offset": 0, "length": 6}})
	if err != nil || out.Result != "binding_candidate" {
		f.t.Fatalf("consume: %+v %v", out, err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.GetTelegramBindingSession(ctx, tx, f.org, f.actor, session.ID)
		return e
	})
	var identity TelegramIdentity
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		identity, e = f.service.ConfirmTelegramBinding(ctx, tx, f.org, f.actor, session.ID, session.Version)
		return e
	})
	return identity, session.ID
}
func TestTelegramServiceVerifyAndUnknownHTTP(t *testing.T) {
	f := newTGFixture(t, false)
	plan := TelegramConnectTarget{org: f.org, actor: f.user, input: TelegramConnectInput{Token: "12345:synthetic_token"}, requestHash: TokenHash("proof")}
	proof, err := f.service.VerifyTelegramConnection(context.Background(), plan)
	if err != nil || proof.bot.ID != telegram.ID(f.remote) {
		t.Fatalf("verify %v", err)
	}
	f.mu.Lock()
	f.hook = "https://other.example/hook"
	f.mu.Unlock()
	if _, err = f.service.VerifyTelegramConnection(context.Background(), plan); !errors.Is(err, ErrTelegramWebhookConflict) {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hook = ""
	f.loseSet = true
	f.mu.Unlock()
	claim := TelegramControlClaim{claim: "local", action: "SET", token: plan.input.Token, coordinates: telegramWebhookCoordinates{URL: "https://openfox.example.test/api/v1/hooks/telegram/abc", Secret: "synthetic"}}
	result := f.service.RunTelegramControl(context.Background(), claim)
	if !errors.Is(result.failure, telegram.ErrUnknown) {
		t.Fatal(result.failure)
	}
	claim.readOnly = true
	result = f.service.RunTelegramControl(context.Background(), claim)
	if !errors.Is(result.failure, telegram.ErrUnknown) {
		t.Fatal("URL equality incorrectly proved secret")
	}
	f.mu.Lock()
	sets := f.setCalls
	f.mu.Unlock()
	if sets != 1 {
		t.Fatalf("non-idempotent write retried %d", sets)
	}
}
func TestTelegramControlIdentityAndGroupsPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	if b.State != "ACTIVE" {
		t.Fatal(b.State)
	}
	identity, session := f.bind(b, 1)
	out, err := f.receive(b, 2, 987654, 2, 987654, "hello", nil)
	if err != nil || out.Result != "queued" {
		t.Fatalf("DM %+v %v", out, err)
	}
	cmd := "/openfox_connect@Current_Bot"
	out, err = f.receive(b, 3, -1001234567890, 3, 987654, cmd, []any{map[string]any{"type": "bot_command", "offset": 0, "length": len(cmd)}})
	if err != nil || out.Result != "group_discovered" {
		t.Fatalf("group %+v %v", out, err)
	}
	var discoveries []TelegramGroupDiscovery
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		discoveries, e = f.service.TelegramGroupDiscoveries(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if len(discoveries) != 1 {
		t.Fatal(discoveries)
	}
	var p TelegramGroupTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.PrepareTelegramGroup(ctx, tx, f.org, f.actor, b.ID, TelegramGroupInput{DiscoveryID: discoveries[0].ID, ConnectionVersion: b.Version, Enabled: true, PublicReplyConsent: true, BackgroundEnabled: true})
		return e
	})
	p, err = f.service.VerifyTelegramGroup(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var group TelegramGroup
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		group, e = f.service.SaveTelegramGroup(ctx, tx, f.org, f.actor, p)
		return e
	})
	for n := int64(4); n < 29; n++ {
		out, err = f.receive(b, n, int64(group.ChatID), n, 777777, "background text", nil)
		if err != nil || out.Result != "background_cached" {
			t.Fatalf("background %+v %v", out, err)
		}
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var count, size int
		e := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(char_length(body)),0) FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=$1::uuid`, group.ID).Scan(&count, &size)
		if e == nil && (count != 20 || size > 8000) {
			t.Fatalf("background budget %d %d", count, size)
		}
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.UnbindTelegram(ctx, tx, f.org, f.actor, identity.ID, identity.Version)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		old, e := f.service.ConfirmTelegramBinding(ctx, tx, f.org, f.actor, session, 0)
		if e == nil && old.Active {
			t.Fatal("completed replay resurrected binding")
		}
		return e
	})
	out, err = f.receive(b, 30, 987654, 30, 987654, "after unbind", nil)
	if err != nil || out.Result != "identity_required" {
		t.Fatalf("revoked %+v %v", out, err)
	}
	next, _ := f.bind(b, 31)
	if next.ID == identity.ID {
		t.Fatal("identity reused")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var oldRoutes, activeRoutes int
		e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE closed_at IS NOT NULL),count(*) FILTER(WHERE closed_at IS NULL) FROM openfox.telegram_routes WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND kind='p2p'`, b.ID).Scan(&oldRoutes, &activeRoutes)
		if e == nil && (oldRoutes != 1 || activeRoutes != 1) {
			t.Fatal("DM history transferred")
		}
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisconnectTelegramBot(ctx, tx, f.org, f.actor, b.ID, TelegramDisconnectInput{Version: b.Version, Mode: "remote_then_local"})
	})
	b = f.control(b)
	if b.State != "DISCONNECTED" || b.ControlStatus != "DONE" {
		t.Fatalf("delete %+v", b)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var cleared bool
		e := tx.QueryRow(ctx, `SELECT credentials_sealed IS NULL FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID).Scan(&cleared)
		if e == nil && !cleared {
			t.Fatal("token retained")
		}
		return e
	})
}
func TestTelegramUnknownControlNeverAutomaticallyRepeatsPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.connect()
	f.mu.Lock()
	f.loseSet = true
	f.mu.Unlock()
	b = f.control(b)
	if b.ControlStatus != "UNKNOWN" {
		t.Fatalf("unknown %+v", b)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		c, e := f.service.ClaimTelegramControl(ctx, tx, f.org, b.ID)
		if e == nil && c != nil {
			t.Fatal("unknown re-claimed")
		}
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.ReconcileTelegramWebhook(ctx, tx, f.org, f.actor, b.ID, b.Version)
	})
	b = f.control(b)
	if b.ControlStatus != "UNKNOWN" {
		t.Fatal("GET inferred header secret")
	}
	out, e := f.receive(b, 0, 987654, 1, 987654, "probe", nil)
	if e != nil || out.Result != "identity_required" {
		t.Fatalf("current header %+v %v", out, e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		b, e = f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if b.State != "ACTIVE" {
		t.Fatal(b.State)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setCalls != 1 {
		t.Fatal(fmt.Sprintf("duplicate set %d", f.setCalls))
	}
}

func TestTelegramControlLatePermissionChangePG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.connect()
	var claim *TelegramControlClaim
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		claim, e = f.service.ClaimTelegramControl(ctx, tx, f.org, b.ID)
		return e
	})
	result := f.service.RunTelegramControl(context.Background(), *claim)
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := c.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(context.Background(), `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, f.org, f.user); e != nil {
		t.Fatal(e)
	}
	other := refsTestID(t)
	if _, e = tx.Exec(context.Background(), `INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, other, other+"@example.test"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, f.org, other); e != nil {
		t.Fatal(e)
	}
	tag, e := tx.Exec(context.Background(), `UPDATE openfox.organization_members SET role='MEMBER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
	var actualVersion uint64
	if e == nil {
		e = tx.QueryRow(context.Background(), `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user).Scan(&actualVersion)
	}
	if e == nil && actualVersion != 2 {
		t.Fatal("fixture did not increment real authorization version")
	}
	if e == nil && tag.RowsAffected() != 1 {
		t.Fatal("membership fixture did not change one row")
	}
	if e == nil {
		e = tx.Commit(context.Background())
	}
	c.Close(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.FinishTelegramControl(ctx, tx, f.org, *claim, result)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadTelegramControl(ctx, tx, b.ID, false)
		if e == nil && (r.bot.State == "ACTIVE" || r.bot.LastError != "permission_denied") {
			t.Fatalf("late membership change activated bot: state=%s reason=%s", r.bot.State, r.bot.LastError)
		}
		return e
	})
}

func (f *tgFixture) preparation(in TelegramConnectInput) TelegramPreparation {
	f.t.Helper()
	var plan TelegramConnectTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, in)
		return e
	})
	proof, e := f.service.VerifyTelegramConnection(context.Background(), plan)
	if e != nil {
		f.t.Fatal(e)
	}
	var p TelegramPreparation
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, plan, proof)
		return e
	})
	return p
}
func TestTelegramPrepareIdempotencyAndGlobalClaimPG(t *testing.T) {
	f := newTGFixture(t, true)
	in := f.input()
	var plan TelegramConnectTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, in)
		return e
	})
	proof, e := f.service.VerifyTelegramConnection(context.Background(), plan)
	if e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		p TelegramPreparation
		e error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for n := 0; n < 2; n++ {
		go func() {
			<-start
			var p TelegramPreparation
			e := f.run(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				p, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, plan, proof)
				return e
			})
			results <- outcome{p, e}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.e != nil || b.e != nil || a.p.ID != b.p.ID {
		t.Fatalf("same operation: %+v %+v", a, b)
	}
	other := newTGFixture(t, true)
	other.mu.Lock()
	other.remote = f.remote
	other.mu.Unlock()
	p2 := other.preparation(other.input())
	type confirmed struct {
		b TelegramBot
		e error
	}
	claims := make(chan confirmed, 2)
	begin := make(chan struct{})
	for _, pair := range []struct {
		f *tgFixture
		p TelegramPreparation
	}{{f, a.p}, {other, p2}} {
		go func(v struct {
			f *tgFixture
			p TelegramPreparation
		}) {
			<-begin
			var bot TelegramBot
			e := v.f.run(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				bot, e = v.f.service.ConfirmTelegramPreparation(ctx, tx, v.f.org, v.f.actor, v.p.ID, TelegramConfirmInput{Version: v.p.Version, RemoteBotID: v.p.RemoteBotID.String()})
				return e
			})
			claims <- confirmed{bot, e}
		}(pair)
	}
	close(begin)
	x, y := <-claims, <-claims
	if !((x.e == nil && errors.Is(y.e, ErrTelegramBotOccupied)) || (y.e == nil && errors.Is(x.e, ErrTelegramBotOccupied))) {
		t.Fatalf("global reservation: %v %v", x.e, y.e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.telegram_bot_controls WHERE organization_id=$1::uuid`, other.org).Scan(&n)
		if e == nil && n != 0 {
			t.Fatal("RLS exposed another tenant")
		}
		return e
	})
}
func TestTelegramReceiptReplayConflictAndExpirationPG(t *testing.T) {
	f := newTGFixture(t, true)
	bot := f.control(f.connect())
	var h telegramWebhookCoordinates
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadTelegramControl(ctx, tx, bot.ID, false)
		if e != nil {
			return e
		}
		h, e = f.service.telegramCoordinates(f.org, r)
		return e
	})
	header := http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{h.Secret}}
	raw := []byte(`{"update_id":71,"edited_message":{}}`)
	for i := 0; i < 2; i++ {
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			r, e := f.service.ReceiveTelegram(ctx, tx, TelegramWebhookLookup{f.org, bot.ID}, h.Route, header, raw)
			if e == nil && r.Duplicate != (i == 1) {
				t.Fatal("receipt duplicate mismatch")
			}
			return e
		})
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReceiveTelegram(ctx, tx, TelegramWebhookLookup{f.org, bot.ID}, h.Route, header, []byte(`{"update_id":71,"channel_post":{}}`))
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("same ID different body: %v", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReceiveTelegram(ctx, tx, TelegramWebhookLookup{f.org, bot.ID}, h.Route, http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{"wrong"}}, raw)
		return e
	})
	if !errors.Is(e, telegram.ErrSignature) {
		t.Fatal(e)
	}
	var session TelegramBindingSession
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.IssueTelegramBinding(ctx, tx, f.org, f.actor, bot.ID)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, session.ID)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error { return f.service.CleanupTelegram(ctx, tx) })
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		v, e := f.service.GetTelegramBindingSession(ctx, tx, f.org, f.actor, session.ID)
		if e == nil && v.Status != "EXPIRED" {
			t.Fatal(v.Status)
		}
		return e
	})
	f.bind(bot, 72)
	out, e := f.receive(bot, 73, 987654, 73, 987654, "pending then expired", nil)
	if e != nil || out.Result != "queued" {
		t.Fatalf("queue %+v %v", out, e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='IGNORED',finished_at=clock_timestamp()-interval '8 days' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot.ID)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error { return f.service.CleanupTelegram(ctx, tx) })
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.telegram_inputs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot.ID).Scan(&n)
		if e == nil && n != 0 {
			t.Fatal("expired unmaterialized authorization retained")
		}
		return e
	})
}

type tgWorkerRunner struct{ f *tgFixture }

func (r tgWorkerRunner) RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return r.f.run(fn)
}
func (r tgWorkerRunner) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	if org != r.f.org {
		return ErrForbidden
	}
	return r.f.run(fn)
}
func TestTelegramControlWorkerAndResolverPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.connect()
	var h telegramWebhookCoordinates
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadTelegramControl(ctx, tx, b.ID, false)
		if e != nil {
			return e
		}
		h, e = f.service.telegramCoordinates(f.org, r)
		return e
	})
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	tx, e := c.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(context.Background(), `SET LOCAL ROLE openfox_context`); e != nil {
		t.Fatal(e)
	}
	var org, bot string
	if e = tx.QueryRow(context.Background(), `SELECT organization_id::text,bot_id::text FROM openfox_private.resolve_telegram_webhook($1)`, TokenHash(h.Route)).Scan(&org, &bot); e != nil {
		t.Fatal(e)
	}
	if org != f.org || bot != b.ID {
		t.Fatal("route resolver wrong target")
	}
	var due bool
	if e = tx.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM openfox_private.list_im_worker_organizations(NULL,500) WHERE organization_id=$1::uuid)`, f.org).Scan(&due); e != nil || !due {
		t.Fatalf("scanner %v due=%v", e, due)
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	worker := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "telegram-test"}
	if e = worker.ProcessTelegramControls(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		b, e = f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if b.State != "ACTIVE" {
		t.Fatal(b.State)
	}
}
