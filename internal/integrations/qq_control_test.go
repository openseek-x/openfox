package integrations

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/qq"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type qqPG struct {
	t                            *testing.T
	dsn, org, user, space, agent string
	actor                        collaboration.Actor
	service                      Service
}

func newQQPG(t *testing.T) *qqPG {
	t.Helper()
	dsn := os.Getenv("OPENFOX_I14_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("exclusive I14 PostgreSQL not configured")
	}
	f := &qqPG{t: t, dsn: dsn, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t), agent: refsTestID(t)}
	device := refsTestID(t)
	c, e := pgx.Connect(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var db string
	if e = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&db); e != nil || !strings.HasPrefix(db, "openfox_i14_") {
		t.Fatal("requires exclusive I14 database", db, e)
	}
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, []any{f.user, f.user + "@example.test"}},
		{`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$1,'I14 fixture')`, []any{f.org}},
		{`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, []any{f.org}},
		{`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, []any{f.org, f.user}},
		{`INSERT INTO openfox.spaces(organization_id,id,name,prefix,visibility) VALUES($1::uuid,$2::uuid,'QQ fixture','QQ','SHARED')`, []any{f.org, f.space}},
		{`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$1,4,$2::uuid,'QQ fixture',clock_timestamp())`, []any{device, f.user}},
		{`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, []any{f.org, device}},
		{`INSERT INTO openfox.agents(organization_id,id,device_id,space_id,owner_user_id,name,kind,visibility,current_config_version,version,allow_direct_chat) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'QQ Agent','CODE','SHARED',1,1,true)`, []any{f.org, f.agent, device, f.space, f.user}},
		{`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by) SELECT $1::uuid,$2::uuid,1,$3::uuid,id,'codex','default',$4::uuid FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$3::uuid`, []any{f.org, f.agent, device, f.user}},
	}
	for _, q := range queries {
		if _, e = c.Exec(context.Background(), q.sql, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		t.Fatal(e)
	}
	box, e := secrets.New(key)
	if e != nil {
		t.Fatal(e)
	}
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test"}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return f
}
func (f *qqPG) run(fn func(context.Context, pgx.Tx) error) error { return f.runOrg(f.org, fn) }
func (f *qqPG) runOrg(org string, fn func(context.Context, pgx.Tx) error) error {
	tenant, e := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: org, ActorID: f.user, AuthorizationVersion: f.actor.AuthorizationVersion}, tenancy.SourceUserRequest)
	if e != nil {
		return e
	}
	ctx := tenancy.WithTenantContext(context.Background(), tenant)
	c, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,$3,'user_request')`, org, f.user, f.actor.AuthorizationVersion); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); e != nil {
		return e
	}
	if e = fn(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (f *qqPG) create(key string) (QQBot, error) {
	var b QQBot
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.CreateQQBot(ctx, tx, f.org, f.actor, QQCreateBotInput{AgentID: f.agent, Name: "Controlled QQ", IdempotencyKey: key})
		return e
	})
	return b, e
}
func qqTLS(t *testing.T, h http.HandlerFunc) *qq.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.bot.qq.com" {
			t.Error("unexpected QQ host")
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.bot.qq.com:443" {
			return nil, errors.New("external request forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &qq.Client{Transport: tr}
}
func (f *qqPG) prepare(b QQBot, app string) (QQConnectWork, error) {
	var w QQConnectWork
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		w, e = f.service.PrepareQQConnection(ctx, tx, f.org, f.actor, b.ID, QQConnectInput{Version: b.Version, AppID: app, AppSecret: "synthetic-secret"})
		return e
	})
	return w, e
}
func (f *qqPG) finish(w QQConnectWork, r QQConnectResult) (QQBot, error) {
	var b QQBot
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.FinishQQConnection(ctx, tx, f.org, f.actor, w, r)
		return e
	})
	return b, e
}

