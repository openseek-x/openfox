package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/requestid"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type PostgresStore struct {
	Pool *postgres.Pool
	// Secrets 加解密 Agent 环境变量；nil 表示未配置主密钥（写入返回 409 SECRETS_UNAVAILABLE）。
	Secrets *secrets.Box
}

func (s PostgresStore) Create(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID string, input CreateInput) (Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return Agent{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return Agent{}, ErrInvalidRequest
	}
	input, err = normalizeCreateInput(input)
	if err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		result, err = createInTx(ctx, tx, selection, actor, spaceID, input)
		return err
	})
	return result, err
}

// CreateInTx 在调用方的租户事务里创建 Agent（小助手确认草稿时与草稿状态同一事务提交）。
// 权限与校验和 Create 完全一致。
func CreateInTx(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, spaceID string, input CreateInput) (Agent, error) {
	if selection.ActorID != actor.UserID || actor.AuthorizationVersion == 0 || actor.Role == "" {
		return Agent{}, ErrInvalidRequest
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return Agent{}, ErrInvalidRequest
	}
	input, err = normalizeCreateInput(input)
	if err != nil {
		return Agent{}, err
	}
	return createInTx(ctx, tx, selection, actor, spaceID, input)
}

func createInTx(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, spaceID string, input CreateInput) (Agent, error) {
	if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
		return Agent{}, err
	}
	if err := requireWritableSpace(ctx, tx, spaceID); err != nil {
		return Agent{}, err
	}
	if err := validateConfigTarget(ctx, tx, selection.OrganizationID, input.Config); err != nil {
		return Agent{}, err
	}
	if err := validateAvatarReference(ctx, tx, selection.OrganizationID, input.Avatar); err != nil {
		return Agent{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return Agent{}, err
	}
	var avatarArg any
	if len(input.Avatar) > 0 {
		avatarArg = string(input.Avatar)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.agents (
			organization_id, id, device_id, space_id, owner_user_id, name,
			description, kind, visibility, current_config_version, version, updated_at,
			title, avatar, allow_assign, allow_direct_chat
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9, 1, 1, clock_timestamp(),
		          $10, $11::jsonb, COALESCE($12::boolean, true), COALESCE($13::boolean, true))
	`, selection.OrganizationID, id, input.Config.ComputerID, spaceID, actor.UserID,
		input.Name, input.Description, input.Kind, input.Visibility, input.Title, avatarArg,
		input.AllowAssign, input.AllowDirectChat); err != nil {
		return Agent{}, mapDatabaseError(err)
	}
	if err := insertConfig(ctx, tx, selection.OrganizationID, id, 1, actor.UserID, input.Config); err != nil {
		return Agent{}, err
	}
	if err := recordAudit(ctx, tx, selection, actor, "agent.created", id, spaceID, 1); err != nil {
		return Agent{}, err
	}
	return loadAgent(ctx, tx, selection.OrganizationID, spaceID, id, actor.UserID, false)
}

func (s PostgresStore) List(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID string, options ListOptions) ([]Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return nil, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	var result []Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
			return err
		}
		query := `
			SELECT ` + agentColumns + `
			  FROM openfox.agents a
			  LEFT JOIN openfox.agent_config_versions c
			    ON c.organization_id = a.organization_id
			   AND c.agent_id = a.id
			   AND c.config_version = a.current_config_version
			 WHERE a.organization_id = $1::uuid
			   AND a.space_id = $2::uuid
			   AND a.builtin_kind IS NULL
			   AND (a.visibility = 'SHARED' OR a.owner_user_id = $3::uuid)`
		args := []any{selection.OrganizationID, spaceID, actor.UserID}
		if !options.IncludeArchived {
			query += " AND a.archived_at IS NULL"
		}
		query += " ORDER BY a.created_at, a.id"
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		result = make([]Agent, 0)
		for rows.Next() {
			item, err := scanAgent(rows)
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (s PostgresStore) Get(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) (Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return Agent{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return Agent{}, ErrInvalidRequest
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return Agent{}, ErrNotFound
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor.UserID, true)
		return err
	})
	return result, err
}

func (s PostgresStore) Update(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, input UpdateInput) (Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return Agent{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil || input.Version == 0 {
		return Agent{}, ErrInvalidRequest
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return Agent{}, ErrNotFound
	}
	if err := validatePatch(input); err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
			return err
		}
		current, err := lockAgent(ctx, tx, selection.OrganizationID, spaceID, agentID)
		if err != nil {
			return err
		}
		// 内置小助手只能通过 EnsureAssistant 维护，不能被改名、改可见性或归档。
		if current.BuiltinKind != "" || (current.Visibility == VisibilityPrivate && current.OwnerUserID != actor.UserID) {
			return ErrForbidden
		}
		if current.Version != input.Version {
			return ErrVersionConflict
		}
		name, description, kind, visibility, archivedAt := current.Name, current.Description, current.Kind, current.Visibility, current.ArchivedAt
		if input.Name != nil {
			name = strings.TrimSpace(*input.Name)
		}
		if input.Description != nil {
			description = *input.Description
		}
		if input.Kind != nil {
			kind = *input.Kind
		}
		if input.Visibility != nil {
			visibility = *input.Visibility
		}
		if input.Archived != nil {
			if *input.Archived {
				now := time.Now().UTC()
				archivedAt = &now
			} else {
				archivedAt = nil
			}
		}
		if err := validateAgentFields(name, description, kind, visibility); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.agents
			   SET name = $1, description = $2, kind = $3, visibility = $4,
			       archived_at = $5, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = $6::uuid AND id = $7::uuid AND version = $8
		`, name, description, kind, visibility, archivedAt, selection.OrganizationID, agentID, input.Version); err != nil {
			return mapDatabaseError(err)
		}
		if err := recordAudit(ctx, tx, selection, actor, "agent.updated", agentID, spaceID, input.Version+1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor.UserID, false)
		return err
	})
	return result, err
}

