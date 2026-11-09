package collaboration

// 专项：组织级、可关联多个空间（恰有一个主空间）。权限由接口层按关联空间判定，这里只做数据规则。

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

var (
	ErrProjectNotFound  = NewRuleError(http.StatusNotFound, "NOT_FOUND", "resource is not available")
	ErrProjectsDisabled = NewRuleError(http.StatusBadRequest, "PROJECTS_DISABLED", "projects are disabled for this space")
	ErrInvalidProject   = NewRuleError(http.StatusBadRequest, "INVALID_PROJECT", "project is not available for this task")
	ErrPrimarySpace     = NewRuleError(http.StatusConflict, "PRIMARY_SPACE", "the primary space cannot be removed; set another space as primary first")
)

const maxProjectLabels = 50

// projectTaskStatsSQL 的口径与迭代一致：已取消 / 重复的任务不计入，完成 = DONE 分类。
const projectTaskStatsSQL = `SELECT t.project_id::text, count(*) FILTER (WHERE ts.category = 'DONE'), count(*)
	  FROM openfox.tasks t
	  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
	 WHERE t.organization_id = openfox_private.current_organization_id()
	   AND t.project_id = ANY($1::uuid[]) AND t.deleted_at IS NULL AND ts.category NOT IN ('CANCELLED', 'DUPLICATE')
	 GROUP BY t.project_id`

var projectStatuses = []string{"backlog", "planned", "in_progress", "completed", "canceled"}

type ProjectSpace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	IsPrimary bool   `json:"is_primary"`
}

type ProjectLead struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type ProjectProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type Project struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Icon         *string              `json:"icon"`
	Description  string               `json:"description"`
	Status       string               `json:"status"`
	Priority     string               `json:"priority"`
	Lead         *ProjectLead         `json:"lead"`
	Progress     ProjectProgress      `json:"progress"`
	Spaces       []ProjectSpace       `json:"spaces"`
	Labels       []TaskLabelItem      `json:"labels"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	Version      uint64               `json:"version"`
	Distribution *ProjectDistribution `json:"distribution,omitempty"`
}

type DistributionItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// ProjectDistribution 是详情概览的进度分布（6.8）：按处理人、按迭代。
type ProjectDistribution struct {
	Assignees  []DistributionItem `json:"assignees"`
	Iterations []DistributionItem `json:"iterations"`
}

type ProjectInput struct {
	SpaceID     string
	Name        string
	Icon        string
	Description string
	Status      string
	Priority    string
	LeadUserID  string
}

type ProjectPatch struct {
	Version     uint64
	Name        *string
	Icon        OptionalString
	Description *string
	Status      *string
	Priority    *string
	LeadUserID  OptionalString
}

// ProjectSpaceRef 是权限判定用的关联空间（不含已删除空间）。
type ProjectSpaceRef struct {
	SpaceID   string
	IsPrimary bool
}

// ProjectSpaceRefs 返回专项的关联空间；专项不存在返回 ErrProjectNotFound。
func (s Service) ProjectSpaceRefs(ctx context.Context, tx pgx.Tx, projectID string) ([]ProjectSpaceRef, error) {
	if !membership.ValidateUUID(projectID) {
		return nil, ErrProjectNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(s.id::text, ''), COALESCE(ps.is_primary, false)
		  FROM openfox.projects p
		  LEFT JOIN openfox.project_spaces ps ON ps.organization_id = p.organization_id AND ps.project_id = p.id
		  LEFT JOIN openfox.spaces s ON s.organization_id = ps.organization_id AND s.id = ps.space_id AND s.deleted_at IS NULL
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.id = $1::uuid`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := false
	var refs []ProjectSpaceRef
	for rows.Next() {
		var ref ProjectSpaceRef
		if err := rows.Scan(&ref.SpaceID, &ref.IsPrimary); err != nil {
			return nil, err
		}
		found = true
		if ref.SpaceID != "" {
			refs = append(refs, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrProjectNotFound
	}
	return refs, nil
}

// validateTaskProject 校验任务可以归属该专项：空间开启专项且专项关联了该空间。
func validateTaskProject(ctx context.Context, tx pgx.Tx, spaceID, projectID string) error {
	var enabled, linked bool
	err := tx.QueryRow(ctx, `
		SELECT sp.projects_enabled, EXISTS (
		         SELECT 1 FROM openfox.project_spaces ps
		          WHERE ps.organization_id = sp.organization_id AND ps.space_id = sp.id AND ps.project_id = $2::uuid)
		  FROM openfox.spaces sp
		 WHERE sp.organization_id = openfox_private.current_organization_id() AND sp.id = $1::uuid AND sp.deleted_at IS NULL`,
		spaceID, projectID).Scan(&enabled, &linked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrProjectsDisabled
	}
	if !linked {
		return ErrInvalidProject
	}
	return nil
}

func requireProjectsEnabled(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT projects_enabled FROM openfox.spaces
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrProjectsDisabled
	}
	return nil
}

