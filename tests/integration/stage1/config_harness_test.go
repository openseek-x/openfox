package stage1

// 全局配置集成测试的 HTTP 夹具：走真实处理器（认证、CSRF、组织选择、策略、租户事务）。
// 每个测试新建自己的组织（超级用户直接插入组织、成员与 7 个默认状态），互不干扰，可在同一个库上重复运行。

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
)

// 全局配置测试使用的固定账号（不依赖 fixture.sql 的 alice / bob）。
const (
	cfgOwnerID    = "00000000-0000-0000-0000-00000000cf01"
	cfgMemberID   = "00000000-0000-0000-0000-00000000cf02"
	cfgOutsiderID = "00000000-0000-0000-0000-00000000cf03"
	cfgPassword   = "stage1 config password"
)

var cfgUsers = map[string]string{
	cfgOwnerID:    "cfg-owner@example.test",
	cfgMemberID:   "cfg-member@example.test",
	cfgOutsiderID: "cfg-outsider@example.test",
}

var (
	cfgHashOnce sync.Once
	cfgHasher   identity.Argon2idHasher
	cfgHash     string
	cfgHashErr  error
)

type cfgEnv struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	server    *httptest.Server
	// org 是本测试独占的组织（owner = OWNER，member = MEMBER）；otherOrg 归 outsider 所有。
	org      string
	otherOrg string
	owner    *cfgClient
	member   *cfgClient
	outsider *cfgClient
	// statuses 是新组织默认状态，按名称索引（Triage / Backlog / Todo / In Progress / Done / Cancelled / Duplicate）。
	statuses map[string]string
}

type cfgClient struct {
	env  *cfgEnv
	http *http.Client
	csrf string
}

type cfgResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r cfgResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// code 返回错误响应里的 error.code。
func (r cfgResponse) code() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func cfgNewID(t *testing.T) string {
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

func cfgSuffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(cfgNewID(t), "-", "")[:10]
}

func newCfgEnv(t *testing.T) *cfgEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cfgHashOnce.Do(func() {
		cfgHasher, cfgHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if cfgHashErr == nil {
			cfgHash, cfgHashErr = cfgHasher.Hash(cfgPassword)
		}
	})
	if cfgHashErr != nil {
		t.Fatal(cfgHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })

	env := &cfgEnv{t: t, ctx: ctx, superuser: superuser, org: cfgNewID(t), otherOrg: cfgNewID(t),
		statuses: map[string]string{}}
	for id, email := range cfgUsers {
		env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, email)
		env.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, cfgHash)
	}
	for _, org := range []struct{ id, owner string }{{env.org, cfgOwnerID}, {env.otherOrg, cfgOutsiderID}} {
		env.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`,
			org.id, "cfg-"+cfgSuffix(t), "Config "+org.id[:8])
		env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
			org.id, org.owner)
		env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4),
			($1::uuid, 'Cancelled', '', 'CANCELLED', 5), ($1::uuid, 'Duplicate', '', 'DUPLICATE', 6)`, org.id)
	}
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`,
		env.org, cfgMemberID)
	rows, err := superuser.Query(ctx, `SELECT name, id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid`, env.org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			t.Fatal(err)
		}
		env.statuses[name] = id
	}
	rows.Close()

	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	env.server = httptest.NewServer(api.New(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: cfgHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false))
	t.Cleanup(env.server.Close)
	env.owner = env.login(cfgUsers[cfgOwnerID])
	env.member = env.login(cfgUsers[cfgMemberID])
	env.outsider = env.login(cfgUsers[cfgOutsiderID])
	return env
}

func (e *cfgEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

// scalar 以超级用户读取一个标量（绕过 RLS，用于断言数据库状态）。
func (e *cfgEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *cfgEnv) login(email string) *cfgClient {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &cfgClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, cfgPassword)
	response, err := client.http.Post(e.server.URL+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login %s status = %d", email, response.StatusCode)
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
	return client
}

// do 发送请求；path 以 "/" 开头时直接使用，否则拼接到本测试组织前缀之后。
// headers 为键值对（例如 "Idempotency-Key", "k"）。
func (c *cfgClient) do(method, path string, body any, headers ...string) cfgResponse {
	c.env.t.Helper()
	return c.doIn(c.env.org, method, path, body, headers...)
}

func (c *cfgClient) doIn(org, method, path string, body any, headers ...string) cfgResponse {
	c.env.t.Helper()
	if !strings.HasPrefix(path, "/") {
		path = "/v1/organizations/" + org + "/" + path
	}
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
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
	return cfgResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

// must 断言状态码并把响应解码到 target（target 可为 nil）。
func (c *cfgClient) must(want int, method, path string, body, target any, headers ...string) cfgResponse {
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

// expectError 断言状态码与错误码（code 为空时只断言状态码）。
func (c *cfgClient) expectError(status int, code, method, path string, body any, headers ...string) {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
}

// ---- 常用资源 ----

type cfgSpace struct {
	ID     string
	TypeID string
}

// createSpace 由 owner 经 API 创建空间（会自动带一个默认任务类型）。
func (e *cfgEnv) createSpace(visibility string) cfgSpace {
	e.t.Helper()
	var space struct {
		ID string `json:"id"`
	}
	e.owner.must(http.StatusCreated, http.MethodPost, "spaces",
		map[string]any{"name": "Space " + cfgSuffix(e.t), "visibility": visibility}, &space)
	var types struct {
		TaskTypes []struct {
			ID string `json:"id"`
		} `json:"task_types"`
	}
	e.owner.must(http.StatusOK, http.MethodGet, "spaces/"+space.ID+"/task-types", nil, &types)
	if len(types.TaskTypes) == 0 {
		e.t.Fatal("new space has no task type")
	}
	return cfgSpace{ID: space.ID, TypeID: types.TaskTypes[0].ID}
}

// createTask 由 owner 经 API 在空间里创建任务，返回任务 ID。
func (e *cfgEnv) createTask(space cfgSpace, statusID string) string {
	e.t.Helper()
	body := map[string]any{"type_id": space.TypeID, "title": "Task " + cfgSuffix(e.t)}
	if statusID != "" {
		body["status_id"] = statusID
	}
	var task struct {
		ID string `json:"id"`
	}
	e.owner.must(http.StatusCreated, http.MethodPost, "spaces/"+space.ID+"/tasks", body, &task)
	if task.ID == "" {
		e.t.Fatal("created task has no id")
	}
	return task.ID
}

// attachLabel 以超级用户直接写 task_labels（不依赖任务写接口；作用域触发器仍然生效）。
func (e *cfgEnv) attachLabel(space cfgSpace, taskID, labelID string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.task_labels (organization_id, space_id, task_id, label_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`, e.org, space.ID, taskID, labelID)
}

