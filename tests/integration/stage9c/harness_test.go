package stage9c

// 任务移动到空间的集成测试夹具：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）+ 本地附件存储。
// 每个测试新建两个组织（env.org 与 env.otherOrg），用户 ID 固定、组织与空间随机，可在同一个库上重复运行。
//
// 角色（env.org）：owner 组织 OWNER；member 组织 MEMBER；guest 组织 GUEST；outsider 只属于 otherOrg。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	mvOwnerID    = "00000000-0000-0000-0000-000000009c01"
	mvMemberID   = "00000000-0000-0000-0000-000000009c02"
	mvGuestID    = "00000000-0000-0000-0000-000000009c03"
	mvOutsiderID = "00000000-0000-0000-0000-000000009c04"
	mvPassword   = "stage9c task move password"
)

var mvRoles = map[string]string{mvOwnerID: "OWNER", mvMemberID: "MEMBER", mvGuestID: "GUEST"}

var (
	mvHashOnce sync.Once
	mvHasher   identity.Argon2idHasher
	mvHash     string
	mvHashErr  error
)

type mvEnv struct {
	t         *testing.T
	ctx       context.Context
	url       string
	superuser *pgx.Conn
	database  *postgres.Pool
	server    *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*mvClient
}

type mvClient struct {
	env  *mvEnv
	http *http.Client
	csrf string
}

type mvResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r mvResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r mvResponse) code() string {
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

func mvNewID(t *testing.T) string {
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

func mvSuffix(t *testing.T) string { return strings.ReplaceAll(mvNewID(t), "-", "")[:8] }

// mvPrefix 返回随机标识符（7 位大写字母数字，首位字母）。
func mvPrefix(t *testing.T) string { return "M" + strings.ToUpper(mvSuffix(t)[:6]) }

func mvEmail(id string) string { return "s9c-" + id[len(id)-4:] + "@example.test" }

func newMvEnv(t *testing.T) *mvEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	mvHashOnce.Do(func() {
		mvHasher, mvHashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if mvHashErr == nil {
			mvHash, mvHashErr = mvHasher.Hash(mvPassword)
		}
	})
	if mvHashErr != nil {
		t.Fatal(mvHashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &mvEnv{t: t, ctx: ctx, url: url, superuser: superuser, org: mvNewID(t), otherOrg: mvNewID(t), clients: map[string]*mvClient{}}
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
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: mvHasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	env.server = httptest.NewServer(api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()},
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	t.Cleanup(env.server.Close)
	return env
}

func (e *mvEnv) seedOrganizations() {
	e.t.Helper()
	for _, id := range []string{mvOwnerID, mvMemberID, mvGuestID, mvOutsiderID} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, mvEmail(id))
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, mvHash)
	}
	for _, org := range []string{e.org, e.otherOrg} {
		e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, "s9c-"+mvSuffix(e.t), "Stage9c "+org[:8])
		// 两个 IN_PROGRESS 状态：用于验证「先试同一状态，再按分类」的映射。
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2), ($1::uuid, 'Review', '', 'IN_PROGRESS', 3),
			($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Canceled', '', 'CANCELLED', 5)`, org)
		e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4)
			ON CONFLICT DO NOTHING`, org)
	}
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		e.otherOrg, mvOutsiderID)
	for id, role := range mvRoles {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, e.org, id, role)
	}
}

func (e *mvEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *mvEnv) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *mvEnv) count(query string, args ...any) int {
	e.t.Helper()
	var value int
	e.scalar(&value, query, args...)
	return value
}

// client 返回 userID 的登录会话（缓存）。
func (e *mvEnv) client(userID string) *mvClient {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &mvClient{env: e, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, mvEmail(userID), mvPassword)
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

func (e *mvEnv) owner() *mvClient    { return e.client(mvOwnerID) }
func (e *mvEnv) member() *mvClient   { return e.client(mvMemberID) }
func (e *mvEnv) guest() *mvClient    { return e.client(mvGuestID) }
func (e *mvEnv) outsider() *mvClient { return e.client(mvOutsiderID) }

// do 发送 JSON 请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *mvClient) do(method, path string, body any, headers ...string) mvResponse {
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

func (c *mvClient) raw(method, path string, reader io.Reader, contentType string, headers ...string) mvResponse {
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
	return mvResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *mvClient) must(want int, method, path string, body, target any, headers ...string) mvResponse {
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

func (c *mvClient) expectError(status int, code, method, path string, body any, headers ...string) mvResponse {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
	return response
}

// upload 以 multipart 上传一个文件到任务附件。
func (c *mvClient) upload(spaceID, taskID, name string, data []byte) mvResponse {
	c.env.t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		c.env.t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		c.env.t.Fatal(err)
	}
	return c.raw(http.MethodPost, taskPath(spaceID, taskID)+"/attachments", &buffer, writer.FormDataContentType())
}

func spacePath(spaceID string) string        { return "spaces/" + spaceID }
func taskPath(spaceID, taskID string) string { return "spaces/" + spaceID + "/tasks/" + taskID }

// ---- 资源 ----

type spaceView struct {
	ID      string `json:"id"`
	Prefix  string `json:"prefix"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type taskView struct {
	ID                 string   `json:"id"`
	SpaceID            string   `json:"space_id"`
	DisplayID          string   `json:"display_id"`
	Sequence           int      `json:"sequence"`
	TypeID             string   `json:"type_id"`
	TypeName           string   `json:"type_name"`
	StatusID           string   `json:"status_id"`
	StatusName         string   `json:"status_name"`
	StatusCategory     string   `json:"status_category"`
	Version            int      `json:"version"`
	Estimate           *float64 `json:"estimate"`
	IterationID        *string  `json:"iteration_id"`
	ProjectID          *string  `json:"project_id"`
	ParentTaskID       *string  `json:"parent_task_id"`
	CanonicalDisplayID string   `json:"canonical_display_id"`
	Assignees          []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"assignees"`
}

// createSpace 由 owner 经 API 建空间（随机名称与标识符；body 可覆盖）。
func (e *mvEnv) createSpace(body map[string]any) spaceView {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["name"]; !ok {
		body["name"] = "Space " + mvSuffix(e.t)
	}
	if _, ok := body["prefix"]; !ok {
		body["prefix"] = mvPrefix(e.t)
	}
	if _, ok := body["visibility"]; !ok {
		body["visibility"] = "private"
	}
	var space spaceView
	e.owner().must(http.StatusCreated, http.MethodPost, "spaces", body, &space)
	return space
}

func (e *mvEnv) addSpaceMember(spaceID, userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ((SELECT organization_id FROM openfox.spaces WHERE id = $1::uuid), $1::uuid, $2::uuid, $3, 'ACTIVE')
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = 'ACTIVE'`,
		spaceID, userID, role)
}