func (s PostgresStore) CreateConfig(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, expectedAgentVersion uint64, input ConfigInput) (Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return Agent{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil || expectedAgentVersion == 0 {
		return Agent{}, ErrInvalidRequest
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return Agent{}, ErrNotFound
	}
	input, err = normalizeConfigInput(input)
	if err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
			return err
		}
		current, err := lockAgent(ctx, tx, selection.OrganizationID, spaceID, agentID)
		if err != nil {
			return err
		}
		// 内置小助手只能通过 EnsureAssistant 维护，不能被改名、改可见性或归档。
		if current.BuiltinKind != "" || (current.Visibility == VisibilityPrivate && current.OwnerUserID != actor.UserID) {
			return ErrForbidden
		}
		if current.ArchivedAt != nil {
			return ErrArchived
		}
		if current.Version != expectedAgentVersion {
			return ErrVersionConflict
		}
		if err := validateConfigTarget(ctx, tx, selection.OrganizationID, input); err != nil {
			return err
		}
		nextConfigVersion := current.CurrentConfigVersion + 1
		if nextConfigVersion == 0 {
			nextConfigVersion = 1
		}
		envDigest, err := computeEnvDigest(ctx, tx, s.Secrets, selection.OrganizationID, agentID)
		if err != nil {
			return err
		}
		if err := insertConfigWithDigest(ctx, tx, selection.OrganizationID, agentID, nextConfigVersion, actor.UserID, input, envDigest); err != nil {
			return err
		}
		commandTag, err := tx.Exec(ctx, `
			UPDATE openfox.agents
			   SET device_id = $1::uuid, current_config_version = $2,
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = $3::uuid AND id = $4::uuid AND version = $5
		`, input.ComputerID, nextConfigVersion, selection.OrganizationID, agentID, expectedAgentVersion)
		if err != nil {
			return mapDatabaseError(err)
		}
		if commandTag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if err := recordAudit(ctx, tx, selection, actor, "agent.config_created", agentID, spaceID, expectedAgentVersion+1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor.UserID, false)
		return err
	})
	return result, err
}