type cfgLabel struct {
	ID            string  `json:"id"`
	SpaceID       *string `json:"space_id"`
	Name          string  `json:"name"`
	Color         *string `json:"color"`
	GroupID       *string `json:"group_id"`
	ArchivedAt    *string `json:"archived_at"`
	TaskCount     int     `json:"task_count"`
	LastAppliedAt *string `json:"last_applied_at"`
	Version       int     `json:"version"`
}

func (e *cfgEnv) createLabel(body map[string]any) cfgLabel {
	e.t.Helper()
	if _, ok := body["name"]; !ok {
		body["name"] = "label-" + cfgSuffix(e.t)
	}
	var label cfgLabel
	e.owner.must(http.StatusCreated, http.MethodPost, "labels", body, &label)
	return label
}

type cfgStatus struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Category           string `json:"category"`
	Position           int    `json:"position"`
	ArchivedAt         string `json:"archived_at"`
	Version            int    `json:"version"`
	IsDefault          bool   `json:"is_default"`
	CollapsedByDefault bool   `json:"collapsed_by_default"`
	TaskCount          int    `json:"task_count"`
	TypeUsage          []struct {
		SpaceID string   `json:"space_id"`
		TypeIDs []string `json:"type_ids"`
	} `json:"type_usage"`
}

func (e *cfgEnv) listStatuses() []cfgStatus {
	e.t.Helper()
	var body struct {
		Statuses []cfgStatus `json:"statuses"`
	}
	e.owner.must(http.StatusOK, http.MethodGet, "task-statuses", nil, &body)
	return body.Statuses
}

func (e *cfgEnv) status(id string) cfgStatus {
	e.t.Helper()
	for _, status := range e.listStatuses() {
		if status.ID == id {
			return status
		}
	}
	e.t.Fatalf("status %s not listed", id)
	return cfgStatus{}
}

func (e *cfgEnv) createStatus(category string) cfgStatus {
	e.t.Helper()
	var status cfgStatus
	e.owner.must(http.StatusCreated, http.MethodPost, "task-statuses",
		map[string]any{"name": "Status " + cfgSuffix(e.t), "category": category}, &status)
	return status
}
