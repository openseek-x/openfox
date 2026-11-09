package collaboration

// 全局配置：组织级 / 空间级标签（含合并与批量）。
// 权限由接口层判定（组织级需管理权限，空间标签需该空间的对象写权限）；这里只做数据规则。
// 合并时不做单选分组互斥校验。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

const (
	LabelScopeOrganization = "organization"
	LabelScopeArchived     = "archived"
	LabelScopePicker       = "picker"
	// 空间标签管理页。
	LabelScopeSpace         = "space"
	LabelScopeSpaceArchived = "space_archived"

	LabelBulkArchive   = "ARCHIVE"
	LabelBulkUnarchive = "UNARCHIVE"
	LabelBulkDelete    = "DELETE"
	LabelBulkMerge     = "MERGE"
)

type Label struct {
	ID            string     `json:"id"`
	SpaceID       *string    `json:"space_id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Color         *string    `json:"color"`
	GroupID       *string    `json:"group_id"`
	ArchivedAt    *time.Time `json:"archived_at"`
	TaskCount     int64      `json:"task_count"`
	LastAppliedAt *time.Time `json:"last_applied_at"`
	Version       uint64     `json:"version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type LabelInput struct {
	Name           string
	Description    string
	Color          string
	GroupID        string
	SpaceID        string
	IdempotencyKey string
}

type LabelUpdateInput struct {
	Name        *string
	Description *string
	Color       *string
	GroupID     OptionalString
	Archived    *bool
	Version     uint64
}

// labelQuerySQL 返回标签查询；usageFilter 限定使用统计子查询的范围，where 追加到主查询。
// task_count / last_applied_at 只统计未删除任务，由一条聚合子查询 LEFT JOIN 得到。
func labelQuerySQL(usageFilter, where string) string {
	return `
	SELECT l.id::text, l.space_id::text, l.name, l.description, l.color, l.group_id::text, l.archived_at,
	       COALESCE(u.task_count, 0), u.last_applied_at, l.version, l.created_at, l.updated_at
	  FROM openfox.labels l
	  LEFT JOIN (
	        SELECT tl.label_id, count(*) AS task_count, max(tl.created_at) AS last_applied_at
	          FROM openfox.task_labels tl
	          JOIN openfox.tasks t
	            ON t.organization_id = tl.organization_id AND t.id = tl.task_id AND t.deleted_at IS NULL
	         WHERE tl.organization_id = openfox_private.current_organization_id() ` + usageFilter + `
	         GROUP BY tl.label_id
	       ) u ON u.label_id = l.id
	 WHERE l.organization_id = openfox_private.current_organization_id() ` + where
}

func scanLabel(row interface{ Scan(...any) error }) (Label, error) {
	var item Label
	var version int64
	if err := row.Scan(&item.ID, &item.SpaceID, &item.Name, &item.Description, &item.Color, &item.GroupID,
		&item.ArchivedAt, &item.TaskCount, &item.LastAppliedAt, &version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Label{}, err
	}
	var err error
	item.Version, err = positiveVersion(version)
	return item, err
}

