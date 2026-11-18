package assign

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/policy"
)

// 阶段 11b：评论 @Agent / @小队 触发运行的端到端路径（真实 PostgreSQL、FORCE RLS）：
// 评论事务写入 MENTIONED 事件 → worker 以原作者权限在其任务会话里把评论作为输入发给 Agent（Agent 不必是处理人）→ 物化 Run；
// 小队提及由队长接手并带上小队指令；重复提交同一 client_comment_id 不重复入队、不重复发消息。
func TestCommentMentionStartsAgentRun(t *testing.T) {
	h := newHarness(t)
	taskID := newID(t)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{
			ID: taskID, TypeID: typeA, StatusID: statusTodo, Title: "评论提及任务", Description: "测试任务",
		})
		return err
	})
	actor := conversations.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion()}
	post := func(commentID, clientID, body string, mentions []conversations.Mention) {
		t.Helper()
		h.inUser(func(ctx context.Context, tx pgx.Tx) error {
			_, err := (conversations.Service{}).PostComment(ctx, tx, orgA, spaceA, taskID, actor, conversations.CommentInput{
				ID: commentID, Body: body, ClientCommentID: clientID, Mentions: mentions,
			})
			return err
		})
	}

	// 1. @Agent：Agent 不是处理人，评论仍然触发运行。
	first := newID(t)
	body := "请看一下 @[Assign agent](openfox://mention/agent/" + agentA + ")"
	post(first, "mention-"+first, body, []conversations.Mention{{AgentID: agentA}})
	h.expectRow(`SELECT count(*)::text FROM openfox.task_agent_events WHERE comment_id = $1::uuid AND kind = 'MENTIONED' AND processed_at IS NULL`, []any{first}, "1")
	// 重放同一 client_comment_id：不新增事件。
	post(first, "mention-"+first, body, []conversations.Mention{{AgentID: agentA}})
	h.expectRow(`SELECT count(*)::text FROM openfox.task_agent_events WHERE comment_id = $1::uuid`, []any{first}, "1")

	result := h.runOnce()
	if result.RunsCreated != 1 {
		t.Fatalf("pass = %+v, want 1 run", result)
	}
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE comment_id = $1::uuid`, []any{first}, "started")
	var conversationID string
	h.queryRow(`SELECT id::text FROM openfox.conversations WHERE task_id = $1::uuid AND agent_id = $2::uuid AND origin = 'USER' AND owner_user_id = $3::uuid`,
		[]any{taskID, agentA, alice}, &conversationID)
	var delivered string
	h.queryRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND client_message_id = $2`,
		[]any{conversationID, "comment-mention:" + first + ":" + agentA}, &delivered)
	if !strings.Contains(delivered, "评论中提到了你") || !strings.Contains(delivered, "请看一下 @Assign agent") ||
		strings.Contains(delivered, "openfox://") {
		t.Fatalf("delivered message = %q", delivered)
	}

	// 2. @小队：队长接手，消息带小队指令；同一个会话里再发一条输入。
	crewID := newID(t)
	h.exec(`INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name, visibility, leader_agent_id, instructions)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '评审小队', 'SHARED', $5::uuid, '先评审再动手')`, orgA, crewID, spaceA, alice, agentA)
	h.exec(`INSERT INTO openfox.agent_crew_members (organization_id, crew_id, agent_id, position) VALUES ($1::uuid, $2::uuid, $3::uuid, 0)`, orgA, crewID, agentA)
	second := newID(t)
	post(second, "mention-"+second, "@[评审小队](openfox://mention/crew/"+crewID+") 帮忙", []conversations.Mention{{CrewID: crewID}})
	h.expectRow(`SELECT agent_id::text || ':' || crew_id::text FROM openfox.task_agent_events WHERE comment_id = $1::uuid`, []any{second}, agentA+":"+crewID)
	h.runOnce()
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE comment_id = $1::uuid`, []any{second}, "started")
	h.queryRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND client_message_id = $2`,
		[]any{conversationID, "comment-mention:" + second + ":" + agentA}, &delivered)
	if !strings.Contains(delivered, "小队「评审小队」") || !strings.Contains(delivered, "小队指令：\n先评审再动手") {
		t.Fatalf("crew message = %q", delivered)
	}

	// 3. 触发开关关闭后只保留提及：不再入队。
	h.exec(`UPDATE openfox.agents SET trigger_mention = false WHERE organization_id = $1::uuid AND id = $2::uuid`, orgA, agentA)
	defer h.exec(`UPDATE openfox.agents SET trigger_mention = true WHERE organization_id = $1::uuid AND id = $2::uuid`, orgA, agentA)
	third := newID(t)
	post(third, "mention-"+third, "hi", []conversations.Mention{{AgentID: agentA}})
	h.expectRow(`SELECT count(*)::text FROM openfox.task_agent_events WHERE comment_id = $1::uuid`, []any{third}, "0")
	h.expectRow(`SELECT count(*)::text FROM openfox.comment_mentions WHERE comment_id = $1::uuid`, []any{third}, "1")
}
