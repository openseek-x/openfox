package collaboration

// 全局配置：任务状态的扩展能力（默认状态、折叠、跨类别重排、替换式删除、视图统计）。
//
// 并发约束：所有改变状态顺序 / 类别 / 默认 / 删除的操作都先按 id 顺序锁定组织内全部
// task_statuses 行（lockStatusRows），因此同一组织的重排被串行化，重编号总是基于最新数据，
// 同一类别内的 position 始终为连续的 0..n-1。

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

// StatusTypeUsage 描述某个空间里有哪些（未归档）任务类型的工作流包含该状态。
type StatusTypeUsage struct {
	SpaceID   string   `json:"space_id"`
	SpaceName string   `json:"space_name"`
	TypeIDs   []string `json:"type_ids"`
	TypeNames []string `json:"type_names"`
}

// StatusMoveInput 是跨类别 / 类别内移动状态的输入；BeforeStatusID 为空表示放到类别末尾。
type StatusMoveInput struct {
	Category       string
	BeforeStatusID *string
	Version        uint64
}

// OrganizationDefaultStatusID 返回当前组织的默认状态 ID：显式默认状态优先，
// 没有显式默认时取 TODO 类别中 position 最小的未归档状态。找不到时返回 ErrStatusNotFound。
// 创建任务未传 status_id 时由任务服务调用（调用方需已在租户事务内）。
func OrganizationDefaultStatusID(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND archived_at IS NULL AND (is_default OR category = 'TODO')
		 ORDER BY is_default DESC, position, id
		 LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrStatusNotFound
	}
	return id, err
}

// ListStatuses 返回带默认标记、任务数和类型使用情况的状态列表（按类别固定顺序再 position）。
func (s Service) ListStatuses(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) ([]TaskStatus, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	return s.listStatusViews(ctx, tx, actor, nil)
}

func (s Service) statusView(ctx context.Context, tx pgx.Tx, actor Actor, id string) (TaskStatus, error) {
	items, err := s.listStatusViews(ctx, tx, actor, []string{id})
	if err != nil {
		return TaskStatus{}, err
	}
	if len(items) == 0 {
		return TaskStatus{}, ErrStatusNotFound
	}
	return items[0], nil
}

