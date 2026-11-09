package wechatint

// 集成测试夹具：真实 HTTP 处理器（登录会话、CSRF、组织选择、租户事务）+ 本机模拟微信（iLink）。
// 绝不访问真实的 ilinkai.weixin.qq.com。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/wechat"
	"openfox/internal/platform/postgres"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
	fakewechat "openfox/tests/support/fakewechat/fake"
)

const password = "wechat integration password"

type bodyLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *bodyLog) add(body string) {
	l.mu.Lock()
	l.bodies = append(l.bodies, body)
	l.mu.Unlock()
}

func (l *bodyLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.bodies, "\n")
}

type client struct {
	t      *testing.T
	base   string
	http   *http.Client
	csrf   string
	log    *bodyLog
	userID string
}

type env struct {
	t        *testing.T
	ctx      context.Context
	super    *pgx.Conn
	pool     *postgres.Pool
	fake     *fakewechat.Server
	server   *httptest.Server
	bare     *httptest.Server // 未配置微信客户端的实例
	log      *bodyLog
	passHash string
	box      *secrets.Box
	wc       *wechat.Client
	service  integrations.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	super, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { super.Close(context.Background()) })
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 24, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	fake := fakewechat.NewServer()
	fake.Start()
	t.Cleanup(fake.Close)
	authService := identity.Service{Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	e := &env{t: t, ctx: ctx, super: super, pool: pool, fake: fake, log: &bodyLog{}, passHash: hash, box: box}
	// 开发 / 测试环境：允许 confirmed 返回的 baseurl 等于配置的基址（本机模拟服务）。
	e.wc = wechat.New(fake.URL, "test", true)
	e.service = integrations.Service{WeChat: e.wc, Box: box}
	e.server = httptest.NewServer(mountAPI(api.NewWithDispatch(authService, pool, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{Secrets: box, PublicURL: "http://127.0.0.1:1", WeChatClient: e.wc})))
	t.Cleanup(e.server.Close)
	e.bare = httptest.NewServer(mountAPI(api.NewWithDispatch(authService, pool, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{Secrets: box, PublicURL: "http://127.0.0.1:1"})))
	t.Cleanup(e.bare.Close)
	return e
}

func mountAPI(handler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", handler))
	return mux
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.super.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *env) scan(dest any, query string, args ...any) {
	e.t.Helper()
	if err := e.super.QueryRow(e.ctx, query, args...).Scan(dest); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	e.scan(&n, query, args...)
	return n
}

func (e *env) newUUID() string {
	var id string
	e.scan(&id, `SELECT gen_random_uuid()::text`)
	return id
}

func (e *env) user(email string) string {
	id := e.newUUID()
	e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2)`, id, email)
	e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)`, id, e.passHash)
	return id
}

func (e *env) login(base, email, userID string) *client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &client{t: e.t, base: base, http: &http.Client{Jar: jar}, log: e.log, userID: userID}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, password)
	response, err := c.http.Post(base+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login %s = %d", email, response.StatusCode)
	}
	parsed, _ := neturl.Parse(base)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			c.csrf = cookie.Value
		}
	}
	if c.csrf == "" {
		e.t.Fatal("login did not set the CSRF cookie")
	}
	return c
}

func (c *client) do(method, path string, body any) (int, string) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	c.log.add(string(payload))
	return response.StatusCode, string(payload)
}

func (c *client) json(want int, method, path string, body, target any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			c.t.Fatalf("decode %s: %v", payload, err)
		}
	}
}