func (e *mvEnv) createTask(client *mvClient, spaceID string, body map[string]any) taskView {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Task " + mvSuffix(e.t)
	}
	var task taskView
	client.must(http.StatusCreated, http.MethodPost, spacePath(spaceID)+"/tasks", body, &task)
	return task
}

func (e *mvEnv) getTask(client *mvClient, spaceID, taskID string) taskView {
	e.t.Helper()
	var task taskView
	client.must(http.StatusOK, http.MethodGet, taskPath(spaceID, taskID), nil, &task)
	return task
}

// statusID 返回组织里指定名称的状态 ID。
func (e *mvEnv) statusID(name string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, e.org, name)
	return id
}

// insertGitLink 直接写一条 PR 关联（模拟 Webhook 已处理过）。
func (e *mvEnv) insertGitLink(task taskView) {
	e.t.Helper()
	connection := e.gitConnection()
	e.exec(`INSERT INTO openfox.task_git_links (organization_id, space_id, task_id, connection_id, provider, kind, repository,
		number, url, state) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'github', 'PULL_REQUEST', 'acme/repo', $5,
		'https://github.example.test/acme/repo/pull/1', 'OPEN')`, e.org, task.SpaceID, task.ID, connection, task.Sequence)
}

var mvConnections = map[string]string{}

// gitConnection 返回（按需创建）测试组织的 GitHub 组织连接。
func (e *mvEnv) gitConnection() string {
	e.t.Helper()
	if id, ok := mvConnections[e.org]; ok {
		return id
	}
	var id string
	e.scalar(&id, `INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name,
		account_kind, created_by) VALUES ($1::uuid, 'github', 'ORGANIZATION', $2, 'acme', 'organization', $3::uuid)
		RETURNING id::text`, e.org, "acme-"+mvSuffix(e.t), mvOwnerID)
	mvConnections[e.org] = id
	return id
}

// insertIssueLink 给任务所在空间建一条双向同步，并写一条 Issue 链接与一条待发送的 ISSUE_UPDATE。返回 Issue 链接 ID。
func (e *mvEnv) insertIssueLink(task taskView) string {
	e.t.Helper()
	var syncLink, issueLink string
	e.scalar(&syncLink, `INSERT INTO openfox.integration_sync_links (organization_id, connection_id, space_id, external_project_id,
		external_project_name, direction, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'acme/issues', 'BIDIRECTIONAL', $5::uuid)
		RETURNING id::text`, e.org, e.gitConnection(), task.SpaceID, "acme/issues-"+mvSuffix(e.t), mvOwnerID)
	e.scalar(&issueLink, `INSERT INTO openfox.task_issue_links (organization_id, sync_link_id, space_id, task_id, repository,
		issue_number, url, state) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'acme/issues', $5,
		'https://github.example.test/acme/issues/issues/1', 'OPEN') RETURNING id::text`,
		e.org, syncLink, task.SpaceID, task.ID, task.Sequence)
	e.exec(`INSERT INTO openfox.integration_sync_outbox (organization_id, sync_link_id, task_id, kind)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'ISSUE_UPDATE')`, e.org, syncLink, task.ID)
	return issueLink
}

var testPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1}, 64)...)

// uploadAttachment 上传一张 PNG，返回附件 ID。
func (e *mvEnv) uploadAttachment(client *mvClient, task taskView) string {
	e.t.Helper()
	response := client.upload(task.SpaceID, task.ID, "shot.png", testPNG)
	if response.Status != http.StatusCreated {
		e.t.Fatalf("upload = %d %s", response.Status, response.Body)
	}
	var item struct {
		ID string `json:"id"`
	}
	response.decode(e.t, &item)
	return item.ID
}
