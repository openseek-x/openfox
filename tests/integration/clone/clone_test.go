// Package clone 在真实 PostgreSQL（FORCE RLS、应用角色）下，经真实 HTTP 处理器验证克隆空间到另一个组织：
// 权限组合、预览与命名冲突、开始 → worker 执行 → 目标组织数据、人员对应、幂等、取消、失败后清理、跨组织隔离。
package clone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/platform/postgres"
)

const password = "clone integration password"

type user struct{ id, email, name string }

var (
	uOwner   = user{"00000000-0000-0000-0000-00000000f001", "clone-owner@example.test", "Owner"}
	uSOnly   = user{"00000000-0000-0000-0000-00000000f002", "clone-sonly@example.test", "S Only Owner"}
	uTOnly   = user{"00000000-0000-0000-0000-00000000f003", "clone-tonly@example.test", "T Only Owner"}
	uMatch   = user{"00000000-0000-0000-0000-00000000f004", "match@example.test", "Match Person"}
	uNoMatch = user{"00000000-0000-0000-0000-00000000f005", "nomatch@example.test", "No Match"}
	uTMember = user{"00000000-0000-0000-0000-00000000f006", "clone-tmember@example.test", "T Member"}
	uAdmin   = user{"00000000-0000-0000-0000-00000000f007", "clone-admin@example.test", "Admin"}
)

const (
	orgS     = "00000000-0000-0000-0000-00000000f0a1"
	orgT     = "00000000-0000-0000-0000-00000000f0b1"
	orgU     = "00000000-0000-0000-0000-00000000f0c1" // 没有任何任务状态 / 配置的目标组织
	agentID  = "00000000-0000-0000-0000-00000000f0e1"
	deviceID = "00000000-0000-0000-0000-00000000f0d1"
	t1       = "00000000-0000-0000-0000-00000000c001"
	t2       = "00000000-0000-0000-0000-00000000c002"
	t3       = "00000000-0000-0000-0000-00000000c003"
	t4       = "00000000-0000-0000-0000-00000000c004"
	t5       = "00000000-0000-0000-0000-00000000c005"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func login(t *testing.T, base string, u user) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: base, http: &http.Client{Jar: jar}}
	resp, err := c.http.Post(base+"/v1/auth/login", "application/json",
		strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, u.email, password)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("login %s = %d", u.email, resp.StatusCode)
	}
	parsed, _ := neturl.Parse(base)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			c.csrf = cookie.Value
		}
	}
	return c
}

func (c *client) do(method, path string, body any, headers map[string]string) (int, string, http.Header) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(payload), resp.Header
}

func (c *client) expect(want int, code, method, path string, body any, headers map[string]string) string {
	c.t.Helper()
	status, payload, _ := c.do(method, path, body, headers)
	if status != want || (code != "" && !strings.Contains(payload, `"`+code+`"`)) {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, payload, want, code)
	}
	return payload
}

func (c *client) decode(want int, method, path string, body any, headers map[string]string, target any) http.Header {
	c.t.Helper()
	status, payload, header := c.do(method, path, body, headers)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			c.t.Fatalf("%s: %v", payload, err)
		}
	}
	return header
}

