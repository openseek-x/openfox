package wp19

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/runtime/codex"
)

// 端到端使用 fixture 中独立的电脑与 Agent，避免与 TestCenterRunDispatchAgainstPostgreSQL 共享状态。
const (
	e2eDevice = "00000000-0000-0000-0000-000000000022"
	e2eAgent  = "00000000-0000-0000-0000-0000000000a4"
)

// e2eEnv 是端到端环境：PostgreSQL + API + Hub + 派发 worker + Daemon（WSS 心跳与唤醒、
// supervisor、SQLite 日志、Codex 适配器）。
type e2eEnv struct {
	h       *harness
	store   dispatch.Store
	hub     *dispatch.Hub
	journal *supervisor.Journal
	source  *daemon.StaticHeartbeatSource
	root    string
}

// startE2E 搭建端到端环境，清理由 t.Cleanup 完成。newAdapter 按 root 构造 Codex 适配器。
func startE2E(t *testing.T, timeout time.Duration, newAdapter func(root string) *codex.Adapter) *e2eEnv {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 16, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 6 * time.Second, LeaseRenew: time.Second, PollInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	hub := dispatch.NewHub()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store, hub: hub}
	h.seedE2E()
	token := h.deviceSession()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(api.NewWithDispatch(identity.Service{Store: identity.PostgresStore{Pool: pool}},
		pool, false, nil, api.AuthRateLimits{}, api.DispatchOptions{Store: store, Hub: hub}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	workerContext, stopWorker := context.WithCancel(ctx)
	t.Cleanup(stopWorker)
	go func() { _ = (&dispatch.Worker{Pool: pool, Store: store, Hub: hub}).Run(workerContext) }()

	// Daemon：只靠 WSS 唤醒（轮询间隔设为 10 分钟），以验证推送链路。
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "workspaces"), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervisor.OpenJournal(ctx, filepath.Join(root, "state", "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	adapter := newAdapter(root)
	adapter.HomeRoot = filepath.Join(root, "state", "codex-homes")
	runner, err := supervisor.New(supervisor.Config{
		Client:  &supervisor.Client{BaseURL: server.URL, Token: daemon.StaticTokenProvider(token)},
		Adapter: adapter, Workdir: manager, Journal: journal, PollInterval: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT openfox_private.current_center_epoch()`).Scan(&epoch)
	})
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{
		CenterEpoch: epoch, ProtocolMajor: daemon.ProtocolMajor, DaemonVersion: "e2e",
		OperatingSystem: "darwin", Architecture: "arm64", Capabilities: []byte(`{"runtime":"codex"}`),
	})
	remote, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL: strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/device/connect", DeviceID: e2eDevice,
		TokenProvider: daemon.StaticTokenProvider(token), HeartbeatInterval: 500 * time.Millisecond,
		HeartbeatSource: source,
		OnRunMessage:    runner.HandleMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonContext, stopDaemon := context.WithCancel(ctx)
	remoteDone, runnerDone := make(chan error, 1), make(chan error, 1)
	go func() { remoteDone <- remote.Run(daemonContext) }()
	go func() { runnerDone <- runner.Run(daemonContext) }()
	t.Cleanup(func() {
		stopDaemon()
		<-remoteDone
		<-runnerDone
	})

	// 等待 WSS 首个心跳完成订阅：探测用的取消唤醒指向不存在的 Run，会被 supervisor 忽略。
	waitUntil(t, "device stream subscription", func() bool {
		return hub.PublishCancel(e2eDevice, orgA, "00000000-0000-4000-8000-0000000000ff", 1) > 0
	})

	return &e2eEnv{h: h, store: store, hub: hub, journal: journal, source: source, root: root}
}

// TestDaemonExecutesDispatchedRunEndToEnd 串起完整链路，Runtime 是假 Codex 脚本（app-server 协议）。
func TestDaemonExecutesDispatchedRunEndToEnd(t *testing.T) {
	env := startE2E(t, 90*time.Second, func(root string) *codex.Adapter {
		adapter := codex.New(writeFakeCodex(t, root))
		adapter.SupportedVersions = []string{"0.0.1-e2e"}
		adapter.Environment = map[string]string{"CODEX_HOME": filepath.Join(root, "user-codex-home")}
		return adapter
	})
	h, store, hub, journal, root, ctx := env.h, env.store, env.hub, env.journal, env.root, env.h.ctx
	// 1. 正常执行：@Agent → worker 派发 → WSS 唤醒 → claim/context/start → 进程 → 结果 → Agent 回复。
	conversation := h.postMention(orgA, alice, spaceA, e2eAgent, "请回复 OK")
	var run execution.Run
	waitUntil(t, "run succeeded", func() bool {
		run = h.latestRun(orgA, conversation)
		return run.State == execution.StateSucceeded
	})
	h.expectRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'AGENT' ORDER BY seq DESC LIMIT 1`,
		[]any{conversation}, "OK")
	// 设备上传的过程事件（started/output）必须落库，不能只有中心生成的 run.result。
	h.expectRow(`SELECT string_agg(DISTINCT type, ',' ORDER BY type) FROM openfox.run_events WHERE run_id = $1::uuid AND type IN ('started','output')`,
		[]any{run.ID}, "output,started")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{e2eDevice}, "0")
	state, err := journal.State(ctx, run.ID, run.Generation)
	if err != nil || state != supervisor.JournalFinished {
		t.Fatalf("journal state = %q, %v", state, err)
	}
	if got := countLines(t, filepath.Join(root, "invocations")); got != 1 {
		t.Fatalf("runtime invocations = %d, want 1", got)
	}

	// 2. 用户取消运行中的 Run：Cancel + Hub 推送 run.cancel → Daemon 停止进程 → CANCELED。
	conversation2 := h.postMention(orgA, alice, spaceA, e2eAgent, "slow task")
	waitUntil(t, "slow run running", func() bool {
		run = h.latestRun(orgA, conversation2)
		return run.State == execution.StateRunning && countLines(t, filepath.Join(root, "invocations")) == 2
	})
	var outcome dispatch.CancelOutcome
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = store.Cancel(ctx, tx, orgA, run.ID, h.actor(orgA, alice), "e2e-cancel-0001")
		return err
	})
	if !outcome.NotifyDevice {
		t.Fatalf("cancel outcome = %+v", outcome)
	}
	hub.PublishCancel(outcome.Run.ComputerID, orgA, run.ID, run.Generation)
	waitUntil(t, "run canceled", func() bool { return h.latestRun(orgA, conversation2).State == execution.StateCanceled })
	h.expectRow(`SELECT count(*)::text FROM openfox.run_events WHERE run_id = $1::uuid AND type = 'cancelled'`, []any{run.ID}, "1")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{e2eDevice}, "0")
	if _, err := os.Stat(filepath.Join(root, "slow-finished")); !os.IsNotExist(err) {
		t.Fatalf("slow runtime was not interrupted: %v", err)
	}
	if got := countLines(t, filepath.Join(root, "invocations")); got != 2 {
		t.Fatalf("runtime invocations = %d, want 2 (no duplicate process)", got)
	}
}