func (s PostgresStore) ListConfigs(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) ([]ConfigVersion, error) {
	if err := s.validate(selection, actor); err != nil {
		return nil, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return nil, ErrNotFound
	}
	var result []ConfigVersion
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
			return err
		}
		if _, err := loadAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor.UserID, true); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.organization_id::text, c.agent_id::text, c.config_version,
			       c.instructions, c.computer_id::text, c.grant_id::text, c.runtime, c.model,
			       c.max_concurrency, c.queue_timeout_seconds, c.run_timeout_seconds,
			       c.directory_mode, c.directory_roots, c.created_by::text, c.created_at,`+configExtraColumns+`
			  FROM openfox.agent_config_versions c
			 WHERE c.organization_id = $1::uuid AND c.agent_id = $2::uuid
			 ORDER BY c.config_version DESC
		`, selection.OrganizationID, agentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		result = make([]ConfigVersion, 0)
		for rows.Next() {
			item, err := scanConfig(rows)
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

// OnlineWindowSeconds 与 dispatch.DefaultDeviceOnlineAfter 一致（90 秒，单测校验两者相等）：
// 电脑在这段时间内有心跳、且授权 ACTIVE，才算 Agent 在线。
const OnlineWindowSeconds = 90

// agentOnlineSQL / agentComputerNameSQL 按当前配置（c）计算在线与电脑名称；a 为 Agent 别名。
const agentOnlineSQL = `COALESCE((
		SELECT d.status = 'ACTIVE' AND d.last_seen_at IS NOT NULL
		       AND d.last_seen_at >= clock_timestamp() - make_interval(secs => 90)
		  FROM openfox.physical_devices d
		  JOIN openfox.device_grants g
		    ON g.organization_id = a.organization_id AND g.id = c.grant_id
		   AND g.device_id = d.id AND g.status = 'ACTIVE'
		 WHERE d.id = c.computer_id), false)`

const agentComputerNameSQL = `(SELECT d.name FROM openfox.physical_devices d WHERE d.id = c.computer_id)`

// agentFavoriteSQL：当前成员是否收藏了 Agent（RLS 只放行本人的收藏行）。
const agentFavoriteSQL = `EXISTS (SELECT 1 FROM openfox.agent_favorites f
		WHERE f.organization_id = a.organization_id
		  AND f.user_id = openfox_private.current_actor_id() AND f.agent_id = a.id)`

// configExtraColumns 读取运行设置；协作模式的历史值 FOLLOW_AGENT 按 FOLLOW_RUNTIME 返回，不改写数据。
const configExtraColumns = `
	COALESCE(c.chat_concurrency, 'DEFAULT'), c.chat_concurrency_value,
	COALESCE(c.workdir_isolation, 'WORKTREE'), COALESCE(c.approval_mode, 'FOLLOW_RUNTIME'),
	CASE COALESCE(c.collaboration_mode, 'FOLLOW_RUNTIME') WHEN 'FOLLOW_AGENT' THEN 'FOLLOW_RUNTIME'
	     ELSE COALESCE(c.collaboration_mode, 'FOLLOW_RUNTIME') END, COALESCE(c.custom_args, '[]'::jsonb),
	COALESCE(c.git_author_name_mode, 'USER_AND_AGENT'), COALESCE(c.git_author_name, ''),
	COALESCE(c.git_author_email_mode, 'USER'), COALESCE(c.git_author_email, ''),
	COALESCE(c.env_digest, ''), c.git_co_author_enabled`

const agentColumns = `
	a.organization_id::text, a.space_id::text, a.id::text, a.owner_user_id::text,
	a.name, a.description, a.kind, a.visibility, a.archived_at,
	COALESCE(a.current_config_version, 0), a.version, a.created_at, a.updated_at,
	COALESCE(c.instructions, ''), COALESCE(c.computer_id::text, ''),
	COALESCE(c.grant_id::text, ''), COALESCE(c.runtime, ''), COALESCE(c.model, ''),
	COALESCE(c.max_concurrency, 0), COALESCE(c.queue_timeout_seconds, 0),
	COALESCE(c.run_timeout_seconds, 0), COALESCE(c.directory_mode, 'NONE'),
	COALESCE(c.directory_roots, '[]'::jsonb), COALESCE(c.created_by::text, ''),
	COALESCE(c.created_at, a.created_at), COALESCE(a.builtin_kind, ''),
	a.title, a.avatar, a.allow_assign, a.allow_direct_chat, a.trigger_crew, a.trigger_mention,
	a.trigger_workflow, a.profile_version, ` + agentOnlineSQL + `, ` + agentComputerNameSQL + `,
	` + agentFavoriteSQL + `,` + configExtraColumns

type rowScanner interface {
	Scan(...any) error
}

func scanAgent(row rowScanner) (Agent, error) {
	var item Agent
	var spaceID, ownerID string
	var kind, visibility string
	var currentVersion, version int64
	var instructions, computerID, grantID, runtime, model, directoryMode, createdBy string
	var maxConcurrency int
	var queueTimeout, runTimeout int64
	var directoryRoots, avatar []byte
	var configCreatedAt time.Time
	var profileVersion int64
	var settings RunSettings
	var customArgs []byte
	var envDigest string
	if err := row.Scan(&item.OrganizationID, &spaceID, &item.ID, &ownerID, &item.Name,
		&item.Description, &kind, &visibility, &item.ArchivedAt, &currentVersion, &version,
		&item.CreatedAt, &item.UpdatedAt, &instructions, &computerID, &grantID, &runtime,
		&model, &maxConcurrency, &queueTimeout, &runTimeout, &directoryMode, &directoryRoots,
		&createdBy, &configCreatedAt, &item.BuiltinKind,
		&item.Title, &avatar, &item.AllowAssign, &item.AllowDirectChat, &item.TriggerCrew,
		&item.TriggerMention, &item.TriggerWorkflow, &profileVersion, &item.Online, &item.ComputerName,
		&item.Favorite, &settings.ChatConcurrency, &settings.ChatConcurrencyValue, &settings.WorkdirIsolation,
		&settings.ApprovalMode, &settings.CollaborationMode, &customArgs, &settings.GitAuthorNameMode,
		&settings.GitAuthorName, &settings.GitAuthorEmailMode, &settings.GitAuthorEmail, &envDigest, &settings.GitCoAuthorEnabled); err != nil {
		return Agent{}, err
	}
	item.SpaceID, item.OwnerUserID = spaceID, ownerID
	item.Kind, item.Visibility = Kind(kind), Visibility(visibility)
	if currentVersion < 0 || version <= 0 || profileVersion <= 0 {
		return Agent{}, ErrInvalidRequest
	}
	item.CurrentConfigVersion, item.Version, item.ProfileVersion = uint64(currentVersion), uint64(version), uint64(profileVersion)
	if len(avatar) > 0 {
		item.Avatar = json.RawMessage(avatar)
	}
	if currentVersion > 0 {
		var roots []string
		if err := json.Unmarshal(directoryRoots, &roots); err != nil {
			return Agent{}, ErrInvalidConfig
		}
		if err := json.Unmarshal(customArgs, &settings.CustomArgs); err != nil {
			return Agent{}, ErrInvalidConfig
		}
		item.Config = &ConfigVersion{
			OrganizationID: item.OrganizationID, AgentID: item.ID, Version: uint64(currentVersion),
			Instructions: instructions, ComputerID: computerID, GrantID: grantID,
			Runtime: runtime, Model: model, MaxConcurrency: maxConcurrency,
			QueueTimeoutSeconds: queueTimeout, RunTimeoutSeconds: runTimeout,
			DirectoryMode: DirectoryMode(directoryMode), DirectoryRoots: roots,
			RunSettings: settings, EnvDigest: envDigest,
			CreatedBy: createdBy, CreatedAt: configCreatedAt,
		}
	}
	return item, nil
}

func scanConfig(row rowScanner) (ConfigVersion, error) {
	var item ConfigVersion
	var version int64
	var directoryMode string
	var directoryRoots, customArgs []byte
	settings := &item.RunSettings
	if err := row.Scan(&item.OrganizationID, &item.AgentID, &version, &item.Instructions,
		&item.ComputerID, &item.GrantID, &item.Runtime, &item.Model, &item.MaxConcurrency,
		&item.QueueTimeoutSeconds, &item.RunTimeoutSeconds, &directoryMode, &directoryRoots,
		&item.CreatedBy, &item.CreatedAt, &settings.ChatConcurrency, &settings.ChatConcurrencyValue,
		&settings.WorkdirIsolation, &settings.ApprovalMode, &settings.CollaborationMode, &customArgs,
		&settings.GitAuthorNameMode, &settings.GitAuthorName, &settings.GitAuthorEmailMode,
		&settings.GitAuthorEmail, &item.EnvDigest, &settings.GitCoAuthorEnabled); err != nil {
		return ConfigVersion{}, err
	}
	if err := json.Unmarshal(customArgs, &settings.CustomArgs); err != nil {
		return ConfigVersion{}, ErrInvalidConfig
	}
	if version <= 0 {
		return ConfigVersion{}, ErrInvalidConfig
	}
	item.Version = uint64(version)
	item.DirectoryMode = DirectoryMode(directoryMode)
	if err := json.Unmarshal(directoryRoots, &item.DirectoryRoots); err != nil {
		return ConfigVersion{}, ErrInvalidConfig
	}
	return item, nil
}

func loadAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID, actorID string, hidePrivate bool) (Agent, error) {
	query := `
		SELECT ` + agentColumns + `
		  FROM openfox.agents a
		  LEFT JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id
		   AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		 WHERE a.organization_id = $1::uuid AND a.space_id = $2::uuid AND a.id = $3::uuid`
	args := []any{organizationID, spaceID, agentID}
	if hidePrivate {
		query += " AND (a.visibility = 'SHARED' OR a.owner_user_id = $4::uuid)"
		args = append(args, actorID)
	}
	item, err := scanAgent(tx.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return item, err
}

func lockAgent(ctx context.Context, tx pgx.Tx, organizationID, spaceID, agentID string) (Agent, error) {
	var item Agent
	var kind, visibility string
	var version, currentVersion int64
	var ownerID, name, description, builtinKind string
	var archivedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text, name, description, kind, visibility, archived_at,
		       COALESCE(current_config_version, 0), version, COALESCE(builtin_kind, '')
		  FROM openfox.agents
		 WHERE organization_id = $1::uuid AND space_id = $2::uuid AND id = $3::uuid
		 FOR UPDATE
	`, organizationID, spaceID, agentID).Scan(&ownerID, &name, &description, &kind, &visibility,
		&archivedAt, &currentVersion, &version, &builtinKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	item = Agent{OrganizationID: organizationID, SpaceID: spaceID, ID: agentID,
		OwnerUserID: ownerID, Name: name, Description: description, Kind: Kind(kind),
		Visibility: Visibility(visibility), BuiltinKind: builtinKind, ArchivedAt: archivedAt,
		CurrentConfigVersion: uint64(currentVersion), Version: uint64(version)}
	return item, nil
}

