package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/discord"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
	"os"
	"strings"
	"sync"
	"testing"
)

type discordFixture struct {
	t                                    *testing.T
	dsn, org, user, space, device, grant string
	actor                                collaboration.Actor
	service                              Service
	remote                               string
	mu                                   sync.Mutex
	calls                                []string
	gateway                              func(http.ResponseWriter, *http.Request)
	sendMode                             string
	posts                                int
	lastPost                             map[string]any
}
type discordTransport struct {
	url   *url.URL
	inner http.RoundTripper
}

func (t discordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "discord.com" && r.URL.Host != "gateway.discord.gg" {
		return nil, errors.New("fixture rejected origin")
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.url.Scheme
	u.Host = t.url.Host
	c.URL = &u
	return t.inner.RoundTrip(c)
}
func newDiscordFixture(t *testing.T) *discordFixture {
	t.Helper()
	f := &discordFixture{t: t, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t), device: refsTestID(t)}
	h := sha256.Sum256([]byte(f.org))
	var n uint64
	for _, v := range h[:7] {
		n = n*256 + uint64(v)
	}
	f.remote = fmt.Sprint(n + 1)
	box, err := secrets.New(bytes.Repeat([]byte{39}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && f.gateway != nil {
			f.gateway(w, r)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v10/channels/7000002/messages" {
			var in map[string]any
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			f.mu.Lock()
			f.posts++
			f.lastPost = in
			mode := f.sendMode
			f.mu.Unlock()
			if mode == "unknown" {
				c, _, _ := w.(http.Hijacker).Hijack()
				_ = c.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if mode == "forbidden" {
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "Missing access", "code": 50013})
				return
			}
			if mode == "rate" {
				w.WriteHeader(429)
				_ = json.NewEncoder(w).Encode(map[string]any{"retry_after": 0.125, "global": false})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "9900001", "channel_id": "7000002", "guild_id": "5000001", "author": map[string]any{"id": f.remote, "username": "Bot", "bot": true}, "content": in["content"], "nonce": in["nonce"], "message_reference": in["message_reference"], "type": 19, "timestamp": "2026-11-15T00:00:00Z"})
			return
		}
		if r.Method != "GET" {
			t.Error("control must be read-only")
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		f.mu.Unlock()
		var body any
		switch r.URL.Path {
		case "/api/v10/users/@me":
			body = map[string]any{"id": f.remote, "username": "fixture_bot", "bot": true}
		case "/api/v10/applications/@me":
			body = map[string]any{"id": "18446744073709551615", "name": "Fixture", "bot": map[string]any{"id": f.remote, "username": "fixture_bot", "bot": true}, "bot_public": true, "bot_require_code_grant": false, "flags": 1 << 18}
		case "/api/v10/gateway/bot":
			body = map[string]any{"url": "wss://gateway.discord.gg", "shards": 1, "session_start_limit": map[string]any{"total": 1000, "remaining": 999, "reset_after": 1000, "max_concurrency": 1}}
		case "/api/v10/channels/7000002/messages/9000001":
			body = discordGatewayMessage(f.remote, "")
		case "/api/v10/channels/7000002/messages":
			if r.URL.Query().Get("before") != "9000001" || r.URL.Query().Get("limit") != "20" {
				t.Error("history not bounded to source")
			}
			body = []any{}
		case "/api/v10/channels/7000002":
			body = map[string]any{"id": "7000002", "guild_id": "5000001", "type": 0, "name": "fixture-channel"}
		case "/api/v10/guilds/5000001/members/6000001", "/api/v10/guilds/5000001/members/" + f.remote:
			body = map[string]any{"user": map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/api/v10/guilds/5000001/members/"), "username": "fixture"}, "pending": false}
		case "/api/v10/channels/7000002/permissions/member/6000001", "/api/v10/channels/7000002/permissions/member/" + f.remote:
			body = map[string]any{"permissions": "68624"}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test", Discord: discord.NewClient(discordTransport{u, server.Client().Transport})}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	f.dsn = os.Getenv("OPENFOX_I17_TEST_DATABASE_URL")
	if f.dsn == "" {
		t.Skip("exclusive I17 PostgreSQL is required")
	}
	c, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var name string
	if err = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || !strings.HasPrefix(name, "openfox_i17_") {
		t.Fatal("exclusive openfox_i17_ database required", err)
	}
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, []any{f.user, f.user + "@example.test"}},
		{`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Discord fixture')`, []any{f.org, f.org}},
		{`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, []any{f.org}},
		{`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, []any{f.org, f.user}},
		{`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES($1::uuid,$2::uuid,'DCTEST','Discord','PRIVATE')`, []any{f.org, f.space}},
		{`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, []any{f.org, f.space, f.user}},
		{`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'Discord fixture',clock_timestamp())`, []any{f.device, "discord-" + f.org, f.user}},
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
func (f *discordFixture) run(fn func(context.Context, pgx.Tx) error) error {
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
func (f *discordFixture) must(fn func(context.Context, pgx.Tx) error) {
	f.t.Helper()
	if err := f.run(fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *discordFixture) input() DiscordConnectInput {
	return DiscordConnectInput{OperationKey: refsTestID(f.t), Kind: BotKindTaskAssistant, SpaceID: f.space, Token: "synthetic_fixture_token", Runtime: &FeishuAssistantRuntime{ComputerID: f.device, GrantID: f.grant, Runtime: "codex", Model: "default"}}
}
func (f *discordFixture) preparation(in DiscordConnectInput) DiscordPreparation {
	f.t.Helper()
	var plan DiscordConnectTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareDiscordConnection(ctx, tx, f.org, f.actor, in)
		return e
	})
	proof, e := f.service.VerifyDiscordConnection(context.Background(), plan)
	if e != nil {
		f.t.Fatal(e)
	}
	var p DiscordPreparation
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.SaveDiscordPreparation(ctx, tx, f.org, f.actor, plan, proof)
		return e
	})
	return p
}
func (f *discordFixture) confirm(p DiscordPreparation) (DiscordBot, error) {
	var b DiscordBot
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ConfirmDiscordPreparation(ctx, tx, f.org, f.actor, p.ID, DiscordConfirmInput{Version: p.Version, RemoteBotID: string(p.RemoteBotID), ApplicationID: string(p.ApplicationID)})
		return e
	})
	return b, e
}
func (f *discordFixture) connect() DiscordBot {
	f.t.Helper()
	p := f.preparation(f.input())
	b, e := f.confirm(p)
	if e != nil {
		f.t.Fatal(e)
	}
	return b
}
func TestDiscordControlPG(t *testing.T) {
	f := newDiscordFixture(t)
	in := f.input()
	p := f.preparation(in)
	p2 := f.preparation(in)
	if p.ID != p2.ID {
		t.Fatal("operation replay changed preparation")
	}
	b, e := f.confirm(p)
	if e != nil {
		t.Fatal(e)
	}
	b2, e := f.confirm(p)
	if e != nil || b.ID != b2.ID {
		t.Fatal("confirm replay", e)
	}
	if b.State != "ACTIVE" || b.ApplicationID != "18446744073709551615" || !strings.Contains(b.InstallationURL, "permissions=68608") {
		t.Fatal(b)
	}
	in.BotID = b.ID
	in.Version = b.Version
	in.OperationKey = refsTestID(t)
	in.Token = "rotated_fixture_token"
	rotate := f.preparation(in)
	next, e := f.confirm(rotate)
	if e != nil {
		t.Fatal(e)
	}
	if next.Version != b.Version+1 {
		t.Fatal("rotation version")
	}
	if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisconnectDiscordBot(ctx, tx, f.org, f.actor, b.ID, b.Version)
	}); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("stale disconnect", e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisconnectDiscordBot(ctx, tx, f.org, f.actor, next.ID, next.Version)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL`, b.ConnectionID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Fatal("credentials not erased")
		}
		return nil
	})
}
func TestDiscordControlConcurrentConfirmPG(t *testing.T) {
	f := newDiscordFixture(t)
	p := f.preparation(f.input())
	var wg sync.WaitGroup
	results := make(chan DiscordBot, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); b, e := f.confirm(p); results <- b; errs <- e }()
	}
	wg.Wait()
	a, b := <-results, <-results
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if a.ID != b.ID {
		t.Fatal("double confirmation created two bots")
	}
}
func TestDiscordControlRevokedSpacePG(t *testing.T) {
	f := newDiscordFixture(t)
	p := f.preparation(f.input())
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	if _, e = c.Exec(context.Background(), `DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, f.org, f.space, f.user); e != nil {
		t.Fatal(e)
	}
	if _, e = f.confirm(p); !errors.Is(e, ErrForbidden) {
		t.Fatal("revoked space accepted", e)
	}
}
func TestDiscordSchemaPermissionsPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.discord_bot_controls`).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Fatal("RLS scope", n)
		}
		var canUpdate bool
		if e := tx.QueryRow(ctx, `SELECT has_table_privilege(current_user,'openfox.discord_inputs','UPDATE')`).Scan(&canUpdate); e != nil {
			return e
		}
		if canUpdate {
			t.Fatal("snapshot mutable")
		}
		return nil
	})
	_ = b
}

func TestDiscordScannersAndDomainPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	if _, e = c.Exec(context.Background(), `SET ROLE openfox_context`); e != nil {
		t.Fatal(e)
	}
	var found bool
	if e = c.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM openfox_private.list_im_connections(NULL,NULL,500) WHERE organization_id=$1::uuid AND connection_id=$2::uuid)`, f.org, b.ConnectionID).Scan(&found); e != nil || !found {
		t.Fatal("connection scanner", found, e)
	}
	if _, e = c.Exec(context.Background(), `RESET ROLE`); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"0", "-1", "18446744073709551616", "01", "1e4"} {
		if _, e = c.Exec(context.Background(), `SELECT $1::openfox.discord_snowflake`, id); e == nil {
			t.Fatalf("invalid snowflake accepted %q", id)
		}
	}
	var value string
	if e = c.QueryRow(context.Background(), `SELECT '18446744073709551615'::openfox.discord_snowflake::text`).Scan(&value); e != nil || value != "18446744073709551615" {
		t.Fatal(value, e)
	}
}
