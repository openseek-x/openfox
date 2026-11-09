package skills

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/devices"
	"openfox/internal/membership"
)

// 从 Runtime 导入：命令 + 每个技能一行 skill_import_items。进度读取时惰性处理过期 / 失败的命令。

const (
	ItemPending  = "pending"
	ItemImported = "imported"
	ItemSkipped  = "skipped"
	ItemFailed   = "failed"
	ItemCanceled = "canceled"
)

// InsertImportItems 为新建的 skill_import 命令写入 pending 项（顺序即 position）。
func InsertImportItems(ctx context.Context, tx pgx.Tx, commandID, spaceID string, names []string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.skill_import_items (organization_id, command_id, space_id, position, name)
		SELECT openfox_private.current_organization_id(), $1::uuid, $2::uuid, t.ord - 1, t.name
		  FROM unnest($3::text[]) WITH ORDINALITY AS t(name, ord)`, commandID, spaceID, names)
	return err
}

type importCommand struct {
	ID       string
	DeviceID string
	Status   string
}

// loadImportCommand 读取发起人自己的导入命令，并对已过期的未完成命令标记 EXPIRED。找不到（或不是发起人 / 空间不符）返回 ErrNotFound。
func loadImportCommand(ctx context.Context, tx pgx.Tx, userID, spaceID, commandID string) (importCommand, error) {
	if !membership.ValidateUUID(commandID) {
		return importCommand{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.device_commands
		   SET status = 'EXPIRED', completed_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND kind = 'skill_import' AND status IN ('PENDING', 'DELIVERED') AND expires_at <= clock_timestamp()`, commandID); err != nil {
		return importCommand{}, err
	}
	var command importCommand
	err := tx.QueryRow(ctx, `
		SELECT id::text, device_id::text, status FROM openfox.device_commands
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND kind = 'skill_import' AND requested_by_user_id = $2::uuid AND params->>'space_id' = $3`,
		commandID, userID, spaceID).Scan(&command.ID, &command.DeviceID, &command.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return importCommand{}, ErrNotFound
	}
	return command, err
}

// settleImport 命令已结束（过期、失败，或成功但仍有未上传的项）时，把剩余 pending 项改为 failed。
func settleImport(ctx context.Context, tx pgx.Tx, command importCommand) error {
	var reason string
	switch command.Status {
	case devices.CommandExpired:
		reason = "COMMAND_EXPIRED"
	case devices.CommandFailed:
		reason = "COMMAND_FAILED"
	case devices.CommandSucceeded:
		reason = "UPLOAD_FAILED"
	default:
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.skill_import_items SET status = 'failed', reason = $2, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND command_id = $1::uuid AND status = 'pending'`,
		command.ID, reason)
	return err
}

func listImportItems(ctx context.Context, tx pgx.Tx, commandID string) ([]ImportItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT name, status, reason, skill_id::text FROM openfox.skill_import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND command_id = $1::uuid
		 ORDER BY position`, commandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ImportItem{}
	for rows.Next() {
		var item ImportItem
		if err := rows.Scan(&item.Name, &item.Status, &item.Reason, &item.SkillID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ImportProgress 汇总导入状态：还有 pending 项为 running；有被取消的项为 canceled；否则 done。
func ImportProgress(commandID, deviceID string, items []ImportItem) RuntimeImport {
	status := "done"
	for _, item := range items {
		if item.Status == ItemPending {
			status = "running"
			break
		}
		if item.Status == ItemCanceled {
			status = "canceled"
		}
	}
	return RuntimeImport{CommandID: commandID, DeviceID: deviceID, Status: status, Items: items}
}

// LoadImport 返回导入进度（只有发起人可读），并惰性处理过期 / 失败的命令。
func LoadImport(ctx context.Context, tx pgx.Tx, userID, spaceID, commandID string) (RuntimeImport, error) {
	command, err := loadImportCommand(ctx, tx, userID, spaceID, commandID)
	if err != nil {
		return RuntimeImport{}, err
	}
	if err := settleImport(ctx, tx, command); err != nil {
		return RuntimeImport{}, err
	}
	items, err := listImportItems(ctx, tx, command.ID)
	if err != nil {
		return RuntimeImport{}, err
	}
	return ImportProgress(command.ID, command.DeviceID, items), nil
}

// CancelImport 取消导入：pending 项改为 canceled（幂等）。电脑之后的上传会被拒绝。
func CancelImport(ctx context.Context, tx pgx.Tx, userID, spaceID, commandID string) (RuntimeImport, error) {
	command, err := loadImportCommand(ctx, tx, userID, spaceID, commandID)
	if err != nil {
		return RuntimeImport{}, err
	}
	if err := settleImport(ctx, tx, command); err != nil {
		return RuntimeImport{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.skill_import_items SET status = 'canceled', updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND command_id = $1::uuid AND status = 'pending'`,
		command.ID); err != nil {
		return RuntimeImport{}, err
	}
	items, err := listImportItems(ctx, tx, command.ID)
	if err != nil {
		return RuntimeImport{}, err
	}
	return ImportProgress(command.ID, command.DeviceID, items), nil
}

// ActiveImport 是设备上传时锁定的导入命令。
type ActiveImport struct {
	ID          string
	SpaceID     string
	Names       []string
	RequestedBy string
}

// LockActiveImport 锁定本设备的 skill_import 命令：不存在 → ErrNotFound；不是未过期的 DELIVERED → ErrImportNotOpen。
func LockActiveImport(ctx context.Context, tx pgx.Tx, deviceID, commandID string) (ActiveImport, error) {
	if !membership.ValidateUUID(commandID) {
		return ActiveImport{}, ErrNotFound
	}
	var active ActiveImport
	var status string
	var params []byte
	var live bool
	err := tx.QueryRow(ctx, `
		SELECT id::text, status, params, requested_by_user_id::text, expires_at > clock_timestamp()
		  FROM openfox.device_commands
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND device_id = $1::uuid AND id = $2::uuid AND kind = 'skill_import'
		 FOR UPDATE`, deviceID, commandID).Scan(&active.ID, &status, &params, &active.RequestedBy, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActiveImport{}, ErrNotFound
	}
	if err != nil {
		return ActiveImport{}, err
	}
	if status != devices.CommandDelivered || !live {
		return ActiveImport{}, ErrImportNotOpen
	}
	var parsed devices.SkillImportParams
	if err := json.Unmarshal(params, &parsed); err != nil {
		return ActiveImport{}, err
	}
	active.SpaceID, active.Names = parsed.SpaceID, parsed.Names
	return active, nil
}

// LockImportItem 锁定一个导入项。
func LockImportItem(ctx context.Context, tx pgx.Tx, commandID, name string) (ImportItem, error) {
	var item ImportItem
	err := tx.QueryRow(ctx, `
		SELECT name, status, reason, skill_id::text FROM openfox.skill_import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND command_id = $1::uuid AND name = $2
		 FOR UPDATE`, commandID, name).Scan(&item.Name, &item.Status, &item.Reason, &item.SkillID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportItem{}, ErrImportName
	}
	return item, err
}

// FinishImportItem 把一个 pending 项记为终态。
func FinishImportItem(ctx context.Context, tx pgx.Tx, commandID, name, status, reason string, skillID *string) (ImportItem, error) {
	item := ImportItem{Name: name, Status: status, Reason: reason, SkillID: skillID}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.skill_import_items
		   SET status = $3, reason = $4, skill_id = $5::uuid, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND command_id = $1::uuid AND name = $2`,
		commandID, name, status, reason, skillID)
	return item, err
}
