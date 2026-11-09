package stage2

// 小队对话集成测试：创建、@ 校验、按 @ / 队长分派、同一会话串行派发、成员被移出后的拒绝、
// 重复与并发提交、旧会话（crew_id 为空）行为不变。直接驱动派发 worker 的组织扫描。

import (
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
)

type crewFixture struct {
	env      *agEnv
	worker   *dispatch.Worker
	space    string
	leader   agAgent
	member   agAgent
	outside  agAgent // 同空间但不在小队里
	crewID   string
	manager  *agClient
	chatUser *agClient
}

func newCrewFixture(t *testing.T) crewFixture {
	e := newAgEnv(t)
	e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 32)
		ON CONFLICT (organization_id) DO UPDATE SET max_slots = 32`, e.org)
	e.exec(`UPDATE openfox.physical_devices SET max_slots = 8, last_seen_at = clock_timestamp() WHERE id = $1::uuid`, e.deviceID)
	space := e.createSpace("SHARED")
	manager := e.client(agManagerID)
	leader := e.createAgent(manager, space, "SHARED", map[string]any{"name": "Lead"})
	member := e.createAgent(manager, space, "SHARED", map[string]any{"name": "Worker"})
	outside := e.createAgent(manager, space, "SHARED", map[string]any{"name": "Outsider"})
	var crew agCrew
	manager.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/crews", map[string]any{
		"name": "Crew " + agSuffix(t), "leader_agent_id": leader.ID, "agent_ids": []string{leader.ID, member.ID},
	}, &crew)
	database, err := postgres.New(e.ctx, postgres.Config{
		URL: os.Getenv("OPENFOX_TEST_DATABASE_URL"), MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return crewFixture{env: e, worker: &dispatch.Worker{Pool: database, Store: store}, space: space,
		leader: leader, member: member, outside: outside, crewID: crew.ID, manager: manager, chatUser: e.client(agMemberID)}
}

type crewConversation struct {
	ID     string `json:"id"`
	CrewID string `json:"crew_id"`
	Agent  string `json:"agent_id"`
}

func (f crewFixture) newConversation(client *agClient) crewConversation {
	f.env.t.Helper()
	var created crewConversation
	client.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID}, &created)
	return created
}

func (f crewFixture) send(client *agClient, conversationID, clientID, body string, mentions ...map[string]string) agResponse {
	return client.do(http.MethodPost, "conversations/"+conversationID+"/messages",
		map[string]any{"body": body, "client_message_id": clientID, "mentions": mentions})
}

func (f crewFixture) mustSend(client *agClient, conversationID, body string, mentions ...map[string]string) {
	f.env.t.Helper()
	response := f.send(client, conversationID, "c-"+agSuffix(f.env.t), body, mentions...)
	if response.Status != http.StatusCreated {
		f.env.t.Fatalf("send = %d %s", response.Status, response.Body)
	}
}

func (f crewFixture) pass() {
	f.env.t.Helper()
	if _, err := f.worker.RunOrganization(f.env.ctx, f.env.org); err != nil {
		f.env.t.Fatalf("RunOrganization: %v", err)
	}
}

// inputs 返回会话输入的 (目标 Agent, 状态)，按创建顺序。
func (f crewFixture) inputs(conversationID string) [][2]string {
	f.env.t.Helper()
	rows, err := f.env.superuser.Query(f.env.ctx, `SELECT COALESCE(target_agent_id::text, ''), status FROM openfox.conversation_inputs
		 WHERE organization_id = $1::uuid AND conversation_id = $2::uuid ORDER BY created_at, id`, f.env.org, conversationID)
	if err != nil {
		f.env.t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var item [2]string
		if err := rows.Scan(&item[0], &item[1]); err != nil {
			f.env.t.Fatal(err)
		}
		out = append(out, item)
	}
	return out
}

// runs 返回会话的 Run 执行 Agent 序列（按创建顺序）。
func (f crewFixture) runs(conversationID string) []string {
	f.env.t.Helper()
	rows, err := f.env.superuser.Query(f.env.ctx, `SELECT agent_id::text FROM openfox.runs
		 WHERE organization_id = $1::uuid AND conversation_id = $2::uuid ORDER BY created_at, id`, f.env.org, conversationID)
	if err != nil {
		f.env.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			f.env.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (f crewFixture) finishRuns(conversationID string) {
	f.env.exec(`UPDATE openfox.runs SET state = 'SUCCEEDED', started_at = COALESCE(started_at, clock_timestamp()), finished_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')`, f.env.org, conversationID)
}

func (f crewFixture) systemMessages(conversationID string) int {
	var n int
	f.env.scalar(&n, `SELECT count(*) FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'SYSTEM'`,
		f.env.org, conversationID)
	return n
}

func agentMention(id string) map[string]string { return map[string]string{"agent_id": id} }
func userMention(id string) map[string]string  { return map[string]string{"user_id": id} }