func TestQQCreateConcurrentAndTenantPG(t *testing.T) {
	f := newQQPG(t)
	const n = 5
	items := make(chan QQBot, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); b, e := f.create("same-operation-key"); items <- b; errs <- e }()
	}
	wg.Wait()
	close(items)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for b := range items {
		if id == "" {
			id = b.ID
		}
		if b.ID != id {
			t.Fatal("idempotency duplicated bot")
		}
	}
	if _, e := f.create("other-operation-key"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("Agent slot not reserved", e)
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.CreateQQBot(ctx, tx, f.org, f.actor, QQCreateBotInput{AgentID: f.agent, Name: "Changed", IdempotencyKey: "same-operation-key"})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("idempotency parameter conflict", e)
	}
	other := newQQPG(t)
	e = other.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := other.service.GetQQBot(ctx, tx, other.org, other.actor, id)
		return e
	})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("cross tenant visible", e)
	}
}
func TestQQCredentialTLSClaimAndLateFencePG(t *testing.T) {
	f := newQQPG(t)
	b, e := f.create("connect-operation-key")
	if e != nil {
		t.Fatal(e)
	}
	w, e := f.prepare(b, "appid-"+f.org)
	if e != nil {
		t.Fatal(e)
	}
	client := qqTLS(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			t.Error("unexpected path")
		}
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"access_token":"synthetic-token","expires_in":7200}`)
	})
	r := ExecuteQQConnection(context.Background(), client, w)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	b, e = f.finish(w, r)
	if e != nil || b.State != "WAITING_WEBHOOK" {
		t.Fatal(b, e)
	}
	if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		u, e := f.service.RevealQQWebhook(ctx, tx, f.org, f.actor, b.ID, b.Version)
		if e == nil && !strings.HasPrefix(u, "https://openfox.example.test/api/v1/hooks/qq/") {
			t.Error("callback prefix")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	other := newQQPG(t)
	b2, e := other.create("cross-claim-operation")
	if e != nil {
		t.Fatal(e)
	}
	w2, e := other.prepare(b2, w.Credentials.AppID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.finish(w2, r); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("cross tenant AppID claim", e)
	}
	if _, e = f.prepare(b, "different-app"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("same bot changed AppID", e)
	}
	w, e = f.prepare(b, b.AppID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.DisconnectQQBot(ctx, tx, f.org, f.actor, b.ID, w.Version)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.finish(w, r); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("late token revived disconnect", e)
	}
	if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var clean bool
		e := tx.QueryRow(ctx, `SELECT c.credentials_sealed IS NULL AND q.token_sealed IS NULL AND q.webhook_hash IS NULL AND q.app_id=$2 FROM openfox.qq_bot_controls q JOIN openfox.integration_connections c ON c.organization_id=q.organization_id AND c.id=q.connection_id WHERE q.bot_id=$1::uuid`, b.ID, b.AppID).Scan(&clean)
		if !clean {
			t.Error("secrets retained or AppID claim cleared")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestQQRouteControlRolePG(t *testing.T) {
	f := newQQPG(t)
	b, e := f.create("route-role-operation")
	if e != nil {
		t.Fatal(e)
	}
	w, e := f.prepare(b, "appid-"+f.org)
	if e != nil {
		t.Fatal(e)
	}
	b, e = f.finish(w, QQConnectResult{Token: qq.Token{AccessToken: "controlled-token", ExpiresAt: time.Now().Add(time.Hour)}})
	if e != nil {
		t.Fatal(e)
	}
	var token string
	if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		u, e := f.service.RevealQQWebhook(ctx, tx, f.org, f.actor, b.ID, b.Version)
		token = u[strings.LastIndex(u, "/")+1:]
		return e
	}); e != nil {
		t.Fatal(e)
	}
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
	e = tx.QueryRow(context.Background(), `SELECT organization_id::text,bot_id::text FROM openfox_private.qq_webhook_route($1)`, TokenHash(token)).Scan(&org, &bot)
	if e != nil || org != f.org || bot != b.ID {
		t.Fatal("router cannot locate without tenant", e)
	}
	_, e = tx.Exec(context.Background(), `SELECT token_sealed FROM openfox.qq_bot_controls`)
	var pe *pgconn.PgError
	if !errors.As(e, &pe) || pe.Code != "42501" {
		t.Fatal("context role can read credential table", e)
	}
}