func insertConfig(ctx context.Context, tx pgx.Tx, organizationID, agentID string, version uint64, actorID string, input ConfigInput) error {
	return insertConfigWithDigest(ctx, tx, organizationID, agentID, version, actorID, input, "")
}

// insertConfigWithDigest 写入一个不可变配置版本；envDigest 由调用方按当前环境变量计算（见 env.go）。
func insertConfigWithDigest(ctx context.Context, tx pgx.Tx, organizationID, agentID string, version uint64, actorID string, input ConfigInput, envDigest string) error {
	roots, err := json.Marshal(input.DirectoryRoots)
	if err != nil {
		return ErrInvalidConfig
	}
	customArgs := input.CustomArgs
	if customArgs == nil {
		customArgs = []string{}
	}
	args, err := json.Marshal(customArgs)
	if err != nil {
		return ErrInvalidConfig
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.agent_config_versions (
			organization_id, agent_id, config_version, instructions, computer_id, grant_id,
			runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds,
			directory_mode, directory_roots, created_by,
			chat_concurrency, chat_concurrency_value, workdir_isolation, approval_mode, collaboration_mode,
			custom_args, git_author_name_mode, git_author_name, git_author_email_mode, git_author_email, env_digest, git_co_author_enabled
		) VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, $6::uuid, $7, $8, $9, $10, $11, $12, $13::jsonb, $14::uuid,
		          $15, $16, $17, $18, $19, $20::jsonb, $21, $22, $23, $24, $25, $26)
	`, organizationID, agentID, int64(version), input.Instructions, input.ComputerID, input.GrantID,
		input.Runtime, input.Model, input.MaxConcurrency, input.QueueTimeoutSeconds,
		input.RunTimeoutSeconds, input.DirectoryMode, roots, actorID,
		input.ChatConcurrency, input.ChatConcurrencyValue, input.WorkdirIsolation, input.ApprovalMode,
		input.CollaborationMode, args, input.GitAuthorNameMode, input.GitAuthorName,
		input.GitAuthorEmailMode, input.GitAuthorEmail, envDigest, input.GitCoAuthorEnabled)
	if err != nil {
		return mapDatabaseError(err)
	}
	return nil
}

func validateConfigTarget(ctx context.Context, tx pgx.Tx, organizationID string, input ConfigInput) error {
	var valid bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM openfox.device_grants g
			  JOIN openfox.physical_devices d ON d.id = g.device_id
			 WHERE g.organization_id = $1::uuid
			   AND g.id = $2::uuid
			   AND g.device_id = $3::uuid
			   AND g.status = 'ACTIVE'
			   AND d.status = 'ACTIVE'
		)
	`, organizationID, input.GrantID, input.ComputerID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrInvalidConfig
	}
	return nil
}

