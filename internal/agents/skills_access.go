package agents

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// 技能装配复用 Agent 现有的权限判断，这里只导出两个入口。

// LockEditableAgent 锁定 Agent 并按「能编辑 Agent」的规则校验（见 lockEditableAgent），已归档返回 ErrArchived。
func LockEditableAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID string, actor Actor) (Agent, error) {
	return lockEditableAgent(ctx, tx, organizationID, spaceID, agentID, actor, false)
}

// RequireReadableAgent 校验空间可读且 Agent 对当前成员可见（共享，或自己的私有 Agent）。
func RequireReadableAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID string, actor Actor) (Agent, error) {
	return requireReadableAgent(ctx, tx, organizationID, spaceID, agentID, actor)
}
