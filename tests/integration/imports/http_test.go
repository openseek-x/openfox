package imports

// 走真实 HTTP 处理器（登录会话、CSRF、组织选择、空间授权、租户事务）验证导入接口：
// 上传（multipart）→ 保存配置 → 开始 → 列表；成员上传 403；超限 413；坏文件 400；草稿版本冲突 409。

import (
	"bytes"
	"context"
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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

const (
	httpOwner    = "00000000-0000-0000-0000-00000000e601"
	httpMember   = "00000000-0000-0000-0000-00000000e602"
	httpPassword = "imports http password"
)

type httpClient struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func TestImportsHTTP(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer superuser.Close(context.Background())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := superuser.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %s: %v", query, err)
		}
	}
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
		MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(httpPassword)
	if err != nil {
		t.Fatal(err)
	}
	var org string
	if err := superuser.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	for id, email := range map[string]string{httpOwner: "imp-http-owner@example.test", httpMember: "imp-http-member@example.test"} {
		exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, email)
		exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, hash)
	}
	exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Imports HTTP')`, org, "imp-http-"+org[:8])
	exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'MEMBER')`, org, httpOwner, httpMember)
	exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
		($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1), ($1::uuid, 'Done', '', 'DONE', 2)`, org)

	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false))
	defer server.Close()
	owner := httpLogin(t, server.URL, "imp-http-owner@example.test")
	member := httpLogin(t, server.URL, "imp-http-member@example.test")
	prefix := "/v1/organizations/" + org

	var space struct {
		ID string `json:"id"`
	}
	owner.json(http.StatusCreated, http.MethodPost, prefix+"/spaces", map[string]any{"name": "Imports", "visibility": "SHARED"}, &space)

	csv := "Summary,Issue key,Issue id,Status\nFirst,IMP-1,9001,Todo\nSecond,IMP-2,9002,Done\n"

	// 成员不能上传；坏文件 400；超限 413。
	if status, body := member.upload(prefix+"/imports/jira-csv", space.ID, []byte(csv)); status != http.StatusForbidden {
		t.Fatalf("member upload = %d %s", status, body)
	}
	if status, body := owner.upload(prefix+"/imports/jira-csv", space.ID, []byte{0xff, 0xfe, 0x00}); status != http.StatusBadRequest ||
		!strings.Contains(body, "INVALID_IMPORT_FILE") {
		t.Fatalf("bad file upload = %d %s", status, body)
	}
	if status, body := owner.upload(prefix+"/imports/jira-csv", space.ID, bytes.Repeat([]byte("a"), 10<<20+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("large upload = %d %s", status, body[:min(len(body), 200)])
	}

	// 上传 → 保存配置（版本冲突 409）→ 开始 → 列表可见。
	status, body := owner.upload(prefix+"/imports/jira-csv", space.ID, []byte(csv))
	if status != http.StatusCreated {
		t.Fatalf("owner upload = %d %s", status, body)
	}
	var draft struct {
		ID            string          `json:"id"`
		Status        string          `json:"status"`
		ConfigVersion int             `json:"config_version"`
		Config        json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal([]byte(body), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Status != "DRAFT" || draft.ConfigVersion != 1 {
		t.Fatalf("draft = %+v", draft)
	}
	owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, prefix+"/imports/"+draft.ID+"/config",
		map[string]any{"config_version": 9, "config": draft.Config})
	var saved struct {
		ConfigVersion int `json:"config_version"`
	}
	owner.json(http.StatusOK, http.MethodPut, prefix+"/imports/"+draft.ID+"/config",
		map[string]any{"config_version": 1, "config": draft.Config}, &saved)
	member.expect(http.StatusForbidden, "", http.MethodPost, prefix+"/imports/"+draft.ID+"/start",
		map[string]any{"config_version": saved.ConfigVersion})
	var started struct {
		Status string `json:"status"`
	}
	owner.json(http.StatusOK, http.MethodPost, prefix+"/imports/"+draft.ID+"/start",
		map[string]any{"config_version": saved.ConfigVersion}, &started)
	if started.Status != "QUEUED" {
		t.Fatalf("started status = %s", started.Status)
	}
	var list struct {
		Imports []struct {
			ID string `json:"id"`
		} `json:"imports"`
		HasActive bool `json:"has_active"`
		CanManage bool `json:"can_manage"`
	}
	owner.json(http.StatusOK, http.MethodGet, prefix+"/imports", nil, &list)
	if len(list.Imports) != 1 || list.Imports[0].ID != draft.ID || !list.HasActive || !list.CanManage {
		t.Fatalf("owner list = %+v", list)
	}
	member.json(http.StatusOK, http.MethodGet, prefix+"/imports", nil, &list)
	if len(list.Imports) != 1 || list.CanManage {
		t.Fatalf("member list = %+v", list)
	}
	// 执行中取消不校验版本（每批都会递增 version）。
	owner.json(http.StatusOK, http.MethodPost, prefix+"/imports/"+draft.ID+"/cancel", map[string]any{"version": 1}, &started)
	if started.Status != "CANCELLED" {
		t.Fatalf("cancel status = %s", started.Status)
	}
}

func httpLogin(t *testing.T, base, email string) *httpClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &httpClient{t: t, base: base, client: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, httpPassword)
	response, err := c.client.Post(base+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login %s = %d", email, response.StatusCode)
	}
	parsed, _ := neturl.Parse(base)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			c.csrf = cookie.Value
		}
	}
	if c.csrf == "" {
		t.Fatal("login did not set the CSRF cookie")
	}
	return c
}

func (c *httpClient) send(request *http.Request) (int, string) {
	c.t.Helper()
	if request.Method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(payload)
}

func (c *httpClient) do(method, path string, body any) (int, string) {
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
	return c.send(request)
}

func (c *httpClient) json(want int, method, path string, body, target any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *httpClient) expect(want int, code, method, path string, body any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want || (code != "" && !strings.Contains(payload, `"`+code+`"`)) {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, payload, want, code)
	}
}

func (c *httpClient) upload(path, spaceID string, content []byte) (int, string) {
	c.t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	_ = writer.WriteField("space_id", spaceID)
	part, err := writer.CreateFormFile("file", "jira.csv")
	if err != nil {
		c.t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = writer.Close()
	request, err := http.NewRequest(http.MethodPost, c.base+path, &buffer)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return c.send(request)
}