// lockProject 锁住专项行，串行化同一专项上的关联空间 / 主空间 / 删除操作，返回当前版本。
func lockProject(ctx context.Context, tx pgx.Tx, projectID string) (uint64, error) {
	var version int64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.projects
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, projectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrProjectNotFound
	}
	if err != nil {
		return 0, err
	}
	return positiveVersion(version)
}

// touchProject 在关联空间 / 标签变化后递增版本，让组织事件通知客户端刷新。
func touchProject(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.projects SET version = version + 1, updated_at = clock_timestamp()
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, projectID)
	return err
}

func (s Service) CreateProject(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input ProjectInput) (Project, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Project{}, err
	}
	name, err := normalizeConfigName(input.Name, 80)
	if err != nil {
		return Project{}, err
	}
	status, priority := input.Status, input.Priority
	if status == "" {
		status = "planned"
	} else if !slices.Contains(projectStatuses, status) {
		return Project{}, invalidInput("status is invalid")
	}
	if priority == "" {
		priority = "NONE"
	} else if priority, err = ParsePriority(priority); err != nil {
		return Project{}, err
	}
	if err := requireProjectsEnabled(ctx, tx, input.SpaceID); err != nil {
		return Project{}, err
	}
	id, err := NewID()
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.projects (organization_id, id, name, icon, description, status, priority, lead_user_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, NULLIF($3, ''), $4, $5, $6, NULLIF($7, '')::uuid, $8::uuid)`,
		id, name, input.Icon, input.Description, status, priority, input.LeadUserID, actor.UserID); err != nil {
		return Project{}, mapDatabaseError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.project_spaces (organization_id, project_id, space_id, is_primary)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, true)`, id, input.SpaceID); err != nil {
		return Project{}, mapDatabaseError(err)
	}
	return s.GetProject(ctx, tx, organizationID, actor, id)
}

func (s Service) UpdateProject(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, projectID string, patch ProjectPatch) (Project, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Project{}, err
	}
	if patch.Version == 0 {
		return Project{}, ErrInvalidVersion
	}
	if patch.Name != nil {
		name, nameErr := normalizeConfigName(*patch.Name, 80)
		if nameErr != nil {
			return Project{}, nameErr
		}
		patch.Name = &name
	}
	if patch.Status != nil && !slices.Contains(projectStatuses, *patch.Status) {
		return Project{}, invalidInput("status is invalid")
	}
	if patch.Priority != nil {
		priority, priorityErr := ParsePriority(*patch.Priority)
		if priorityErr != nil {
			return Project{}, priorityErr
		}
		patch.Priority = &priority
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.projects
		   SET name = COALESCE($3, name),
		       icon = CASE WHEN $4 THEN NULLIF($5, '') ELSE icon END,
		       description = COALESCE($6, description),
		       status = COALESCE($7, status),
		       priority = COALESCE($8, priority),
		       lead_user_id = CASE WHEN $9 THEN NULLIF($10, '')::uuid ELSE lead_user_id END,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
		projectID, int64(patch.Version), patch.Name, patch.Icon.Set, optionalText(patch.Icon), patch.Description,
		patch.Status, patch.Priority, patch.LeadUserID.Set, optionalText(patch.LeadUserID))
	if err != nil {
		return Project{}, mapDatabaseError(err)
	}
	if result.RowsAffected() == 0 {
		if _, err := lockProject(ctx, tx, projectID); err != nil {
			return Project{}, err
		}
		return Project{}, ErrVersionConflict
	}
	return s.GetProject(ctx, tx, organizationID, actor, projectID)
}

