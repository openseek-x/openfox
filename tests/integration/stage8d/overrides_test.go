// Package stage8d 验证对话级 Runtime / 模型 / 权限模式覆盖在真实 PostgreSQL（FORCE RLS、应用角色）下的行为：
// 覆盖接口的鉴权与校验、创建 Run 时写入快照（下一次运行生效）、设备 Context 读快照，以及「绝不悄悄放宽权限」的派发前核对。
package stage8d

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
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
	orgB    = "00000000-0000-0000-0000-0000000000b1"
	alice   = "00000000-0000-0000-0000-000000000001"
	bob     = "00000000-0000-0000-0000-000000000002"
	spaceA  = "00000000-0000-0000-0000-0000000000aa"
	agentA  = "00000000-0000-0000-0000-0000000000a2"
	device1 = "00000000-0000-0000-0000-000000000011"
	// 本测试新增的 orgA 成员：评论者、只读访客、非会话成员。
	carol = "00000000-0000-0000-0000-0000000000c1"
	viewr = "00000000-0000-0000-0000-0000000000c2"
	erin  = "00000000-0000-0000-0000-0000000000c3"
)

// 心跳能力：codex 四档；claude-code 四档且有模型列表；opencode 不可运行；pi 只有 FOLLOW_RUNTIME（approval_modes 缺省）。
const fullCapabilities = `{"runtimes":{
  "codex":{"runnable":true,"models":[{"id":"gpt-5.1","display_name":"GPT","is_default":true}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"]},
  "claude-code":{"runnable":true,"models":[{"id":"default","display_name":"Default","is_default":true},{"id":"sonnet","display_name":"Sonnet"}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"]},
  "opencode":{"runnable":false,"models":[],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","WORKSPACE_WRITE","FULL_ACCESS"]},
  "pi":{"runnable":true,"models":[]}}}`

type harness struct {
	t      *testing.T
	ctx    context.Context
	url    string
	pool   *postgres.Pool
	store  dispatch.Store
	worker *dispatch.Worker
}

func TestConversationOverridesAgainstPostgreSQL(t *testing.T) {
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
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}}
	h.seed()

	t.Run("snapshot follows overrides and next run only", h.testSnapshots)
	t.Run("overrides endpoint rules", h.testEndpointRules)
	t.Run("override options endpoint", h.testOverrideOptions)
	t.Run("never silently widen permissions", h.testNeverWiden)
	t.Run("legacy runs fall back to the config version", h.testLegacyFallback)
	t.Run("MCP lease and input ownership", h.testMCPContext)
	t.Run("conversation workspace permissions", h.testWorkspacePermissions)
	t.Run("reasoning snapshots and capability checks", h.testReasoningSnapshots)
	t.Run("Git defaults and immutable run snapshots", h.testGitSnapshots)
	t.Run("work-directory encrypted query and isolation", h.testWorkDirectoryPath)
}

