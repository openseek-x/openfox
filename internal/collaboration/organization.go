package collaboration

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/policy"
)

// 组织设置：图标、名称、描述、slug、默认语言、优先级档位、软删除。

var (
	ErrSlugInvalid              = NewRuleError(http.StatusBadRequest, "SLUG_INVALID", "slug may only contain lowercase letters, digits and hyphens")
	ErrSlugReserved             = NewRuleError(http.StatusBadRequest, "SLUG_RESERVED", "slug is reserved")
	ErrSlugTaken                = NewRuleError(http.StatusConflict, "SLUG_TAKEN", "slug is already in use")
	ErrOrganizationConfirm      = NewRuleError(http.StatusBadRequest, "CONFIRM_MISMATCH", "confirmation does not match the organization name")
	ErrOrganizationForbidden    = NewRuleError(http.StatusForbidden, "ACTION_FORBIDDEN", "only organization owners or admins can do this")
	ErrPriorityLevelUnavailable = NewRuleError(http.StatusBadRequest, "PRIORITY_LEVEL_UNAVAILABLE", "the organization uses 4 priority levels")
)

// 与 create_first_organization 的 slug 规则一致（3–40 位）。
var organizationSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// reservedOrganizationSlugs 与站点路由或常见系统地址冲突。
var reservedOrganizationSlugs = map[string]bool{
	"api": true, "app": true, "admin": true, "assets": true, "docs": true, "help": true, "invite": true,
	"join": true, "login": true, "logout": true, "settings": true, "static": true, "status": true,
	"support": true, "welcome": true, "www": true,
}

var organizationLocales = map[string]bool{"zh-CN": true, "en-US": true}

// OrganizationSettings 对应契约 OrganizationSettings。
type OrganizationSettings struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    string  `json:"description"`
	IconKind       *string `json:"icon_kind"`
	Icon           *string `json:"icon"`
	IconURL        *string `json:"icon_url"`
	DefaultLocale  string  `json:"default_locale"`
	PriorityLevels int     `json:"priority_levels"`
	Version        uint64  `json:"version"`
	MyRole         string  `json:"my_role"`
	CanManage      bool    `json:"can_manage"`
	CanDelete      bool    `json:"can_delete"`
	// LeaveBlocked：sole_owner = 唯一所有者且还有其他成员；sole_member = 只剩自己（只能删除组织）。
	LeaveBlocked *string `json:"leave_blocked"`
}

// OrganizationPatch 只改出现的字段；Icon 出现时整体替换（IconKind 为空表示清除，回到首字母头像）。
type OrganizationPatch struct {
	Version        uint64
	Name           *string
	Slug           *string
	Description    *string
	SetIcon        bool
	IconKind       string
	Icon           string
	DefaultLocale  *string
	PriorityLevels *int
}

func canManageOrganization(role policy.OrganizationRole) bool {
	return role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin
}

// IsPriorityLevelViolation 识别 000037 优先级档位触发器的拒绝（组织为 4 级时写入 LOWEST）。
func IsPriorityLevelViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "priority_levels"
}

func (s Service) GetOrganizationSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (OrganizationSettings, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return OrganizationSettings{}, err
	}
	return loadOrganizationSettings(ctx, tx, organizationID, actor, "")
}

func loadOrganizationSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, lock string) (OrganizationSettings, error) {
	var item OrganizationSettings
	var version int64
	var owners, members int
	err := tx.QueryRow(ctx, `
		SELECT o.id::text, o.name, o.slug, o.description, o.icon_kind, COALESCE(o.icon, o.icon_upload_id::text),
		       o.default_locale, o.priority_levels, o.version,
		       (SELECT count(*) FROM openfox.organization_members m
		         WHERE m.organization_id = o.id AND m.status = 'ACTIVE' AND m.role = 'OWNER'),
		       (SELECT count(*) FROM openfox.organization_members m
		         WHERE m.organization_id = o.id AND m.status = 'ACTIVE')
		  FROM openfox.organizations o
		 WHERE o.id = openfox_private.current_organization_id() AND o.deleted_at IS NULL`+lock).Scan(
		&item.ID, &item.Name, &item.Slug, &item.Description, &item.IconKind, &item.Icon,
		&item.DefaultLocale, &item.PriorityLevels, &version, &owners, &members)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationSettings{}, ErrSpaceNotFound
	}
	if err != nil {
		return OrganizationSettings{}, err
	}
	if item.Version, err = positiveVersion(version); err != nil {
		return OrganizationSettings{}, err
	}
	if item.IconKind != nil && *item.IconKind == "upload" {
		url := IconURL(organizationID, *item.Icon)
		item.IconURL = &url
	}
	item.MyRole = string(actor.Role)
	item.CanManage = canManageOrganization(actor.Role)
	item.CanDelete = actor.Role == policy.OrganizationRoleOwner
	switch {
	case members <= 1:
		blocked := "sole_member"
		item.LeaveBlocked = &blocked
	case actor.Role == policy.OrganizationRoleOwner && owners <= 1:
		blocked := "sole_owner"
		item.LeaveBlocked = &blocked
	}
	return item, nil
}

