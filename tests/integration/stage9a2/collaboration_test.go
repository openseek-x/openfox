// Package stage9a2 验证 Agent 协作模式在真实 PostgreSQL（FORCE RLS、应用角色）下的行为：
// 经 Agent 配置接口保存协作模式（FOLLOW_AGENT 归一、非法值拒绝）、设备列表上报的协作模式，
// 派发前核对（设备不支持时运行失败、不派发）以及设备 Context 下发协作模式。
package stage9a2

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/executiondelivery"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/resourcepolicy"
	"openfox/internal/tenancy"
)

// 与 tests/integration/db/fixture.sql 保持一致。
const (
	orgA    = "00000000-0000-0000-0000-0000000000a1"
	alice   = "00000000-0000-0000-0000-000000000001"
	spaceA  = "00000000-0000-0000-0000-0000000000aa"
	agentA  = "00000000-0000-0000-0000-0000000000a2"
	device1 = "00000000-0000-0000-0000-000000000011"
)

// 旧版后台服务的心跳：没有 collaboration_modes。
const legacyCapabilities = `{"runtimes":{
  "codex":{"runnable":true,"models":[{"id":"gpt-5.1","is_default":true}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"]},
  "claude-code":{"runnable":true,"models":[{"id":"default","is_default":true}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"]}}}`

// 新版心跳：claude-code 三种协作模式都支持，codex 只有 FOLLOW_RUNTIME（与真实适配器一致）。
const currentCapabilities = `{"runtimes":{
  "codex":{"runnable":true,"models":[{"id":"gpt-5.1","is_default":true}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"],"collaboration_modes":["FOLLOW_RUNTIME"]},
  "claude-code":{"runnable":true,"models":[{"id":"default","is_default":true}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"],"collaboration_modes":["FOLLOW_RUNTIME","DEFAULT","PLAN"]}}}`

type harness struct {
	t      *testing.T
	ctx    context.Context
	url    string
	pool   *postgres.Pool
	store  dispatch.Store
	worker *dispatch.Worker
	agents agents.PostgresStore
}

func TestCollaborationModeAgainstPostgreSQL(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}, agents: agents.PostgresStore{Pool: pool}}
	h.seed()

	t.Run("save normalizes and validates the mode", h.testSave)
	t.Run("device list reports collaboration modes", h.testDeviceList)
	t.Run("unsupported mode fails the run without dispatch", h.testUnsupported)
	t.Run("supported mode reaches the device context", h.testSupported)
	t.Run("follow runtime always dispatches", h.testFollowRuntime)
}

// testSave：经 Agent 配置接口保存。FOLLOW_AGENT 归一为 FOLLOW_RUNTIME 落库；非法值 ErrInvalidConfig（API 映射为 400）且不新增版本；
// 迁移前遗留的 FOLLOW_AGENT 行读出来是 FOLLOW_RUNTIME。
func (h *harness) testSave(t *testing.T) {
	saved := h.saveConfig("codex", "gpt-5.1", "FOLLOW_AGENT")
	if saved.Config == nil || saved.Config.CollaborationMode != "FOLLOW_RUNTIME" {
		t.Fatalf("saved config = %+v", saved.Config)
	}
	h.expectCount(`SELECT count(*) FROM openfox.agent_config_versions WHERE agent_id = $1::uuid AND config_version = $2 AND collaboration_mode = 'FOLLOW_RUNTIME'`, 1, agentA, int64(saved.CurrentConfigVersion))

	plan := h.saveConfig("claude-code", "default", "plan")
	if plan.Config.CollaborationMode != "PLAN" {
		t.Fatalf("lowercase plan saved as %q", plan.Config.CollaborationMode)
	}
	for _, bad := range []string{"AUTO", "FOLLOW_AGENTS", "plan mode"} {
		if _, err := h.trySaveConfig("codex", "gpt-5.1", bad); !errors.Is(err, agents.ErrInvalidConfig) {
			t.Fatalf("save %q error = %v, want ErrInvalidConfig", bad, err)
		}
	}
	if got := h.currentAgent().CurrentConfigVersion; got != plan.CurrentConfigVersion {
		t.Fatalf("invalid saves created config version %d (want %d)", got, plan.CurrentConfigVersion)
	}

	// 遗留数据：直接写入 FOLLOW_AGENT（列 CHECK 允许），读取时归一。
	h.insertLegacyFollowAgent()
	if got := h.currentAgent().Config.CollaborationMode; got != "FOLLOW_RUNTIME" {
		t.Fatalf("legacy FOLLOW_AGENT read back as %q", got)
	}
	configs, err := h.agents.ListConfigs(h.ctx, h.selection(), h.agentActor(), spaceA, agentA)
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range configs {
		if config.CollaborationMode == "FOLLOW_AGENT" {
			t.Fatalf("config version %d still exposes FOLLOW_AGENT", config.Version)
		}
	}
}

