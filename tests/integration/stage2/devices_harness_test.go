package stage2

// 电脑与统一事件流集成测试的夹具：真实 HTTP 处理器 + 真实租户事务。
// 每个测试新建自己的两个组织（dv.org 与 dv.otherOrg），互不干扰，可在同一个库上重复运行。
// 账号行全局共享（ON CONFLICT DO NOTHING），组织成员关系按测试组织各自插入。

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
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	dvOwnerID    = "00000000-0000-0000-0000-0000000de501"
	dvAdminID    = "00000000-0000-0000-0000-0000000de502"
	dvMemberID   = "00000000-0000-0000-0000-0000000de503"
	dvMember2ID  = "00000000-0000-0000-0000-0000000de504"
	dvGuestID    = "00000000-0000-0000-0000-0000000de505"
	dvOutsiderID = "00000000-0000-0000-0000-0000000de506"
	dvPassword   = "stage2 devices password"
)

// dvRoles 是测试组织内的成员角色；outsider 只属于 otherOrg。
var dvRoles = map[string]string{
	dvOwnerID: "OWNER", dvAdminID: "ADMIN", dvMemberID: "MEMBER", dvMember2ID: "MEMBER", dvGuestID: "GUEST",
}

var (
	dvHashOnce sync.Once
	dvHasher   identity.Argon2idHasher
	dvHash     string
	dvHashErr  error
)

type dvEnv struct {
	t         *testing.T
	ctx       context.Context
	url       string
	superuser *pgx.Conn
	pool      *postgres.Pool
	server    *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*dvClient
}

type dvClient struct {
	env  *dvEnv
	http *http.Client
	csrf string
}

type dvResponse struct {
	Status int
	Body   []byte
}

func (r dvResponse) code() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func dvNewID(t *testing.T) string {
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

func dvSuffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(dvNewID(t), "-", "")[:10]
}

func newDvEnv(t *testing.T) *dvEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dvHashOnce.Do(func() {
		dvHasher, dvHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if dvHashErr == nil {
			dvHash, dvHashErr = dvHasher.Hash(dvPassword)
		}
	})
	if dvHashErr != nil {
		t.Fatal(dvHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &dvEnv{t: t, ctx: ctx, url: url, superuser: superuser, org: dvNewID(t), otherOrg: dvNewID(t),
		clients: map[string]*dvClient{}}
	for _, id := range []string{dvOwnerID, dvAdminID, dvMemberID, dvMember2ID, dvGuestID, dvOutsiderID} {
		env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, dvEmail(id))
		env.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, dvHash)
	}
	for _, org := range []string{env.org, env.otherOrg} {
		env.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`,
			org, "dv-"+dvSuffix(t), "Devices "+org[:8])
		// 新组织的 7 个默认状态（创建空间时生成默认任务类型需要）。
		env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4),
			($1::uuid, 'Cancelled', '', 'CANCELLED', 5), ($1::uuid, 'Duplicate', '', 'DUPLICATE', 6)`, org)
	}
	for id, role := range dvRoles {
		env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`,
			env.org, id, role)
	}
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		env.otherOrg, dvOutsiderID)

	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	env.pool = database
	env.server = httptest.NewServer(api.New(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: dvHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false))
	t.Cleanup(env.server.Close)
	return env
}

func dvEmail(id string) string { return "dv-" + id[len(id)-4:] + "@example.test" }

func (e *dvEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

// scalar 以超级用户读取一个标量（绕过 RLS，用于断言数据库状态）。
func (e *dvEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

// client 返回某账号登录后的 HTTP 客户端（按需登录并缓存）。
func (e *dvEnv) client(userID string) *dvClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &dvClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, dvEmail(userID), dvPassword)
	response, err := client.http.Post(e.server.URL+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login %s status = %d", userID, response.StatusCode)
	}
	parsed, err := neturl.Parse(e.server.URL)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			client.csrf = cookie.Value
		}
	}
	if client.csrf == "" {
		e.t.Fatal("login did not set the CSRF cookie")
	}
	e.clients[userID] = client
	return client
}

// do 发送请求；path 以 "/" 开头时直接使用，否则拼接到 org 前缀之后。
func (c *dvClient) do(org, method, path string, body any) dvResponse {
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
	request, err := http.NewRequestWithContext(c.env.ctx, method, c.env.server.URL+path, reader)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
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
	return dvResponse{Status: response.StatusCode, Body: payload}
}

// must 在测试组织里请求并断言状态码，把响应解码到 target（可为 nil）。
func (c *dvClient) must(want int, method, path string, body, target any) dvResponse {
	c.env.t.Helper()
	response := c.do(c.env.org, method, path, body)
	if response.Status != want {
		c.env.t.Fatalf("%s %s = %d %s, want %d", method, path, response.Status, response.Body, want)
	}
	if target != nil {
		if err := json.Unmarshal(response.Body, target); err != nil {
			c.env.t.Fatalf("decode %s: %v", response.Body, err)
		}
	}
	return response
}

func (c *dvClient) expectError(status int, code, method, path string, body any) {
	c.env.t.Helper()
	response := c.do(c.env.org, method, path, body)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
}

// createSpace 由组织 owner 经 API 创建空间（visibility 为 shared / private），owner 自动成为空间成员。
func (e *dvEnv) createSpace(visibility string) string {
	e.t.Helper()
	var space struct {
		ID string `json:"id"`
	}
	e.client(dvOwnerID).must(http.StatusCreated, http.MethodPost, "spaces",
		map[string]any{"name": "Space " + dvSuffix(e.t), "visibility": visibility}, &space)
	return space.ID
}

func (e *dvEnv) addSpaceMember(spaceID, userID, role, status string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = EXCLUDED.status`,
		e.org, spaceID, userID, role, status)
}