// testSnapshots：覆盖后新 Run 的快照是覆盖值、设备 Context 返回覆盖值；进行中的 Run 不受后续修改影响；
// 清除覆盖回到 Agent 默认；只覆盖 Runtime 时模型为 default。
func (h *harness) testSnapshots(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	conv := h.newConversation(alice)

	// 1. 只覆盖 Runtime：模型落到 default（Agent 配置的 gpt-5.1 属于 codex），权限沿用 FOLLOW_RUNTIME。
	summary := h.patch(alice, conv, conversations.OverridesPatch{Runtime: set("claude-code"), ExpectedVersion: ver(0)})
	if summary.Overrides.Version != 1 || *summary.Overrides.Runtime != "claude-code" ||
		summary.Effective == nil || *summary.Effective != (conversations.EffectiveConfig{Runtime: "claude-code", Model: "default", ApprovalMode: "FOLLOW_RUNTIME", ReasoningEffort: "DEFAULT"}) ||
		summary.AgentDefaults == nil || summary.AgentDefaults.Runtime != "codex" || summary.AgentDefaults.Model != "gpt-5.1" {
		t.Fatalf("summary after runtime override = %+v effective=%+v defaults=%+v", summary.Overrides, summary.Effective, summary.AgentDefaults)
	}
	h.expectCount(`SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'SYSTEM' AND body LIKE 'alice changed%Runtime set to claude-code%next message%'`, 1, conv)

	run1 := h.sendAndDispatch(alice, conv, "first")
	h.expectSnapshot(run1, "claude-code", "default", "FOLLOW_RUNTIME")
	lease := h.claim(run1)
	ctxt := h.context(lease)
	if ctxt.Runtime != "claude-code" || ctxt.Model != "default" || ctxt.ApprovalMode != "FOLLOW_RUNTIME" {
		t.Fatalf("device context = %s/%s/%s", ctxt.Runtime, ctxt.Model, ctxt.ApprovalMode)
	}

	// 2. 运行进行中修改覆盖：当前 Run 的快照与设备 Context 不变。
	h.patch(alice, conv, conversations.OverridesPatch{Model: set("sonnet"), ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(1)})
	h.expectSnapshot(run1, "claude-code", "default", "FOLLOW_RUNTIME")
	if again := h.context(lease); again.Model != "default" || again.ApprovalMode != "FOLLOW_RUNTIME" {
		t.Fatalf("context of in-flight run changed: %+v", again)
	}
	h.finish(run1, lease)

	// 3. 下一次运行用新值。
	run2 := h.sendAndDispatch(alice, conv, "second")
	h.expectSnapshot(run2, "claude-code", "sonnet", "READ_ONLY")
	lease2 := h.claim(run2)
	if got := h.context(lease2); got.Model != "sonnet" || got.ApprovalMode != "READ_ONLY" {
		t.Fatalf("second context = %+v", got)
	}
	h.finish(run2, lease2)

	// 4. 清除全部覆盖：回到 Agent 默认。
	cleared := h.patch(alice, conv, conversations.OverridesPatch{Runtime: null(), Model: null(), ApprovalMode: null(), ExpectedVersion: ver(2)})
	if cleared.Overrides.Runtime != nil || cleared.Overrides.Model != nil || cleared.Overrides.ApprovalMode != nil || cleared.Overrides.Version != 3 {
		t.Fatalf("cleared overrides = %+v", cleared.Overrides)
	}
	run3 := h.sendAndDispatch(alice, conv, "third")
	h.expectSnapshot(run3, "codex", "gpt-5.1", "FOLLOW_RUNTIME")
	lease3 := h.claim(run3)
	h.finish(run3, lease3)

	// 5. 排队中的 Run 的快照也不被后续修改改写（容量占满使 Run 保持 QUEUED 不好构造，这里直接断言快照列在整个生命周期不变）。
	h.expectSnapshot(run1, "claude-code", "default", "FOLLOW_RUNTIME")
	h.expectSnapshot(run2, "claude-code", "sonnet", "READ_ONLY")

	// 6. Agent 默认权限模式也走同一条路径；对话覆盖优先。
	h.setAgentConfig("codex", "gpt-5.1", "WORKSPACE_WRITE")
	run4 := h.sendAndDispatch(alice, conv, "fourth")
	h.expectSnapshot(run4, "codex", "gpt-5.1", "WORKSPACE_WRITE")
	h.finish(run4, h.claim(run4))
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
}

