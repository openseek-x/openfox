package spacebundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 这个文件从「源组织」读取一个空间。tx 必须是已设置源组织租户上下文的事务（发起人身份）；
// 调用方负责权限判断（组织所有者 / 管理员且能管理该空间），这里只做读取。

var ErrSpaceNotFound = errors.New("spacebundle: space not found")

// Org 是源组织的标识，写入包头。
type Org struct{ ID, Name string }

// Limits 是超出上限时的哪一项。
type Limits struct {
	Tasks, Comments, Activities bool
}

// Summary 是预览用的计数（只做 COUNT，不加载内容）。
type Summary struct {
	SpaceName         string
	SpacePrefix       string
	UploadedSpaceIcon bool
	Tasks             int
	Comments          int
	Activities        int
	Projects          int
	TaskTypes         int
	Statuses          int
	Labels            int
	Fields            int
	Agents            int // 源空间未归档的 Agent
	// AgentAssigned 是处理人里含 Agent 的任务数（克隆后去掉 Agent 处理人）。
	AgentAssigned int
	// 以下为不会克隆的内容。
	Links, GitLinks, IssueLinks, Automations, Documents, Skills, Repos, Iterations, CrossSpaceRelations int
}

// Over 返回超出的上限。
func (s Summary) Over() Limits {
	return Limits{Tasks: s.Tasks > MaxTasks, Comments: s.Comments > MaxComments, Activities: s.Activities > MaxActivities}
}

const taskScope = `t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid AND t.deleted_at IS NULL`

