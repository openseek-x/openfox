package collaboration

// 空间成员：列表筛选、添加 / 改角色、移出 / 离开。
// 权限由接口层判定；这里做数据规则：最后一个 OWNER、唯一可读空间、目标须为组织 ACTIVE 成员。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	ErrLastOwner = NewRuleError(http.StatusConflict, "LAST_OWNER", "the space must keep at least one active owner")
	ErrOnlySpace = NewRuleError(http.StatusConflict, "ONLY_SPACE", "this is the member's only active space")
	// errOwnerGrantForbidden：只有组织 OWNER / ADMIN 能授予空间 OWNER。
	errOwnerGrantForbidden = NewRuleError(http.StatusForbidden, "ACTION_FORBIDDEN", "only organization owners and admins can grant OWNER")
)

// SpaceMemberDetail 对应契约 SpaceMemberDetail。
type SpaceMemberDetail struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	OrgRole   *string   `json:"org_role"`
	OnlySpace bool      `json:"only_space"`
}

// otherReadableSpaceSQL 判断目标用户（om 为其组织成员行）除本空间（spaceParam）外是否还有可读的活跃空间。
// 规则与 SpaceReadableSQL 一致，但用目标用户自己的组织角色（om.role）。
func otherReadableSpaceSQL(spaceParam string) string {
	return `EXISTS (
	SELECT 1 FROM openfox.spaces os
	 WHERE os.organization_id = om.organization_id AND os.id <> ` + spaceParam + `::uuid
	   AND os.status = 'ACTIVE' AND os.deleted_at IS NULL
	   AND (EXISTS (SELECT 1 FROM openfox.space_members x
	                 WHERE x.organization_id = os.organization_id AND x.space_id = os.id
	                   AND x.user_id = om.user_id AND x.status = 'ACTIVE')
	        OR (os.visibility = 'SHARED' AND om.role <> 'GUEST' AND NOT EXISTS (
	                 SELECT 1 FROM openfox.space_members x
	                  WHERE x.organization_id = os.organization_id AND x.space_id = os.id
	                    AND x.user_id = om.user_id AND x.status <> 'ACTIVE'))))`
}