func TestCrewConversationCreate(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	created := f.newConversation(user)
	if created.CrewID != f.crewID || created.Agent != f.leader.ID {
		t.Fatalf("created = %+v", created)
	}
	// 详情里带 crew_id；agent 是队长。
	var summary struct {
		CrewID string `json:"crew_id"`
		Agent  struct {
			ID string `json:"id"`
		} `json:"agent"`
	}
	user.must(http.StatusOK, http.MethodGet, "conversations/"+created.ID, nil, &summary)
	if summary.CrewID != f.crewID || summary.Agent.ID != f.leader.ID {
		t.Fatalf("summary = %+v", summary)
	}
	// 传 agent_id 时必须等于队长。
	user.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID, "agent_id": f.leader.ID}, nil)
	user.expectError(http.StatusBadRequest, "", http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID, "agent_id": f.member.ID})
	// 小队会话可以挂任务，任务校验与 Agent 会话相同：不存在的任务 404（成功路径见 stage9f）。
	// 小队须在该空间；小队不存在 / 其他组织不可见。
	user.expectError(http.StatusNotFound, "", http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID, "task_id": agNewID(t)})
	other := f.env.createSpace("SHARED")
	user.expectError(http.StatusBadRequest, "", http.MethodPost, "conversations", map[string]any{"space_id": other, "crew_id": f.crewID})
	user.expectError(http.StatusNotFound, "", http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": agNewID(t)})
	f.env.client(agOutsiderID).doIn(f.env.otherOrg, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID}).
		mustStatus(t, http.StatusNotFound)
	// 队长被归档后，由下一个可用成员作为队长；全部不可用时 403。
	f.env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, f.leader.ID)
	var next crewConversation
	user.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID}, &next)
	if next.Agent != f.member.ID {
		t.Fatalf("fallback leader = %s, want %s", next.Agent, f.member.ID)
	}
	f.env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, f.member.ID)
	user.expectError(http.StatusForbidden, "", http.MethodPost, "conversations", map[string]any{"space_id": f.space, "crew_id": f.crewID})
}

func TestCrewConversationDispatch(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)

	t.Run("no mention goes to the leader", func(t *testing.T) {
		f.mustSend(user, conv.ID, "大家好")
		if got := f.inputs(conv.ID); len(got) != 1 || got[0][0] != f.leader.ID {
			t.Fatalf("inputs = %v", got)
		}
		f.pass()
		if got := f.runs(conv.ID); len(got) != 1 || got[0] != f.leader.ID {
			t.Fatalf("runs = %v", got)
		}
		f.finishRuns(conv.ID)
	})

	t.Run("mention routes to that member", func(t *testing.T) {
		f.mustSend(user, conv.ID, "@Worker 帮忙看下", agentMention(f.member.ID))
		f.pass()
		if got := f.runs(conv.ID); len(got) != 2 || got[1] != f.member.ID {
			t.Fatalf("runs = %v", got)
		}
		f.finishRuns(conv.ID)
	})

	t.Run("two mentions run one after another in order", func(t *testing.T) {
		f.mustSend(user, conv.ID, "两位都看下", agentMention(f.member.ID), agentMention(f.leader.ID))
		f.pass()
		got := f.runs(conv.ID)
		if len(got) != 3 || got[2] != f.member.ID {
			t.Fatalf("runs after first pass = %v", got)
		}
		// 第一个 Run 未结束时，第二个目标的输入保持 PENDING，不会并行再建 Run。
		f.pass()
		if again := f.runs(conv.ID); len(again) != 3 {
			t.Fatalf("second run created while first active: %v", again)
		}
		f.finishRuns(conv.ID)
		f.pass()
		if final := f.runs(conv.ID); len(final) != 4 || final[3] != f.leader.ID {
			t.Fatalf("runs = %v", final)
		}
		f.finishRuns(conv.ID)
	})

	t.Run("later input never overtakes an earlier one for another target", func(t *testing.T) {
		f.mustSend(user, conv.ID, "先给成员", agentMention(f.member.ID))
		f.mustSend(user, conv.ID, "再给队长", agentMention(f.leader.ID))
		f.mustSend(user, conv.ID, "又给成员", agentMention(f.member.ID))
		for round := 0; round < 3; round++ {
			f.pass()
			f.finishRuns(conv.ID)
		}
		got := f.runs(conv.ID)
		tail := got[len(got)-3:]
		if tail[0] != f.member.ID || tail[1] != f.leader.ID || tail[2] != f.member.ID {
			t.Fatalf("run order = %v", tail)
		}
	})

	t.Run("same target consecutive inputs merge into one run", func(t *testing.T) {
		before := len(f.runs(conv.ID))
		f.mustSend(user, conv.ID, "第一条", agentMention(f.leader.ID))
		f.mustSend(user, conv.ID, "第二条", agentMention(f.leader.ID))
		f.pass()
		if got := f.runs(conv.ID); len(got) != before+1 {
			t.Fatalf("runs = %d, want %d", len(got), before+1)
		}
		f.finishRuns(conv.ID)
	})

	t.Run("mention validation", func(t *testing.T) {
		for _, mention := range []map[string]string{agentMention(f.outside.ID), agentMention(agNewID(t))} {
			f.send(user, conv.ID, "c-"+agSuffix(t), "@x", mention).mustStatus(t, http.StatusBadRequest)
		}
		// 只 @ 成员的消息是成员之间的交流，不触发执行。
		before := len(f.inputs(conv.ID))
		f.mustSend(user, conv.ID, "@同事 看一下", userMention(agMemberID))
		if after := len(f.inputs(conv.ID)); after != before {
			t.Fatalf("user-only mention created inputs: %d -> %d", before, after)
		}
		// 被归档的成员不能再被 @。
		f.env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, f.outside.ID)
		f.send(user, conv.ID, "c-"+agSuffix(t), "@x", agentMention(f.outside.ID)).mustStatus(t, http.StatusBadRequest)
	})

	t.Run("outsiders cannot post", func(t *testing.T) {
		f.send(f.env.client(agGuestID), conv.ID, "c-"+agSuffix(t), "hi").mustStatus(t, http.StatusNotFound)
	})
}

