package collaboration

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 全局搜索（命令面板）。每一类结果各是一条有界查询（LIMIT），可见性复用现有规则：
// 空间可读用 SpaceReadableSQL（与 policy.authorizeSpaceRead 对齐），私人视图靠 RLS + 创建者条件，
// 不另写更宽的规则。匹配只做标题 / 名称的 ILIKE（% _ \ 已转义）与编号精确匹配，不做全文检索。

// SearchHit 是一条搜索结果；各类字段含义见 contracts/openapi.yaml 的 SearchItem。
type SearchHit struct {
	ID                                                         string
	Title                                                      string
	Subtitle                                                   string
	SpaceID                                                    string
	DisplayID                                                  string
	ConversationID, ConversationKind, TaskDisplayID, MessageID string
	MessageSeq                                                 int64
	OccurredAt                                                 *time.Time
	SenderID, SenderName, SenderKind                           string
	Total                                                      int
}

// SearchScope 是各类搜索共用的参数。Role 是调用者的组织角色（大写），用作 SpaceReadableSQL 的 $1。
type SearchScope struct {
	Query         string   // 已去除首尾空白，长度已由调用方校验
	SpaceIDs      []string // Optional readable-space restriction.
	Limit, Offset int
	WithTotal     bool
	TaskFilter    SearchTaskFilter
	Role          string
}

// SearchPattern 返回 ILIKE 的匹配串（包含匹配，通配符已转义）。
func SearchPattern(query string) string {
	return "%" + escapeLike(query) + "%"
}