func authorizeSpace(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, action policy.Action, hide bool) error {
	access, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, organizationID, spaceID, actor.UserID)
	if err != nil {
		if errors.Is(err, collaboration.ErrSpaceNotFound) {
			return ErrNotFound
		}
		return err
	}
	request := policy.Request{
		Action: action,
		Actor:  policy.Actor{OrganizationID: organizationID, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive},
		Space:  &policy.Space{OrganizationID: organizationID, ID: spaceID, Visibility: parseSpaceVisibility(access.Space.Visibility)},
	}
	if access.Membership != nil {
		membershipState := policy.MembershipActive
		if access.Membership.Status != "ACTIVE" {
			membershipState = policy.MembershipRevoked
		}
		request.SpaceMembership = &policy.SpaceMembership{
			OrganizationID: organizationID, SpaceID: spaceID, ActorID: actor.UserID,
			Role: parseSpaceRole(access.Membership.Role), Membership: membershipState,
		}
	}
	if policy.Authorize(request).Allowed() {
		return nil
	}
	if hide {
		return ErrNotFound
	}
	return ErrForbidden
}

// requireWritableSpace：停用空间只读，新建 Agent / 小队、复制 Agent 返回 409 SPACE_RETIRED。
// 调用方已完成 authorizeSpace（已删除空间在那里按不存在处理）。
func requireWritableSpace(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
		spaceID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "ARCHIVED" {
		return collaboration.ErrSpaceRetired
	}
	return nil
}