// insertDevice 以超级用户写一台属于 ownerID 的在线电脑，并按 scope 授权给测试组织。
// target 是 PERSONAL 的用户或 SPACE 的空间（ORGANIZATION 传空）。
func (e *dvEnv) insertDevice(ownerID, scope, target string) string {
	e.t.Helper()
	id := dvNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at, capabilities)
		VALUES ($1::uuid, $2, 2, $3::uuid, $4, clock_timestamp(), $5::jsonb)`,
		id, "dv-device-"+dvSuffix(e.t), ownerID, "Mac "+dvSuffix(e.t),
		`{"runtimes":{"codex":{"version":"0.50.0","login_status":"LOGGED_IN","runnable":true,"models":[{"id":"gpt-5","display_name":"GPT-5","is_default":true}]}},"timezone":"Asia/Shanghai"}`)
	var userID, spaceID any
	switch scope {
	case "PERSONAL":
		userID = target
	case "SPACE":
		spaceID = target
	}
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id, scope, scope_user_id, scope_space_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5::uuid)`, e.org, id, scope, userID, spaceID)
	return id
}

// insertAgent 以超级用户写一个绑定电脑的受管 Agent，返回 ID。
func (e *dvEnv) insertAgent(spaceID, ownerID, deviceID, visibility string) string {
	e.t.Helper()
	id := dvNewID(e.t)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, 1)`,
		e.org, id, deviceID, spaceID, ownerID, "Agent "+dvSuffix(e.t), visibility)
	return id
}

// insertConversation 写一个对话；visibility 为 PRIVATE / SHARED，members 是 SHARED 对话的 COMMENTER 成员。
// taskID 非空时对话绑定该任务。
func (e *dvEnv) insertConversation(spaceID, agentID, taskID, ownerID, visibility string, members ...string) string {
	e.t.Helper()
	id := dvNewID(e.t)
	e.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, task_id, owner_user_id, visibility)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid, $6::uuid, $7)`,
		e.org, id, spaceID, agentID, taskID, ownerID, visibility)
	for _, member := range members {
		e.exec(`INSERT INTO openfox.conversation_members (organization_id, conversation_id, user_id, access_level)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'COMMENTER')`, e.org, id, member)
	}
	return id
}

// insertRun 写一个指定状态的 Run（QUEUED / RUNNING），空间、Agent、任务取自对话，电脑取自 Agent。
func (e *dvEnv) insertRun(conversationID, actorID, state string) string {
	e.t.Helper()
	id := dvNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state, generation)
		SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.task_id, c.agent_id, a.device_id, g.id, 1, 1, $3::uuid, $4, 1
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE c.organization_id = $1::uuid AND c.id = $5::uuid`,
		e.org, id, actorID, state, conversationID)
	return id
}

// createTask 由组织 owner 经 API 在空间里创建任务，返回任务 ID。
func (e *dvEnv) createTask(spaceID string) string {
	e.t.Helper()
	owner := e.client(dvOwnerID)
	var types struct {
		TaskTypes []struct {
			ID string `json:"id"`
		} `json:"task_types"`
	}
	owner.must(http.StatusOK, http.MethodGet, "spaces/"+spaceID+"/task-types", nil, &types)
	if len(types.TaskTypes) == 0 {
		e.t.Fatal("space has no task type")
	}
	var task struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "spaces/"+spaceID+"/tasks",
		map[string]any{"type_id": types.TaskTypes[0].ID, "title": "Task " + dvSuffix(e.t)}, &task)
	return task.ID
}

// selection 返回成员在测试组织里的租户选择与角色（直接调用服务层时使用）。
func (e *dvEnv) selection(userID string) (tenancy.OrganizationSelection, policy.OrganizationRole) {
	e.t.Helper()
	var version int64
	e.scalar(&version, `SELECT authorization_version FROM openfox.organization_members
		WHERE organization_id = $1::uuid AND user_id = $2::uuid`, e.org, userID)
	return tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: userID, AuthorizationVersion: uint64(version)},
		policy.OrganizationRole(strings.ToLower(dvRoles[userID]))
}

// inOrg 以某成员身份在测试组织的用户请求事务里执行 fn。
func (e *dvEnv) inOrg(userID string, fn func(context.Context, pgx.Tx) error) error {
	e.t.Helper()
	selection, _ := e.selection(userID)
	return e.pool.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}
