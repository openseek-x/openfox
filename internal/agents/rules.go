package agents

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// 的业务错误码：接口层按 RuleError 原样输出（见 internal/api/agents.go 的 writeAgentError）。
var (
	ErrSecretsUnavailable = collaboration.NewRuleError(http.StatusConflict, "SECRETS_UNAVAILABLE",
		"secret encryption key is not configured")
	ErrDeviceScopeMismatch = collaboration.NewRuleError(http.StatusConflict, "DEVICE_SCOPE_MISMATCH",
		"the Agent's computer grant does not allow the target space or owner")
	ErrLimitReached = collaboration.NewRuleError(http.StatusConflict, "LIMIT_REACHED", "limit reached")
	ErrEnvVarExists = collaboration.NewRuleError(http.StatusConflict, "NAME_CONFLICT",
		"environment variable already exists")
	ErrRepoConflict = collaboration.NewRuleError(http.StatusConflict, "REPO_CONFLICT",
		"repository is already configured for this Agent")
	ErrCrewMembershipConflict = collaboration.NewRuleError(http.StatusConflict, "AGENT_CREW_MEMBERSHIP_CONFLICT",
		"Agent is already an exclusive member of another crew")
	ErrCrewLeaderRequired = collaboration.NewRuleError(http.StatusConflict, "CREW_LEADER_REQUIRED",
		"crew leader cannot be removed")
	ErrCrewLeaderSpaceMismatch = collaboration.NewRuleError(http.StatusConflict, "CREW_LEADER_SPACE_MISMATCH",
		"crew leader is not in the target space")
	ErrInvalidOwner = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_OWNER",
		"owner must be an active member who can read the space")
	ErrResourceNotFound = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "resource is not available")
)

