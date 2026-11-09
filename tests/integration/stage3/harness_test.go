package stage3

// 空间设置与生命周期的集成测试夹具：真实 HTTP 处理器（认证、CSRF、组织选择、策略、租户事务）。
// 每个测试新建自己的两个组织（env.org 与 env.otherOrg），可在同一个库上重复运行（用户 ID 固定，组织与空间随机）。
//
// 角色：
//   - owner：组织 OWNER；admin：组织 ADMIN；
//   - manager：组织 MEMBER，在 env.space 里是空间 OWNER；member：组织 MEMBER，在 env.space 里是空间 MEMBER；
//   - other：组织 MEMBER，不是任何空间的显式成员；guest：组织 GUEST；
//   - outsider：只属于 otherOrg（跨租户）。

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

const (
	s3OwnerID    = "00000000-0000-0000-0000-000000003a01"
	s3AdminID    = "00000000-0000-0000-0000-000000003a02"
	s3ManagerID  = "00000000-0000-0000-0000-000000003a03"
	s3MemberID   = "00000000-0000-0000-0000-000000003a04"
	s3OtherID    = "00000000-0000-0000-0000-000000003a05"
	s3GuestID    = "00000000-0000-0000-0000-000000003a06"
	s3OutsiderID = "00000000-0000-0000-0000-000000003a07"
	s3Password   = "stage3 space settings password"
)

var s3Roles = map[string]string{
	s3OwnerID: "OWNER", s3AdminID: "ADMIN", s3ManagerID: "MEMBER", s3MemberID: "MEMBER", s3OtherID: "MEMBER", s3GuestID: "GUEST",
}

var (
	s3HashOnce sync.Once
	s3Hasher   identity.Argon2idHasher
	s3Hash     string
	s3HashErr  error
)

type s3Env struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	database  *postgres.Pool
	auth      identity.Service
	server    *httptest.Server
	org       string
	otherOrg  string
	clients   map[string]*s3Client
	// space 是 owner 创建的私有空间：manager 为空间 OWNER，member 为空间 MEMBER。
	space SpaceView
	// otherSpace 是 otherOrg 里的空间（跨租户用例）。
	otherSpace SpaceView
}

type s3Client struct {
	env    *s3Env
	http   *http.Client
	csrf   string
	server *httptest.Server
}

type s3Response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r s3Response) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r s3Response) code() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error.Code
}

// SpaceView 是 SpaceDetail 的测试视图（只取用例需要的字段）。
type SpaceView struct {
	ID            string  `json:"id"`
	Prefix        string  `json:"prefix"`
	Name          string  `json:"name"`
	Visibility    string  `json:"visibility"`
	Version       int     `json:"version"`
	Icon          *string `json:"icon"`
	Purpose       string  `json:"purpose"`
	Status        string  `json:"status"`
	RetiredAt     *string `json:"retired_at"`
	Position      *int    `json:"position"`
	MyRole        *string `json:"my_role"`
	CanManage     bool    `json:"can_manage"`
	MemberCount   int     `json:"member_count"`
	TaskTypeCount int     `json:"task_type_count"`
	LabelCount    int     `json:"label_count"`
	MultiAssignee bool    `json:"multi_assignee"`
	Features      struct {
		Views      bool `json:"views"`
		Projects   bool `json:"projects"`
		Iterations bool `json:"iterations"`
		Triage     bool `json:"triage"`
	} `json:"features"`
	Estimation struct {
		Scale            string `json:"scale"`
		Extended         bool   `json:"extended"`
		AllowZero        bool   `json:"allow_zero"`
		CountUnestimated bool   `json:"count_unestimated"`
	} `json:"estimation"`
}

// TaskView 是任务响应的测试视图。
type TaskView struct {
	ID             string   `json:"id"`
	SpaceID        string   `json:"space_id"`
	DisplayID      string   `json:"display_id"`
	Sequence       int      `json:"sequence"`
	TypeID         string   `json:"type_id"`
	TypeName       string   `json:"type_name"`
	StatusID       string   `json:"status_id"`
	StatusCategory string   `json:"status_category"`
	Version        int      `json:"version"`
	Estimate       *float64 `json:"estimate"`
	IterationID    *string  `json:"iteration_id"`
	ParentTaskID   *string  `json:"parent_task_id"`
	// 只有 by-display-id 返回。
	CanonicalDisplayID string `json:"canonical_display_id"`
}

func s3NewID(t *testing.T) string {
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

func s3Suffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(s3NewID(t), "-", "")[:8]
}

// s3Prefix 返回一个随机的新格式标识符（7 位大写字母数字，首位为字母）。
func s3Prefix(t *testing.T) string {
	t.Helper()
	return "S" + strings.ToUpper(s3Suffix(t)[:6])
}

func s3Email(id string) string { return "s3-" + id[len(id)-4:] + "@example.test" }