// testDeviceList：设备列表按心跳给出 collaboration_modes；旧版心跳只有 FOLLOW_RUNTIME。
func (h *harness) testDeviceList(t *testing.T) {
	h.setCapabilities(legacyCapabilities)
	if got := h.deviceModes("claude-code"); !slices.Equal(got, []string{"FOLLOW_RUNTIME"}) {
		t.Fatalf("legacy claude-code collaboration_modes = %v", got)
	}
	h.setCapabilities(currentCapabilities)
	if got := h.deviceModes("claude-code"); !slices.Equal(got, []string{"FOLLOW_RUNTIME", "DEFAULT", "PLAN"}) {
		t.Fatalf("claude-code collaboration_modes = %v", got)
	}
	if got := h.deviceModes("codex"); !slices.Equal(got, []string{"FOLLOW_RUNTIME"}) {
		t.Fatalf("codex collaboration_modes = %v", got)
	}
}

// testUnsupported：保存 PLAN / DEFAULT 后派发到不支持的设备，运行失败（RUNTIME_UNAVAILABLE）、没有派发邀约，并写一条系统消息。
func (h *harness) testUnsupported(t *testing.T) {
	cases := []struct {
		name, capabilities, runtime, model, mode string
	}{
		{"legacy daemon PLAN", legacyCapabilities, "claude-code", "default", "PLAN"},
		{"legacy daemon DEFAULT", legacyCapabilities, "claude-code", "default", "DEFAULT"},
		{"codex PLAN", currentCapabilities, "codex", "gpt-5.1", "PLAN"},
	}
	for _, c := range cases {
		h.setCapabilities(c.capabilities)
		h.saveConfig(c.runtime, c.model, c.mode)
		conv := h.newConversation()
		run := h.sendAndMaterialize(conv, c.name)
		r := h.run(run)
		if r.State != execution.StateFailed || h.failureCode(run) != "RUNTIME_UNAVAILABLE" || r.OfferID != "" {
			t.Fatalf("%s: state=%s failure=%s offer=%q", c.name, r.State, h.failureCode(run), r.OfferID)
		}
		h.expectCount(`SELECT count(*) FROM openfox.dispatch_offers WHERE run_id = $1::uuid`, 0, run)
		h.expectCount(`SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'SYSTEM' AND body LIKE '%collaboration mode `+c.mode+` is not supported%'`, 1, conv)
	}
}

// testSupported：设备支持时正常派发，设备 Context 带上协作模式；PLAN 与任何权限档位（含 FULL_ACCESS）组合都不报错。
func (h *harness) testSupported(t *testing.T) {
	h.setCapabilities(currentCapabilities)
	for _, mode := range []string{"PLAN", "DEFAULT"} {
		h.saveConfigWith("claude-code", "default", mode, "FULL_ACCESS")
		conv := h.newConversation()
		run := h.sendAndDispatch(conv, "supported "+mode)
		lease := h.claim(run)
		if got := h.context(lease); got.CollaborationMode != mode || got.ApprovalMode != "FULL_ACCESS" || got.Runtime != "claude-code" {
			t.Fatalf("%s context = runtime %s approval %s collaboration %s", mode, got.Runtime, got.ApprovalMode, got.CollaborationMode)
		}
		h.finish(run, lease)
	}
}

// testFollowRuntime：FOLLOW_RUNTIME（以及遗留 FOLLOW_AGENT）在旧版心跳上也照常派发，Context 下发 FOLLOW_RUNTIME。
func (h *harness) testFollowRuntime(t *testing.T) {
	h.setCapabilities(legacyCapabilities)
	h.saveConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	h.dispatchExpectingFollowRuntime(t, "follow runtime")
	h.insertLegacyFollowAgent()
	h.dispatchExpectingFollowRuntime(t, "legacy follow agent")
}

func (h *harness) dispatchExpectingFollowRuntime(t *testing.T, body string) {
	t.Helper()
	conv := h.newConversation()
	run := h.sendAndDispatch(conv, body)
	lease := h.claim(run)
	if got := h.context(lease); got.CollaborationMode != "FOLLOW_RUNTIME" {
		t.Fatalf("%s: context collaboration_mode = %q", body, got.CollaborationMode)
	}
	h.finish(run, lease)
}

