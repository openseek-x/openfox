package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/execution"
)

// 阶段 12f2 小队队长委派（功能清单 M-01）：队长 Agent 通过 crew_delegate 工具把工作交给同小队成员 Agent。
// 效果等同于人在小队对话里 @ 该成员：写一条队长作者的消息（带 @ 成员）和一条目标为该成员的执行输入，
// 之后由现有的 MaterializeInputs / 派发路径处理，小队共用目录串行、容量、排队、超时规则全部沿用。
const (
	// crewDelegateLimit 是队长每次 Run 最多委派的次数。
	crewDelegateLimit = 5
	// crewDelegateSourcePrefix 是委派输入 source_event_id 的前缀；完整键为 prefix + 队长 runID + ":" + callID。
	// 同一个键只能产生一次输入与一个触发（唯一约束），工具调用重试因此不会重复生成成员运行。
	crewDelegateSourcePrefix = "crew-delegate:"
)

func crewDelegateSource(runID, callID string) string {
	return crewDelegateSourcePrefix + runID + ":" + callID
}

// crewLeaderRun 判断 run 是否是小队对话里队长的运行，并且不是由委派产生的运行（防循环）。
// 队长以发起人可见的成员集合解析（与 @ 分派一致）；成员运行、非小队对话、被委派产生的运行都返回 false。
func crewLeaderRun(ctx context.Context, tx pgx.Tx, run execution.Run) (string, bool, error) {
	var crewID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(crew_id::text, '') FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, run.ConversationID).Scan(&crewID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if crewID == "" {
		return "", false, nil
	}
	_, leader, err := conversations.CrewMembers(ctx, tx, crewID, run.SpaceID, run.ActorID)
	if err != nil || leader == "" || leader != run.AgentID {
		return "", false, err
	}
	var delegated bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.conversation_inputs
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid
		   AND left(source_event_id, $3) = $2)`, run.ID, crewDelegateSourcePrefix, len(crewDelegateSourcePrefix)).Scan(&delegated); err != nil {
		return "", false, err
	}
	return crewID, !delegated, nil
}

// toolCrewDelegate 实现 crew_delegate。调用方 CallTool 已持有该 Run 的行锁（FOR UPDATE），
// 所以同一队长 Run 的并发调用在这里串行，计数判断不会被并发突破。
func (s Store) toolCrewDelegate(ctx context.Context, tx pgx.Tx, run execution.Run, callID string, args map[string]json.RawMessage) (ToolResult, error) {
	agentArg, ok := socialArg(args, "agent", 254)
	if !ok || agentArg == "" {
		return ToolResult{Text: "agent is required (a crew member agent's name or ID)"}, nil
	}
	instruction, ok := socialArg(args, "instruction", maxToolCommentBody)
	if !ok || instruction == "" {
		return ToolResult{Text: fmt.Sprintf("instruction must be 1-%d characters", maxToolCommentBody)}, nil
	}
	crewID, leader, err := crewLeaderRun(ctx, tx, run)
	if err != nil {
		return ToolResult{}, err
	}
	if !leader {
		return ToolResult{Text: "crew_delegate is only available to the crew leader in a crew conversation"}, nil
	}
	// 解析成员：按 ID 或名称匹配本小队的 Agent（真人成员、小队外 Agent 都匹配不到）。
	var memberID, memberName string
	err = tx.QueryRow(ctx, `
		SELECT a.id::text, a.name FROM openfox.agent_crew_members m
		  JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.agent_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.crew_id = $1::uuid
		   AND (a.id::text = lower($2) OR lower(a.name) = lower($2))
		 LIMIT 1`, crewID, agentArg).Scan(&memberID, &memberName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "agent is not a member of this crew"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	if memberID == run.AgentID {
		return ToolResult{Text: "the leader cannot delegate to itself"}, nil
	}
	// 可用成员 = 同空间、ACTIVE、未归档，并且对触发运行的真人可见（J-04：按原始触发人计算）。
	available, _, err := conversations.CrewMembers(ctx, tx, crewID, run.SpaceID, run.ActorID)
	if err != nil {
		return ToolResult{}, err
	}
	usable := false
	for _, id := range available {
		usable = usable || id == memberID
	}
	if !usable {
		return ToolResult{Text: "member agent is unavailable (archived, inactive or not accessible to the requester)"}, nil
	}
	var leaderName string
	if err := tx.QueryRow(ctx, `SELECT name FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, run.AgentID).Scan(&leaderName); err != nil {
		return ToolResult{}, err
	}
	body := fmt.Sprintf("[Crew delegation] Crew leader %s (run %s) delegated this to you:\n\n%s", leaderName, run.ID, instruction)
	source := crewDelegateSource(run.ID, callID)
	// 幂等：同一键（run + call_id）已写过则按内容比较；同一 Run 里对同一成员重复提交同样的说明也视为已委派。
	var existingTarget, existingBody string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(target_agent_id::text, ''), body FROM openfox.conversation_inputs
		 WHERE organization_id = openfox_private.current_organization_id() AND source_event_id = $1`, source).Scan(&existingTarget, &existingBody)
	if err == nil {
		if existingTarget == memberID && existingBody == body {
			return ToolResult{OK: true, Text: "delegation already queued"}, nil
		}
		return ToolResult{Text: "call ID already used for different content"}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{}, err
	}
	var used int
	var duplicate bool
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(bool_or(target_agent_id = $3::uuid AND body = $4), false)
		  FROM openfox.conversation_inputs
		 WHERE organization_id = openfox_private.current_organization_id() AND conversation_id = $1::uuid
		   AND left(source_event_id, length($2)) = $2`,
		run.ConversationID, crewDelegateSourcePrefix+run.ID+":", memberID, body).Scan(&used, &duplicate); err != nil {
		return ToolResult{}, err
	}
	if duplicate {
		return ToolResult{OK: true, Text: "delegation already queued"}, nil
	}
	if used >= crewDelegateLimit {
		return ToolResult{Text: fmt.Sprintf("delegation limit reached: at most %d delegations per run", crewDelegateLimit)}, nil
	}
	// 调用时就按真人权限做一次与派发相同的核验（Agent 可用、电脑授权、空间与任务门禁），不通过直接拒绝。
	// 委派等同人工 @ 成员，不是任务指派的自动触发，所以 automatic 传 false（待规划任务按人工触发的现有规则判定）。
	var lifecycle string
	if err := tx.QueryRow(ctx, `SELECT lifecycle FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, run.ConversationID).Scan(&lifecycle); err != nil {
		return ToolResult{}, err
	}
	reason, _, err := s.evaluate(ctx, tx, run.OrganizationID, run.ConversationID, lifecycle, memberID, run.ActorID, false)
	if err != nil {
		return ToolResult{}, err
	}
	if reason != "" {
		return ToolResult{Text: "the requester cannot run that member agent right now (" + string(reason) + ")"}, nil
	}
	// 时间线上的可辨识消息：队长作者、@ 成员、client_message_id 与输入使用同一个幂等键。
	var messageID string
	err = tx.QueryRow(ctx, `
		WITH next AS (
		  UPDATE openfox.conversations SET next_message_seq = next_message_seq + 1, version = version + 1, updated_at = clock_timestamp()
		   WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lifecycle = 'OPEN'
		   RETURNING next_message_seq - 1 AS seq)
		INSERT INTO openfox.messages (organization_id, conversation_id, seq, author_kind, author_agent_id, body, client_message_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, seq, 'AGENT', $2::uuid, $3, $4 FROM next
		RETURNING id::text`, run.ConversationID, run.AgentID, instruction, source).Scan(&messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "the conversation is closed"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.message_mentions (organization_id, message_id, position, mentioned_agent_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 0, $2::uuid)`, messageID, memberID); err != nil {
		return ToolResult{}, err
	}
	if err := conversations.InsertDelegatedInput(ctx, tx, run.ConversationID, messageID, body, source, memberID); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{OK: true, Text: fmt.Sprintf(
		"delegated to %s (%d of %d delegations used). Crew runs share the task directory serially, so %s starts after your current run finishes.",
		strings.TrimSpace(memberName), used+1, crewDelegateLimit, strings.TrimSpace(memberName))}, nil
}
