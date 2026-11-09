package collaboration

// 导入写入：不经过分诊、通知、关注者与 Agent 指派事件的任务创建 / 覆盖。
// 调用方（internal/imports 的 worker）已在事务里设置 openfox.import_job 标记，评论与状态自动化因此不会触发。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// ImportComment 是导入的一条评论。AuthorUserID 为空时以 actor 作为作者（正文里已带来源姓名前缀）。
type ImportComment struct {
	AuthorUserID string
	Body         string
	CreatedAt    time.Time
	ClientID     string
}

// ImportTaskInput 是导入写入任务的内容。日期为 YYYY-MM-DD，空串表示未设置。
type ImportTaskInput struct {
	Title       string
	Description string
	TypeID      string
	StatusID    string
	Priority    string
	// AssigneeUserID 与 ExternalAssigneeID 二选一，可都为空。
	AssigneeUserID     string
	ExternalAssigneeID string
	LabelIDs           []string
	CreatedAt          time.Time
	StartDate          string
	DueDate            string
	Estimate           *float64
	SourceKey          string
	Comments           []ImportComment
	// Sequence 大于 0 时作为任务序号（保留来源票号），并且不推进 next_task_number；撞号时退回自动序号。
	Sequence int64
	// Source 写入 task.imported 动态的来源，空表示 jira。
	Source string
	// IterationID 非空时加入该迭代（迭代须属于同一空间，状态不限）。
	IterationID string
}

// importQuerier 是 pgx.Tx 与其子事务共有的查询接口。
type importQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// importValidateIteration 导入的迭代归属校验：空间已开启迭代，迭代存在于该空间且未取消。
// 与 validateTaskIteration 不同，已完成的迭代也允许（Sprint 已关闭的任务要归入已完成迭代）。
func importValidateIteration(ctx context.Context, tx pgx.Tx, spaceID, iterationID string) error {
	if !membership.ValidateUUID(iterationID) {
		return ErrInvalidIteration
	}
	var ok bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.iterations i JOIN openfox.spaces s ON s.organization_id = i.organization_id AND s.id = i.space_id
		                WHERE i.organization_id = openfox_private.current_organization_id() AND i.space_id = $1::uuid
		                  AND i.id = $2::uuid AND i.status <> 'canceled' AND s.iterations_enabled)`, spaceID, iterationID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrInvalidIteration
	}
	return nil
}

// ImportCommentBody 生成评论正文：作者映射到成员时用原文，否则加首行「**姓名**（来自 Jira · 时间）」。
func ImportCommentBody(authorMapped bool, sourceName string, at time.Time, hasAt bool, body string) string {
	if authorMapped {
		return body
	}
	name := strings.TrimSpace(sourceName)
	if name == "" {
		name = "未知用户"
	}
	when := "时间未知"
	if hasAt {
		// 按解析时的时区（目标空间时区）显示，与来源系统里看到的时间一致。
		when = at.Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("**%s**（来自 Jira · %s）\n\n%s", name, when, body)
}

// ImportCommentClientID 用来源 + 时间 + 作者 + 正文生成稳定的 client_comment_id，覆盖导入时据此去重。
func ImportCommentClientID(source string, at time.Time, author, body string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + at.UTC().Format(time.RFC3339Nano) + "\x00" + author + "\x00" + body))
	return "import:" + hex.EncodeToString(sum[:])[:40]
}

// ImportAssignable 判断成员能否作为该空间任务的处理人（组织 ACTIVE 成员且可读该空间）。
func (s Service) ImportAssignable(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, userID string) bool {
	if !membership.ValidateUUID(userID) {
		return false
	}
	return s.validateAssigneeRefs(ctx, tx, spaceID, actor, []TaskAssigneeRef{{Kind: "HUMAN", ID: userID}}) == nil
}

func (s Service) importValidate(input *ImportTaskInput) (priority, startDate, dueDate string, err error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len([]rune(input.Title)) > 500 || len([]rune(input.Description)) > 10000 {
		return "", "", "", ErrInvalidTask
	}
	if input.AssigneeUserID != "" && input.ExternalAssigneeID != "" {
		return "", "", "", ErrInvalidTask
	}
	if strings.TrimSpace(input.Priority) == "" {
		input.Priority = "NONE"
	}
	if priority, err = ParsePriority(input.Priority); err != nil {
		return "", "", "", err
	}
	if startDate, err = parseDate(input.StartDate); err != nil {
		return "", "", "", err
	}
	if dueDate, err = parseDate(input.DueDate); err != nil {
		return "", "", "", err
	}
	if err = checkDateRange(startDate, dueDate); err != nil {
		return "", "", "", err
	}
	return priority, startDate, dueDate, nil
}

func (s Service) importCheckEstimate(ctx context.Context, tx pgx.Tx, spaceID string, estimate *float64) error {
	if estimate == nil {
		return nil
	}
	settings, err := loadSpaceEstimation(ctx, tx, spaceID, "")
	if err != nil {
		return err
	}
	return ValidateEstimate(settings, *estimate)
}

func (s Service) importWriteComments(ctx context.Context, tx pgx.Tx, spaceID, taskID string, actor Actor, comments []ImportComment) error {
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" || len(body) > 65000 {
			continue
		}
		author := c.AuthorUserID
		if author == "" {
			author = actor.UserID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, client_comment_id, created_at)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, NULLIF($5, ''),
			        COALESCE($6::timestamptz, clock_timestamp()))
			ON CONFLICT DO NOTHING`, spaceID, taskID, author, body, c.ClientID, nullTime(c.CreatedAt)); err != nil {
			return mapDatabaseError(err)
		}
	}
	return nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// CreateIssueTask preserves the import path for a space without triage. Live Issue sync,