// testEndpointRules：鉴权、乐观锁、非法取值、小队对话、不能借此换电脑。
func (h *harness) testEndpointRules(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	conv := h.newConversation(alice)
	h.exec(`INSERT INTO openfox.conversation_members (organization_id, conversation_id, user_id, access_level)
	        VALUES ($1::uuid, $2::uuid, $3::uuid, 'COMMENTER'), ($1::uuid, $2::uuid, $4::uuid, 'VIEWER')`, orgA, conv, carol, viewr)
	h.exec(`UPDATE openfox.conversations SET visibility = 'SHARED' WHERE id = $1::uuid`, conv)

	// 能发消息的人（所有者、评论者）都能改。
	h.patch(carol, conv, conversations.OverridesPatch{ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(0)})
	h.patch(alice, conv, conversations.OverridesPatch{Model: set("gpt-5.1"), ExpectedVersion: ver(1)})
	// 只读访客 403、非成员 404、跨组织 404。
	expectErr(t, h.try(viewr, orgA, conv, conversations.OverridesPatch{ApprovalMode: set("FULL_ACCESS"), ExpectedVersion: ver(2)}), conversations.ErrForbidden)
	expectErr(t, h.try(erin, orgA, conv, conversations.OverridesPatch{ApprovalMode: set("FULL_ACCESS"), ExpectedVersion: ver(2)}), conversations.ErrNotFound)
	expectErr(t, h.try(bob, orgB, conv, conversations.OverridesPatch{ApprovalMode: set("FULL_ACCESS"), ExpectedVersion: ver(2)}), conversations.ErrNotFound)
	// 乐观锁：旧版本 409；相同的最终状态是幂等的（不改版本）。
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{ApprovalMode: set("FULL_ACCESS"), ExpectedVersion: ver(1)}), conversations.ErrVersionConflict)
	same := h.patch(alice, conv, conversations.OverridesPatch{ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(0)})
	if same.Overrides.Version != 2 {
		t.Fatalf("idempotent patch bumped version: %+v", same.Overrides)
	}
	// 缺少 expected_version / 没有任何字段。
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{ApprovalMode: set("FULL_ACCESS")}), conversations.ErrInvalidRequest)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{ExpectedVersion: ver(2)}), conversations.ErrInvalidRequest)
	// 非法取值：Runtime / 档位 / 模型格式。
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Runtime: set("gemini"), ExpectedVersion: ver(2)}), conversations.ErrInvalidRequest)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{ApprovalMode: set("YOLO"), ExpectedVersion: ver(2)}), conversations.ErrInvalidRequest)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Model: set("bad\nmodel"), ExpectedVersion: ver(2)}), conversations.ErrInvalidRequest)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Model: set(strings.Repeat("m", 129)), ExpectedVersion: ver(2)}), conversations.ErrInvalidRequest)
	// 模型不在 codex 的列表里；Runtime 不在心跳里不可选；换 Runtime 时旧模型覆盖不匹配新 Runtime 则整体拒绝。
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Model: set("nope"), ExpectedVersion: ver(2)}), conversations.ErrModelNotAvailable)
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[]}}}`)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Runtime: set("claude-code"), ExpectedVersion: ver(2)}), conversations.ErrRuntimeNotAvailable)
	h.setCapabilities(fullCapabilities)
	expectErr(t, h.try(alice, orgA, conv, conversations.OverridesPatch{Runtime: set("claude-code"), ExpectedVersion: ver(2)}), conversations.ErrModelNotAvailable) // 旧覆盖 gpt-5.1 不属于 claude-code
	h.patch(alice, conv, conversations.OverridesPatch{Runtime: set("claude-code"), Model: set("sonnet"), ExpectedVersion: ver(2)})
	// 带 models_error 的 Runtime 只校验格式。
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[{"id":"gpt-5.1"}],"models_error":"boom"}}}`)
	h.patch(alice, conv, conversations.OverridesPatch{Runtime: null(), Model: set("anything-goes"), ExpectedVersion: ver(3)})
	h.setCapabilities(fullCapabilities)

	// 只覆盖权限时即使电脑能力没有上报 Runtime 也可以（设备支持与否由派发前核对负责）。
	// 小队对话不允许覆盖。
	crewConv := h.newConversation(alice)
	h.exec(`ALTER TABLE openfox.conversations DISABLE TRIGGER USER`)
	h.exec(`INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'crew') ON CONFLICT DO NOTHING`,
		orgA, crewID, spaceA, alice)
	h.exec(`UPDATE openfox.conversations SET crew_id = $2::uuid WHERE id = $1::uuid`, crewConv, crewID)
	h.exec(`ALTER TABLE openfox.conversations ENABLE TRIGGER USER`)
	expectErr(t, h.try(alice, orgA, crewConv, conversations.OverridesPatch{ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(0)}), conversations.ErrCrewConversation)

	// 数据库兜底：小队对话不能带覆盖；非法值被 CHECK 拒绝。
	if err := h.tryExec(`UPDATE openfox.conversations SET approval_mode_override = 'READ_ONLY' WHERE id = $1::uuid`, crewConv); err == nil {
		t.Fatal("crew conversation accepted an override at the database level")
	}
	if err := h.tryExec(`UPDATE openfox.conversations SET runtime_override = 'gemini' WHERE id = $1::uuid`, conv); err == nil {
		t.Fatal("invalid runtime_override accepted at the database level")
	}
	// 事件：覆盖修改写 conversation.updated；时间线有系统消息。
	h.expectCount(`SELECT (count(*) >= 4)::int FROM openfox.organization_events WHERE conversation_id = $1::uuid AND type = 'conversation.updated' AND version > 1`, 1, conv)
	h.expectCount(`SELECT (count(*) >= 4)::int FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'SYSTEM'`, 1, conv)
}

