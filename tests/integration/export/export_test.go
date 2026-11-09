// Package export 在真实 PostgreSQL（FORCE RLS、应用角色）下，经真实 HTTP 处理器验证导出任务数据：
// CSV 字段与转义、公式注入防护、BOM、JSON 快照包可被读取器读回、按发起人读权限过滤、访客 403、下载鉴权、
// 幂等、过期清理、上限与审计。
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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
	"openfox/internal/artifacts"
	"openfox/internal/exports"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/spacebundle"
)

const password = "export integration password"

type user struct{ id, email, name string }

var (
	uOwner   = user{"00000000-0000-0000-0000-00000000e001", "export-owner@example.test", "Owner"}
	uAdmin   = user{"00000000-0000-0000-0000-00000000e002", "export-admin@example.test", "Admin"}
	uMember  = user{"00000000-0000-0000-0000-00000000e003", "export-member@example.test", "Member"}
	uMember2 = user{"00000000-0000-0000-0000-00000000e004", "export-member2@example.test", "Member Two"}
	uGuest   = user{"00000000-0000-0000-0000-00000000e005", "export-guest@example.test", "Guest"}
	uOther   = user{"00000000-0000-0000-0000-00000000e006", "export-other@example.test", "Other Org Owner"}
)

const (
	orgX   = "00000000-0000-0000-0000-00000000e0a1"
	orgY   = "00000000-0000-0000-0000-00000000e0b1"
	taskA1 = "00000000-0000-0000-0000-00000000c001"
	taskA2 = "00000000-0000-0000-0000-00000000c002"
	taskA3 = "00000000-0000-0000-0000-00000000c003"
	taskA4 = "00000000-0000-0000-0000-00000000c004"
	taskB1 = "00000000-0000-0000-0000-00000000c011"
	taskS1 = "00000000-0000-0000-0000-00000000c021"
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

func (c *client) raw(method, path string, body any, headers map[string]string) (int, []byte, http.Header) {
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
	return resp.StatusCode, payload, resp.Header
}

func (c *client) expect(want int, code, method, path string, body any, headers map[string]string) string {
	c.t.Helper()
	status, payload, _ := c.raw(method, path, body, headers)
	if status != want || (code != "" && !strings.Contains(string(payload), `"`+code+`"`)) {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, payload, want, code)
	}
	return string(payload)
}