// saveConfig 经 agents.PostgresStore.CreateConfig（与 POST config-versions 同一路径）保存新配置版本，权限模式 FOLLOW_RUNTIME。
func (h *harness) saveConfig(runtimeName, model, collaboration string) agents.Agent {
	h.t.Helper()
	return h.saveConfigWith(runtimeName, model, collaboration, "FOLLOW_RUNTIME")
}

func (h *harness) saveConfigWith(runtimeName, model, collaboration, approval string) agents.Agent {
	h.t.Helper()
	agent, err := h.trySaveConfigWith(runtimeName, model, collaboration, approval)
	if err != nil {
		h.t.Fatalf("save config %s/%s/%s: %v", runtimeName, model, collaboration, err)
	}
	return agent
}

func (h *harness) trySaveConfig(runtimeName, model, collaboration string) (agents.Agent, error) {
	return h.trySaveConfigWith(runtimeName, model, collaboration, "FOLLOW_RUNTIME")
}

func (h *harness) trySaveConfigWith(runtimeName, model, collaboration, approval string) (agents.Agent, error) {
	h.t.Helper()
	current := h.currentAgent()
	if current.Config == nil {
		h.t.Fatal("agent has no current config")
	}
	input := agents.ConfigInput{
		ComputerID: device1, GrantID: current.Config.GrantID, Runtime: runtimeName, Model: model,
		MaxConcurrency: 2, QueueTimeoutSeconds: 3600, RunTimeoutSeconds: 600,
		RunSettings: agents.RunSettings{ApprovalMode: approval, CollaborationMode: collaboration},
	}
	return h.agents.CreateConfig(h.ctx, h.selection(), h.agentActor(), spaceA, agentA, current.Version, input)
}

func (h *harness) currentAgent() agents.Agent {
	h.t.Helper()
	agent, err := h.agents.Get(h.ctx, h.selection(), h.agentActor(), spaceA, agentA)
	if err != nil {
		h.t.Fatal(err)
	}
	return agent
}

func (h *harness) selection() tenancy.OrganizationSelection {
	return tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
}

func (h *harness) agentActor() agents.Actor {
	return agents.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion(), RequestID: "stage9a2"}
}

// insertLegacyFollowAgent 模拟 9a-2 之前保存的配置：复制当前版本，collaboration_mode 写成 FOLLOW_AGENT（测试夹具，不经过接口）。
func (h *harness) insertLegacyFollowAgent() {
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
	defer func() { _ = tx.Rollback(h.ctx) }()
	if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, $3, 'user_request')`, orgA, alice, int64(h.authVersion())); err != nil {
		h.t.Fatal(err)
	}
	var next int64
	if err := tx.QueryRow(h.ctx, `
		INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model,
		       max_concurrency, queue_timeout_seconds, run_timeout_seconds, approval_mode, collaboration_mode, created_by)
		SELECT c.organization_id, c.agent_id, a.current_config_version + 1, c.computer_id, c.grant_id, c.runtime, c.model,
		       c.max_concurrency, c.queue_timeout_seconds, c.run_timeout_seconds, c.approval_mode, 'FOLLOW_AGENT', c.created_by
		  FROM openfox.agents a
		  JOIN openfox.agent_config_versions c ON c.organization_id = a.organization_id AND c.agent_id = a.id AND c.config_version = a.current_config_version
		 WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		RETURNING config_version`, orgA, agentA).Scan(&next); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `UPDATE openfox.agents SET current_config_version = $3 WHERE organization_id = $1::uuid AND id = $2::uuid`, orgA, agentA, next); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	h.expectCount(`SELECT count(*) FROM openfox.agent_config_versions WHERE agent_id = $1::uuid AND config_version = $2 AND collaboration_mode = 'FOLLOW_AGENT'`, 1, agentA, next)
}

// deviceModes 通过 devices.PostgresStore.List（GET /devices 同一路径）读取某个 Runtime 的协作模式。
func (h *harness) deviceModes(provider string) []string {
	h.t.Helper()
	list, err := (devices.PostgresStore{Pool: h.pool}).List(h.ctx, h.selection())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, device := range list {
		if device.ID != device1 {
			continue
		}
		for _, runtime := range device.Runtimes {
			if runtime.Provider == provider {
				return runtime.CollaborationModes
			}
		}
	}
	h.t.Fatalf("runtime %s not found on device %s", provider, device1)
	return nil
}

func (h *harness) setCapabilities(raw string) {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET capabilities = $1::jsonb, last_seen_at = clock_timestamp() WHERE id = $2::uuid`, raw, device1)
}

func (h *harness) newConversation() string {
	h.t.Helper()
	id := newID(h.t)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Create(ctx, tx, orgA, h.actor(), conversations.CreateInput{
			ID: id, SpaceID: spaceA, AgentID: agentA, Visibility: "PRIVATE",
		})
		return err
	})
	return id
}

