package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	cliTestToken = "ofx_pat_SECRET-TOKEN-0123456789abcdef"
	cliOrgID     = "00000000-0000-4000-8000-0000000000a1"
	cliSpaceID   = "00000000-0000-4000-8000-0000000000b1"
	cliTaskID    = "00000000-0000-4000-8000-0000000000c1"
	cliUserID    = "00000000-0000-4000-8000-0000000000d1"
	cliAgentID   = "00000000-0000-4000-8000-0000000000e1"
)

// cliRecord 是假中心收到的一次请求。
type cliRecord struct {
	Method, Path, Query, Auth, Key, UserAgent string
	Body                                      string
}

// cliCenter 是模拟中心：按 "METHOD 路径" 分派，记录所有请求。
type cliCenter struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	records  []cliRecord
	handlers map[string]http.HandlerFunc
}

func newCLICenter(t *testing.T) *cliCenter {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	center := &cliCenter{t: t, handlers: map[string]http.HandlerFunc{}}
	center.server = httptest.NewUnstartedServer(http.HandlerFunc(center.serve))
	center.server.Listener = listener
	center.server.Start()
	t.Cleanup(center.server.Close)
	center.json("GET /api/v1/me", 200, `{"id":"`+cliUserID+`","email":"cli@example.test","full_name":"Cli"}`)
	center.json("GET /api/v1/me/organizations", 200, `{"organizations":[{"id":"`+cliOrgID+`","slug":"acme","name":"Acme","role":"OWNER","created_at":"2026-01-01T00:00:00Z"}]}`)
	return center
}

func (c *cliCenter) serve(w http.ResponseWriter, r *http.Request) {
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(r.Body)
	c.mu.Lock()
	c.records = append(c.records, cliRecord{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("User-Agent"), body.String()})
	handler := c.handlers[r.Method+" "+r.URL.Path]
	c.mu.Unlock()
	if handler == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no such route","request_id":"req-404"}}`))
		return
	}
	r.Body = nil
	handler(w, r)
}

func (c *cliCenter) handle(route string, handler http.HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[route] = handler
}

func (c *cliCenter) json(route string, status int, body string) {
	c.handle(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func (c *cliCenter) apiError(route string, status int, code string) {
	c.json(route, status, fmt.Sprintf(`{"error":{"code":%q,"message":"boom","request_id":"req-1"}}`, code))
}

func (c *cliCenter) requests(method, path string) []cliRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []cliRecord
	for _, r := range c.records {
		if r.Method == method && r.Path == path {
			found = append(found, r)
		}
	}
	return found
}

// cliHarness 封装一次 CLI 调用的依赖：隔离配置目录、环境变量、stdin 与输出缓冲。
type cliHarness struct {
	t      *testing.T
	dir    string
	env    map[string]string
	stdin  string
	stdout bytes.Buffer
	stderr bytes.Buffer
	sleeps int
}

func newCLIHarness(t *testing.T) *cliHarness {
	t.Helper()
	return &cliHarness{t: t, dir: filepath.Join(t.TempDir(), "OpenFox"), env: map[string]string{}}
}

func (h *cliHarness) run(args ...string) error {
	h.stdout.Reset()
	h.stderr.Reset()
	keys := 0
	deps := cliDeps{
		stdin: strings.NewReader(h.stdin), stdout: &h.stdout, stderr: &h.stderr,
		getenv:    func(name string) string { return h.env[name] },
		configDir: func() (string, error) { return h.dir, nil },
		newKey:    func() string { keys++; return fmt.Sprintf("generated-key-%04d", keys) },
		sleep:     func(time.Duration) { h.sleeps++ },
		// 关闭连接复用：避免 Go 传输层对带 Idempotency-Key 的请求自行重发，保证测试只观察 CLI 自己的重试。
		httpClient: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second},
	}
	return runCLI(args[0], args[1:], deps)
}

// login 用 stdin 里的令牌登录到假中心，并选中组织。
func (h *cliHarness) login(center *cliCenter) {
	h.t.Helper()
	h.stdin = cliTestToken + "\n"
	if err := h.run("auth", "login", "--profile", "work", "--server", center.server.URL, "--org", "acme"); err != nil {
		h.t.Fatal(err)
	}
	h.stdin = ""
}

func exitCodeOf(err error) int {
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	if err == nil {
		return 0
	}
	return -1
}