type env struct {
	t     *testing.T
	ctx   context.Context
	super *pgx.Conn
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
		e.t.Fatalf("%s: %v", query, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.super.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

type cloneJob struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	Phase         string  `json:"phase"`
	TargetSpaceID *string `json:"target_space_id"`
	TargetName    string  `json:"target_space_name"`
	Total         int     `json:"total_count"`
	Created       int     `json:"created_count"`
	Failed        int     `json:"failed_count"`
	ErrorCode     string  `json:"error_code"`
	Cancellable   bool    `json:"cancellable"`
}

type issue struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type preview struct {
	TargetName   string `json:"target_name"`
	TargetPrefix string `json:"target_prefix"`
	NameConflict bool   `json:"name_conflict"`
	CanStart     bool   `json:"can_start"`
	Counts       struct {
		Tasks      int `json:"tasks"`
		Comments   int `json:"comments"`
		Activities int `json:"activities"`
		Projects   int `json:"projects"`
		Labels     int `json:"labels"`
	} `json:"counts"`
	Blockers []issue `json:"blockers"`
	Warnings []issue `json:"warnings"`
	Skipped  []issue `json:"skipped"`
}

func has(list []issue, code string) bool {
	for _, i := range list {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestSpaceClone(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	super, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(context.Background())
	e := &env{t: t, ctx: ctx, super: super}
	seedOrganizations(t, e)

	database, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hasher, _ := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	storageRoot := t.TempDir()
	storage := artifacts.Local{Root: storageRoot}
	server := httptest.NewServer(api.NewWithStorage(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false, storage))
	defer server.Close()
	hash, _ := hasher.Hash(password)
	for _, u := range []user{uOwner, uSOnly, uTOnly, uMatch, uNoMatch, uTMember, uAdmin} {
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash`, u.id, hash)
	}
	owner := login(t, server.URL, uOwner)
	sOnly := login(t, server.URL, uSOnly)
	tOnly := login(t, server.URL, uTOnly)
	match := login(t, server.URL, uMatch)
	tMember := login(t, server.URL, uTMember)
	admin := login(t, server.URL, uAdmin)

	// 源空间（经 API 创建，保证默认类型与工作流）与目标组织里的同名空间（制造命名冲突）。
	var space, existing struct {
		ID string `json:"id"`
	}
	owner.decode(http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgS+"/spaces",
		map[string]any{"name": "Alpha", "prefix": "ALP", "visibility": "SHARED"}, nil, &space)
	owner.decode(http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgT+"/spaces",
		map[string]any{"name": "Alpha", "prefix": "ALP", "visibility": "SHARED"}, nil, &existing)
	seedSourceSpace(t, e, space.ID)
	iconID := "00000000-0000-0000-0000-00000000c109"
	e.exec(`INSERT INTO openfox.uploaded_icons (organization_id,id,storage_key,content_type,size_bytes,sha256,created_by)
		VALUES ($1::uuid,$2::uuid,'icons/clone-test','image/png',1,repeat('a',64),$3::uuid)`, orgS, iconID, uOwner.id)
	e.exec(`UPDATE openfox.spaces SET icon=$3 WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, space.ID, "upload:"+iconID)

	srcBase := "/v1/organizations/" + orgS + "/spaces/" + space.ID + "/clone"
	sourceSnapshot := func() string {
		return e.str(`SELECT (SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid)::text || '/' ||
			(SELECT COALESCE(sum(version), 0) FROM openfox.tasks WHERE organization_id = $1::uuid)::text || '/' ||
			(SELECT count(*) FROM openfox.task_comments WHERE organization_id = $1::uuid)::text || '/' ||
			(SELECT version FROM openfox.spaces WHERE id = $2::uuid)::text`, orgS, space.ID)
	}
	before := sourceSnapshot()

	t.Run("targets and permission combinations", func(t *testing.T) {
		var list struct {
			Targets []struct{ ID, Name string } `json:"targets"`
		}
		owner.decode(http.StatusOK, http.MethodGet, srcBase+"/targets", nil, nil, &list)
		if len(list.Targets) != 2 || list.Targets[0].ID != orgT || list.Targets[1].ID != orgU {
			t.Fatalf("targets = %+v", list.Targets)
		}
		// 在目标组织只是成员：目标列表为空。
		sOnly.decode(http.StatusOK, http.MethodGet, srcBase+"/targets", nil, nil, &list)
		if len(list.Targets) != 0 {
			t.Fatalf("S-only owner targets = %+v", list.Targets)
		}
		// 在源组织只是成员：403。
		tOnly.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, srcBase+"/targets", nil, nil)
		// 缺任一边都是 403（预览与开始都一样）。
		body := map[string]any{"target_organization_id": orgT, "name": "Alpha Copy", "prefix": "ALC"}
		sOnly.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, srcBase+"/preview", body, nil)
		tOnly.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, srcBase+"/preview", body, nil)
		match.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, srcBase+"/preview", body, nil)
		sOnly.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, srcBase, body, map[string]string{"Idempotency-Key": "perm-key-0001"})
		tOnly.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, srcBase, body, map[string]string{"Idempotency-Key": "perm-key-0002"})
		// 目标组织不合法：自己、不是 UUID。
		owner.expect(http.StatusBadRequest, "INVALID_CLONE_TARGET", http.MethodPost, srcBase+"/preview", map[string]any{"target_organization_id": orgS}, nil)
		owner.expect(http.StatusBadRequest, "INVALID_CLONE_TARGET", http.MethodPost, srcBase+"/preview", map[string]any{"target_organization_id": "nope"}, nil)
		// 名称超过 32 字。
		owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, srcBase+"/preview",
			map[string]any{"target_organization_id": orgT, "name": strings.Repeat("长", 33)}, nil)
		owner.expect(http.StatusBadRequest, "INVALID_IDENTIFIER", http.MethodPost, srcBase+"/preview",
			map[string]any{"target_organization_id": orgT, "name": "X", "prefix": "a-b"}, nil)
		// 不存在的空间。
		owner.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+orgS+"/spaces/00000000-0000-0000-0000-0000000000ff/clone/preview", body, nil)
	})

	t.Run("preview counts, skipped items and naming conflict", func(t *testing.T) {
		var p preview
		owner.decode(http.StatusOK, http.MethodPost, srcBase+"/preview", map[string]any{"target_organization_id": orgT}, nil, &p)
		if !p.NameConflict || p.CanStart || !has(p.Blockers, "name_taken") || !has(p.Blockers, "identifier_taken") {
			t.Fatalf("conflict preview = %+v", p)
		}
		// 已删除的任务不计入；评论与动态按任务统计。
		if p.Counts.Tasks != 4 || p.Counts.Comments != 3 || p.Counts.Activities != 2 || p.Counts.Projects != 1 || p.Counts.Labels != 2 {
			t.Fatalf("counts = %+v", p.Counts)
		}
		if !has(p.Warnings, "agents_need_reconfigure") || !has(p.Warnings, "actor_history_kept") {
			t.Fatalf("warnings = %+v", p.Warnings)
		}
		if !has(p.Skipped, "links") || !has(p.Skipped, "space_icon") {
			t.Fatalf("skipped = %+v", p.Skipped)
		}
		owner.decode(http.StatusOK, http.MethodPost, srcBase+"/preview",
			map[string]any{"target_organization_id": orgT, "name": "Alpha Copy", "prefix": "alc"}, nil, &p)
		if p.NameConflict || !p.CanStart || p.TargetPrefix != "ALC" || p.TargetName != "Alpha Copy" || len(p.Blockers) != 0 {
			t.Fatalf("resolved preview = %+v", p)
		}
		// 预览不产生任何写入。
		if got := e.count(`SELECT count(*) FROM openfox.import_jobs WHERE source = 'space_clone'`); got != 0 {
			t.Fatalf("preview created %d jobs", got)
		}
	})

	startBody := map[string]any{"target_organization_id": orgT, "name": "Alpha Copy", "prefix": "ALC"}
	key := map[string]string{"Idempotency-Key": "clone-key-0001"}
	var job cloneJob
	t.Run("start validations and idempotency", func(t *testing.T) {
		owner.expect(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", http.MethodPost, srcBase, startBody, nil)
		owner.expect(http.StatusConflict, "IDENTIFIER_TAKEN", http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Other", "prefix": "ALP"}, map[string]string{"Idempotency-Key": "clone-key-0002"})
		owner.expect(http.StatusConflict, "NAME_TAKEN", http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "alpha", "prefix": "ALZ"}, map[string]string{"Idempotency-Key": "clone-key-0003"})
		owner.decode(http.StatusCreated, http.MethodPost, srcBase, startBody, key, &job)
		if job.Status != "QUEUED" || job.TargetSpaceID != nil || job.TargetName != "Alpha Copy" || job.Total != 4 || !job.Cancellable {
			t.Fatalf("job = %+v", job)
		}
		var replay cloneJob
		header := owner.decode(http.StatusOK, http.MethodPost, srcBase, startBody, key, &replay)
		if replay.ID != job.ID || header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("replay = %+v header=%v", replay, header)
		}
		// 一个任务、一个快照包；重复提交没有留下多余的包。
		matches, _ := filepath.Glob(filepath.Join(storageRoot, "space-clone", orgT, "*"))
		if len(matches) != 1 {
			t.Fatalf("bundles = %v", matches)
		}
		// 任务挂在目标组织：目标管理员发起人可读；目标组织其他成员 403；非发起人管理员 404。
		jobPath := "/v1/organizations/" + orgT + "/clone-jobs/" + job.ID
		owner.expect(http.StatusOK, "", http.MethodGet, jobPath, nil, nil)
		tMember.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, jobPath, nil, nil)
		tOnly.expect(http.StatusNotFound, "", http.MethodGet, jobPath, nil, nil)
		// 源组织的路径读不到这个任务。
		owner.expect(http.StatusNotFound, "", http.MethodGet, "/v1/organizations/"+orgS+"/clone-jobs/"+job.ID, nil, nil)
	})

	worker := &imports.Worker{Pool: database, Instance: "clone-1", Storage: storage}
	finishIn := func(org string, w *imports.Worker, id string) cloneJob {
		t.Helper()
		for i := 0; i < 60; i++ {
			if err := w.RunOrganization(ctx, org); err != nil {
				t.Fatalf("worker: %v", err)
			}
			var j cloneJob
			owner.decode(http.StatusOK, http.MethodGet, "/v1/organizations/"+org+"/clone-jobs/"+id, nil, nil, &j)
			switch j.Status {
			case "SUCCEEDED", "CLEANED", "FAILED", "CANCELLED", "CLEANUP_FAILED":
				return j
			}
		}
		t.Fatalf("clone %s did not finish", id)
		return cloneJob{}
	}
	finish := func(w *imports.Worker, id string) cloneJob { return finishIn(orgT, w, id) }

	var copyID string
	t.Run("run and verify the copy", func(t *testing.T) {
		job = finish(worker, job.ID)
		if job.Status != "SUCCEEDED" || job.Created != 4 || job.Failed != 0 || job.TargetSpaceID == nil || job.Cancellable {
			t.Fatalf("finished = %+v", job)
		}
		copyID = *job.TargetSpaceID
		q := func(sql string, args ...any) string { return e.str(sql, append([]any{orgT, copyID}, args...)...) }
		if got := q(`SELECT COALESCE(icon,'') FROM openfox.spaces WHERE organization_id=$1::uuid AND id=$2::uuid`); got != "" {
			t.Fatalf("clone retained another organization's uploaded icon: %s", got)
		}
		if got := q(`SELECT name || '/' || prefix || '/' || visibility FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`); got != "Alpha Copy/ALC/SHARED" {
			t.Fatalf("space = %s", got)
		}
		// 任务数量与票号一致，已删除的任务不克隆。
		if got := q(`SELECT string_agg(sequence::text || ':' || title, ',' ORDER BY sequence) FROM openfox.tasks
			WHERE organization_id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL`); got != "1:Parent task,2:Child task,3:Plain task,4:Agent task" {
			t.Fatalf("tasks = %s", got)
		}
		if got := q(`SELECT next_task_number::text FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`); got != "6" {
			t.Fatalf("next_task_number = %s", got)
		}
		// 父子关系、关联关系保留。
		if got := q(`SELECT p.title FROM openfox.tasks c JOIN openfox.tasks p ON p.id = c.parent_task_id AND p.organization_id = c.organization_id
			WHERE c.organization_id = $1::uuid AND c.space_id = $2::uuid AND c.title = 'Child task'`); got != "Parent task" {
			t.Fatalf("parent = %q", got)
		}
		if got := q(`SELECT s.title || '>' || r.builtin_type || '>' || d.title FROM openfox.task_relations r
			JOIN openfox.tasks s ON s.id = r.source_task_id AND s.organization_id = r.organization_id
			JOIN openfox.tasks d ON d.id = r.target_task_id AND d.organization_id = r.organization_id
			WHERE r.organization_id = $1::uuid AND r.source_space_id = $2::uuid`); got != "Parent task>BLOCKS>Plain task" {
			t.Fatalf("relation = %q", got)
		}
		// 任务字段。
		if got := q(`SELECT priority || '/' || start_date::text || '/' || due_date::text || '/' || description || '/' || acceptance FROM openfox.tasks
			WHERE organization_id = $1::uuid AND space_id = $2::uuid AND title = 'Child task'`); got != "HIGH/2026-01-02/2026-01-09/child body/child acceptance" {
			t.Fatalf("child fields = %q", got)
		}
		// 状态：已有的沿用（Todo / Done 的 ID 不变），没有的新建并记录。
		if got := e.str(`SELECT count(*)::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND lower(name) IN ('todo', 'done')`, orgT); got != "2" {
			t.Fatalf("existing statuses duplicated: %s", got)
		}
		if got := q(`SELECT st.name FROM openfox.tasks t JOIN openfox.task_statuses st ON st.id = t.status_id AND st.organization_id = t.organization_id
			WHERE t.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Child task'`); got != "Doing" {
			t.Fatalf("child status = %q", got)
		}
		if n := e.count(`SELECT count(*) FROM openfox.import_created_resources r JOIN openfox.task_statuses s ON s.id = r.resource_id
			WHERE r.organization_id = $1::uuid AND r.job_id = $2::uuid AND r.kind = 'STATUS' AND s.name IN ('Backlog', 'Doing')`, orgT, job.ID); n != 2 {
			t.Fatalf("created status resources = %d", n)
		}
		// 标签、自定义字段（按名称复用 / 新建）、专项。
		if got := q(`SELECT string_agg(l.name, ',' ORDER BY l.name) FROM openfox.task_labels tl JOIN openfox.labels l ON l.id = tl.label_id AND l.organization_id = tl.organization_id
			WHERE tl.organization_id = $1::uuid AND tl.space_id = $2::uuid`); got != "bug,ui" {
			t.Fatalf("labels = %q", got)
		}
		if n := e.count(`SELECT count(*) FROM openfox.custom_fields WHERE organization_id = $1::uuid AND lower(name) = 'notes'`, orgT); n != 1 {
			t.Fatalf("Notes field duplicated: %d", n)
		}
		if n := e.count(`SELECT count(*) FROM openfox.custom_fields WHERE organization_id = $1::uuid AND name = 'Severity' AND kind = 'SELECT'`, orgT); n != 1 {
			t.Fatalf("Severity field = %d", n)
		}
		if got := q(`SELECT o.label || '|' || (SELECT v2.value #>> '{}' FROM openfox.task_field_values v2 WHERE v2.organization_id = v.organization_id AND v2.task_id = v.task_id AND v2.field_id <> v.field_id)
			FROM openfox.task_field_values v JOIN openfox.custom_field_options o ON o.id::text = v.value #>> '{}' AND o.organization_id = v.organization_id
			WHERE v.organization_id = $1::uuid AND v.space_id = $2::uuid`); got != "High|hello" {
			t.Fatalf("field values = %q", got)
		}
		if got := q(`SELECT p.name FROM openfox.tasks t JOIN openfox.projects p ON p.id = t.project_id AND p.organization_id = t.organization_id
			WHERE t.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Child task'`); got != "Roadmap" {
			t.Fatalf("project = %q", got)
		}
		// 人员：邮箱对应成员；对应不上的成为外部账号（不带邮箱）；Agent 处理人被去掉并留下动态。
		if got := q(`SELECT assignee_id::text FROM openfox.tasks WHERE organization_id = $1::uuid AND space_id = $2::uuid AND title = 'Child task'`); got != uMatch.id {
			t.Fatalf("child assignee = %q", got)
		}
		if got := q(`SELECT a.display_name || '|' || a.provider || '|' || a.email || '|' || a.source_label || '|' || COALESCE(a.linked_user_id::text, '-')
			FROM openfox.tasks t JOIN openfox.external_accounts a ON a.id = t.external_assignee_id AND a.organization_id = t.organization_id
			WHERE t.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Plain task'`); got != "No Match|openfox||Source Org|-" {
			t.Fatalf("external assignee = %q", got)
		}
		if n := e.count(`SELECT count(*) FROM openfox.task_assignees WHERE organization_id = $1::uuid AND space_id = $2::uuid AND kind <> 'HUMAN'`, orgT, copyID); n != 0 {
			t.Fatalf("agent assignees kept: %d", n)
		}
		if got := q(`SELECT a.safe_diff->'agents'->>0 FROM openfox.task_activities a JOIN openfox.tasks t ON t.id = a.task_id AND t.organization_id = a.organization_id
			WHERE a.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Agent task' AND a.kind = 'task.clone_agent_removed'`); got != "Bot" {
			t.Fatalf("agent note = %q", got)
		}
		if n := e.count(`SELECT count(*) FROM openfox.task_followers WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`, orgT, copyID, uMatch.id); n != 1 {
			t.Fatalf("followers = %d", n)
		}
		// 评论：作者对应得上的沿用作者；对应不上的记为发起人并在正文首行标注原作者与来源组织。
		if got := q(`SELECT author_user_id::text || '|' || body FROM openfox.task_comments c JOIN openfox.tasks t ON t.id = c.task_id AND t.organization_id = c.organization_id
			WHERE c.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Parent task'`); got != uMatch.id+"|Looks good" {
			t.Fatalf("matched comment = %q", got)
		}
		if got := q(`SELECT author_user_id::text || '|' || body FROM openfox.task_comments c JOIN openfox.tasks t ON t.id = c.task_id AND t.organization_id = c.organization_id
			WHERE c.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.title = 'Child task'`); !strings.HasPrefix(got, uOwner.id+"|**No Match**（来自 Source Org · ") || !strings.HasSuffix(got, "Needs work") {
			t.Fatalf("unmatched comment = %q", got)
		}
		// 动态作为只读历史保留原操作者名称与原始时间。
		if got := q(`SELECT string_agg((a.safe_diff->>'actor_name') || ':' || (a.safe_diff->>'source_kind'), ',' ORDER BY a.created_at) FROM openfox.task_activities a
			WHERE a.organization_id = $1::uuid AND a.space_id = $2::uuid AND a.kind = 'task.cloned_history'`); got != "Owner:task.created,Match Person:task.status_changed" {
			t.Fatalf("history = %q", got)
		}
		// 自动化与通知没有被触发。
		if n := e.count(`SELECT count(*) FROM openfox.automation_events WHERE organization_id = $1::uuid AND space_id = $2::uuid`, orgT, copyID); n != 0 {
			t.Fatalf("automation events = %d", n)
		}
		// 审计：目标组织与源组织各一条开始记录。
		if n := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'space.clone.started'`, orgT); n != 1 {
			t.Fatalf("target audit = %d", n)
		}
		if n := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'space.clone.started'`, orgS); n != 1 {
			t.Fatalf("source audit = %d", n)
		}
		// 快照包在完成后删除。
		if matches, _ := filepath.Glob(filepath.Join(storageRoot, "space-clone", orgT, job.ID+"*")); len(matches) != 0 {
			t.Fatalf("bundle not deleted: %v", matches)
		}
		// 克隆完成后同键重放仍返回同一个任务。
		var replay cloneJob
		owner.decode(http.StatusOK, http.MethodPost, srcBase, startBody, key, &replay)
		if replay.ID != job.ID {
			t.Fatalf("replay after finish = %+v", replay)
		}
		// 源空间不变。
		if after := sourceSnapshot(); after != before {
			t.Fatalf("source changed: %s -> %s", before, after)
		}
	})

	t.Run("cancel while queued", func(t *testing.T) {
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Alpha Cancel", "prefix": "ALX"}, map[string]string{"Idempotency-Key": "clone-key-0010"}, &j)
		jobPath := "/v1/organizations/" + orgT + "/clone-jobs/" + j.ID
		var cancelled cloneJob
		owner.decode(http.StatusOK, http.MethodPost, jobPath+"/cancel", nil, nil, &cancelled)
		if cancelled.Status != "CANCELLED" || cancelled.Cancellable {
			t.Fatalf("cancelled = %+v", cancelled)
		}
		if err := worker.RunOrganization(ctx, orgT); err != nil {
			t.Fatal(err)
		}
		if n := e.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND name = 'Alpha Cancel'`, orgT); n != 0 {
			t.Fatalf("cancelled clone created a space")
		}
		if matches, _ := filepath.Glob(filepath.Join(storageRoot, "space-clone", orgT, j.ID+"*")); len(matches) != 0 {
			t.Fatalf("bundle not deleted after cancel: %v", matches)
		}
		// 已结束的任务再取消是幂等的。
		owner.decode(http.StatusOK, http.MethodPost, jobPath+"/cancel", nil, nil, &cancelled)
		tMember.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, jobPath+"/cancel", nil, nil)
	})

	t.Run("failure after creating data is cleaned up", func(t *testing.T) {
		// 源组织新增一个状态与一个自定义字段：克隆会在目标组织新建它们，失败后应归档。
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES ($1::uuid, '00000000-0000-0000-0000-00000000a0a1', 'QA', 'IN_PROGRESS', 9)`, orgS)
		e.exec(`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
			SELECT $1::uuid, $2::uuid, id, '00000000-0000-0000-0000-00000000a0a1', 9, false FROM openfox.task_types WHERE organization_id = $1::uuid AND space_id = $2::uuid AND is_default`, orgS, space.ID)
		e.exec(`INSERT INTO openfox.custom_fields (organization_id, id, name, kind) VALUES ($1::uuid, '00000000-0000-0000-0000-00000000a0a2', 'Extra', 'TEXT')`, orgS)
		e.exec(`INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by)
			SELECT $1::uuid, $2::uuid, id, '00000000-0000-0000-0000-00000000a0a2', 5, $3::uuid FROM openfox.task_types WHERE organization_id = $1::uuid AND space_id = $2::uuid AND is_default`,
			orgS, space.ID, uOwner.id)
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Alpha Fail", "prefix": "ALF"}, map[string]string{"Idempotency-Key": "clone-key-0020"}, &j)
		// 先只跑前两个批次（校验 + 建空间与配置），然后让快照包消失。
		partial := &imports.Worker{Pool: database, Instance: "clone-2", Storage: storage, MaxBatches: 2}
		if err := partial.RunOrganization(ctx, orgT); err != nil {
			t.Fatal(err)
		}
		var mid cloneJob
		owner.decode(http.StatusOK, http.MethodGet, "/v1/organizations/"+orgT+"/clone-jobs/"+j.ID, nil, nil, &mid)
		if mid.Status != "RUNNING" || mid.Phase != "importing" || mid.TargetSpaceID == nil {
			t.Fatalf("mid = %+v", mid)
		}
		if e.str(`SELECT name FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = 'QA'`, orgT) != "QA" {
			t.Fatal("QA status was not created")
		}
		matches, _ := filepath.Glob(filepath.Join(storageRoot, "space-clone", orgT, j.ID+"*"))
		if len(matches) != 1 {
			t.Fatalf("bundle = %v", matches)
		}
		if err := os.Remove(matches[0]); err != nil {
			t.Fatal(err)
		}
		// 另一个实例接手（租约未过期的话由同一个实例继续）。
		final := finish(&imports.Worker{Pool: database, Instance: "clone-2", Storage: storage}, j.ID)
		if final.Status != "CLEANED" || final.ErrorCode != "BUNDLE_MISSING" {
			t.Fatalf("final = %+v", final)
		}
		failedSpace := *mid.TargetSpaceID
		if e.str(`SELECT (deleted_at IS NOT NULL)::text FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, failedSpace) != "true" {
			t.Fatal("failed clone space was not deleted")
		}
		if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL`, orgT, failedSpace); n != 0 {
			t.Fatalf("live tasks after cleanup = %d", n)
		}
		if e.str(`SELECT (archived_at IS NOT NULL)::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = 'QA'`, orgT) != "true" {
			t.Fatal("QA status was not archived")
		}
		if e.str(`SELECT (archived_at IS NOT NULL)::text FROM openfox.custom_fields WHERE organization_id = $1::uuid AND name = 'Extra'`, orgT) != "true" {
			t.Fatal("Extra field was not archived")
		}
		// 第一次克隆的数据不受影响。
		if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL`, orgT, copyID); n != 4 {
			t.Fatalf("first clone tasks = %d", n)
		}
		if after := sourceSnapshot(); after == "" {
			t.Fatal("source snapshot")
		}
	})

	t.Run("cancel while running cleans up", func(t *testing.T) {
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Alpha Run Cancel", "prefix": "ALR"}, map[string]string{"Idempotency-Key": "clone-key-0030"}, &j)
		partial := &imports.Worker{Pool: database, Instance: "clone-3", Storage: storage, MaxBatches: 2}
		if err := partial.RunOrganization(ctx, orgT); err != nil {
			t.Fatal(err)
		}
		jobPath := "/v1/organizations/" + orgT + "/clone-jobs/" + j.ID
		var mid cloneJob
		owner.decode(http.StatusOK, http.MethodPost, jobPath+"/cancel", nil, nil, &mid)
		if mid.Status != "CANCEL_REQUESTED" || mid.TargetSpaceID == nil {
			t.Fatalf("cancel requested = %+v", mid)
		}
		final := finish(&imports.Worker{Pool: database, Instance: "clone-3", Storage: storage}, j.ID)
		if final.Status != "CLEANED" || final.ErrorCode != "CANCELLED" {
			t.Fatalf("final = %+v", final)
		}
		if e.str(`SELECT (deleted_at IS NOT NULL)::text FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, *mid.TargetSpaceID) != "true" {
			t.Fatal("cancelled clone space was not deleted")
		}
		if matches, _ := filepath.Glob(filepath.Join(storageRoot, "space-clone", orgT, j.ID+"*")); len(matches) != 0 {
			t.Fatalf("bundle not deleted: %v", matches)
		}
	})

	t.Run("permissions are re-checked when the worker runs", func(t *testing.T) {
		// 发起人是两边的管理员（所有者不能被降级，组织要求至少一个活跃所有者）。
		start := func(name, prefix, key string) cloneJob {
			var j cloneJob
			admin.decode(http.StatusCreated, http.MethodPost, srcBase,
				map[string]any{"target_organization_id": orgT, "name": name, "prefix": prefix}, map[string]string{"Idempotency-Key": key}, &j)
			return j
		}
		setRole := func(org, role string) {
			e.exec(`UPDATE openfox.organization_members SET role = $3 WHERE organization_id = $1::uuid AND user_id = $2::uuid`, org, uAdmin.id, role)
		}
		run := func(instance, id string) (status, code, spaceID string) {
			w := &imports.Worker{Pool: database, Instance: instance, Storage: storage}
			for i := 0; i < 30; i++ {
				if err := w.RunOrganization(ctx, orgT); err != nil {
					t.Fatalf("worker: %v", err)
				}
				status = e.str(`SELECT status FROM openfox.import_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, id)
				switch status {
				case "SUCCEEDED", "CLEANED", "FAILED", "CANCELLED", "CLEANUP_FAILED":
					return status, e.str(`SELECT error_code FROM openfox.import_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, id),
						e.str(`SELECT COALESCE(space_id::text, '') FROM openfox.import_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, id)
				}
			}
			t.Fatalf("job %s did not finish", id)
			return
		}
		// 源组织降级：开始阶段就拒绝，什么都不创建。
		j := start("Alpha SrcDemoted", "ALQ", "clone-key-0040")
		setRole(orgS, "MEMBER")
		status, code, spaceID := run("clone-4", j.ID)
		setRole(orgS, "ADMIN")
		if status != "FAILED" || code != "SOURCE_FORBIDDEN" || spaceID != "" {
			t.Fatalf("source demoted = %s %s %q", status, code, spaceID)
		}
		// 目标组织降级。
		j = start("Alpha TgtDemoted", "ALT", "clone-key-0041")
		setRole(orgT, "MEMBER")
		status, code, spaceID = run("clone-5", j.ID)
		setRole(orgT, "ADMIN")
		if status != "FAILED" || code != "IMPORTER_FORBIDDEN" || spaceID != "" {
			t.Fatalf("target demoted = %s %s %q", status, code, spaceID)
		}
		for _, name := range []string{"Alpha SrcDemoted", "Alpha TgtDemoted"} {
			if n := e.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND name = $2`, orgT, name); n != 0 {
				t.Fatalf("space %q was created", name)
			}
		}
		// 管理员（非所有者）也能在两边都是管理员时发起。
		_ = status
	})

	t.Run("target organization without any configuration", func(t *testing.T) {
		// 目标组织没有任何任务状态：新空间不会自带默认任务类型（回归：creating_space 曾因此一直 no rows in result set）。
		var p preview
		owner.decode(http.StatusOK, http.MethodPost, srcBase+"/preview", map[string]any{"target_organization_id": orgU}, nil, &p)
		if p.NameConflict || !p.CanStart {
			t.Fatalf("preview = %+v", p)
		}
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase, map[string]any{"target_organization_id": orgU},
			map[string]string{"Idempotency-Key": "clone-key-0050"}, &j)
		final := finishIn(orgU, &imports.Worker{Pool: database, Instance: "clone-6", Storage: storage}, j.ID)
		if final.Status != "SUCCEEDED" || final.Created != 4 || final.TargetSpaceID == nil || final.ErrorCode != "" {
			t.Fatalf("final = %+v", final)
		}
		space := *final.TargetSpaceID
		if got := e.str(`SELECT string_agg(name || ':' || is_default::text, ',' ORDER BY name) FROM openfox.task_types
			WHERE organization_id = $1::uuid AND space_id = $2::uuid AND archived_at IS NULL`, orgU, space); got != "Task:true" {
			t.Fatalf("types = %q", got)
		}
		if got := e.str(`SELECT string_agg(s.name, ',' ORDER BY s.position) FROM openfox.workflow_statuses w JOIN openfox.task_statuses s ON s.id = w.status_id AND s.organization_id = w.organization_id
			WHERE w.organization_id = $1::uuid AND w.space_id = $2::uuid`, orgU, space); !strings.Contains(got, "Todo") || !strings.Contains(got, "Done") {
			t.Fatalf("workflow = %q", got)
		}
		if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL AND type_id IS NOT NULL`, orgU, space); n != 4 {
			t.Fatalf("tasks = %d", n)
		}
	})

	t.Run("permanent failure goes to cleanup with a readable message", func(t *testing.T) {
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Alpha Broken", "prefix": "ALB"}, map[string]string{"Idempotency-Key": "clone-key-0060"}, &j)
		partial := &imports.Worker{Pool: database, Instance: "clone-7", Storage: storage, MaxBatches: 2}
		if err := partial.RunOrganization(ctx, orgT); err != nil {
			t.Fatal(err)
		}
		spaceID := e.str(`SELECT space_id::text FROM openfox.import_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, j.ID)
		if spaceID == "" {
			t.Fatal("space was not created")
		}
		// 让所有任务写入失败，并让失败记录的主键冲突：批次整体出错，错误不是瞬时的。
		e.exec(`DELETE FROM openfox.workflow_statuses WHERE organization_id = $1::uuid AND space_id = $2::uuid`, orgT, spaceID)
		e.exec(`INSERT INTO openfox.import_items (organization_id, job_id, ordinal, external_id, raw, status, action, processed_at)
			VALUES ($1::uuid, $2::uuid, 0, 'sabotage', '{}', 'SKIPPED', 'skipped_existing', clock_timestamp())`, orgT, j.ID)
		final := finish(&imports.Worker{Pool: database, Instance: "clone-7", Storage: storage}, j.ID)
		if final.Status != "CLEANED" || final.ErrorCode != "CLONE_FAILED" {
			t.Fatalf("final = %+v", final)
		}
		msg := e.str(`SELECT error_message FROM openfox.import_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, j.ID)
		if strings.Contains(msg, "duplicate") || strings.Contains(msg, "SQLSTATE") || !strings.Contains(msg, "内部错误") {
			t.Fatalf("error_message = %q", msg)
		}
		if e.str(`SELECT (deleted_at IS NOT NULL)::text FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, spaceID) != "true" {
			t.Fatal("space was not deleted")
		}
	})

	t.Run("transient failures stop after the retry limit", func(t *testing.T) {
		var j cloneJob
		owner.decode(http.StatusCreated, http.MethodPost, srcBase,
			map[string]any{"target_organization_id": orgT, "name": "Alpha Retries", "prefix": "ALY"}, map[string]string{"Idempotency-Key": "clone-key-0070"}, &j)
		partial := &imports.Worker{Pool: database, Instance: "clone-8", Storage: storage, MaxBatches: 2}
		if err := partial.RunOrganization(ctx, orgT); err != nil {
			t.Fatal(err)
		}
		// 模拟同一任务连续失败多次：计数超过上限后下一次认领就转入清理，而不是继续重试。
		e.exec(`UPDATE openfox.import_jobs SET attempts = 9, lease_owner = '', lease_expires_at = NULL, error_message = '克隆暂时失败，正在重试。'
			WHERE organization_id = $1::uuid AND id = $2::uuid`, orgT, j.ID)
		final := finish(&imports.Worker{Pool: database, Instance: "clone-9", Storage: storage}, j.ID)
		if final.Status != "CLEANED" || final.ErrorCode != "TOO_MANY_ATTEMPTS" {
			t.Fatalf("final = %+v", final)
		}
		if n := e.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND name = 'Alpha Retries' AND deleted_at IS NULL`, orgT); n != 0 {
			t.Fatalf("space still live: %d", n)
		}
	})

	t.Run("cross organization isolation", func(t *testing.T) {
		// 目标组织的普通成员读不到源组织的任何数据。
		tMember.expect(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "/v1/organizations/"+orgS+"/spaces/"+space.ID, nil, nil)
		tMember.expect(http.StatusNotFound, "NOT_FOUND", http.MethodGet, srcBase+"/targets", nil, nil)
		// 目标组织里没有任何源组织的组织级数据（例如源组织专有的 Backlog 之外的成员外部账号邮箱）。
		if n := e.count(`SELECT count(*) FROM openfox.external_accounts WHERE organization_id = $1::uuid AND email <> ''`, orgT); n != 0 {
			t.Fatalf("emails leaked into target external accounts: %d", n)
		}
		if n := e.count(`SELECT count(*) FROM openfox.users u JOIN openfox.organization_members m ON m.user_id = u.id WHERE m.organization_id = $1::uuid AND u.id = $2::uuid`, orgT, uNoMatch.id); n != 0 {
			t.Fatalf("unmatched source member became a target member")
		}
	})
}

func seedOrganizations(t *testing.T, e *env) {
	t.Helper()
	for _, u := range []user{uOwner, uSOnly, uTOnly, uMatch, uNoMatch, uTMember, uAdmin} {
		e.exec(`INSERT INTO openfox.users (id, email, full_name) VALUES ($1::uuid, $2, $3) ON CONFLICT DO NOTHING`, u.id, u.email, u.name)
	}
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, 'clone-src', 'Source Org'), ($2::uuid, 'clone-dst', 'Target Org'), ($3::uuid, 'clone-empty', 'Empty Org')`, orgS, orgT, orgU)
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
		SELECT id, slug, name, created_at FROM openfox.organizations WHERE id IN ($1::uuid, $2::uuid, $3::uuid) ON CONFLICT DO NOTHING`, orgS, orgT, orgU)
	members := []struct {
		org  string
		u    user
		role string
	}{
		{orgS, uOwner, "OWNER"}, {orgT, uOwner, "OWNER"},
		{orgS, uSOnly, "OWNER"}, {orgT, uSOnly, "MEMBER"},
		{orgS, uTOnly, "MEMBER"}, {orgT, uTOnly, "OWNER"},
		{orgS, uMatch, "MEMBER"}, {orgT, uMatch, "MEMBER"},
		{orgS, uNoMatch, "MEMBER"},
		{orgT, uTMember, "MEMBER"},
		{orgS, uAdmin, "ADMIN"}, {orgT, uAdmin, "ADMIN"},
		{orgU, uOwner, "OWNER"},
	}
	for _, m := range members {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, m.org, m.u.id, m.role)
	}
	// 源组织：四个状态；目标组织：只有 Todo / Done 和一个同名自定义字段 Notes。
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, category, position) VALUES
		($1::uuid, 'Backlog', 'BACKLOG', 1), ($1::uuid, 'Todo', 'TODO', 2), ($1::uuid, 'Doing', 'IN_PROGRESS', 3), ($1::uuid, 'Done', 'DONE', 4),
		($2::uuid, 'Todo', 'TODO', 1), ($2::uuid, 'Done', 'DONE', 2)`, orgS, orgT)
	e.exec(`INSERT INTO openfox.custom_fields (organization_id, name, kind) VALUES ($1::uuid, 'Notes', 'TEXT')`, orgT)
}

