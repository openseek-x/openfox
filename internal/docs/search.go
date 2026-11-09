package docs

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// SearchHit 是全局搜索里的一条文档结果。
type SearchHit struct {
	Total                               int
	ID, Title, Kind, SpaceID, SpaceName string
}

// SearchAcrossSpaces 跨空间按标题搜索文档（不含文件夹）：空间可读（SpaceReadableSQL），
// 范围与 SearchNodes 一致——文档集节点加本人的「我的文档」（他人的我的文档由 RLS 与条件双重排除）。
// pattern 是已转义的 ILIKE 匹配串，role 是调用者的大写组织角色。
func SearchAcrossSpaces(ctx context.Context, tx pgx.Tx, scope collaboration.SearchScope) ([]SearchHit, error) {
	filter := ""
	args := []any{scope.Role, collaboration.SearchPattern(scope.Query), scope.Limit, scope.Offset}
	if len(scope.SpaceIDs) > 0 {
		args = append(args, scope.SpaceIDs)
		filter = ` AND n.space_id = ANY($5::uuid[])`
	}
	rows, err := tx.Query(ctx, `
		SELECT n.id::text, n.title, n.kind, n.space_id::text, s.name, `+scope.CountSQL()+`
		  FROM openfox.doc_nodes n
		  JOIN openfox.spaces s ON s.organization_id = n.organization_id AND s.id = n.space_id
		 WHERE n.organization_id = openfox_private.current_organization_id()
		   AND n.kind <> 'folder'
		   AND (n.set_id IS NOT NULL OR n.owner_user_id = openfox_private.current_actor_id())
		   AND `+collaboration.SpaceReadableSQL+`
		   AND n.title ILIKE $2 ESCAPE '\'`+filter+`
		 ORDER BY n.updated_at DESC, n.id
		 LIMIT $3 OFFSET $4`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.ID, &hit.Title, &hit.Kind, &hit.SpaceID, &hit.SpaceName, &hit.Total); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
