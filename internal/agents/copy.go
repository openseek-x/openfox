package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// copySuffix 是复制时默认名称的后缀；重名时依次尝试「{name} 副本 2」「{name} 副本 3」…
const (
	copySuffix       = " 副本"
	maxCopyNameTries = 50
)

// CopyAgent 复制资料、当前配置（新 Agent 的配置版本 1）、环境变量（重新加密）与仓库。
// 所有者为操作人、可见性 PRIVATE；需要能读源 Agent 并能管理该空间。idempotencyKey 非空时按 agent.copy 幂等。
func (s PostgresStore) CopyAgent(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, name *string, idempotencyKey string) (Agent, bool, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return Agent{}, false, err
	}
	requestedName := ""
	if name != nil {
		requestedName = strings.TrimSpace(*name)
		if err := validateAgentFields(requestedName, "", KindCode, VisibilityPrivate); err != nil {
			return Agent{}, false, err
		}
	}
	var result Agent
	var replay bool
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		source, err := requireReadableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor)
		if err != nil {
			return err
		}
		if source.BuiltinKind != "" {
			return ErrForbidden
		}
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
			return err
		}
		if err := requireWritableSpace(ctx, tx, spaceID); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "agent.copy", idempotencyKey, struct {
			SpaceID string `json:"space_id"`
			AgentID string `json:"agent_id"`
			Name    string `json:"name"`
		}{spaceID, agentID, requestedName})
		if err != nil {
			return err
		}
		if claim.Replay {
			replay = true
			result, err = loadAgentByID(ctx, tx, selection.OrganizationID, claim.ResourceID, actor.UserID)
			return err
		}
		if source.Config == nil {
			return ErrInvalidConfig
		}
		if err := checkGrantScope(ctx, tx, selection.OrganizationID, agentID, spaceID, actor.UserID); err != nil {
			return err
		}
		copyName := requestedName
		if copyName == "" {
			if copyName, err = pickCopyName(ctx, tx, spaceID, source.Name); err != nil {
				return err
			}
		}
		id, err := membership.NewID()
		if err != nil {
			return err
		}
		var avatarArg any
		if len(source.Avatar) > 0 {
			avatarArg = string(source.Avatar)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agents (
				organization_id, id, device_id, space_id, owner_user_id, name, description, kind, visibility,
				current_config_version, version, updated_at, title, avatar, allow_assign, allow_direct_chat,
				trigger_crew, trigger_mention, trigger_workflow
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, 'PRIVATE', 1, 1, clock_timestamp(),
			          $9, $10::jsonb, $11, $12, $13, $14, $15)`,
			selection.OrganizationID, id, source.Config.ComputerID, spaceID, actor.UserID, copyName,
			source.Description, source.Kind, source.Title, avatarArg, source.AllowAssign, source.AllowDirectChat,
			source.TriggerCrew, source.TriggerMention, source.TriggerWorkflow); err != nil {
			return mapDatabaseError(err)
		}
		if err := copyEnvVars(ctx, tx, s.Secrets, selection.OrganizationID, agentID, id, actor.UserID); err != nil {
			return err
		}
		envDigest, err := computeEnvDigest(ctx, tx, s.Secrets, selection.OrganizationID, id)
		if err != nil {
			return err
		}
		config := source.Config
		if err := insertConfigWithDigest(ctx, tx, selection.OrganizationID, id, 1, actor.UserID, ConfigInput{
			Instructions: config.Instructions, ComputerID: config.ComputerID, GrantID: config.GrantID,
			Runtime: config.Runtime, Model: config.Model, MaxConcurrency: config.MaxConcurrency,
			QueueTimeoutSeconds: config.QueueTimeoutSeconds, RunTimeoutSeconds: config.RunTimeoutSeconds,
			DirectoryMode: config.DirectoryMode, DirectoryRoots: config.DirectoryRoots, RunSettings: config.RunSettings,
		}, envDigest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agent_repos (organization_id, agent_id, url, base_branch, position, created_by)
			SELECT organization_id, $2::uuid, url, base_branch, position, $3::uuid
			  FROM openfox.agent_repos
			 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid`,
			agentID, id, actor.UserID); err != nil {
			return mapDatabaseError(err)
		}
		if err := idempotency.Complete(ctx, tx, claim, id); err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, selection, actor, "agent.copied", id, spaceID, 1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, selection.OrganizationID, spaceID, id, actor.UserID, false)
		return err
	})
	return result, replay, err
}

// CopyName 返回第 attempt 个候选名称（attempt 从 1 开始）；名称按字符截断，保证不超过 255 个字符。
func CopyName(base string, attempt int) string {
	suffix := copySuffix
	if attempt > 1 {
		suffix = fmt.Sprintf("%s %d", copySuffix, attempt)
	}
	runes := []rune(strings.TrimSpace(base))
	if limit := 255 - len([]rune(suffix)); len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes)) + suffix
}

// pickCopyName 一次查询找出空间里已被占用的候选名称（不区分大小写，与 agents_space_name_unique 一致），取第一个空闲的。
func pickCopyName(ctx context.Context, tx pgx.Tx, spaceID, base string) (string, error) {
	candidates := make([]string, 0, maxCopyNameTries)
	lowered := make([]string, 0, maxCopyNameTries)
	for attempt := 1; attempt <= maxCopyNameTries; attempt++ {
		name := CopyName(base, attempt)
		candidates = append(candidates, name)
		lowered = append(lowered, strings.ToLower(name))
	}
	rows, err := tx.Query(ctx, `
		SELECT lower(name) FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND archived_at IS NULL AND builtin_kind IS NULL AND lower(name) = ANY($2::text[])`, spaceID, lowered)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		taken[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for index, name := range candidates {
		if !taken[lowered[index]] {
			return name, nil
		}
	}
	return "", ErrNameConflict
}

// loadAgentByID 按 ID 读取（不限定空间），只返回对当前成员可见的 Agent。
func loadAgentByID(ctx context.Context, tx pgx.Tx, organizationID, agentID, actorID string) (Agent, error) {
	item, err := scanAgent(tx.QueryRow(ctx, `
		SELECT `+agentColumns+`
		  FROM openfox.agents a
		  LEFT JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id
		   AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		 WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		   AND (a.visibility = 'SHARED' OR a.owner_user_id = $3::uuid)`, organizationID, agentID, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return item, err
}
