package imports

// Jira API 导入（本机 httptest TLS 服务模拟 Jira，经 safehttp 注入放行 127.0.0.1 并信任测试证书）、
// 新建空间 + 保留票号 + 迭代、回滚、连接错误映射、拉取失败，以及外部账号页签的列表与批量关联。
// 全程走真实 HTTP 处理器；worker 直接调用 RunOrganization。

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
)

const (
	apiOwner  = "00000000-0000-0000-0000-00000000f701"
	apiMember = "00000000-0000-0000-0000-00000000f702"
	apiMatch  = "00000000-0000-0000-0000-00000000f703"
	apiGuest  = "00000000-0000-0000-0000-00000000f704"
	apiToken  = "tok-ok-123456"
)

// ---------- 模拟 Jira ----------

type fakeJira struct {
	t          *testing.T
	srv        *httptest.Server
	deployment string

	mu           sync.Mutex
	rateLimitOne bool // 下一次请求第 2 页时先返回一次 429
	rateLimited  int
	failSearch   bool // 每页（maxResults = 100）返回 500
	countTotal   int  // 非 0 时 Count 返回该值
	searchCalls  int
}

func newFakeJira(t *testing.T, deployment string) *fakeJira {
	f := &fakeJira{t: t, deployment: deployment}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) safe() safehttp.Options {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	return safehttp.Options{RootCAs: pool, AllowAddr: func(net.IP) bool { return true }}
}

func (f *fakeJira) user(id, name, email string) map[string]any {
	u := map[string]any{"displayName": name, "emailAddress": email}
	if f.deployment == "cloud" {
		u["accountId"] = id
	} else {
		u["name"] = id
	}
	return u
}

func (f *fakeJira) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if f.deployment == "server" {
		return header == "Bearer "+apiToken
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	return err == nil && strings.HasSuffix(string(raw), ":"+apiToken)
}

const issueCount = 250

func (f *fakeJira) issue(i int) map[string]any {
	status, category := "To Do", "new"
	switch {
	case i%5 == 0:
		status, category = "Done", "done"
	case i%2 == 0:
		status, category = "In Progress", "indeterminate"
	}
	fields := map[string]any{
		"summary":     fmt.Sprintf("Issue %d", i),
		"status":      map[string]any{"name": status, "statusCategory": map[string]any{"key": category}},
		"issuetype":   map[string]any{"name": "Task"},
		"priority":    map[string]any{"name": "High"},
		"created":     "2026-01-02T03:04:05.000+0800",
		"updated":     "2026-01-03T03:04:05.000+0800",
		"description": "desc " + strconv.Itoa(i),
		"labels":      []string{},
		"reporter":    f.user("acc-rep", "Reporter Person", "reporter@jira.test"),
		"creator":     f.user("acc-rep", "Reporter Person", "reporter@jira.test"),
	}
	if i%3 == 0 {
		fields["labels"] = []string{"alpha", "beta"}
	}
	switch {
	case i == 1:
		fields["assignee"] = f.user("acc-1", "Alice Jira", "IMP-API-match@Example.Test")
		fields["customfield_10016"] = 5
	case i%7 == 0:
		fields["assignee"] = f.user("acc-ext", "Ext User", "imp-api-member@example.test")
	}
	if i == 2 {
		fields["comment"] = map[string]any{"comments": f.comments(0, 100), "total": 150}
	}
	var sprints []any
	switch i % 10 {
	case 3:
		sprints = append(sprints, f.sprint(7, "Sprint 7", "closed", "2026-01-05T09:00:00.000Z", "2026-01-19T09:00:00.000Z", "2026-01-19T10:00:00.000Z"))
	case 4:
		sprints = append(sprints, f.sprint(8, "Sprint 8", "active", "2026-02-01T09:00:00.000Z", "2026-02-14T09:00:00.000Z", ""))
	case 5:
		sprints = append(sprints, f.sprint(9, "Sprint 9", "future", "", "", ""))
	}
	fields["customfield_10020"] = sprints
	return map[string]any{"id": strconv.Itoa(70000 + i), "key": fmt.Sprintf("PRJ-%d", i), "fields": fields}
}

// sprint：cloud / 新版返回对象；server 用旧版字符串。
func (f *fakeJira) sprint(id int, name, state, start, end, complete string) any {
	if f.deployment == "cloud" {
		return map[string]any{"id": id, "name": name, "state": state, "startDate": start, "endDate": end, "completeDate": complete}
	}
	nul := func(s string) string {
		if s == "" {
			return "<null>"
		}
		return s
	}
	return fmt.Sprintf("com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=%d,rapidViewId=2,state=%s,name=%s,startDate=%s,endDate=%s,completeDate=%s,sequence=%d]",
		id, strings.ToUpper(state), name, nul(start), nul(end), nul(complete), id)
}