func (c *client) decode(want int, method, path string, body any, headers map[string]string, target any) http.Header {
	c.t.Helper()
	status, payload, header := c.raw(method, path, body, headers)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal(payload, target); err != nil {
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

type exportJob struct {
	ID           string `json:"id"`
	Format       string `json:"format"`
	Status       string `json:"status"`
	SpaceCount   int    `json:"space_count"`
	TaskCount    int    `json:"task_count"`
	FileName     string `json:"file_name"`
	SizeBytes    int64  `json:"size_bytes"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Downloadable bool   `json:"downloadable"`
	CreatedBy    string `json:"created_by"`
}

type spaceRef struct {
	ID string `json:"id"`
}

var keySeq int

func nextKey() map[string]string {
	keySeq++
	return map[string]string{"Idempotency-Key": fmt.Sprintf("export-key-%04d", keySeq)}
}

func TestTaskExport(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	super, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(context.Background())
	e := &env{t: t, ctx: ctx, super: super}
	seedOrganizations(e)

	database, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 60 * time.Second, LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hasher, _ := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	storage := artifacts.Local{Root: t.TempDir()}
	server := httptest.NewServer(api.NewWithStorage(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false, storage))
	defer server.Close()
	hash, _ := hasher.Hash(password)
	for _, u := range []user{uOwner, uAdmin, uMember, uMember2, uGuest, uOther} {
		e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash`, u.id, hash)
	}
	owner := login(t, server.URL, uOwner)
	admin := login(t, server.URL, uAdmin)
	member := login(t, server.URL, uMember)
	member2 := login(t, server.URL, uMember2)
	guest := login(t, server.URL, uGuest)
	other := login(t, server.URL, uOther)

	var alpha, beta, secret spaceRef
	owner.decode(http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgX+"/spaces", map[string]any{"name": "Alpha", "prefix": "ALP", "visibility": "SHARED"}, nil, &alpha)
	owner.decode(http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgX+"/spaces", map[string]any{"name": "Beta", "prefix": "BET", "visibility": "SHARED"}, nil, &beta)
	owner.decode(http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgX+"/spaces", map[string]any{"name": "Secret", "prefix": "SEC", "visibility": "PRIVATE"}, nil, &secret)
	seedTasks(e, alpha.ID, beta.ID, secret.ID)

	worker := &exports.Worker{Pool: database, Instance: "export-1", Storage: storage, TempDir: t.TempDir()}
	base := "/v1/organizations/" + orgX
	runWorker := func() {
		t.Helper()
		if err := worker.RunOrganization(ctx, orgX); err != nil {
			t.Fatalf("worker: %v", err)
		}
	}
	// start 发起并让 worker 跑完，返回终态任务。
	start := func(c *client, formatName string, spaces ...string) exportJob {
		t.Helper()
		var job exportJob
		c.decode(http.StatusCreated, http.MethodPost, base+"/exports", map[string]any{"space_ids": spaces, "format": formatName}, nextKey(), &job)
		if job.Status != "QUEUED" {
			t.Fatalf("new job = %+v", job)
		}
		runWorker()
		c.decode(http.StatusOK, http.MethodGet, base+"/exports/"+job.ID, nil, nil, &job)
		return job
	}
	download := func(c *client, id string) (int, []byte, http.Header) {
		return c.raw(http.MethodGet, base+"/exports/"+id+"/download", nil, nil)
	}

	t.Run("space list follows read permission and guests are refused", func(t *testing.T) {
		names := func(c *client) []string {
			var list struct {
				Spaces []struct {
					Name      string `json:"name"`
					TaskCount int    `json:"task_count"`
				} `json:"spaces"`
			}
			c.decode(http.StatusOK, http.MethodGet, base+"/export-spaces", nil, nil, &list)
			var out []string
			for _, s := range list.Spaces {
				out = append(out, fmt.Sprintf("%s:%d", s.Name, s.TaskCount))
			}
			return out
		}
		// 已删除的任务不计数；私有空间只有空间成员（创建者）能看到，管理员也不例外。
		if got := strings.Join(names(owner), ","); got != "Alpha:3,Beta:1,Secret:1" {
			t.Fatalf("owner spaces = %s", got)
		}
		if got := strings.Join(names(admin), ","); got != "Alpha:3,Beta:1" {
			t.Fatalf("admin spaces = %s", got)
		}
		if got := strings.Join(names(member), ","); got != "Alpha:3,Beta:1" {
			t.Fatalf("member spaces = %s", got)
		}
		guest.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, base+"/export-spaces", nil, nil)
		guest.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, base+"/exports", nil, nil)
		guest.expect(http.StatusForbidden, "FORBIDDEN", http.MethodPost, base+"/exports",
			map[string]any{"space_ids": []string{alpha.ID}, "format": "csv"}, nextKey())
	})

	t.Run("request validation", func(t *testing.T) {
		body := func(format string, ids ...string) map[string]any {
			return map[string]any{"space_ids": ids, "format": format}
		}
		owner.expect(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", http.MethodPost, base+"/exports", body("csv", alpha.ID), nil)
		owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base+"/exports", body("xlsx", alpha.ID), nextKey())
		owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base+"/exports", body("csv"), nextKey())
		owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base+"/exports", body("csv", "not-a-uuid"), nextKey())
		owner.expect(http.StatusNotFound, "SPACE_NOT_FOUND", http.MethodPost, base+"/exports", body("csv", "00000000-0000-0000-0000-0000000000ff"), nextKey())
		// 读不到的空间当作不存在，不泄露私有空间。
		admin.expect(http.StatusNotFound, "SPACE_NOT_FOUND", http.MethodPost, base+"/exports", body("csv", alpha.ID, secret.ID), nextKey())
		// 超过 20 个空间。
		ids := make([]string, 21)
		for i := range ids {
			ids[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		}
		payload := owner.expect(http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE", http.MethodPost, base+"/exports", body("csv", ids...), nextKey())
		if !strings.Contains(payload, `"max_spaces":20`) || !strings.Contains(payload, `"max_tasks":100000`) {
			t.Fatalf("limit details = %s", payload)
		}
	})

	t.Run("single space CSV", func(t *testing.T) {
		job := start(owner, "csv", alpha.ID)
		if job.Status != "SUCCEEDED" || job.SpaceCount != 1 || job.TaskCount != 3 || !job.Downloadable || !strings.HasSuffix(job.FileName, ".csv") {
			t.Fatalf("job = %+v", job)
		}
		status, body, header := download(owner, job.ID)
		if status != http.StatusOK {
			t.Fatalf("download = %d %s", status, body)
		}
		if ct := header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
			t.Fatalf("content type = %q", ct)
		}
		if cd := header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, job.FileName) {
			t.Fatalf("content disposition = %q", cd)
		}
		if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("headers = %v", header)
		}
		if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
			t.Fatal("missing BOM")
		}
		rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF}))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 4 { // 表头 + 3 个任务（已删除的不在）
			t.Fatalf("rows = %d: %v", len(rows), rows)
		}
		col := map[string]int{}
		for i, name := range rows[0] {
			col[name] = i
		}
		byNumber := map[string][]string{}
		for _, row := range rows[1:] {
			byNumber[row[col["编号"]]] = row
		}
		if _, ok := byNumber["ALP-4"]; ok {
			t.Fatal("deleted task exported")
		}
		injected := byNumber["ALP-1"]
		if injected == nil || injected[col["标题"]] != "'=cmd|' /C calc'!A0" {
			t.Fatalf("formula injection title = %v", injected)
		}
		if injected[col["描述"]] != "line1\nline2, \"quoted\"" {
			t.Fatalf("description = %q", injected[col["描述"]])
		}
		child := byNumber["ALP-2"]
		if child[col["父任务编号"]] != "ALP-1" || child[col["优先级"]] != "高" || child[col["截止日期"]] != "2026-01-09" ||
			child[col["处理人"]] != uMember.email || child[col["标签"]] != "bug;ui" || child[col["状态"]] != "Doing" ||
			child[col["Notes"]] != "'-hello" {
			t.Fatalf("child row = %v", child)
		}
		if child[col["创建人"]] != uOwner.email {
			t.Fatalf("creator = %q", child[col["创建人"]])
		}
		if job.SizeBytes != int64(len(body)) {
			t.Fatalf("size %d != %d", job.SizeBytes, len(body))
		}
	})

	t.Run("multiple spaces CSV is a zip with one CSV per space", func(t *testing.T) {
		job := start(owner, "csv", alpha.ID, beta.ID)
		if job.Status != "SUCCEEDED" || job.SpaceCount != 2 || job.TaskCount != 4 || !strings.HasSuffix(job.FileName, ".zip") {
			t.Fatalf("job = %+v", job)
		}
		status, body, header := download(owner, job.ID)
		if status != http.StatusOK || header.Get("Content-Type") != "application/zip" {
			t.Fatalf("download = %d %q", status, header.Get("Content-Type"))
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, f := range zr.File {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			got[f.Name] = len(rows) - 1
		}
		if got["ALP.csv"] != 3 || got["BET.csv"] != 1 || len(got) != 2 {
			t.Fatalf("zip entries = %v", got)
		}
	})

	t.Run("JSON snapshot package can be read back by the 7a reader", func(t *testing.T) {
		job := start(owner, "json", alpha.ID)
		if job.Status != "SUCCEEDED" || !strings.HasSuffix(job.FileName, ".jsonl.gz") {
			t.Fatalf("job = %+v", job)
		}
		_, body, header := download(owner, job.ID)
		if header.Get("Content-Type") != "application/gzip" {
			t.Fatalf("content type = %q", header.Get("Content-Type"))
		}
		hdr, end, err := spacebundle.ReadEnd(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Version != spacebundle.Version || hdr.Space.Prefix != "ALP" || end.Counts["task"] != 3 || end.Counts["comment"] != 2 || end.Counts["activity"] != 1 {
			t.Fatalf("header = %+v counts = %v", hdr, end.Counts)
		}
		raws, err := spacebundle.Select(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }, spacebundle.KindTask, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := spacebundle.Decode[spacebundle.Task](raws)
		if err != nil || len(tasks) != 3 {
			t.Fatalf("tasks = %d %v", len(tasks), err)
		}
		// 多个空间：一个 zip，每个空间一个快照包。
		multi := start(owner, "json", alpha.ID, beta.ID)
		_, zbody, _ := download(owner, multi.ID)
		zr, err := zip.NewReader(bytes.NewReader(zbody), int64(len(zbody)))
		if err != nil || len(zr.File) != 2 {
			t.Fatalf("zip = %v %v", len(zr.File), err)
		}
		for _, f := range zr.File {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if _, _, err := spacebundle.ReadEnd(bytes.NewReader(data)); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
	})

	t.Run("export is filtered by the initiator's read permission at generation time", func(t *testing.T) {
		var job exportJob
		owner.decode(http.StatusCreated, http.MethodPost, base+"/exports",
			map[string]any{"space_ids": []string{alpha.ID, secret.ID}, "format": "csv"}, nextKey(), &job)
		if job.SpaceCount != 2 || job.TaskCount != 4 {
			t.Fatalf("queued = %+v", job)
		}
		// 生成之前发起人失去私有空间的访问：文件里不能出现该空间。
		e.exec(`UPDATE openfox.space_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`, orgX, secret.ID, uOwner.id)
		runWorker()
		owner.decode(http.StatusOK, http.MethodGet, base+"/exports/"+job.ID, nil, nil, &job)
		if job.Status != "SUCCEEDED" || job.SpaceCount != 1 || job.TaskCount != 3 || !strings.HasSuffix(job.FileName, ".csv") {
			t.Fatalf("filtered job = %+v", job)
		}
		_, body, _ := download(owner, job.ID)
		if strings.Contains(string(body), "secret task") || strings.Contains(string(body), "SEC-") {
			t.Fatalf("private task leaked:\n%s", body)
		}
		// 所选空间全都读不到：失败，原因可读。
		var none exportJob
		owner.decode(http.StatusCreated, http.MethodPost, base+"/exports", map[string]any{"space_ids": []string{alpha.ID}, "format": "csv"}, nextKey(), &none)
		e.exec(`UPDATE openfox.space_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`, orgX, alpha.ID, uOwner.id)
		e.exec(`UPDATE openfox.spaces SET visibility = 'PRIVATE' WHERE organization_id = $1::uuid AND id = $2::uuid`, orgX, alpha.ID)
		runWorker()
		owner.decode(http.StatusOK, http.MethodGet, base+"/exports/"+none.ID, nil, nil, &none)
		if none.Status != "FAILED" || none.ErrorCode != "NO_READABLE_SPACES" || none.ErrorMessage == "" || none.Downloadable {
			t.Fatalf("unreadable job = %+v", none)
		}
		if status, _, _ := download(owner, none.ID); status != http.StatusConflict {
			t.Fatalf("download of failed job = %d", status)
		}
		e.exec(`UPDATE openfox.spaces SET visibility = 'SHARED' WHERE organization_id = $1::uuid AND id = $2::uuid`, orgX, alpha.ID)
		e.exec(`UPDATE openfox.space_members SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, orgX, uOwner.id)
	})

	t.Run("download and read authorization", func(t *testing.T) {
		job := start(member, "csv", beta.ID)
		if job.Status != "SUCCEEDED" {
			t.Fatalf("job = %+v", job)
		}
		if status, _, _ := download(member, job.ID); status != http.StatusOK {
			t.Fatalf("initiator download = %d", status)
		}
		// 管理员可下载；其他成员 403（详情同样）；访客 403；不存在 404。
		if status, _, _ := download(admin, job.ID); status != http.StatusOK {
			t.Fatalf("admin download = %d", status)
		}
		if status, _, _ := download(member2, job.ID); status != http.StatusForbidden {
			t.Fatalf("other member download = %d", status)
		}
		member2.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, base+"/exports/"+job.ID, nil, nil)
		if status, _, _ := download(guest, job.ID); status != http.StatusForbidden {
			t.Fatalf("guest download = %d", status)
		}
		member.expect(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/exports/00000000-0000-0000-0000-0000000000ee/download", nil, nil)
		// 未登录不能下载。
		anon := &http.Client{}
		resp, err := anon.Get(server.URL + base + "/exports/" + job.ID + "/download")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous download = %d", resp.StatusCode)
		}
		// 其他组织的所有者看不到这个组织的导出。
		if status, _, _ := download(other, job.ID); status != http.StatusForbidden && status != http.StatusNotFound {
			t.Fatalf("other org download = %d", status)
		}
		// 列表：成员只看到自己的；管理员看到全部。
		var mine, all struct {
			Exports []exportJob `json:"exports"`
		}
		member2.decode(http.StatusOK, http.MethodGet, base+"/exports", nil, nil, &mine)
		if len(mine.Exports) != 0 {
			t.Fatalf("member2 sees %d exports", len(mine.Exports))
		}
		member.decode(http.StatusOK, http.MethodGet, base+"/exports", nil, nil, &mine)
		for _, j := range mine.Exports {
			if j.CreatedBy != uMember.id {
				t.Fatalf("member sees someone else's export: %+v", j)
			}
		}
		admin.decode(http.StatusOK, http.MethodGet, base+"/exports", nil, nil, &all)
		if len(all.Exports) <= len(mine.Exports) {
			t.Fatalf("admin sees %d, member %d", len(all.Exports), len(mine.Exports))
		}
	})

	t.Run("idempotency", func(t *testing.T) {
		headers := map[string]string{"Idempotency-Key": "export-idem-0001"}
		body := map[string]any{"space_ids": []string{beta.ID}, "format": "csv"}
		var first, again exportJob
		owner.decode(http.StatusCreated, http.MethodPost, base+"/exports", body, headers, &first)
		header := owner.decode(http.StatusOK, http.MethodPost, base+"/exports", body, headers, &again)
		if again.ID != first.ID || header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("replay = %+v header %v", again, header)
		}
		owner.expect(http.StatusConflict, "", http.MethodPost, base+"/exports",
			map[string]any{"space_ids": []string{alpha.ID}, "format": "csv"}, headers)
		if n := e.count(`SELECT count(*) FROM openfox.export_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgX, first.ID); n != 1 {
			t.Fatalf("jobs = %d", n)
		}
		runWorker()
	})

	t.Run("expiry removes the file and refuses the download", func(t *testing.T) {
		job := start(owner, "csv", beta.ID)
		key := e.str(`SELECT file_key FROM openfox.export_jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, orgX, job.ID)
		if _, err := storage.Open(ctx, key); err != nil {
			t.Fatalf("file missing before expiry: %v", err)
		}
		if exp := e.str(`SELECT (expires_at - finished_at >= interval '6 days 23 hours' AND expires_at - finished_at <= interval '7 days 1 hour')::text FROM openfox.export_jobs WHERE id = $1::uuid`, job.ID); exp != "true" {
			t.Fatalf("retention is not 7 days")
		}
		e.exec(`UPDATE openfox.export_jobs SET expires_at = clock_timestamp() - interval '1 minute' WHERE organization_id = $1::uuid AND id = $2::uuid`, orgX, job.ID)
		// 过期但 worker 还没清理：下载已经被拒绝。
		if status, _, _ := download(owner, job.ID); status != http.StatusGone {
			t.Fatalf("download after expiry (before cleanup) = %d", status)
		}
		runWorker()
		if st := e.str(`SELECT status FROM openfox.export_jobs WHERE id = $1::uuid`, job.ID); st != "EXPIRED" {
			t.Fatalf("status = %s", st)
		}
		if f, err := storage.Open(ctx, key); err == nil {
			f.Close()
			t.Fatal("expired file still in storage")
		}
		if status, _, _ := download(owner, job.ID); status != http.StatusGone {
			t.Fatalf("download after cleanup = %d", status)
		}
		var got exportJob
		owner.decode(http.StatusOK, http.MethodGet, base+"/exports/"+job.ID, nil, nil, &got)
		if got.Status != "EXPIRED" || got.Downloadable {
			t.Fatalf("expired job = %+v", got)
		}
	})

	t.Run("audit records spaces, format and tasks", func(t *testing.T) {
		job := start(owner, "json", alpha.ID, beta.ID)
		got := e.str(`SELECT safe_metadata->>'result' FROM openfox.audit_events
			WHERE organization_id = $1::uuid AND action = 'task.export.completed' AND resource_id = $2 ORDER BY created_at DESC LIMIT 1`, orgX, job.ID)
		if got != "spaces=2;format=json;tasks=4" {
			t.Fatalf("completed audit = %q", got)
		}
		requested := e.str(`SELECT safe_metadata->>'result' FROM openfox.audit_events
			WHERE organization_id = $1::uuid AND action = 'task.export.requested' AND resource_id = $2`, orgX, job.ID)
		if requested != "spaces=2;format=json;tasks=4" {
			t.Fatalf("requested audit = %q", requested)
		}
	})

	t.Run("more than 100000 tasks is refused", func(t *testing.T) {
		var huge spaceRef
		owner.decode(http.StatusCreated, http.MethodPost, base+"/spaces", map[string]any{"name": "Huge", "prefix": "HUG", "visibility": "SHARED"}, nil, &huge)
		typeID := e.str(`SELECT id::text FROM openfox.task_types WHERE organization_id = $1::uuid AND space_id = $2::uuid AND is_default`, orgX, huge.ID)
		statusID := e.str(`SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = 'Todo'`, orgX)
		e.exec(`SET session_replication_role = replica`)
		e.exec(`INSERT INTO openfox.tasks (organization_id, id, space_id, sequence, type_id, status_id, title, description, acceptance, priority, rank)
			SELECT $1::uuid, gen_random_uuid(), $2::uuid, g, $3::uuid, $4::uuid, 'bulk ' || g, '', '', 'NORMAL', 'r' || lpad(g::text, 7, '0')
			  FROM generate_series(1, 100001) g`, orgX, huge.ID, typeID, statusID)
		e.exec(`SET session_replication_role = origin`)
		payload := owner.expect(http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE", http.MethodPost, base+"/exports",
			map[string]any{"space_ids": []string{huge.ID}, "format": "csv"}, nextKey())
		if !strings.Contains(payload, `"tasks":100001`) {
			t.Fatalf("details = %s", payload)
		}
		e.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE organization_id = $1::uuid AND space_id = $3::uuid`, orgX, uOwner.id, huge.ID)
	})
}

func seedOrganizations(e *env) {
	for _, u := range []user{uOwner, uAdmin, uMember, uMember2, uGuest, uOther} {
		e.exec(`INSERT INTO openfox.users (id, email, full_name) VALUES ($1::uuid, $2, $3) ON CONFLICT DO NOTHING`, u.id, u.email, u.name)
	}
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, 'export-x', 'Export Org'), ($2::uuid, 'export-y', 'Other Org')`, orgX, orgY)
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
		SELECT id, slug, name, created_at FROM openfox.organizations WHERE id IN ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, orgX, orgY)
	members := []struct {
		org  string
		u    user
		role string
	}{
		{orgX, uOwner, "OWNER"}, {orgX, uAdmin, "ADMIN"}, {orgX, uMember, "MEMBER"}, {orgX, uMember2, "MEMBER"}, {orgX, uGuest, "GUEST"},
		{orgY, uOther, "OWNER"},
	}
	for _, m := range members {
		e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, m.org, m.u.id, m.role)
	}
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, category, position) VALUES
		($1::uuid, 'Todo', 'TODO', 1), ($1::uuid, 'Doing', 'IN_PROGRESS', 2), ($1::uuid, 'Done', 'DONE', 3)`, orgX)
	e.exec(`INSERT INTO openfox.custom_fields (organization_id, id, name, kind) VALUES ($1::uuid, '00000000-0000-0000-0000-00000000a001', 'Notes', 'TEXT')`, orgX)
}