// options 以 user 身份读取可选项。
func (h *harness) options(user, org, conv string) (conversations.OverrideOptions, error) {
	h.t.Helper()
	var result conversations.OverrideOptions
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: h.authVersion(org, user)}
	err := h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = (conversations.Service{}).OverrideOptions(ctx, tx, org, conv, h.actor(org, user))
		return err
	})
	return result, err
}

// testOverrideOptions（8d-2）：只读可选项接口。成员可读且内容来自心跳；鉴权与修改覆盖一致；
// 只读访客 403、非成员 / 跨组织 404、小队对话 409；不含电脑名称等设备信息；旧心跳只有 FOLLOW_RUNTIME。
func (h *harness) testOverrideOptions(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	conv := h.newConversation(alice)
	h.exec(`INSERT INTO openfox.conversation_members (organization_id, conversation_id, user_id, access_level)
	        VALUES ($1::uuid, $2::uuid, $3::uuid, 'COMMENTER'), ($1::uuid, $2::uuid, $4::uuid, 'VIEWER')`, orgA, conv, carol, viewr)
	h.exec(`UPDATE openfox.conversations SET visibility = 'SHARED' WHERE id = $1::uuid`, conv)

	// 所有者与评论者（能发消息的人）都能读；内容来自心跳。
	for _, user := range []string{alice, carol} {
		options, err := h.options(user, orgA, conv)
		if err != nil {
			t.Fatalf("%s options: %v", user, err)
		}
		if !options.ComputerOnline || len(options.Runtimes) != 4 {
			t.Fatalf("options = %+v", options)
		}
		byName := map[string]conversations.OverrideOptionRuntime{}
		for _, item := range options.Runtimes {
			byName[item.Provider] = item
		}
		codex := byName["codex"]
		if !codex.Runnable || len(codex.Models) != 1 || codex.Models[0].ID != "gpt-5.1" || !codex.Models[0].IsDefault ||
			codex.Models[0].DisplayName != "GPT" || len(codex.ApprovalModes) != 4 {
			t.Fatalf("codex = %+v", codex)
		}
		if byName["opencode"].Runnable || len(byName["claude-code"].Models) != 2 {
			t.Fatalf("runtimes = %+v", byName)
		}
		// pi 没有上报 approval_modes：只有 FOLLOW_RUNTIME。
		if modes := byName["pi"].ApprovalModes; len(modes) != 1 || modes[0] != "FOLLOW_RUNTIME" {
			t.Fatalf("pi modes = %v", modes)
		}
	}
	// 与修改接口同一套鉴权：访客 403、非成员 404、跨组织 404。
	if _, err := h.options(viewr, orgA, conv); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := h.options(erin, orgA, conv); !errors.Is(err, conversations.ErrNotFound) {
		t.Fatalf("non member: %v", err)
	}
	if _, err := h.options(bob, orgB, conv); !errors.Is(err, conversations.ErrNotFound) {
		t.Fatalf("cross organization: %v", err)
	}
	// 电脑离线：仍返回上一次心跳的列表，computer_online = false。
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() - interval '1 hour' WHERE id = $1::uuid`, device1)
	if options, err := h.options(alice, orgA, conv); err != nil || options.ComputerOnline || len(options.Runtimes) != 4 {
		t.Fatalf("offline options = %+v, %v", options, err)
	}
	// 没有 capabilities（旧设备 / 从未心跳）：空列表，不是错误。
	h.setCapabilities(`{}`)
	if options, err := h.options(alice, orgA, conv); err != nil || len(options.Runtimes) != 0 || options.Runtimes == nil {
		t.Fatalf("empty options = %+v, %v", options, err)
	}
	h.setCapabilities(fullCapabilities)
	// 小队对话与修改接口一样 409。
	crewConv := h.newConversation(alice)
	h.exec(`ALTER TABLE openfox.conversations DISABLE TRIGGER USER`)
	h.exec(`INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'crew') ON CONFLICT DO NOTHING`,
		orgA, crewID, spaceA, alice)
	h.exec(`UPDATE openfox.conversations SET crew_id = $2::uuid WHERE id = $1::uuid`, crewConv, crewID)
	h.exec(`ALTER TABLE openfox.conversations ENABLE TRIGGER USER`)
	if _, err := h.options(alice, orgA, crewConv); !errors.Is(err, conversations.ErrCrewConversation) {
		t.Fatalf("crew: %v", err)
	}
	// 响应里不含电脑名称等设备信息（类型上只有 computer_online 与 runtimes）。
	raw, _ := json.Marshal(conversations.OverrideOptions{})
	if string(raw) != `{"computer_online":false,"runtimes":null}` {
		t.Fatalf("shape = %s", raw)
	}
}

// testNeverWiden：设备不支持要求的权限档位时，Run 不会被领取执行，而是失败并给出原因；Runtime 不可运行时排队等待。
func (h *harness) testNeverWiden(t *testing.T) {
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")

	// 旧版心跳：没有 approval_modes。READ_ONLY 被拒；FOLLOW_RUNTIME 照常派发。
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[{"id":"gpt-5.1"}]},"claude-code":{"runnable":true,"models":[]},"pi":{"runnable":true,"models":[]},"opencode":{"runnable":false,"models":[]}}}`)
	conv := h.newConversation(alice)
	h.patch(alice, conv, conversations.OverridesPatch{ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(0)})
	run := h.sendAndMaterialize(alice, conv, "legacy daemon")
	r := h.run(run)
	if r.State != execution.StateFailed || h.failureCode(run) != "RUNTIME_UNAVAILABLE" || r.OfferID != "" {
		t.Fatalf("READ_ONLY on a daemon without approval_modes: state=%s failure=%s offer=%q", r.State, h.failureCode(run), r.OfferID)
	}
	h.expectCount(`SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'SYSTEM' AND body LIKE '%does not support the READ_ONLY permission mode%'`, 1, conv)
	h.expectCount(`SELECT count(*) FROM openfox.dispatch_offers WHERE run_id = $1::uuid`, 0, run)

	// 清除覆盖后同一台旧设备仍可 FOLLOW_RUNTIME 执行。
	h.patch(alice, conv, conversations.OverridesPatch{ApprovalMode: null(), ExpectedVersion: ver(1)})
	ok := h.sendAndDispatch(alice, conv, "follow runtime still works")
	h.finish(ok, h.claim(ok))

	// pi 只支持 FOLLOW_RUNTIME：即使心跳声称四档也不会，这里心跳不含 approval_modes，非 FOLLOW 档位一律失败。
	conv2 := h.newConversation(alice)
	h.patch(alice, conv2, conversations.OverridesPatch{Runtime: set("pi"), ApprovalMode: set("WORKSPACE_WRITE"), ExpectedVersion: ver(0)})
	piRun := h.sendAndMaterialize(alice, conv2, "pi with a mode")
	if h.run(piRun).State != execution.StateFailed || h.failureCode(piRun) != "RUNTIME_UNAVAILABLE" {
		t.Fatalf("pi run = %+v", h.run(piRun))
	}

	// 档位被设备支持（codex 四档）：READ_ONLY 正常派发。
	h.setCapabilities(fullCapabilities)
	conv3 := h.newConversation(alice)
	h.patch(alice, conv3, conversations.OverridesPatch{ApprovalMode: set("READ_ONLY"), ExpectedVersion: ver(0)})
	good := h.sendAndDispatch(alice, conv3, "supported")
	h.expectSnapshot(good, "codex", "gpt-5.1", "READ_ONLY")
	h.finish(good, h.claim(good))

	// Agent 配置的默认档位不支持时同样失败（不只是对话覆盖）。
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[{"id":"gpt-5.1"}]}}}`)
	h.setAgentConfig("codex", "gpt-5.1", "FULL_ACCESS")
	conv4 := h.newConversation(alice)
	agentRun := h.sendAndMaterialize(alice, conv4, "agent default FULL_ACCESS on a legacy daemon")
	if h.run(agentRun).State != execution.StateFailed {
		t.Fatalf("agent default FULL_ACCESS ran on a daemon without approval_modes: %+v", h.run(agentRun))
	}
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")

	// Runtime 不可运行（心跳 runnable=false）：Run 保持 QUEUED，等待原因 RUNTIME_UNAVAILABLE；恢复可运行后被派发。
	h.setCapabilities(fullCapabilities)
	conv5 := h.newConversation(alice)
	h.patch(alice, conv5, conversations.OverridesPatch{Runtime: set("opencode"), ExpectedVersion: ver(0)})
	waiting := h.sendAndMaterialize(alice, conv5, "opencode is not runnable")
	if r := h.run(waiting); r.State != execution.StateQueued || r.WaitReason != execution.WaitRuntimeUnavailable || r.OfferID != "" {
		t.Fatalf("waiting run = %s/%s offer=%q", r.State, r.WaitReason, r.OfferID)
	}
	h.setCapabilities(strings.Replace(fullCapabilities, `"opencode":{"runnable":false`, `"opencode":{"runnable":true`, 1))
	h.runOnce()
	if r := h.run(waiting); r.OfferID == "" {
		t.Fatalf("run was not offered once the runtime became runnable: %+v", r)
	}
	h.finish(waiting, h.claim(waiting))
	// Runtime 在心跳里完全没有：同样等待。
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[]}}}`)
	conv6 := h.newConversation(alice)
	h.setCapabilities(fullCapabilities)
	h.patch(alice, conv6, conversations.OverridesPatch{Runtime: set("claude-code"), ExpectedVersion: ver(0)})
	h.setCapabilities(`{"runtimes":{"codex":{"runnable":true,"models":[]}}}`)
	missing := h.sendAndMaterialize(alice, conv6, "claude disappeared")
	if r := h.run(missing); r.State != execution.StateQueued || r.WaitReason != execution.WaitRuntimeUnavailable {
		t.Fatalf("missing runtime run = %s/%s", r.State, r.WaitReason)
	}
	h.setCapabilities(fullCapabilities)
}

