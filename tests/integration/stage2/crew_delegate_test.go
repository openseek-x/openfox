package stage2

// 阶段 12f2 小队队长委派（crew_delegate）集成测试：队长委派成功、非队长/成员/小队外/自己/已归档被拒、
// 每次运行最多 5 次（含并发）、同一调用重试幂等、成员运行没有该工具且无法再委派、
// 发起真人无权时被拒、成员运行发起人为触发队长的真人、跨组织隔离。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

func delegateSelector(sel tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return sel, nil
	})
}

// activeRun 把会话里最新的 QUEUED Run 直接置为 RUNNING 并给有效租约，返回 (runID, generation)。
func (f crewFixture) activeRun(conversationID string) (string, int64) {
	f.env.t.Helper()
	var runID string
	var generation int64
	f.env.scalar(&runID, `SELECT id::text FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
		AND state IN ('QUEUED','PREPARING','RUNNING') ORDER BY created_at DESC, id DESC LIMIT 1`, f.env.org, conversationID)
	f.env.exec(`UPDATE openfox.runs SET state = 'RUNNING', started_at = clock_timestamp(),
		lease_expires_at = clock_timestamp() + interval '10 minutes' WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID)
	f.env.scalar(&generation, `SELECT generation FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID)
	return runID, generation
}

func (f crewFixture) toolIn(org, runID string, generation int64, tool, callID string, args map[string]any) (dispatch.ToolResult, error) {
	raw, _ := json.Marshal(args)
	var result dispatch.ToolResult
	sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: f.env.deviceID, AuthorizationVersion: 1}
	err := f.worker.Pool.RunForOrganization(f.env.ctx, org, tenancy.SourceDeviceRequest, delegateSelector(sel), func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = f.worker.Store.CallTool(ctx, tx, org, f.env.deviceID, runID, tool, dispatch.ToolCall{Generation: generation, CallID: callID, Arguments: raw})
		return err
	})
	return result, err
}

func (f crewFixture) delegate(runID string, generation int64, callID, agent, text string) dispatch.ToolResult {
	f.env.t.Helper()
	result, err := f.toolIn(f.env.org, runID, generation, "crew_delegate", callID, map[string]any{"agent": agent, "instruction": text})
	if err != nil {
		f.env.t.Fatalf("crew_delegate: %v", err)
	}
	return result
}

func (f crewFixture) delegatedInputs(conversationID string) int {
	var n int
	f.env.scalar(&n, `SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
		AND source_event_id LIKE 'crew-delegate:%'`, f.env.org, conversationID)
	return n
}

// leaderRunFor 让真人 user 触发一次队长运行并置为 RUNNING。
func (f crewFixture) leaderRunFor(user *agClient, conversationID string) (string, int64) {
	f.mustSend(user, conversationID, "请协调")
	f.pass()
	return f.activeRun(conversationID)
}

func TestCrewDelegate(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)
	runID, gen := f.leaderRunFor(user, conv.ID)

	t.Run("leader delegates to a member and the run is attributed to the human", func(t *testing.T) {
		got := f.delegate(runID, gen, "c1", "Worker", "整理发布说明")
		if !got.OK || !strings.Contains(got.Text, "after your current run finishes") {
			t.Fatalf("result = %+v", got)
		}
		if f.delegatedInputs(conv.ID) != 1 {
			t.Fatal("expected one delegated input")
		}
		var author, kind string
		f.env.scalar(&author, `SELECT author_agent_id::text FROM openfox.messages WHERE organization_id=$1::uuid AND conversation_id=$2::uuid AND client_message_id LIKE 'crew-delegate:%'`, f.env.org, conv.ID)
		if author != f.leader.ID {
			t.Fatalf("message author = %s", author)
		}
		_ = kind
		// 成员要等队长这次运行结束后才开始（共用目录串行）。
		f.pass()
		if got := f.runs(conv.ID); len(got) != 1 {
			t.Fatalf("member run started before leader finished: %v", got)
		}
		f.finishRuns(conv.ID)
		f.pass()
		runs := f.runs(conv.ID)
		if len(runs) != 2 || runs[1] != f.member.ID {
			t.Fatalf("runs = %v", runs)
		}
		var actor string
		f.env.scalar(&actor, `SELECT actor_id::text FROM openfox.runs WHERE organization_id=$1::uuid AND conversation_id=$2::uuid AND agent_id=$3::uuid`, f.env.org, conv.ID, f.member.ID)
		if actor != agMemberID {
			t.Fatalf("member run actor = %s, want the triggering human %s", actor, agMemberID)
		}
	})

	t.Run("member run has no crew_delegate and cannot delegate", func(t *testing.T) {
		memberRun, memberGen := f.activeRun(conv.ID)
		got, err := f.toolIn(f.env.org, memberRun, memberGen, "crew_delegate", "m1", map[string]any{"agent": "Lead", "instruction": "again"})
		if err != nil || got.OK {
			t.Fatalf("member delegate = %+v, %v", got, err)
		}
		if f.delegatedInputs(conv.ID) != 1 {
			t.Fatal("member created a new delegation")
		}
		f.finishRuns(conv.ID)
	})
}