// sendAndMaterialize 发一条 @Agent 消息并跑一轮派发，返回新建的 Run ID（可能因设备能力核对而失败）。
func (h *harness) sendAndMaterialize(conv, body string) string {
	h.t.Helper()
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		id := newID(h.t)
		_, err := (conversations.Service{}).PostMessage(ctx, tx, orgA, conv, h.actor(), conversations.MessageInput{
			ID: id, ClientMessageID: "client-" + id, Body: body, Mentions: []conversations.Mention{{AgentID: agentA}},
		})
		return err
	})
	h.runOnce()
	var runID string
	h.queryRow(`SELECT id::text FROM openfox.runs WHERE conversation_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, []any{conv}, &runID)
	return runID
}

func (h *harness) sendAndDispatch(conv, body string) string {
	h.t.Helper()
	runID := h.sendAndMaterialize(conv, body)
	if r := h.run(runID); r.OfferID == "" {
		h.t.Fatalf("run %s was not offered: state=%s wait=%s failure=%s", runID, r.State, r.WaitReason, h.failureCode(runID))
	}
	return runID
}

func (h *harness) run(runID string) execution.Run {
	h.t.Helper()
	var run execution.Run
	h.inUser(func(ctx context.Context, tx pgx.Tx) (err error) {
		run, err = dispatch.LoadRun(ctx, tx, runID)
		return err
	})
	return run
}

func (h *harness) failureCode(runID string) string {
	var code *string
	h.queryRow(`SELECT failure_code FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &code)
	if code == nil {
		return ""
	}
	return *code
}

func (h *harness) claim(runID string) dispatch.Lease {
	h.t.Helper()
	var offerID string
	h.queryRow(`SELECT offer_id::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &offerID)
	var lease dispatch.Lease
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = h.store.Claim(ctx, tx, orgA, device1, offerID)
		return err
	})
	return lease
}

func (h *harness) context(lease dispatch.Lease) dispatch.RunContext {
	h.t.Helper()
	var result dispatch.RunContext
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = h.store.Context(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})
	return result
}

// finish 走设备结果回写路径把 Run 正常结束（释放容量），让会话可以接收下一个 Run。
func (h *harness) finish(runID string, lease dispatch.Lease) {
	h.t.Helper()
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := h.store.Start(ctx, tx, orgA, device1, runID, lease.Generation); err != nil {
			return err
		}
		_, err := (executiondelivery.Service{Resources: resourcepolicy.Store{}}).AppendResult(ctx, tx, orgA,
			deviceauth.Principal{DeviceID: device1}, runID, executiondelivery.ResultInput{
				ID: newID(h.t), Generation: lease.Generation, Status: "SUCCEEDED", Summary: "done", Output: "OK",
			})
		return err
	})
}

func (h *harness) runOnce() {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device1)
	if _, err := h.worker.RunOnce(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) actor() conversations.Actor {
	return conversations.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion()}
}

func (h *harness) authVersion() uint64 {
	h.t.Helper()
	var version int64
	h.queryRow(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`, []any{orgA, alice}, &version)
	return uint64(version)
}

func (h *harness) inUser(fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(h.selection()), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inDevice(fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: device1, AuthorizationVersion: 1}
	if err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceDeviceRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func selector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

// seed 与 stage8d 相同：把夹具 Agent 放进 spaceA，建 config_version 1（codex，绑定 device1）。
func (h *harness) seed() {
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
	if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, orgA, alice); err != nil {
		h.t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE openfox.agents SET space_id = '` + spaceA + `', owner_user_id = '` + alice + `', name = 'Collaboration agent', kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = '` + orgA + `' AND id = '` + agentA + `'`,
		`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		 SELECT '` + orgA + `', '` + agentA + `', 1, '` + device1 + `', g.id, 'codex', 'gpt-5.1', 2, 3600, 600, '` + alice + `' FROM openfox.device_grants g WHERE g.organization_id = '` + orgA + `' AND g.device_id = '` + device1 + `'`,
	} {
		if _, err := tx.Exec(h.ctx, statement); err != nil {
			h.t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp(), max_slots = 4 WHERE id = $1::uuid`, device1)
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if _, err := conn.Exec(h.ctx, query, args...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) queryRow(query string, args []any, dest ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if err := conn.QueryRow(h.ctx, query, args...).Scan(dest...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) expectCount(query string, want int, args ...any) {
	h.t.Helper()
	var got int
	h.queryRow(query, args, &got)
	if got != want {
		h.t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func newID(t *testing.T) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
