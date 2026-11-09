package stage2

// Agent 与小队集成测试的夹具：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）。
// 每个测试新建自己的两个组织（ag.org 与 ag.otherOrg），可在同一个库上重复运行。
// 处理器通过 DispatchOptions.Secrets 注入用测试密钥构造的 secrets.Box；noSecrets 是未配置主密钥的同库处理器。

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
	"openfox/internal/secrets"
)

const (
	agOwnerID    = "00000000-0000-0000-0000-0000000a6e01"
	agAdminID    = "00000000-0000-0000-0000-0000000a6e02"
	agManagerID  = "00000000-0000-0000-0000-0000000a6e03"
	agMemberID   = "00000000-0000-0000-0000-0000000a6e04"
	agGuestID    = "00000000-0000-0000-0000-0000000a6e05"
	agOutsiderID = "00000000-0000-0000-0000-0000000a6e06"
	agPassword   = "stage2 agents password"
)

// agRoles 是测试组织内的成员角色；outsider 只属于 otherOrg。manager 通过空间角色 OWNER 管理具体空间。
var agRoles = map[string]string{
	agOwnerID: "OWNER", agAdminID: "ADMIN", agManagerID: "MEMBER", agMemberID: "MEMBER", agGuestID: "GUEST",
}

// agTestKey 是测试专用主密钥（32 字节），不用于任何真实环境。
var agTestKey = []byte("openfox-stage2-agents-test-key!!")

var (
	agHashOnce sync.Once
	agHasher   identity.Argon2idHasher
	agHash     string
	agHashErr  error
)

type agEnv struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	server    *httptest.Server
	noSecrets *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*agClient
	deviceID  string
	grantID   string
}

type agClient struct {
	env    *agEnv
	http   *http.Client
	csrf   string
	server *httptest.Server
}

type agResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r agResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// mustStatus 要求响应状态码等于 want。
func (r agResponse) mustStatus(t *testing.T, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status = %d %s, want %d", r.Status, r.Body, want)
	}
}

func (r agResponse) code() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func agNewID(t *testing.T) string {
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

func agSuffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(agNewID(t), "-", "")[:10]
}

func agEmail(id string) string { return "ag-" + id[len(id)-4:] + "@example.test" }

func newAgEnv(t *testing.T) *agEnv {
	return newAgEnvWithOptions(t, api.DispatchOptions{})
}

func newAgEnvWithOptions(t *testing.T, opts api.DispatchOptions) *agEnv {
	return newAgEnvWithOptionsAndStorage(t, opts, nil)
}

func newAgEnvWithOptionsAndStorage(t *testing.T, opts api.DispatchOptions, storage artifacts.Storage) *agEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	agHashOnce.Do(func() {
		agHasher, agHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if agHashErr == nil {
			agHash, agHashErr = agHasher.Hash(agPassword)
		}
	})
	if agHashErr != nil {
		t.Fatal(agHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &agEnv{t: t, ctx: ctx, superuser: superuser, org: agNewID(t), otherOrg: agNewID(t), clients: map[string]*agClient{}}
	for _, id := range []string{agOwnerID, agAdminID, agManagerID, agMemberID, agGuestID, agOutsiderID} {
		env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, agEmail(id))
		env.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, agHash)
	}
	for _, org := range []struct{ id, owner string }{{env.org, agOwnerID}, {env.otherOrg, agOutsiderID}} {
		env.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`,
			org.id, "ag-"+agSuffix(t), "Agents "+org.id[:8])
		env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Todo', '', 'TODO', 0), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 1), ($1::uuid, 'Done', '', 'DONE', 2)`, org.id)
	}
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		env.otherOrg, agOutsiderID)
	for id, role := range agRoles {
		env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`,
			env.org, id, role)
	}
	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 16, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	box, err := secrets.New(agTestKey)
	if err != nil {
		t.Fatal(err)
	}
	identityService := identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: agHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}
	opts.Secrets = box
	env.server = httptest.NewServer(api.NewWithDispatch(identityService, database, false, storage, api.DefaultAuthRateLimits, opts))
	t.Cleanup(env.server.Close)
	env.noSecrets = httptest.NewServer(api.NewWithDispatch(identityService, database, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{}))
	t.Cleanup(env.noSecrets.Close)
	// 一台在线电脑，组织级授权 ACTIVE。
	env.deviceID = agNewID(t)
	env.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at)
		VALUES ($1::uuid, $2, 4, $3::uuid, 'ag-mac', clock_timestamp())`, env.deviceID, "ag-device-"+agSuffix(t), agOwnerID)
	env.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, env.org, env.deviceID)
	env.scalar(&env.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`,
		env.org, env.deviceID)
	return env
}

func (e *agEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *agEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

// client 返回 userID 在主处理器（带主密钥）上的登录会话（缓存）。
func (e *agEnv) client(userID string) *agClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	client := e.login(e.server, userID)
	e.clients[userID] = client
	return client
}

// clientWithoutSecrets 返回 userID 在未配置主密钥的处理器上的会话。
func (e *agEnv) clientWithoutSecrets(userID string) *agClient {
	e.t.Helper()
	return e.login(e.noSecrets, userID)
}

func (e *agEnv) login(server *httptest.Server, userID string) *agClient {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &agClient{env: e, http: &http.Client{Jar: jar}, server: server}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, agEmail(userID), agPassword)
	response, err := client.http.Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login %s status = %d", userID, response.StatusCode)
	}
	parsed, _ := neturl.Parse(server.URL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			client.csrf = cookie.Value
		}
	}
	return client
}

// do 发送请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *agClient) do(method, path string, body any, headers ...string) agResponse {
	c.env.t.Helper()
	return c.doIn(c.env.org, method, path, body, headers...)
}

func (c *agClient) doIn(org, method, path string, body any, headers ...string) agResponse {
	c.env.t.Helper()
	if !strings.HasPrefix(path, "/") {
		path = "/v1/organizations/" + org + "/" + path
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.env.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(c.env.ctx, method, c.server.URL+path, reader)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
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
	return agResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *agClient) must(want int, method, path string, body, target any, headers ...string) agResponse {
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

func (c *agClient) expectError(status int, code, method, path string, body any, headers ...string) {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
}

// ---- 资源 ----

// createSpace 由组织 owner 经 API 创建空间；manager 成为空间 OWNER（可管理），member 是普通空间成员。
func (e *agEnv) createSpace(visibility string) string {
	e.t.Helper()
	var space struct {
		ID string `json:"id"`
	}
	e.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "spaces",
		map[string]any{"name": "Space " + agSuffix(e.t), "visibility": visibility}, &space)
	e.addSpaceMember(space.ID, agManagerID, "OWNER")
	e.addSpaceMember(space.ID, agMemberID, "MEMBER")
	return space.ID
}

func (e *agEnv) addSpaceMember(spaceID, userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'ACTIVE')
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = 'ACTIVE'`,
		e.org, spaceID, userID, role)
}

