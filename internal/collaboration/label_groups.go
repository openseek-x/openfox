package collaboration

// 全局配置：标签分组（界面称「单选分组」）。
// 起分组分两种范围：组织级（space_id 为空）与空间级；组内标签必须与分组同范围，由写入事务校验
// （迁移 000033 删除了 labels_group_organization_only）。分组名称仍在整个组织内唯一（label_groups_name_unique）。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

type LabelGroup struct {
	ID          string    `json:"id"`
	SpaceID     *string   `json:"space_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Color       string    `json:"color"`
	LabelCount  int64     `json:"label_count"`
	Version     uint64    `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LabelGroupInput struct {
	// SpaceID 为空 = 组织级分组。
	SpaceID        string
	Name           string
	Description    string
	Color          string
	LabelIDs       []string
	IdempotencyKey string
}

type LabelGroupUpdateInput struct {
	Name        *string
	Description *string
	Color       *string
	Version     uint64
}

const labelGroupSelectSQL = `
	SELECT g.id::text, g.space_id::text, g.name, g.description, g.color, COALESCE(c.label_count, 0),
	       g.version, g.created_at, g.updated_at
	  FROM openfox.label_groups g
	  LEFT JOIN (
	        SELECT l.group_id, count(*) AS label_count
	          FROM openfox.labels l
	         WHERE l.organization_id = openfox_private.current_organization_id() AND l.group_id IS NOT NULL
	         GROUP BY l.group_id
	       ) c ON c.group_id = g.id
	 WHERE g.organization_id = openfox_private.current_organization_id()`

func scanLabelGroup(row interface{ Scan(...any) error }) (LabelGroup, error) {
	var item LabelGroup
	var version int64
	if err := row.Scan(&item.ID, &item.SpaceID, &item.Name, &item.Description, &item.Color, &item.LabelCount,
		&version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return LabelGroup{}, err
	}
	var err error
	item.Version, err = positiveVersion(version)
	return item, err
}

// ListLabelGroups 列出分组：spaceID 为空 = 组织级分组，否则为该空间的分组（接口层已校验空间可读）。
func (s Service) ListLabelGroups(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string) ([]LabelGroup, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if spaceID != "" && !membership.ValidateUUID(spaceID) {
		return nil, ErrSpaceNotFound
	}
	rows, err := tx.Query(ctx, labelGroupSelectSQL+` AND g.space_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		ORDER BY g.created_at, g.id LIMIT $1`, MaxLabelGroups, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LabelGroup, 0)
	for rows.Next() {
		item, err := scanLabelGroup(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) GetLabelGroup(ctx context.Context, tx pgx.Tx, organizationID, groupID string, actor Actor) (LabelGroup, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return LabelGroup{}, err
	}
	return s.getLabelGroup(ctx, tx, groupID)
}

func (s Service) getLabelGroup(ctx context.Context, tx pgx.Tx, groupID string) (LabelGroup, error) {
	if !membership.ValidateUUID(groupID) {
		return LabelGroup{}, configNotFound("label group not found")
	}
	item, err := scanLabelGroup(tx.QueryRow(ctx, labelGroupSelectSQL+` AND g.id = $1::uuid`, groupID))
	if errors.Is(err, pgx.ErrNoRows) {
		return LabelGroup{}, configNotFound("label group not found")
	}
	return item, err
}

// CreateLabelGroup 创建分组；LabelIDs 把这些组织级标签移入新分组（用于删除后的撤销）。
// 返回的 bool 为 true 表示幂等重放。
func (s Service) CreateLabelGroup(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input LabelGroupInput) (LabelGroup, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return LabelGroup{}, false, err
	}
	name, err := normalizeConfigName(input.Name, 50)
	if err != nil {
		return LabelGroup{}, false, err
	}
	description, err := validateDescription(input.Description)
	if err != nil {
		return LabelGroup{}, false, err
	}
	if err := validateColor(input.Color); err != nil {
		return LabelGroup{}, false, err
	}
	labelIDs, err := uniqueUUIDs(input.LabelIDs, MaxLabels)
	if err != nil {
		return LabelGroup{}, false, err
	}
	spaceID := strings.ToLower(strings.TrimSpace(input.SpaceID))
	if spaceID != "" && !membership.ValidateUUID(spaceID) {
		return LabelGroup{}, false, ErrSpaceNotFound
	}
	claim, err := idempotency.Begin(ctx, tx, "label_group.create", input.IdempotencyKey, struct {
		Name, Description, Color, SpaceID string
		LabelIDs                          []string
	}{name, description, input.Color, spaceID, labelIDs})
	if err != nil {
		return LabelGroup{}, false, err
	}
	if claim.Replay {
		item, err := s.getLabelGroup(ctx, tx, claim.ResourceID)
		return item, true, err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id IS NOT DISTINCT FROM NULLIF($1, '')::uuid`, spaceID).Scan(&count); err != nil {
		return LabelGroup{}, false, err
	}
	if count >= MaxLabelGroups {
		return LabelGroup{}, false, ErrLimitReached
	}
	groupID, err := membership.NewID()
	if err != nil {
		return LabelGroup{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.label_groups (organization_id, id, name, description, color, created_by, space_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5::uuid, NULLIF($6, '')::uuid)`,
		groupID, name, description, input.Color, actor.UserID, spaceID); err != nil {
		return LabelGroup{}, false, configDatabaseError(err)
	}
	if len(labelIDs) > 0 {
		result, err := tx.Exec(ctx, `
			UPDATE openfox.labels
			   SET group_id = $1::uuid, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = ANY($2::uuid[]) AND space_id IS NOT DISTINCT FROM NULLIF($3, '')::uuid`, groupID, labelIDs, spaceID)
		if err != nil {
			return LabelGroup{}, false, configDatabaseError(err)
		}
		if int(result.RowsAffected()) != len(labelIDs) {
			return LabelGroup{}, false, invalidInput("label_ids must all belong to the group scope")
		}
	}
	if err := idempotency.Complete(ctx, tx, claim, groupID); err != nil {
		return LabelGroup{}, false, err
	}
	item, err := s.getLabelGroup(ctx, tx, groupID)
	return item, false, err
}

func (s Service) UpdateLabelGroup(ctx context.Context, tx pgx.Tx, organizationID, groupID string, actor Actor, input LabelGroupUpdateInput) (LabelGroup, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return LabelGroup{}, err
	}
	if !membership.ValidateUUID(groupID) {
		return LabelGroup{}, configNotFound("label group not found")
	}
	if input.Version == 0 {
		return LabelGroup{}, ErrInvalidVersion
	}
	var name, description, color *string
	if input.Name != nil {
		value, err := normalizeConfigName(*input.Name, 50)
		if err != nil {
			return LabelGroup{}, err
		}
		name = &value
	}
	if input.Description != nil {
		value, err := validateDescription(*input.Description)
		if err != nil {
			return LabelGroup{}, err
		}
		description = &value
	}
	if input.Color != nil {
		if err := validateColor(*input.Color); err != nil {
			return LabelGroup{}, err
		}
		color = input.Color
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.label_groups
		   SET name = COALESCE($2, name), description = COALESCE($3, description),
		       color = COALESCE($4, color), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $5`, groupID, name, description, color, int64(input.Version))
	if err != nil {
		return LabelGroup{}, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return LabelGroup{}, versionOrNotFound(ctx, tx, "openfox.label_groups", groupID, "label group not found")
	}
	return s.getLabelGroup(ctx, tx, groupID)
}

// DeleteLabelGroup 把组内标签移出分组（version + 1）后删除分组，返回被移出的标签 ID。
func (s Service) DeleteLabelGroup(ctx context.Context, tx pgx.Tx, organizationID, groupID string, actor Actor) ([]string, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(groupID) {
		return nil, configNotFound("label group not found")
	}
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   FOR UPDATE`, groupID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, configNotFound("label group not found")
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		UPDATE openfox.labels
		   SET group_id = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND group_id = $1::uuid
		RETURNING id::text`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	removed := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		removed = append(removed, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, groupID)
	if err != nil {
		return nil, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return nil, configNotFound("label group not found")
	}
	return removed, nil
}

// LabelGroupSpaceID 返回分组所属空间（组织级分组为 nil），供接口层决定需要哪种权限。
func (s Service) LabelGroupSpaceID(ctx context.Context, tx pgx.Tx, organizationID, groupID string, actor Actor) (*string, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(groupID) {
		return nil, configNotFound("label group not found")
	}
	var spaceID *string
	err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, groupID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, configNotFound("label group not found")
	}
	return spaceID, err
}

// requireGroupScope 以 FOR SHARE 锁定分组并校验它与标签同范围（组织级标签只能进组织分组，空间标签只能进同一空间的分组）。
// 锁住分组行可防止并发删除分组后插入悬空引用（外键也会兜底）。
func requireGroupScope(ctx context.Context, tx pgx.Tx, groupID string, labelSpaceID *string) error {
	var groupSpace *string
	err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   FOR SHARE`, groupID).Scan(&groupSpace)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidInput("group_id is invalid")
	}
	if err != nil {
		return err
	}
	if !sameLabelScope(groupSpace, labelSpaceID) {
		return invalidInput("labels must belong to a group of the same scope")
	}
	return nil
}