// ListSpaceMembersDetail 列出空间显式成员（含已撤销），按加入时间；q 按邮箱包含过滤，role 按角色过滤。
func (s Service) ListSpaceMembersDetail(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, query, role string) ([]SpaceMemberDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(spaceID) {
		return nil, ErrSpaceNotFound
	}
	query = strings.TrimSpace(query)
	if len([]rune(query)) > 100 {
		return nil, invalidInput("q is too long")
	}
	if role != "" {
		parsed, err := parseSpaceMemberRole(role)
		if err != nil || parsed != strings.ToUpper(strings.TrimSpace(role)) {
			return nil, invalidInput("role must be OWNER, EDITOR, MEMBER or VIEWER")
		}
		role = parsed
	}
	pattern := ""
	if query != "" {
		pattern = "%" + escapeLike(query) + "%"
	}
	rows, err := tx.Query(ctx, `
		SELECT sm.user_id::text, u.email, sm.role, sm.status, sm.created_at,
		       CASE WHEN om.status = 'ACTIVE' THEN om.role END,
		       COALESCE(om.status = 'ACTIVE' AND sm.status = 'ACTIVE' AND NOT `+otherReadableSpaceSQL("$1")+`, false)
		  FROM openfox.space_members sm
		  JOIN openfox.users u ON u.id = sm.user_id
		  LEFT JOIN openfox.organization_members om
		    ON om.organization_id = sm.organization_id AND om.user_id = sm.user_id
		 WHERE sm.organization_id = openfox_private.current_organization_id() AND sm.space_id = $1::uuid
		   AND ($2 = '' OR u.email ILIKE $2)
		   AND ($3 = '' OR sm.role = $3)
		 ORDER BY sm.created_at, sm.user_id
		 LIMIT 2000`, spaceID, pattern, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SpaceMemberDetail, 0)
	for rows.Next() {
		var item SpaceMemberDetail
		if err := rows.Scan(&item.UserID, &item.Email, &item.Role, &item.Status, &item.CreatedAt, &item.OrgRole,
			&item.OnlySpace); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SelfJoinAllowed 判断操作人能否不经管理权限自助加入：对自己、SHARED 空间、非 GUEST、role = MEMBER，
// 且没有被撤销的显式成员关系（被撤销的人需要管理员重新添加）。
func SelfJoinAllowed(access SpaceAccess, actor Actor, userID, role, status string) bool {
	if !strings.EqualFold(userID, actor.UserID) || actor.Role == policy.OrganizationRoleGuest {
		return false
	}
	if !strings.EqualFold(access.Space.Visibility, "SHARED") {
		return false
	}
	if strings.ToUpper(strings.TrimSpace(role)) != "MEMBER" {
		return false
	}
	if status := strings.ToUpper(strings.TrimSpace(status)); status != "" && status != "ACTIVE" {
		return false
	}
	return access.Membership == nil
}

// lockSpaceOwners 锁定空间的全部 ACTIVE OWNER 行并返回其用户 ID；并发降级 / 移出 OWNER 在这里串行化。
func lockSpaceOwners(ctx context.Context, tx pgx.Tx, spaceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT user_id::text FROM openfox.space_members
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND role = 'OWNER' AND status = 'ACTIVE'
		 ORDER BY user_id
		   FOR UPDATE`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owners = append(owners, id)
	}
	return owners, rows.Err()
}

func onlyActiveOwner(owners []string, userID string) bool {
	return len(owners) == 1 && strings.EqualFold(owners[0], userID)
}

// requireOtherSpace 在目标用户离开后没有任何可读的活跃空间时返回 ErrOnlySpace。
func requireOtherSpace(ctx context.Context, tx pgx.Tx, spaceID, userID string) error {
	var hasOther bool
	err := tx.QueryRow(ctx, `
		SELECT `+otherReadableSpaceSQL("$2")+`
		  FROM openfox.organization_members om
		 WHERE om.organization_id = openfox_private.current_organization_id() AND om.user_id = $1::uuid`,
		userID, spaceID).Scan(&hasOther)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !hasOther {
		return ErrOnlySpace
	}
	return nil
}

// UpsertSpaceMemberDetail 添加成员或修改角色 / 状态。只有组织 OWNER / ADMIN 能授予 OWNER（自助加入由接口层限定为 MEMBER）。
func (s Service) UpsertSpaceMemberDetail(ctx context.Context, tx pgx.Tx, organizationID, spaceID, userID string, actor Actor, input SpaceMemberInput) (SpaceMemberDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceMemberDetail{}, err
	}
	userID = strings.ToLower(strings.TrimSpace(userID))
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(userID) {
		return SpaceMemberDetail{}, ErrSpaceMemberNotFound
	}
	role, err := parseSpaceMemberRole(input.Role)
	if err != nil {
		return SpaceMemberDetail{}, err
	}
	if role == "OWNER" && actor.Role != policy.OrganizationRoleOwner && actor.Role != policy.OrganizationRoleAdmin {
		return SpaceMemberDetail{}, errOwnerGrantForbidden
	}
	status := strings.ToUpper(strings.TrimSpace(input.Status))
	if status == "" {
		status = "ACTIVE"
	}
	if status != "ACTIVE" && status != "REVOKED" {
		return SpaceMemberDetail{}, ErrInvalidStatus
	}
	var orgRole string
	err = tx.QueryRow(ctx, `
		SELECT role FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid AND status = 'ACTIVE'`,
		userID).Scan(&orgRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceMemberDetail{}, ErrSpaceMemberNotFound
	}
	if err != nil {
		return SpaceMemberDetail{}, err
	}
	// 组织访客（GUEST）不能担任空间管理员。
	if orgRole == "GUEST" && (role == "OWNER" || role == "EDITOR") {
		return SpaceMemberDetail{}, invalidInput("guests cannot be space administrators")
	}
	owners, err := lockSpaceOwners(ctx, tx, spaceID)
	if err != nil {
		return SpaceMemberDetail{}, err
	}
	if (role != "OWNER" || status != "ACTIVE") && onlyActiveOwner(owners, userID) {
		return SpaceMemberDetail{}, ErrLastOwner
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4)
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE
		   SET role = EXCLUDED.role, status = EXCLUDED.status`, spaceID, userID, role, status); err != nil {
		return SpaceMemberDetail{}, mapDatabaseError(err)
	}
	if err := emitSpaceMembershipChange(ctx, tx, spaceID); err != nil {
		return SpaceMemberDetail{}, err
	}
	items, err := s.ListSpaceMembersDetail(ctx, tx, organizationID, spaceID, actor, "", "")
	if err != nil {
		return SpaceMemberDetail{}, err
	}
	for _, item := range items {
		if item.UserID == userID {
			return item, nil
		}
	}
	return SpaceMemberDetail{}, ErrSpaceMemberNotFound
}

// RemoveSpaceMember 删除显式成员行（移出或离开）。最后一个 ACTIVE OWNER → LAST_OWNER；
// 移出后该用户没有任何可读的活跃空间 → ONLY_SPACE。目标不是显式成员 → 404。
func (s Service) RemoveSpaceMember(ctx context.Context, tx pgx.Tx, organizationID, spaceID, userID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	userID = strings.ToLower(strings.TrimSpace(userID))
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(userID) {
		return ErrSpaceMemberNotFound
	}
	owners, err := lockSpaceOwners(ctx, tx, spaceID)
	if err != nil {
		return err
	}
	var status string
	err = tx.QueryRow(ctx, `
		SELECT status FROM openfox.space_members
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND user_id = $2::uuid
		   FOR UPDATE`, spaceID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceMemberNotFound
	}
	if err != nil {
		return err
	}
	if onlyActiveOwner(owners, userID) {
		return ErrLastOwner
	}
	if status == "ACTIVE" {
		if err := requireOtherSpace(ctx, tx, spaceID, userID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM openfox.space_members
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND user_id = $2::uuid`,
		spaceID, userID)
	if err != nil {
		return mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrSpaceMemberNotFound
	}
	return emitSpaceMembershipChange(ctx, tx, spaceID)
}

// Membership changes alter both space visibility and task write permissions.
// Keep the event in the same transaction so failed writes never notify clients.
func emitSpaceMembershipChange(ctx context.Context, tx pgx.Tx, spaceID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, space_id)
		VALUES (openfox_private.current_organization_id(), 'space.changed', 'space', $1::uuid, $1::uuid)`, spaceID)
	return err
}