func (h *harness) seedE2E() {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	tx, err := conn.Begin(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, orgA, alice); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `
		UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'E2E agent',
		       kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL,
		       current_config_version = COALESCE((SELECT max(c.config_version) FROM openfox.agent_config_versions c
		           WHERE c.organization_id = $1::uuid AND c.agent_id = $4::uuid), 0) + 1
		 WHERE organization_id = $1::uuid AND id = $4::uuid`, orgA, spaceA, alice, e2eAgent); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `
		INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, a.current_config_version, $3::uuid, g.id, 'codex', 'default', 1, 3600, 600, $4::uuid
		  FROM openfox.device_grants g
		  JOIN openfox.agents a ON a.organization_id = g.organization_id AND a.id = $2::uuid
		 WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`,
		orgA, e2eAgent, e2eDevice, alice); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

// deviceSession 为 fixture 电脑登记测试密钥，并走真实的挑战-签名流程换取设备会话。
func (h *harness) deviceSession() string {
	h.t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	keyID := fmt.Sprintf("e2e-%x", publicKey[:8])
	h.exec(`INSERT INTO openfox.device_keys (device_id, key_id, public_key) VALUES ($1::uuid, $2, $3)`, e2eDevice, keyID, []byte(publicKey))
	auth := deviceauth.Service{Store: deviceauth.PostgresStore{Pool: h.pool}, Now: time.Now}
	challenge, err := auth.IssueChallenge(h.ctx, deviceauth.ChallengeRequest{DeviceID: e2eDevice, KeyID: keyID})
	if err != nil {
		h.t.Fatal(err)
	}
	session, err := auth.Verify(h.ctx, deviceauth.VerifyRequest{
		ChallengeID: challenge.ID, DeviceID: challenge.DeviceID, KeyID: challenge.KeyID,
		Signature: ed25519.Sign(privateKey, deviceauth.CanonicalPayload(challenge.ID, challenge.DeviceID, challenge.KeyID, challenge.Nonce, challenge.Challenge)),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return session.Token
}

func (h *harness) latestRun(org, conversationID string) execution.Run {
	h.t.Helper()
	var runID string
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	err = conn.QueryRow(h.ctx, `SELECT id::text FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid ORDER BY created_at DESC LIMIT 1`, org, conversationID).Scan(&runID)
	if err == pgx.ErrNoRows {
		return execution.Run{}
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return h.runForConversation(org, conversationID)
}

// writeFakeCodex 写一个遵循 codex app-server 协议的假 CLI：每次启动 app-server 追加一行调用记录；
// prompt 含 "slow" 时不结束 turn，直到收到 turn/interrupt 才以 interrupted 收尾。
func writeFakeCodex(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "codex-e2e.sh")
	script := `#!/bin/sh
state_dir='` + root + `'
case "$1" in
  --version) echo 'codex-cli 0.0.1-e2e'; exit 0 ;;
  login) echo 'Logged in using e2e fixture'; exit 0 ;;
  app-server) if [ "$2" = "--help" ]; then echo 'codex app-server'; exit 0; fi ;;
  *) exit 2 ;;
esac
echo "$PWD" >> "$state_dir/invocations"
IFS= read -r line || exit 0
echo '{"id":1,"result":{"userAgent":"e2e"}}'
IFS= read -r line || exit 0
IFS= read -r line || exit 0
echo '{"id":2,"result":{"thread":{"id":"th-e2e"}}}'
IFS= read -r turn || exit 0
echo '{"id":3,"result":{"turn":{"id":"tu-e2e","status":"inProgress"}}}'
case "$turn" in
  *slow*)
    IFS= read -r line || exit 0
    case "$line" in
      *turn/interrupt*)
        echo '{"id":4,"result":{}}'
        echo '{"method":"turn/completed","params":{"threadId":"th-e2e","turn":{"id":"tu-e2e","status":"interrupted"}}}' ;;
      *) touch "$state_dir/slow-finished" ;;
    esac ;;
  *)
    echo '{"method":"item/completed","params":{"threadId":"th-e2e","item":{"type":"agentMessage","text":"OK","phase":"final_answer"}}}'
    echo '{"method":"turn/completed","params":{"threadId":"th-e2e","turn":{"id":"tu-e2e","status":"completed"}}}' ;;
esac
while IFS= read -r line; do :; done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// 适配器从用户 CODEX_HOME 复制登录凭据；这里提供一个假的源目录。
	source := filepath.Join(root, "user-codex-home")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(contents), "\n")
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	waitUntilFor(t, what, 30*time.Second, condition)
}

func waitUntilFor(t *testing.T, what string, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
