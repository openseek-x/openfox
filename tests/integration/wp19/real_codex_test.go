package wp19

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/runtime/codex"
)

// TestDaemonExecutesDispatchedRunWithRealCodex 与假 Codex 版本走同一条链路，Runtime 换成本机真实 Codex
// （app-server，审批 never，workspace-write，可写根仅 Daemon 管理的工作目录，允许联网）。
// 会调用真实模型，只在 OFD_REAL_CODEX=1 时运行；版本由 OFD_REAL_CODEX_VERSION 指定。
// 覆盖：心跳上报可用模型 → 中心保存 → 按列表选择模型 → 派发执行 → 回复写回 → 用户取消运行中的 Run。
func TestDaemonExecutesDispatchedRunWithRealCodex(t *testing.T) {
	if os.Getenv("OFD_REAL_CODEX") != "1" {
		t.Skip("set OFD_REAL_CODEX=1 to run against the installed Codex")
	}
	version := strings.TrimSpace(os.Getenv("OFD_REAL_CODEX_VERSION"))
	if version == "" {
		t.Fatal("OFD_REAL_CODEX_VERSION is required")
	}
	var adapter *codex.Adapter
	env := startE2E(t, 15*time.Minute, func(string) *codex.Adapter {
		adapter = codex.New("")
		adapter.SupportedVersions = []string{version}
		return adapter
	})
	h, ctx := env.h, env.h.ctx

	// 1. 模型列表：适配器读取 → 心跳上报 → 中心 physical_devices.capabilities → 选择默认模型。
	models, err := adapter.ListModels(ctx)
	if err != nil || len(models) == 0 {
		t.Fatalf("ListModels = %v, %v", models, err)
	}
	capabilities, _ := json.Marshal(map[string]any{"runtimes": map[string]any{"codex": map[string]any{
		"version": version, "runnable": true, "models": models,
	}}})
	env.source.SetCapabilities(capabilities)
	var selected string
	waitUntil(t, "models stored by the center", func() bool {
		selected = h.queryText(`
			SELECT COALESCE((SELECT m->>'id' FROM jsonb_array_elements(capabilities->'runtimes'->'codex'->'models') m
			                  ORDER BY (m->>'is_default')::boolean DESC LIMIT 1), '')
			  FROM openfox.physical_devices WHERE id = $1::uuid`, e2eDevice)
		return selected != ""
	})
	t.Logf("models reported=%d selected=%s", len(models), selected)
	h.selectAgentModel(orgA, e2eAgent, selected)

	// 2. 正常执行：回复写回对话，过程事件落库，CODEX_HOME 已清理。
	conversation := h.postMention(orgA, alice, spaceA, e2eAgent, "Reply with exactly: OK")
	var run execution.Run
	waitUntilFor(t, "real run finished", 10*time.Minute, func() bool {
		run = h.latestRun(orgA, conversation)
		return run.State == execution.StateSucceeded || run.State == execution.StateFailed || run.State == execution.StateCanceled
	})
	reply := h.queryText(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'AGENT' ORDER BY seq DESC LIMIT 1`, conversation)
	events := h.queryText(`SELECT string_agg(type, ',' ORDER BY stream_seq) FROM openfox.run_events WHERE run_id = $1::uuid`, run.ID)
	t.Logf("run state=%s reply=%q events=%s", run.State, reply, events)
	if run.State != execution.StateSucceeded || strings.TrimSpace(reply) != "OK" {
		t.Fatalf("run state = %s, reply = %q", run.State, reply)
	}
	if !strings.Contains(events, "started") || !strings.Contains(events, "output") {
		t.Fatalf("run events = %s", events)
	}
	// 执行步骤（prep_env 与 turn_end 必有）与 token 用量。
	steps := h.queryText(`SELECT string_agg(DISTINCT safe_payload->'data'->'step'->>'kind', ',' ORDER BY safe_payload->'data'->'step'->>'kind')
		FROM openfox.run_events WHERE run_id = $1::uuid AND type = 'step'`, run.ID)
	usage := h.queryText(`SELECT usage::text FROM openfox.run_results WHERE run_id = $1::uuid`, run.ID)
	t.Logf("step kinds=%s usage=%s", steps, usage)
	if !strings.Contains(steps, "prep_env") || !strings.Contains(steps, "turn_end") || !strings.Contains(usage, `"output"`) {
		t.Fatalf("step kinds = %s, usage = %s", steps, usage)
	}
	h.expectRow(`SELECT r.model FROM openfox.runs r JOIN openfox.agent_config_versions c
		ON c.organization_id = r.organization_id AND c.agent_id = r.agent_id AND c.config_version = r.config_version
		WHERE r.id = $1::uuid`, []any{run.ID}, selected)

	// 3. 取消：运行中的长命令被用户取消 → turn/interrupt → CANCELED，槽位释放，无遗留进程。
	conversation2 := h.postMention(orgA, alice, spaceA, e2eAgent,
		"Run the shell command `sleep 127` in the current directory and wait for it to finish, then reply with exactly: DONE")
	waitUntilFor(t, "slow run running", 3*time.Minute, func() bool {
		run = h.latestRun(orgA, conversation2)
		return run.State == execution.StateRunning
	})
	// 给 Codex 时间真正开始执行命令。
	time.Sleep(20 * time.Second)
	var outcome dispatch.CancelOutcome
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = env.store.Cancel(ctx, tx, orgA, run.ID, h.actor(orgA, alice), "real-cancel-0001")
		return err
	})
	if !outcome.NotifyDevice {
		t.Fatalf("cancel outcome = %+v", outcome)
	}
	cancelledAt := time.Now()
	env.hub.PublishCancel(outcome.Run.ComputerID, orgA, run.ID, run.Generation)
	waitUntilFor(t, "real run canceled", 2*time.Minute, func() bool {
		return h.latestRun(orgA, conversation2).State == execution.StateCanceled
	})
	t.Logf("cancel confirmed after %s", time.Since(cancelledAt).Round(100*time.Millisecond))
	h.expectRow(`SELECT count(*)::text FROM openfox.run_events WHERE run_id = $1::uuid AND type = 'cancelled'`, []any{run.ID}, "1")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{e2eDevice}, "0")
}

func (h *harness) queryText(query string, args ...any) string {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	var value *string
	if err := conn.QueryRow(h.ctx, query, args...).Scan(&value); err != nil && err != pgx.ErrNoRows {
		h.t.Fatal(err)
	}
	if value == nil {
		return ""
	}
	return *value
}

// selectAgentModel 模拟用户在 Agent 配置中选择模型：配置版本不可变，新增一个版本并切换。
func (h *harness) selectAgentModel(org, agent, model string) {
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
	if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, org, alice); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `
		INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT organization_id, agent_id, config_version + 1, computer_id, grant_id,
		       runtime, $3, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by
		  FROM openfox.agent_config_versions
		 WHERE organization_id = $1::uuid AND agent_id = $2::uuid
		 ORDER BY config_version DESC LIMIT 1`, org, agent, model); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `
		UPDATE openfox.agents SET current_config_version = current_config_version + 1
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, org, agent); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}