func TestCrewDelegateRejections(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)
	runID, gen := f.leaderRunFor(user, conv.ID)

	for name, c := range map[string]struct{ agent, text string }{
		"outside agent": {"Outsider", "x"},
		"leader itself": {"Lead", "x"},
		"unknown agent": {"Nobody", "x"},
		"blank text":    {"Worker", "  "},
		"too long text": {"Worker", strings.Repeat("a", 10001)},
	} {
		if got := f.delegate(runID, gen, "r-"+strings.ReplaceAll(name, " ", ""), c.agent, c.text); got.OK {
			t.Fatalf("%s accepted: %+v", name, got)
		}
	}
	// 小队外的 Agent 即便用 ID 也不行。
	if got := f.delegate(runID, gen, "r-id", f.outside.ID, "x"); got.OK {
		t.Fatalf("outside by id accepted: %+v", got)
	}
	// 已归档成员。
	f.env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.env.org, f.member.ID)
	if got := f.delegate(runID, gen, "r-arch", "Worker", "x"); got.OK {
		t.Fatalf("archived accepted: %+v", got)
	}
	f.env.exec(`UPDATE openfox.agents SET archived_at = NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, f.env.org, f.member.ID)
	// 对触发真人不可见（私有且属于他人）：按原始触发人计算，拒绝。
	f.env.exec(`UPDATE openfox.agents SET visibility='PRIVATE', owner_user_id=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, f.env.org, f.member.ID, agManagerID)
	if got := f.delegate(runID, gen, "r-priv", "Worker", "x"); got.OK {
		t.Fatalf("invisible member accepted: %+v", got)
	}
	f.env.exec(`UPDATE openfox.agents SET visibility='SHARED' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.env.org, f.member.ID)
	if n := f.delegatedInputs(conv.ID); n != 0 {
		t.Fatalf("rejected calls produced %d inputs", n)
	}
	// 跨组织：另一个组织看不到该 Run。
	if _, err := f.toolIn(f.env.otherOrg, runID, gen, "crew_delegate", "x-org", map[string]any{"agent": "Worker", "instruction": "x"}); err == nil {
		t.Fatal("cross-organization call succeeded")
	}
	if n := f.delegatedInputs(conv.ID); n != 0 {
		t.Fatalf("cross-org produced %d inputs", n)
	}
}

func TestCrewDelegateNonLeaderAndPlainConversation(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	// 成员在小队对话里被 @ 运行：不是队长，拒绝。
	conv := f.newConversation(user)
	f.mustSend(user, conv.ID, "@Worker 你好", agentMention(f.member.ID))
	f.pass()
	runID, gen := f.activeRun(conv.ID)
	got, err := f.toolIn(f.env.org, runID, gen, "crew_delegate", "n1", map[string]any{"agent": "Lead", "instruction": "x"})
	if err != nil || got.OK {
		t.Fatalf("non-leader delegate = %+v, %v", got, err)
	}
	// 非小队对话：拒绝。
	var plain crewConversation
	user.must(201, "POST", "conversations", map[string]any{"space_id": f.space, "agent_id": f.leader.ID}, &plain)
	f.mustSend(user, plain.ID, "hi", agentMention(f.leader.ID))
	f.pass()
	plainRun, plainGen := f.activeRun(plain.ID)
	got, err = f.toolIn(f.env.org, plainRun, plainGen, "crew_delegate", "p1", map[string]any{"agent": "Worker", "instruction": "x"})
	if err != nil || got.OK {
		t.Fatalf("plain conversation delegate = %+v, %v", got, err)
	}
}

func TestCrewDelegateLimitAndIdempotency(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)
	runID, gen := f.leaderRunFor(user, conv.ID)

	// 同一 call_id 重试：只生成一次。
	for i := 0; i < 3; i++ {
		if got := f.delegate(runID, gen, "same", "Worker", "任务 0"); !got.OK {
			t.Fatalf("retry %d = %+v", i, got)
		}
	}
	// 同一 Run 内对同一成员重复同样的说明（不同 call_id）也只生成一次。
	if got := f.delegate(runID, gen, "other-call", "Worker", "任务 0"); !got.OK {
		t.Fatalf("duplicate content = %+v", got)
	}
	if n := f.delegatedInputs(conv.ID); n != 1 {
		t.Fatalf("inputs after retries = %d, want 1", n)
	}
	// 同一 call_id 不同内容被拒。
	if got := f.delegate(runID, gen, "same", "Worker", "别的内容"); got.OK {
		t.Fatalf("conflicting reuse accepted: %+v", got)
	}
	// 并发：8 个不同委派同时到达，总数不得超过 5。
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.toolIn(f.env.org, runID, gen, "crew_delegate", "par-"+string(rune('a'+i)), map[string]any{"agent": "Worker", "instruction": "并发任务 " + string(rune('a'+i))})
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("concurrent call: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if n := f.delegatedInputs(conv.ID); n != 5 {
		t.Fatalf("delegated inputs = %d, want exactly 5", n)
	}
	// 第 6 次被拒。
	got := f.delegate(runID, gen, "sixth", "Worker", "第六个")
	if got.OK || !strings.Contains(got.Text, "limit") {
		t.Fatalf("sixth = %+v", got)
	}
	// 队长的下一次运行重新计数。
	f.finishRuns(conv.ID)
	time.Sleep(10 * time.Millisecond)
}