// seedSourceSpace 直接写入源空间内容（superuser）：标签、字段、专项、五个任务（一个已删除）、人员、关联、评论、动态。
func seedSourceSpace(t *testing.T, e *env, spaceID string) {
	t.Helper()
	typeID := e.str(`SELECT id::text FROM openfox.task_types WHERE organization_id = $1::uuid AND space_id = $2::uuid AND is_default`, orgS, spaceID)
	status := func(name string) string {
		return e.str(`SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, orgS, name)
	}
	e.exec(`INSERT INTO openfox.labels (organization_id, space_id, name, color) VALUES ($1::uuid, $2::uuid, 'bug', '#ff0000'), ($1::uuid, $2::uuid, 'ui', '#00ff00')`, orgS, spaceID)
	e.exec(`INSERT INTO openfox.custom_fields (organization_id, id, name, kind) VALUES
		($1::uuid, '00000000-0000-0000-0000-00000000a001', 'Notes', 'TEXT'), ($1::uuid, '00000000-0000-0000-0000-00000000a002', 'Severity', 'SELECT')`, orgS)
	e.exec(`INSERT INTO openfox.custom_field_options (organization_id, field_id, id, label, color, position) VALUES
		($1::uuid, '00000000-0000-0000-0000-00000000a002', '00000000-0000-0000-0000-00000000a0c1', 'High', '#ff0000', 0),
		($1::uuid, '00000000-0000-0000-0000-00000000a002', '00000000-0000-0000-0000-00000000a0c2', 'Low', '#00ff00', 1)`, orgS)
	e.exec(`INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by) VALUES
		($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a001', 0, $4::uuid),
		($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a002', 1, $4::uuid)`, orgS, spaceID, typeID, uOwner.id)
	e.exec(`INSERT INTO openfox.projects (organization_id, id, name, created_by) VALUES ($1::uuid, '00000000-0000-0000-0000-00000000a0b1', 'Roadmap', $2::uuid)`, orgS, uOwner.id)
	e.exec(`INSERT INTO openfox.project_spaces (organization_id, project_id, space_id, is_primary) VALUES ($1::uuid, '00000000-0000-0000-0000-00000000a0b1', $2::uuid, true)`, orgS, spaceID)

	e.exec(`INSERT INTO openfox.tasks (organization_id, id, space_id, sequence, type_id, status_id, title, description, acceptance, priority, rank,
	                                    start_date, due_date, parent_task_id, project_id, created_at, updated_at) VALUES
		($1::uuid, $3::uuid, $2::uuid, 1, $4::uuid, $5::uuid, 'Parent task', 'parent body', '', 'NORMAL', 'a1', NULL, NULL, NULL, NULL, '2026-01-01T08:00:00Z', '2026-01-01T09:00:00Z'),
		($1::uuid, $6::uuid, $2::uuid, 2, $4::uuid, $7::uuid, 'Child task', 'child body', 'child acceptance', 'HIGH', 'a2', '2026-01-02', '2026-01-09', $3::uuid,
		 '00000000-0000-0000-0000-00000000a0b1', '2026-01-02T08:00:00Z', '2026-01-02T09:00:00Z'),
		($1::uuid, $8::uuid, $2::uuid, 3, $4::uuid, $5::uuid, 'Plain task', '', '', 'LOW', 'a3', NULL, NULL, NULL, NULL, '2026-01-03T08:00:00Z', '2026-01-03T09:00:00Z'),
		($1::uuid, $9::uuid, $2::uuid, 4, $4::uuid, $5::uuid, 'Agent task', '', '', 'NORMAL', 'a4', NULL, NULL, NULL, NULL, '2026-01-04T08:00:00Z', '2026-01-04T09:00:00Z'),
		($1::uuid, $10::uuid, $2::uuid, 5, $4::uuid, $5::uuid, 'Deleted task', '', '', 'NORMAL', 'a5', NULL, NULL, NULL, NULL, '2026-01-05T08:00:00Z', '2026-01-05T09:00:00Z')`,
		orgS, spaceID, t1, typeID, status("Todo"), t2, status("Doing"), t3, t4, t5)
	e.exec(`UPDATE openfox.spaces SET next_task_number = 6 WHERE organization_id = $1::uuid AND id = $2::uuid`, orgS, spaceID)
	e.exec(`INSERT INTO openfox.task_links (organization_id, space_id, task_id, url, title, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, 'https://example.test/doc', 'Doc', $4::uuid)`, orgS, spaceID, t1, uOwner.id)
	e.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE id = $1::uuid`, t5, uOwner.id)
	e.exec(`UPDATE openfox.tasks SET assignee_id = $2::uuid WHERE id = $1::uuid`, t2, uMatch.id)
	e.exec(`INSERT INTO openfox.task_labels (organization_id, space_id, task_id, label_id)
		SELECT $1::uuid, $2::uuid, $3::uuid, id FROM openfox.labels WHERE organization_id = $1::uuid AND space_id = $2::uuid`, orgS, spaceID, t2)
	e.exec(`INSERT INTO openfox.task_field_values (organization_id, space_id, task_id, field_id, value, updated_by) VALUES
		($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a001', '"hello"', $4::uuid),
		($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a002', '"00000000-0000-0000-0000-00000000a0c1"', $4::uuid)`, orgS, spaceID, t2, uOwner.id)

	// 组织级 Agent（space 为空，满足 agents 的身份约束）作为任务 4 的处理人。
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES ($1::uuid, 'clone-device', 1)`, deviceID)
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, orgS, deviceID)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, name) VALUES ($1::uuid, $2::uuid, $3::uuid, 'Bot')`, orgS, agentID, deviceID)
	e.exec(`INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by) VALUES
		($1::uuid, $2::uuid, $3::uuid, 'HUMAN', $5::uuid, $7::uuid), ($1::uuid, $2::uuid, $4::uuid, 'HUMAN', $6::uuid, $7::uuid)`,
		orgS, spaceID, t2, t3, uMatch.id, uNoMatch.id, uOwner.id)
	e.exec(`INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, agent_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, 'AGENT', $4::uuid, $5::uuid)`,
		orgS, spaceID, t4, agentID, uOwner.id)
	e.exec(`INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, 'HUMAN', $4::uuid, $5::uuid)`,
		orgS, spaceID, t2, uMatch.id, uOwner.id)
	e.exec(`INSERT INTO openfox.task_relations (organization_id, source_space_id, source_task_id, target_space_id, target_task_id, builtin_type, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $2::uuid, $4::uuid, 'BLOCKS', $5::uuid)`, orgS, spaceID, t1, t3, uOwner.id)
	e.exec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, created_at) VALUES
		($1::uuid, $2::uuid, $3::uuid, $6::uuid, 'Looks good', '2026-01-03T10:00:00Z'),
		($1::uuid, $2::uuid, $4::uuid, $7::uuid, 'Needs work', '2026-01-04T10:00:00Z'),
		($1::uuid, $2::uuid, $5::uuid, $8::uuid, 'Plain note', '2026-01-05T10:00:00Z')`, orgS, spaceID, t1, t2, t3, uMatch.id, uNoMatch.id, uOwner.id)
	e.exec(`INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, source_id, created_at) VALUES
		($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.created', 'seed-a1', '2026-01-01T08:00:00Z'),
		($1::uuid, $2::uuid, $3::uuid, $5::uuid, 'task.status_changed', 'seed-a2', '2026-01-02T08:00:00Z')`, orgS, spaceID, t1, uOwner.id, uMatch.id)
}
