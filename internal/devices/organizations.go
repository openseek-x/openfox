package devices

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// 一台电脑共享给多个组织（阶段 12e，D-09）。
//
// 已定产品决策（R-16）：只有电脑所有者本人可以把电脑加入另一个组织；可选的组织是所有者
// 「自己是 ACTIVE 成员、且在该组织有接入电脑权限（非 GUEST）」的组织，不需要对方组织管理员另行确认。
// 新授权一律是 PERSONAL（只授权给所有者本人），所有者之后可以按既有规则改为组织或空间共享。
// 对方组织 Owner / Admin 可以用既有的撤销 / 移出接口在本组织内移除这台电脑的授权。

// OrganizationStore 是多组织管理的存储（PostgresStore 实现；单测替身可以不实现，API 返回 501）。
type OrganizationStore interface {
	ListDeviceOrganizations(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) (DeviceOrganizations, error)
	JoinOrganization(ctx context.Context, target tenancy.OrganizationSelection, role, deviceID string) (Device, bool, error)
	DeviceActiveOrganizations(ctx context.Context, deviceID string) ([]string, error)
}

// DeviceOrganization 是「允许的组织」列表的一行。
type DeviceOrganization struct {
	OrganizationID string  `json:"organization_id"`
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	GrantStatus    *string `json:"grant_status"`
	GrantScope     *string `json:"grant_scope"`
	Current        bool    `json:"current"`
	// RejoinBlocked：该组织的 Owner/Admin 移除过这台电脑且尚未重新允许；所有者不能自行加回。
	RejoinBlocked bool `json:"rejoin_blocked"`
	// CanJoin：所有者在该组织不是访客、电脑尚未在该组织 ACTIVE，且没有被该组织管理员移除
	// （所有者本人是该组织 Owner/Admin 时不受此限）。
	CanJoin bool `json:"can_join"`
	// CanRevoke：查看者在该组织是 OWNER / ADMIN、且授权 ACTIVE（既有撤销接口的权限）。
	CanRevoke bool `json:"can_revoke"`
	// CanRemove：授权 ACTIVE 且查看者是电脑所有者或该组织 OWNER / ADMIN（既有移出接口的权限）。
	CanRemove bool `json:"can_remove"`
}

type DeviceOrganizations struct {
	// Owner 为 true 时列表包含所有者本人的全部 ACTIVE 组织；否则只有当前组织一行。
	Owner         bool                 `json:"owner"`
	Organizations []DeviceOrganization `json:"organizations"`
}

func (s PostgresStore) ListDeviceOrganizations(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) (DeviceOrganizations, error) {
	if s.Pool == nil {
		return DeviceOrganizations{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return DeviceOrganizations{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return DeviceOrganizations{}, ErrDeviceNotFound
	}
	result := DeviceOrganizations{Organizations: []DeviceOrganization{}}
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var ownerID, grantStatus, grantScope, slug, name string
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(d.owner_user_id::text, ''), g.status, g.scope, o.slug, o.name
			  FROM openfox.device_grants g
			  JOIN openfox.physical_devices d ON d.id = g.device_id
			  JOIN openfox.organizations o ON o.id = g.organization_id
			 WHERE g.organization_id = $2::uuid AND g.device_id = $3::uuid AND g.status = 'ACTIVE'
			   AND `+deviceVisibleSQL, viewer.role(), selection.OrganizationID, deviceID).
			Scan(&ownerID, &grantStatus, &grantScope, &slug, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeviceNotFound
		}
		if err != nil {
			return err
		}
		owner := ownerID != "" && ownerID == selection.ActorID
		result.Owner = owner
		if !owner {
			result.Organizations = append(result.Organizations, DeviceOrganization{
				OrganizationID: selection.OrganizationID, Slug: slug, Name: name, Role: viewer.role(),
				GrantStatus: &grantStatus, GrantScope: &grantScope, Current: true,
				CanRevoke: viewer.manager(), CanRemove: viewer.manager(),
			})
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, slug, name, role, grant_status, grant_scope, rejoin_blocked
			  FROM openfox_private.list_device_owner_organizations($1::uuid, $2::uuid)`, deviceID, selection.ActorID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item DeviceOrganization
			if err := rows.Scan(&item.OrganizationID, &item.Slug, &item.Name, &item.Role, &item.GrantStatus, &item.GrantScope, &item.RejoinBlocked); err != nil {
				return err
			}
			role := strings.ToUpper(strings.TrimSpace(item.Role))
			item.Role = role
			active := item.GrantStatus != nil && *item.GrantStatus == "ACTIVE"
			manager := role == "OWNER" || role == "ADMIN"
			item.Current = item.OrganizationID == selection.OrganizationID
			item.CanJoin = !active && role != "GUEST" && (!item.RejoinBlocked || manager)
			item.CanRevoke = active && manager
			item.CanRemove = active
			result.Organizations = append(result.Organizations, item)
		}
		return rows.Err()
	})
	if err != nil {
		return DeviceOrganizations{}, err
	}
	return result, nil
}

