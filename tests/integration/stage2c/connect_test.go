package stage2c

// 连接电脑与 Runtime 的端到端测试：真实中心（/api 前缀 + 派发 worker + Hub）+
// 编译出的 openfox 二进制（子进程）。流程：接口签发 v2 安装令牌 → `openfox computer register --no-service`
// → `openfox daemon run --config` → 心跳上线与 storage 上报 → 派发一次假 Runtime 的 Run（环境变量注入、
// custom_args 白名单、输出脱敏）→ 远程命令 runtime_probe / runtime_test / storage_cleanup 回执 →
// 会话令牌续期（--token-renew-before 测试钩子让 24 小时的会话每隔几秒续期一次）。
// 不启动真实 Codex，不安装 launchd。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/dispatch"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/secrets"
)

const (
	ownerID  = "00000000-0000-0000-0000-0000002c0001"
	password = "stage2c connect password"
	// secretValue 是 Agent 环境变量的明文，不得出现在日志、事件、消息与结果里。
	secretValue = "sk-stage2c-E2E-PLAINTEXT-4242"
)

// testKey 是测试专用主密钥（32 字节），不用于任何真实环境。
var testKey = []byte("openfox-stage2c-connect-key-32b!")

type env struct {
	t         *testing.T
	ctx       context.Context
	superuser *pgx.Conn
	center    string
	org       string
	http      *http.Client
	csrf      string
	work      string
	binary    string
}

func newID(t *testing.T) string {
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

func newEnv(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	superuser, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, superuser: superuser, org: newID(t)}
	e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'c2c-owner@example.test') ON CONFLICT DO NOTHING`, ownerID)
	e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, credential_version = 1, status = 'ACTIVE'`, ownerID, hash)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Stage 2c')`, e.org, "c2c-"+e.org[:8])
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
		($1::uuid, 'Todo', '', 'TODO', 0), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 1), ($1::uuid, 'Done', '', 'DONE', 2)`, e.org)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`, e.org, ownerID)
	e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 8)`, e.org)

	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 16, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	box, err := secrets.New(testKey)
	if err != nil {
		t.Fatal(err)
	}
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 10 * time.Second, LeaseRenew: time.Second, PollInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	hub := dispatch.NewHub()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	e.center = "http://" + listener.Addr().String()
	handler := api.NewWithDispatch(identity.Service{Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}, pool, false, artifacts.Local{Root: t.TempDir()}, api.DefaultAuthRateLimits,
		api.DispatchOptions{Store: store, Hub: hub, Secrets: box, PublicURL: e.center})
	// 与 openfox-server 一致：API 挂在 /api 前缀下。
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", handler))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	workerContext, stopWorker := context.WithCancel(ctx)
	t.Cleanup(stopWorker)
	go func() { _ = (&dispatch.Worker{Pool: pool, Store: store, Hub: hub}).Run(workerContext) }()

	e.work = t.TempDir()
	e.binary = filepath.Join(e.work, "bin", "openfox")
	build := exec.CommandContext(ctx, "go", "build", "-o", e.binary, "openfox/cmd/openfox")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	e.login()
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.superuser.Exec(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

func (e *env) scalar(target any, query string, args ...any) {
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, query, args...).Scan(target); err != nil {
		e.t.Fatalf("query %s: %v", query, err)
	}
}

func (e *env) login() {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.http = &http.Client{Jar: jar, Timeout: 10 * time.Second}
	payload := fmt.Sprintf(`{"email":"c2c-owner@example.test","password":%q,"client_kind":"web"}`, password)
	response, err := e.http.Post(e.center+"/api/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login status = %d", response.StatusCode)
	}
	parsed, _ := neturl.Parse(e.center)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			e.csrf = cookie.Value
		}
	}
}

// call 以组织 owner 身份调用 /api/v1/organizations/{org}/<path>，检查状态码并解码响应。
func (e *env) call(want int, method, path string, body, out any) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(e.ctx, method, e.center+"/api/v1/organizations/"+e.org+"/"+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", e.csrf)
	}
	response, err := e.http.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, response.StatusCode, payload, want)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			e.t.Fatalf("decode %s: %v", payload, err)
		}
	}
}

// fakeCodex 是遵循 app-server 协议的假 CLI：记录参数与看到的环境，最终回复里故意带出环境变量明文（验证脱敏）。
const fakeCodex = `#!/bin/sh
# Runtime 子进程只拿到白名单环境，状态目录由测试写死在脚本里。
state='@STATE@'
args="$*"
while [ "$1" = "-c" ]; do shift 2; done
case "$1" in
  --version) echo 'codex-cli 0.0.1-e2e'; exit 0 ;;
  login) echo 'Logged in using e2e fixture'; exit 0 ;;
  app-server) if [ "$2" = "--help" ]; then echo 'codex app-server'; exit 0; fi ;;
  *) exit 2 ;;