// Summarize 返回源空间的计数。空间不存在或已删除返回 ErrSpaceNotFound。
func Summarize(ctx context.Context, tx pgx.Tx, spaceID string) (Summary, error) {
	var s Summary
	err := tx.QueryRow(ctx, `
		SELECT name, prefix, COALESCE(icon LIKE 'upload:%', false) FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&s.SpaceName, &s.SpacePrefix, &s.UploadedSpaceIcon)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrSpaceNotFound
	}
	if err != nil {
		return s, err
	}
	// 每个计数一条语句，语义清楚；都走 space_id / task_id 索引。
	counts := []struct {
		dst *int
		sql string
	}{
		{&s.Tasks, `SELECT count(*) FROM openfox.tasks t WHERE ` + taskScope},
		{&s.Comments, `SELECT count(*) FROM openfox.task_comments c JOIN openfox.tasks t ON t.organization_id = c.organization_id AND t.id = c.task_id WHERE ` + taskScope},
		{&s.Activities, `SELECT count(*) FROM openfox.task_activities a JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id WHERE ` + taskScope},
		{&s.Projects, `SELECT count(DISTINCT t.project_id) FROM openfox.tasks t WHERE ` + taskScope + ` AND t.project_id IS NOT NULL`},
		{&s.TaskTypes, `SELECT count(*) FROM openfox.task_types y WHERE y.organization_id = openfox_private.current_organization_id() AND y.space_id = $1::uuid AND y.archived_at IS NULL`},
		{&s.Statuses, `SELECT count(DISTINCT w.status_id) FROM openfox.workflow_statuses w WHERE w.organization_id = openfox_private.current_organization_id() AND w.space_id = $1::uuid`},
		{&s.Labels, `SELECT count(*) FROM openfox.labels l WHERE l.organization_id = openfox_private.current_organization_id() AND l.space_id = $1::uuid AND l.archived_at IS NULL`},
		{&s.Fields, `SELECT count(DISTINCT f.field_id) FROM openfox.task_type_fields f WHERE f.organization_id = openfox_private.current_organization_id() AND f.space_id = $1::uuid`},
		{&s.Agents, `SELECT count(*) FROM openfox.agents a WHERE a.organization_id = openfox_private.current_organization_id() AND a.space_id = $1::uuid AND a.archived_at IS NULL`},
		{&s.AgentAssigned, `SELECT count(DISTINCT x.task_id) FROM openfox.task_assignees x JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id WHERE ` + taskScope + ` AND x.kind <> 'HUMAN'`},
		{&s.Links, `SELECT count(*) FROM openfox.task_links x JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id WHERE ` + taskScope},
		{&s.GitLinks, `SELECT count(*) FROM openfox.task_git_links x JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id WHERE ` + taskScope},
		{&s.IssueLinks, `SELECT count(*) FROM openfox.task_issue_links x JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id WHERE ` + taskScope},
		{&s.Automations, `SELECT count(*) FROM openfox.automations x WHERE x.organization_id = openfox_private.current_organization_id() AND x.space_id = $1::uuid`},
		{&s.Documents, `SELECT count(*) FROM openfox.doc_nodes x WHERE x.organization_id = openfox_private.current_organization_id() AND x.space_id = $1::uuid`},
		{&s.Skills, `SELECT count(*) FROM openfox.skills x WHERE x.organization_id = openfox_private.current_organization_id() AND x.space_id = $1::uuid`},
		{&s.Repos, `SELECT count(*) FROM openfox.space_repos x WHERE x.organization_id = openfox_private.current_organization_id() AND x.space_id = $1::uuid`},
		{&s.Iterations, `SELECT count(*) FROM openfox.iterations x WHERE x.organization_id = openfox_private.current_organization_id() AND x.space_id = $1::uuid`},
		{&s.CrossSpaceRelations, `SELECT count(*) FROM openfox.task_relations r
			 WHERE r.organization_id = openfox_private.current_organization_id()
			   AND (r.source_space_id = $1::uuid) <> (r.target_space_id = $1::uuid)`},
	}
	for _, c := range counts {
		if err := tx.QueryRow(ctx, c.sql, spaceID).Scan(c.dst); err != nil {
			return s, err
		}
	}
	return s, nil
}

// people 收集被引用的人员，按键去重。
type people struct {
	byKey map[string]Person
}

func newPeople() *people { return &people{byKey: map[string]Person{}} }

func (p *people) user(id string) string {
	if id == "" {
		return ""
	}
	key := "u:" + id
	if _, ok := p.byKey[key]; !ok {
		p.byKey[key] = Person{Key: key, Kind: "user"}
	}
	return key
}

func (p *people) external(id, name string) string {
	key := "x:" + id
	if _, ok := p.byKey[key]; !ok {
		p.byKey[key] = Person{Key: key, Kind: "external", Name: name}
	}
	return key
}

// ExportOptions 是导出的可选项（零值即克隆使用的行为）。
type ExportOptions struct {
	// MaxTasks / MaxComments / MaxActivities 大于 0 时替换默认上限（导出任务数据的总量上限由调用方另行控制）。
	MaxTasks, MaxComments, MaxActivities int
	// Extras 让任务多带迭代名称与创建人（导出任务数据的 CSV 需要）。
	Extras bool
}

func (o ExportOptions) limits() (tasks, comments, activities int) {
	tasks, comments, activities = MaxTasks, MaxComments, MaxActivities
	if o.MaxTasks > 0 {
		tasks = o.MaxTasks
	}
	if o.MaxComments > 0 {
		comments = o.MaxComments
	}
	if o.MaxActivities > 0 {
		activities = o.MaxActivities
	}
	return
}

// Export 把源空间写入 w（不写 end：由 w.Close 写）。超出任务 / 评论 / 动态上限返回 ErrTooLarge。
func Export(ctx context.Context, tx pgx.Tx, org Org, spaceID string, w *Writer) error {
	return ExportWith(ctx, tx, org, spaceID, w, ExportOptions{})
}

// ExportWith 同 Export，可调整上限并带上额外字段。
func ExportWith(ctx context.Context, tx pgx.Tx, org Org, spaceID string, w *Writer, opts ExportOptions) error {
	sum, err := Summarize(ctx, tx, spaceID)
	if err != nil {
		return err
	}
	maxTasks, maxComments, maxActivities := opts.limits()
	if sum.Tasks > maxTasks || sum.Comments > maxComments || sum.Activities > maxActivities {
		return fmt.Errorf("%w: counts exceed limits", ErrTooLarge)
	}
	var sp Space
	var icon *string
	var tz string
	var retired bool
	if err := tx.QueryRow(ctx, `
		SELECT name, prefix, visibility, icon, purpose, views_enabled, projects_enabled, multi_assignee,
		       estimation_scale, estimation_extended, estimation_allow_zero, estimation_count_unestimated,
		       next_task_number, iterations_enabled, triage_enabled, iteration_timezone, retired_at IS NOT NULL
		  FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).
		Scan(&sp.Name, &sp.Prefix, &sp.Visibility, &icon, &sp.Purpose, &sp.ViewsEnabled, &sp.ProjectsOn, &sp.MultiAssignee,
			&sp.EstimationScale, &sp.EstimationExtended, &sp.EstimationAllowZero, &sp.EstimationCountUnscored,
			&sp.NextTaskNumber, &sp.HasIterations, &sp.HasTriage, &tz, &retired); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSpaceNotFound
		}
		return err
	}
	if icon != nil {
		sp.Icon = *icon
	}
	sp.IterationTimezone = tz
	if err := w.Write(KindHeader, Header{Version: Version, SourceOrganization: org.ID, SourceOrgName: org.Name,
		SourceSpaceID: spaceID, CreatedAt: time.Now().UTC(), Space: sp}); err != nil {
		return err
	}
	ppl := newPeople()
	e := exporter{ctx: ctx, tx: tx, spaceID: spaceID, w: w, people: ppl, extras: opts.Extras}
	steps := []func() error{e.statuses, e.types, e.labels, e.fields, e.projects, e.tasks, e.relations, e.comments, e.activities, e.writePeople}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