// JoinOrganization 把所有者的电脑加入 target 组织（target 是所有者在目标组织的已认证选择，role 为其角色）。
// 已经 ACTIVE 时幂等返回（第二个返回值 false）；曾被撤销或移出的授权重新激活为 PERSONAL。
func (s PostgresStore) JoinOrganization(ctx context.Context, target tenancy.OrganizationSelection, role, deviceID string) (Device, bool, error) {
	if s.Pool == nil {
		return Device{}, false, postgres.ErrNilPool
	}
	if err := validateSelection(target); err != nil {
		return Device{}, false, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return Device{}, false, ErrDeviceNotFound
	}
	if strings.EqualFold(strings.TrimSpace(role), "GUEST") {
		return Device{}, false, ErrForbidden
	}
	var result Device
	created := false
	err = s.runForSelection(ctx, target, func(ctx context.Context, tx pgx.Tx) error {
		var ownerID string
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(owner_user_id::text, '') FROM openfox.physical_devices
			 WHERE id = $1::uuid AND status = 'ACTIVE'
			 FOR UPDATE`, deviceID).Scan(&ownerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeviceNotFound
		}
		if err != nil {
			return err
		}
		if ownerID == "" || ownerID != target.ActorID {
			// 不暴露别人电脑是否存在：非所有者一律按“没有这台电脑”处理。
			return ErrDeviceNotFound
		}
		var status string
		var blocked bool
		err = tx.QueryRow(ctx, `
			SELECT status, removed_by_manager_at IS NOT NULL FROM openfox.device_grants
			 WHERE organization_id = $1::uuid AND device_id = $2::uuid
			 FOR UPDATE`, target.OrganizationID, deviceID).Scan(&status, &blocked)
		switch {
		case err == nil && status == "ACTIVE":
		case err == nil && blocked && !managerRole(role):
			// 该组织的 Owner/Admin 移除过这台电脑：所有者不能自行加回，需管理员先「重新允许」。
			return ErrRejoinBlocked
		case err == nil:
			tag, err := tx.Exec(ctx, `
				UPDATE openfox.device_grants
				   SET status = 'ACTIVE', scope = 'PERSONAL', scope_user_id = $3::uuid, scope_space_id = NULL,
				       removed_by_manager_at = NULL,
				       permission_version = permission_version + 1
				 WHERE organization_id = $1::uuid AND device_id = $2::uuid AND status <> 'ACTIVE'`,
				target.OrganizationID, deviceID, target.ActorID)
			if err != nil {
				return err
			}
			created = tag.RowsAffected() == 1
		case errors.Is(err, pgx.ErrNoRows):
			tag, err := tx.Exec(ctx, `
				INSERT INTO openfox.device_grants (organization_id, device_id, scope, scope_user_id)
				VALUES ($1::uuid, $2::uuid, 'PERSONAL', $3::uuid)
				ON CONFLICT (organization_id, device_id) DO NOTHING`, target.OrganizationID, deviceID, target.ActorID)
			if err != nil {
				return err
			}
			created = tag.RowsAffected() == 1
		default:
			return err
		}
		if created {
			if err := recordAudit(ctx, tx, target, deviceID, "device.organization_joined", map[string]string{
				"status": "ACTIVE", "grant_scope": string(ScopePersonal),
			}); err != nil {
				return err
			}
		}
		result, err = loadDevice(ctx, tx, target.OrganizationID, deviceID)
		return err
	})
	if err != nil {
		return Device{}, false, err
	}
	return result, created, nil
}

// DeviceActiveOrganizations 返回电脑 ACTIVE 授权的组织（设备侧，daemon 多组织心跳与领取用）。
func (s PostgresStore) DeviceActiveOrganizations(ctx context.Context, deviceID string) ([]string, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	organizations := make([]string, 0, 2)
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text FROM openfox_private.list_device_active_organizations($1::uuid)`, deviceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			organizations = append(organizations, id)
		}
		return rows.Err()
	})
	return organizations, err
}