func assertNoToken(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if strings.Contains(text, cliTestToken) || strings.Contains(text, "SECRET-TOKEN") {
			t.Fatalf("token leaked: %s", text)
		}
	}
}

func TestCLILoginStoresTokenPrivatelyAndNeverPrintsIt(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.stdin = cliTestToken + "\n"
	if err := h.run("auth", "login", "--profile", "work", "--server", center.server.URL+"/", "--org", "acme"); err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, h.stdout.String(), h.stderr.String())
	cliDir := filepath.Join(h.dir, "cli")
	for path, perm := range map[string]os.FileMode{cliDir: 0o700, filepath.Join(cliDir, "tokens"): 0o700,
		filepath.Join(cliDir, "tokens", "work"): 0o600, filepath.Join(cliDir, "profiles.json"): 0o600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != perm {
			t.Fatalf("%s mode = %v, %v; want %o", path, info, err, perm)
		}
	}
	profiles, _ := os.ReadFile(filepath.Join(cliDir, "profiles.json"))
	assertNoToken(t, string(profiles))
	if strings.Contains(string(profiles), center.server.URL+"/\"") {
		t.Fatalf("server URL was not normalized: %s", profiles)
	}
	// 设备配置目录里不能出现 CLI 文件。
	if _, err := os.Stat(filepath.Join(h.dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("device config.json touched: %v", err)
	}
	records := center.requests("GET", "/api/v1/me")
	if len(records) != 1 || records[0].Auth != "Bearer "+cliTestToken || !strings.HasPrefix(records[0].UserAgent, "openfox-cli/") {
		t.Fatalf("records = %#v", records)
	}
	// 后续命令使用已保存的 profile 与令牌。
	center.json("GET /api/v1/organizations/"+cliOrgID+"/spaces", 200, `{"spaces":[{"id":"`+cliSpaceID+`","prefix":"OPEN","name":"Open","visibility":"shared"}]}`)
	if err := h.run("space", "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "OPEN") {
		t.Fatalf("stdout = %s", h.stdout.String())
	}
	assertNoToken(t, h.stdout.String(), h.stderr.String())
}

func TestCLILoginRejectsTokenArgumentAndBadToken(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	// 令牌当作参数传入：当作多余参数拒绝，且错误信息不回显它。
	err := h.run("auth", "login", "--server", center.server.URL, cliTestToken)
	if exitCodeOf(err) != cliExitUsage {
		t.Fatalf("err = %v", err)
	}
	assertNoToken(t, err.Error(), h.stdout.String(), h.stderr.String())
	err = h.run("auth", "login", "--server", center.server.URL, "--token", cliTestToken)
	if exitCodeOf(err) != cliExitUsage {
		t.Fatalf("err = %v", err)
	}
	assertNoToken(t, err.Error())
	if len(center.requests("GET", "/api/v1/me")) != 0 {
		t.Fatal("center was contacted with an argv token")
	}
	// 格式不对的令牌在本地就拒绝，不发请求。
	h.stdin = "not-a-pat-secret-value"
	err = h.run("auth", "login", "--server", center.server.URL)
	if exitCodeOf(err) != cliExitUsage || strings.Contains(err.Error(), "not-a-pat-secret-value") {
		t.Fatalf("err = %v", err)
	}
	// 中心拒绝（401）：退出码 4，不写任何本地文件。
	center.apiError("GET /api/v1/me", 401, "UNAUTHENTICATED")
	h.stdin = cliTestToken
	err = h.run("auth", "login", "--server", center.server.URL)
	if exitCodeOf(err) != cliExitAuth {
		t.Fatalf("err = %v", err)
	}
	assertNoToken(t, err.Error(), h.stdout.String(), h.stderr.String())
	if _, statErr := os.Stat(filepath.Join(h.dir, "cli")); !os.IsNotExist(statErr) {
		t.Fatalf("files written after a rejected login: %v", statErr)
	}
}

