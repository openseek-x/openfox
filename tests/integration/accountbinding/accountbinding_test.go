package accountbinding

// 第三方账号绑定页：列表与 ready 条件、个人 Jira 绑定、外部账号关联、导入衔接。
// 本机 httptest TLS 服务模拟 Jira，经 safehttp 注入放行 127.0.0.1 并信任测试证书；全程走真实 HTTP 处理器。

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const (
	uOwner = "00000000-0000-0000-0000-00000000a701"
	uM1    = "00000000-0000-0000-0000-00000000a702"
	uM2    = "00000000-0000-0000-0000-00000000a703"
	uM3    = "00000000-0000-0000-0000-00000000a704"
	uGuest = "00000000-0000-0000-0000-00000000a705"
)

type fakeUser struct{ id, name, email string }

// ---------- 模拟 Jira（cloud） ----------

type fakeJira struct {
	srv      *httptest.Server
	myself   atomic.Int64 // /myself 被调用次数
	tokens   map[string]fakeUser
	rejected atomic.Int64
}

func newFakeJira(t *testing.T) *fakeJira {
	f := &fakeJira{tokens: map[string]fakeUser{
		"tok-admin":     {"admin", "Admin", "admin@jira.test"},
		"tok-m1":        {"acc-m1", "M One", "BIND-M1@example.test"}, // 与 OpenFox 邮箱只差大小写
		"tok-m1-hidden": {"acc-m1", "M One", ""},
		"tok-m2":        {"acc-m2", "M Two", "bind-m2@example.test"},
		"tok-m3":        {"acc-m3", "M Three", "bind-m3@example.test"},
		"tok-mismatch":  {"acc-mm", "Other", "other@jira.test"},
	}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) safe() safehttp.Options {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	return safehttp.Options{RootCAs: pool, AllowAddr: func(net.IP) bool { return true }}
}

func (f *fakeJira) token(r *http.Request) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Basic "))
	if err != nil {
		return ""
	}
	s := string(raw)
	return s[strings.LastIndex(s, ":")+1:]
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeJira) issue(project string, n int, assignee string) map[string]any {
	return map[string]any{"id": fmt.Sprintf("%d", 80000+n), "key": fmt.Sprintf("%s-%d", project, n), "fields": map[string]any{
		"summary": fmt.Sprintf("Issue %d", n), "description": "d", "labels": []string{},
		"status":    map[string]any{"name": "To Do", "statusCategory": map[string]any{"key": "new"}},
		"issuetype": map[string]any{"name": "Task"}, "priority": map[string]any{"name": "High"},
		"created": "2026-01-02T03:04:05.000+0800", "updated": "2026-01-03T03:04:05.000+0800",
		"assignee": map[string]any{"accountId": assignee, "displayName": assignee},
	}}
}