// GetProject 返回专项详情（含进度分布）。
func (s Service) GetProject(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, projectID string) (Project, error) {
	if !membership.ValidateUUID(projectID) {
		return Project{}, ErrProjectNotFound
	}
	items, err := s.projectsByID(ctx, tx, actor, []string{projectID})
	if err != nil {
		return Project{}, err
	}
	if len(items) == 0 {
		return Project{}, ErrProjectNotFound
	}
	distribution, err := projectDistribution(ctx, tx, projectID)
	if err != nil {
		return Project{}, err
	}
	items[0].Distribution = &distribution
	return items[0], nil
}

type ProjectListOptions struct {
	SpaceID string
	Status  string
	Limit   int
	Cursor  string
}

// ListProjects 列出关联到该空间的专项，按创建时间倒序（keyset 分页）。
func (s Service) ListProjects(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, options ProjectListOptions) (Page[Project], error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Page[Project]{}, err
	}
	if options.Limit < 1 || options.Limit > MaxPageSize {
		return Page[Project]{}, ErrInvalidPageSize
	}
	if options.Status != "" && !slices.Contains(projectStatuses, options.Status) {
		return Page[Project]{}, invalidInput("status is invalid")
	}
	q := &taskQuery{}
	conds := []string{
		"p.organization_id = openfox_private.current_organization_id()",
		"EXISTS (SELECT 1 FROM openfox.project_spaces ps WHERE ps.organization_id = p.organization_id AND ps.project_id = p.id AND ps.space_id = " + q.arg(options.SpaceID) + "::uuid)",
	}
	if options.Status != "" {
		conds = append(conds, "p.status = "+q.arg(options.Status))
	}
	if options.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(options.Cursor)
		at, id, found := strings.Cut(string(raw), "|")
		createdAt, parseErr := time.Parse(time.RFC3339Nano, at)
		if err != nil || !found || parseErr != nil || !membership.ValidateUUID(id) {
			return Page[Project]{}, ErrInvalidCursor
		}
		conds = append(conds, "(p.created_at, p.id) < ("+q.arg(createdAt)+"::timestamptz, "+q.arg(id)+"::uuid)")
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text FROM openfox.projects p WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY p.created_at DESC, p.id DESC LIMIT `+q.arg(options.Limit+1), q.args...)
	if err != nil {
		return Page[Project]{}, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Page[Project]{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Page[Project]{}, err
	}
	more := len(ids) > options.Limit
	if more {
		ids = ids[:options.Limit]
	}
	page := Page[Project]{Items: []Project{}}
	if len(ids) == 0 {
		return page, nil
	}
	if page.Items, err = s.projectsByID(ctx, tx, actor, ids); err != nil {
		return Page[Project]{}, err
	}
	if more {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	return page, nil
}

// projectsByID 批量读取专项及其负责人、可读的关联空间、标签与进度（每类一次查询），保持 ids 的顺序。
func (s Service) projectsByID(ctx context.Context, tx pgx.Tx, actor Actor, ids []string) ([]Project, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id::text, p.name, p.icon, p.description, p.status, p.priority,
		       COALESCE(p.lead_user_id::text, ''), COALESCE(u.email, ''), p.created_at, p.updated_at, p.version
		  FROM openfox.projects p
		  LEFT JOIN openfox.users u ON u.id = p.lead_user_id
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]*Project, len(ids))
	for rows.Next() {
		var item Project
		var leadID, leadEmail string
		var version int64
		if err := rows.Scan(&item.ID, &item.Name, &item.Icon, &item.Description, &item.Status, &item.Priority,
			&leadID, &leadEmail, &item.CreatedAt, &item.UpdatedAt, &version); err != nil {
			return nil, err
		}
		if item.Version, err = positiveVersion(version); err != nil {
			return nil, err
		}
		if leadID != "" {
			item.Lead = &ProjectLead{ID: leadID, Email: leadEmail}
		}
		item.Spaces, item.Labels = []ProjectSpace{}, []TaskLabelItem{}
		byID[item.ID] = &item
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 只返回当前用户能读的关联空间，避免泄露私有空间的名称。
	rows, err = tx.Query(ctx, `
		SELECT ps.project_id::text, s.id::text, s.name, s.prefix, ps.is_primary
		  FROM openfox.project_spaces ps
		  JOIN openfox.spaces s ON s.organization_id = ps.organization_id AND s.id = ps.space_id
		 WHERE ps.organization_id = openfox_private.current_organization_id() AND ps.project_id = ANY($2::uuid[])
		   AND `+SpaceReadableSQL+`
		 ORDER BY ps.is_primary DESC, s.name, s.id`, strings.ToUpper(string(actor.Role)), ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var projectID string
		var space ProjectSpace
		if err := rows.Scan(&projectID, &space.ID, &space.Name, &space.Prefix, &space.IsPrimary); err != nil {
			rows.Close()
			return nil, err
		}
		byID[projectID].Spaces = append(byID[projectID].Spaces, space)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT pl.project_id::text, l.id::text, l.name, l.color, l.group_id::text, l.space_id::text
		  FROM openfox.project_labels pl
		  JOIN openfox.labels l ON l.organization_id = pl.organization_id AND l.id = pl.label_id
		 WHERE pl.organization_id = openfox_private.current_organization_id() AND pl.project_id = ANY($1::uuid[])
		 ORDER BY l.name, l.id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var projectID string
		var label TaskLabelItem
		if err := rows.Scan(&projectID, &label.ID, &label.Name, &label.Color, &label.GroupID, &label.SpaceID); err != nil {
			rows.Close()
			return nil, err
		}
		byID[projectID].Labels = append(byID[projectID].Labels, label)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, projectTaskStatsSQL, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var projectID string
		var progress ProjectProgress
		if err := rows.Scan(&projectID, &progress.Done, &progress.Total); err != nil {
			rows.Close()
			return nil, err
		}
		byID[projectID].Progress = progress
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	items := make([]Project, 0, len(ids))
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			items = append(items, *item)
		}
	}
	return items, nil
}

// projectDistribution 用一次聚合查询取出按处理人、按迭代的分布。
// 处理人优先看 task_assignees，没有行的旧任务看投影列（与任务筛选一致）；没有处理人 / 迭代的任务 key 为空串。
func projectDistribution(ctx context.Context, tx pgx.Tx, projectID string) (ProjectDistribution, error) {
	rows, err := tx.Query(ctx, `
		WITH pt AS (
			SELECT t.organization_id, t.id, t.iteration_id, t.assignee_id, t.executor_agent_id, ts.category = 'DONE' AS done,
			       EXISTS (SELECT 1 FROM openfox.task_assignees x WHERE x.organization_id = t.organization_id AND x.task_id = t.id) AS has_rows
			  FROM openfox.tasks t
			  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
			 WHERE t.organization_id = openfox_private.current_organization_id()
			   AND t.project_id = $1::uuid AND t.deleted_at IS NULL AND ts.category NOT IN ('CANCELLED', 'DUPLICATE')
		), grouped AS (
			SELECT 'assignee' AS dim, COALESCE(a.user_id, a.agent_id, a.crew_id)::text AS key,
			       COALESCE(u.email, ag.name, cr.name, '') AS label, NULL::int AS number, pt.done
			  FROM pt
			  JOIN openfox.task_assignees a ON a.organization_id = pt.organization_id AND a.task_id = pt.id
			  LEFT JOIN openfox.users u ON u.id = a.user_id
			  LEFT JOIN openfox.agents ag ON ag.organization_id = a.organization_id AND ag.id = a.agent_id
			  LEFT JOIN openfox.agent_crews cr ON cr.organization_id = a.organization_id AND cr.id = a.crew_id
			UNION ALL
			SELECT 'assignee', COALESCE(pt.assignee_id, pt.executor_agent_id)::text, COALESCE(u.email, ag.name, ''), NULL, pt.done
			  FROM pt
			  LEFT JOIN openfox.users u ON u.id = pt.assignee_id
			  LEFT JOIN openfox.agents ag ON ag.organization_id = pt.organization_id AND ag.id = pt.executor_agent_id
			 WHERE NOT pt.has_rows
			UNION ALL
			SELECT 'iteration', COALESCE(i.id::text, ''), COALESCE(i.name, ''), i.number, pt.done
			  FROM pt
			  LEFT JOIN openfox.iterations i ON i.organization_id = pt.organization_id AND i.id = pt.iteration_id
		)
		SELECT dim, COALESCE(key, ''), label, number, count(*) FILTER (WHERE done)::int, count(*)::int
		  FROM grouped GROUP BY dim, key, label, number
		 ORDER BY dim, count(*) DESC, label, key`, projectID)
	if err != nil {
		return ProjectDistribution{}, err
	}
	defer rows.Close()
	result := ProjectDistribution{Assignees: []DistributionItem{}, Iterations: []DistributionItem{}}
	for rows.Next() {
		var dim string
		var number *int
		var item DistributionItem
		if err := rows.Scan(&dim, &item.Key, &item.Label, &number, &item.Done, &item.Total); err != nil {
			return ProjectDistribution{}, err
		}
		if dim == "iteration" {
			if number != nil {
				item.Label = iterationDisplayName(item.Label, *number)
			}
			result.Iterations = append(result.Iterations, item)
		} else {
			result.Assignees = append(result.Assignees, item)
		}
	}
	return result, rows.Err()
}

// clearProjectTasks 清空专项下任务的 project_id（spaceID 非空时只处理该空间），并给每个未删除的任务写 task.project_changed 动态。
// 已软删除的任务也要清空，否则外键会挡住删除专项。
func clearProjectTasks(ctx context.Context, tx pgx.Tx, projectID, spaceID, actorID, source string) error {
	_, err := tx.Exec(ctx, `
		WITH cleared AS (
			UPDATE openfox.tasks SET project_id = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid
			   AND ($2 = '' OR space_id = NULLIF($2, '')::uuid)
			RETURNING id, space_id, deleted_at
		)
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), c.space_id, c.id, gen_random_uuid(), $3::uuid, 'task.project_changed',
		       jsonb_build_object('from_project_id', $1::uuid, 'to_project_id', NULL), $4 || ':' || c.id::text
		  FROM cleared c WHERE c.deleted_at IS NULL
		ON CONFLICT (organization_id, source_id) DO NOTHING`, projectID, spaceID, actorID, source)
	return mapDatabaseError(err)
}

func (s Service) ProjectDeleteImpact(ctx context.Context, tx pgx.Tx, projectID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*)::int FROM openfox.tasks
		WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND deleted_at IS NULL`, projectID).Scan(&count)
	return count, err
}