func (c *client) expect(want int, code, method, path string, body any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want || (code != "" && !strings.Contains(payload, `"`+code+`"`)) {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, payload, want, code)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// world 是一个组织 A（所有者、管理员、成员、访客）和组织 B（另一个所有者）。
type world struct {
	*env
	org, orgB                           string
	ownerID, adminID, memberID, guestID string
	ownerBID                            string
	owner, admin, member, guest         *client
	ownerB                              *client
	space                               string
	deviceID, grantID                   string
	poller                              *integrations.WeChatPoller
	worker                              *integrations.WeChatWorker
}

func (w *world) path(suffix string) string { return "/v1/organizations/" + w.org + suffix }

func setup(t *testing.T) *world {
	e := newEnv(t)
	// 同一个库里其他用例留下的绑定不应被本用例的轮询管理器接手。
	e.exec(`UPDATE openfox.wechat_bindings SET status = 'REMOVED', removed_at = clock_timestamp(), bot_token_sealed = '\x'::bytea
	         WHERE status <> 'REMOVED'`)
	w := &world{env: e}
	w.org, w.orgB = e.newUUID(), e.newUUID()
	suffix := w.org[:8]
	mk := func(name string, target *string) {
		*target = e.user(fmt.Sprintf("wc-%s-%s@example.test", name, suffix))
	}
	mk("owner", &w.ownerID)
	mk("admin", &w.adminID)
	mk("member", &w.memberID)
	mk("guest", &w.guestID)
	mk("ownerb", &w.ownerBID)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'WeChat A'), ($3::uuid, $4, 'WeChat B')`,
		w.org, "wc-a-"+suffix, w.orgB, "wc-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at) VALUES ($1::uuid, $2, 'WeChat A', now()), ($3::uuid, $4, 'WeChat B', now())`,
		w.org, "wc-a-"+suffix, w.orgB, "wc-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'ADMIN'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'GUEST'),
		($6::uuid, $7::uuid, 'OWNER')`, w.org, w.ownerID, w.adminID, w.memberID, w.guestID, w.orgB, w.ownerBID)
	for _, org := range []string{w.org, w.orgB} {
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Cancelled', '', 'CANCELLED', 5)`, org)
	}
	base := e.server.URL + "/api"
	w.owner = e.login(base, "wc-owner-"+suffix+"@example.test", w.ownerID)
	w.admin = e.login(base, "wc-admin-"+suffix+"@example.test", w.adminID)
	w.member = e.login(base, "wc-member-"+suffix+"@example.test", w.memberID)
	w.guest = e.login(base, "wc-guest-"+suffix+"@example.test", w.guestID)
	w.ownerB = e.login(base, "wc-ownerb-"+suffix+"@example.test", w.ownerBID)
	var space struct {
		ID string `json:"id"`
	}
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Fox", "prefix": "fox", "visibility": "SHARED"}, &space)
	w.space = space.ID
	w.deviceID = e.newUUID()
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at)
		VALUES ($1::uuid, $2, 4, $3::uuid, 'wc-mac', clock_timestamp())`, w.deviceID, "wc-device-"+suffix, w.ownerID)
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, w.org, w.deviceID)
	e.scan(&w.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, w.org, w.deviceID)
	w.poller = &integrations.WeChatPoller{Pool: w.pool, Service: w.service, Instance: "poller-1",
		ScanInterval: time.Hour, RenewInterval: time.Hour, RetryBase: 100 * time.Millisecond}
	w.worker = &integrations.WeChatWorker{Pool: w.pool, Service: w.service, Instance: "worker-1"}
	t.Cleanup(w.poller.Stop)
	return w
}

func (w *world) createAgent(c *client, name, visibility string) string {
	w.t.Helper()
	var agent struct {
		ID string `json:"id"`
	}
	c.json(http.StatusCreated, http.MethodPost, w.path("/spaces/"+w.space+"/agents"),
		map[string]any{"name": name, "kind": "CODE", "visibility": visibility,
			"config": map[string]any{"computer_id": w.deviceID, "grant_id": w.grantID, "runtime": "codex", "model": "gpt-5"}}, &agent)
	return agent.ID
}

// pump 跑一轮轮询管理器扫描与 worker。
func (w *world) pump() {
	w.t.Helper()
	if err := w.poller.Scan(w.ctx); err != nil {
		w.t.Fatal(err)
	}
	if err := w.worker.RunOnce(w.ctx); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) agentSays(conversationID, agentID, kind, body string) {
	w.t.Helper()
	id := w.newUUID()
	selection := tenancy.OrganizationSelection{OrganizationID: w.org, ActorID: w.memberID, AuthorizationVersion: 1}
	err := w.pool.RunForOrganization(w.ctx, w.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			var seq int64
			if err := tx.QueryRow(ctx, `UPDATE openfox.conversations SET next_message_seq = next_message_seq + 1
				WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING next_message_seq - 1`, conversationID).Scan(&seq); err != nil {
				return err
			}
			if kind == "AGENT" {
				_, err := tx.Exec(ctx, `INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, author_agent_id, body)
					VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'AGENT', $4::uuid, $5)`, id, conversationID, seq, agentID, body)
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, body)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'SYSTEM', $4)`, id, conversationID, seq, body)
			return err
		})
	if err != nil {
		w.t.Fatal(err)
	}
}

// wechatClientProd 构造生产模式的客户端（不允许等于配置基址）。
func wechatClientProd(base string) *wechat.Client { return wechat.New(base, "test", false) }