// 成员被移出小队后，针对它的输入按 Agent 不可用拒绝（写 SYSTEM 消息），后面的输入不被阻塞。
func TestCrewConversationRemovedMember(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)
	f.mustSend(user, conv.ID, "给成员", agentMention(f.member.ID))
	f.mustSend(user, conv.ID, "给队长", agentMention(f.leader.ID))
	f.manager.must(http.StatusNoContent, http.MethodDelete, "spaces/"+f.space+"/crews/"+f.crewID+"/members/agent/"+f.member.ID, nil, nil)
	f.pass()
	if got := f.inputs(conv.ID); got[0][1] != "REJECTED" {
		t.Fatalf("inputs = %v", got)
	}
	if f.systemMessages(conv.ID) != 1 {
		t.Fatalf("system messages = %d", f.systemMessages(conv.ID))
	}
	f.pass()
	if got := f.runs(conv.ID); len(got) != 1 || got[0] != f.leader.ID {
		t.Fatalf("runs = %v", got)
	}
	// 被移出的成员也不能再被 @。
	f.send(user, conv.ID, "c-"+agSuffix(t), "x", agentMention(f.member.ID)).mustStatus(t, http.StatusBadRequest)
}

func TestCrewConversationDuplicateAndConcurrentSubmit(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	conv := f.newConversation(user)
	first := f.send(user, conv.ID, "same-id", "你好")
	first.mustStatus(t, http.StatusCreated)
	f.send(user, conv.ID, "same-id", "你好").mustStatus(t, http.StatusCreated)
	f.send(user, conv.ID, "same-id", "内容不同").mustStatus(t, http.StatusConflict)
	if got := f.inputs(conv.ID); len(got) != 1 {
		t.Fatalf("inputs after duplicate = %v", got)
	}
	var wg sync.WaitGroup
	statuses := make([]int, 6)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.send(user, conv.ID, "race-id", "并发", agentMention(f.member.ID)).Status
		}(i)
	}
	wg.Wait()
	for _, status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("concurrent statuses = %v", statuses)
		}
	}
	var messages int
	f.env.scalar(&messages, `SELECT count(*) FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND client_message_id = 'race-id'`, f.env.org, conv.ID)
	if messages != 1 || len(f.inputs(conv.ID)) != 2 {
		t.Fatalf("messages = %d inputs = %v", messages, f.inputs(conv.ID))
	}
}

// 回归：旧会话（crew_id 为空）的分派与校验与改动前完全一致。
func TestLegacyConversationUnchangedByCrewSupport(t *testing.T) {
	f := newCrewFixture(t)
	user := f.chatUser
	var conv convSummary
	user.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "agent_id": f.leader.ID}, &conv)
	f.send(user, conv.ID, "c-1", "无 @", nil...).mustStatus(t, http.StatusCreated)
	if got := f.inputs(conv.ID); len(got) != 0 {
		t.Fatalf("legacy message without mention created inputs: %v", got)
	}
	f.send(user, conv.ID, "c-2", "@Lead", agentMention(f.leader.ID)).mustStatus(t, http.StatusCreated)
	if got := f.inputs(conv.ID); len(got) != 1 || got[0][0] != "" {
		t.Fatalf("legacy input = %v (target must stay NULL)", got)
	}
	// 同一小队里的其他成员不能被 @。
	f.send(user, conv.ID, "c-3", "@Worker", agentMention(f.member.ID)).mustStatus(t, http.StatusBadRequest)
	f.pass()
	if got := f.runs(conv.ID); len(got) != 1 || got[0] != f.leader.ID {
		t.Fatalf("runs = %v", got)
	}
}