// DeleteProject 硬删除：同一事务内清空任务的 project_id 再删除专项（级联关联空间与标签）。
func (s Service) DeleteProject(ctx context.Context, tx pgx.Tx, projectID string, version uint64, actor Actor, requestID string) error {
	current, err := lockProject(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if current != version {
		return ErrVersionConflict
	}
	if err := clearProjectTasks(ctx, tx, projectID, "", actor.UserID, requestID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM openfox.projects WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, projectID)
	return mapDatabaseError(err)
}

func (s Service) ListProjectSpaces(ctx context.Context, tx pgx.Tx, actor Actor, projectID string) ([]ProjectSpace, error) {
	items, err := s.projectsByID(ctx, tx, actor, []string{projectID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrProjectNotFound
	}
	return items[0].Spaces, nil
}

func (s Service) AddProjectSpace(ctx context.Context, tx pgx.Tx, projectID, spaceID string) error {
	if !membership.ValidateUUID(spaceID) {
		return ErrSpaceNotFound
	}
	if err := requireProjectsEnabled(ctx, tx, spaceID); err != nil {
		return err
	}
	if _, err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.project_spaces (organization_id, project_id, space_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`, projectID, spaceID); err != nil {
		return mapDatabaseError(err)
	}
	return touchProject(ctx, tx, projectID)
}

// RemoveProjectSpace 移除关联空间并清空该空间里属于此专项的任务；主空间（含最后一个空间）不能移除。
func (s Service) RemoveProjectSpace(ctx context.Context, tx pgx.Tx, projectID, spaceID string, actor Actor, requestID string) error {
	if !membership.ValidateUUID(spaceID) {
		return ErrProjectNotFound
	}
	if _, err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}
	var primary bool
	err := tx.QueryRow(ctx, `
		SELECT is_primary FROM openfox.project_spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND space_id = $2::uuid`,
		projectID, spaceID).Scan(&primary)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProjectNotFound
	}
	if err != nil {
		return err
	}
	if primary {
		return ErrPrimarySpace
	}
	if err := clearProjectTasks(ctx, tx, projectID, spaceID, actor.UserID, requestID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.project_spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND space_id = $2::uuid`,
		projectID, spaceID); err != nil {
		return err
	}
	return touchProject(ctx, tx, projectID)
}

func (s Service) SetProjectPrimarySpace(ctx context.Context, tx pgx.Tx, projectID, spaceID string) error {
	if !membership.ValidateUUID(spaceID) {
		return ErrProjectNotFound
	}
	if _, err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}
	var primary bool
	err := tx.QueryRow(ctx, `SELECT is_primary FROM openfox.project_spaces
		WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND space_id = $2::uuid`,
		projectID, spaceID).Scan(&primary)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProjectNotFound
	}
	if err != nil || primary {
		return err
	}
	// 先取消旧主空间再设置新的：部分唯一索引按行即时检查，不能放在一条语句里。
	if _, err := tx.Exec(ctx, `UPDATE openfox.project_spaces SET is_primary = false
		WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND is_primary`, projectID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.project_spaces SET is_primary = true
		WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND space_id = $2::uuid`, projectID, spaceID); err != nil {
		return err
	}
	return touchProject(ctx, tx, projectID)
}

// SetProjectLabels 整体替换专项标签：标签必须未归档，且是组织级标签或属于已关联的空间。
func (s Service) SetProjectLabels(ctx context.Context, tx pgx.Tx, projectID string, labelIDs []string) error {
	if len(labelIDs) > maxProjectLabels {
		return invalidInput("label_ids supports at most 50 labels")
	}
	ids := make([]string, 0, len(labelIDs))
	for _, raw := range labelIDs {
		id := strings.ToLower(strings.TrimSpace(raw))
		if !membership.ValidateUUID(id) {
			return invalidInput("label_ids contains an invalid id")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if _, err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}
	var usable int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM openfox.labels l
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.id = ANY($2::uuid[]) AND l.archived_at IS NULL
		   AND (l.space_id IS NULL OR EXISTS (SELECT 1 FROM openfox.project_spaces ps
		         WHERE ps.organization_id = l.organization_id AND ps.project_id = $1::uuid AND ps.space_id = l.space_id))`,
		projectID, ids).Scan(&usable); err != nil {
		return err
	}
	if usable != len(ids) {
		return invalidInput("label_ids contains a label that is not available")
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.project_labels
		 WHERE organization_id = openfox_private.current_organization_id() AND project_id = $1::uuid AND NOT (label_id = ANY($2::uuid[]))`,
		projectID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.project_labels (organization_id, project_id, label_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, unnest($2::uuid[])
		ON CONFLICT DO NOTHING`, projectID, ids); err != nil {
		return err
	}
	return touchProject(ctx, tx, projectID)
}