func TestCLIEnvironmentOverridesAndNoProfile(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.env["OPENFOX_SERVER"], h.env["OPENFOX_TOKEN"], h.env["OPENFOX_ORG"] = center.server.URL, cliTestToken, cliOrgID
	center.json("GET /api/v1/organizations/"+cliOrgID+"/spaces", 200, `{"spaces":[]}`)
	if err := h.run("space", "list", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(h.stdout.String()) != `{"spaces":[]}` {
		t.Fatalf("json output not verbatim: %q", h.stdout.String())
	}
	if _, err := os.Stat(h.dir); !os.IsNotExist(err) {
		t.Fatalf("environment-only run wrote files: %v", err)
	}
	// 缺令牌：退出码 4；缺服务器：退出码 2。
	delete(h.env, "OPENFOX_TOKEN")
	if code := exitCodeOf(h.run("space", "list")); code != cliExitAuth {
		t.Fatalf("missing token code = %d", code)
	}
	delete(h.env, "OPENFOX_SERVER")
	if code := exitCodeOf(h.run("space", "list")); code != cliExitUsage {
		t.Fatalf("missing server code = %d", code)
	}
}

func TestCLIExitCodes(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	path := "GET /api/v1/organizations/" + cliOrgID + "/spaces"
	cases := []struct {
		status int
		code   int
	}{{401, cliExitAuth}, {403, cliExitAuth}, {404, cliExitNotFound}, {409, cliExitConflict}, {400, cliExitUsage},
		{502, cliExitUnreachable}, {503, cliExitUnreachable}, {500, cliExitGeneral}, {429, cliExitGeneral}}
	for _, tc := range cases {
		center.apiError(path, tc.status, "SOME_CODE")
		err := h.run("space", "list")
		if exitCodeOf(err) != tc.code {
			t.Fatalf("status %d: exit %d (%v), want %d", tc.status, exitCodeOf(err), err, tc.code)
		}
		if !strings.Contains(err.Error(), "SOME_CODE") || !strings.Contains(err.Error(), "req-1") {
			t.Fatalf("error lacks code/request id: %v", err)
		}
	}
	// json 模式：错误是 stderr 上的 JSON。
	center.apiError(path, 404, "SPACE_NOT_FOUND")
	err := h.run("space", "list", "--output", "json")
	var decoded struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
			Status    int    `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &decoded) != nil || decoded.Error.Code != "SPACE_NOT_FOUND" || decoded.Error.RequestID != "req-1" || decoded.Error.Status != 404 {
		t.Fatalf("json error = %s", err.Error())
	}
	// 中心不可达：退出码 5。
	h.env["OPENFOX_SERVER"] = "http://127.0.0.1:1"
	if code := exitCodeOf(h.run("space", "list")); code != cliExitUnreachable {
		t.Fatalf("unreachable code = %d", code)
	}
	// 参数错误。
	delete(h.env, "OPENFOX_SERVER")
	for _, args := range [][]string{{"task", "get"}, {"task", "list"}, {"task", "get", "bad ref"}, {"nope"}, {"task", "list", "--space", "OPEN", "--limit", "500"}, {"space", "list", "--output", "xml"}} {
		if code := exitCodeOf(h.run(args...)); code != cliExitUsage {
			t.Fatalf("%v: exit %d, want %d", args, code, cliExitUsage)
		}
	}
}

func TestCLIIdempotentWritesRetryOnceWithSameKey(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	base := "/api/v1/organizations/" + cliOrgID
	center.json("GET "+base+"/spaces", 200, `{"spaces":[{"id":"`+cliSpaceID+`","prefix":"OPEN","name":"Open"}]}`)
	// 第一次请求直接断开连接（网络错误），第二次成功。
	failFirst := func(status int, body string) http.HandlerFunc {
		var mu sync.Mutex
		calls := 0
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	center.handle("POST "+base+"/spaces/"+cliSpaceID+"/tasks", failFirst(201, `{"id":"`+cliTaskID+`","display_id":"OPEN-1","title":"T","version":1}`))
	if err := h.run("task", "create", "--space", "OPEN", "--title", "T"); err != nil {
		t.Fatal(err)
	}
	posts := center.requests("POST", base+"/spaces/"+cliSpaceID+"/tasks")
	if len(posts) != 2 || posts[0].Key == "" || posts[0].Key != posts[1].Key || h.sleeps != 1 {
		t.Fatalf("create posts = %#v sleeps = %d", posts, h.sleeps)
	}
	// 指定的 --idempotency-key 原样发送；格式不合法的本地拒绝。
	center.json("POST "+base+"/spaces/"+cliSpaceID+"/tasks", 201, `{"id":"`+cliTaskID+`","display_id":"OPEN-2","title":"T","version":1}`)
	if err := h.run("task", "create", "--space", "OPEN", "--title", "T", "--idempotency-key", "my-key-12345"); err != nil {
		t.Fatal(err)
	}
	if last := center.requests("POST", base+"/spaces/"+cliSpaceID+"/tasks"); last[len(last)-1].Key != "my-key-12345" {
		t.Fatalf("key = %q", last[len(last)-1].Key)
	}
	if code := exitCodeOf(h.run("task", "create", "--space", "OPEN", "--title", "T", "--idempotency-key", "x")); code != cliExitUsage {
		t.Fatalf("short key exit = %d", code)
	}
	// 评论：重试沿用同一个 client_comment_id。
	center.json("GET "+base+"/tasks/by-display-id/OPEN-1", 200, `{"id":"`+cliTaskID+`","space_id":"`+cliSpaceID+`","display_id":"OPEN-1","version":3}`)
	center.handle("POST "+base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID+"/comments", failFirst(201, `{"id":"c1"}`))
	if err := h.run("task", "comment", "add", "OPEN-1", "--body", "hi"); err != nil {
		t.Fatal(err)
	}
	comments := center.requests("POST", base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID+"/comments")
	var first, second struct {
		ClientCommentID string `json:"client_comment_id"`
	}
	if len(comments) != 2 || json.Unmarshal([]byte(comments[0].Body), &first) != nil || json.Unmarshal([]byte(comments[1].Body), &second) != nil ||
		first.ClientCommentID == "" || first != second {
		t.Fatalf("comment posts = %#v", comments)
	}
	// 没有幂等键的写请求（update）网络错误不重试，退出码 5。
	center.handle("PATCH "+base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID, failFirst(200, `{}`))
	err := h.run("task", "update", "OPEN-1", "--title", "New")
	if exitCodeOf(err) != cliExitUnreachable || len(center.requests("PATCH", base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID)) != 1 {
		t.Fatalf("update err = %v", err)
	}
	// 读请求同样不重试。
	center.handle("GET "+base+"/tasks/by-display-id/OPEN-9", failFirst(200, `{}`))
	before := len(center.requests("GET", base+"/tasks/by-display-id/OPEN-9"))
	if code := exitCodeOf(h.run("task", "get", "OPEN-9")); code != cliExitUnreachable || len(center.requests("GET", base+"/tasks/by-display-id/OPEN-9"))-before != 1 {
		t.Fatalf("get exit = %d", code)
	}
}

func TestCLIAllStopsAtLimit(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	base := "/api/v1/organizations/" + cliOrgID
	center.json("GET "+base+"/spaces", 200, `{"spaces":[{"id":"`+cliSpaceID+`","prefix":"OPEN","name":"Open"}]}`)
	var mu sync.Mutex
	var limits []string
	center.handle("GET "+base+"/spaces/"+cliSpaceID+"/tasks", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		limits = append(limits, r.URL.Query().Get("limit"))
		mu.Unlock()
		n := 100
		fmt.Sscan(r.URL.Query().Get("limit"), &n)
		var items []string
		for i := 0; i < n; i++ {
			items = append(items, `{"display_id":"OPEN-1","title":"t"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tasks":[%s],"next_cursor":"more-%d"}`, strings.Join(items, ","), len(limits))
	})
	if err := h.run("task", "list", "--space", "OPEN", "--all", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tasks      []json.RawMessage `json:"tasks"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) != maxAllItems || len(limits) != 10 || out.NextCursor != "more-10" {
		t.Fatalf("items = %d, requests = %d, next = %q", len(out.Tasks), len(limits), out.NextCursor)
	}
	if !strings.Contains(h.stderr.String(), "--cursor more-10") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	// 指定更小的 --limit 时页大小受限，但总量仍不超过上限。
	limits = nil
	if err := h.run("task", "list", "--space", "OPEN", "--all", "--limit", "7", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(h.stdout.Bytes(), &out)
	if len(out.Tasks) != maxAllItems || limits[0] != "7" {
		t.Fatalf("items = %d, first limit = %v", len(out.Tasks), limits[:1])
	}
	// 游标不前进时中止，防止死循环。
	center.json("GET "+base+"/spaces/"+cliSpaceID+"/tasks", 200, `{"tasks":[{"display_id":"OPEN-1"}],"next_cursor":"same"}`)
	if code := exitCodeOf(h.run("task", "list", "--space", "OPEN", "--all", "--cursor", "same")); code != cliExitGeneral {
		t.Fatalf("stuck cursor exit = %d", code)
	}
}

func TestCLITaskFlowAndFlagOrder(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	base := "/api/v1/organizations/" + cliOrgID
	statusID := "00000000-0000-4000-8000-0000000000f1"
	center.json("GET "+base+"/spaces", 200, `{"spaces":[{"id":"`+cliSpaceID+`","prefix":"OPEN","name":"Open"}]}`)
	center.json("GET "+base+"/task-statuses", 200, `{"statuses":[{"id":"`+statusID+`","name":"In Progress"}]}`)
	center.json("GET "+base+"/tasks/by-display-id/OPEN-12", 200, `{"id":"`+cliTaskID+`","space_id":"`+cliSpaceID+`","display_id":"OPEN-12","title":"Fix","status_name":"Todo","priority":"HIGH","version":7,"assignees":[{"name":"Ann"}]}`)
	center.json("PATCH "+base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID, 200, `{"id":"`+cliTaskID+`","display_id":"OPEN-12","title":"Fix","version":8}`)
	// 选项在位置参数之后也能解析。
	if err := h.run("task", "get", "OPEN-12", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), `"version":7`) {
		t.Fatalf("stdout = %s", h.stdout.String())
	}
	if err := h.run("task", "get", "OPEN-12"); err != nil || !strings.Contains(h.stdout.String(), "Ann") {
		t.Fatalf("table = %s, %v", h.stdout.String(), err)
	}
	// 更新：状态名解析为 ID，version 取自刚读到的任务；"none" 清空日期。
	if err := h.run("task", "update", "OPEN-12", "--status", "in progress", "--due", "none", "--priority", "urgent"); err != nil {
		t.Fatal(err)
	}
	patch := center.requests("PATCH", base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID)[0]
	var body map[string]any
	_ = json.Unmarshal([]byte(patch.Body), &body)
	if body["status_id"] != statusID || body["version"] != float64(7) || body["priority"] != "URGENT" || body["due_date"] != nil {
		t.Fatalf("patch body = %s", patch.Body)
	}
	if _, has := body["due_date"]; !has {
		t.Fatalf("due_date null missing: %s", patch.Body)
	}
	// 没有任何改动字段 → 参数错误；乐观锁冲突 → 7。
	if code := exitCodeOf(h.run("task", "update", "OPEN-12")); code != cliExitUsage {
		t.Fatalf("empty update exit = %d", code)
	}
	center.apiError("PATCH "+base+"/spaces/"+cliSpaceID+"/tasks/"+cliTaskID, 409, "VERSION_CONFLICT")
	if code := exitCodeOf(h.run("task", "update", "OPEN-12", "--title", "x")); code != cliExitConflict {
		t.Fatalf("conflict exit = %d", code)
	}
	// --mine 先查 /me，再用 assignee=user:<id> 过滤。
	center.json("GET "+base+"/spaces/"+cliSpaceID+"/tasks", 200, `{"tasks":[{"display_id":"OPEN-12","status_name":"Todo","priority":"HIGH","title":"Fix"}],"next_cursor":""}`)
	if err := h.run("task", "list", "--space", "open", "--mine", "--status", "In Progress", "--q", "fix"); err != nil {
		t.Fatal(err)
	}
	list := center.requests("GET", base+"/spaces/"+cliSpaceID+"/tasks")[0]
	for _, want := range []string{"assignee=user%3A" + cliUserID, "status_id=" + statusID, "q=fix"} {
		if !strings.Contains(list.Query, want) {
			t.Fatalf("query %q lacks %q", list.Query, want)
		}
	}
	// 控制字符不会原样输出到终端。
	center.json("GET "+base+"/spaces/"+cliSpaceID+"/tasks", 200, "{\"tasks\":[{\"display_id\":\"OPEN-1\",\"title\":\"a\\u001b[31mred\"}],\"next_cursor\":\"\"}")
	if err := h.run("task", "list", "--space", "OPEN"); err != nil || strings.ContainsRune(h.stdout.String(), 0x1b) {
		t.Fatalf("escape leaked: %q, %v", h.stdout.String(), err)
	}
}

func TestCLIRunCancelAndConversation(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	base := "/api/v1/organizations/" + cliOrgID
	runID := "00000000-0000-4000-8000-0000000000f2"
	convID := "00000000-0000-4000-8000-0000000000f3"
	center.json("POST "+base+"/runs/"+runID+"/cancel", 200, `{"run":{"id":"`+runID+`","state":"CANCELED"},"replayed":false}`)
	if err := h.run("run", "cancel", runID); err != nil {
		t.Fatal(err)
	}
	if key := center.requests("POST", base+"/runs/"+runID+"/cancel")[0].Key; key == "" {
		t.Fatal("cancel without Idempotency-Key")
	}
	center.json("GET "+base+"/conversations/"+convID, 200, `{"id":"`+convID+`","version":4}`)
	center.json("POST "+base+"/conversations/"+convID+"/complete", 200, `{"id":"`+convID+`"}`)
	if err := h.run("conversation", "complete", convID, "--complete-task"); err != nil {
		t.Fatal(err)
	}
	if body := center.requests("POST", base+"/conversations/"+convID+"/complete")[0].Body; !strings.Contains(body, `"version":4`) || !strings.Contains(body, `"complete_task":true`) {
		t.Fatalf("complete body = %s", body)
	}
	// 消息正文可以来自 stdin；不是 UUID 的会话 ID 本地拒绝。
	center.json("POST "+base+"/conversations/"+convID+"/messages", 201, `{"id":"m1","seq":2}`)
	h.stdin = "hello from stdin\n"
	if err := h.run("conversation", "send", convID); err != nil {
		t.Fatal(err)
	}
	if body := center.requests("POST", base+"/conversations/"+convID+"/messages")[0].Body; !strings.Contains(body, "hello from stdin") || !strings.Contains(body, "client_message_id") {
		t.Fatalf("send body = %s", body)
	}
	h.stdin = ""
	if code := exitCodeOf(h.run("conversation", "send", convID)); code != cliExitUsage {
		t.Fatalf("empty body exit = %d", code)
	}
	if code := exitCodeOf(h.run("conversation", "get", "abc")); code != cliExitUsage {
		t.Fatalf("bad id exit = %d", code)
	}
}

func TestCLIWarnsForPlainHTTPRemoteAndLogoutKeepsServerToken(t *testing.T) {
	h := newCLIHarness(t)
	h.stdin = cliTestToken
	// 不会真正连接：.invalid 域名解析失败，只关心警告。
	_ = h.run("auth", "login", "--server", "http://center.invalid:8080")
	if !strings.Contains(h.stderr.String(), "plain http") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	assertNoToken(t, h.stderr.String(), h.stdout.String())
	center := newCLICenter(t)
	h.login(center)
	if strings.Contains(h.stderr.String(), "plain http") {
		t.Fatalf("loopback http should not warn: %q", h.stderr.String())
	}
	if err := h.run("auth", "logout"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "revoke") {
		t.Fatalf("logout hint missing: %q", h.stdout.String())
	}
	if _, err := os.Stat(filepath.Join(h.dir, "cli", "tokens", "work")); !os.IsNotExist(err) {
		t.Fatalf("token file left: %v", err)
	}
	if code := exitCodeOf(h.run("space", "list")); code == 0 {
		t.Fatal("space list succeeded after logout")
	}
}

func TestCLIProfileListAndUse(t *testing.T) {
	center, h := newCLICenter(t), newCLIHarness(t)
	h.login(center)
	h.stdin = cliTestToken
	if err := h.run("auth", "login", "--profile", "second", "--server", center.server.URL); err != nil {
		t.Fatal(err)
	}
	if err := h.run("profile", "list", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Current  string `json:"current"`
		Profiles []struct {
			Name     string `json:"name"`
			HasToken bool   `json:"has_token"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &listed); err != nil || listed.Current != "work" || len(listed.Profiles) != 2 {
		t.Fatalf("list = %s, %v", h.stdout.String(), err)
	}
	assertNoToken(t, h.stdout.String())
	if err := h.run("profile", "use", "second"); err != nil {
		t.Fatal(err)
	}
	if code := exitCodeOf(h.run("profile", "use", "missing")); code != cliExitNotFound {
		t.Fatalf("use missing exit = %d", code)
	}
	if code := exitCodeOf(h.run("profile", "use", "../x")); code != cliExitNotFound {
		t.Fatalf("path-like name exit = %d", code)
	}
	if err := h.run("org", "use", "acme"); err != nil {
		t.Fatal(err)
	}
	profiles, _ := os.ReadFile(filepath.Join(h.dir, "cli", "profiles.json"))
	if !strings.Contains(string(profiles), cliOrgID) {
		t.Fatalf("org not stored: %s", profiles)
	}
	if code := exitCodeOf(h.run("org", "use", "unknown-org")); code != cliExitNotFound {
		t.Fatalf("unknown org exit = %d", code)
	}
}