// LowestPriorityCount 是切换为 4 级时会被改为「低」的任务数（含所有空间，不受空间可见性限制）。
func (s Service) LowestPriorityCount(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (int, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	if !canManageOrganization(actor.Role) {
		return 0, ErrOrganizationForbidden
	}
	var count int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND priority = 'LOWEST'
		   AND deleted_at IS NULL`).Scan(&count)
	return count, err
}

// UpdateOrganizationSettings 锁组织行后按 version 更新。5 → 4 级时在同一事务里把 LOWEST 任务与专项改为 LOW
// （确认由界面负责）；组织行锁与优先级触发器的 FOR SHARE 互斥，切换后不会残留 LOWEST。
func (s Service) UpdateOrganizationSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, patch OrganizationPatch) (OrganizationSettings, int, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return OrganizationSettings{}, 0, err
	}
	if !canManageOrganization(actor.Role) {
		return OrganizationSettings{}, 0, ErrOrganizationForbidden
	}
	current, err := loadOrganizationSettings(ctx, tx, organizationID, actor, " FOR UPDATE OF o")
	if err != nil {
		return OrganizationSettings{}, 0, err
	}
	if patch.Version == 0 {
		return OrganizationSettings{}, 0, ErrInvalidVersion
	}
	if current.Version != patch.Version {
		return OrganizationSettings{}, 0, ErrVersionConflict
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if n := len([]rune(name)); n < 1 || n > 100 {
			return OrganizationSettings{}, 0, ErrInvalidName
		}
		patch.Name = &name
	}
	if patch.Slug != nil {
		slug := strings.TrimSpace(*patch.Slug)
		if !organizationSlugPattern.MatchString(slug) {
			return OrganizationSettings{}, 0, ErrSlugInvalid
		}
		if reservedOrganizationSlugs[slug] {
			return OrganizationSettings{}, 0, ErrSlugReserved
		}
		patch.Slug = &slug
	}
	if patch.Description != nil {
		description := strings.TrimSpace(*patch.Description)
		if len([]rune(description)) > 500 {
			return OrganizationSettings{}, 0, invalidInput("description is too long")
		}
		patch.Description = &description
	}
	var kind, icon, upload any
	if patch.SetIcon {
		if kind, icon, upload, err = NormalizeViewIcon(patch.IconKind, patch.Icon); err != nil {
			return OrganizationSettings{}, 0, err
		}
	}
	if patch.DefaultLocale != nil && !organizationLocales[*patch.DefaultLocale] {
		return OrganizationSettings{}, 0, invalidInput("default_locale is invalid")
	}
	if patch.PriorityLevels != nil && *patch.PriorityLevels != 4 && *patch.PriorityLevels != 5 {
		return OrganizationSettings{}, 0, invalidInput("priority_levels must be 4 or 5")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.organizations
		   SET name = COALESCE($1, name), slug = COALESCE($2, slug), description = COALESCE($3, description),
		       icon_kind = CASE WHEN $4 THEN $5 ELSE icon_kind END,
		       icon = CASE WHEN $4 THEN $6 ELSE icon END,
		       icon_upload_id = CASE WHEN $4 THEN $7::uuid ELSE icon_upload_id END,
		       default_locale = COALESCE($8, default_locale), priority_levels = COALESCE($9, priority_levels),
		       version = version + 1
		 WHERE id = openfox_private.current_organization_id()`,
		patch.Name, patch.Slug, patch.Description, patch.SetIcon, kind, icon, upload,
		patch.DefaultLocale, patch.PriorityLevels); err != nil {
		if isUniqueViolation(err) {
			return OrganizationSettings{}, 0, ErrSlugTaken
		}
		if errors.Is(mapDatabaseError(err), ErrInvalidTask) {
			return OrganizationSettings{}, 0, invalidInput("icon is unavailable")
		}
		return OrganizationSettings{}, 0, err
	}
	downgraded := 0
	if patch.PriorityLevels != nil && *patch.PriorityLevels == 4 && current.PriorityLevels == 5 {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.tasks SET priority = 'LOW', version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND priority = 'LOWEST'`)
		if err != nil {
			return OrganizationSettings{}, 0, err
		}
		downgraded = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.projects SET priority = 'LOW', version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND priority = 'LOWEST'`); err != nil {
			return OrganizationSettings{}, 0, err
		}
	}
	updated, err := loadOrganizationSettings(ctx, tx, organizationID, actor, "")
	return updated, downgraded, err
}