func (f *fakeJira) serve(w http.ResponseWriter, r *http.Request) {
	tok := f.token(r)
	if r.URL.Path == "/rest/api/2/myself" {
		f.myself.Add(1)
		if tok == "tok-500" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		u, ok := f.tokens[tok]
		if !ok {
			f.rejected.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write(w, 200, map[string]any{"accountId": u.id, "displayName": u.name, "emailAddress": u.email})
		return
	}
	if _, ok := f.tokens[tok]; !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/rest/api/2/project/"):
		key := strings.TrimPrefix(r.URL.Path, "/rest/api/2/project/")
		write(w, 200, map[string]any{"id": "1", "key": key, "name": key})
	case r.URL.Path == "/rest/api/2/field":
		write(w, 200, []any{})
	case r.URL.Path == "/rest/api/2/search/approximate-count":
		write(w, 200, map[string]any{"count": f.issues(r.URL.Query().Get("jql"))})
	case r.URL.Path == "/rest/api/2/search/jql" || r.URL.Path == "/rest/api/2/search":
		project, assignees := f.dataset(r.URL.Query().Get("jql"))
		issues := []any{}
		for i, a := range assignees {
			issues = append(issues, f.issue(project, i+1, a))
		}
		write(w, 200, map[string]any{"issues": issues})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeJira) dataset(jql string) (string, []string) {
	if strings.Contains(jql, "PR2") {
		return "PR2", []string{"acc-m3", "acc-m1"}
	}
	return "PRJ", []string{"acc-m1", "acc-m1", "acc-m2", "acc-other"}
}

func (f *fakeJira) issues(jql string) int { _, a := f.dataset(jql); return len(a) }

// ---------- 环境 ----------

type env struct {
	t      *testing.T
	ctx    context.Context
	super  *pgx.Conn
	org    string
	prefix string
	jira   *fakeJira
	worker *imports.Worker
	owner  *httpClient
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.super.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *env) str(query string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.super.QueryRow(e.ctx, query, args...).Scan(&v); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func (e *env) num(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.super.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
	return n
}

type bindingDoc struct {
	ConnectionID string `json:"connection_id"`
	DisplayName  string `json:"display_name"`
	Detail       string `json:"detail"`
	Version      int64  `json:"version"`
}

type viewDoc struct {
	Email     string `json:"email"`
	JiraSites []struct {
		SiteURL    string `json:"site_url"`
		Deployment string `json:"deployment"`
		Host       string `json:"host"`
	} `json:"jira_sites"`
	Providers []struct {
		ID                string      `json:"id"`
		Name              string      `json:"name"`
		Availability      string      `json:"availability"`
		Method            string      `json:"method"`
		OrganizationReady bool        `json:"organization_ready"`
		Binding           *bindingDoc `json:"binding"`
		Instances         []struct {
			ConnectionID string      `json:"connection_id"`
			Host         string      `json:"host"`
			DisplayName  string      `json:"display_name"`
			Binding      *bindingDoc `json:"binding"`
		} `json:"instances"`
	} `json:"providers"`
}

func (v viewDoc) provider(id string) (ready bool, binding *bindingDoc) {
	for _, p := range v.Providers {
		if p.ID == id {
			return p.OrganizationReady, p.Binding
		}
	}
	panic("provider not found: " + id)
}

type bindResult struct {
	Binding         bindingDoc `json:"binding"`
	LinkedAccounts  int        `json:"linked_accounts"`
	ReassignedTasks int        `json:"reassigned_tasks"`
	SkippedAccounts int        `json:"skipped_accounts"`
}

type jobDoc struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Phase         string `json:"phase"`
	ConfigVersion int64  `json:"config_version"`
	Analysis      *struct {
		Users []struct {
			SourceID        string  `json:"source_id"`
			SuggestedUserID *string `json:"suggested_user_id"`
			SuggestReason   string  `json:"suggest_reason"`
		} `json:"users"`
	} `json:"analysis"`
}

// runImport 创建 Jira API 导入、拉取、保存默认配置（来源用户全部作为外部账号）并开始，返回草稿分析与完成后的任务。
func (e *env) runImport(project, spaceName string) jobDoc {
	e.t.Helper()
	var draft jobDoc
	e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/imports/jira-api", map[string]any{
		"deployment": "cloud", "site_url": e.jira.srv.URL, "project_key": project,
		"auth":      map[string]any{"kind": "api_token", "email": "admin@jira.test", "secret": "tok-admin"},
		"new_space": map[string]any{"name": spaceName, "visibility": "SHARED"}}, &draft)
	var job jobDoc
	for i := 0; i < 60; i++ {
		if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatalf("worker: %v", err)
		}
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+draft.ID, nil, &job)
		if job.Phase == "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if job.Phase != "" || job.Analysis == nil {
		e.t.Fatalf("拉取未完成: %+v", job)
	}
	analysis := job
	var saved jobDoc
	e.owner.json(http.StatusOK, http.MethodPut, e.prefix+"/imports/"+draft.ID+"/config", map[string]any{"config_version": job.ConfigVersion,
		"config": map[string]any{"columns": []any{}, "statuses": []any{}, "users": []any{},
			"options": map[string]any{"import_open": true, "import_done": true}}}, &saved)
	e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+draft.ID+"/start", map[string]any{"config_version": saved.ConfigVersion}, nil)
	for i := 0; i < 80; i++ {
		if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatalf("worker: %v", err)
		}
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+draft.ID, nil, &job)
		if job.Status == "SUCCEEDED" || job.Status == "FAILED" {
			break
		}
	}
	if job.Status != "SUCCEEDED" {
		e.t.Fatalf("导入未成功: %+v", job)
	}
	return analysis
}

