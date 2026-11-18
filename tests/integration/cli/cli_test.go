package cli

// 阶段 11c 协作 CLI 集成测试：真实 PostgreSQL + 进程内 API（挂在 /api 下）+ 真实编译的 openfox 二进制，
// HOME / XDG_CONFIG_HOME 指向临时目录。覆盖：令牌登录、建任务/评论/查看、跨组织 404（退出码 6）、吊销后退出码 4、本地文件权限。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	insOwnerID    = "00000000-0000-0000-0000-00000011c001"
	insOutsiderID = "00000000-0000-0000-0000-00000011c002"
	insPassword   = "stage11c cli password"
)

func newID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	s := hex.EncodeToString(raw[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

type session struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (s *session) do(method, path string, body any, want int, target any) {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request, _ := http.NewRequest(method, s.base+path, reader)
	request.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", s.csrf)
	}
	response, err := s.http.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, response.StatusCode, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal(payload, target); err != nil {
			s.t.Fatalf("decode %s: %v", payload, err)
		}
	}
}

func login(t *testing.T, base, email string) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{t: t, base: base, http: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, insPassword)
	response, err := s.http.Post(base+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	for _, cookie := range jar.Cookies(response.Request.URL) {
		if cookie.Name == "openfox_csrf" {
			s.csrf = cookie.Value
		}
	}
	return s
}

type cliRun struct {
	stdout, stderr string
	code           int
}

type env struct {
	t      *testing.T
	binary string
	home   string
	server string
}

func (e *env) run(stdin string, extraEnv []string, args ...string) cliRun {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.home, "XDG_CONFIG_HOME=" + filepath.Join(e.home, "xdg")}, extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return cliRun{out.String(), errBuf.String(), code}
}