func parseSpaceVisibility(value string) policy.Visibility {
	if strings.EqualFold(value, "SHARED") {
		return policy.VisibilityPublic
	}
	return policy.VisibilityPrivate
}

func parseSpaceRole(value string) policy.SpaceRole {
	switch strings.ToUpper(value) {
	case "OWNER", "EDITOR":
		return policy.SpaceRoleAdministrator
	case "MEMBER":
		return policy.SpaceRoleMember
	case "VIEWER":
		return policy.SpaceRoleVisitor
	default:
		return ""
	}
}

func (s PostgresStore) run(ctx context.Context, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func (s PostgresStore) validate(selection tenancy.OrganizationSelection, actor Actor) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	if _, err := tenancy.NewTenantContext(selection, tenancy.SourceUserRequest); err != nil {
		return ErrInvalidRequest
	}
	if selection.ActorID != actor.UserID || actor.AuthorizationVersion == 0 || actor.Role == "" {
		return ErrInvalidRequest
	}
	return nil
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if err := validateAgentFields(input.Name, input.Description, input.Kind, input.Visibility); err != nil {
		return CreateInput{}, err
	}
	title, err := validateTitle(input.Title)
	if err != nil {
		return CreateInput{}, err
	}
	input.Title = title
	if input.Avatar, err = normalizeAvatar(collaboration.OptionalRaw{Set: len(input.Avatar) > 0, Value: input.Avatar}); err != nil {
		return CreateInput{}, err
	}
	config, err := normalizeConfigInput(input.Config)
	if err != nil {
		return CreateInput{}, err
	}
	input.Config = config
	return input, nil
}

