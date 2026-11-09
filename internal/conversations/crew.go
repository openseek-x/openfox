package conversations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 小队对话。分派规则：
//   - 消息 @ 到的小队成员 Agent 各自处理（每个 @ 一条输入，目标 Agent 记在 conversation_inputs.target_agent_id）；
//   - 没有 @ 任何 Agent 且没有 @ 任何成员时，由小队队长处理；
//   - 只 @ 了成员（没有 @ Agent）的消息是成员之间的交流，不触发执行。
//
// 队长 = 小队指定的队长（需仍是可用成员），否则 position 最小的可用成员。
// 「可用成员」= 小队成员中状态 ACTIVE、未归档、在会话所在空间，并且对发消息的人可见（共享，或自己的私有 Agent）。

// crewAgents 返回小队对发起人可用的成员 Agent（按 position 排序）与队长。
func crewAgents(ctx context.Context, tx pgx.Tx, crewID, spaceID, userID string) ([]string, string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id::text
		  FROM openfox.agent_crew_members m
		  JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.agent_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.crew_id = $1::uuid
		   AND a.space_id = $2::uuid AND a.status = 'ACTIVE' AND a.archived_at IS NULL
		   AND (a.visibility = 'SHARED' OR a.owner_user_id = $3::uuid)
		 ORDER BY m.position, a.id`, crewID, spaceID, userID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(ids) == 0 {
		return nil, "", nil
	}
	var explicit string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(leader_agent_id::text, '') FROM openfox.agent_crews
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, crewID).Scan(&explicit); err != nil {
		return nil, "", err
	}
	leader := ids[0]
	for _, id := range ids {
		if id == explicit {
			leader = id
			break
		}
	}
	return ids, leader, nil
}

// resolveCrewForCreate 校验小队可用并返回队长：小队须在该空间、未归档、对操作人可见，且至少有一个可用成员。
// 起小队会话可以挂任务（快捷指令发给小队）：task_id 由 Create 后续与 Agent 会话同一段逻辑校验
// （格式、任务存在未删除、在同一空间；任务可读随空间可读）。
func (s Service) resolveCrewForCreate(ctx context.Context, tx pgx.Tx, organizationID string, input CreateInput, actor Actor) (string, error) {
	if !membership.ValidateUUID(input.CrewID) || !membership.ValidateUUID(input.SpaceID) {
		return "", ErrInvalidRequest
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, input.SpaceID, input.ID, policy.ActionCreateObject); err != nil {
		return "", err
	}
	var spaceID, visibility, owner string
	var archived bool
	if err := tx.QueryRow(ctx, `
		SELECT space_id::text, visibility, owner_user_id::text, archived_at IS NOT NULL
		  FROM openfox.agent_crews
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, input.CrewID).
		Scan(&spaceID, &visibility, &owner, &archived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if archived || (visibility == "PRIVATE" && owner != actor.UserID) {
		return "", ErrNotFound
	}
	if spaceID != input.SpaceID {
		return "", ErrInvalidRequest
	}
	_, leader, err := crewAgents(ctx, tx, input.CrewID, input.SpaceID, actor.UserID)
	if err != nil {
		return "", err
	}
	if leader == "" {
		// 小队里没有对操作人可用的 Agent，无法接收消息。
		return "", ErrForbidden
	}
	if input.AgentID != "" && input.AgentID != leader {
		return "", ErrInvalidRequest
	}
	return leader, nil
}

// crewTargets 计算小队对话一条消息要交给哪些 Agent 执行（规则见文件头）。
func crewTargets(ctx context.Context, tx pgx.Tx, conversation Conversation, actorID string, mentions []Mention) ([]string, error) {
	var targets []string
	hasUser := false
	for _, mention := range mentions {
		if mention.AgentID != "" {
			targets = append(targets, mention.AgentID)
		} else {
			hasUser = true
		}
	}
	if len(targets) > 0 || hasUser {
		return targets, nil
	}
	_, leader, err := crewAgents(ctx, tx, conversation.CrewID, conversation.SpaceID, actorID)
	if err != nil {
		return nil, err
	}
	if leader == "" {
		return nil, ErrForbidden
	}
	return []string{leader}, nil
}