// listStatusViews 固定三次查询：状态 + 任务数、类型使用情况、组织默认状态。ids 为空表示全部。
func (s Service) listStatusViews(ctx context.Context, tx pgx.Tx, actor Actor, ids []string) ([]TaskStatus, error) {
	rows, err := tx.Query(ctx, `
		SELECT ts.id::text, ts.name, ts.description, ts.category, ts.position, COALESCE(ts.color, ''),
		       COALESCE(ts.icon, ''), ts.archived_at, ts.version, ts.created_at,
		       ts.collapsed_by_default, COALESCE(c.task_count, 0), ts.updated_at
		  FROM openfox.task_statuses ts
		  LEFT JOIN (
		        SELECT t.status_id, count(*) AS task_count
		          FROM openfox.tasks t
		         WHERE t.organization_id = openfox_private.current_organization_id()
		           AND t.deleted_at IS NULL
		           AND ($1::uuid[] IS NULL OR t.status_id = ANY($1::uuid[]))
		         GROUP BY t.status_id
		       ) c ON c.status_id = ts.id
		 WHERE ts.organization_id = openfox_private.current_organization_id()
		   AND ($1::uuid[] IS NULL OR ts.id = ANY($1::uuid[]))
		 ORDER BY `+statusCategoryOrderSQL+`, ts.position, ts.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TaskStatus, 0)
	for rows.Next() {
		var item TaskStatus
		var version int64
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Category, &item.Position,
			&item.Color, &item.Icon, &item.ArchivedAt, &version, &item.CreatedAt,
			&item.CollapsedByDefault, &item.TaskCount, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if item.Version, err = positiveVersion(version); err != nil {
			return nil, err
		}
		item.TypeUsage = []StatusTypeUsage{}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(items) == 0 {
		return items, nil
	}

	usage, err := s.statusTypeUsage(ctx, tx, actor, ids)
	if err != nil {
		return nil, err
	}
	defaultID, err := OrganizationDefaultStatusID(ctx, tx)
	if err != nil && !errors.Is(err, ErrStatusNotFound) {
		return nil, err
	}
	for index := range items {
		if list, ok := usage[items[index].ID]; ok {
			items[index].TypeUsage = list
		}
		items[index].IsDefault = items[index].ID == defaultID
	}
	return items, nil
}

// statusTypeUsage 只统计调用者可读的 ACTIVE 空间里未归档的任务类型。
func (s Service) statusTypeUsage(ctx context.Context, tx pgx.Tx, actor Actor, ids []string) (map[string][]StatusTypeUsage, error) {
	rows, err := tx.Query(ctx, `
		SELECT ws.status_id::text, s.id::text, s.name, tt.id::text, tt.name
		  FROM openfox.workflow_statuses ws
		  JOIN openfox.task_types tt
		    ON tt.organization_id = ws.organization_id AND tt.id = ws.task_type_id AND tt.archived_at IS NULL
		  JOIN openfox.spaces s
		    ON s.organization_id = tt.organization_id AND s.id = tt.space_id AND s.status = 'ACTIVE'
		 WHERE ws.organization_id = openfox_private.current_organization_id()
		   AND ($2::uuid[] IS NULL OR ws.status_id = ANY($2::uuid[]))
		   AND `+SpaceReadableSQL+`
		 ORDER BY s.name, s.id, tt.created_at, tt.id`, strings.ToUpper(string(actor.Role)), ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]StatusTypeUsage)
	for rows.Next() {
		var statusID, spaceID, spaceName, typeID, typeName string
		if err := rows.Scan(&statusID, &spaceID, &spaceName, &typeID, &typeName); err != nil {
			return nil, err
		}
		list := result[statusID]
		// 行按空间排序，同一空间的类型相邻。
		if n := len(list); n > 0 && list[n-1].SpaceID == spaceID {
			list[n-1].TypeIDs = append(list[n-1].TypeIDs, typeID)
			list[n-1].TypeNames = append(list[n-1].TypeNames, typeName)
		} else {
			list = append(list, StatusTypeUsage{SpaceID: spaceID, SpaceName: spaceName,
				TypeIDs: []string{typeID}, TypeNames: []string{typeName}})
		}
		result[statusID] = list
	}
	return result, rows.Err()
}

type statusRow struct {
	ID        string
	Category  string
	Position  int
	Version   int64
	Archived  bool
	IsDefault bool
}

// lockStatusRows 按 id 顺序锁定并读取组织内全部状态行，保证同一组织的重排串行化。
func lockStatusRows(ctx context.Context, tx pgx.Tx) ([]statusRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, category, position, version, archived_at IS NOT NULL, is_default
		  FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id()
		 ORDER BY id
		   FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]statusRow, 0)
	for rows.Next() {
		var item statusRow
		if err := rows.Scan(&item.ID, &item.Category, &item.Position, &item.Version, &item.Archived, &item.IsDefault); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func findStatusRow(rows []statusRow, id string) *statusRow {
	for index := range rows {
		if rows[index].ID == id {
			return &rows[index]
		}
	}
	return nil
}

// categoryOrder 返回某类别内的状态 ID，按 (position, id) 排序，可排除一个 ID。
func categoryOrder(rows []statusRow, category, exclude string) []statusRow {
	list := make([]statusRow, 0)
	for _, row := range rows {
		if row.Category == category && row.ID != exclude {
			list = append(list, row)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Position != list[j].Position {
			return list[i].Position < list[j].Position
		}
		return list[i].ID < list[j].ID
	})
	return list
}

// renumberStatuses 把 ordered 里 position 与下标不一致的行写成 0..n-1。
func renumberStatuses(ctx context.Context, tx pgx.Tx, ordered []string, current map[string]int) error {
	ids := make([]string, 0)
	positions := make([]int32, 0)
	for index, id := range ordered {
		if current[id] != index {
			ids = append(ids, id)
			positions = append(positions, int32(index))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_statuses ts
		   SET position = v.position, updated_at = clock_timestamp()
		  FROM unnest($1::uuid[], $2::int[]) AS v(id, position)
		 WHERE ts.organization_id = openfox_private.current_organization_id() AND ts.id = v.id`,
		ids, positions)
	return err
}