// testLegacyFallback：迁移之前创建的 Run 没有快照列，设备 Context 回退到配置版本。
func (h *harness) testLegacyFallback(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	conv := h.newConversation(alice)
	run := h.sendAndDispatch(alice, conv, "legacy")
	h.exec(`UPDATE openfox.runs SET runtime = NULL, model = NULL, approval_mode = NULL WHERE id = $1::uuid`, run)
	lease := h.claim(run)
	if got := h.context(lease); got.Runtime != "codex" || got.Model != "gpt-5.1" || got.ApprovalMode != "FOLLOW_RUNTIME" {
		t.Fatalf("legacy context = %+v", got)
	}
	h.finish(run, lease)
	// 快照要么全空要么全有。
	if err := h.tryExec(`UPDATE openfox.runs SET runtime = 'codex' WHERE id = $1::uuid`, run); err == nil {
		t.Fatal("partial snapshot accepted")
	}
}

const crewID = "00000000-0000-0000-0000-0000000000d1"

func set(value string) conversations.OptionalString {
	return conversations.OptionalString{Set: true, Value: &value}
}
func null() conversations.OptionalString { return conversations.OptionalString{Set: true} }
func ver(value int64) *int64             { return &value }

func expectErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

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
		`INSERT INTO openfox.users (id, email) VALUES ('` + carol + `', 'carol@example.test'), ('` + viewr + `', 'viewer@example.test'), ('` + erin + `', 'erin@example.test') ON CONFLICT DO NOTHING`,
		`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ('` + orgA + `', '` + carol + `', 'MEMBER'), ('` + orgA + `', '` + viewr + `', 'MEMBER'), ('` + orgA + `', '` + erin + `', 'MEMBER') ON CONFLICT DO NOTHING`,
		`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ('` + orgA + `', '` + spaceA + `', '` + carol + `', 'MEMBER', 'ACTIVE'), ('` + orgA + `', '` + spaceA + `', '` + viewr + `', 'MEMBER', 'ACTIVE') ON CONFLICT DO NOTHING`,
		`UPDATE openfox.agents SET space_id = '` + spaceA + `', owner_user_id = '` + alice + `', name = 'Override agent', kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = '` + orgA + `' AND id = '` + agentA + `'`,
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