type agAgent struct {
	ID                   string          `json:"id"`
	SpaceID              string          `json:"space_id"`
	OwnerUserID          string          `json:"owner_user_id"`
	Name                 string          `json:"name"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	Avatar               json.RawMessage `json:"avatar"`
	Visibility           string          `json:"visibility"`
	AllowAssign          bool            `json:"allow_assign"`
	AllowDirectChat      bool            `json:"allow_direct_chat"`
	TriggerWorkflow      bool            `json:"trigger_workflow"`
	ProfileVersion       int             `json:"profile_version"`
	Version              int             `json:"version"`
	CurrentConfigVersion int             `json:"current_config_version"`
	Online               bool            `json:"online"`
	ComputerName         *string         `json:"computer_name"`
	Favorite             bool            `json:"favorite"`
	Config               *struct {
		ComputerID       string   `json:"computer_id"`
		GrantID          string   `json:"grant_id"`
		Runtime          string   `json:"runtime"`
		Model            string   `json:"model"`
		ChatConcurrency  string   `json:"chat_concurrency"`
		WorkdirIsolation string   `json:"workdir_isolation"`
		CustomArgs       []string `json:"custom_args"`
		EnvDigest        string   `json:"env_digest"`
	} `json:"config"`
}

func agentPath(spaceID, agentID string) string { return "spaces/" + spaceID + "/agents/" + agentID }

// configBody 是一份完整的配置输入（电脑与授权取测试设备）。
func (e *agEnv) configBody(extra map[string]any) map[string]any {
	body := map[string]any{"computer_id": e.deviceID, "grant_id": e.grantID, "runtime": "codex", "model": "gpt-5"}
	for key, value := range extra {
		body[key] = value
	}
	return body
}

// createAgent 由 client 经 API 在空间里创建 Agent。
func (e *agEnv) createAgent(client *agClient, spaceID, visibility string, extra map[string]any) agAgent {
	e.t.Helper()
	body := map[string]any{"name": "Agent " + agSuffix(e.t), "kind": "CODE", "visibility": visibility, "config": e.configBody(nil)}
	for key, value := range extra {
		body[key] = value
	}
	var agent agAgent
	client.must(http.StatusCreated, http.MethodPost, "spaces/"+spaceID+"/agents", body, &agent)
	return agent
}

func (e *agEnv) getAgent(client *agClient, spaceID, agentID string) agAgent {
	e.t.Helper()
	var agent agAgent
	client.must(http.StatusOK, http.MethodGet, agentPath(spaceID, agentID), nil, &agent)
	return agent
}

// taskTypeID 读取空间的第一个任务类型。
func (e *agEnv) taskTypeID(spaceID string) string {
	e.t.Helper()
	var types struct {
		TaskTypes []struct {
			ID string `json:"id"`
		} `json:"task_types"`
	}
	e.client(agOwnerID).must(http.StatusOK, http.MethodGet, "spaces/"+spaceID+"/task-types", nil, &types)
	if len(types.TaskTypes) == 0 {
		e.t.Fatal("space has no task type")
	}
	return types.TaskTypes[0].ID
}
