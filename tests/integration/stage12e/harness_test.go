package stage12e

// 阶段 12e「电脑管理与运维」集成测试夹具（由 stage12b 夹具改写）：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）
// + 真实设备注册 / 挑战签名 / 会话流程。每个测试新建三个组织（env.org、env.otherOrg、env.guestOrg），用户 ID 固定、组织随机，
// 可在同一个库上重复运行。
//
// 角色：owner 在 org 是 OWNER、在 otherOrg 是 MEMBER、在 guestOrg 是 GUEST（电脑所有者）；admin 在 org 是 ADMIN；
// member 在 org 是 MEMBER；outsider 是 otherOrg 与 guestOrg 的 OWNER，不属于 org。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"openfox/internal/artifacts"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

const (
	qcOwnerID    = "00000000-0000-0000-0000-0000000012e1"
	qcMemberID   = "00000000-0000-0000-0000-0000000012e2"
	qcGuestID    = "00000000-0000-0000-0000-0000000012e3"
	qcOutsiderID = "00000000-0000-0000-0000-0000000012e4"
	qcAdminID    = "00000000-0000-0000-0000-0000000012e6"
	qcPassword   = "stage12e computers password"
)

var qcRoles = map[string]string{qcOwnerID: "OWNER", qcMemberID: "MEMBER", qcGuestID: "GUEST", qcAdminID: "ADMIN"}

var (
	qcHashOnce sync.Once
	qcHasher   identity.Argon2idHasher
	qcHash     string
	qcHashErr  error
)

type qcEnv struct {
	t         *testing.T
	ctx       context.Context
	url       string
	superuser *pgx.Conn
	database  *postgres.Pool
	server    *httptest.Server
	org       string
	otherOrg  string
	guestOrg  string
	clients   map[string]*qcClient
}

type qcClient struct {
	env  *qcEnv
	http *http.Client
	csrf string
}

type qcResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r qcResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r qcResponse) code() string {
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func qcNewID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	text := hex.EncodeToString(raw[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func qcSuffix(t *testing.T) string { return strings.ReplaceAll(qcNewID(t), "-", "")[:8] }

// qcPrefix 返回随机标识符（7 位大写字母数字，首位字母）。
func qcPrefix(t *testing.T) string { return "M" + strings.ToUpper(qcSuffix(t)[:6]) }

func qcEmail(id string) string { return "s12e-" + id[len(id)-4:] + "@example.test" }

func newQcEnv(t *testing.T) *qcEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	qcHashOnce.Do(func() {
		qcHasher, qcHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if qcHashErr == nil {
			qcHash, qcHashErr = qcHasher.Hash(qcPassword)
		}
	})
	if qcHashErr != nil {
		t.Fatal(qcHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &qcEnv{t: t, ctx: ctx, url: url, superuser: superuser, org: qcNewID(t), otherOrg: qcNewID(t), guestOrg: qcNewID(t), clients: map[string]*qcClient{}}
	env.seedOrganizations()
	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 24, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	env.database = database
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: qcHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	env.server = httptest.NewServer(api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()},
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	t.Cleanup(env.server.Close)
	return env
}

func (e *qcEnv) seedOrganizations() {
	e.t.Helper()
	for _, id := range []string{qcOwnerID, qcMemberID, qcGuestID, qcOutsiderID, qcAdminID} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, qcEmail(id))
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, qcHash)
	}
	for _, org := range []string{e.org, e.otherOrg, e.guestOrg} {
		e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, "s12e-"+qcSuffix(e.t), "Stage12e "+org[:8])
		// 组织目录（经接口创建组织时由 create_first_organization 写入；直接写库的夹具需要补上）。
		e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
			SELECT id, slug, name, created_at FROM openfox.organizations WHERE id = $1::uuid`, org)
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2), ($1::uuid, 'Review', '', 'IN_PROGRESS', 3),
			($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Canceled', '', 'CANCELLED', 5)`, org)
		e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4)
			ON CONFLICT DO NOTHING`, org)
	}
	for _, org := range []string{e.otherOrg, e.guestOrg} {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
			org, qcOutsiderID)
	}
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`,
		e.otherOrg, qcOwnerID)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'GUEST')`,
		e.guestOrg, qcOwnerID)
	for id, role := range qcRoles {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, e.org, id, role)
	}
}

func (e *qcEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *qcEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *qcEnv) count(query string, args ...any) int {
	e.t.Helper()
	var value int
	e.scalar(&value, query, args...)
	return value
}

// client 返回 userID 的登录会话（缓存）。
func (e *qcEnv) client(userID string) *qcClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &qcClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, qcEmail(userID), qcPassword)
	response, err := client.http.Post(e.server.URL+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login %s status = %d", userID, response.StatusCode)
	}
	parsed, _ := neturl.Parse(e.server.URL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			client.csrf = cookie.Value
		}
	}
	e.clients[userID] = client
	return client
}

func (e *qcEnv) owner() *qcClient    { return e.client(qcOwnerID) }
func (e *qcEnv) admin() *qcClient    { return e.client(qcAdminID) }
func (e *qcEnv) member() *qcClient   { return e.client(qcMemberID) }
func (e *qcEnv) guest() *qcClient    { return e.client(qcGuestID) }
func (e *qcEnv) outsider() *qcClient { return e.client(qcOutsiderID) }

// in 把相对路径拼到指定组织前缀之后。
func in(org, path string) string { return "/v1/organizations/" + org + "/" + path }

// do 发送 JSON 请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *qcClient) do(method, path string, body any, headers ...string) qcResponse {
	c.env.t.Helper()
	var reader io.Reader
	contentType := ""
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.env.t.Fatal(err)
		}
		reader, contentType = bytes.NewReader(encoded), "application/json"
	}
	return c.raw(method, path, reader, contentType, headers...)
}

func (c *qcClient) raw(method, path string, reader io.Reader, contentType string, headers ...string) qcResponse {
	c.env.t.Helper()
	if !strings.HasPrefix(path, "/") {
		path = "/v1/organizations/" + c.env.org + "/" + path
	}
	request, err := http.NewRequestWithContext(c.env.ctx, method, c.env.server.URL+path, reader)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.env.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		c.env.t.Fatal(err)
	}
	return qcResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *qcClient) must(want int, method, path string, body, target any, headers ...string) qcResponse {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != want {
		c.env.t.Fatalf("%s %s = %d %s, want %d", method, path, response.Status, response.Body, want)
	}
	if target != nil {
		response.decode(c.env.t, target)
	}
	return response
}

func (c *qcClient) expectError(status int, code, method, path string, body any, headers ...string) qcResponse {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
	return response
}