func collectHits(rows pgx.Rows, scan func(pgx.Rows, *SearchHit) error) ([]SearchHit, error) {
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var hit SearchHit
		if err := scan(rows, &hit); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// CountSQL avoids counting every match for legacy, bounded autocomplete requests.
func (s SearchScope) CountSQL() string {
	if s.WithTotal {
		return "count(*) OVER ()"
	}
	return "0"
}

func spaceFilter(q *taskQuery, column string, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return " AND " + column + " = ANY(" + q.arg(ids) + "::uuid[])"
}

// SearchTasks 按标题或编号搜索任务：编号精确匹配（含空间历史标识符）排最前，其次按最近更新。
func (Service) SearchTasks(ctx context.Context, tx pgx.Tx, scope SearchScope) ([]SearchHit, error) {
	q := &taskQuery{}
	q.arg(scope.Role) // $1：SpaceReadableSQL 的组织角色
	pattern := q.arg(SearchPattern(scope.Query))
	exact := "false::boolean"
	match := "t.title ILIKE " + pattern + ` ESCAPE '\'`
	if prefix, sequence, ok := splitDisplayID(scope.Query); ok {
		pp, ps := q.arg(prefix), q.arg(sequence)
		// 精确编号：当前前缀或历史前缀；也匹配移动前的旧编号（别名表，源空间的当前或历史前缀）。
		exact = `((t.sequence = ` + ps + `::bigint AND (lower(s.prefix) = lower(` + pp + `) OR EXISTS (
			SELECT 1 FROM openfox.space_prefix_history h
			 WHERE h.organization_id = s.organization_id AND h.space_id = s.id AND lower(h.prefix) = lower(` + pp + `))))
			OR EXISTS (
			SELECT 1 FROM openfox.task_display_aliases a
			  JOIN openfox.spaces os ON os.organization_id = a.organization_id AND os.id = a.space_id
			 WHERE a.organization_id = t.organization_id AND a.task_id = t.id AND a.sequence = ` + ps + `::bigint
			   AND (lower(os.prefix) = lower(` + pp + `) OR EXISTS (
			        SELECT 1 FROM openfox.space_prefix_history oh
			         WHERE oh.organization_id = os.organization_id AND oh.space_id = os.id AND lower(oh.prefix) = lower(` + pp + `)))))`
		match = "(" + match + " OR " + exact + ")"
	}
	where := spaceFilter(q, "t.space_id", scope.SpaceIDs)
	for _, condition := range scope.TaskFilter.conditions(q) {
		where += " AND " + condition
	}
	page := " LIMIT " + q.arg(scope.Limit) + " OFFSET " + q.arg(scope.Offset)
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.title, t.space_id::text, s.prefix, t.sequence, s.name, `+scope.CountSQL()+`
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		  JOIN openfox.task_statuses ts ON ts.organization_id=t.organization_id AND ts.id=t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.deleted_at IS NULL AND `+SpaceReadableSQL+` AND `+match+where+`
		 ORDER BY `+exact+` DESC, t.updated_at DESC, t.id
		 `+page, q.args...)
	if err != nil {
		return nil, err
	}
	return collectHits(rows, func(row pgx.Rows, hit *SearchHit) error {
		var prefix string
		var sequence int64
		if err := row.Scan(&hit.ID, &hit.Title, &hit.SpaceID, &prefix, &sequence, &hit.Subtitle, &hit.Total); err != nil {
			return err
		}
		hit.DisplayID = displayID(prefix, sequence)
		return nil
	})
}

// SearchProjects 按名称搜索专项：专项在组织内，可读 = 至少一个关联空间可读（与 authorizeProject 一致）。
// 返回的 SpaceID 是一个可读的关联空间（主空间优先），前端据此拼跳转地址。
func (Service) SearchProjects(ctx context.Context, tx pgx.Tx, scope SearchScope) ([]SearchHit, error) {
	q := &taskQuery{}
	q.arg(scope.Role)
	pattern := q.arg(SearchPattern(scope.Query))
	where := spaceFilter(q, "ps.space_id", scope.SpaceIDs)
	page := " LIMIT " + q.arg(scope.Limit) + " OFFSET " + q.arg(scope.Offset)
	rows, err := tx.Query(ctx, `
		SELECT p.id::text, p.name, rs.id, rs.name, `+scope.CountSQL()+`
		  FROM openfox.projects p
		  JOIN LATERAL (
		        SELECT s.id::text AS id, s.name
		          FROM openfox.project_spaces ps
		          JOIN openfox.spaces s ON s.organization_id = ps.organization_id AND s.id = ps.space_id
		         WHERE ps.organization_id = p.organization_id AND ps.project_id = p.id
		           AND s.projects_enabled AND `+SpaceReadableSQL+where+`
		         ORDER BY ps.is_primary DESC, s.name, s.id
		         LIMIT 1) rs ON true
		 WHERE p.organization_id = openfox_private.current_organization_id()
		   AND p.name ILIKE `+pattern+` ESCAPE '\'
		 ORDER BY p.updated_at DESC, p.id
		 `+page, q.args...)
	if err != nil {
		return nil, err
	}
	return collectHits(rows, func(row pgx.Rows, hit *SearchHit) error {
		return row.Scan(&hit.ID, &hit.Title, &hit.SpaceID, &hit.Subtitle, &hit.Total)
	})
}

// SearchViews 按名称搜索视图：空间可读且启用视图；他人的私人视图由 RLS 与创建者条件双重排除。
func (Service) SearchViews(ctx context.Context, tx pgx.Tx, scope SearchScope) ([]SearchHit, error) {
	q := &taskQuery{}
	q.arg(scope.Role)
	pattern := q.arg(SearchPattern(scope.Query))
	where := spaceFilter(q, "v.space_id", scope.SpaceIDs)
	page := " LIMIT " + q.arg(scope.Limit) + " OFFSET " + q.arg(scope.Offset)
	rows, err := tx.Query(ctx, `
		SELECT v.id::text, v.name, v.space_id::text, s.name, `+scope.CountSQL()+`
		  FROM openfox.views v
		  JOIN openfox.spaces s ON s.organization_id = v.organization_id AND s.id = v.space_id
		 WHERE v.organization_id = openfox_private.current_organization_id()
		   AND (v.visibility = 'space' OR v.created_by = openfox_private.current_actor_id())
		   AND s.views_enabled AND `+SpaceReadableSQL+`
		   AND v.name ILIKE `+pattern+` ESCAPE '\'`+where+`
		 ORDER BY v.updated_at DESC, v.id
		 `+page, q.args...)
	if err != nil {
		return nil, err
	}
	return collectHits(rows, func(row pgx.Rows, hit *SearchHit) error {
		return row.Scan(&hit.ID, &hit.Title, &hit.SpaceID, &hit.Subtitle, &hit.Total)
	})
}

// SearchSpaces 按名称或标识符搜索空间：只含活跃且可读的空间，标识符精确匹配排最前。
func (Service) SearchSpaces(ctx context.Context, tx pgx.Tx, scope SearchScope) ([]SearchHit, error) {
	q := &taskQuery{}
	q.arg(scope.Role)
	pattern := q.arg(SearchPattern(scope.Query))
	exactArg := q.arg(scope.Query)
	where := spaceFilter(q, "s.id", scope.SpaceIDs)
	page := " LIMIT " + q.arg(scope.Limit) + " OFFSET " + q.arg(scope.Offset)
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.name, s.prefix, `+scope.CountSQL()+`
		  FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id()
		   AND s.status = 'ACTIVE' AND `+SpaceReadableSQL+`
		   AND (s.name ILIKE `+pattern+` ESCAPE '\' OR s.prefix ILIKE `+pattern+` ESCAPE '\')`+where+`
		 ORDER BY (lower(s.prefix) = lower(`+exactArg+`)) DESC, s.name, s.id
		 `+page, q.args...)
	if err != nil {
		return nil, err
	}
	return collectHits(rows, func(row pgx.Rows, hit *SearchHit) error {
		if err := row.Scan(&hit.ID, &hit.Title, &hit.Subtitle, &hit.Total); err != nil {
			return err
		}
		hit.SpaceID = hit.ID
		return nil
	})
}