// setAgentConfig 追加一个新的配置版本并设为当前版本（配置版本不可变；测试夹具，不经过 Agent 配置接口）。
func (h *harness) setAgentConfig(runtimeName, model, approval string) {
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
	if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, $3, 'user_request')`, orgA, alice, int64(h.authVersion(orgA, alice))); err != nil {
		h.t.Fatal(err)
	}
	var next int64
	if err := tx.QueryRow(h.ctx, `
		INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model,
		       max_concurrency, queue_timeout_seconds, run_timeout_seconds, approval_mode, created_by)
		SELECT organization_id, agent_id, (SELECT max(config_version) + 1 FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid),
		       computer_id, grant_id, $3, $4, max_concurrency, queue_timeout_seconds, run_timeout_seconds, $5, created_by
		  FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = 1
		RETURNING config_version`, orgA, agentA, runtimeName, model, approval).Scan(&next); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tx.Exec(h.ctx, `UPDATE openfox.agents SET current_config_version = $3 WHERE organization_id = $1::uuid AND id = $2::uuid`, orgA, agentA, next); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) setCapabilities(raw string) {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET capabilities = $1::jsonb, last_seen_at = clock_timestamp() WHERE id = $2::uuid`, raw, device1)
}

func (h *harness) newConversation(user string) string {
	h.t.Helper()
	id := newID(h.t)
	h.inUser(orgA, user, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Create(ctx, tx, orgA, h.actor(orgA, user), conversations.CreateInput{
			ID: id, SpaceID: spaceA, AgentID: agentA, Visibility: "PRIVATE",
		})
		return err
	})
	return id
}