// OrganizationDeletion 汇总软删除的连带处理。
type OrganizationDeletion struct {
	ArchivedAgents int
	RevokedGrants  int
}

// PrepareOrganizationDelete 校验所有者与确认名称（区分大小写、不去空白）并锁组织行；之后由调用方取消 Run，
// 再调用 ApplyOrganizationDelete。
func (s Service) PrepareOrganizationDelete(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, confirmName string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if actor.Role != policy.OrganizationRoleOwner {
		return ErrOrganizationForbidden
	}
	current, err := loadOrganizationSettings(ctx, tx, organizationID, actor, " FOR UPDATE OF o")
	if err != nil {
		return err
	}
	if confirmName != current.Name {
		return ErrOrganizationConfirm
	}
	return nil
}

// ApplyOrganizationDelete 归档全部未归档的 Agent、吊销全部电脑授权，并写 deleted_at（目录同步后对所有人立即不可见）。
func (s Service) ApplyOrganizationDelete(ctx context.Context, tx pgx.Tx, actor Actor) (OrganizationDeletion, error) {
	var result OrganizationDeletion
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.agents
		   SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND archived_at IS NULL`)
	if err != nil {
		return result, err
	}
	result.ArchivedAgents = int(tag.RowsAffected())
	tag, err = tx.Exec(ctx, `
		UPDATE openfox.device_grants
		   SET status = 'REVOKED', permission_version = permission_version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'ACTIVE'`)
	if err != nil {
		return result, err
	}
	result.RevokedGrants = int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.organizations
		   SET deleted_at = clock_timestamp(), deleted_by = $1::uuid, version = version + 1
		 WHERE id = openfox_private.current_organization_id() AND deleted_at IS NULL`, actor.UserID); err != nil {
		return result, err
	}
	return result, nil
}

// MemberSpace 是成员页「n 个空间」浮层的一项。
type MemberSpace struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	Icon       *string `json:"icon"`
	Visibility string  `json:"visibility"`
}

// MemberSpaces 返回这些成员显式加入且仍活跃的空间（按名称排序），只包含当前操作人能读到的空间，
// 不暴露对方所在、而自己看不到的私密空间。
func MemberSpaces(ctx context.Context, tx pgx.Tx, actor Actor, userIDs []string) (map[string][]MemberSpace, error) {
	result := make(map[string][]MemberSpace, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT sm.user_id::text, s.id::text, s.name, s.prefix, s.icon, lower(s.visibility)
		  FROM openfox.space_members sm
		  JOIN openfox.spaces s ON s.organization_id = sm.organization_id AND s.id = sm.space_id
		 WHERE sm.organization_id = openfox_private.current_organization_id()
		   AND sm.user_id = ANY($2::uuid[]) AND sm.status = 'ACTIVE' AND s.status = 'ACTIVE'
		   AND `+SpaceReadableSQL+`
		 ORDER BY s.name, s.id`, strings.ToUpper(string(actor.Role)), userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var item MemberSpace
		if err := rows.Scan(&userID, &item.ID, &item.Name, &item.Prefix, &item.Icon, &item.Visibility); err != nil {
			return nil, err
		}
		result[userID] = append(result[userID], item)
	}
	return result, rows.Err()
}
