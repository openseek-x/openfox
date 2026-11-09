package agents

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// SearchHit 是全局搜索里的一条 Agent 结果。
type SearchHit struct {
	Total                               int
	ID, Name, SpaceID, SpaceName, Title string
}

// SearchAgents 按名称搜索 Agent，可见性与目录（Directory）完全一致：空间活跃且可读、
// 共享或本人的 Agent、不含内置小助手、不含小队专属 Agent、不含已归档。tx 必须是租户事务。
// role 是调用者的大写组织角色（SpaceReadableSQL 的 $1），pattern 是已转义的 ILIKE 匹配串。
func SearchAgents(ctx context.Context, tx pgx.Tx, scope collaboration.SearchScope) ([]SearchHit, error) {
	filter := ""
	args := []any{scope.Role, collaboration.SearchPattern(scope.Query), scope.Limit, scope.Offset}
	if len(scope.SpaceIDs) > 0 {
		args = append(args, scope.SpaceIDs)
		filter = ` AND a.space_id = ANY($5::uuid[])`
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.name, a.space_id::text, s.name, a.title, `+scope.CountSQL()+`
		  FROM openfox.agents a
		  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.space_id IS NOT NULL
		   AND `+directorySpaceSQL+`
		   AND `+directoryAgentVisibleSQL+` AND `+agentNotExclusiveSQL+`
		   AND a.archived_at IS NULL
		   AND a.name ILIKE $2 ESCAPE '\'`+filter+`
		 ORDER BY a.name, a.id
		 LIMIT $3 OFFSET $4`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.ID, &hit.Name, &hit.SpaceID, &hit.SpaceName, &hit.Title, &hit.Total); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