type exporter struct {
	ctx     context.Context
	tx      pgx.Tx
	spaceID string
	w       *Writer
	people  *people
	extras  bool

	usedFieldIDs  map[string]bool
	usedProjects  map[string]bool
	taskIDs       map[string]bool
	usedRelTypes  map[string]bool
	usedStatusIDs map[string]bool
}

func (e *exporter) statuses() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT s.id::text, s.name, s.category, COALESCE(s.color, ''), COALESCE(s.icon, '')
		  FROM openfox.task_statuses s
		 WHERE s.organization_id = openfox_private.current_organization_id()
		   AND (s.id IN (SELECT w.status_id FROM openfox.workflow_statuses w
		                  WHERE w.organization_id = s.organization_id AND w.space_id = $1::uuid)
		        OR s.id IN (SELECT t.status_id FROM openfox.tasks t WHERE `+taskScope+`))
		 ORDER BY s.position, s.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	e.usedStatusIDs = map[string]bool{}
	var items []Status
	for rows.Next() {
		var s Status
		if err := rows.Scan(&s.ID, &s.Name, &s.Category, &s.Color, &s.Icon); err != nil {
			return err
		}
		items = append(items, s)
		e.usedStatusIDs[s.ID] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range items {
		if err := e.w.Write(KindStatus, s); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) types() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT y.id::text, y.name, y.description, COALESCE(y.icon, ''), y.enabled, y.is_default, y.archived_at IS NOT NULL
		  FROM openfox.task_types y
		 WHERE y.organization_id = openfox_private.current_organization_id() AND y.space_id = $1::uuid
		   AND (y.archived_at IS NULL OR y.id IN (SELECT t.type_id FROM openfox.tasks t WHERE `+taskScope+`))
		 ORDER BY y.is_default DESC, y.created_at, y.id`, e.spaceID)
	if err != nil {
		return err
	}
	var items []Type
	for rows.Next() {
		var y Type
		if err := rows.Scan(&y.ID, &y.Name, &y.Description, &y.Icon, &y.Enabled, &y.IsDefault, &y.Archived); err != nil {
			rows.Close()
			return err
		}
		y.Workflow = []WorkflowStatus{}
		items = append(items, y)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	index := map[string]int{}
	for i, y := range items {
		index[y.ID] = i
	}
	wf, err := e.tx.Query(e.ctx, `
		SELECT task_type_id::text, status_id::text, position, is_default FROM openfox.workflow_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		 ORDER BY task_type_id, position, status_id`, e.spaceID)
	if err != nil {
		return err
	}
	for wf.Next() {
		var typeID string
		var ws WorkflowStatus
		if err := wf.Scan(&typeID, &ws.StatusID, &ws.Position, &ws.IsDefault); err != nil {
			wf.Close()
			return err
		}
		if i, ok := index[typeID]; ok {
			items[i].Workflow = append(items[i].Workflow, ws)
		}
	}
	wf.Close()
	if err := wf.Err(); err != nil {
		return err
	}
	fields, err := e.tx.Query(e.ctx, `
		SELECT task_type_id::text, field_id::text FROM openfox.task_type_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		 ORDER BY task_type_id, position, field_id`, e.spaceID)
	if err != nil {
		return err
	}
	e.usedFieldIDs = map[string]bool{}
	for fields.Next() {
		var typeID, fieldID string
		if err := fields.Scan(&typeID, &fieldID); err != nil {
			fields.Close()
			return err
		}
		if i, ok := index[typeID]; ok {
			items[i].FieldIDs = append(items[i].FieldIDs, fieldID)
		}
		e.usedFieldIDs[fieldID] = true
	}
	fields.Close()
	if err := fields.Err(); err != nil {
		return err
	}
	for _, y := range items {
		if err := e.w.Write(KindType, y); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) labels() error {
	// 空间标签 + 任务上挂着的其它标签（组织级标签克隆后也是新空间里的平铺标签）。
	rows, err := e.tx.Query(e.ctx, `
		SELECT l.id::text, l.name, COALESCE(l.color, ''), l.description FROM openfox.labels l
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.archived_at IS NULL
		   AND (l.space_id = $1::uuid OR l.id IN (
		        SELECT tl.label_id FROM openfox.task_labels tl JOIN openfox.tasks t ON t.organization_id = tl.organization_id AND t.id = tl.task_id
		         WHERE `+taskScope+`))
		 ORDER BY lower(l.name), l.id`, e.spaceID)
	if err != nil {
		return err
	}
	var items []Label
	seen := map[string]bool{}
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.Description); err != nil {
			rows.Close()
			return err
		}
		// 同名（不区分大小写）只保留第一个：新空间里标签名唯一。
		if seen[strings.ToLower(l.Name)] {
			continue
		}
		seen[strings.ToLower(l.Name)] = true
		items = append(items, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range items {
		if err := e.w.Write(KindLabel, l); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) fields() error {
	// 类型引用的字段 + 任务上有值的字段。
	valued, err := e.tx.Query(e.ctx, `
		SELECT DISTINCT v.field_id::text FROM openfox.task_field_values v
		  JOIN openfox.tasks t ON t.organization_id = v.organization_id AND t.id = v.task_id
		 WHERE `+taskScope, e.spaceID)
	if err != nil {
		return err
	}
	for valued.Next() {
		var id string
		if err := valued.Scan(&id); err != nil {
			valued.Close()
			return err
		}
		e.usedFieldIDs[id] = true
	}
	valued.Close()
	if err := valued.Err(); err != nil {
		return err
	}
	ids := make([]string, 0, len(e.usedFieldIDs))
	for id := range e.usedFieldIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil
	}
	rows, err := e.tx.Query(e.ctx, `
		SELECT id::text, name, kind, description, default_value, COALESCE(member_scope, '')
		  FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[]) AND archived_at IS NULL
		 ORDER BY id`, ids)
	if err != nil {
		return err
	}
	var items []Field
	index := map[string]int{}
	for rows.Next() {
		var f Field
		var def []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Kind, &f.Description, &def, &f.MemberScope); err != nil {
			rows.Close()
			return err
		}
		if len(def) > 0 {
			f.Default = json.RawMessage(def)
		}
		index[f.ID] = len(items)
		items = append(items, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	opts, err := e.tx.Query(e.ctx, `
		SELECT field_id::text, id::text, label, color, position FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id() AND field_id = ANY($1::uuid[]) AND archived_at IS NULL
		 ORDER BY field_id, position, id`, ids)
	if err != nil {
		return err
	}
	for opts.Next() {
		var fieldID string
		var o FieldOption
		if err := opts.Scan(&fieldID, &o.ID, &o.Label, &o.Color, &o.Position); err != nil {
			opts.Close()
			return err
		}
		if i, ok := index[fieldID]; ok {
			items[i].Options = append(items[i].Options, o)
		}
	}
	opts.Close()
	if err := opts.Err(); err != nil {
		return err
	}
	e.usedFieldIDs = map[string]bool{}
	for _, f := range items {
		e.usedFieldIDs[f.ID] = true
		if err := e.w.Write(KindField, f); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) projects() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT p.id::text, p.name, COALESCE(p.icon, ''), p.description, p.status, p.priority FROM openfox.projects p
		 WHERE p.organization_id = openfox_private.current_organization_id()
		   AND p.id IN (SELECT t.project_id FROM openfox.tasks t WHERE `+taskScope+` AND t.project_id IS NOT NULL)
		 ORDER BY lower(p.name), p.id`, e.spaceID)
	if err != nil {
		return err
	}
	var items []Project
	e.usedProjects = map[string]bool{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Icon, &p.Description, &p.Status, &p.Priority); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
		e.usedProjects[p.ID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range items {
		if err := e.w.Write(KindProject, p); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) tasks() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT t.id::text, t.sequence, t.title, t.description, t.acceptance, t.priority, t.type_id::text, t.status_id::text,
		       COALESCE(t.parent_task_id::text, ''), COALESCE(t.project_id::text, ''),
		       COALESCE(to_char(t.start_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), ''),
		       t.estimate::float8, t.created_at, t.updated_at, COALESCE(t.external_assignee_id::text, '')
		  FROM openfox.tasks t
		 WHERE `+taskScope+`
		 ORDER BY t.rank NULLS LAST, t.sequence, t.id`, e.spaceID)
	if err != nil {
		return err
	}
	var items []Task
	index := map[string]int{}
	e.taskIDs = map[string]bool{}
	externalIDs := map[string]bool{}
	for rows.Next() {
		var t Task
		var external string
		if err := rows.Scan(&t.ID, &t.Sequence, &t.Title, &t.Description, &t.Acceptance, &t.Priority, &t.TypeID, &t.StatusID,
			&t.ParentID, &t.ProjectID, &t.StartDate, &t.DueDate, &t.Estimate, &t.CreatedAt, &t.UpdatedAt, &external); err != nil {
			rows.Close()
			return err
		}
		index[t.ID] = len(items)
		e.taskIDs[t.ID] = true
		t.External = external
		if external != "" {
			externalIDs[external] = true
		}
		items = append(items, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// 父任务不在包内（已删除）时去掉父引用。
	for i := range items {
		if items[i].ParentID != "" && !e.taskIDs[items[i].ParentID] {
			items[i].ParentID = ""
		}
	}
	if err := e.loadAssignees(items, index); err != nil {
		return err
	}
	if err := e.loadFollowers(items, index); err != nil {
		return err
	}
	if err := e.loadLabels(items, index); err != nil {
		return err
	}
	if err := e.loadFieldValues(items, index); err != nil {
		return err
	}
	if err := e.resolveExternal(items, externalIDs); err != nil {
		return err
	}
	if e.extras {
		if err := e.loadExtras(items, index); err != nil {
			return err
		}
	}
	for _, t := range items {
		if err := e.w.Write(KindTask, t); err != nil {
			return err
		}
	}
	return nil
}

// loadExtras 补迭代名称（没有名称时用「#编号」）与创建人（第一条 task.created 动态的操作者）。
func (e *exporter) loadExtras(items []Task, index map[string]int) error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT t.id::text, CASE WHEN i.name <> '' THEN i.name ELSE '#' || i.number::text END
		  FROM openfox.tasks t
		  JOIN openfox.iterations i ON i.organization_id = t.organization_id AND i.id = t.iteration_id
		 WHERE `+taskScope, e.spaceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		if i, ok := index[id]; ok {
			items[i].IterationName = name
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	creators, err := e.tx.Query(e.ctx, `
		SELECT DISTINCT ON (a.task_id) a.task_id::text, a.actor_id::text
		  FROM openfox.task_activities a
		  JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id
		 WHERE `+taskScope+` AND a.kind = 'task.created'
		 ORDER BY a.task_id, a.created_at, a.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer creators.Close()
	for creators.Next() {
		var id, actor string
		if err := creators.Scan(&id, &actor); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			items[i].CreatedByKey = e.people.user(actor)
		}
	}
	return creators.Err()
}

func (e *exporter) loadAssignees(items []Task, index map[string]int) error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT x.task_id::text, x.kind, COALESCE(x.user_id::text, ''), COALESCE(a.name, ''), COALESCE(c.name, '')
		  FROM openfox.task_assignees x
		  JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id
		  LEFT JOIN openfox.agents a ON a.organization_id = x.organization_id AND a.id = x.agent_id
		  LEFT JOIN openfox.agent_crews c ON c.organization_id = x.organization_id AND c.id = x.crew_id
		 WHERE `+taskScope+`
		 ORDER BY x.task_id, x.created_at, x.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, kind, userID, agentName, crewName string
		if err := rows.Scan(&taskID, &kind, &userID, &agentName, &crewName); err != nil {
			return err
		}
		i, ok := index[taskID]
		if !ok {
			continue
		}
		as := Assignee{Kind: kind}
		switch kind {
		case "HUMAN":
			as.PersonKey = e.people.user(userID)
		case "AGENT":
			as.Name = agentName
		default:
			as.Name = crewName
		}
		items[i].Assignees = append(items[i].Assignees, as)
	}
	return rows.Err()
}

func (e *exporter) loadFollowers(items []Task, index map[string]int) error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT x.task_id::text, x.user_id::text FROM openfox.task_followers x
		  JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id
		 WHERE `+taskScope+` AND x.kind = 'HUMAN' AND x.user_id IS NOT NULL
		 ORDER BY x.task_id, x.created_at, x.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, userID string
		if err := rows.Scan(&taskID, &userID); err != nil {
			return err
		}
		if i, ok := index[taskID]; ok {
			items[i].Followers = append(items[i].Followers, e.people.user(userID))
		}
	}
	return rows.Err()
}

func (e *exporter) loadLabels(items []Task, index map[string]int) error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT x.task_id::text, x.label_id::text FROM openfox.task_labels x
		  JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id
		 WHERE `+taskScope+`
		 ORDER BY x.task_id, x.created_at, x.label_id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, labelID string
		if err := rows.Scan(&taskID, &labelID); err != nil {
			return err
		}
		if i, ok := index[taskID]; ok {
			items[i].LabelIDs = append(items[i].LabelIDs, labelID)
		}
	}
	return rows.Err()
}

func (e *exporter) loadFieldValues(items []Task, index map[string]int) error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT v.task_id::text, v.field_id::text, v.value FROM openfox.task_field_values v
		  JOIN openfox.tasks t ON t.organization_id = v.organization_id AND t.id = v.task_id
		 WHERE `+taskScope+`
		 ORDER BY v.task_id, v.field_id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, fieldID string
		var value []byte
		if err := rows.Scan(&taskID, &fieldID, &value); err != nil {
			return err
		}
		i, ok := index[taskID]
		if !ok || !e.usedFieldIDs[fieldID] {
			continue
		}
		if items[i].Fields == nil {
			items[i].Fields = map[string]json.RawMessage{}
		}
		items[i].Fields[fieldID] = json.RawMessage(value)
	}
	return rows.Err()
}

func (e *exporter) resolveExternal(items []Task, ids map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rows, err := e.tx.Query(e.ctx, `
		SELECT id::text, display_name FROM openfox.external_accounts
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, list)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		names[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if id := items[i].External; id != "" {
			items[i].External = e.people.external(id, names[id])
		}
	}
	return nil
}

func (e *exporter) relations() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT r.source_task_id::text, r.target_task_id::text, COALESCE(r.builtin_type, ''), COALESCE(r.relation_type_id::text, '')
		  FROM openfox.task_relations r
		 WHERE r.organization_id = openfox_private.current_organization_id()
		   AND r.source_space_id = $1::uuid AND r.target_space_id = $1::uuid
		 ORDER BY r.created_at, r.id`, e.spaceID)
	if err != nil {
		return err
	}
	var items []Relation
	e.usedRelTypes = map[string]bool{}
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.SourceTaskID, &r.TargetTaskID, &r.Builtin, &r.TypeID); err != nil {
			rows.Close()
			return err
		}
		// 任一端是已删除任务的关联不带。
		if !e.taskIDs[r.SourceTaskID] || !e.taskIDs[r.TargetTaskID] {
			continue
		}
		items = append(items, r)
		if r.TypeID != "" {
			e.usedRelTypes[r.TypeID] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(e.usedRelTypes) > 0 {
		ids := make([]string, 0, len(e.usedRelTypes))
		for id := range e.usedRelTypes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		tr, err := e.tx.Query(e.ctx, `
			SELECT id::text, name, outward_label, inward_label FROM openfox.task_relation_types
			 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[]) ORDER BY id`, ids)
		if err != nil {
			return err
		}
		var types []RelType
		for tr.Next() {
			var t RelType
			if err := tr.Scan(&t.ID, &t.Name, &t.Outward, &t.Inward); err != nil {
				tr.Close()
				return err
			}
			types = append(types, t)
		}
		tr.Close()
		if err := tr.Err(); err != nil {
			return err
		}
		for _, t := range types {
			if err := e.w.Write(KindRelType, t); err != nil {
				return err
			}
		}
	}
	for _, r := range items {
		if err := e.w.Write(KindRelation, r); err != nil {
			return err
		}
	}
	return nil
}

func (e *exporter) comments() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT c.id::text, c.task_id::text, COALESCE(c.author_user_id::text, ''), COALESCE(a.name, ''), c.body, c.created_at
		  FROM openfox.task_comments c
		  JOIN openfox.tasks t ON t.organization_id = c.organization_id AND t.id = c.task_id
		  LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
		 WHERE `+taskScope+`
		 ORDER BY c.created_at, c.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Comment
		var author string
		if err := rows.Scan(&c.ID, &c.TaskID, &author, &c.AgentName, &c.Body, &c.CreatedAt); err != nil {
			return err
		}
		c.AuthorKey = e.people.user(author)
		if err := e.w.Write(KindComment, c); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (e *exporter) activities() error {
	rows, err := e.tx.Query(e.ctx, `
		SELECT a.task_id::text, a.actor_id::text, a.kind, a.created_at
		  FROM openfox.task_activities a
		  JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id
		 WHERE `+taskScope+`
		 ORDER BY a.created_at, a.id`, e.spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Activity
		var actor string
		if err := rows.Scan(&a.TaskID, &actor, &a.Kind, &a.CreatedAt); err != nil {
			return err
		}
		a.ActorKey = e.people.user(actor)
		if err := e.w.Write(KindActivity, a); err != nil {
			return err
		}
	}
	return rows.Err()
}

// writePeople 查询被引用成员的姓名与邮箱并写出（成员已离开组织时 users 仍可读）。
func (e *exporter) writePeople() error {
	var userIDs []string
	for key, p := range e.people.byKey {
		if p.Kind == "user" {
			userIDs = append(userIDs, strings.TrimPrefix(key, "u:"))
		}
	}
	sort.Strings(userIDs)
	if len(userIDs) > 0 {
		rows, err := e.tx.Query(e.ctx, `
			SELECT id::text, full_name, email FROM openfox.users WHERE id = ANY($1::uuid[])`, userIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, name, email string
			if err := rows.Scan(&id, &name, &email); err != nil {
				rows.Close()
				return err
			}
			p := e.people.byKey["u:"+id]
			p.Email = email
			p.Name = strings.TrimSpace(name)
			if p.Name == "" {
				p.Name = strings.SplitN(email, "@", 2)[0]
			}
			e.people.byKey["u:"+id] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(e.people.byKey))
	for k := range e.people.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := e.people.byKey[k]
		if p.Name == "" {
			p.Name = "未知用户"
		}
		if err := e.w.Write(KindPerson, p); err != nil {
			return err
		}
	}
	return nil
}
