package dispatch

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/execution"
)

const (
	maxMessageBody = 60000
	// 与 notifications_summary_valid 约束一致（按字节截断，必然不超过 512 个字符）。
	maxNotificationSummary = 512
)

// AuthorKind 决定终态回写到对话中的消息作者。
type AuthorKind string

const (
	AuthorAgent  AuthorKind = "AGENT"
	AuthorSystem AuthorKind = "SYSTEM"
)

// FinalizeConversation 在 Run 进入终态的同一事务中：把分配给该 Run 的输入标记为
// CONSUMED，向对话追加一条消息，并（可选）给原始发起人发送终态通知。
// 调用方必须已完成 Run 终态 CAS；本函数不改变 Run 状态。
// 失败终态发 RUN_FAILED 通知，其余发 REPLY；通知以 run:<id> 去重；发起人已失去成员资格时跳过通知，不使结果提交失败。
func FinalizeConversation(ctx context.Context, tx pgx.Tx, run execution.Run, author AuthorKind, body string, notify bool) (string, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.conversation_inputs
		   SET status = 'CONSUMED', consumed_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND status = 'ASSIGNED'`, run.ID); err != nil {
		return "", err
	}
	body = boundedText(body, maxMessageBody)
	// 小助手的成功回复里可能带草稿块：拆成正文 + 一条 ACTION_PROPOSAL 草稿卡。
	var proposal *conversations.ProposalPayload
	if author == AuthorAgent && run.State == execution.StateSucceeded {
		builtin, err := assistantAgent(ctx, tx, run.AgentID)
		if err != nil {
			return "", err
		}
		if builtin {
			body, proposal = conversations.SplitAssistantReply(body)
		}
	}
	messageID := ""
	if body != "" || proposal == nil {
		if body == "" {
			body = defaultTerminalBody(run.State)
		}
		id, err := insertTerminalMessage(ctx, tx, run, author, body)
		if err != nil {
			return "", err
		}
		messageID = id
	}
	if proposal != nil {
		encoded, err := json.Marshal(proposal)
		if err != nil {
			return "", err
		}
		summary := proposalSummary(*proposal)
		id, err := conversations.AppendAgentMessage(ctx, tx, run.ConversationID, run.AgentID,
			conversations.MessageKindProposal, summary, encoded)
		if err != nil {
			return "", err
		}
		if body == "" {
			body = summary
		}
		messageID = id
	}
	// 指派会话：运行成功且 Agent 没有自己设置等待状态时，交给人验收（见 task_tools.go）。
	if err := onAssignmentRunSucceeded(ctx, tx, run); err != nil {
		return "", err
	}
	if !notify {
		return messageID, nil
	}
	if run.State == execution.StateSucceeded {
		if _, err := tx.Exec(ctx, `SELECT openfox_private.enqueue_notification(m.user_id,'RUN_COMPLETED',$2::uuid,$3::uuid,NULLIF($4,'')::uuid,NULL,$5,'Agent execution completed.')
            FROM openfox.organization_members m WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$1 AND m.status='ACTIVE'`,
			run.ActorID, run.ConversationID, messageID, run.TaskID, "run-completed:"+run.ID); err != nil {
			return "", err
		}
	}
	kind, summary := notificationFor(run, body)
	if kind == "REPLY" && (author != AuthorAgent || run.TaskID == "") {
		return messageID, nil
	}
	if _, err := tx.Exec(ctx, `
		SELECT openfox_private.enqueue_notification(
			m.user_id, $6, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, NULL::uuid,
			$5, $7)
        FROM openfox.conversations c JOIN openfox.organization_members m ON m.organization_id=c.organization_id
            AND m.user_id=CASE WHEN $6='REPLY' THEN c.owner_user_id ELSE $1::uuid END AND m.status='ACTIVE'
        WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$2`,
		run.ActorID, run.ConversationID, messageID, run.TaskID, "run:"+run.ID, kind, summary); err != nil {
		return "", err
	}
	return messageID, nil
}

func insertTerminalMessage(ctx context.Context, tx pgx.Tx, run execution.Run, author AuthorKind, body string) (string, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.conversations
		   SET next_message_seq = next_message_seq + 1,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING next_message_seq - 1`, run.ConversationID).Scan(&seq); err != nil {
		return "", err
	}
	messageID, err := newUUID()
	if err != nil {
		return "", err
	}
	agentID := ""
	if author == AuthorAgent {
		agentID = run.AgentID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.messages (
			organization_id, id, conversation_id, seq, author_kind, author_agent_id, body
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4,
		          NULLIF($5, '')::uuid, $6)`,
		messageID, run.ConversationID, seq, string(author), agentID, body); err != nil {
		return "", err
	}
	return messageID, nil
}

// assistantAgent 判断 Run 的 Agent 是不是内置小助手（只有它的回复会解析草稿块）。
func assistantAgent(ctx context.Context, tx pgx.Tx, agentID string) (bool, error) {
	var builtin bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND builtin_kind = 'ASSISTANT')`, agentID).Scan(&builtin)
	return builtin, err
}

// proposalSummary 是草稿卡消息的正文（列表预览与通知用），以「已生成：」开头。
func proposalSummary(proposal conversations.ProposalPayload) string {
	name := ""
	switch {
	case proposal.Agent != nil:
		name = proposal.Agent.Name
	case proposal.Task != nil:
		name = proposal.Task.Title
	}
	if name == "" {
		return "已生成草稿"
	}
	return "已生成：" + name
}

// notificationFor 决定终态通知的类型与摘要：失败的 Run 发 RUN_FAILED，摘要取失败原因
// （收件箱行内的红色错误胶囊），其余终态沿用 REPLY。
func notificationFor(run execution.Run, body string) (string, string) {
	if run.State != execution.StateFailed {
		return "REPLY", "An Agent replied in a conversation."
	}
	summary := boundedText(run.Reason, maxNotificationSummary)
	if summary == "" {
		summary = boundedText(body, maxNotificationSummary)
	}
	if summary == "" {
		summary = defaultTerminalBody(run.State)
	}
	return "RUN_FAILED", summary
}

func defaultTerminalBody(state execution.State) string {
	switch state {
	case execution.StateSucceeded:
		return "The Agent finished without a text reply."
	case execution.StateCanceled:
		return "The Agent run was canceled."
	default:
		return "The Agent run failed."
	}
}