func (f *fakeJira) comments(from, to int) []any {
	out := []any{}
	for n := from + 1; n <= to; n++ {
		out = append(out, map[string]any{"id": fmt.Sprintf("c%d", n), "author": f.user("acc-1", "Alice Jira", "IMP-API-match@Example.Test"),
			"body": fmt.Sprintf("comment %d", n), "created": "2026-01-04T00:00:00.000+0800"})
	}
	return out
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeJira) serve(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/rest/api/2/myself":
		writeJSONResponse(w, 200, f.user("admin", "Admin", "admin@jira.test"))
	case strings.EqualFold(r.URL.Path, "/rest/api/2/project/PRJ"):
		writeJSONResponse(w, 200, map[string]any{"id": "1", "key": "PRJ", "name": "Project"})
	case strings.HasPrefix(r.URL.Path, "/rest/api/2/project/"):
		w.WriteHeader(http.StatusNotFound)
	case r.URL.Path == "/rest/api/2/field":
		writeJSONResponse(w, 200, []any{
			map[string]any{"id": "customfield_10020", "name": "Sprint", "custom": true, "schema": map[string]any{"type": "array", "custom": "com.pyxis.greenhopper.jira:gh-sprint"}},
			map[string]any{"id": "customfield_10016", "name": "Story point estimate", "custom": true, "schema": map[string]any{"type": "number"}},
		})
	case r.URL.Path == "/rest/api/2/search/approximate-count":
		count := issueCount
		if f.countTotal != 0 {
			count = f.countTotal
		}
		writeJSONResponse(w, 200, map[string]any{"count": count})
	case r.URL.Path == "/rest/api/2/search/jql" || r.URL.Path == "/rest/api/2/search":
		f.search(w, r)
	case r.URL.Path == "/rest/api/2/issue/70002/comment":
		start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		writeJSONResponse(w, 200, map[string]any{"comments": f.comments(start, min(start+100, 150)), "total": 150})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeJira) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.Contains(q.Get("jql"), "BAD") {
		writeJSONResponse(w, 400, map[string]any{"errorMessages": []string{"Error in the JQL Query: The quoted string 'BAD' is invalid."}, "errors": map[string]any{}})
		return
	}
	max, _ := strconv.Atoi(q.Get("maxResults"))
	offset := 0
	if f.deployment == "cloud" {
		offset, _ = strconv.Atoi(strings.TrimPrefix(q.Get("nextPageToken"), "o"))
	} else {
		offset, _ = strconv.Atoi(q.Get("startAt"))
	}
	f.mu.Lock()
	f.searchCalls++
	if max == 100 && f.failSearch {
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if max == 100 && offset == 100 && f.rateLimitOne {
		f.rateLimitOne = false
		f.rateLimited++
		f.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	total := issueCount
	if f.countTotal != 0 {
		total = f.countTotal
	}
	f.mu.Unlock()
	end := min(offset+min(max, 100), issueCount)
	issues := []any{}
	for i := offset + 1; i <= end; i++ {
		issues = append(issues, f.issue(i))
	}
	if f.deployment == "cloud" {
		body := map[string]any{"issues": issues}
		if end < issueCount {
			body["nextPageToken"] = "o" + strconv.Itoa(end)
		}
		writeJSONResponse(w, 200, body)
		return
	}
	writeJSONResponse(w, 200, map[string]any{"startAt": offset, "maxResults": max, "total": total, "issues": issues})
}

// ---------- 环境 ----------

type apiEnv struct {
	t      *testing.T
	ctx    context.Context
	url    string
	super  *pgx.Conn
	pool   *postgres.Pool
	org    string
	prefix string
	box    *secrets.Box
	jira   *fakeJira
	server *httptest.Server
	owner  *httpClient
	member *httpClient
	guest  *httpClient
	worker *imports.Worker
}

func (e *apiEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.super.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *apiEnv) str(query string, args ...any) string {
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

func (e *apiEnv) num(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.super.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
	return n
}

func (e *apiEnv) newServer(box *secrets.Box, opts safehttp.Options, hasher identity.Argon2idHasher) *httptest.Server {
	srv := httptest.NewServer(api.NewWithDispatch(identity.Service{
		Store: identity.PostgresStore{Pool: e.pool}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, e.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, ImportSafeHTTP: opts}))
	e.t.Cleanup(srv.Close)
	return srv
}

func TestImportsAPI(t *testing.T) {
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
	hash, err := hasher.Hash(httpPassword)
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
	e := &apiEnv{t: t, ctx: ctx, url: url, super: super, pool: pool, box: box}
	e.jira = newFakeJira(t, "cloud")
	if err := super.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&e.org); err != nil {
		t.Fatal(err)
	}
	for id, email := range map[string]string{apiOwner: "imp-api-owner@example.test", apiMember: "imp-api-member@example.test",
		apiMatch: "imp-api-match@example.test", apiGuest: "imp-api-guest@example.test"} {
		e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, email)
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
			    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, hash)
	}
	// 来源邮箱 IMP-API-match@Example.Test 与成员邮箱只差大小写。
	e.exec(`UPDATE openfox.users SET email = 'Imp-Api-Match@example.test' WHERE id = $1::uuid`, apiMatch)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Imports API')`, e.org, "imp-api-"+e.org[:8])
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'MEMBER'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'GUEST')`,
		e.org, apiOwner, apiMember, apiMatch, apiGuest)
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position, is_default) VALUES
		($1::uuid, 'To Do', '', 'TODO', 0, true), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 1, false), ($1::uuid, 'Done', '', 'DONE', 2, false)`, e.org)
	e.prefix = "/v1/organizations/" + e.org

	e.server = e.newServer(box, e.jira.safe(), hasher)
	e.owner = httpLogin(t, e.server.URL, "imp-api-owner@example.test")
	e.member = httpLogin(t, e.server.URL, "imp-api-member@example.test")
	e.guest = httpLogin(t, e.server.URL, "imp-api-guest@example.test")
	e.worker = &imports.Worker{Pool: pool, Instance: "api-w1", Secrets: box, Safe: e.jira.safe(), FetchBackoff: 20 * time.Millisecond}

	cloudRequest := func(extra map[string]any) map[string]any {
		body := map[string]any{"deployment": "cloud", "site_url": e.jira.srv.URL, "project_key": "PRJ",
			"auth": map[string]any{"kind": "api_token", "email": "admin@jira.test", "secret": apiToken}}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}

	var jobID, matchSuggest string
	var cloudJob jobDoc
	t.Run("cloud fetch with rate limit", func(t *testing.T) {
		e.jira.mu.Lock()
		e.jira.rateLimitOne = true
		e.jira.mu.Unlock()
		var draft jobDoc
		e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/imports/jira-api",
			cloudRequest(map[string]any{"new_space": map[string]any{"name": "Jira Import", "visibility": "SHARED"}}), &draft)
		jobID = draft.ID
		if draft.Status != "DRAFT" || draft.Phase != "fetching" || draft.SpaceID != nil || draft.Source != "jira_cloud" ||
			draft.Fetch == nil || draft.Fetch.Fetched != 0 || draft.Fetch.Total == nil || *draft.Fetch.Total != issueCount ||
			draft.NewSpace == nil || draft.NewSpace.Prefix != "PRJ" || draft.SpaceName != "Jira Import" {
			t.Fatalf("draft = %+v", draft)
		}
		if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE id = $1::uuid AND credentials_sealed IS NOT NULL`, jobID) != 1 {
			t.Fatal("凭据应加密保存")
		}
		raw := e.str(`SELECT preview::text || config::text || encode(credentials_sealed, 'escape') FROM openfox.import_jobs WHERE id = $1::uuid`, jobID)
		if strings.Contains(raw, apiToken) {
			t.Fatal("令牌不应明文落库")
		}
		// worker 拉取：第 2 页先遇到一次 429（Retry-After: 1）。
		sawRetry := false
		for i := 0; i < 60; i++ {
			if err := e.worker.RunOrganization(ctx, e.org); err != nil {
				t.Fatalf("worker: %v", err)
			}
			e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+jobID, nil, &cloudJob)
			if cloudJob.RetryAt != nil {
				sawRetry = true
			}
			if cloudJob.Phase == "" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if cloudJob.Phase != "" || !sawRetry || e.jira.rateLimited != 1 {
			t.Fatalf("phase=%q sawRetry=%v rateLimited=%d", cloudJob.Phase, sawRetry, e.jira.rateLimited)
		}
		if cloudJob.Fetch == nil || cloudJob.Fetch.Fetched != issueCount || cloudJob.Fetch.Total == nil || *cloudJob.Fetch.Total != issueCount {
			t.Fatalf("fetch = %+v", cloudJob.Fetch)
		}
		if e.num(`SELECT count(*) FROM openfox.import_items WHERE job_id = $1::uuid`, jobID) != issueCount ||
			e.num(`SELECT count(*) FROM openfox.import_jobs WHERE id = $1::uuid AND credentials_sealed IS NULL AND credentials_key_id = ''`, jobID) != 1 {
			t.Fatal("条目数或凭据清除不符")
		}
		a := cloudJob.Analysis
		if a == nil || !a.Capabilities.Sprints || !a.Capabilities.KeepNumbers || a.Capabilities.SprintCount != 2 || a.Counts.Total != issueCount {
			t.Fatalf("analysis = %+v", a)
		}
		for _, u := range a.Users {
			if u.SourceID == "acc-1" && u.SuggestedUserID != nil {
				matchSuggest = *u.SuggestedUserID
			}
		}
		if matchSuggest != apiMatch {
			t.Fatalf("邮箱建议 = %q", matchSuggest)
		}
		for _, u := range a.Users {
			if u.SourceID == "acc-1" && (u.SuggestReason != "email" || u.Email == "") {
				t.Fatalf("suggest_reason = %+v", u)
			}
			if u.SourceID == "acc-rep" && u.SuggestReason != "" {
				t.Fatalf("acc-rep = %+v", u)
			}
		}
		// ImportJob.api：详情与列表都返回，只有邮箱 / 用户名，没有令牌。
		for _, got := range []*jobDoc{&cloudJob} {
			if got.API == nil || got.API.Deployment != "cloud" || got.API.ProjectKey != "PRJ" || got.API.AuthKind != "api_token" ||
				got.API.AuthUser != "admin@jira.test" || got.API.SiteURL != e.jira.srv.URL {
				t.Fatalf("api = %+v", got.API)
			}
		}
		_, detailBody := e.owner.do(http.MethodGet, e.prefix+"/imports/"+jobID, nil)
		if strings.Contains(detailBody, apiToken) || !strings.Contains(detailBody, `"auth_user":"admin@jira.test"`) {
			t.Fatal("详情应带 api 且不含令牌")
		}
		if strings.Contains(e.str(`SELECT preview::text FROM openfox.import_jobs WHERE id = $1::uuid`, jobID), apiToken) {
			t.Fatal("令牌不应出现在 preview")
		}
	})

	var spaceID string
	t.Run("configure start new space keep numbers sprints", func(t *testing.T) {
		cfg := map[string]any{"columns": []any{}, "statuses": []any{},
			"users":   []any{map[string]any{"source_id": "acc-1", "mode": "member", "user_id": matchSuggest}},
			"options": map[string]any{"import_open": true, "import_done": true, "import_sprints": true, "keep_numbers": true}}
		var saved jobDoc
		e.owner.json(http.StatusOK, http.MethodPut, e.prefix+"/imports/"+jobID+"/config",
			map[string]any{"config_version": cloudJob.ConfigVersion, "config": cfg}, &saved)
		var started jobDoc
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+jobID+"/start", map[string]any{"config_version": saved.ConfigVersion}, &started)
		if started.Status != "QUEUED" {
			t.Fatalf("started = %+v", started)
		}
		done := e.drain(jobID)
		if done.Status != "SUCCEEDED" || !done.SpaceCreated || done.SpaceID == nil || done.CreatedCount != issueCount {
			t.Fatalf("done = %+v", done)
		}
		spaceID = *done.SpaceID
		// 列表（开始之后的记录）同样带 api，且不含令牌。
		_, listBody := e.owner.do(http.MethodGet, e.prefix+"/imports", nil)
		if strings.Contains(listBody, apiToken) || !strings.Contains(listBody, `"auth_user":"admin@jira.test"`) {
			t.Fatalf("列表应带 api 且不含令牌: %s", listBody)
		}
		if got := e.str(`SELECT prefix FROM openfox.spaces WHERE id = $1::uuid`, spaceID); got != "PRJ" {
			t.Fatalf("prefix = %s", got)
		}
		if got := e.num(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND sequence = substring(title from 7)::int`, spaceID); got != issueCount {
			t.Fatalf("序号等于 Jira 票号的任务 = %d", got)
		}
		if got := e.num(`SELECT next_task_number FROM openfox.spaces WHERE id = $1::uuid`, spaceID); got != issueCount+1 {
			t.Fatalf("next_task_number = %d", got)
		}
		if got := e.str(`SELECT string_agg(status, ',' ORDER BY number) FROM openfox.iterations WHERE space_id = $1::uuid`, spaceID); got != "completed,active" {
			t.Fatalf("iterations = %q", got)
		}
		iter := func(title string) string {
			return e.str(`SELECT (SELECT i.status FROM openfox.iterations i WHERE i.id = t.iteration_id) FROM openfox.tasks t WHERE t.space_id = $1::uuid AND t.title = $2`, spaceID, title)
		}
		if iter("Issue 3") != "completed" || iter("Issue 4") != "active" || iter("Issue 5") != "" {
			t.Fatalf("归属: %q %q %q", iter("Issue 3"), iter("Issue 4"), iter("Issue 5"))
		}
		statusOf := func(title string) string {
			return e.str(`SELECT s.name FROM openfox.tasks t JOIN openfox.task_statuses s ON s.id = t.status_id WHERE t.space_id = $1::uuid AND t.title = $2`, spaceID, title)
		}
		if statusOf("Issue 5") != "Done" || statusOf("Issue 2") != "In Progress" || statusOf("Issue 1") != "To Do" {
			t.Fatalf("状态: %q %q %q", statusOf("Issue 5"), statusOf("Issue 2"), statusOf("Issue 1"))
		}
		if got := e.num(`SELECT count(*) FROM openfox.task_comments c JOIN openfox.tasks t ON t.id = c.task_id WHERE t.space_id = $1::uuid AND t.title = 'Issue 2'`, spaceID); got != 150 {
			t.Fatalf("评论数 = %d", got)
		}
		if got := e.str(`SELECT assignee_id::text FROM openfox.tasks WHERE space_id = $1::uuid AND title = 'Issue 1'`, spaceID); got != apiMatch {
			t.Fatalf("Issue 1 处理人 = %q", got)
		}
		if got := e.str(`SELECT source_label FROM openfox.external_accounts WHERE organization_id = $1::uuid AND external_user_id = 'acc-ext'`, e.org); got != strings.TrimPrefix(e.jira.srv.URL, "https://") {
			t.Fatalf("source_label = %q", got)
		}
	})

	var extID string
	var extVersion int
	t.Run("external accounts list and links", func(t *testing.T) {
		// 补几个账号，覆盖筛选 / 分页。
		e.exec(`INSERT INTO openfox.external_accounts (organization_id, provider, external_user_id, display_name, email, created_by, source_label)
			SELECT $1::uuid, 'jira', 'x-' || n, 'Extra ' || n, '', $2::uuid, 'Jira CSV' FROM generate_series(1, 3) n`, e.org, apiOwner)
		e.exec(`INSERT INTO openfox.external_accounts (organization_id, provider, external_user_id, display_name, email, created_by, source_label)
			VALUES ($1::uuid, 'jira', 'pct', '100%_done', '', $2::uuid, 'Jira CSV')`, e.org, apiOwner)
		var list accountList
		e.member.json(http.StatusOK, http.MethodGet, e.prefix+"/external-accounts", nil, &list)
		if list.CanManage || list.Total != 6 || list.Unlinked != 6 || len(list.Providers) != 1 || len(list.Sources) != 2 {
			t.Fatalf("list = %+v", list)
		}
		e.guest.expect(http.StatusForbidden, "", http.MethodGet, e.prefix+"/external-accounts", nil)
		// 分页：每页 3 条。
		seen := map[string]bool{}
		cursor := ""
		for page := 0; page < 5; page++ {
			var p accountList
			e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/external-accounts?limit=3&cursor="+cursor, nil, &p)
			for _, a := range p.Accounts {
				if seen[a.ID] {
					t.Fatalf("分页重复 %s", a.ID)
				}
				seen[a.ID] = true
			}
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
		if len(seen) != 6 {
			t.Fatalf("分页共 %d 条", len(seen))
		}
		// 筛选：q 转义 % 与 _；source；link。
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/external-accounts?q=%25", nil, &list)
		if list.Total != 1 || list.Accounts[0].ExternalUserID != "pct" {
			t.Fatalf("q=%% => %+v", list)
		}
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/external-accounts?q=ext&provider=jira&link=unlinked&source="+strings.TrimPrefix(e.jira.srv.URL, "https://"), nil, &list)
		var ext *accountDoc
		for i := range list.Accounts {
			if list.Accounts[i].ExternalUserID == "acc-ext" {
				ext = &list.Accounts[i]
			}
		}
		if ext == nil {
			t.Fatalf("没有找到 acc-ext: %+v", list)
		}
		wantTasks := issueCount / 7
		if ext.SuggestedUserID == nil || *ext.SuggestedUserID != apiMember || ext.TaskCount != wantTasks || ext.LinkMethod != "" || ext.LinkedUser != nil {
			t.Fatalf("ext = %+v", ext)
		}
		extID, extVersion = ext.ID, ext.Version
		// 成员不能保存。
		e.member.expect(http.StatusForbidden, "", http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion, "user_id": apiMember}}})
		// 版本冲突：整体失败，details.ids 为冲突账号。
		status, body := e.owner.do(http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion + 5, "user_id": apiMember}}})
		if status != http.StatusConflict || !strings.Contains(body, "VERSION_CONFLICT") || !strings.Contains(body, extID) {
			t.Fatalf("conflict = %d %s", status, body)
		}
		// 关联：任务转给成员。
		var saved struct {
			Accounts        []accountDoc `json:"accounts"`
			ReassignedTasks int          `json:"reassigned_tasks"`
		}
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion, "user_id": apiMember}}}, &saved)
		if saved.ReassignedTasks != wantTasks || len(saved.Accounts) != 1 || saved.Accounts[0].LinkMethod != "manual" ||
			saved.Accounts[0].LinkedUser == nil || saved.Accounts[0].LinkedUser.ID != apiMember || saved.Accounts[0].Version != extVersion+1 ||
			saved.Accounts[0].TaskCount != 0 {
			t.Fatalf("saved = %+v", saved)
		}
		if got := e.num(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND assignee_id = $2::uuid AND external_assignee_id IS NULL`, spaceID, apiMember); got != wantTasks {
			t.Fatalf("转出的任务 = %d", got)
		}
		// 同一成员重复保存：幂等，版本不变，不重复转任务。
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion + 1, "user_id": apiMember}}}, &saved)
		if saved.ReassignedTasks != 0 || saved.Accounts[0].Version != extVersion+1 {
			t.Fatalf("idempotent = %+v", saved)
		}
		// 取消关联：任务不还原。
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion + 1, "user_id": nil}}}, &saved)
		if saved.Accounts[0].LinkedUser != nil || saved.Accounts[0].LinkMethod != "" || saved.Accounts[0].Version != extVersion+2 {
			t.Fatalf("unlink = %+v", saved)
		}
		if got := e.num(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND assignee_id = $2::uuid`, spaceID, apiMember); got != wantTasks {
			t.Fatalf("取消关联后任务被还原: %d", got)
		}
		// 目标不是活跃成员：400。
		e.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, e.prefix+"/external-accounts/links",
			map[string]any{"changes": []any{map[string]any{"id": extID, "version": extVersion + 2, "user_id": "00000000-0000-0000-0000-00000000ffff"}}})
		if got := e.num(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action IN ('external_account.linked', 'external_account.unlinked')`, e.org); got != 2 {
			t.Fatalf("审计 = %d", got)
		}
	})

	t.Run("rollback keeps space and removes iterations", func(t *testing.T) {
		var preview struct {
			Eligible     bool `json:"eligible"`
			SpaceCreated bool `json:"space_created"`
			Cleanup      struct {
				Iterations int `json:"iterations"`
			} `json:"cleanup"`
		}
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+jobID+"/rollback", nil, &preview)
		if !preview.Eligible || !preview.SpaceCreated || preview.Cleanup.Iterations != 2 {
			t.Fatalf("preview = %+v", preview)
		}
		var job jobDoc
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+jobID, nil, &job)
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+jobID+"/rollback", map[string]any{"version": job.Version}, &job)
		if job.Status != "ROLLED_BACK" {
			t.Fatalf("job = %+v", job)
		}
		if e.num(`SELECT count(*) FROM openfox.iterations WHERE space_id = $1::uuid`, spaceID) != 0 ||
			e.num(`SELECT count(*) FROM openfox.spaces WHERE id = $1::uuid AND deleted_at IS NULL`, spaceID) != 1 ||
			e.num(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, spaceID) != 0 {
			t.Fatal("回滚应删除迭代与任务、保留空间")
		}
	})

	t.Run("connection errors", func(t *testing.T) {
		reason := func(c *httpClient, body map[string]any) string {
			t.Helper()
			status, payload := c.do(http.MethodPost, e.prefix+"/imports/jira-api", body)
			if status != http.StatusBadRequest || !strings.Contains(payload, "JIRA_CONNECTION_FAILED") {
				t.Fatalf("status = %d %s", status, payload)
			}
			var doc struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(payload), &doc)
			if strings.Contains(payload, apiToken) {
				t.Fatal("错误响应不应含令牌")
			}
			r, _ := doc.Error.Details["reason"].(string)
			return r
		}
		space := map[string]any{"new_space": map[string]any{"name": "Err space", "prefix": "ERR1"}}
		bad := cloudRequest(space)
		bad["auth"] = map[string]any{"kind": "api_token", "email": "admin@jira.test", "secret": "wrong"}
		if got := reason(e.owner, bad); got != "auth_failed" {
			t.Fatalf("auth: %s", got)
		}
		if got := reason(e.owner, cloudRequest(map[string]any{"project_key": "NOPE", "new_space": map[string]any{"name": "Err space", "prefix": "ERR1"}})); got != "project_not_found" {
			t.Fatalf("project: %s", got)
		}
		if got := reason(e.owner, cloudRequest(map[string]any{"jql": "BAD", "new_space": map[string]any{"name": "Err space", "prefix": "ERR1"}})); got != "jql_invalid" {
			t.Fatalf("jql: %s", got)
		}
		e.jira.mu.Lock()
		e.jira.countTotal = 6000
		e.jira.mu.Unlock()
		if got := reason(e.owner, cloudRequest(space)); got != "too_many_issues" {
			t.Fatalf("too many: %s", got)
		}
		e.jira.mu.Lock()
		e.jira.countTotal = 0
		e.jira.mu.Unlock()
		if got := reason(e.owner, cloudRequest(map[string]any{"site_url": "http://jira.example.com", "new_space": map[string]any{"name": "Err space", "prefix": "ERR1"}})); got != "invalid_url" {
			t.Fatalf("invalid url: %s", got)
		}
		// 不注入放行：127.0.0.1 被拦截。
		strict := e.newServer(box, safehttp.Options{RootCAs: e.jira.safe().RootCAs}, hasher)
		sc := httpLogin(t, strict.URL, "imp-api-owner@example.test")
		if got := reason(sc, cloudRequest(space)); got != "blocked_address" {
			t.Fatalf("blocked: %s", got)
		}
		// 没有配置加密密钥：503。
		nobox := e.newServer(nil, e.jira.safe(), hasher)
		nc := httpLogin(t, nobox.URL, "imp-api-owner@example.test")
		nc.expect(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPost, e.prefix+"/imports/jira-api", cloudRequest(space))
		// 参数错误 / 成员无权限。
		e.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, e.prefix+"/imports/jira-api", cloudRequest(nil))
		e.member.expect(http.StatusForbidden, "", http.MethodPost, e.prefix+"/imports/jira-api", cloudRequest(space))
		if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id = $1::uuid AND error_code = '' AND status = 'DRAFT' AND phase = 'fetching'`, e.org) != 0 {
			t.Fatal("失败的创建不应留下草稿")
		}
	})

	t.Run("fetch failure discards draft", func(t *testing.T) {
		var draft jobDoc
		e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/imports/jira-api",
			cloudRequest(map[string]any{"new_space": map[string]any{"name": "Fail space", "prefix": "FAIL"}}), &draft)
		e.jira.mu.Lock()
		e.jira.failSearch = true
		e.jira.mu.Unlock()
		var job jobDoc
		for i := 0; i < 60; i++ {
			_ = e.worker.RunOrganization(ctx, e.org)
			e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+draft.ID, nil, &job)
			if job.Status == "DISCARDED" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		e.jira.mu.Lock()
		e.jira.failSearch = false
		e.jira.mu.Unlock()
		if job.Status != "DISCARDED" || job.ErrorCode != "JIRA_FETCH_FAILED" || job.ErrorMessage == "" {
			t.Fatalf("job = %+v", job)
		}
		if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE id = $1::uuid AND credentials_sealed IS NULL`, draft.ID) != 1 {
			t.Fatal("凭据应已清除")
		}
		// 拉取中不能保存配置或开始。
		var pending jobDoc
		e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/imports/jira-api",
			cloudRequest(map[string]any{"new_space": map[string]any{"name": "Pending space", "prefix": "PEND"}}), &pending)
		e.owner.expect(http.StatusConflict, "IMPORT_STATE", http.MethodPost, e.prefix+"/imports/"+pending.ID+"/start", map[string]any{"config_version": 1})
		e.owner.expect(http.StatusConflict, "IMPORT_STATE", http.MethodPut, e.prefix+"/imports/"+pending.ID+"/config",
			map[string]any{"config_version": 1, "config": map[string]any{"columns": []any{}, "statuses": []any{}, "users": []any{}, "options": map[string]any{}}})
		// 取消拉取中的草稿：丢弃并清除凭据。
		var cancelled jobDoc
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+pending.ID+"/cancel", map[string]any{"version": pending.Version}, &cancelled)
		if cancelled.Status != "DISCARDED" || e.num(`SELECT count(*) FROM openfox.import_jobs WHERE id = $1::uuid AND credentials_sealed IS NULL`, pending.ID) != 1 {
			t.Fatalf("cancelled = %+v", cancelled)
		}
	})

	t.Run("server deployment into existing space", func(t *testing.T) {
		server := newFakeJira(t, "server")
		w := &imports.Worker{Pool: pool, Instance: "api-w2", Secrets: box, Safe: server.safe(), FetchBackoff: 20 * time.Millisecond}
		var space struct {
			ID string `json:"id"`
		}
		e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/spaces", map[string]any{"name": "Existing", "visibility": "SHARED"}, &space)
		body := map[string]any{"deployment": "server", "site_url": server.srv.URL + "/", "project_key": "prj", "space_id": space.ID,
			"auth": map[string]any{"kind": "pat", "secret": apiToken}}
		var draft jobDoc
		e.owner.json(http.StatusCreated, http.MethodPost, e.prefix+"/imports/jira-api", body, &draft)
		if draft.Source != "jira_server" || draft.SpaceID == nil || *draft.SpaceID != space.ID || draft.Fetch == nil || draft.Fetch.Total == nil || *draft.Fetch.Total != issueCount {
			t.Fatalf("draft = %+v", draft)
		}
		var job jobDoc
		for i := 0; i < 40; i++ {
			if err := w.RunOrganization(ctx, e.org); err != nil {
				t.Fatal(err)
			}
			e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+draft.ID, nil, &job)
			if job.Phase == "" {
				break
			}
		}
		if job.Phase != "" || job.Fetch == nil || job.Fetch.Fetched != issueCount || job.Analysis == nil {
			t.Fatalf("job = %+v", job)
		}
		// 目标空间没有开启迭代，也不是新空间：两个选项都不可用。
		if job.Analysis.Capabilities.Sprints || job.Analysis.Capabilities.KeepNumbers || job.Analysis.Capabilities.SprintCount != 2 {
			t.Fatalf("capabilities = %+v", job.Analysis.Capabilities)
		}
		for _, option := range []string{"import_sprints", "keep_numbers"} {
			e.owner.expect(http.StatusBadRequest, "INVALID_IMPORT_CONFIG", http.MethodPut, e.prefix+"/imports/"+draft.ID+"/config",
				map[string]any{"config_version": job.ConfigVersion, "config": map[string]any{"columns": []any{}, "statuses": []any{}, "users": []any{},
					"options": map[string]any{"import_open": true, "import_done": true, option: true}}})
		}
		// 服务器旧版 Sprint 字符串也能解析（sprint_count 为 2）；导入普通任务。
		cfg := map[string]any{"columns": []any{}, "statuses": []any{}, "users": []any{}, "options": map[string]any{"import_open": true, "import_done": true}}
		var saved jobDoc
		e.owner.json(http.StatusOK, http.MethodPut, e.prefix+"/imports/"+draft.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &saved)
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+draft.ID+"/start", map[string]any{"config_version": saved.ConfigVersion}, &saved)
		if done := e.drainWith(w, draft.ID); done.Status != "SUCCEEDED" || done.CreatedCount != issueCount {
			t.Fatalf("done = %+v", done)
		}
	})

	t.Run("csv new space with keep numbers", func(t *testing.T) {
		csv := "Summary,Issue key,Issue id,Status\nA,ZED-5,1,To Do\nB,ZED-9,2,Done\nC,ZED-2,3,To Do\n"
		status, body := e.owner.uploadFields(e.prefix+"/imports/jira-csv", map[string]string{"new_space_name": "CSV Space"}, []byte(csv))
		if status != http.StatusCreated {
			t.Fatalf("upload = %d %s", status, body)
		}
		var draft jobDoc
		_ = json.Unmarshal([]byte(body), &draft)
		if draft.SpaceID != nil || draft.NewSpace == nil || draft.NewSpace.Prefix != "ZED" || draft.SpaceName != "CSV Space" ||
			draft.Analysis == nil || !draft.Analysis.Capabilities.KeepNumbers || draft.Analysis.Capabilities.Sprints {
			t.Fatalf("draft = %+v", draft)
		}
		if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE id = $1::uuid AND space_id IS NULL`, draft.ID) != 1 ||
			e.num(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND prefix = 'ZED'`, e.org) != 0 {
			t.Fatal("草稿阶段不应创建空间")
		}
		var raw map[string]any
		_ = json.Unmarshal([]byte(body), &raw)
		cfg := raw["config"].(map[string]any)
		cfg["options"] = map[string]any{"import_open": true, "import_done": true, "keep_numbers": true}
		var saved jobDoc
		e.owner.json(http.StatusOK, http.MethodPut, e.prefix+"/imports/"+draft.ID+"/config", map[string]any{"config_version": draft.ConfigVersion, "config": cfg}, &saved)
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+draft.ID+"/start", map[string]any{"config_version": saved.ConfigVersion}, &saved)
		done := e.drain(draft.ID)
		if done.Status != "SUCCEEDED" || !done.SpaceCreated || done.SpaceID == nil {
			t.Fatalf("done = %+v", done)
		}
		got := e.str(`SELECT string_agg(sequence::text, ',' ORDER BY title) FROM openfox.tasks WHERE space_id = $1::uuid`, *done.SpaceID)
		if got != "5,9,2" || e.num(`SELECT next_task_number FROM openfox.spaces WHERE id = $1::uuid`, *done.SpaceID) != 10 {
			t.Fatalf("sequences = %s", got)
		}
		// 丢弃草稿后不留下空间；前缀被占用时上传就失败。
		status, body = e.owner.uploadFields(e.prefix+"/imports/jira-csv", map[string]string{"new_space_name": "Discard Space", "new_space_prefix": "DSC"}, []byte(csv))
		if status != http.StatusCreated {
			t.Fatalf("upload = %d %s", status, body)
		}
		var discard jobDoc
		_ = json.Unmarshal([]byte(body), &discard)
		e.owner.json(http.StatusOK, http.MethodPost, e.prefix+"/imports/"+discard.ID+"/cancel", map[string]any{"version": discard.Version}, &discard)
		if discard.Status != "DISCARDED" || e.num(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND name = 'Discard Space'`, e.org) != 0 {
			t.Fatalf("discard = %+v", discard)
		}
		status, body = e.owner.uploadFields(e.prefix+"/imports/jira-csv", map[string]string{"new_space_name": "Dup", "new_space_prefix": "ZED"}, []byte(csv))
		if status != http.StatusConflict || !strings.Contains(body, "IDENTIFIER_TAKEN") {
			t.Fatalf("dup prefix = %d %s", status, body)
		}
		status, body = e.owner.uploadFields(e.prefix+"/imports/jira-csv", map[string]string{"new_space_name": "Bad", "new_space_prefix": "a"}, []byte(csv))
		if status != http.StatusBadRequest || !strings.Contains(body, "INVALID_IDENTIFIER") {
			t.Fatalf("bad prefix = %d %s", status, body)
		}
		status, body = e.owner.uploadFields(e.prefix+"/imports/jira-csv", map[string]string{"space_id": spaceID, "new_space_name": "Both"}, []byte(csv))
		if status != http.StatusBadRequest {
			t.Fatalf("both = %d %s", status, body)
		}
	})
}