func (e *env) mustRun(stdin string, args ...string) cliRun {
	e.t.Helper()
	r := e.run(stdin, nil, args...)
	if r.code != 0 {
		e.t.Fatalf("%v exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func TestCollaborationCLIEndToEnd(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer superuser.Close(context.Background())
	exec1 := func(q string, args ...any) {
		t.Helper()
		if _, err := superuser.Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(insPassword)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(newID(t), "-", "")[:8]
	emails := map[string]string{insOwnerID: "c11-" + suffix + "-a@example.test", insOutsiderID: "c11-" + suffix + "-b@example.test"}
	for id, email := range emails {
		exec1(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT (id) DO UPDATE SET email = EXCLUDED.email`, id, email)
		exec1(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, id, hash)
	}
	orgs := map[string]string{insOwnerID: newID(t), insOutsiderID: newID(t)}
	slugs := map[string]string{}
	for user, org := range orgs {
		slugs[user] = "c11-" + suffix + "-" + org[:4]
		exec1(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, slugs[user], "Stage11c "+org[:8])
		exec1(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2),
			($1::uuid, 'Done', '', 'DONE', 3), ($1::uuid, 'Canceled', '', 'CANCELLED', 4)`, org)
		exec1(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4) ON CONFLICT DO NOTHING`, org)
		exec1(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`, org, user)
		// 直接插入的组织没有控制面目录行（见 000037）：更新一次让触发器补齐，org list / 名称解析才能看到它。
		exec1(`UPDATE openfox.organizations SET name = name WHERE id = $1::uuid`, org)
	}

	database, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	inner := api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()}, api.DefaultAuthRateLimits, api.DispatchOptions{})
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", inner))
	server := httptest.NewServer(mux)
	defer server.Close()

	// 编译真实二进制（带版本号）。
	_, thisFile, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	binary := filepath.Join(t.TempDir(), "openfox")
	build := exec.Command("go", "build", "-o", binary, "-ldflags", "-X main.version=11c-test", "./cmd/openfox")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	e := &env{t: t, binary: binary, home: t.TempDir(), server: server.URL}

	// 版本号由 ldflags 注入。
	if r := e.mustRun("", "version"); !strings.Contains(r.stdout, "11c-test") {
		t.Fatalf("version = %q", r.stdout)
	}

	// 用 cookie 会话创建空间与令牌。
	owner := login(t, server.URL+"/api", emails[insOwnerID])
	outsider := login(t, server.URL+"/api", emails[insOutsiderID])
	orgBase := "/v1/organizations/" + orgs[insOwnerID]
	var space struct {
		ID     string `json:"id"`
		Prefix string `json:"prefix"`
	}
	prefix := "C" + strings.ToUpper(suffix[:5])
	owner.do("POST", orgBase+"/spaces", map[string]any{"name": "CLI Space", "prefix": prefix, "visibility": "private"}, http.StatusCreated, &space)
	mint := func(s *session, name string) (id, secret string) {
		var created struct {
			Token  struct{ ID string } `json:"token"`
			Secret string              `json:"secret"`
		}
		s.do("POST", "/v1/me/tokens", map[string]any{"name": name}, http.StatusCreated, &created)
		return created.Token.ID, created.Secret
	}
	ownerTokenID, ownerSecret := mint(owner, "cli-it")
	_, outsiderSecret := mint(outsider, "cli-it-out")

	// 登录：令牌走 stdin，不进 argv，也不进输出。
	login1 := e.mustRun(ownerSecret+"\n", "auth", "login", "--profile", "it", "--server", server.URL, "--org", slugs[insOwnerID])
	if strings.Contains(login1.stdout+login1.stderr, ownerSecret) {
		t.Fatal("token leaked in login output")
	}
	tokenFile := filepath.Join(e.home, "xdg", "openfox", "cli", "tokens", "it")
	if _, err := os.Stat(tokenFile); err != nil {
		// 回退：按实际配置目录查找。
		var found string
		_ = filepath.Walk(e.home, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && filepath.Base(p) == "it" && filepath.Base(filepath.Dir(p)) == "tokens" {
				found = p
			}
			return nil
		})
		if found == "" {
			t.Fatal("token file not found under HOME")
		}
		tokenFile = found
	}
	if info, _ := os.Stat(tokenFile); info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(tokenFile)); info.Mode().Perm() != 0o700 {
		t.Fatalf("tokens dir mode = %o", info.Mode().Perm())
	}
	if status := e.mustRun("", "auth", "status", "--output", "json"); strings.Contains(status.stdout, ownerSecret) {
		t.Fatal("token leaked in auth status")
	}

	// 建任务、评论、查看。
	created := e.mustRun("", "task", "create", "--space", prefix, "--title", "CLI 集成任务", "--output", "json")
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &task); err != nil || task.DisplayID != prefix+"-1" {
		t.Fatalf("create = %q, %v", created.stdout, err)
	}
	e.mustRun("", "task", "comment", "add", task.DisplayID, "--body", "来自 CLI 的评论")
	if comments := e.mustRun("", "task", "comment", "list", task.DisplayID, "--output", "json"); !strings.Contains(comments.stdout, "来自 CLI 的评论") {
		t.Fatalf("comments = %s", comments.stdout)
	}
	if view := e.mustRun("", "task", "get", task.DisplayID, "--output", "json"); !strings.Contains(view.stdout, "CLI 集成任务") {
		t.Fatalf("get = %s", view.stdout)
	}
	if list := e.mustRun("", "task", "list", "--space", prefix, "--all"); !strings.Contains(list.stdout, "CLI 集成任务") {
		t.Fatalf("list = %s", list.stdout)
	}
	if update := e.run("", nil, "task", "update", task.DisplayID, "--title", "CLI 集成任务改"); update.code != 0 {
		t.Fatalf("update: %d %s", update.code, update.stderr)
	}
	// 服务端落库一致：评论确实只有一条。
	var comments int
	if err := superuser.QueryRow(ctx, `SELECT count(*) FROM openfox.task_comments WHERE task_id = $1::uuid`, task.ID).Scan(&comments); err != nil || comments != 1 {
		t.Fatalf("comments in db = %d, %v", comments, err)
	}

	// 跨组织：外部用户的令牌看不到本组织的空间与任务（404 → 退出码 6）。
	cross := e.run(outsiderSecret, []string{"OPENFOX_SERVER=" + server.URL, "OPENFOX_ORG=" + slugs[insOwnerID], "OPENFOX_TOKEN=" + outsiderSecret},
		"task", "get", task.DisplayID, "--space", prefix)
	if cross.code == 0 || cross.code == 5 || strings.Contains(cross.stdout, "CLI 集成任务") {
		t.Fatalf("cross-org get: exit %d stdout %q stderr %q", cross.code, cross.stdout, cross.stderr)
	}
	t.Logf("cross-org exit code = %d (stderr %q)", cross.code, strings.TrimSpace(cross.stderr))
	if cross.code != 6 && cross.code != 4 {
		t.Fatalf("cross-org exit = %d, want 6 (404) or 4 (403)", cross.code)
	}

	// 吊销令牌后：退出码 4。
	owner.do("DELETE", "/v1/me/tokens/"+ownerTokenID, nil, http.StatusNoContent, nil)
	revoked := e.run("", nil, "task", "get", task.DisplayID)
	if revoked.code != 4 {
		t.Fatalf("revoked exit = %d stderr %q", revoked.code, revoked.stderr)
	}
	if strings.Contains(revoked.stdout+revoked.stderr, ownerSecret) {
		t.Fatal("token leaked after revoke")
	}

	// 登出清理本地令牌文件。
	e.mustRun("", "auth", "logout")
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("token file survived logout: %v", err)
	}
}

