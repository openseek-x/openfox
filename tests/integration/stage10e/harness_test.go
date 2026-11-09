package stage10e

// 任务回收站集成测试夹具：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）。
// 每个测试新建两个组织（env.org 与 env.otherOrg），用户 ID 固定、组织与空间随机，可在同一个库上重复运行。
//
// 角色（env.org）：owner 组织 OWNER；member / member2 / spaceAdmin / visitor 组织 MEMBER（空间角色由测试按需加入）；
// guest 组织 GUEST；outsider 只属于 otherOrg。

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
	trOwnerID      = "00000000-0000-0000-0000-000000010e01"
	trMemberID     = "00000000-0000-0000-0000-000000010e02"
	trMember2ID    = "00000000-0000-0000-0000-000000010e03"
	trSpaceAdminID = "00000000-0000-0000-0000-000000010e04"
	trVisitorID    = "00000000-0000-0000-0000-000000010e05"
	trGuestID      = "00000000-0000-0000-0000-000000010e06"
	trOutsiderID   = "00000000-0000-0000-0000-000000010e07"
	trPassword     = "stage10e task trash password"
)

var trRoles = map[string]string{
	trOwnerID: "OWNER", trMemberID: "MEMBER", trMember2ID: "MEMBER", trSpaceAdminID: "MEMBER",
	trVisitorID: "MEMBER", trGuestID: "GUEST",
}

var (
	trHashOnce sync.Once
	trHasher   identity.Argon2idHasher
	trHash     string
	trHashErr  error
)

type trEnv struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	server    *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*trClient
}

type trClient struct {
	env  *trEnv
	http *http.Client
	csrf string
}

type trResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r trResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r trResponse) code() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func trNewID(t *testing.T) string {
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

func trSuffix(t *testing.T) string { return strings.ReplaceAll(trNewID(t), "-", "")[:8] }

// trPrefix 返回随机标识符（7 位大写字母数字，首位字母）。
func trPrefix(t *testing.T) string { return "R" + strings.ToUpper(trSuffix(t)[:6]) }

func trEmail(id string) string { return "s10e-" + id[len(id)-4:] + "@example.test" }

func newTrEnv(t *testing.T) *trEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	trHashOnce.Do(func() {
		trHasher, trHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if trHashErr == nil {
			trHash, trHashErr = trHasher.Hash(trPassword)
		}
	})
	if trHashErr != nil {
		t.Fatal(trHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &trEnv{t: t, ctx: ctx, superuser: superuser, org: trNewID(t), otherOrg: trNewID(t), clients: map[string]*trClient{}}
	env.seedOrganizations()
	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 16, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: trHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	env.server = httptest.NewServer(api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()},
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	t.Cleanup(env.server.Close)
	return env
}

func (e *trEnv) seedOrganizations() {
	e.t.Helper()
	for _, id := range []string{trOwnerID, trMemberID, trMember2ID, trSpaceAdminID, trVisitorID, trGuestID, trOutsiderID} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, trEmail(id))
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, trHash)
	}
	// 删除人名称：member 填了姓名，其他人用邮箱。
	e.exec(`UPDATE openfox.users SET full_name = 'Member One' WHERE id = $1::uuid`, trMemberID)
	for _, org := range []string{e.org, e.otherOrg} {
		e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, "s10e-"+trSuffix(e.t), "Stage10e "+org[:8])
		// 两个 IN_PROGRESS 状态：用于验证类型归档后「先试同一状态，再按分类」的映射。
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2), ($1::uuid, 'Review', '', 'IN_PROGRESS', 3),
			($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Canceled', '', 'CANCELLED', 5)`, org)
		e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4)
			ON CONFLICT DO NOTHING`, org)
	}
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		e.otherOrg, trOutsiderID)
	for id, role := range trRoles {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, e.org, id, role)
	}
}

func (e *trEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *trEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *trEnv) count(query string, args ...any) int {
	e.t.Helper()
	var value int
	e.scalar(&value, query, args...)
	return value
}

// client 返回 userID 的登录会话（缓存）。
func (e *trEnv) client(userID string) *trClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &trClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, trEmail(userID), trPassword)
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

func (e *trEnv) owner() *trClient      { return e.client(trOwnerID) }
func (e *trEnv) member() *trClient     { return e.client(trMemberID) }
func (e *trEnv) member2() *trClient    { return e.client(trMember2ID) }
func (e *trEnv) spaceAdmin() *trClient { return e.client(trSpaceAdminID) }
func (e *trEnv) visitor() *trClient    { return e.client(trVisitorID) }
func (e *trEnv) guest() *trClient      { return e.client(trGuestID) }
func (e *trEnv) outsider() *trClient   { return e.client(trOutsiderID) }

// do 发送 JSON 请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *trClient) do(method, path string, body any, headers ...string) trResponse {
	c.env.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.env.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/v1/organizations/" + c.env.org + "/" + path
	}
	request, err := http.NewRequestWithContext(c.env.ctx, method, c.env.server.URL+path, reader)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if body != nil {
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
	return trResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *trClient) must(want int, method, path string, body, target any, headers ...string) trResponse {
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

func (c *trClient) expectError(status int, code, method, path string, body any, headers ...string) trResponse {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
	return response
}

func spacePath(spaceID string) string        { return "spaces/" + spaceID }
func taskPath(spaceID, taskID string) string { return "spaces/" + spaceID + "/tasks/" + taskID }

// ---- 资源 ----

type spaceView struct {
	ID      string `json:"id"`
	Prefix  string `json:"prefix"`
	Version int    `json:"version"`
}

type taskView struct {
	ID                 string  `json:"id"`
	SpaceID            string  `json:"space_id"`
	DisplayID          string  `json:"display_id"`
	Sequence           int     `json:"sequence"`
	TypeID             string  `json:"type_id"`
	TypeName           string  `json:"type_name"`
	StatusID           string  `json:"status_id"`
	StatusName         string  `json:"status_name"`
	Version            int     `json:"version"`
	ParentTaskID       *string `json:"parent_task_id"`
	CanonicalDisplayID string  `json:"canonical_display_id"`
}

type deletedTaskView struct {
	ID             string  `json:"id"`
	SpaceID        string  `json:"space_id"`
	DisplayID      string  `json:"display_id"`
	Title          string  `json:"title"`
	TypeName       string  `json:"type_name"`
	StatusName     string  `json:"status_name"`
	StatusCategory string  `json:"status_category"`
	DeletedAt      string  `json:"deleted_at"`
	Version        int     `json:"version"`
	TypeIcon       *string `json:"type_icon"`
	DeletedBy      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"deleted_by"`
}