// unlike a bulk historical import, participates in the enabled space's triage rules.
func (s Service) CreateIssueTask(ctx context.Context, tx pgx.Tx, orgID, spaceID string, actor Actor, input ImportTaskInput) (string, int64, error) {
	cfg, err := loadTriageConfig(ctx, tx, spaceID)
	if err != nil {
		return "", 0, err
	}
	if !cfg.Enabled {
		return s.ImportCreateTask(ctx, tx, orgID, spaceID, actor, input)
	}
	task, err := s.CreateTask(ctx, tx, orgID, spaceID, actor, TaskInput{Title: input.Title, Description: input.Description,
		AssigneeUserID: input.AssigneeUserID, integrationSource: true})
	if err != nil {
		return "", 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.tasks SET created_at=COALESCE($2::timestamptz,created_at),
	    external_assignee_id=CASE WHEN assignee_id IS NULL THEN NULLIF($3,'')::uuid END
	    WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, task.ID, nullTime(input.CreatedAt), input.ExternalAssigneeID)
	if err != nil {
		return "", 0, err
	}
	err = newActivityWriter(tx, spaceID, task.ID, actor.UserID, "").add(ctx, "task.imported", map[string]any{"source": input.Source, "key": truncateRunes(input.SourceKey, 200)})
	return task.ID, int64(task.Version), err
}

// ImportCreateTask 导入创建一个任务：状态须在类型工作流内；rank 追加到末尾；不进分诊、不通知、不加关注者、不产生 Agent 事件。
func (s Service) ImportCreateTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input ImportTaskInput) (string, int64, error) {
	id, version, _, err := s.ImportCreateTaskEx(ctx, tx, organizationID, spaceID, actor, input)
	return id, version, err
}

// ImportCreateTaskEx 与 ImportCreateTask 相同，另外返回 numberConflict：指定的 Sequence 已被占用，已改用自动序号。
func (s Service) ImportCreateTaskEx(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input ImportTaskInput) (string, int64, bool, error) {
	id, version, conflict, err := s.importCreateTask(ctx, tx, organizationID, spaceID, actor, input)
	return id, version, conflict, err
}