func (h *harness) try(user, org, conv string, patch conversations.OverridesPatch) error {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: h.authVersion(org, user)}
	return h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).UpdateOverrides(ctx, tx, org, conv, h.actor(org, user), patch)
		return err
	})
}

func (h *harness) patch(user, conv string, patch conversations.OverridesPatch) conversations.Summary {
	h.t.Helper()
	var summary conversations.Summary
	h.inUser(orgA, user, func(ctx context.Context, tx pgx.Tx) (err error) {
		summary, err = (conversations.Service{}).UpdateOverrides(ctx, tx, orgA, conv, h.actor(orgA, user), patch)
		return err
	})
	return summary
}

// sendAndMaterialize 发一条 @Agent 消息并跑一轮派发，返回新建的 Run ID（可能因设备能力核对而失败或等待）。
func (h *harness) sendAndMaterialize(user, conv, body string) string {
	h.t.Helper()
	h.inUser(orgA, user, func(ctx context.Context, tx pgx.Tx) error {
		id := newID(h.t)
		_, err := (conversations.Service{}).PostMessage(ctx, tx, orgA, conv, h.actor(orgA, user), conversations.MessageInput{
			AuthenticatedUserInput: true, ID: id, ClientMessageID: "client-" + id, Body: body, Mentions: []conversations.Mention{{AgentID: agentA}},
		})
		return err
	})
	h.runOnce()
	var runID string
	h.queryRow(`SELECT id::text FROM openfox.runs WHERE conversation_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, []any{conv}, &runID)
	return runID
}

func (h *harness) sendAndDispatch(user, conv, body string) string {
	h.t.Helper()
	runID := h.sendAndMaterialize(user, conv, body)
	if r := h.run(runID); r.OfferID == "" {
		h.t.Fatalf("run %s was not offered: state=%s wait=%s", runID, r.State, r.WaitReason)
	}
	return runID
}

func (h *harness) run(runID string) execution.Run {
	h.t.Helper()
	var run execution.Run
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
		run, err = dispatch.LoadRun(ctx, tx, runID)
		return err
	})
	return run
}

func (h *harness) failureCode(runID string) string {
	var code string
	h.queryRow(`SELECT failure_code FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &code)
	return code
}