// lockEditableAgent 在事务内锁定 Agent 并按「能编辑 Agent」的规则校验（profile、环境变量、仓库、配置共用）：
//   - 空间不可读、或他人的私有 Agent：ErrNotFound（不暴露存在性）。
//   - 内置小助手：ErrForbidden。
//   - 共享 Agent：所有者或空间管理者；私有 Agent：仅所有者。
//   - allowArchived 为 false 时已归档返回 ErrArchived。
func lockEditableAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID string, actor Actor, allowArchived bool) (Agent, error) {
	if err := authorizeSpace(ctx, tx, organizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
		return Agent{}, err
	}
	var item Agent
	var kind, visibility, builtin string
	var avatar []byte
	var version, profileVersion, currentConfig int64
	err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text, name, title, description, avatar, kind, visibility, archived_at,
		       COALESCE(current_config_version, 0), version, profile_version, COALESCE(builtin_kind, ''),
		       allow_assign, allow_direct_chat, trigger_crew, trigger_mention, trigger_workflow
		  FROM openfox.agents
		 WHERE organization_id = $1::uuid AND space_id = $2::uuid AND id = $3::uuid
		 FOR UPDATE`, organizationID, spaceID, agentID).Scan(&item.OwnerUserID, &item.Name, &item.Title,
		&item.Description, &avatar, &kind, &visibility, &item.ArchivedAt, &currentConfig, &version,
		&profileVersion, &builtin, &item.AllowAssign, &item.AllowDirectChat, &item.TriggerCrew,
		&item.TriggerMention, &item.TriggerWorkflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	item.OrganizationID, item.SpaceID, item.ID = organizationID, spaceID, agentID
	item.Kind, item.Visibility, item.BuiltinKind = Kind(kind), Visibility(visibility), builtin
	item.CurrentConfigVersion, item.Version, item.ProfileVersion = uint64(currentConfig), uint64(version), uint64(profileVersion)
	if len(avatar) > 0 {
		item.Avatar = avatar
	}
	owner := item.OwnerUserID == actor.UserID
	if item.Visibility == VisibilityPrivate && !owner {
		return Agent{}, ErrNotFound
	}
	if item.BuiltinKind != "" {
		return Agent{}, ErrForbidden
	}
	if !owner {
		if err := authorizeSpace(ctx, tx, organizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
			return Agent{}, err
		}
	}
	if !allowArchived && item.ArchivedAt != nil {
		return Agent{}, ErrArchived
	}
	return item, nil
}

// requireReadableAgent 校验空间可读且 Agent 对当前成员可见（共享，或自己的私有 Agent），返回 Agent。
func requireReadableAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID string, actor Actor) (Agent, error) {
	if err := authorizeSpace(ctx, tx, organizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
		return Agent{}, err
	}
	return loadAgent(ctx, tx, organizationID, spaceID, agentID, actor.UserID, true)
}

// canViewSecretValues：Agent 所有者与组织 OWNER / ADMIN 可看环境变量原值。
func canViewSecretValues(agent Agent, actor Actor) bool {
	if agent.OwnerUserID == actor.UserID {
		return true
	}
	return actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin
}

// memberCanReadSpaceSQL：$1 为用户 ID 数组、$2 为空间 ID；与 SpaceReadableSQL 一致，但用目标成员自己的组织角色。
const memberCanReadSpaceSQL = `
	SELECT om.user_id::text FROM openfox.organization_members om
	  JOIN openfox.spaces s ON s.organization_id = om.organization_id AND s.id = $2::uuid AND s.status = 'ACTIVE'
	 WHERE om.organization_id = openfox_private.current_organization_id()
	   AND om.user_id = ANY($1::uuid[]) AND om.status = 'ACTIVE'
	   AND (EXISTS (SELECT 1 FROM openfox.space_members sm
	                 WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	                   AND sm.user_id = om.user_id AND sm.status = 'ACTIVE')
	        OR (s.visibility = 'SHARED' AND om.role <> 'GUEST' AND NOT EXISTS (
	                 SELECT 1 FROM openfox.space_members sm
	                  WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	                    AND sm.user_id = om.user_id AND sm.status <> 'ACTIVE')))`

// membersCanReadSpace 返回 userIDs 中都能读该空间的组织 ACTIVE 成员是否齐全。
func membersCanReadSpace(ctx context.Context, tx pgx.Tx, spaceID string, userIDs []string) (bool, error) {
	if len(userIDs) == 0 {
		return true, nil
	}
	rows, err := tx.Query(ctx, memberCanReadSpaceSQL, userIDs, spaceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		matched++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return matched == len(userIDs), nil
}

// checkGrantScope 校验 Agent 当前配置的电脑授权允许目标空间与所有者（与 validate_agent_config_version 触发器一致）。
// 没有配置行（旧夹具）时不校验。
func checkGrantScope(ctx context.Context, tx pgx.Tx, organizationID, agentID, spaceID, ownerID string) error {
	var scope string
	var scopeSpace, scopeUser *string
	err := tx.QueryRow(ctx, `
		SELECT g.scope, g.scope_space_id::text, g.scope_user_id::text
		  FROM openfox.agents a
		  JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		  JOIN openfox.device_grants g ON g.organization_id = c.organization_id AND g.id = c.grant_id
		 WHERE a.organization_id = $1::uuid AND a.id = $2::uuid`, organizationID, agentID).Scan(&scope, &scopeSpace, &scopeUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return grantAllows(scope, scopeSpace, scopeUser, spaceID, ownerID)
}

func grantAllows(scope string, scopeSpace, scopeUser *string, spaceID, ownerID string) error {
	switch scope {
	case "ORGANIZATION":
		return nil
	case "SPACE":
		if scopeSpace != nil && *scopeSpace == spaceID {
			return nil
		}
	case "PERSONAL":
		if scopeUser != nil && *scopeUser == ownerID {
			return nil
		}
	}
	return ErrDeviceScopeMismatch
}

// validateTitle：头衔最多 64 个字符，不能有控制字符；去掉首尾空白。
func validateTitle(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 64 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", ErrInvalidRequest
	}
	return value, nil
}

// touchCrew 递增小队版本（成员变化也视为小队修改，供前端乐观锁与刷新判断）。
func touchCrew(ctx context.Context, tx pgx.Tx, crewID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.agent_crews SET version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, crewID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
