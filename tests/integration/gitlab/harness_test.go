package gitlabint

// 集成测试夹具：真实 HTTP 处理器（登录会话、CSRF、组织选择、空间授权、租户事务）+ 本机模拟 GitLab。
// 绝不访问真实的 GitLab / 极狐 / GitHub / Gitee。

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
	"openfox/internal/integrations/gitlab"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
	fakegitlab "openfox/tests/support/fakegitlab/fake"
)

const password = "gitlab integration password"

// bodyLog 记录所有登录用户请求的响应体，用于断言令牌从未出现在任何响应里。
type bodyLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *bodyLog) add(body string) {
	l.mu.Lock()
	l.bodies = append(l.bodies, body)
	l.mu.Unlock()
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
	t         *testing.T
	ctx       context.Context
	super     *pgx.Conn
	pool      *postgres.Pool
	fake      *fakegitlab.Server
	server    *httptest.Server
	strict    *httptest.Server // 生产式严格实例地址校验（拒绝本机 / 私网）
	bare      *httptest.Server // 未配置 Git 工厂的实例
	nobox     *httptest.Server // 已配置但没有主密钥的实例
	log       *bodyLog
	hasher    identity.Argon2idHasher
	passHash  string
	publicURL string
	box       *secrets.Box
	factory   integrations.GitClientFactory
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
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
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
	fake := fakegitlab.New(fakegitlab.Options{ClientID: "app-id", ClientSecret: "app-secret", Login: "lin", UserID: 4242,
		Groups: []string{"openfox-dev", "other-group"}})
	t.Cleanup(fake.Close)
	authService := identity.Service{Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	e := &env{t: t, ctx: ctx, super: super, pool: pool, fake: fake, log: &bodyLog{}, hasher: hasher, passHash: hash, box: box}
	local := gitlab.Options{AllowLocal: true}
	e.factory = integrations.NewGitClientFactory(local)
	mk := func(opts api.DispatchOptions) *httptest.Server {
		return httptest.NewServer(mountAPI(api.NewWithDispatch(authService, pool, false, nil, api.DefaultAuthRateLimits, opts)))
	}
	// 配置完整的实例：PUBLIC_URL 指向自己，GitLab 实例是本机模拟服务（开发 / 测试环境允许 http 与本机地址）。
	e.server = httptest.NewUnstartedServer(nil)
	e.publicURL = "http://" + e.server.Listener.Addr().String()
	e.server.Config.Handler = mountAPI(api.NewWithDispatch(authService, pool, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{Secrets: box, PublicURL: e.publicURL, GitFactory: e.factory, GitValidateInstance: integrations.NewInstanceValidator(local)}))
	e.server.Start()
	t.Cleanup(e.server.Close)
	// 严格实例：生产环境的校验规则，本机模拟服务的地址会被拒绝。
	strictOpts := gitlab.Options{Safe: safehttp.Options{}}
	e.strict = mk(api.DispatchOptions{Secrets: box, PublicURL: e.publicURL, GitFactory: integrations.NewGitClientFactory(strictOpts),
		GitValidateInstance: integrations.NewInstanceValidator(strictOpts)})
	t.Cleanup(e.strict.Close)
	// 未配置实例：用于 409 INTEGRATION_NOT_CONFIGURED。
	e.bare = mk(api.DispatchOptions{Secrets: box, PublicURL: "http://127.0.0.1:1"})
	t.Cleanup(e.bare.Close)
	// 没有主密钥：503 SECRETS_UNAVAILABLE。
	e.nobox = mk(api.DispatchOptions{PublicURL: "http://127.0.0.1:1", GitFactory: e.factory, GitValidateInstance: integrations.NewInstanceValidator(local)})
	t.Cleanup(e.nobox.Close)
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

func (e *env) newUUID() string {
	var id string
	e.scan(&id, `SELECT gen_random_uuid()::text`)
	return id
}

// user 建立用户（带密码）并返回 ID。
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

// noRedirect 返回共享登录 cookie、但不跟随重定向的客户端。
func (c *client) noRedirect() *http.Client {
	return &http.Client{Jar: c.http.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