func newS3Env(t *testing.T) *s3Env {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	s3HashOnce.Do(func() {
		s3Hasher, s3HashErr = identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
		})
		if s3HashErr == nil {
			s3Hash, s3HashErr = s3Hasher.Hash(s3Password)
		}
	})
	if s3HashErr != nil {
		t.Fatal(s3HashErr)
	}
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	env := &s3Env{t: t, ctx: ctx, superuser: superuser, org: s3NewID(t), otherOrg: s3NewID(t), clients: map[string]*s3Client{}}
	for _, id := range []string{s3OwnerID, s3AdminID, s3ManagerID, s3MemberID, s3OtherID, s3GuestID, s3OutsiderID} {
		env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, s3Email(id))
		env.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, s3Hash)
	}
	for _, org := range []struct{ id string }{{env.org}, {env.otherOrg}} {
		env.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`,
			org.id, "s3-"+s3Suffix(t), "Stage3 "+org.id[:8])
		env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2), ($1::uuid, 'Done', '', 'DONE', 3),
			($1::uuid, 'Canceled', '', 'CANCELLED', 4)`, org.id)
	}
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`,
		env.otherOrg, s3OutsiderID)
	for id, role := range s3Roles {
		env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`,
			env.org, id, role)
	}
	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 24, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	env.database = database
	identityService := identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: s3Hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}
	env.auth = identityService
	env.server = httptest.NewServer(api.NewWithDispatch(identityService, database, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{}))
	t.Cleanup(env.server.Close)

	env.space = env.createSpace("private")
	env.addSpaceMember(env.space.ID, s3ManagerID, "OWNER")
	env.addSpaceMember(env.space.ID, s3MemberID, "MEMBER")
	env.otherSpace = env.createSpaceIn(env.otherOrg, s3OutsiderID, map[string]any{"name": "Other " + s3Suffix(t)})
	return env
}

func (e *s3Env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *s3Env) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *s3Env) count(query string, args ...any) int {
	e.t.Helper()
	var value int
	e.scalar(&value, query, args...)
	return value
}

// client 返回 userID 的登录会话（缓存）。
func (e *s3Env) client(userID string) *s3Client {
	e.t.Helper()
	if client, ok := e.clients[userID]; ok {
		return client
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &s3Client{env: e, http: &http.Client{Jar: jar}, server: e.server}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, s3Email(userID), s3Password)
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

func (e *s3Env) owner() *s3Client    { return e.client(s3OwnerID) }
func (e *s3Env) admin() *s3Client    { return e.client(s3AdminID) }
func (e *s3Env) manager() *s3Client  { return e.client(s3ManagerID) }
func (e *s3Env) member() *s3Client   { return e.client(s3MemberID) }
func (e *s3Env) other() *s3Client    { return e.client(s3OtherID) }
func (e *s3Env) guest() *s3Client    { return e.client(s3GuestID) }
func (e *s3Env) outsider() *s3Client { return e.client(s3OutsiderID) }

// do 发送请求；path 不以 "/" 开头时拼到测试组织前缀之后。headers 为键值对。
func (c *s3Client) do(method, path string, body any, headers ...string) s3Response {
	c.env.t.Helper()
	return c.doIn(c.env.org, method, path, body, headers...)
}

func (c *s3Client) doIn(org, method, path string, body any, headers ...string) s3Response {
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
	return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func (c *s3Client) must(want int, method, path string, body, target any, headers ...string) s3Response {
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

func (c *s3Client) expectError(status int, code, method, path string, body any, headers ...string) s3Response {
	c.env.t.Helper()
	response := c.do(method, path, body, headers...)
	if response.Status != status || (code != "" && response.code() != code) {
		c.env.t.Fatalf("%s %s = %d %s, want %d %s", method, path, response.Status, response.Body, status, code)
	}
	return response
}

// ---- 资源 ----

// createSpace 由组织 owner 经 API 创建空间（随机名称与标识符）。
func (e *s3Env) createSpace(visibility string) SpaceView {
	e.t.Helper()
	return e.createSpaceIn(e.org, s3OwnerID, map[string]any{"name": "Space " + s3Suffix(e.t), "visibility": visibility})
}

func (e *s3Env) createSpaceIn(org, userID string, body map[string]any) SpaceView {
	e.t.Helper()
	if _, ok := body["prefix"]; !ok {
		body["prefix"] = s3Prefix(e.t)
	}
	var space SpaceView
	response := e.client(userID).doIn(org, http.MethodPost, "spaces", body)
	if response.Status != http.StatusCreated {
		e.t.Fatalf("create space = %d %s", response.Status, response.Body)
	}
	response.decode(e.t, &space)
	return space
}

func (e *s3Env) addSpaceMember(spaceID, userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ((SELECT organization_id FROM openfox.spaces WHERE id = $1::uuid), $1::uuid, $2::uuid, $3, 'ACTIVE')
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = 'ACTIVE'`,
		spaceID, userID, role)
}

// getSpace 以 client 身份读取空间详情。
func (e *s3Env) getSpace(client *s3Client, spaceID string) SpaceView {
	e.t.Helper()
	var space SpaceView
	client.must(http.StatusOK, http.MethodGet, "spaces/"+spaceID, nil, &space)
	return space
}

// createTask 以 client 身份在空间里建任务。
func (e *s3Env) createTask(client *s3Client, spaceID string, body map[string]any) TaskView {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Task " + s3Suffix(e.t)
	}
	var task TaskView
	client.must(http.StatusCreated, http.MethodPost, "spaces/"+spaceID+"/tasks", body, &task)
	return task
}

// statusID 返回组织里指定分类的状态 ID。
func (e *s3Env) statusID(org, category string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND category = $2
		ORDER BY position LIMIT 1`, org, category)
	return id
}

// parallel 并发执行 n 次 fn，返回每次的状态码。
func parallel(n int, fn func(index int) int) []int {
	statuses := make([]int, n)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < n; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			statuses[index] = fn(index)
		}(index)
	}
	close(start)
	wait.Wait()
	return statuses
}

func countStatus(statuses []int, want int) int {
	total := 0
	for _, status := range statuses {
		if status == want {
			total++
		}
	}
	return total
}