// ErrRejoinBlocked 表示目标组织的 Owner/Admin 移除过这台电脑，所有者不能自行加回。
var ErrRejoinBlocked = errors.New("devices: the organization's managers removed this computer")

func managerRole(role string) bool {
	role = strings.ToUpper(strings.TrimSpace(role))
	return role == "OWNER" || role == "ADMIN"
}

// RemovedDevice 是「被本组织管理员移除、尚未重新允许」的一台电脑。
type RemovedDevice struct {
	DeviceID   string    `json:"device_id"`
	Name       string    `json:"name"`
	OwnerEmail *string   `json:"owner_email"`
	RemovedAt  time.Time `json:"removed_at"`
}

// RejoinStore 是「被管理员移除后重新允许」的存储（PostgresStore 实现；单测替身可以不实现，API 返回 501）。
type RejoinStore interface {
	ListRemovedDevices(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer) ([]RemovedDevice, error)
	AllowRejoin(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) error
}

// ListRemovedDevices 列出被本组织 Owner/Admin 移除、尚未重新允许的电脑（仅 Owner/Admin；最多 100 台，最近移除的在前）。
func (s PostgresStore) ListRemovedDevices(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer) ([]RemovedDevice, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return nil, ErrInvalidRequest
	}
	if !viewer.manager() {
		return nil, ErrForbidden
	}
	result := make([]RemovedDevice, 0)
	err := s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id::text, d.name, u.email, g.removed_by_manager_at
			  FROM openfox.device_grants g
			  JOIN openfox.physical_devices d ON d.id = g.device_id
			  LEFT JOIN openfox.users u ON u.id = d.owner_user_id
			 WHERE g.organization_id = openfox_private.current_organization_id()
			   AND g.status <> 'ACTIVE' AND g.removed_by_manager_at IS NOT NULL AND d.status = 'ACTIVE'
			 ORDER BY g.removed_by_manager_at DESC, d.id
			 LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RemovedDevice
			if err := rows.Scan(&item.DeviceID, &item.Name, &item.OwnerEmail, &item.RemovedAt); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

// AllowRejoin 清除「被管理员移除」标记（仅 Owner/Admin）：之后电脑所有者可以再把电脑加入本组织；不会自动加入。
// 没有该标记（从未被管理员移除、已重新允许、或已重新加入）返回 ErrDeviceNotFound。
func (s PostgresStore) AllowRejoin(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return ErrDeviceNotFound
	}
	if !viewer.manager() {
		return ErrForbidden
	}
	return s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.device_grants
			   SET removed_by_manager_at = NULL
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND status <> 'ACTIVE' AND removed_by_manager_at IS NOT NULL`, deviceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeviceNotFound
		}
		return recordAudit(ctx, tx, selection, deviceID, "device.rejoin_allowed", map[string]string{"status": "REVOKED"})
	})
}