func (s Service) importCreateTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input ImportTaskInput) (string, int64, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return "", 0, false, err
	}
	if !membership.ValidateUUID(spaceID) {
		return "", 0, false, ErrSpaceNotFound
	}
	priority, startDate, dueDate, err := s.importValidate(&input)
	if err != nil {
		return "", 0, false, err
	}
	if _, err := s.loadSpace(ctx, tx, organizationID, spaceID, actor.UserID, true); err != nil {
		return "", 0, false, err
	}
	if err := s.importCheckEstimate(ctx, tx, spaceID, input.Estimate); err != nil {
		return "", 0, false, err
	}
	typeID := input.TypeID
	if typeID == "" {
		if typeID, err = defaultTypeID(ctx, tx, organizationID, spaceID); err != nil {
			return "", 0, false, err
		}
	}
	if !membership.ValidateUUID(typeID) {
		return "", 0, false, ErrTaskTypeNotFound
	}
	flow, err := loadWorkflow(ctx, tx, organizationID, spaceID, typeID)
	if err != nil {
		return "", 0, false, err
	}
	statusID := input.StatusID
	if statusID == "" {
		statusID = flow.defaultStatusID
	}
	if !membership.ValidateUUID(statusID) || !flow.contains(statusID) {
		return "", 0, false, ErrInvalidWorkflow
	}
	var humans []TaskAssigneeRef
	if input.AssigneeUserID != "" {
		humans = []TaskAssigneeRef{{Kind: "HUMAN", ID: input.AssigneeUserID}}
		if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, humans); err != nil {
			return "", 0, false, err
		}
	}
	rank, err := resolveRank(ctx, tx, spaceID, "", "", "")
	if err != nil {
		return "", 0, false, err
	}
	if input.IterationID != "" {
		if err := importValidateIteration(ctx, tx, spaceID, input.IterationID); err != nil {
			return "", 0, false, err
		}
	}
	taskID, err := membership.NewID()
	if err != nil {
		return "", 0, false, err
	}
	conflict := false
	var version int64
	insertTask := func(q importQuerier, sequence int64) error {
		return q.QueryRow(ctx, `
			INSERT INTO openfox.tasks (
				organization_id, id, space_id, sequence, type_id, status_id, title, description, assignee_id,
				priority, start_date, due_date, rank, estimate, external_assignee_id, created_at, iteration_id
			) VALUES (
				openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4::uuid, $5::uuid, $6, $7,
				NULLIF($8, '')::uuid, $9, NULLIF($10, '')::date, NULLIF($11, '')::date, $12,
				round($13::float8::numeric, 2), NULLIF($14, '')::uuid, COALESCE($15::timestamptz, clock_timestamp()),
				NULLIF($16, '')::uuid
			) RETURNING version`, taskID, spaceID, sequence, typeID, statusID, input.Title, input.Description,
			input.AssigneeUserID, priority, startDate, dueDate, rank, input.Estimate, input.ExternalAssigneeID,
			nullTime(input.CreatedAt), input.IterationID).Scan(&version)
	}
	if input.Sequence > 0 {
		// 保留票号：在 SAVEPOINT 里直接用指定序号；撞号时回到自动序号，由调用方记告警。
		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", 0, false, err
		}
		err = insertTask(sp, input.Sequence)
		switch {
		case err == nil:
			if err := sp.Commit(ctx); err != nil {
				return "", 0, false, err
			}
		case isUniqueViolation(err):
			_ = sp.Rollback(ctx)
			conflict = true
		default:
			_ = sp.Rollback(ctx)
			return "", 0, false, mapDatabaseError(err)
		}
	}
	if input.Sequence <= 0 || conflict {
		sequence, err := nextTaskSequence(ctx, tx, organizationID, spaceID)
		if err != nil {
			return "", 0, false, err
		}
		if err := insertTask(tx, sequence); err != nil {
			return "", 0, false, mapDatabaseError(err)
		}
	}
	if len(humans) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)`,
			spaceID, taskID, input.AssigneeUserID, actor.UserID); err != nil {
			return "", 0, false, mapDatabaseError(err)
		}
	}
	if len(input.LabelIDs) > 0 {
		ids, err := normalizeLabelIDs(input.LabelIDs)
		if err != nil {
			return "", 0, false, err
		}
		if err := s.writeLabelSet(ctx, tx, newDiscardWriter(), spaceID, taskID, nil, ids); err != nil {
			return "", 0, false, err
		}
	}
	writer := newActivityWriter(tx, spaceID, taskID, actor.UserID, "")
	source := input.Source
	if source == "" {
		source = "jira"
	}
	if err := writer.add(ctx, "task.imported", map[string]any{"source": source, "key": truncateRunes(input.SourceKey, 200)}); err != nil {
		return "", 0, false, err
	}
	if err := s.importWriteComments(ctx, tx, spaceID, taskID, actor, input.Comments); err != nil {
		return "", 0, false, err
	}
	return taskID, version, conflict, nil
}

// ImportUpdateTask 覆盖导入：更新标题、描述、状态、优先级、日期与估点；来源有处理人时替换人员处理人（或改为外部账号）；
// 只补充缺少的标签；评论按 client_comment_id 去重。version + 1。
func (s Service) ImportUpdateTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, taskID string, input ImportTaskInput) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return 0, ErrTaskNotFound
	}
	priority, startDate, dueDate, err := s.importValidate(&input)
	if err != nil {
		return 0, err
	}
	var typeID string
	err = tx.QueryRow(ctx, `
		SELECT type_id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL FOR UPDATE`, spaceID, taskID).Scan(&typeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTaskNotFound
	}
	if err != nil {
		return 0, err
	}
	if err := s.importCheckEstimate(ctx, tx, spaceID, input.Estimate); err != nil {
		return 0, err
	}
	flow, err := loadWorkflow(ctx, tx, organizationID, spaceID, typeID)
	if err != nil {
		return 0, err
	}
	if input.StatusID != "" && (!membership.ValidateUUID(input.StatusID) || !flow.contains(input.StatusID)) {
		return 0, ErrInvalidWorkflow
	}
	if input.AssigneeUserID != "" {
		if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, []TaskAssigneeRef{{Kind: "HUMAN", ID: input.AssigneeUserID}}); err != nil {
			return 0, err
		}
	}
	if input.IterationID != "" {
		if err := importValidateIteration(ctx, tx, spaceID, input.IterationID); err != nil {
			return 0, err
		}
	}
	setAssignee := input.AssigneeUserID != "" || input.ExternalAssigneeID != ""
	if setAssignee {
		// 先删现有人员处理人，再写来源处理人；assignee_id 投影保持与 task_assignees 的第一个 HUMAN 一致。
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.task_assignees
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid AND kind = 'HUMAN'`, taskID); err != nil {
			return 0, err
		}
		if input.AssigneeUserID != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)`,
				spaceID, taskID, input.AssigneeUserID, actor.UserID); err != nil {
				return 0, mapDatabaseError(err)
			}
		}
	}
	var version int64
	err = tx.QueryRow(ctx, `
		UPDATE openfox.tasks
		   SET title = $3, description = $4, status_id = COALESCE(NULLIF($5, '')::uuid, status_id), priority = $6,
		       start_date = NULLIF($7, '')::date, due_date = NULLIF($8, '')::date,
		       estimate = round($9::float8::numeric, 2),
		       assignee_id = CASE WHEN $10::boolean THEN NULLIF($11, '')::uuid ELSE assignee_id END,
		       external_assignee_id = CASE WHEN $10::boolean THEN NULLIF($12, '')::uuid ELSE external_assignee_id END,
		       iteration_id = COALESCE(NULLIF($13, '')::uuid, iteration_id),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND space_id = $2::uuid
		RETURNING version`, taskID, spaceID, input.Title, input.Description, input.StatusID, priority,
		startDate, dueDate, input.Estimate, setAssignee, input.AssigneeUserID, input.ExternalAssigneeID, input.IterationID).Scan(&version)
	if err != nil {
		return 0, mapDatabaseError(err)
	}
	if len(input.LabelIDs) > 0 {
		ids, err := normalizeLabelIDs(input.LabelIDs)
		if err != nil {
			return 0, err
		}
		rows, err := tx.Query(ctx, `
			SELECT label_id::text FROM openfox.task_labels
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID)
		if err != nil {
			return 0, err
		}
		var current []TaskLabelItem
		have := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			current = append(current, TaskLabelItem{ID: id})
			have[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		desired := make([]string, 0, len(current)+len(ids))
		for _, item := range current {
			desired = append(desired, item.ID)
		}
		for _, id := range ids {
			if !have[id] {
				desired = append(desired, id)
			}
		}
		if len(desired) > len(current) {
			if err := s.writeLabelSet(ctx, tx, newDiscardWriter(), spaceID, taskID, current, desired); err != nil {
				return 0, err
			}
		}
	}
	writer := newActivityWriter(tx, spaceID, taskID, actor.UserID, "")
	if err := writer.add(ctx, "task.import_updated", map[string]any{"source": input.Source, "key": truncateRunes(input.SourceKey, 200)}); err != nil {
		return 0, err
	}
	if err := s.importWriteComments(ctx, tx, spaceID, taskID, actor, input.Comments); err != nil {
		return 0, err
	}
	return version, nil
}