// seedTasks 直接写入任务（superuser）：Alpha 三个任务 + 一个已删除，Beta 一个，Secret（私有）一个。
func seedTasks(e *env, alpha, beta, secret string) {
	typeOf := func(space string) string {
		return e.str(`SELECT id::text FROM openfox.task_types WHERE organization_id = $1::uuid AND space_id = $2::uuid AND is_default`, orgX, space)
	}
	status := func(name string) string {
		return e.str(`SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, orgX, name)
	}
	e.exec(`INSERT INTO openfox.labels (organization_id, space_id, name, color) VALUES ($1::uuid, $2::uuid, 'bug', '#ff0000'), ($1::uuid, $2::uuid, 'ui', '#00ff00')`, orgX, alpha)
	e.exec(`INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a001', 0, $4::uuid)`, orgX, alpha, typeOf(alpha), uOwner.id)
	insert := func(space, id string, seq int, title, desc, priority, rank, statusName string, parent any) {
		e.exec(`INSERT INTO openfox.tasks (organization_id, id, space_id, sequence, type_id, status_id, title, description, acceptance, priority, rank,
		                                    due_date, parent_task_id, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, $7, $8, '', $9, $10, $11, $12::uuid, '2026-01-01T08:00:00Z', '2026-01-01T09:00:00Z')`,
			orgX, id, space, seq, typeOf(space), status(statusName), title, desc, priority, rank,
			map[bool]any{true: "2026-01-09", false: nil}[seq == 2], parent)
	}
	insert(alpha, taskA1, 1, "=cmd|' /C calc'!A0", "line1\nline2, \"quoted\"", "NORMAL", "a1", "Todo", nil)
	insert(alpha, taskA2, 2, "Child task", "", "HIGH", "a2", "Doing", taskA1)
	insert(alpha, taskA3, 3, "Plain task", "", "LOW", "a3", "Done", nil)
	insert(alpha, taskA4, 4, "Deleted task", "", "NORMAL", "a4", "Todo", nil)
	e.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE id = $1::uuid`, taskA4, uOwner.id)
	insert(beta, taskB1, 1, "Beta task", "", "NORMAL", "b1", "Todo", nil)
	insert(secret, taskS1, 1, "secret task", "", "NORMAL", "s1", "Todo", nil)
	e.exec(`INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, 'HUMAN', $4::uuid, $5::uuid)`,
		orgX, alpha, taskA2, uMember.id, uOwner.id)
	e.exec(`INSERT INTO openfox.task_labels (organization_id, space_id, task_id, label_id)
		SELECT $1::uuid, $2::uuid, $3::uuid, id FROM openfox.labels WHERE organization_id = $1::uuid AND space_id = $2::uuid`, orgX, alpha, taskA2)
	e.exec(`INSERT INTO openfox.task_field_values (organization_id, space_id, task_id, field_id, value, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, '00000000-0000-0000-0000-00000000a001', '"-hello"', $4::uuid)`, orgX, alpha, taskA2, uOwner.id)
	e.exec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, created_at) VALUES
		($1::uuid, $2::uuid, $3::uuid, $5::uuid, 'Looks good', '2026-01-03T10:00:00Z'),
		($1::uuid, $2::uuid, $4::uuid, $5::uuid, 'Needs work', '2026-01-04T10:00:00Z')`, orgX, alpha, taskA1, taskA2, uMember.id)
	e.exec(`INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, source_id, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.created', 'seed-a1', '2026-01-01T08:00:00Z')`, orgX, alpha, taskA2, uOwner.id)
}
