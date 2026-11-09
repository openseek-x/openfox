package collaboration

// 全局配置：任务关联类型。内置的 4 个类型是代码常量（不建行），自定义类型存 task_relation_types。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

const builtinRelationPrefix = "builtin:"

// BuiltinRelationKeys 是内置关联类型的固定顺序；RELATES 是对称类型。
var BuiltinRelationKeys = []string{"BLOCKS", "CLONES", "DUPLICATES", "RELATES"}

// RelationType 的内置项 name / outward_label / inward_label 为空串，由前端按 key 做多语言渲染。
type RelationType struct {
	ID           string     `json:"id"`
	Builtin      bool       `json:"builtin"`
	Key          *string    `json:"key"`
	Name         string     `json:"name"`
	OutwardLabel string     `json:"outward_label"`
	InwardLabel  string     `json:"inward_label"`
	Symmetric    bool       `json:"symmetric"`
	Version      *uint64    `json:"version"`
	CreatedAt    *time.Time `json:"created_at"`
}

type RelationTypeInput struct {
	Name           string
	OutwardLabel   string
	InwardLabel    string
	IdempotencyKey string
}

type RelationTypeUpdateInput struct {
	Name         *string
	OutwardLabel *string
	InwardLabel  *string
	Version      uint64
}

// IsBuiltinRelationKey 判断 key 是否为内置关联类型。
func IsBuiltinRelationKey(key string) bool {
	for _, candidate := range BuiltinRelationKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

func builtinRelationTypes() []RelationType {
	items := make([]RelationType, 0, len(BuiltinRelationKeys))
	for _, key := range BuiltinRelationKeys {
		key := key
		items = append(items, RelationType{ID: builtinRelationPrefix + key, Builtin: true, Key: &key,
			Symmetric: key == "RELATES"})
	}
	return items
}

// classifyRelationTypeID 区分内置 id（返回 builtin=true）与自定义 uuid。
func classifyRelationTypeID(id string) (builtin bool, err error) {
	if strings.HasPrefix(id, builtinRelationPrefix) {
		if IsBuiltinRelationKey(strings.TrimPrefix(id, builtinRelationPrefix)) {
			return true, nil
		}
		return false, configNotFound("relation type not found")
	}
	if !membership.ValidateUUID(id) {
		return false, configNotFound("relation type not found")
	}
	return false, nil
}

const relationTypeSelectSQL = `
	SELECT id::text, name, outward_label, inward_label, version, created_at
	  FROM openfox.task_relation_types
	 WHERE organization_id = openfox_private.current_organization_id()`

func scanRelationType(row interface{ Scan(...any) error }) (RelationType, error) {
	var item RelationType
	var version int64
	var createdAt time.Time
	if err := row.Scan(&item.ID, &item.Name, &item.OutwardLabel, &item.InwardLabel, &version, &createdAt); err != nil {
		return RelationType{}, err
	}
	number, err := positiveVersion(version)
	if err != nil {
		return RelationType{}, err
	}
	item.Version = &number
	item.CreatedAt = &createdAt
	return item, nil
}

func (s Service) ListRelationTypes(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) ([]RelationType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	items := builtinRelationTypes()
	rows, err := tx.Query(ctx, relationTypeSelectSQL+` ORDER BY created_at, id LIMIT $1`, MaxRelationTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanRelationType(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) getRelationType(ctx context.Context, tx pgx.Tx, id string) (RelationType, error) {
	builtin, err := classifyRelationTypeID(id)
	if err != nil {
		return RelationType{}, err
	}
	if builtin {
		for _, item := range builtinRelationTypes() {
			if item.ID == id {
				return item, nil
			}
		}
	}
	item, err := scanRelationType(tx.QueryRow(ctx, relationTypeSelectSQL+` AND id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RelationType{}, configNotFound("relation type not found")
	}
	return item, err
}

func normalizeRelationTexts(name, outward, inward string) (string, string, string, error) {
	name, err := normalizeConfigName(name, 50)
	if err != nil {
		return "", "", "", err
	}
	outward, err = normalizeConfigName(outward, 50)
	if err != nil {
		return "", "", "", err
	}
	inward, err = normalizeConfigName(inward, 50)
	if err != nil {
		return "", "", "", err
	}
	return name, outward, inward, nil
}

// CreateRelationType 创建自定义关联类型；返回的 bool 为 true 表示幂等重放。
func (s Service) CreateRelationType(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input RelationTypeInput) (RelationType, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return RelationType{}, false, err
	}
	name, outward, inward, err := normalizeRelationTexts(input.Name, input.OutwardLabel, input.InwardLabel)
	if err != nil {
		return RelationType{}, false, err
	}
	claim, err := idempotency.Begin(ctx, tx, "relation_type.create", input.IdempotencyKey, struct {
		Name, OutwardLabel, InwardLabel string
	}{name, outward, inward})
	if err != nil {
		return RelationType{}, false, err
	}
	if claim.Replay {
		item, err := s.getRelationType(ctx, tx, claim.ResourceID)
		return item, true, err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.task_relation_types
		 WHERE organization_id = openfox_private.current_organization_id()`).Scan(&count); err != nil {
		return RelationType{}, false, err
	}
	if count >= MaxRelationTypes {
		return RelationType{}, false, ErrLimitReached
	}
	id, err := membership.NewID()
	if err != nil {
		return RelationType{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_relation_types (organization_id, id, name, outward_label, inward_label, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5::uuid)`,
		id, name, outward, inward, actor.UserID); err != nil {
		return RelationType{}, false, configDatabaseError(err)
	}
	if err := idempotency.Complete(ctx, tx, claim, id); err != nil {
		return RelationType{}, false, err
	}
	item, err := s.getRelationType(ctx, tx, id)
	return item, false, err
}

func (s Service) UpdateRelationType(ctx context.Context, tx pgx.Tx, organizationID, id string, actor Actor, input RelationTypeUpdateInput) (RelationType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return RelationType{}, err
	}
	builtin, err := classifyRelationTypeID(id)
	if err != nil {
		return RelationType{}, err
	}
	if builtin {
		return RelationType{}, ErrRelationTypeBuiltin
	}
	if input.Version == 0 {
		return RelationType{}, ErrInvalidVersion
	}
	var name, outward, inward *string
	for _, field := range []struct {
		source *string
		target **string
	}{{input.Name, &name}, {input.OutwardLabel, &outward}, {input.InwardLabel, &inward}} {
		if field.source == nil {
			continue
		}
		value, err := normalizeConfigName(*field.source, 50)
		if err != nil {
			return RelationType{}, err
		}
		*field.target = &value
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.task_relation_types
		   SET name = COALESCE($2, name), outward_label = COALESCE($3, outward_label),
		       inward_label = COALESCE($4, inward_label), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $5`, id, name, outward, inward, int64(input.Version))
	if err != nil {
		return RelationType{}, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return RelationType{}, versionOrNotFound(ctx, tx, "openfox.task_relation_types", id, "relation type not found")
	}
	return s.getRelationType(ctx, tx, id)
}

// DeleteRelationType 同事务删除使用该类型的任务关联后删除类型。
func (s Service) DeleteRelationType(ctx context.Context, tx pgx.Tx, organizationID, id string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	builtin, err := classifyRelationTypeID(id)
	if err != nil {
		return err
	}
	if builtin {
		return ErrRelationTypeBuiltin
	}
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_relation_types
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   FOR UPDATE`, id).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configNotFound("relation type not found")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_relations
		 WHERE organization_id = openfox_private.current_organization_id() AND relation_type_id = $1::uuid`, id); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_relation_types
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id)
	if err != nil {
		return configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return configNotFound("relation type not found")
	}
	return nil
}