// CreateStatus 创建组织级状态：未传 position 时追加到类别末尾；创建后追加到所有未归档任务类型的工作流。
// 返回的 bool 为 true 表示这是一次幂等重放。
func (s Service) CreateStatus(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input TaskStatusInput) (TaskStatus, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskStatus{}, false, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 100 {
		return TaskStatus{}, false, ErrInvalidName
	}
	description, color, icon, err := validateStatusDisplay(input.Description, input.Color, input.Icon)
	if err != nil {
		return TaskStatus{}, false, err
	}
	category, err := ParseCategory(input.Category)
	if err != nil {
		return TaskStatus{}, false, err
	}
	if input.Position != nil && *input.Position < 0 {
		return TaskStatus{}, false, ErrInvalidStatus
	}
	claim, err := idempotency.Begin(ctx, tx, "task_status.create", input.IdempotencyKey, struct {
		Name, Description, Category, Color, Icon string
		Position                                 *int
	}{name, description, category, color, icon, input.Position})
	if err != nil {
		return TaskStatus{}, false, err
	}
	if claim.Replay {
		view, err := s.statusView(ctx, tx, actor, claim.ResourceID)
		return view, true, err
	}

	rows, err := lockStatusRows(ctx, tx)
	if err != nil {
		return TaskStatus{}, false, err
	}
	if len(rows) >= MaxStatuses {
		return TaskStatus{}, false, ErrLimitReached
	}
	position := 0
	if input.Position != nil {
		position = *input.Position
	} else {
		for _, row := range rows {
			if row.Category == category && row.Position >= position {
				position = row.Position + 1
			}
		}
	}
	statusID, err := membership.NewID()
	if err != nil {
		return TaskStatus{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_statuses (
			organization_id, id, name, description, category, position, color, icon
		) VALUES (
			openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, '')
		)`, statusID, name, description, category, position, color, icon); err != nil {
		// 重名（task_statuses_name_unique）→ 409 NAME_TAKEN。
		return TaskStatus{}, false, configDatabaseError(err)
	}
	// 新状态自动加入组织内所有未归档任务类型的工作流（追加到末尾），并让类型版本前进，
	// 使持有旧版本的工作流编辑请求冲突，避免把新状态覆盖掉。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
		SELECT tt.organization_id, tt.space_id, tt.id, $1::uuid,
		       COALESCE((SELECT max(ws.position) + 1 FROM openfox.workflow_statuses ws
		                  WHERE ws.organization_id = tt.organization_id AND ws.task_type_id = tt.id), 0),
		       false
		  FROM openfox.task_types tt
		 WHERE tt.organization_id = openfox_private.current_organization_id() AND tt.archived_at IS NULL`,
		statusID); err != nil {
		return TaskStatus{}, false, mapDatabaseError(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_types SET version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND archived_at IS NULL`); err != nil {
		return TaskStatus{}, false, err
	}
	if err := idempotency.Complete(ctx, tx, claim, statusID); err != nil {
		return TaskStatus{}, false, err
	}
	view, err := s.statusView(ctx, tx, actor, statusID)
	return view, false, err
}

// MoveStatus 把状态移动到 category 的 before 之前（为空表示末尾），可跨类别。
// 跨类别时同事务更新该状态下全部任务的 tasks.status 投影（触发器在 status_id 被写入时重算）。
func (s Service) MoveStatus(ctx context.Context, tx pgx.Tx, organizationID, statusID string, actor Actor, input StatusMoveInput) ([]TaskStatus, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(statusID) {
		return nil, ErrStatusNotFound
	}
	category, err := ParseCategory(input.Category)
	if err != nil {
		return nil, invalidInput("invalid status category")
	}
	if input.Version == 0 {
		return nil, ErrInvalidVersion
	}
	before := ""
	if input.BeforeStatusID != nil {
		before = strings.ToLower(strings.TrimSpace(*input.BeforeStatusID))
		if !membership.ValidateUUID(before) || before == statusID {
			return nil, invalidInput("before_status_id is invalid")
		}
	}

	rows, err := lockStatusRows(ctx, tx)
	if err != nil {
		return nil, err
	}
	target := findStatusRow(rows, statusID)
	if target == nil {
		return nil, ErrStatusNotFound
	}
	if uint64(target.Version) != input.Version {
		return nil, ErrVersionConflict
	}
	oldCategory := target.Category
	categoryChanged := oldCategory != category
	if categoryChanged {
		if target.IsDefault && category != "BACKLOG" && category != "TODO" {
			return nil, ErrStatusDefaultCategory
		}
		if IsTerminalCategory(category) {
			var workflowDefault bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM openfox.workflow_statuses
					 WHERE organization_id = openfox_private.current_organization_id()
					   AND status_id = $1::uuid AND is_default)`, statusID).Scan(&workflowDefault); err != nil {
				return nil, err
			}
			if workflowDefault {
				return nil, ErrStatusIsWorkflowDefault
			}
		}
		var affected int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.tasks
			 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`,
			statusID).Scan(&affected); err != nil {
			return nil, err
		}
		if affected > MaxStatusMoveTasks {
			return nil, ErrStatusMoveTooLarge
		}
	}

	ordered := make([]string, 0)
	for _, row := range categoryOrder(rows, category, statusID) {
		ordered = append(ordered, row.ID)
	}
	insertAt := len(ordered)
	if before != "" {
		insertAt = -1
		for index, id := range ordered {
			if id == before {
				insertAt = index
				break
			}
		}
		if insertAt < 0 {
			return nil, invalidInput("before_status_id must belong to the target category")
		}
	}
	ordered = append(ordered[:insertAt], append([]string{statusID}, ordered[insertAt:]...)...)

	positions := make(map[string]int, len(rows))
	for _, row := range rows {
		positions[row.ID] = row.Position
	}
	// 目标行单独写（含 version + 1），其余行只改 position。
	targetPosition := insertAt
	if err := renumberAround(ctx, tx, ordered, statusID, positions); err != nil {
		return nil, err
	}

	if categoryChanged {
		oldOrdered := make([]string, 0)
		for _, row := range categoryOrder(rows, oldCategory, statusID) {
			oldOrdered = append(oldOrdered, row.ID)
		}
		if err := renumberStatuses(ctx, tx, oldOrdered, positions); err != nil {
			return nil, err
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.task_statuses
		   SET category = $2, position = $3, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $4`, statusID, category, targetPosition, int64(input.Version))
	if err != nil {
		return nil, mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return nil, ErrVersionConflict
	}
	if categoryChanged {
		// 写 status_id 会触发 sync_task_status_projection，按新类别重算 tasks.status。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks SET status_id = status_id
			 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`,
			statusID); err != nil {
			return nil, err
		}
	}
	return s.listStatusViews(ctx, tx, actor, nil)
}

// renumberAround 把 ordered（含目标）里除目标外 position 与下标不一致的行改成下标。
func renumberAround(ctx context.Context, tx pgx.Tx, ordered []string, skip string, current map[string]int) error {
	ids := make([]string, 0)
	positions := make([]int32, 0)
	for index, id := range ordered {
		if id == skip || current[id] == index {
			continue
		}
		ids = append(ids, id)
		positions = append(positions, int32(index))
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_statuses ts
		   SET position = v.position, updated_at = clock_timestamp()
		  FROM unnest($1::uuid[], $2::int[]) AS v(id, position)
		 WHERE ts.organization_id = openfox_private.current_organization_id() AND ts.id = v.id`,
		ids, positions)
	return err
}