// ListLabels 按 scope 列出标签；picker 需要 spaceID（接口层已校验空间可读）。
func (s Service) ListLabels(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, scope, spaceID string) ([]Label, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	var where string
	var args []any
	switch scope {
	case LabelScopeOrganization, "":
		where = `AND l.space_id IS NULL AND l.archived_at IS NULL`
	case LabelScopeArchived:
		where = `AND l.space_id IS NULL AND l.archived_at IS NOT NULL`
	case LabelScopePicker:
		if !membership.ValidateUUID(spaceID) {
			return nil, invalidInput("space_id is required for the picker scope")
		}
		where = `AND l.archived_at IS NULL AND (l.space_id IS NULL OR l.space_id = $1::uuid)`
		args = append(args, spaceID)
	case LabelScopeSpace, LabelScopeSpaceArchived:
		if !membership.ValidateUUID(spaceID) {
			return nil, invalidInput("space_id is required for the space scopes")
		}
		where = `AND l.space_id = $1::uuid AND l.archived_at IS NULL`
		if scope == LabelScopeSpaceArchived {
			where = `AND l.space_id = $1::uuid AND l.archived_at IS NOT NULL`
		}
		args = append(args, spaceID)
	default:
		return nil, invalidInput("scope must be organization, archived, picker, space or space_archived")
	}
	rows, err := tx.Query(ctx, labelQuerySQL("", where)+` ORDER BY l.created_at, l.id LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Label, 0)
	for rows.Next() {
		item, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) GetLabel(ctx context.Context, tx pgx.Tx, organizationID, labelID string, actor Actor) (Label, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Label{}, err
	}
	return s.getLabel(ctx, tx, labelID)
}

func (s Service) getLabel(ctx context.Context, tx pgx.Tx, labelID string) (Label, error) {
	if !membership.ValidateUUID(labelID) {
		return Label{}, configNotFound("label not found")
	}
	item, err := scanLabel(tx.QueryRow(ctx,
		labelQuerySQL(`AND tl.label_id = $1::uuid`, `AND l.id = $1::uuid`), labelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Label{}, configNotFound("label not found")
	}
	return item, err
}

// LabelSpaceID 返回标签所属空间（组织级标签为 nil），供接口层决定需要哪种权限。
func (s Service) LabelSpaceID(ctx context.Context, tx pgx.Tx, organizationID, labelID string, actor Actor) (*string, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(labelID) {
		return nil, configNotFound("label not found")
	}
	var spaceID *string
	err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		labelID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, configNotFound("label not found")
	}
	return spaceID, err
}

// CreateLabel 创建标签：SpaceID 为空 = 组织级，否则为空间标签；GroupID 须为同范围的分组。
// 返回的 bool 为 true 表示幂等重放。
func (s Service) CreateLabel(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input LabelInput) (Label, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Label{}, false, err
	}
	name, err := normalizeConfigName(input.Name, 50)
	if err != nil {
		return Label{}, false, err
	}
	description, err := validateDescription(input.Description)
	if err != nil {
		return Label{}, false, err
	}
	if input.Color != "" {
		if err := validateColor(input.Color); err != nil {
			return Label{}, false, err
		}
	}
	spaceID := strings.ToLower(strings.TrimSpace(input.SpaceID))
	groupID := strings.ToLower(strings.TrimSpace(input.GroupID))
	if spaceID != "" && !membership.ValidateUUID(spaceID) {
		return Label{}, false, invalidInput("space_id is invalid")
	}
	if groupID != "" && !membership.ValidateUUID(groupID) {
		return Label{}, false, invalidInput("group_id is invalid")
	}
	claim, err := idempotency.Begin(ctx, tx, "label.create", input.IdempotencyKey, struct {
		Name, Description, Color, GroupID, SpaceID string
	}{name, description, input.Color, groupID, spaceID})
	if err != nil {
		return Label{}, false, err
	}
	if claim.Replay {
		item, err := s.getLabel(ctx, tx, claim.ResourceID)
		return item, true, err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id IS NOT DISTINCT FROM NULLIF($1, '')::uuid`, spaceID).Scan(&count); err != nil {
		return Label{}, false, err
	}
	if count >= MaxLabels {
		return Label{}, false, ErrLimitReached
	}
	if groupID != "" {
		var labelSpace *string
		if spaceID != "" {
			labelSpace = &spaceID
		}
		if err := requireGroupScope(ctx, tx, groupID, labelSpace); err != nil {
			return Label{}, false, err
		}
	}
	labelID, err := membership.NewID()
	if err != nil {
		return Label{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.labels (organization_id, space_id, id, name, description, color, group_id)
		VALUES (openfox_private.current_organization_id(), NULLIF($1, '')::uuid, $2::uuid, $3, $4,
		        NULLIF($5, ''), NULLIF($6, '')::uuid)`,
		spaceID, labelID, name, description, input.Color, groupID); err != nil {
		return Label{}, false, configDatabaseError(err)
	}
	if err := idempotency.Complete(ctx, tx, claim, labelID); err != nil {
		return Label{}, false, err
	}
	item, err := s.getLabel(ctx, tx, labelID)
	return item, false, err
}

func (s Service) UpdateLabel(ctx context.Context, tx pgx.Tx, organizationID, labelID string, actor Actor, input LabelUpdateInput) (Label, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Label{}, err
	}
	if !membership.ValidateUUID(labelID) {
		return Label{}, configNotFound("label not found")
	}
	if input.Version == 0 {
		return Label{}, ErrInvalidVersion
	}
	var name, description, color *string
	if input.Name != nil {
		value, err := normalizeConfigName(*input.Name, 50)
		if err != nil {
			return Label{}, err
		}
		name = &value
	}
	if input.Description != nil {
		value, err := validateDescription(*input.Description)
		if err != nil {
			return Label{}, err
		}
		description = &value
	}
	if input.Color != nil {
		if err := validateColor(*input.Color); err != nil {
			return Label{}, err
		}
		color = input.Color
	}
	var groupID *string
	if input.GroupID.Set && input.GroupID.Value != nil {
		value := strings.ToLower(strings.TrimSpace(*input.GroupID.Value))
		if !membership.ValidateUUID(value) {
			return Label{}, invalidInput("group_id is invalid")
		}
		groupID = &value
	}

	var currentSpace *string
	var currentVersion int64
	var currentArchived bool
	if err := tx.QueryRow(ctx, `
		SELECT space_id::text, version, archived_at IS NOT NULL FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   FOR UPDATE`, labelID).Scan(&currentSpace, &currentVersion, &currentArchived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Label{}, configNotFound("label not found")
		}
		return Label{}, err
	}
	if uint64(currentVersion) != input.Version {
		return Label{}, ErrVersionConflict
	}
	// 分组须与标签同范围；空间标签也可以进同一空间的分组、可以归档。
	if groupID != nil {
		if err := requireGroupScope(ctx, tx, *groupID, currentSpace); err != nil {
			return Label{}, err
		}
	}
	// 归档状态：未传保持原样。
	archived := currentArchived
	if input.Archived != nil {
		archived = *input.Archived
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.labels
		   SET name = COALESCE($2, name), description = COALESCE($3, description),
		       color = COALESCE($4, color),
		       group_id = CASE WHEN $5::boolean THEN $6::uuid ELSE group_id END,
		       archived_at = CASE WHEN $7::boolean THEN COALESCE(archived_at, clock_timestamp()) ELSE NULL END,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $8`,
		labelID, name, description, color, input.GroupID.Set, groupID, archived, int64(input.Version))
	if err != nil {
		return Label{}, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return Label{}, ErrVersionConflict
	}
	return s.getLabel(ctx, tx, labelID)
}

// DeleteLabel 先删任务关联再删标签（同事务）。
func (s Service) DeleteLabel(ctx context.Context, tx pgx.Tx, organizationID, labelID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(labelID) {
		return configNotFound("label not found")
	}
	if _, err := s.lockLabels(ctx, tx, []string{labelID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_labels
		 WHERE organization_id = openfox_private.current_organization_id() AND label_id = $1::uuid`, labelID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, labelID)
	if err != nil {
		return configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return configNotFound("label not found")
	}
	return nil
}

type lockedLabel struct {
	ID       string
	SpaceID  *string
	Archived bool
}

// lockLabels 按 id 顺序锁定标签；任何一个不存在都返回 404。
func (s Service) lockLabels(ctx context.Context, tx pgx.Tx, ids []string) (map[string]lockedLabel, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, space_id::text, archived_at IS NOT NULL FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])
		 ORDER BY id
		   FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]lockedLabel, len(ids))
	for rows.Next() {
		var item lockedLabel
		if err := rows.Scan(&item.ID, &item.SpaceID, &item.Archived); err != nil {
			return nil, err
		}
		result[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, configNotFound("label not found")
	}
	return result, nil
}

func sameLabelScope(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// MergeLabel 把 source 合并进 target 并删除 source；两者须同为组织级或同一空间，target 须未归档。
func (s Service) MergeLabel(ctx context.Context, tx pgx.Tx, organizationID, sourceID, targetID string, actor Actor) (Label, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Label{}, err
	}
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	if !membership.ValidateUUID(sourceID) {
		return Label{}, configNotFound("label not found")
	}
	if !membership.ValidateUUID(targetID) || targetID == sourceID {
		return Label{}, invalidInput("target_label_id must be a different label")
	}
	locked, err := s.lockLabels(ctx, tx, []string{sourceID, targetID})
	if err != nil {
		return Label{}, err
	}
	source, target := locked[sourceID], locked[targetID]
	if !sameLabelScope(source.SpaceID, target.SpaceID) {
		return Label{}, invalidInput("labels must belong to the same scope")
	}
	if target.Archived {
		return Label{}, invalidInput("the target label is archived")
	}
	if err := mergeLabelsInto(ctx, tx, []string{sourceID}, targetID); err != nil {
		return Label{}, err
	}
	return s.getLabel(ctx, tx, targetID)
}

// mergeLabelsInto 转移任务关联（重复的忽略）并删除来源标签；调用方已锁定这些标签。
func mergeLabelsInto(ctx context.Context, tx pgx.Tx, sourceIDs []string, targetID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_labels (organization_id, space_id, task_id, label_id, created_at)
		SELECT tl.organization_id, tl.space_id, tl.task_id, $2::uuid, tl.created_at
		  FROM openfox.task_labels tl
		 WHERE tl.organization_id = openfox_private.current_organization_id()
		   AND tl.label_id = ANY($1::uuid[])
		ON CONFLICT DO NOTHING`, sourceIDs, targetID); err != nil {
		return configDatabaseError(err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_labels
		 WHERE organization_id = openfox_private.current_organization_id() AND label_id = ANY($1::uuid[])`,
		sourceIDs); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, sourceIDs)
	if err != nil {
		return configDatabaseError(err)
	}
	if int(result.RowsAffected()) != len(sourceIDs) {
		return configNotFound("label not found")
	}
	return nil
}

// BulkLabels 批量处理一个范围内的标签（spaceID 为空 = 组织级，否则为该空间标签），返回实际受影响的标签数。
// ARCHIVE / UNARCHIVE / DELETE 忽略不存在或不在该范围的标签；MERGE 要求目标是同范围的未归档标签。
func (s Service) BulkLabels(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, action string, labelIDs []string, targetID, spaceID string) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	spaceID = strings.ToLower(strings.TrimSpace(spaceID))
	if spaceID != "" && !membership.ValidateUUID(spaceID) {
		return 0, ErrSpaceNotFound
	}
	ids, err := uniqueUUIDs(labelIDs, MaxBulkLabels)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, invalidInput("label_ids must contain 1 to 100 ids")
	}
	// 先按 id 顺序锁定所有涉及的组织级标签，避免与并发的合并 / 删除交叉死锁。
	lockIDs := append([]string(nil), ids...)
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	if action == LabelBulkMerge {
		if !membership.ValidateUUID(targetID) {
			return 0, invalidInput("target_label_id is required for MERGE")
		}
		lockIDs = append(lockIDs, targetID)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, archived_at IS NOT NULL FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND space_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		 ORDER BY id
		   FOR UPDATE`, lockIDs, spaceID)
	if err != nil {
		return 0, err
	}
	present := make(map[string]bool)
	targetArchived := false
	for rows.Next() {
		var id string
		var archived bool
		if err := rows.Scan(&id, &archived); err != nil {
			rows.Close()
			return 0, err
		}
		present[id] = true
		if id == targetID {
			targetArchived = archived
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	existing := make([]string, 0, len(ids))
	for _, id := range ids {
		if present[id] {
			existing = append(existing, id)
		}
	}

	switch action {
	case LabelBulkArchive, LabelBulkUnarchive:
		archive := action == LabelBulkArchive
		result, err := tx.Exec(ctx, `
			UPDATE openfox.labels
			   SET archived_at = CASE WHEN $2::boolean THEN clock_timestamp() ELSE NULL END,
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = ANY($1::uuid[]) AND (archived_at IS NOT NULL) <> $2::boolean`, existing, archive)
		if err != nil {
			return 0, configDatabaseError(err)
		}
		return result.RowsAffected(), nil
	case LabelBulkDelete:
		if len(existing) == 0 {
			return 0, nil
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.task_labels
			 WHERE organization_id = openfox_private.current_organization_id() AND label_id = ANY($1::uuid[])`,
			existing); err != nil {
			return 0, err
		}
		result, err := tx.Exec(ctx, `
			DELETE FROM openfox.labels
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = ANY($1::uuid[])`, existing)
		if err != nil {
			return 0, configDatabaseError(err)
		}
		return result.RowsAffected(), nil
	case LabelBulkMerge:
		if !present[targetID] {
			return 0, configNotFound("target label not found")
		}
		if targetArchived {
			return 0, invalidInput("the target label is archived")
		}
		sources := make([]string, 0, len(existing))
		for _, id := range existing {
			if id != targetID {
				sources = append(sources, id)
			}
		}
		if len(sources) == 0 {
			return 0, nil
		}
		if err := mergeLabelsInto(ctx, tx, sources, targetID); err != nil {
			return 0, err
		}
		return int64(len(sources)), nil
	default:
		return 0, invalidInput("action must be ARCHIVE, UNARCHIVE, DELETE or MERGE")
	}
}