func validateAgentFields(name, description string, kind Kind, visibility Visibility) error {
	if name == "" || len([]rune(name)) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrInvalidRequest
	}
	if len([]rune(description)) > 10000 || strings.IndexFunc(description, unicode.IsControl) >= 0 {
		return ErrInvalidRequest
	}
	if kind != KindCode && kind != KindOffice {
		return ErrInvalidRequest
	}
	if visibility != VisibilityPrivate && visibility != VisibilityShared {
		return ErrInvalidRequest
	}
	return nil
}

func validatePatch(input UpdateInput) error {
	if input.Name != nil && (strings.TrimSpace(*input.Name) == "" || len([]rune(strings.TrimSpace(*input.Name))) > 255) {
		return ErrInvalidRequest
	}
	if input.Description != nil && len([]rune(*input.Description)) > 10000 {
		return ErrInvalidRequest
	}
	if input.Kind != nil && *input.Kind != KindCode && *input.Kind != KindOffice {
		return ErrInvalidRequest
	}
	if input.Visibility != nil && *input.Visibility != VisibilityPrivate && *input.Visibility != VisibilityShared {
		return ErrInvalidRequest
	}
	return nil
}

func normalizeConfigInput(input ConfigInput) (ConfigInput, error) {
	if input.DirectoryRoots == nil {
		input.DirectoryRoots = []string{}
	}
	input.Instructions = strings.TrimSpace(input.Instructions)
	input.ComputerID = strings.ToLower(strings.TrimSpace(input.ComputerID))
	input.GrantID = strings.ToLower(strings.TrimSpace(input.GrantID))
	input.Runtime = strings.TrimSpace(input.Runtime)
	input.Model = strings.TrimSpace(input.Model)
	if input.MaxConcurrency == 0 {
		input.MaxConcurrency = 1
	}
	if input.QueueTimeoutSeconds == 0 {
		input.QueueTimeoutSeconds = 3600
	}
	if input.RunTimeoutSeconds == 0 {
		input.RunTimeoutSeconds = 3600
	}
	if input.DirectoryMode == "" {
		input.DirectoryMode = DirectoryNone
	}
	if len([]rune(input.Instructions)) > 100000 || strings.IndexFunc(input.Instructions, isInstructionControl) >= 0 {
		return ConfigInput{}, ErrInvalidConfig
	}
	if !membership.ValidateUUID(input.ComputerID) || !membership.ValidateUUID(input.GrantID) || input.Runtime == "" || input.Model == "" || len(input.Runtime) > 64 || len(input.Model) > 128 {
		return ConfigInput{}, ErrInvalidConfig
	}
	if strings.IndexFunc(input.Runtime, unicode.IsControl) >= 0 || strings.IndexFunc(input.Model, unicode.IsControl) >= 0 {
		return ConfigInput{}, ErrInvalidConfig
	}
	if input.MaxConcurrency < 1 || input.MaxConcurrency > 16 || input.QueueTimeoutSeconds < 1 || input.QueueTimeoutSeconds > 604800 || input.RunTimeoutSeconds < 60 || input.RunTimeoutSeconds > 28800 {
		return ConfigInput{}, ErrInvalidConfig
	}
	if input.DirectoryMode != DirectoryNone && input.DirectoryMode != DirectoryWorkspace && input.DirectoryMode != DirectoryExplicit {
		return ConfigInput{}, ErrInvalidConfig
	}
	if input.DirectoryMode != DirectoryExplicit && len(input.DirectoryRoots) != 0 {
		return ConfigInput{}, ErrInvalidConfig
	}
	if input.DirectoryMode == DirectoryExplicit && (len(input.DirectoryRoots) == 0 || len(input.DirectoryRoots) > 32) {
		return ConfigInput{}, ErrInvalidConfig
	}
	for index, root := range input.DirectoryRoots {
		root = strings.TrimSpace(root)
		if root == "" || len(root) > 256 || strings.HasPrefix(root, "/") || strings.HasPrefix(root, "~") || strings.Contains(root, "\\") || strings.IndexFunc(root, unicode.IsControl) >= 0 {
			return ConfigInput{}, ErrInvalidConfig
		}
		for _, part := range strings.Split(root, "/") {
			if part == ".." || part == "." || part == "" {
				return ConfigInput{}, ErrInvalidConfig
			}
		}
		input.DirectoryRoots[index] = root
	}
	settings, err := normalizeRunSettings(input.RunSettings)
	if err != nil {
		return ConfigInput{}, err
	}
	input.RunSettings = settings
	return input, nil
}