// SetDefaultStatus 把状态设为组织默认状态：必须是未归档的 BACKLOG / TODO 状态。幂等：已是默认时不改版本。
func (s Service) SetDefaultStatus(ctx context.Context, tx pgx.Tx, organizationID, statusID string, actor Actor, version uint64) ([]TaskStatus, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(statusID) {
		return nil, ErrStatusNotFound
	}
	if version == 0 {
		return nil, ErrInvalidVersion
	}
	rows, err := lockStatusRows(ctx, tx)
	if err != nil {
		return nil, err
	}
	target := findStatusRow(rows, statusID)
	if target == nil {
		return nil, ErrStatusNotFound
	}
	if uint64(target.Version) != version {
		return nil, ErrVersionConflict
	}
	if target.Archived || (target.Category != "BACKLOG" && target.Category != "TODO") {
		return nil, ErrStatusDefaultCategory
	}
	if !target.IsDefault {
		// 先清旧默认再设新默认：task_statuses_default_unique 要求同一时刻至多一个。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_statuses
			   SET is_default = false, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND is_default`); err != nil {
			return nil, err
		}
		result, err := tx.Exec(ctx, `
			UPDATE openfox.task_statuses
			   SET is_default = true, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, statusID)
		if err != nil {
			return nil, mapDatabaseError(err)
		}
		if result.RowsAffected() != 1 {
			return nil, ErrStatusNotFound
		}
	}
	return s.listStatusViews(ctx, tx, actor, nil)
}