// ImportSetParent 导入的父子关联：按 id 顺序锁住子任务与父任务，套用 checkParent 的同空间 / 环路 / 层级规则。
func (s Service) ImportSetParent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, parentTaskID string, actor Actor) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	if !membership.ValidateUUID(taskID) || !membership.ValidateUUID(parentTaskID) {
		return 0, errInvalidParent
	}
	if taskID == parentTaskID {
		return 0, errHierarchyCycle
	}
	ids := []string{taskID, parentTaskID}
	sort.Strings(ids)
	for _, id := range ids {
		var found string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM openfox.tasks
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL FOR UPDATE`, spaceID, id).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errInvalidParent
		}
		if err != nil {
			return 0, err
		}
	}
	if err := s.checkParent(ctx, tx, spaceID, taskID, parentTaskID); err != nil {
		return 0, err
	}
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE openfox.tasks SET parent_task_id = $3::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid
		RETURNING version`, spaceID, taskID, parentTaskID).Scan(&version)
	return version, mapDatabaseErrorOrNil(err)
}

func mapDatabaseErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return mapDatabaseError(err)
}

// CheckEstimate 校验估点是否符合空间估时刻度（导入前预检，失败时 worker 只记告警、不让整条失败）。
func (s Service) CheckEstimate(ctx context.Context, tx pgx.Tx, spaceID string, value float64) error {
	return s.importCheckEstimate(ctx, tx, spaceID, &value)
}