func TestAccountBindings(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	super, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(context.Background())
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, super: super, jira: newFakeJira(t)}
	var org2 string
	if err := super.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&e.org, &org2); err != nil {
		t.Fatal(err)
	}
	for id, email := range map[string]string{uOwner: "bind-owner@example.test", uM1: "bind-m1@example.test", uM2: "bind-m2@example.test",
		uM3: "bind-m3@example.test", uGuest: "bind-guest@example.test"} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, email)
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, hash)
	}
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Bindings'), ($3::uuid, $4, 'Other')`,
		e.org, "bind-"+e.org[:8], org2, "bind-"+org2[:8])
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'MEMBER'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'MEMBER'),
		($1::uuid, $6::uuid, 'GUEST'), ($7::uuid, $3::uuid, 'OWNER')`, e.org, uOwner, uM1, uM2, uM3, uGuest, org2)
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position, is_default) VALUES
		($1::uuid, 'To Do', '', 'TODO', 0, true), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 1, false), ($1::uuid, 'Done', '', 'DONE', 2, false)`, e.org)
	e.prefix = "/v1/organizations/" + e.org

	newServer := func(githubConfigured bool) *httptest.Server {
		srv := httptest.NewServer(api.NewWithDispatch(identity.Service{
			Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
			SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
		}, pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, ImportSafeHTTP: e.jira.safe(), GitHubConfigured: githubConfigured, GiteeConfigured: githubConfigured}))
		t.Cleanup(srv.Close)
		return srv
	}
	srv := newServer(false)
	srvGH := newServer(true)
	e.owner = httpLogin(t, srv.URL, "bind-owner@example.test")
	m1 := httpLogin(t, srv.URL, "bind-m1@example.test")
	m2 := httpLogin(t, srv.URL, "bind-m2@example.test")
	m3 := httpLogin(t, srv.URL, "bind-m3@example.test")
	guest := httpLogin(t, srv.URL, "bind-guest@example.test")
	m1GH := httpLogin(t, srvGH.URL, "bind-m1@example.test")
	e.worker = &imports.Worker{Pool: pool, Instance: "ab-w1", Secrets: box, Safe: e.jira.safe(), FetchBackoff: 20 * time.Millisecond}
	bindURL := e.prefix + "/me/account-bindings"
	jiraURL := bindURL + "/jira"
	body := func(token, email string) map[string]any {
		return map[string]any{"site_url": e.jira.srv.URL, "deployment": "cloud", "email": email, "api_token": token}
	}
	view := func(c *httpClient) viewDoc {
		var v viewDoc
		c.json(http.StatusOK, http.MethodGet, bindURL, nil, &v)
		return v
	}

	t.Run("list rows and readiness", func(t *testing.T) {
		v := view(m1)
		want := []string{"wechat", "github", "gitlab_cn", "gitlab", "gitee", "jira", "tapd", "zentao", "pingcode", "teambition", "slack", "feishu", "lark", "dingtalk"}
		if len(v.Providers) != 14 || v.Email != "bind-m1@example.test" || len(v.JiraSites) != 0 {
			t.Fatalf("view = %+v", v)
		}
		for i, p := range v.Providers {
			if p.ID != want[i] || p.OrganizationReady || p.Binding != nil {
				t.Fatalf("row %d = %+v", i, p)
			}
			available := p.ID == "wechat" || p.ID == "github" || p.ID == "gitlab_cn" || p.ID == "gitlab" || p.ID == "gitee" || p.ID == "jira" || p.ID == "zentao" || p.ID == "pingcode" || p.ID == "feishu" || p.ID == "lark" || p.ID == "dingtalk"
			if available != (p.Availability == "available") || (!available && p.Method != "none") {
				t.Fatalf("row %d availability = %+v", i, p)
			}
		}
		methods := map[string]string{"wechat": "link", "github": "oauth", "gitlab_cn": "oauth", "gitlab": "oauth", "gitee": "oauth", "jira": "form", "zentao": "link", "pingcode": "link", "feishu": "oauth", "lark": "oauth", "dingtalk": "oauth"}
		for _, p := range v.Providers {
			if m, ok := methods[p.ID]; ok && p.Method != m {
				t.Fatalf("%s method = %s", p.ID, p.Method)
			}
		}
		// GitHub：组织连接 ACTIVE 且服务端已配置才 ready；飞书：组织应用 ACTIVE。
		e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'github', 'ORGANIZATION', 'acme', 'acme', 'org', $2::uuid)`, e.org, uOwner)
		if ready, _ := view(m1).provider("github"); ready {
			t.Fatal("服务端未配置 GitHub 应用时不应 ready")
		}
		var vg viewDoc
		m1GH.json(http.StatusOK, http.MethodGet, bindURL, nil, &vg)
		if ready, _ := vg.provider("github"); !ready {
			t.Fatal("GitHub 应 ready")
		}
		e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'feishu', 'ORGANIZATION', $2, 'app', 'app', $3::uuid)`, e.org, "cli_"+e.org[:8], uOwner)
		if ready, _ := view(m1).provider("feishu"); !ready {
			t.Fatal("飞书应 ready")
		}
		// Lark、钉钉与飞书同构：各自的组织应用 ACTIVE 才 ready，互不影响。
		if ready, _ := view(m1).provider("lark"); ready {
			t.Fatal("没有 Lark 应用时 Lark 不应 ready")
		}
		e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'lark', 'ORGANIZATION', $2, 'app', 'app', $3::uuid), ($1::uuid, 'dingtalk', 'ORGANIZATION', $4, 'app', 'app', $3::uuid)`,
			e.org, "cli_"+e.org[:8], uOwner, "ding"+e.org[:8])
		if ready, _ := view(m1).provider("lark"); !ready {
			t.Fatal("Lark 应 ready")
		}
		if ready, _ := view(m1).provider("dingtalk"); !ready {
			t.Fatal("钉钉应 ready")
		}
		// 个人连接：GitHub 与飞书各一条，本人可见、他人不可见。
		e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, owner_user_id, external_id, display_name, account_kind, settings, created_by)
			VALUES ($1::uuid, 'github', 'PERSONAL', $2::uuid, '42', 'm1-gh', 'user', '{"avatar_url":"https://a.test/x.png"}', $2::uuid)`, e.org, uM1)
		e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, created_by)
			SELECT organization_id, 'feishu', 'PERSONAL', id, $2::uuid, 'ou_1', '飞书的我', 'user', $2::uuid FROM openfox.integration_connections
			 WHERE organization_id = $1::uuid AND provider = 'feishu' AND scope = 'ORGANIZATION'`, e.org, uM1)
		v = view(m1)
		if _, b := v.provider("github"); b == nil || b.DisplayName != "m1-gh" {
			t.Fatalf("github binding = %+v", b)
		}
		if _, b := v.provider("feishu"); b == nil || b.DisplayName != "飞书的我" {
			t.Fatalf("feishu binding = %+v", b)
		}
		for _, id := range []string{"lark", "dingtalk"} {
			e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, created_by)
				SELECT organization_id, $3, 'PERSONAL', id, $2::uuid, 'ou_1', $3 || ' 的我', 'user', $2::uuid FROM openfox.integration_connections
				 WHERE organization_id = $1::uuid AND provider = $3 AND scope = 'ORGANIZATION'`, e.org, uM1, id)
		}
		v = view(m1)
		for _, id := range []string{"lark", "dingtalk"} {
			if _, b := v.provider(id); b == nil || b.DisplayName != id+" 的我" {
				t.Fatalf("%s binding = %+v", id, b)
			}
		}
		if _, b := view(m2).provider("github"); b != nil {
			t.Fatal("他人的个人绑定不应可见")
		}
	})

	t.Run("zentao readiness and personal comment binding", func(t *testing.T) {
		if ready, binding := view(m1).provider("zentao"); ready || binding != nil {
			t.Fatalf("zentao without organization connection = %v %+v", ready, binding)
		}
		orgConnectionID := e.str(`INSERT INTO openfox.integration_connections
			(organization_id, provider, scope, external_id, display_name, account_kind, status, created_by)
			VALUES ($1::uuid, 'zentao', 'ORGANIZATION', 'zentao-site', 'ZenTao', 'site', 'NEEDS_ATTENTION', $2::uuid)
			RETURNING id::text`, e.org, uOwner)
		if ready, _ := view(m1).provider("zentao"); ready {
			t.Fatal("zentao organization connection needing attention must not be ready")
		}
		e.exec(`UPDATE openfox.integration_connections SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, orgConnectionID)
		if ready, _ := view(m1).provider("zentao"); !ready {
			t.Fatal("ACTIVE zentao organization connection must be ready")
		}
		e.exec(`INSERT INTO openfox.integration_connections
			(organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'zentao', 'PERSONAL', $2::uuid, $3::uuid, 'unrelated', 'unrelated', 'user', $3::uuid)`, e.org, orgConnectionID, uM2)
		if _, binding := view(m2).provider("zentao"); binding != nil {
			t.Fatalf("non-comment zentao connection must not appear: %+v", binding)
		}
		personalID := e.str(`INSERT INTO openfox.integration_connections
			(organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'zentao', 'PERSONAL', $2::uuid, $3::uuid, 'zen-m1', 'Zen One', 'sync_comments', $3::uuid)
			RETURNING id::text`, e.org, orgConnectionID, uM1)
		if ready, binding := view(m1).provider("zentao"); !ready || binding == nil || binding.ConnectionID != personalID || binding.DisplayName != "Zen One" {
			t.Fatalf("zentao binding = %v %+v", ready, binding)
		}
		if _, binding := view(m2).provider("zentao"); binding != nil {
			t.Fatalf("other member must not see zentao binding: %+v", binding)
		}
		var other viewDoc
		m1.json(http.StatusOK, http.MethodGet, "/v1/organizations/"+org2+"/me/account-bindings", nil, &other)
		if ready, binding := other.provider("zentao"); ready || binding != nil {
			t.Fatalf("other organization must not see zentao connection: %v %+v", ready, binding)
		}
		e.exec(`UPDATE openfox.integration_connections SET status = 'DISCONNECTED', disconnected_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, personalID)
		if _, binding := view(m1).provider("zentao"); binding != nil {
			t.Fatalf("disconnected zentao binding must not appear: %+v", binding)
		}
	})

	t.Run("pingcode personal binding isolation and revocation", func(t *testing.T) {
		orgConnectionID := e.str(`INSERT INTO openfox.integration_connections
			(organization_id, provider, scope, external_id, display_name, account_kind, status, created_by)
			VALUES ($1::uuid, 'pingcode', 'ORGANIZATION', 'team-1', 'PingCode', 'token', 'NEEDS_ATTENTION', $2::uuid)
			RETURNING id::text`, e.org, uOwner)
		if ready, _ := view(m1).provider("pingcode"); ready {
			t.Fatal("organization connection needing attention must not be ready")
		}
		e.exec(`UPDATE openfox.integration_connections SET status='ACTIVE' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, orgConnectionID)
		if ready, _ := view(m1).provider("pingcode"); !ready {
			t.Fatal("active organization connection must be ready")
		}
		prepare := func(user string, role policy.OrganizationRole) error {
			selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: user, AuthorizationVersion: 1}
			selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
			return pool.RunForOrganization(ctx, e.org, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
				_, err := (integrations.Service{Box: box}).PreparePingCodePersonal(ctx, tx, e.org, collaboration.Actor{UserID: user, Role: role, AuthorizationVersion: 1}, integrations.PingCodePersonalInput{ConnectionID: orgConnectionID, Token: "personal-fixture"})
				return err
			})
		}
		if err := prepare(uM1, policy.OrganizationRoleMember); err != nil {
			t.Fatalf("non-admin member cannot prepare personal binding: %v", err)
		}
		if err := prepare(uGuest, policy.OrganizationRoleGuest); err == nil {
			t.Fatal("guest prepared personal binding")
		}
		personalID := e.str(`INSERT INTO openfox.integration_connections
			(organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, settings, created_by)
			VALUES ($1::uuid, 'pingcode', 'PERSONAL', $2::uuid, $3::uuid, 'ping-user-1', 'Ping One', 'sync_comments', '{"auth_method":"pingcode_personal"}', $3::uuid)
			RETURNING id::text`, e.org, orgConnectionID, uM1)
		sealed, err := box.Seal([]byte(`{"token":"personal-fixture","company_id":"team-1","user_id":"ping-user-1"}`), "integration:"+e.org+":"+personalID)
		if err != nil {
			t.Fatal(err)
		}
		blob := append([]byte(sealed.KeyID), sealed.Nonce...)
		blob = append(blob, sealed.Ciphertext...)
		e.exec(`UPDATE openfox.integration_connections SET credentials_sealed=$3,credentials_key_id=$4 WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, personalID, blob, sealed.KeyID)
		if ready, binding := view(m1).provider("pingcode"); !ready || binding == nil || binding.ConnectionID != personalID {
			t.Fatalf("personal binding = %v %+v", ready, binding)
		}
		if _, binding := view(m2).provider("pingcode"); binding != nil {
			t.Fatalf("other member's binding visible: %+v", binding)
		}
		var other viewDoc
		m1.json(http.StatusOK, http.MethodGet, "/v1/organizations/"+org2+"/me/account-bindings", nil, &other)
		if ready, binding := other.provider("pingcode"); ready || binding != nil {
			t.Fatalf("other organization sees binding: %v %+v", ready, binding)
		}
		load := func(organization, parent, author string) error {
			actor := uOwner
			if organization == org2 {
				actor = uM1
			}
			selection := tenancy.OrganizationSelection{OrganizationID: organization, ActorID: actor, AuthorizationVersion: 1}
			selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
			return pool.RunForOrganization(ctx, organization, tenancy.SourceOrganizationWorker, selector, func(ctx context.Context, tx pgx.Tx) error {
				_, err := (integrations.Service{Box: box}).LoadPingCodePersonal(ctx, tx, organization, parent, author)
				return err
			})
		}
		if err := load(e.org, orgConnectionID, uM1); err != nil {
			t.Fatalf("owner binding load: %v", err)
		}
		if err := load(e.org, orgConnectionID, uM2); err == nil {
			t.Fatal("other member loaded personal binding")
		}
		if err := load(org2, orgConnectionID, uM1); err == nil {
			t.Fatal("other organization loaded personal binding")
		}
		e.exec(`UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, orgConnectionID)
		if err := load(e.org, orgConnectionID, uM1); err == nil {
			t.Fatal("inactive parent authorized personal binding")
		}
		e.exec(`UPDATE openfox.integration_connections SET status='ACTIVE' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, orgConnectionID)
		m1.json(http.StatusOK, http.MethodPost, e.prefix+"/integrations/connections/"+personalID+"/disconnect", map[string]any{"version": 1, "confirm_phrase": ""}, nil)
		if err := load(e.org, orgConnectionID, uM1); err == nil {
			t.Fatal("disconnected binding authorized comment delivery")
		}
		if remaining := e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND credentials_sealed IS NOT NULL`, e.org, personalID); remaining != 0 {
			t.Fatal("disconnected personal token was not erased")
		}
	})

	t.Run("git provider rows: readiness, instances and isolation", func(t *testing.T) {
		insertOrg := func(provider, external, status, instanceURL string) string {
			var id string
			if err := super.QueryRow(ctx, `INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, status, settings, created_by)
				VALUES ($1::uuid, $2, 'ORGANIZATION', $3, $3, 'org', $4, jsonb_build_object('instance_url', $5::text), $6::uuid) RETURNING id::text`,
				e.org, provider, external, status, instanceURL, uOwner).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		instances := func(v viewDoc, id string) (items []string) {
			for _, p := range v.Providers {
				if p.ID == id {
					for _, in := range p.Instances {
						items = append(items, in.Host)
					}
				}
			}
			return
		}
		// 没有组织连接：三行都不 ready，GitLab 系的 instances 为空。
		for _, id := range []string{"gitlab_cn", "gitlab", "gitee"} {
			if ready, b := view(m1).provider(id); ready || b != nil {
				t.Fatalf("%s must not be ready: %v %v", id, ready, b)
			}
		}
		// NEEDS_ATTENTION 的 GitLab 连接不算 ready；ACTIVE 的才算。
		insertOrg("gitlab", "stale", "NEEDS_ATTENTION", "https://old.example.test")
		if ready, _ := view(m1).provider("gitlab"); ready {
			t.Fatal("NEEDS_ATTENTION 的实例不应 ready")
		}
		a := insertOrg("gitlab", "grp-a", "ACTIVE", "https://git.a.example.test")
		b := insertOrg("gitlab", "grp-b", "ACTIVE", "https://git.b.example.test:8443")
		if ready, _ := view(m1).provider("gitlab"); !ready {
			t.Fatal("GitLab 应 ready")
		}
		if ready, _ := view(m1).provider("gitlab_cn"); ready {
			t.Fatal("极狐没有连接，不应 ready（GitLab 的连接不能串到极狐）")
		}
		if got := instances(view(m1), "gitlab"); len(got) != 2 || got[0] != "git.a.example.test" || got[1] != "git.b.example.test:8443" {
			t.Fatalf("instances = %v", got)
		}
		// Gitee：有 ACTIVE 组织连接还要服务端已配置。
		insertOrg("gitee", "acme-gitee", "ACTIVE", "")
		if ready, _ := view(m1).provider("gitee"); ready {
			t.Fatal("服务端未配置 Gitee 时不应 ready")
		}
		var vg viewDoc
		m1GH.json(http.StatusOK, http.MethodGet, bindURL, nil, &vg)
		if ready, _ := vg.provider("gitee"); !ready {
			t.Fatal("Gitee 应 ready")
		}
		// 个人绑定：先绑实例 A，行的 binding 取 A 且 detail 是实例主机。
		personal := func(provider, parent, external string, owner string) {
			e.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, account_kind, created_by)
				VALUES ($1::uuid, $2, 'PERSONAL', NULLIF($3, '')::uuid, $4::uuid, $5, $5, 'user', $4::uuid)`, e.org, provider, parent, owner, external)
		}
		personal("gitlab", a, "m1-a", uM1)
		v := view(m1)
		if _, bind := v.provider("gitlab"); bind == nil || bind.DisplayName != "m1-a" || bind.Detail != "git.a.example.test" {
			t.Fatalf("one instance bound = %+v", bind)
		}
		for _, p := range v.Providers {
			if p.ID == "gitlab" && (len(p.Instances) != 2 || p.Instances[0].Binding == nil || p.Instances[1].Binding != nil) {
				t.Fatalf("instances = %+v", p.Instances)
			}
		}
		// 再绑实例 B：detail 变成「2 个实例」，行的 binding 仍是第一个已绑定实例。
		personal("gitlab", b, "m1-b", uM1)
		if _, bind := view(m1).provider("gitlab"); bind == nil || bind.DisplayName != "m1-a" || bind.Detail != "2 个实例" {
			t.Fatalf("two instances bound = %+v", bind)
		}
		// 只绑了实例 B 的成员：行的 binding 是 B。
		personal("gitlab", b, "m2-b", uM2)
		if _, bind := view(m2).provider("gitlab"); bind == nil || bind.DisplayName != "m2-b" || bind.Detail != "git.b.example.test:8443" {
			t.Fatalf("m2 binding = %+v", bind)
		}
		// Gitee 个人连接与 GitHub 一样是单个（不挂父连接）。
		personal("gitee", "", "m1-gitee", uM1)
		if _, bind := view(m1).provider("gitee"); bind == nil || bind.DisplayName != "m1-gitee" {
			t.Fatalf("gitee binding = %+v", bind)
		}
		// 极狐行不会拿到 GitLab 的个人绑定。
		if _, bind := view(m1).provider("gitlab_cn"); bind != nil {
			t.Fatalf("gitlab_cn binding = %+v", bind)
		}
		// 已断开的个人连接不再计入；他人看不到本人的绑定；其他组织看不到本组织的实例。
		e.exec(`UPDATE openfox.integration_connections SET status = 'DISCONNECTED', disconnected_at = now() WHERE organization_id = $1::uuid AND external_id = 'm1-b'`, e.org)
		if _, bind := view(m1).provider("gitlab"); bind == nil || bind.Detail != "git.a.example.test" {
			t.Fatalf("after disconnect = %+v", bind)
		}
		if _, bind := view(m3).provider("gitlab"); bind != nil {
			t.Fatalf("m3 must not see others' bindings: %+v", bind)
		}
		var other viewDoc
		m1.json(http.StatusOK, http.MethodGet, "/v1/organizations/"+org2+"/me/account-bindings", nil, &other)
		for _, id := range []string{"gitlab", "gitlab_cn", "gitee"} {
			if ready, bind := other.provider(id); ready || bind != nil {
				t.Fatalf("org2 %s leaks: %v %v", id, ready, bind)
			}
		}
		if got := instances(other, "gitlab"); len(got) != 0 {
			t.Fatalf("org2 instances = %v", got)
		}
		// 汇总响应里不含任何令牌 / 凭据字段。
		status, payload := m1.do(http.MethodGet, bindURL, nil)
		if status != http.StatusOK || strings.Contains(payload, "credentials") || strings.Contains(payload, "token") {
			t.Fatalf("payload leaks: %d %.200s", status, payload)
		}
	})

	t.Run("guest forbidden and unknown site", func(t *testing.T) {
		guest.expect(http.StatusForbidden, "", http.MethodGet, bindURL, nil)
		guest.expect(http.StatusForbidden, "", http.MethodPut, jiraURL, body("tok-m1", "bind-m1@example.test"))
		// 还没有导入过 Jira：任何站点都不在列表里，且不向 Jira 发请求。
		m1.expect(http.StatusBadRequest, "JIRA_SITE_UNKNOWN", http.MethodPut, jiraURL, body("tok-m1", "bind-m1@example.test"))
		if e.jira.myself.Load() != 0 {
			t.Fatal("站点未知时不应发出网络请求")
		}
	})

	// 导入 PRJ：acc-m1 两个任务、acc-m2、acc-other 与报告人以外的外部账号。
	e.runImport("PRJ", "Bind Import")
	baseline := e.jira.myself.Load() // 导入本身会调用 /myself，之后的绑定请求才计数
	t.Run("sites after import and request validation", func(t *testing.T) {
		v := view(m1)
		if len(v.JiraSites) != 1 || v.JiraSites[0].SiteURL != e.jira.srv.URL || v.JiraSites[0].Deployment != "cloud" ||
			v.JiraSites[0].Host != strings.TrimPrefix(e.jira.srv.URL, "https://") {
			t.Fatalf("sites = %+v", v.JiraSites)
		}
		if ready, _ := v.provider("jira"); !ready {
			t.Fatal("Jira 应 ready")
		}
		other := body("tok-m1", "bind-m1@example.test")
		other["site_url"] = "https://other.example.com"
		m1.expect(http.StatusBadRequest, "JIRA_SITE_UNKNOWN", http.MethodPut, jiraURL, other)
		wrongDeployment := body("tok-m1", "bind-m1@example.test")
		wrongDeployment["deployment"] = "server"
		m1.expect(http.StatusBadRequest, "JIRA_SITE_UNKNOWN", http.MethodPut, jiraURL, wrongDeployment)
		m1.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, jiraURL, body("", "bind-m1@example.test"))
		if e.jira.myself.Load() != baseline {
			t.Fatal("请求不合法时不应发出网络请求")
		}
	})

	t.Run("credential errors", func(t *testing.T) {
		before := e.jira.myself.Load()
		// cloud 邮箱不一致：先于网络请求检查。
		m1.expect(http.StatusUnprocessableEntity, "JIRA_EMAIL_MISMATCH", http.MethodPut, jiraURL, body("tok-m1", "someone@else.test"))
		if e.jira.myself.Load() != before {
			t.Fatal("邮箱不一致时不应发出网络请求")
		}
		// /myself 返回的邮箱与 OpenFox 邮箱不一致。
		m1.expect(http.StatusUnprocessableEntity, "JIRA_EMAIL_MISMATCH", http.MethodPut, jiraURL, body("tok-mismatch", "bind-m1@example.test"))
		m1.expect(http.StatusUnprocessableEntity, "JIRA_AUTH_FAILED", http.MethodPut, jiraURL, body("tok-wrong", "bind-m1@example.test"))
		m1.expect(http.StatusBadGateway, "JIRA_UNAVAILABLE", http.MethodPut, jiraURL, body("tok-500", "bind-m1@example.test"))
		if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE provider = 'jira'`) != 0 {
			t.Fatal("失败时不应留下 Jira 连接")
		}
	})

	t.Run("blocked address", func(t *testing.T) {
		// 默认（未放行回环）的 safehttp：同一站点被 SSRF 防护拦截。
		strictSrv := httptest.NewServer(api.NewWithDispatch(identity.Service{
			Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
			SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
		}, pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box,
			ImportSafeHTTP: safehttp.Options{RootCAs: e.jira.safe().RootCAs}}))
		defer strictSrv.Close()
		strict := httpLogin(t, strictSrv.URL, "bind-m1@example.test")
		strict.expect(http.StatusBadRequest, "JIRA_BLOCKED_ADDRESS", http.MethodPut, jiraURL, body("tok-m1", "bind-m1@example.test"))
	})

	acc := func(id string) (linked, method string) {
		return e.str(`SELECT COALESCE(linked_user_id::text, '') FROM openfox.external_accounts WHERE provider = 'jira' AND external_user_id = $1`, id),
			e.str(`SELECT link_method FROM openfox.external_accounts WHERE provider = 'jira' AND external_user_id = $1`, id)
	}

	var m1Binding bindingDoc
	t.Run("bind links accounts and reassigns tasks", func(t *testing.T) {
		if l, _ := acc("acc-m1"); l != "" {
			t.Fatalf("导入后 acc-m1 应未关联: %q", l)
		}
		var res bindResult
		m1.json(http.StatusOK, http.MethodPut, jiraURL, body("tok-m1", "BIND-m1@example.test"), &res)
		if res.LinkedAccounts != 1 || res.ReassignedTasks != 2 || res.SkippedAccounts != 0 ||
			res.Binding.DisplayName != "M One" || res.Binding.Detail != strings.TrimPrefix(e.jira.srv.URL, "https://") || res.Binding.Version != 1 {
			t.Fatalf("result = %+v", res)
		}
		m1Binding = res.Binding
		if l, method := acc("acc-m1"); l != uM1 || method != "verified" {
			t.Fatalf("acc-m1 = %q %q", l, method)
		}
		if l, _ := acc("acc-other"); l != "" {
			t.Fatal("其他外部账号不应被关联")
		}
		if n := e.num(`SELECT count(*) FROM openfox.tasks t JOIN openfox.external_accounts a ON a.provider = 'jira' AND a.external_user_id = 'acc-m1'
			WHERE t.assignee_id = $1::uuid AND t.external_assignee_id IS NULL`, uM1); n != 2 {
			t.Fatalf("转给本人的任务 = %d", n)
		}
		// 凭证不落库：连接行没有凭据，也不含令牌；审计里也没有。
		row := e.str(`SELECT to_jsonb(c)::text FROM openfox.integration_connections c WHERE id = $1::uuid`, res.Binding.ConnectionID)
		if strings.Contains(row, "tok-m1") || e.num(`SELECT count(*) FROM openfox.integration_connections WHERE id = $1::uuid AND credentials_sealed IS NULL`, res.Binding.ConnectionID) != 1 {
			t.Fatalf("连接行 = %s", row)
		}
		if e.num(`SELECT count(*) FROM openfox.audit_events WHERE action IN ('integration.connection_created', 'external_account.linked') AND safe_metadata::text LIKE '%tok-%'`) != 0 {
			t.Fatal("审计里不应有令牌")
		}
		if e.num(`SELECT count(*) FROM openfox.audit_events WHERE action = 'external_account.linked' AND safe_metadata->>'result' = 'verified'`) != 1 {
			t.Fatal("应有一条关联审计")
		}
		_, b := view(m1).provider("jira")
		if b == nil || b.ConnectionID != res.Binding.ConnectionID {
			t.Fatalf("列表里的绑定 = %+v", b)
		}
		if _, other := view(m2).provider("jira"); other != nil {
			t.Fatal("他人不应看到我的 Jira 绑定")
		}
	})

	t.Run("already linked to someone else is skipped", func(t *testing.T) {
		e.exec(`UPDATE openfox.external_accounts SET linked_user_id = $1::uuid, linked_at = now(), link_method = 'manual' WHERE provider = 'jira' AND external_user_id = 'acc-m2'`, uOwner)
		var res bindResult
		m2.json(http.StatusOK, http.MethodPut, jiraURL, body("tok-m2", "bind-m2@example.test"), &res)
		if res.LinkedAccounts != 0 || res.SkippedAccounts != 1 || res.ReassignedTasks != 0 {
			t.Fatalf("result = %+v", res)
		}
		if l, method := acc("acc-m2"); l != uOwner || method != "manual" {
			t.Fatalf("acc-m2 不应被改动: %q %q", l, method)
		}
	})

	t.Run("same jira account cannot be bound twice", func(t *testing.T) {
		// m2 已绑定 acc-m2；换成 acc-m1 的身份（邮箱被隐藏）会撞唯一约束。
		m2.expect(http.StatusConflict, "JIRA_ACCOUNT_IN_USE", http.MethodPut, jiraURL, body("tok-m1-hidden", "bind-m2@example.test"))
		if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE provider = 'jira' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED' AND external_id = 'acc-m2'`) != 1 {
			t.Fatal("冲突时 m2 的原绑定应保持")
		}
		// 第三个成员也不能占用 acc-m1。
		m3.expect(http.StatusConflict, "JIRA_ACCOUNT_IN_USE", http.MethodPut, jiraURL, body("tok-m1-hidden", "bind-m3@example.test"))
	})

	t.Run("update replaces the binding", func(t *testing.T) {
		var res bindResult
		m1.json(http.StatusOK, http.MethodPut, jiraURL, body("tok-m1", "bind-m1@example.test"), &res)
		if res.Binding.ConnectionID != m1Binding.ConnectionID || res.Binding.Version != 2 || res.LinkedAccounts != 0 || res.SkippedAccounts != 0 {
			t.Fatalf("result = %+v", res)
		}
		if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE provider = 'jira' AND owner_user_id = $1::uuid AND status <> 'DISCONNECTED'`, uM1) != 1 {
			t.Fatal("每个成员只有一个未断开的 Jira 绑定")
		}
	})

	t.Run("import after binding suggests verified and auto links", func(t *testing.T) {
		m3.json(http.StatusOK, http.MethodPut, jiraURL, body("tok-m3", "bind-m3@example.test"), nil)
		analysis := e.runImport("PR2", "Bind Import 2")
		suggested := map[string]string{}
		for _, u := range analysis.Analysis.Users {
			if u.SuggestedUserID != nil {
				suggested[u.SourceID] = *u.SuggestedUserID + "/" + u.SuggestReason
			}
		}
		if suggested["acc-m3"] != uM3+"/verified" || suggested["acc-m1"] != uM1+"/verified" || suggested["acc-other"] != "" {
			t.Fatalf("suggested = %v", suggested)
		}
		if l, method := acc("acc-m3"); l != uM3 || method != "verified" {
			t.Fatalf("导入时应自动关联 acc-m3: %q %q", l, method)
		}
		if n := e.num(`SELECT count(*) FROM openfox.tasks WHERE assignee_id = $1::uuid AND external_assignee_id IS NULL AND title = 'Issue 1'`, uM3); n != 1 {
			t.Fatalf("acc-m3 的任务应属于 m3: %d", n)
		}
	})

	t.Run("disconnect keeps links", func(t *testing.T) {
		var res bindResult
		m1.json(http.StatusOK, http.MethodPut, jiraURL, body("tok-m1", "bind-m1@example.test"), &res)
		m1.json(http.StatusOK, http.MethodPost, e.prefix+"/integrations/connections/"+res.Binding.ConnectionID+"/disconnect",
			map[string]any{"version": res.Binding.Version, "confirm_phrase": ""}, nil)
		if _, b := view(m1).provider("jira"); b != nil {
			t.Fatalf("断开后不应再有绑定: %+v", b)
		}
		if l, method := acc("acc-m1"); l != uM1 || method != "verified" {
			t.Fatalf("已建立的关联不撤销: %q %q", l, method)
		}
		// 别人不能断开我的连接。
		m2.expect(http.StatusNotFound, "", http.MethodPost, e.prefix+"/integrations/connections/"+res.Binding.ConnectionID+"/disconnect",
			map[string]any{"version": 1, "confirm_phrase": ""})
	})

	t.Run("cross organization isolation", func(t *testing.T) {
		// 其他组织：看不到本组织的站点与绑定；非成员访问本组织被拒绝。
		var v viewDoc
		m1.json(http.StatusOK, http.MethodGet, "/v1/organizations/"+org2+"/me/account-bindings", nil, &v)
		if len(v.JiraSites) != 0 {
			t.Fatalf("org2 sites = %+v", v.JiraSites)
		}
		for _, p := range v.Providers {
			if p.Binding != nil {
				t.Fatalf("org2 binding = %+v", p)
			}
		}
		m1.expect(http.StatusBadRequest, "JIRA_SITE_UNKNOWN", http.MethodPut, "/v1/organizations/"+org2+"/me/account-bindings/jira", body("tok-m1", "bind-m1@example.test"))
		if status, _ := m2.do(http.MethodGet, "/v1/organizations/"+org2+"/me/account-bindings", nil); status == http.StatusOK {
			t.Fatal("非成员不应能读取其他组织")
		}
	})
}