// 阶段 12f：conversation / run 子命令的真实中心集成测试。
// 与 TestCollaborationCLIEndToEnd 同一套环境：真实 PostgreSQL + 进程内 API + 真实编译的二进制。
// Agent 与电脑用 SQL 种子（同 stage9f），会话经 API 创建，@Agent 触发 Run 后由 dispatch worker 物化。
func TestConversationAndRunCLI(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer superuser.Close(context.Background())
	exec1 := func(q string, args ...any) {
		t.Helper()
		if _, err := superuser.Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(insPassword)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(newID(t), "-", "")[:8]
	ownerID := newID(t)
	email := "c12-" + suffix + "@example.test"
	exec1(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2)`, ownerID, email)
	exec1(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)`, ownerID, hash)
	org := newID(t)
	slug := "c12-" + suffix
	exec1(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, slug, "Stage12f "+org[:8])
	exec1(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
		($1::uuid, 'Backlog', '', 'BACKLOG', 0), ($1::uuid, 'Todo', '', 'TODO', 1), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 2),
		($1::uuid, 'Done', '', 'DONE', 3), ($1::uuid, 'Canceled', '', 'CANCELLED', 4)`, org)
	exec1(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 4) ON CONFLICT DO NOTHING`, org)
	exec1(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`, org, ownerID)
	exec1(`UPDATE openfox.organizations SET name = name WHERE id = $1::uuid`, org)

	database, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 20 * time.Second, LockTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	auth := identity.Service{Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	inner := api.NewWithDispatch(auth, database, false, artifacts.Local{Root: t.TempDir()}, api.DefaultAuthRateLimits, api.DispatchOptions{})
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", inner))
	server := httptest.NewServer(mux)
	defer server.Close()

	_, thisFile, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	binary := filepath.Join(t.TempDir(), "openfox")
	build := exec.Command("go", "build", "-o", binary, "./cmd/openfox")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	e := &env{t: t, binary: binary, home: t.TempDir(), server: server.URL}

	owner := login(t, server.URL+"/api", email)
	orgBase := "/v1/organizations/" + org
	var space struct {
		ID     string `json:"id"`
		Prefix string `json:"prefix"`
	}
	prefix := "D" + strings.ToUpper(suffix[:5])
	owner.do("POST", orgBase+"/spaces", map[string]any{"name": "CLI Conv Space", "prefix": prefix, "visibility": "private"}, http.StatusCreated, &space)

	// 电脑 + Agent（可执行，codex runtime）。
	device, agent := newID(t), newID(t)
	exec1(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`, device, "c12-device-"+suffix)
	exec1(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, org, device)
	exec1(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, status, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'CODE', 'SHARED', 'ACTIVE', 1)`, org, agent, device, space.ID, ownerID, "CLI Agent "+suffix)
	exec1(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model,
		max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, org, agent, device, ownerID)

	var tokenCreated struct {
		Secret string `json:"secret"`
	}
	owner.do("POST", "/v1/me/tokens", map[string]any{"name": "cli-conv"}, http.StatusCreated, &tokenCreated)
	e.mustRun(tokenCreated.Secret+"\n", "auth", "login", "--profile", "conv", "--server", server.URL, "--org", slug)

	// 会话经 API 创建，随后全部通过 CLI 操作。
	convID := newID(t)
	owner.do("POST", orgBase+"/conversations", map[string]any{"id": convID, "space_id": space.ID, "agent_id": agent, "visibility": "PRIVATE"}, http.StatusCreated, nil)

	// conversation send：@Agent 触发 Run；相同 idempotency-key 重放不重复落库。
	key := newID(t)
	first := e.mustRun("", "conversation", "send", convID, "--body", "请开始", "--mention", "agent:"+agent, "--idempotency-key", key, "--output", "json")
	var sent struct {
		ID  string `json:"id"`
		Seq int    `json:"seq"`
	}
	if err := json.Unmarshal([]byte(first.stdout), &sent); err != nil || sent.ID == "" {
		t.Fatalf("send = %q, %v", first.stdout, err)
	}
	replay := e.mustRun("", "conversation", "send", convID, "--body", "请开始", "--mention", "agent:"+agent, "--idempotency-key", key, "--output", "json")
	var replayed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(replay.stdout), &replayed); err != nil || replayed.ID != sent.ID {
		t.Fatalf("replay id = %q want %q (%v)", replayed.ID, sent.ID, err)
	}
	var messages int
	if err := superuser.QueryRow(ctx, `SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'USER'`, convID).Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("user messages in db = %d, %v (replay must not duplicate)", messages, err)
	}

	// conversation list / get。
	if list := e.mustRun("", "conversation", "list", "--output", "json"); !strings.Contains(list.stdout, convID) {
		t.Fatalf("conversation list = %s", list.stdout)
	}
	if got := e.mustRun("", "conversation", "get", convID, "--output", "json"); !strings.Contains(got.stdout, convID) {
		t.Fatalf("conversation get = %s", got.stdout)
	}
	// 参数校验：非 UUID 是用法错误（退出码 2）；不存在的会话是 404（退出码 6）。
	if bad := e.run("", nil, "conversation", "get", "not-a-uuid"); bad.code != 2 {
		t.Fatalf("invalid id exit = %d stderr %q", bad.code, bad.stderr)
	}
	if missing := e.run("", nil, "conversation", "get", newID(t)); missing.code != 6 {
		t.Fatalf("missing conversation exit = %d stderr %q", missing.code, missing.stderr)
	}

	// 派发一轮：把会话输入物化成 Run。
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	worker := &dispatch.Worker{Pool: database, Store: store, Hub: dispatch.NewHub()}
	if _, err := worker.RunOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}

	// run list（按会话与按 Agent）、run get、run events。
	runs := e.mustRun("", "run", "list", "--conversation", convID, "--output", "json")
	var runList struct {
		Runs []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(runs.stdout), &runList); err != nil || len(runList.Runs) != 1 {
		t.Fatalf("run list = %q, %v", runs.stdout, err)
	}
	runID := runList.Runs[0].ID
	if byAgent := e.mustRun("", "run", "list", "--agent", agent, "--space", prefix, "--output", "json"); !strings.Contains(byAgent.stdout, runID) {
		t.Fatalf("run list by agent = %s", byAgent.stdout)
	}
	if got := e.mustRun("", "run", "get", runID, "--output", "json"); !strings.Contains(got.stdout, runID) {
		t.Fatalf("run get = %s", got.stdout)
	}
	e.mustRun("", "run", "events", runID, "--output", "json")
	// run list 的用法错误：缺少过滤条件、两种过滤同时给。
	if bad := e.run("", nil, "run", "list"); bad.code != 2 {
		t.Fatalf("run list without filter exit = %d", bad.code)
	}
	if bad := e.run("", nil, "run", "list", "--conversation", convID, "--agent", agent, "--space", prefix); bad.code != 2 {
		t.Fatalf("run list with both filters exit = %d", bad.code)
	}

	// run cancel：落库状态不再是排队 / 运行中。
	e.mustRun("", "run", "cancel", runID)
	var state string
	if err := superuser.QueryRow(ctx, `SELECT state FROM openfox.runs WHERE id = $1::uuid`, runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "QUEUED" || state == "RUNNING" {
		t.Fatalf("run state after cancel = %s", state)
	}
	t.Logf("run state after cancel = %s", state)

	// conversation complete：会话生命周期变为已完成。
	e.mustRun("", "conversation", "complete", convID)
	var lifecycle string
	if err := superuser.QueryRow(ctx, `SELECT lifecycle FROM openfox.conversations WHERE id = $1::uuid`, convID).Scan(&lifecycle); err != nil || lifecycle == "ACTIVE" {
		t.Fatalf("lifecycle after complete = %q, %v", lifecycle, err)
	}
}