type trashPage struct {
	Tasks      []deletedTaskView `json:"tasks"`
	NextCursor string            `json:"next_cursor"`
	Scope      string            `json:"scope"`
}

// createSpace 由 owner 经 API 建私有空间（随机名称与标识符）。
func (e *trEnv) createSpace() spaceView {
	e.t.Helper()
	var space spaceView
	e.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{
		"name": "Space " + trSuffix(e.t), "prefix": trPrefix(e.t), "visibility": "private",
	}, &space)
	return space
}

func (e *trEnv) addSpaceMember(spaceID, userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ((SELECT organization_id FROM openfox.spaces WHERE id = $1::uuid), $1::uuid, $2::uuid, $3, 'ACTIVE')
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = 'ACTIVE'`,
		spaceID, userID, role)
}

// newTrashSpace 建一个私有空间：member / member2 为成员，spaceAdmin 为空间管理员，visitor 为访客。
func (e *trEnv) newTrashSpace() spaceView {
	e.t.Helper()
	space := e.createSpace()
	e.addSpaceMember(space.ID, trMemberID, "MEMBER")
	e.addSpaceMember(space.ID, trMember2ID, "MEMBER")
	e.addSpaceMember(space.ID, trSpaceAdminID, "OWNER")
	e.addSpaceMember(space.ID, trVisitorID, "VIEWER")
	return space
}

func (e *trEnv) createTask(client *trClient, spaceID string, body map[string]any) taskView {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Task " + trSuffix(e.t)
	}
	var task taskView
	client.must(http.StatusCreated, http.MethodPost, spacePath(spaceID)+"/tasks", body, &task)
	return task
}

func (e *trEnv) getTask(client *trClient, spaceID, taskID string) taskView {
	e.t.Helper()
	var task taskView
	client.must(http.StatusOK, http.MethodGet, taskPath(spaceID, taskID), nil, &task)
	return task
}

// deleteTask 删除任务，返回删除后的版本（请求版本 + 1）。
func (e *trEnv) deleteTask(client *trClient, task taskView) int {
	e.t.Helper()
	client.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s?version=%d", taskPath(task.SpaceID, task.ID), task.Version), nil, nil)
	return task.Version + 1
}

func (e *trEnv) trash(client *trClient, spaceID, query string) trashPage {
	e.t.Helper()
	var page trashPage
	path := spacePath(spaceID) + "/deleted-tasks"
	if query != "" {
		path += "?" + query
	}
	client.must(http.StatusOK, http.MethodGet, path, nil, &page)
	return page
}

func (e *trEnv) restore(client *trClient, spaceID, taskID string, version int, headers ...string) trResponse {
	e.t.Helper()
	return client.do(http.MethodPost, taskPath(spaceID, taskID)+"/restore", map[string]any{"version": version}, headers...)
}

func (e *trEnv) mustRestore(client *trClient, spaceID, taskID string, version int) taskView {
	e.t.Helper()
	response := e.restore(client, spaceID, taskID, version)
	if response.Status != http.StatusOK {
		e.t.Fatalf("restore %s = %d %s", taskID, response.Status, response.Body)
	}
	var task taskView
	response.decode(e.t, &task)
	return task
}

// statusID 返回组织里指定名称的状态 ID。
func (e *trEnv) statusID(name string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, e.org, name)
	return id
}

func ids(page trashPage) []string {
	result := make([]string, 0, len(page.Tasks))
	for _, item := range page.Tasks {
		result = append(result, item.ID)
	}
	return result
}
