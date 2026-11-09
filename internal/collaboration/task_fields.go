package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 任务上的自定义字段取值：只能写绑定到该任务类型且未归档的字段。

var errFieldNotBound = NewRuleError(400, "FIELD_NOT_BOUND", "field is not bound to the task type or is archived")

// TaskFieldValue 是任务详情里的一个字段及其取值；Bound=false 表示字段已解绑或归档但任务上仍有取值。
type TaskFieldValue struct {
	Field CustomField     `json:"field"`
	Value json.RawMessage `json:"value"`
	Bound bool            `json:"bound"`
}

func (s Service) taskTypeID(ctx context.Context, tx pgx.Tx, spaceID, taskID string) (string, error) {
	var typeID string
	err := tx.QueryRow(ctx, `
		SELECT type_id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL`, spaceID, taskID).Scan(&typeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	return typeID, err
}

func (s Service) boundFields(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID string, actor Actor) ([]CustomField, error) {
	return s.ListTypeFields(ctx, tx, organizationID, spaceID, typeID, actor)
}

// writeFieldValue 校验并写入（或清空）一个字段取值。
func (s Service) writeFieldValue(ctx context.Context, tx pgx.Tx, w *activityWriter, spaceID, taskID string, actor Actor, field CustomField, raw json.RawMessage) (json.RawMessage, time.Time, error) {
	optionIDs := make(map[string]bool, len(field.Options))
	for _, option := range field.Options {
		optionIDs[option.ID] = true
	}
	memberScope := ""
	if field.MemberScope != nil {
		memberScope = *field.MemberScope
	}
	value, ref, err := NormalizeFieldValue(field.Kind, memberScope, raw, optionIDs)
	if err != nil {
		return nil, time.Time{}, err
	}
	if ref != nil {
		if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, []TaskAssigneeRef{{Kind: ref.Kind, ID: ref.ID}}); err != nil {
			return nil, time.Time{}, errInvalidMemberValue
		}
	}
	var updatedAt time.Time
	if value == nil {
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.task_field_values
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND task_id = $1::uuid AND field_id = $2::uuid`, taskID, field.ID); err != nil {
			return nil, time.Time{}, err
		}
		updatedAt = time.Now().UTC()
	} else if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.task_field_values (organization_id, space_id, task_id, field_id, value, updated_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5::uuid)
		ON CONFLICT (organization_id, task_id, field_id) DO UPDATE
		   SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = clock_timestamp()
		RETURNING updated_at`, spaceID, taskID, field.ID, []byte(value), actor.UserID).Scan(&updatedAt); err != nil {
		return nil, time.Time{}, mapDatabaseError(err)
	}
	// 字段名随动态保存，字段之后改名或归档时时间线仍能显示当时的名称。
	if err := w.add(ctx, "task.field_changed", map[string]any{"field_id": field.ID, "field_name": field.Name}); err != nil {
		return nil, time.Time{}, err
	}
	return value, updatedAt, nil
}

var errInvalidMemberValue = NewRuleError(400, "INVALID_REQUEST", "value references an unavailable member")

// writeBoundFieldValues validates all submitted values against the current type bindings.
func (s Service) writeBoundFieldValues(ctx context.Context, tx pgx.Tx, w *activityWriter, organizationID, spaceID, taskID, typeID string, actor Actor, values map[string]json.RawMessage) error {
	if len(values) > 100 {
		return errFieldNotBound
	}
	bound, err := s.ListTypeFields(ctx, tx, organizationID, spaceID, typeID, actor)
	if err != nil {
		return err
	}
	byID := make(map[string]CustomField, len(bound))
	for _, field := range bound {
		byID[field.ID] = field
	}
	for fieldID, raw := range values {
		field, ok := byID[fieldID]
		if !ok {
			return errFieldNotBound
		}
		if _, _, err := s.writeFieldValue(ctx, tx, w, spaceID, taskID, actor, field, raw); err != nil {
			return err
		}
	}
	return nil
}

// ListTaskFields 返回任务类型绑定的字段（按 position）及取值，另附已解绑但仍有取值的字段。
func (s Service) ListTaskFields(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) ([]TaskFieldValue, error) {
	typeID, err := s.taskTypeID(ctx, tx, spaceID, taskID)
	if err != nil {
		return nil, err
	}
	bound, err := s.ListTypeFields(ctx, tx, organizationID, spaceID, typeID, actor)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT field_id::text, value FROM openfox.task_field_values
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]json.RawMessage)
	for rows.Next() {
		var id string
		var value []byte
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		values[id] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	items := make([]TaskFieldValue, 0, len(bound)+len(values))
	boundIDs := make(map[string]bool, len(bound))
	for _, field := range bound {
		boundIDs[field.ID] = true
		items = append(items, TaskFieldValue{Field: field, Value: values[field.ID], Bound: true})
	}
	var orphanIDs []string
	for id := range values {
		if !boundIDs[id] {
			orphanIDs = append(orphanIDs, id)
		}
	}
	if len(orphanIDs) > 0 {
		orphans, err := s.customFieldsQuery(ctx, tx, `AND cf.id = ANY($1::uuid[])`, orphanIDs)
		if err != nil {
			return nil, err
		}
		for _, field := range orphans {
			items = append(items, TaskFieldValue{Field: field, Value: values[field.ID], Bound: false})
		}
	}
	return items, nil
}

func (s Service) boundFieldFor(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, fieldID string, actor Actor) (CustomField, error) {
	if !membership.ValidateUUID(fieldID) {
		return CustomField{}, errFieldNotBound
	}
	typeID, err := s.taskTypeID(ctx, tx, spaceID, taskID)
	if err != nil {
		return CustomField{}, err
	}
	bound, err := s.ListTypeFields(ctx, tx, organizationID, spaceID, typeID, actor)
	if err != nil {
		return CustomField{}, err
	}
	for _, field := range bound {
		if field.ID == fieldID {
			return field, nil
		}
	}
	return CustomField{}, errFieldNotBound
}

// SetTaskField 写入字段取值（null 或空数组清空）。
func (s Service) SetTaskField(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, fieldID string, actor Actor, raw json.RawMessage, requestID string) (json.RawMessage, time.Time, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, time.Time{}, err
	}
	field, err := s.boundFieldFor(ctx, tx, organizationID, spaceID, taskID, fieldID, actor)
	if err != nil {
		return nil, time.Time{}, err
	}
	return s.writeFieldValue(ctx, tx, newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID), spaceID, taskID, actor, field, raw)
}

// ClearTaskField 删除字段取值；字段已解绑时也允许清理残留取值。
func (s Service) ClearTaskField(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, fieldID string, actor Actor, requestID string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(fieldID) {
		return errFieldNotBound
	}
	if _, err := s.taskTypeID(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_field_values
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND field_id = $2::uuid`, taskID, fieldID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	var fieldName string
	if err := tx.QueryRow(ctx, `
		SELECT name FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, fieldID).Scan(&fieldName); err != nil {
		return err
	}
	return newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.field_changed", map[string]any{"field_id": fieldID, "field_name": fieldName})
}