// ---------- 响应结构与辅助 ----------

type jobDoc struct {
	ID            string     `json:"id"`
	SpaceID       *string    `json:"space_id"`
	SpaceName     string     `json:"space_name"`
	SpaceCreated  bool       `json:"space_created"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	Phase         string     `json:"phase"`
	ErrorCode     string     `json:"error_code"`
	ErrorMessage  string     `json:"error_message"`
	CreatedCount  int        `json:"created_count"`
	Version       int64      `json:"version"`
	ConfigVersion int64      `json:"config_version"`
	RetryAt       *time.Time `json:"retry_at"`
	API           *struct {
		Deployment string `json:"deployment"`
		SiteURL    string `json:"site_url"`
		ProjectKey string `json:"project_key"`
		JQL        string `json:"jql"`
		AuthKind   string `json:"auth_kind"`
		AuthUser   string `json:"auth_user"`
	} `json:"api"`
	NewSpace *struct {
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
	} `json:"new_space"`
	Fetch *struct {
		Fetched int  `json:"fetched"`
		Total   *int `json:"total"`
	} `json:"fetch"`
	Analysis *struct {
		Capabilities struct {
			Sprints     bool `json:"sprints"`
			KeepNumbers bool `json:"keep_numbers"`
			SprintCount int  `json:"sprint_count"`
		} `json:"capabilities"`
		Counts struct {
			Total int `json:"total"`
		} `json:"counts"`
		Users []struct {
			SourceID        string  `json:"source_id"`
			SuggestedUserID *string `json:"suggested_user_id"`
			SuggestReason   string  `json:"suggest_reason"`
			Email           string  `json:"email"`
		} `json:"users"`
	} `json:"analysis"`
}

type accountDoc struct {
	ID              string  `json:"id"`
	ExternalUserID  string  `json:"external_user_id"`
	LinkMethod      string  `json:"link_method"`
	SuggestedUserID *string `json:"suggested_user_id"`
	TaskCount       int     `json:"task_count"`
	Version         int     `json:"version"`
	LinkedUser      *struct {
		ID string `json:"id"`
	} `json:"linked_user"`
}

type accountList struct {
	Accounts   []accountDoc `json:"accounts"`
	NextCursor string       `json:"next_cursor"`
	Total      int          `json:"total"`
	Unlinked   int          `json:"unlinked"`
	Providers  []string     `json:"providers"`
	Sources    []string     `json:"sources"`
	CanManage  bool         `json:"can_manage"`
}

func (e *apiEnv) drain(jobID string) jobDoc { return e.drainWith(e.worker, jobID) }

func (e *apiEnv) drainWith(w *imports.Worker, jobID string) jobDoc {
	e.t.Helper()
	var job jobDoc
	for i := 0; i < 80; i++ {
		if err := w.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatalf("worker: %v", err)
		}
		e.owner.json(http.StatusOK, http.MethodGet, e.prefix+"/imports/"+jobID, nil, &job)
		switch job.Status {
		case "SUCCEEDED", "FAILED", "CANCELLED", "ROLLED_BACK", "DISCARDED":
			return job
		}
	}
	e.t.Fatalf("job %s did not finish: %+v", jobID, job)
	return job
}

// uploadFields 与 upload 相同，但表单字段由调用方给出（new_space_* / space_id）。
func (c *httpClient) uploadFields(path string, fields map[string]string, content []byte) (int, string) {
	c.t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = form.WriteField(k, v)
	}
	part, err := form.CreateFormFile("file", "export.csv")
	if err != nil {
		c.t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = form.Close()
	request, err := http.NewRequest(http.MethodPost, c.base+path, &buf)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	return c.send(request)
}