esac
printf '%s\n' "$args" >> "$state/args"
{ echo "API_TOKEN=${API_TOKEN:-}"; echo "HOME=$HOME"; echo "GIT_AUTHOR_EMAIL=${GIT_AUTHOR_EMAIL:-}"; } >> "$state/env"
IFS= read -r line || exit 0
echo '{"id":1,"result":{"userAgent":"e2e"}}'
IFS= read -r line || exit 0
IFS= read -r line || exit 0
case "$line" in
  *model/list*)
    echo '{"id":2,"result":{"data":[{"id":"gpt-e2e","model":"gpt-e2e","displayName":"GPT E2E","hidden":false,"isDefault":true}],"nextCursor":null}}'
    while IFS= read -r line; do :; done; exit 0 ;;
esac
# 每次真实 Run（有 thread）记录独立 CODEX_HOME 里的技能，目录按调用顺序编号为 run-1、run-2……
n=1; while ! mkdir "$state/run-$n" 2>/dev/null; do n=$((n+1)); done
echo "$CODEX_HOME" > "$state/run-$n/home"
if [ -d "$CODEX_HOME/skills" ]; then
  (cd "$CODEX_HOME/skills" && find . -type f | sort) > "$state/run-$n/files"
  (cd "$CODEX_HOME/skills" && find . -type f ! -perm 600) > "$state/run-$n/badfiles"
  (cd "$CODEX_HOME/skills" && find . -type d ! -perm 700) > "$state/run-$n/baddirs"
  cat "$CODEX_HOME/skills/alpha/SKILL.md" > "$state/run-$n/alpha-skill-md" 2>/dev/null
  cat "$CODEX_HOME/skills/alpha/scripts/run.sh" > "$state/run-$n/alpha-run-sh" 2>/dev/null
fi
echo '{"id":2,"result":{"thread":{"id":"th-e2e"}}}'
IFS= read -r turn || exit 0
echo '{"id":3,"result":{"turn":{"id":"tu-e2e","status":"inProgress"}}}'
printf '{"method":"item/completed","params":{"threadId":"th-e2e","item":{"type":"agentMessage","text":"OK key=%s","phase":"final_answer"}}}\n' "$API_TOKEN"
echo '{"method":"turn/completed","params":{"threadId":"th-e2e","turn":{"id":"tu-e2e","status":"completed"}}}'
while IFS= read -r line; do :; done
exit 0
`

type command struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"error_code"`
}

func (e *env) runCommand(deviceID, kind string, params any) command {
	e.t.Helper()
	var created struct {
		Command command `json:"command"`
	}
	e.call(http.StatusAccepted, http.MethodPost, "devices/"+deviceID+"/commands", map[string]any{"kind": kind, "params": params}, &created)
	var current struct {
		Command command `json:"command"`
	}
	waitFor(e.t, kind+" command", 45*time.Second, func() bool {
		e.call(http.StatusOK, http.MethodGet, "devices/"+deviceID+"/commands/"+created.Command.ID, nil, &current)
		return current.Command.Status != "PENDING" && current.Command.Status != "DELIVERED"
	})
	if current.Command.Status != "SUCCEEDED" {
		e.t.Fatalf("%s command = %+v %s", kind, current.Command, current.Command.Result)
	}
	return current.Command
}

