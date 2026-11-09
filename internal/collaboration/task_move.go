package collaboration

// 把一组任务移到另一个空间。单任务移动（移动整棵子树）与停用空间的「移动未完成任务」共用 moveTasks。
// 子表搬迁规则见 moveTasks 的注释；单任务移动额外做字段映射、写旧编号别名、动态、关闭指派会话与源空间事件。

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// movingTask 是移动需要的任务信息。
type movingTask struct {
	ID, Title, TypeID, TypeName, Category, StatusID, ParentID, ProjectID, IterationID string
	Sequence                                                                          int64
	Estimate                                                                          *float64
	Version                                                                           int64
	InTriage                                                                          bool
}

const movingTaskColumns = `t.id::text, t.title, t.type_id::text, tt.name, t.status, t.status_id::text,
	COALESCE(t.parent_task_id::text, ''), COALESCE(t.project_id::text, ''), COALESCE(t.iteration_id::text, ''),
	t.sequence, t.estimate::float8, t.version, t.triage_entered_at IS NOT NULL`

func scanMovingTasks(rows pgx.Rows) ([]movingTask, error) {
	defer rows.Close()
	items := make([]movingTask, 0)
	for rows.Next() {
		var item movingTask
		if err := rows.Scan(&item.ID, &item.Title, &item.TypeID, &item.TypeName, &item.Category, &item.StatusID,
			&item.ParentID, &item.ProjectID, &item.IterationID, &item.Sequence, &item.Estimate, &item.Version,
			&item.InTriage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > maxRetireTasks {
		return nil, NewRuleError(409, "TOO_MANY_TASKS", "too many tasks to process at once")
	}
	return items, nil
}

// loadOpenMovingTasks 返回空间里的未完成任务（停用空间移动用），按序号排列。
func loadOpenMovingTasks(ctx context.Context, tx pgx.Tx, spaceID string) ([]movingTask, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+movingTaskColumns+`
		  FROM openfox.tasks t
		  JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		   AND `+openTaskSQL+`
		 ORDER BY t.sequence
		 LIMIT $2`, spaceID, maxRetireTasks+1)
	if err != nil {
		return nil, err
	}
	return scanMovingTasks(rows)
}

// loadMovingSubtree 返回根任务与它未删除的全部后代（任意状态），根任务排第一，其余按序号。
// 已删除的任务不进入集合，递归在它那里停止；它的子任务（若有）由 moveTasks 解除父子关系。
func loadMovingSubtree(ctx context.Context, tx pgx.Tx, spaceID, rootID string) ([]movingTask, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT t.id FROM openfox.tasks t
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
			   AND t.id = $2::uuid AND t.deleted_at IS NULL
			UNION
			SELECT c.id FROM openfox.tasks c
			  JOIN tree p ON c.parent_task_id = p.id
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.space_id = $1::uuid
			   AND c.deleted_at IS NULL)
		SELECT `+movingTaskColumns+`
		  FROM openfox.tasks t
		  JOIN tree ON tree.id = t.id
		  JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		 ORDER BY (t.id = $2::uuid) DESC, t.sequence
		 LIMIT $3`, spaceID, rootID, maxRetireTasks+1)
	if err != nil {
		return nil, err
	}
	return scanMovingTasks(rows)
}

// moveTarget 是目标空间里映射所需的配置。
type moveTarget struct {
	defaultType targetType
	byName      map[string]targetType
	estimation  SpaceEstimation
	// projects：目标空间也关联着的专项。
	projects map[string]bool
}

func loadMoveTarget(ctx context.Context, tx pgx.Tx, targetID string, single bool) (moveTarget, error) {
	types, err := loadTargetTypes(ctx, tx, targetID)
	if err != nil {
		return moveTarget{}, err
	}
	if len(types) == 0 {
		return moveTarget{}, ErrInvalidTarget
	}
	target := moveTarget{defaultType: types[0], byName: map[string]targetType{}, projects: map[string]bool{}}
	for _, item := range types {
		key := strings.ToLower(item.Name)
		if _, exists := target.byName[key]; !exists {
			target.byName[key] = item
		}
	}
	if !single {
		return target, nil
	}
	if target.estimation, err = loadSpaceEstimation(ctx, tx, targetID, ""); err != nil {
		if err == ErrSpaceNotFound {
			return moveTarget{}, ErrInvalidTarget
		}
		return moveTarget{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT project_id::text FROM openfox.project_spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, targetID)
	if err != nil {
		return moveTarget{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return moveTarget{}, err
		}
		target.projects[id] = true
	}
	return target, rows.Err()
}

// taskMapping 是一个任务移到目标空间后的字段值。
type taskMapping struct {
	TypeID, TypeName, StatusID string
	// ParentID 为空表示移动后没有父任务；ParentDetached 表示原来有父任务、但父任务不随之移动。
	ParentID       string
	ParentDetached bool
	Estimate       *float64
	ProjectID      string
}

// mapTask 计算一个任务的映射。
//   - 类型：目标空间同名（不区分大小写）启用类型，否则目标默认类型；
//   - 状态：单任务移动先试目标工作流里的同一状态；再取同分类中 position 最小的；都没有时用工作流默认状态；
//   - 父任务：父任务也在移动集合里时保留，否则置空；
//   - 估时 / 专项：单任务移动时换到目标刻度的最近档位（取不到清空）、目标空间不在专项里时清空；停用移动保持原值。
func mapTask(task movingTask, target moveTarget, moved map[string]bool, single bool) taskMapping {
	kind, ok := target.byName[strings.ToLower(task.TypeName)]
	if !ok {
		kind = target.defaultType
	}
	mapping := taskMapping{TypeID: kind.ID, TypeName: kind.Name, Estimate: task.Estimate, ProjectID: task.ProjectID}
	mapping.StatusID = kind.mapStatus(task.StatusID, task.Category, single)
	if task.ParentID != "" {
		if moved[task.ParentID] {
			mapping.ParentID = task.ParentID
		} else {
			mapping.ParentDetached = true
		}
	}
	if single {
		if task.Estimate != nil {
			mapping.Estimate = suggestEstimate(target.estimation, *task.Estimate)
		}
		if task.ProjectID != "" && !target.projects[task.ProjectID] {
			mapping.ProjectID = ""
		}
	}
	return mapping
}

// moveTasksInput 是 moveTasks 的输入。Single 为 true 表示单任务移动（字段映射、别名、动态等，见文件头）。
type moveTasksInput struct {
	SourceID, TargetID string
	Tasks              []movingTask
	Single             bool
	ActorID, RequestID string
	// CanceledRuns 只写进根任务的 task.moved 动态，便于幂等重放时返回。
	CanceledRuns int
}

// moveTasksResult：Sequences 与 input.Tasks 一一对应。
type moveTasksResult struct {
	Sequences []int64
}

// moveTasks 把 input.Tasks 移到目标空间（调用方已校验权限、锁住两个空间行与任务）。
//
// 任务子表（处理人、关注者、链接、表情、字段值）以 (organization_id, space_id, task_id) 复合外键引用任务，
// 且 openfox_app 对其中多张表没有 UPDATE 权限，因此先把这些行删除并以 JSON 暂存，改写任务后再按新空间插回。
// 评论、动态、标签关联只以 (organization_id, task_id) 引用任务，直接改写 space_id。
// Git 关联与 Issue 链接由外键 ON UPDATE CASCADE 随任务改写（迁移 000064）；Issue 链接先解除同步（D6）。
// 迭代属于空间，一律清空。会话与 Run 留在原空间。
func moveTasks(ctx context.Context, tx pgx.Tx, input moveTasksInput) (moveTasksResult, error) {
	tasks := input.Tasks
	if len(tasks) == 0 {
		return moveTasksResult{}, nil
	}
	target, err := loadMoveTarget(ctx, tx, input.TargetID, input.Single)
	if err != nil {
		return moveTasksResult{}, err
	}
	moved := make(map[string]bool, len(tasks))
	ids := make([]string, len(tasks))
	for index, task := range tasks {
		moved[task.ID] = true
		ids[index] = task.ID
	}
	// 目标空间序号：一次性预留 len(tasks) 个（目标空间行已由调用方锁住，或在这里锁住）。
	var firstSequence int64
	err = tx.QueryRow(ctx, `
		UPDATE openfox.spaces SET next_task_number = next_task_number + $2, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'ACTIVE' AND deleted_at IS NULL
		RETURNING next_task_number - $2`, input.TargetID, len(tasks)).Scan(&firstSequence)
	if err == pgx.ErrNoRows {
		return moveTasksResult{}, ErrInvalidTarget
	}
	if err != nil {
		return moveTasksResult{}, err
	}
	result := moveTasksResult{Sequences: make([]int64, len(tasks))}
	typeIDs, statusIDs := make([]string, len(tasks)), make([]string, len(tasks))
	parents, projects := make([]*string, len(tasks)), make([]*string, len(tasks))
	estimates := make([]*float64, len(tasks))
	for index, task := range tasks {
		mapping := mapTask(task, target, moved, input.Single)
		typeIDs[index], statusIDs[index], estimates[index] = mapping.TypeID, mapping.StatusID, mapping.Estimate
		result.Sequences[index] = firstSequence + int64(index)
		if mapping.ParentID != "" {
			parent := mapping.ParentID
			parents[index] = &parent
		}
		if mapping.ProjectID != "" {
			project := mapping.ProjectID
			projects[index] = &project
		}
	}
	// 留在原空间的子任务（含已删除任务）与被移走的父任务解除关系。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET parent_task_id = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND parent_task_id = ANY($2::uuid[]) AND NOT (id = ANY($2::uuid[]))`, input.SourceID, ids); err != nil {
		return moveTasksResult{}, mapDatabaseError(err)
	}
	if input.Single {
		// 单任务移动：被移除的 Agent 处理人写 UNASSIGNED 事件（运行已由调用方在同一事务里取消，事件兜底取消之后新起的运行）。
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_agent_events (organization_id, kind, space_id, task_id, agent_id, actor_user_id)
			SELECT a.organization_id, 'UNASSIGNED', $2::uuid, a.task_id, a.agent_id, $3::uuid
			  FROM openfox.task_assignees a
			 WHERE a.organization_id = openfox_private.current_organization_id()
			   AND a.task_id = ANY($1::uuid[]) AND a.kind = 'AGENT'`, ids, input.SourceID, input.ActorID); err != nil {
			return moveTasksResult{}, err
		}
	}
	if err := detachSpaceScopedRows(ctx, tx, ids); err != nil {
		return moveTasksResult{}, err
	}
	detached := map[string][]byte{}
	for _, table := range movableTaskChildren {
		var payload []byte
		if err := tx.QueryRow(ctx, `
			WITH d AS (
				DELETE FROM openfox.`+table+` x
				 WHERE x.organization_id = openfox_private.current_organization_id() AND x.task_id = ANY($1::uuid[])
				RETURNING x.*)
			SELECT COALESCE(jsonb_agg(to_jsonb(d)), '[]'::jsonb) FROM d`, ids).Scan(&payload); err != nil {
			return moveTasksResult{}, err
		}
		detached[table] = payload
	}
	var relations []byte
	if err := tx.QueryRow(ctx, `
		WITH d AS (
			DELETE FROM openfox.task_relations x
			 WHERE x.organization_id = openfox_private.current_organization_id()
			   AND (x.source_task_id = ANY($1::uuid[]) OR x.target_task_id = ANY($1::uuid[]))
			RETURNING x.*)
		SELECT COALESCE(jsonb_agg(to_jsonb(d)), '[]'::jsonb) FROM d`, ids).Scan(&relations); err != nil {
		return moveTasksResult{}, err
	}
	// 单任务移动（$9）：估时 / 专项按映射改写，分拣台字段清空（D7）。停用移动保持原值，与之前一致。
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t
		   SET space_id = $2::uuid, type_id = m.type_id, status_id = m.status_id, sequence = m.sequence,
		       parent_task_id = m.parent_id, executor_agent_id = NULL, rank = NULL, iteration_id = NULL,
		       estimate = CASE WHEN $9 THEN m.estimate ELSE t.estimate END,
		       project_id = CASE WHEN $9 THEN m.project_id ELSE t.project_id END,
		       triage_entered_at = CASE WHEN $9 THEN NULL ELSE t.triage_entered_at END,
		       triage_source = CASE WHEN $9 THEN NULL ELSE t.triage_source END,
		       triage_snoozed_until = CASE WHEN $9 THEN NULL ELSE t.triage_snoozed_until END,
		       version = t.version + 1, updated_at = clock_timestamp()
		  FROM unnest($3::uuid[], $4::uuid[], $5::uuid[], $6::bigint[], $7::uuid[], $8::numeric[], $10::uuid[])
		       AS m(task_id, type_id, status_id, sequence, parent_id, estimate, project_id)
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		   AND t.id = m.task_id AND t.deleted_at IS NULL`,
		input.SourceID, input.TargetID, ids, typeIDs, statusIDs, result.Sequences, parents, estimates, input.Single, projects)
	if err != nil {
		return moveTasksResult{}, mapDatabaseError(err)
	}
	if int(tag.RowsAffected()) != len(tasks) {
		return moveTasksResult{}, ErrConflict
	}
	for _, table := range []string{"task_comments", "task_activities", "task_labels"} {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.`+table+` SET space_id = $1::uuid
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($2::uuid[])`,
			input.TargetID, ids); err != nil {
			return moveTasksResult{}, mapDatabaseError(err)
		}
	}
	for _, table := range movableTaskChildren {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.`+table+`
			SELECT (jsonb_populate_record(NULL::openfox.`+table+`, e || jsonb_build_object('space_id', $2::uuid))).*
			  FROM jsonb_array_elements($1::jsonb) e`, detached[table], input.TargetID); err != nil {
			return moveTasksResult{}, mapDatabaseError(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_relations
		SELECT (jsonb_populate_record(NULL::openfox.task_relations, e || jsonb_build_object(
		           'source_space_id', CASE WHEN (e->>'source_task_id')::uuid = ANY($2::uuid[]) THEN $3::text ELSE e->>'source_space_id' END,
		           'target_space_id', CASE WHEN (e->>'target_task_id')::uuid = ANY($2::uuid[]) THEN $3::text ELSE e->>'target_space_id' END))).*
		  FROM jsonb_array_elements($1::jsonb) e`, relations, ids, input.TargetID); err != nil {
		return moveTasksResult{}, mapDatabaseError(err)
	}
	if input.Single {
		if err := recordSingleMove(ctx, tx, input, ids, result.Sequences); err != nil {
			return moveTasksResult{}, err
		}
	}
	return result, nil
}

// movableTaskChildren 是以 (organization_id, space_id, task_id) 引用任务、移动时需要整体搬迁的子表。
// task_reactions 引用评论，必须排在评论 space_id 改写之后插回（插回发生在改写之后，顺序满足）。
var movableTaskChildren = []string{"task_assignees", "task_followers", "task_links", "task_field_values", "task_reactions"}

// detachSpaceScopedRows 删除不能跟到别的空间的关联，并解除 Issue 同步：
//   - Agent / 小队处理人与关注者（属于原空间；人员处理人保留）；
//   - 本空间标签（组织级标签保留，随任务改写 space_id）；
//   - Issue 链接（task_issue_links 与同步配置按空间绑定，D6）：置 unlinked_at，未发送的出站项置为 SKIPPED。
//     远端 Issue 本身不动。必须在改写 tasks 之前做：tasks 的 UPDATE 触发器只为未解除的链接入队同步。
func detachSpaceScopedRows(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, statement := range []string{
		`DELETE FROM openfox.task_assignees WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = ANY($1::uuid[]) AND kind <> 'HUMAN'`,
		`DELETE FROM openfox.task_followers WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = ANY($1::uuid[]) AND kind <> 'HUMAN'`,
		`DELETE FROM openfox.task_labels tl WHERE tl.organization_id = openfox_private.current_organization_id()
		   AND tl.task_id = ANY($1::uuid[])
		   AND EXISTS (SELECT 1 FROM openfox.labels l
		                WHERE l.organization_id = tl.organization_id AND l.id = tl.label_id AND l.space_id IS NOT NULL)`,
		`UPDATE openfox.task_issue_links SET unlinked_at = clock_timestamp(), updated_at = clock_timestamp()
		  WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($1::uuid[])
		    AND unlinked_at IS NULL`,
		`UPDATE openfox.integration_sync_outbox
		    SET status = 'SKIPPED', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL,
		        last_error = 'task moved', updated_at = clock_timestamp()
		  WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($1::uuid[])
		    AND status IN ('PENDING', 'SENDING')`,
	} {
		if _, err := tx.Exec(ctx, statement, ids); err != nil {
			return mapDatabaseError(err)
		}
	}
	return nil
}

// recordSingleMove 是单任务移动的收尾：旧编号别名（D1）、task.moved 动态、关闭指派会话、源空间刷新事件。
func recordSingleMove(ctx context.Context, tx pgx.Tx, input moveTasksInput, ids []string, sequences []int64) error {
	var sourcePrefix, targetPrefix string
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT prefix FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid),
		       (SELECT prefix FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $2::uuid)`,
		input.SourceID, input.TargetID).Scan(&sourcePrefix, &targetPrefix); err != nil {
		return err
	}
	oldSequences := make([]int64, len(input.Tasks))
	diffs, sources := make([]string, len(input.Tasks)), make([]string, len(input.Tasks))
	for index, task := range input.Tasks {
		oldSequences[index] = task.Sequence
		diff := map[string]any{
			"from_space_id": input.SourceID, "to_space_id": input.TargetID,
			"from_display_id": displayID(sourcePrefix, task.Sequence), "to_display_id": displayID(targetPrefix, sequences[index]),
		}
		if index == 0 {
			diff["moved_count"], diff["canceled_runs"] = len(input.Tasks), input.CanceledRuns
		}
		payload, err := json.Marshal(diff)
		if err != nil {
			return err
		}
		diffs[index], sources[index] = string(payload), input.RequestID+":move:"+task.ID
	}
	// 源空间的旧序号 → 任务。同一序号再次写入（导入插回空序号后又被移走）时指向最新的任务。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_display_aliases (organization_id, space_id, sequence, task_id, moved_by)
		SELECT openfox_private.current_organization_id(), $1::uuid, m.sequence, m.task_id, $4::uuid
		  FROM unnest($2::uuid[], $3::bigint[]) AS m(task_id, sequence)
		ON CONFLICT (organization_id, space_id, sequence)
		DO UPDATE SET task_id = EXCLUDED.task_id, moved_at = EXCLUDED.moved_at, moved_by = EXCLUDED.moved_by`,
		input.SourceID, ids, oldSequences, input.ActorID); err != nil {
		return mapDatabaseError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, m.task_id, gen_random_uuid(), $2::uuid,
		       'task.moved', m.diff::jsonb, m.source_id
		  FROM unnest($3::uuid[], $4::text[], $5::text[]) AS m(task_id, diff, source_id)`,
		input.TargetID, input.ActorID, ids, diffs, sources); err != nil {
		return mapDatabaseError(err)
	}
	return closeMovedTaskConversations(ctx, tx, input.SourceID, ids)
}

// closeMovedTaskConversations 把移走任务的指派会话改为 COMPLETED：会话留在源空间，运行的范围触发器只校验
// 运行与会话的 task_id 一致，不关会话的话源空间的 Agent 还能继续给已移走的任务起新运行。
// 然后给源空间补发一条 task.changed：tasks 的事件触发器按新 space_id 只发给目标空间，源空间列表也要刷新。
func closeMovedTaskConversations(ctx context.Context, tx pgx.Tx, sourceID string, ids []string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET lifecycle = 'COMPLETED', version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($1::uuid[])
		   AND origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN'`, ids); err != nil {
		return mapDatabaseError(err)
	}
	limit := len(ids)
	if limit > 100 {
		limit = 100
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
		VALUES (openfox_private.current_organization_id(), 'task.changed', 'task', $1::uuid,
		        jsonb_build_object('task_ids', to_jsonb($2::uuid[]), 'count', $3::int))`,
		sourceID, ids[:limit], len(ids))
	return err
}