func (h *harness) expectSnapshot(runID, runtimeName, model, approval string) {
	h.t.Helper()
	var gotRuntime, gotModel, gotApproval string
	h.queryRow(`SELECT runtime, model, approval_mode FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &gotRuntime, &gotModel, &gotApproval)
	if gotRuntime != runtimeName || gotModel != model || gotApproval != approval {
		h.t.Fatalf("run snapshot = %s/%s/%s, want %s/%s/%s", gotRuntime, gotModel, gotApproval, runtimeName, model, approval)
	}
}

func (h *harness) claim(runID string) dispatch.Lease {
	h.t.Helper()
	var offerID string
	h.queryRow(`SELECT offer_id::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &offerID)
	var lease dispatch.Lease
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = h.store.Claim(ctx, tx, orgA, device1, offerID)
		return err
	})
	return lease
}

func (h *harness) context(lease dispatch.Lease) dispatch.RunContext {
	h.t.Helper()
	var result dispatch.RunContext
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = h.store.Context(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})
	return result
}

// finish 走设备结果回写路径把 Run 正常结束（释放容量），让会话可以接收下一个 Run。
func (h *harness) finish(runID string, lease dispatch.Lease) {
	h.t.Helper()
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
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

func (h *harness) runOnce() dispatch.PassResult {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device1)
	result, err := h.worker.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *harness) actor(org, user string) conversations.Actor {
	role := policy.OrganizationRoleMember
	if user == alice || user == bob {
		role = policy.OrganizationRoleOwner
	}
	return conversations.Actor{UserID: user, Role: role, AuthorizationVersion: h.authVersion(org, user)}
}

func (h *harness) authVersion(org, user string) uint64 {
	h.t.Helper()
	var version int64
	if err := h.queryRowErr(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`, []any{org, user}, &version); err != nil {
		// 非该组织成员：返回 1，由被测代码拒绝。
		return 1
	}
	return uint64(version)
}

func (h *harness) inUser(org, user string, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: h.authVersion(org, user)}
	if err := h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inDevice(org string, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: device1, AuthorizationVersion: 1}
	if err := h.pool.RunForOrganization(h.ctx, org, tenancy.SourceDeviceRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func selector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	if err := h.tryExec(query, args...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) tryExec(query string, args ...any) error {
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	_, err = conn.Exec(h.ctx, query, args...)
	return err
}

func (h *harness) queryRow(query string, args []any, dest ...any) {
	h.t.Helper()
	if err := h.queryRowErr(query, args, dest...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) queryRowErr(query string, args []any, dest ...any) error {
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	return conn.QueryRow(h.ctx, query, args...).Scan(dest...)
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
