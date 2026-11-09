package stage10c

// 任务批量修改的集成测试夹具：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）。
// 每个测试新建两个组织（env.org 与 env.otherOrg），用户 ID 固定、组织与空间随机，可在同一个库上重复运行。
//
// 角色（env.org）：owner 组织 OWNER；member 组织 MEMBER；viewer 组织 MEMBER（在测试空间里是 VIEWER）。

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
	"openfox/internal/dispatch"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

const (
	bcOwnerID    = "00000000-0000-0000-0000-00000010c001"
	bcMemberID   = "00000000-0000-0000-0000-00000010c002"
	bcViewerID   = "00000000-0000-0000-0000-00000010c003"
	bcOutsiderID = "00000000-0000-0000-0000-00000010c004"
	bcPassword   = "stage10c batch update password"
)

var bcRoles = map[string]string{bcOwnerID: "OWNER", bcMemberID: "MEMBER", bcViewerID: "MEMBER"}

var (
	bcHashOnce sync.Once
	bcHasher   identity.Argon2idHasher
	bcHash     string
	bcHashErr  error
)

type bcEnv struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	database  *postgres.Pool
	server    *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*bcClient
}

type bcClient struct {
	env  *bcEnv
	http *http.Client
	csrf string
}

type bcResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r bcResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

type bcError struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details"`
}

func (r bcResponse) apiError() bcError {
	var body struct {
		Error bcError `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error
}

// taskIDs 返回 error.details.task_ids（按出现顺序）。
func (e bcError) taskIDs() []string {
	raw, _ := e.Details["task_ids"].([]any)
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		ids = append(ids, fmt.Sprint(item))
	}
	return ids
}

func bcNewID(t *testing.T) string {
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

func bcSuffix(t *testing.T) string { return strings.ReplaceAll(bcNewID(t), "-", "")[:8] }

func bcPrefix(t *testing.T) string { return "B" + strings.ToUpper(bcSuffix(t)[:6]) }

func bcEmail(id string) string { return "s10c-" + id[len(id)-4:] + "@example.test" }

func newBcEnv(t *testing.T) *bcEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)
	bcHashOnce.Do(func() {
		bcHasher, bcHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if bcHashErr == nil {
			bcHash, bcHashErr = bcHasher.Hash(bcPassword)
		}
	})
	if bcHashErr != nil {
		t.Fatal(bcHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &bcEnv{t: t, ctx: ctx, superuser: superuser, org: bcNewID(t), otherOrg: bcNewID(t), clients: map[string]*bcClient{}}
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
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: bcHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	env.server = httptest.NewServer(api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()},
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	t.Cleanup(env.server.Close)
	return env
}

func (e *bcEnv) seedOrganizations() {
	e.t.Helper()
	for _, id := range []string{bcOwnerID, bcMemberID, bcViewerID, bcOutsiderID} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, bcEmail(id))
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, bcHash)
	}
	for _, org := range []string{e.org, e.otherOrg} {
		e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, "s10c-"+bcSuffix(e.t), "Stage10c "+org[:8])
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2), ($1::uuid, 'Review', '', 'IN_PROGRESS', 3),
			($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Canceled', '', 'CANCELLED', 5)`, org)
		e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4)
			ON CONFLICT DO NOTHING`, org)
	}
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		e.otherOrg, bcOutsiderID)
	for id, role := range bcRoles {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, e.org, id, role)
	}
}

func (e *bcEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *bcEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *bcEnv) count(query string, args ...any) int {
	e.t.Helper()
	var value int
	e.scalar(&value, query, args...)
	return value
}

func (e *bcEnv) client(userID string) *bcClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &bcClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, bcEmail(userID), bcPassword)
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

func (e *bcEnv) owner() *bcClient  { return e.client(bcOwnerID) }
func (e *bcEnv) member() *bcClient { return e.client(bcMemberID) }
func (e *bcEnv) viewer() *bcClient { return e.client(bcViewerID) }

// do 发送 JSON 请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *bcClient) do(method, path string, body any, headers ...string) bcResponse {
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
	return bcResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *bcClient) must(want int, method, path string, body, target any, headers ...string) bcResponse {
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

func spacePath(spaceID string) string        { return "spaces/" + spaceID }
func taskPath(spaceID, taskID string) string { return "spaces/" + spaceID + "/tasks/" + taskID }

type spaceView struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type taskView struct {
	ID         string `json:"id"`
	SpaceID    string `json:"space_id"`
	TypeID     string `json:"type_id"`
	StatusID   string `json:"status_id"`
	Priority   string `json:"priority"`
	Version    int    `json:"version"`
	LabelItems []struct {
		ID string `json:"id"`
	} `json:"label_items"`
	Assignees []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"assignees"`
}

func (t taskView) labelIDs() []string {
	ids := make([]string, 0, len(t.LabelItems))
	for _, item := range t.LabelItems {
		ids = append(ids, item.ID)
	}
	return ids
}