func waitFor(t *testing.T, what string, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(contents)
}

func TestComputerRegisterDaemonRunAndRemoteCommands(t *testing.T) {
	e := newEnv(t)
	home := filepath.Join(e.work, "home")
	state := filepath.Join(e.work, "state")
	userCodex := filepath.Join(e.work, "user-codex-home")
	configDir := filepath.Join(e.work, "config")
	for _, dir := range []string{home, state, userCodex} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userCodex, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(e.work, "codex-e2e.sh")
	if err := os.WriteFile(codexPath, []byte(strings.ReplaceAll(fakeCodex, "@STATE@", state)), 0o700); err != nil {
		t.Fatal(err)
	}
	// 子进程使用临时 HOME，避免读写用户真实的配置目录与 ~/.codex。
	childEnv := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + userCodex, "TMPDIR=" + e.work}

	// 1. 接口签发 v2 安装令牌（组织范围）。
	var issued struct {
		Token        string `json:"token"`
		InstallToken string `json:"install_token"`
		Command      string `json:"command"`
		Scope        string `json:"scope"`
	}
	e.call(http.StatusCreated, http.MethodPost, "devices/registration-tokens", map[string]any{"name": "e2e-mac", "scope": "ORGANIZATION"}, &issued)
	if !strings.HasPrefix(issued.InstallToken, "ofx_v2_") || !strings.Contains(issued.Command, issued.InstallToken) {
		t.Fatalf("issued = %+v", issued)
	}

	// 2. openfox computer register --no-service。
	register := exec.CommandContext(e.ctx, e.binary, "computer", "register", "--install-token", issued.InstallToken,
		"--config-dir", configDir, "--no-service", "--supported-version", "0.0.1-e2e", "--runtime-executable", codexPath)
	register.Env = childEnv
	output, err := register.CombinedOutput()
	if err != nil {
		t.Fatalf("register: %v\n%s", err, output)
	}
	if strings.Contains(string(output), issued.InstallToken) || strings.Contains(string(output), issued.Token) {
		t.Fatalf("register output leaked the token:\n%s", output)
	}
	for _, name := range []string{"config.json", "device-key", "device-token"} {
		if info, err := os.Lstat(filepath.Join(configDir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s = %v, %v", name, info, err)
		}
	}
	var config struct {
		DeviceID      string `json:"device_id"`
		TokenFile     string `json:"token_file"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(configDir, "config.json"))), &config); err != nil {
		t.Fatal(err)
	}
	firstToken := strings.TrimSpace(readFile(t, config.TokenFile))
	// 重复注册（未加 --force）被拒绝，退出码 3。
	again := exec.CommandContext(e.ctx, e.binary, "computer", "register", "--install-token", issued.InstallToken, "--config-dir", configDir, "--no-service")
	again.Env = childEnv
	if err := again.Run(); err == nil || again.ProcessState.ExitCode() != 3 {
		t.Fatalf("second register exit = %v", err)
	}

	// 3. openfox daemon run --config（会话令牌每隔约 5 秒续期一次）。
	logPath := filepath.Join(e.work, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket 路径上限约 104 字节：socket、journal 与独立 CODEX_HOME 放在 /tmp 下的短目录。
	runtimeDir, err := os.MkdirTemp("/tmp", "of2c-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	daemonCmd := exec.Command(e.binary, "daemon", "run", "--config", filepath.Join(configDir, "config.json"),
		"--socket", filepath.Join(runtimeDir, "d.sock"),
		"--heartbeat-interval", "500ms", "--offer-poll-interval", "1s", "--token-renew-before", "23h59m55s")
	daemonCmd.Env, daemonCmd.Stdout, daemonCmd.Stderr = childEnv, logFile, logFile
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemonCmd.Wait() }()
	stopped := false
	stopDaemon := func() {
		if stopped {
			return
		}
		stopped = true
		_ = daemonCmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-daemonDone:
		case <-time.After(30 * time.Second):
			_ = daemonCmd.Process.Kill()
			<-daemonDone
		}
		_ = logFile.Close()
	}
	t.Cleanup(stopDaemon)

	// 4. 心跳上线：设备详情出现 storage 与 Runtime 能力。
	type deviceView struct {
		Online            bool            `json:"online"`
		Storage           json.RawMessage `json:"storage"`
		RuntimesCheckedAt *time.Time      `json:"runtimes_checked_at"`
	}
	var device deviceView
	waitFor(t, "device online with storage", 60*time.Second, func() bool {
		device = deviceView{}
		e.call(http.StatusOK, http.MethodGet, "devices/"+config.DeviceID, nil, &device)
		return device.Online && len(device.Storage) > 0 && string(device.Storage) != "null" && device.RuntimesCheckedAt != nil
	})
	var storage struct {
		TotalBytes   *int64 `json:"total_bytes"`
		JournalBytes int64  `json:"journal_bytes"`
	}
	if err := json.Unmarshal(device.Storage, &storage); err != nil || storage.TotalBytes == nil || storage.JournalBytes <= 0 {
		t.Fatalf("storage = %s, %v", device.Storage, err)
	}

	// 5. 派发一次 Run：Agent 环境变量注入 Runtime 子进程，保留名丢弃，custom_args 白名单生效，输出脱敏。
	var grantID string
	e.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, config.DeviceID)
	var space struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "E2E " + e.org[:6], "visibility": "SHARED"}, &space)
	var agent struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "spaces/"+space.ID+"/agents", map[string]any{
		"name": "Coder", "kind": "CODE", "visibility": "SHARED",
		"config": map[string]any{
			"computer_id": config.DeviceID, "grant_id": grantID, "runtime": "codex", "model": "default",
			"custom_args":          []string{"-c", "model_reasoning_effort=high", "-c", "sandbox_mode=danger-full-access"},
			"git_author_name_mode": "MANUAL", "git_author_name": "E2E Bot", "git_author_email_mode": "MANUAL", "git_author_email": "bot@example.test",
		},
	}, &agent)
	agentPath := "spaces/" + space.ID + "/agents/" + agent.ID
	e.call(http.StatusOK, http.MethodPut, agentPath+"/env-vars/API_TOKEN", map[string]any{"value": secretValue}, nil)
	e.call(http.StatusOK, http.MethodPut, agentPath+"/env-vars/HOME", map[string]any{"value": "/tmp/evil-home"}, nil)
	var conversation struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space.ID, "agent_id": agent.ID}, &conversation)
	messageID := newID(t)
	e.call(http.StatusCreated, http.MethodPost, "conversations/"+conversation.ID+"/messages", map[string]any{
		"id": messageID, "client_message_id": "client-" + messageID, "body": "say OK", "mentions": []map[string]string{{"agent_id": agent.ID}},
	}, nil)
	var runID, runState string
	waitFor(t, "run succeeded", 60*time.Second, func() bool {
		err := e.superuser.QueryRow(e.ctx, `SELECT id::text, state FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
			ORDER BY created_at DESC LIMIT 1`, e.org, conversation.ID).Scan(&runID, &runState)
		return err == nil && (runState == "SUCCEEDED" || runState == "FAILED" || runState == "CANCELED")
	})
	if runState != "SUCCEEDED" {
		t.Fatalf("run state = %s\n%s", runState, readFile(t, logPath))
	}
	var reply string
	e.scalar(&reply, `SELECT body FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'AGENT'
		ORDER BY seq DESC LIMIT 1`, e.org, conversation.ID)
	if reply != "OK key=[REDACTED]" {
		t.Fatalf("agent reply = %q", reply)
	}
	runArgs, runEnv := readFile(t, filepath.Join(state, "args")), readFile(t, filepath.Join(state, "env"))
	if !strings.Contains(runArgs, "-c model_reasoning_effort=high app-server") || strings.Contains(runArgs, "sandbox_mode") {
		t.Fatalf("runtime args = %q", runArgs)
	}
	if !strings.Contains(runEnv, "API_TOKEN="+secretValue) || strings.Contains(runEnv, "/tmp/evil-home") || !strings.Contains(runEnv, "GIT_AUTHOR_EMAIL=bot@example.test") {
		t.Fatalf("runtime env = %q", runEnv)
	}
	var leaks, warnings int
	e.scalar(&leaks, `SELECT count(*) FROM openfox.run_events WHERE organization_id = $1::uuid AND safe_payload::text LIKE '%' || $2 || '%'`, e.org, secretValue)
	e.scalar(&warnings, `SELECT count(*) FROM openfox.run_events WHERE organization_id = $1::uuid AND run_id = $2::uuid AND type = 'warning'
		AND (safe_payload::text LIKE '%daemon_environment_reserved%' OR safe_payload::text LIKE '%daemon_custom_arg_dropped%')`, e.org, runID)
	if leaks != 0 || warnings != 2 {
		t.Fatalf("event leaks = %d, daemon warnings = %d", leaks, warnings)
	}

	// 6. 远程命令：runtime_probe / runtime_test / storage_cleanup。
	probe := e.runCommand(config.DeviceID, "runtime_probe", map[string]any{})
	if !strings.Contains(string(probe.Result), `"runtimes"`) || !strings.Contains(string(probe.Result), `"checked_at"`) {
		t.Fatalf("probe result = %s", probe.Result)
	}
	test := e.runCommand(config.DeviceID, "runtime_test", map[string]any{"provider": "codex", "model": "gpt-e2e"})
	var testResult struct {
		OK             bool `json:"ok"`
		ModelAvailable bool `json:"model_available"`
	}
	if err := json.Unmarshal(test.Result, &testResult); err != nil || !testResult.OK || !testResult.ModelAvailable {
		t.Fatalf("runtime_test result = %s, %v", test.Result, err)
	}
	cleanup := e.runCommand(config.DeviceID, "storage_cleanup", map[string]any{})
	var cleanupResult struct {
		FreedBytes *int64 `json:"freed_bytes"`
		Removed    *struct {
			Worktrees int `json:"worktrees"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(cleanup.Result, &cleanupResult); err != nil || cleanupResult.FreedBytes == nil || cleanupResult.Removed == nil {
		t.Fatalf("storage_cleanup result = %s, %v", cleanup.Result, err)
	}

	// 7. 会话令牌续期：token 文件被替换为新令牌，权限仍为 0600，设备保持在线。
	waitFor(t, "session token renewed", 30*time.Second, func() bool {
		current := strings.TrimSpace(readFile(t, config.TokenFile))
		return current != "" && current != firstToken
	})
	if info, err := os.Lstat(config.TokenFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file after renewal = %v, %v", info, err)
	}
	// 启动时续期一次；之后会话剩余有效期低于 --token-renew-before 时再续期（约每 5 秒）。
	waitFor(t, "periodic renewal", 30*time.Second, func() bool {
		var sessions int
		e.scalar(&sessions, `SELECT count(*) FROM openfox.device_sessions WHERE device_id = $1::uuid`, config.DeviceID)
		return sessions >= 3
	})
	probeAfter := e.runCommand(config.DeviceID, "runtime_probe", map[string]any{})
	if probeAfter.Status != "SUCCEEDED" {
		t.Fatalf("probe after renewal = %+v", probeAfter)
	}

	// 8. 停止 Daemon，检查日志不含令牌与环境变量明文。
	stopDaemon()
	logText := readFile(t, logPath)
	for _, value := range []string{secretValue, issued.InstallToken, issued.Token, firstToken, strings.TrimSpace(readFile(t, config.TokenFile)), "ed25519-seed"} {
		if value != "" && strings.Contains(logText, value) {
			t.Fatalf("daemon log leaked a secret value:\n%s", logText)
		}
	}
	if !strings.Contains(logText, "daemon_token_renewed") {
		t.Fatalf("daemon log has no renewal record:\n%s", logText)
	}
}