// isInstructionControl：指令是多行提示词，允许换行和制表符，其余控制字符仍拒绝。
func isInstructionControl(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
}

func normalizeUUID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !membership.ValidateUUID(value) {
		return "", ErrInvalidRequest
	}
	return value, nil
}

func mapDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrNameConflict, pgErr.ConstraintName)
		case "23514":
			return fmt.Errorf("%w: %s", ErrInvalidConfig, pgErr.ConstraintName)
		case "55000":
			return fmt.Errorf("%w: %s", ErrImmutableConfig, pgErr.ConstraintName)
		}
	}
	return err
}

func recordAudit(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, action, agentID, spaceID string, version uint64) error {
	return recordResourceAudit(ctx, tx, selection, actor, "agent", action, agentID, spaceID, version)
}

// recordResourceAudit 按资源类型写审计事件（agent、agent_crew）。
func recordResourceAudit(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, kind, action, resourceID, spaceID string, version uint64) error {
	requestID := strings.TrimSpace(actor.RequestID)
	if requestID == "" {
		requestID = requestid.New()
	}
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: selection.OrganizationID, ActorID: actor.UserID,
		Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: kind, OrganizationID: selection.OrganizationID, SpaceID: spaceID, ID: resourceID},
		RequestID: requestID, AuthorizationVersion: selection.AuthorizationVersion,
		SafeMetadata: map[string]string{"resource_kind": kind, "space_id": spaceID, "version": strconv.FormatUint(version, 10)},
		CreatedAt:    time.Now().UTC(),
	})
}