func (t taskView) assigneeKeys() []string {
	keys := make([]string, 0, len(t.Assignees))
	for _, item := range t.Assignees {
		keys = append(keys, item.Kind+":"+item.ID)
	}
	return keys
}

func (e *bcEnv) createSpace() spaceView {
	e.t.Helper()
	var space spaceView
	e.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{
		"name": "Space " + bcSuffix(e.t), "prefix": bcPrefix(e.t), "visibility": "private",
	}, &space)
	return space
}

func (e *bcEnv) addSpaceMember(spaceID, userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ((SELECT organization_id FROM openfox.spaces WHERE id = $1::uuid), $1::uuid, $2::uuid, $3, 'ACTIVE')
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = 'ACTIVE'`,
		spaceID, userID, role)
}

func (e *bcEnv) createTask(spaceID string, body map[string]any) taskView {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Task " + bcSuffix(e.t)
	}
	var task taskView
	e.owner().must(http.StatusCreated, http.MethodPost, spacePath(spaceID)+"/tasks", body, &task)
	return task
}

func (e *bcEnv) createTasks(spaceID string, n int, body map[string]any) []taskView {
	e.t.Helper()
	tasks := make([]taskView, 0, n)
	for index := 0; index < n; index++ {
		copied := map[string]any{}
		for key, value := range body {
			copied[key] = value
		}
		tasks = append(tasks, e.createTask(spaceID, copied))
	}
	return tasks
}

func (e *bcEnv) getTask(spaceID, taskID string) taskView {
	e.t.Helper()
	var task taskView
	e.owner().must(http.StatusOK, http.MethodGet, taskPath(spaceID, taskID), nil, &task)
	return task
}

func (e *bcEnv) statusID(name string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, e.org, name)
	return id
}

func (e *bcEnv) createLabel(name string) string {
	e.t.Helper()
	var label struct {
		ID string `json:"id"`
	}
	e.owner().must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": name + " " + bcSuffix(e.t)}, &label)
	return label.ID
}

// groupLabel 在组织级分组里建一个标签（组织级分组默认单选）。
func (e *bcEnv) groupLabel(groupID, name string) string {
	e.t.Helper()
	var label struct {
		ID string `json:"id"`
	}
	e.owner().must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": name + " " + bcSuffix(e.t), "group_id": groupID}, &label)
	return label.ID
}

// seedAgent 在 spaceID 建一个可指派的 Agent（独立电脑 + 组织授权 + 配置版本 1）。
func (e *bcEnv) seedAgent(spaceID string) string {
	e.t.Helper()
	device, agent := bcNewID(e.t), bcNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`,
		device, "s10c-device-"+bcSuffix(e.t))
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, e.org, device)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, status,
		current_config_version) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'CODE', 'SHARED', 'ACTIVE', 1)`,
		e.org, agent, device, spaceID, bcOwnerID, "Agent "+bcSuffix(e.t))
	e.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, e.org, agent, device, bcOwnerID)
	return agent
}

// dispatchOnce 跑一轮派发（处理指派事件）。
func (e *bcEnv) dispatchOnce() {
	e.t.Helper()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	worker := &dispatch.Worker{Pool: e.database, Store: store, Hub: dispatch.NewHub()}
	if _, err := worker.RunOnce(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// ---- 批量接口 ----

func batchPath(spaceID string) string { return spacePath(spaceID) + "/tasks/batch-update" }

func refs(tasks ...taskView) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, map[string]any{"id": task.ID, "version": task.Version})
	}
	return out
}

type batchResult struct {
	Tasks []taskView `json:"tasks"`
}

func (c *bcClient) batch(spaceID string, tasks []map[string]any, patch map[string]any, headers ...string) bcResponse {
	c.env.t.Helper()
	return c.do(http.MethodPost, batchPath(spaceID), map[string]any{"tasks": tasks, "patch": patch}, headers...)
}

func (c *bcClient) mustBatch(spaceID string, tasks []map[string]any, patch map[string]any, headers ...string) []taskView {
	c.env.t.Helper()
	response := c.batch(spaceID, tasks, patch, headers...)
	if response.Status != http.StatusOK {
		c.env.t.Fatalf("batch %v = %d %s", patch, response.Status, response.Body)
	}
	var result batchResult
	response.decode(c.env.t, &result)
	return result.Tasks
}

func (c *bcClient) expectBatchError(status int, code string, spaceID string, tasks []map[string]any, patch map[string]any) bcError {
	c.env.t.Helper()
	response := c.batch(spaceID, tasks, patch)
	failure := response.apiError()
	if response.Status != status || failure.Code != code {
		c.env.t.Fatalf("batch %v = %d %s, want %d %s", patch, response.Status, response.Body, status, code)
	}
	return failure
}

// activityCount 返回这些任务上指定种类的动态条数。
func (e *bcEnv) activityCount(kind string, tasks ...taskView) int {
	e.t.Helper()
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return e.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = ANY($1::uuid[]) AND kind = $2`, ids, kind)
}

func (e *bcEnv) totalActivities(tasks ...taskView) int {
	e.t.Helper()
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return e.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = ANY($1::uuid[])`, ids)
}