// SearchPeople 按姓名或邮箱搜索组织内的活跃成员。调用方必须先确认调用者有读组织成员的权限
// （与 GET /members 同一判断）。有空间筛选时只含该空间的活跃成员，且该空间必须对调用者可读。
func (Service) SearchPeople(ctx context.Context, tx pgx.Tx, scope SearchScope) ([]SearchHit, error) {
	q := &taskQuery{}
	q.arg(scope.Role)
	pattern := q.arg(SearchPattern(scope.Query))
	exactArg := q.arg(scope.Query)
	// 没有空间筛选时 $1 不被引用，用类型转换让 PostgreSQL 能推断出参数类型。
	where := " AND $1::text IS NOT NULL"
	if len(scope.SpaceIDs) > 0 {
		where = ` AND EXISTS (
		       SELECT 1 FROM openfox.space_members sm
		       JOIN openfox.spaces s ON s.organization_id = sm.organization_id AND s.id = sm.space_id
		        WHERE sm.organization_id = m.organization_id AND sm.user_id = m.user_id
		          AND sm.status = 'ACTIVE' AND sm.space_id = ANY(` + q.arg(scope.SpaceIDs) + `::uuid[]) AND ` + SpaceReadableSQL + `)`
	}
	page := " LIMIT " + q.arg(scope.Limit) + " OFFSET " + q.arg(scope.Offset)
	rows, err := tx.Query(ctx, `
		SELECT m.user_id::text, COALESCE(NULLIF(u.full_name, ''), u.email), u.email, `+scope.CountSQL()+`
		  FROM openfox.organization_members m
		  JOIN openfox.users u ON u.id = m.user_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.status = 'ACTIVE'
		   AND (u.email ILIKE `+pattern+` ESCAPE '\' OR u.full_name ILIKE `+pattern+` ESCAPE '\')`+where+`
		 ORDER BY (lower(u.email) = lower(`+exactArg+`)) DESC, COALESCE(NULLIF(u.full_name, ''), u.email), m.user_id
		 `+page, q.args...)
	if err != nil {
		return nil, err
	}
	return collectHits(rows, func(row pgx.Rows, hit *SearchHit) error {
		return row.Scan(&hit.ID, &hit.Title, &hit.Subtitle, &hit.Total)
	})
}

// SearchQueryValid 校验 q：去除首尾空白后 1–100 个字符。返回规整后的查询。
func SearchQueryValid(raw string) (string, bool) {
	query := strings.TrimSpace(raw)
	n := len([]rune(query))
	return query, n >= 1 && n <= 100
}