// DeleteStatus 删除状态。有任务（含软删除）使用时必须给出 replacementStatusID，
// 同事务把这些任务改到替换状态（version + 1），再删工作流行与状态行。
func (s Service) DeleteStatus(ctx context.Context, tx pgx.Tx, organizationID, statusID, replacementStatusID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(statusID) {
		return ErrStatusNotFound
	}
	replacementStatusID = strings.ToLower(strings.TrimSpace(replacementStatusID))
	if replacementStatusID != "" && (!membership.ValidateUUID(replacementStatusID) || replacementStatusID == statusID) {
		return invalidInput("replacement_status_id is invalid")
	}
	rows, err := lockStatusRows(ctx, tx)
	if err != nil {
		return err
	}
	target := findStatusRow(rows, statusID)
	if target == nil {
		return ErrStatusNotFound
	}
	if target.IsDefault {
		return ErrStatusDefaultCategory
	}
	var workflowDefault bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM openfox.workflow_statuses
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND status_id = $1::uuid AND is_default)`, statusID).Scan(&workflowDefault); err != nil {
		return err
	}
	if workflowDefault {
		return ErrStatusIsWorkflowDefault
	}
	if !target.Archived && len(categoryOrder(activeRows(rows), target.Category, statusID)) == 0 {
		return ErrStatusRequired
	}

	var taskCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`,
		statusID).Scan(&taskCount); err != nil {
		return err
	}
	if taskCount > 0 {
		if replacementStatusID == "" {
			return ErrStatusReplacementRequired
		}
		if taskCount > MaxStatusMoveTasks {
			return ErrStatusMoveTooLarge
		}
		replacement := findStatusRow(rows, replacementStatusID)
		if replacement == nil || replacement.Archived {
			return invalidInput("replacement status is unavailable")
		}
		var missing bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM openfox.tasks t
				 WHERE t.organization_id = openfox_private.current_organization_id()
				   AND t.status_id = $1::uuid
				   AND NOT EXISTS (
				       SELECT 1 FROM openfox.workflow_statuses ws
				        WHERE ws.organization_id = t.organization_id AND ws.task_type_id = t.type_id
				          AND ws.status_id = $2::uuid))`, statusID, replacementStatusID).Scan(&missing); err != nil {
			return err
		}
		if missing {
			return ErrStatusReplacementNotInWorkflow
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks
			   SET status_id = $2::uuid, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`,
			statusID, replacementStatusID); err != nil {
			return mapDatabaseError(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_types tt SET version = version + 1
		 WHERE tt.organization_id = openfox_private.current_organization_id()
		   AND EXISTS (
		       SELECT 1 FROM openfox.workflow_statuses ws
		        WHERE ws.organization_id = tt.organization_id AND ws.task_type_id = tt.id
		          AND ws.status_id = $1::uuid)`, statusID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.workflow_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`,
		statusID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, statusID)
	if err != nil {
		return mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrStatusNotFound
	}
	return nil
}

func activeRows(rows []statusRow) []statusRow {
	list := make([]statusRow, 0, len(rows))
	for _, row := range rows {
		if !row.Archived {
			list = append(list, row)
		}
	}
	return list
}
