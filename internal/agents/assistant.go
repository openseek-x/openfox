package agents

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 首页「小助手」：每个成员在组织内一个内置的私有 Agent（builtinKind = assistant）。
// 第一次选 Runtime 时创建，之后切换 Runtime 只追加配置版本；不出现在目录和空间 Agent 列表里，
// 也不能通过通用的修改接口改名、改可见性或归档。
const (
	BuiltinAssistant = "ASSISTANT"
	assistantName    = "小助手"
	// 候选空间扫描上限：只需要找到一个本人可管理的空间。
	assistantSpaceScan = 50
)

// ErrNoAssistantSpace 表示组织里没有本人可以放置小助手的空间（需要空间管理权限）。
var ErrNoAssistantSpace = errors.New("agents: no space available for the assistant")

// AssistantInput 是小助手运行的电脑与 Runtime。Model 为空时用 Runtime 默认模型。
type AssistantInput struct {
	ComputerID string `json:"computer_id"`
	GrantID    string `json:"grant_id"`
	Runtime    string `json:"runtime"`
	Model      string `json:"model"`
}

// assistantInstructions 是内置指令：约定草稿块协议，中心据此拆出草稿卡，由用户确认后才真正创建。
const assistantInstructions = `你是 OpenFox 的「小助手」，帮当前成员在组织里起草 Agent 和任务，也回答关于 OpenFox 使用的问题。
用中文简洁回答。

当用户想创建 Agent 或任务时，先根据对话整理一份草稿，在回复末尾附上且只附上一个代码块，语言标记写 openfox-action，内容是 JSON：

创建 Agent：
` + "```openfox-action" + `
{"action": "create_agent", "agent": {"name": "名称", "description": "一句话说明", "instructions": "这个 Agent 的工作指令"}}
` + "```" + `

创建任务：
` + "```openfox-action" + `
{"action": "create_task", "task": {"title": "标题", "description": "任务描述"}}
` + "```" + `

规则：
- 草稿只是建议，用户会在卡片上修改并确认后才真正创建。不要声称已经创建成功，也不要编造 ID 或链接。
- 名称不超过 60 个字，标题不超过 120 个字；信息不足时先追问，不要输出草稿块。
- 一次回复最多一个草稿块；普通问答不要输出草稿块。`

// GetAssistant 返回本人的小助手；还没创建时返回 nil。
func (s PostgresStore) GetAssistant(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor) (*Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return nil, err
	}
	var result *Agent
	err := s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		item, err := loadAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		result = &item
		return nil
	})
	return result, err
}

// EnsureAssistant 确保本人的小助手存在并运行在指定的电脑与 Runtime 上：
// 不存在时在本人可管理的空间里创建；已存在时配置不同则追加一个配置版本；已归档则恢复。
// 幂等：同样的输入重复调用不产生新版本。并发创建由 agents_builtin_assistant_unique 兜底。
func (s PostgresStore) EnsureAssistant(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, input AssistantInput) (Agent, error) {
	if err := s.validate(selection, actor); err != nil {
		return Agent{}, err
	}
	config, err := normalizeConfigInput(ConfigInput{
		Instructions: assistantInstructions,
		ComputerID:   input.ComputerID,
		GrantID:      input.GrantID,
		Runtime:      defaultString(input.Runtime, "codex"),
		Model:        defaultString(input.Model, "default"),
	})
	if err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := validateConfigTarget(ctx, tx, selection.OrganizationID, config); err != nil {
			return err
		}
		current, found, err := lockAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
		if err != nil {
			return err
		}
		if !found {
			created, err := insertAssistant(ctx, tx, selection, actor, config)
			if err != nil {
				return err
			}
			if created != "" {
				result, err = loadAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
				return err
			}
			// 并发请求先插入了：改为在它的基础上更新。
			if current, found, err = lockAssistant(ctx, tx, selection.OrganizationID, actor.UserID); err != nil {
				return err
			} else if !found {
				return ErrVersionConflict
			}
		}
		// Runtime changes on the same grant must retain the assistant's approved directory.
		if current.ComputerID == config.ComputerID && current.GrantID == config.GrantID {
			config.DirectoryMode, config.DirectoryRoots, config.WorkdirIsolation = current.DirectoryMode, current.DirectoryRoots, current.Isolation
		}
		if err := updateAssistant(ctx, tx, selection, actor, current, config); err != nil {
			return err
		}
		result, err = loadAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
		return err
	})
	return result, err
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func loadAssistant(ctx context.Context, tx pgx.Tx, organizationID, actorID string) (Agent, error) {
	item, err := scanAgent(tx.QueryRow(ctx, `
		SELECT `+agentColumns+`
		  FROM openfox.agents a
		  LEFT JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id
		   AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		 WHERE a.organization_id = $1::uuid AND a.owner_user_id = $2::uuid
		   AND a.builtin_kind = 'ASSISTANT'`, organizationID, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return item, err
}

// assistantRow 是锁定后比较配置所需的字段。
type assistantRow struct {
	ID, SpaceID                                string
	DirectoryMode                              DirectoryMode
	DirectoryRoots                             []string
	Isolation                                  string
	Version, ConfigVersion                     uint64
	Archived                                   bool
	ComputerID, GrantID, Runtime, Model, Instr string
}

func lockAssistant(ctx context.Context, tx pgx.Tx, organizationID, actorID string) (assistantRow, bool, error) {
	var row assistantRow
	var version, configVersion int64
	err := tx.QueryRow(ctx, `
		SELECT a.id::text, a.space_id::text, a.version, COALESCE(a.current_config_version, 0),
		       a.archived_at IS NOT NULL,
		       COALESCE(c.computer_id::text, ''), COALESCE(c.grant_id::text, ''),
		       COALESCE(c.runtime, ''), COALESCE(c.model, ''), COALESCE(c.instructions, ''), COALESCE(c.directory_mode, 'NONE'), ARRAY(SELECT jsonb_array_elements_text(c.directory_roots)), COALESCE(c.workdir_isolation,'WORKTREE')
		  FROM openfox.agents a
		  LEFT JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id
		   AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		 WHERE a.organization_id = $1::uuid AND a.owner_user_id = $2::uuid
		   AND a.builtin_kind = 'ASSISTANT'
		 FOR UPDATE OF a`, organizationID, actorID).Scan(&row.ID, &row.SpaceID, &version, &configVersion,
		&row.Archived, &row.ComputerID, &row.GrantID, &row.Runtime, &row.Model, &row.Instr, &row.DirectoryMode, &row.DirectoryRoots, &row.Isolation)
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantRow{}, false, nil
	}
	if err != nil {
		return assistantRow{}, false, err
	}
	row.Version, row.ConfigVersion = uint64(version), uint64(configVersion)
	return row, true, nil
}

// insertAssistant 返回新建的 Agent ID；并发下别的请求已建好时返回空串。
func insertAssistant(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, config ConfigInput) (string, error) {
	spaceID, err := pickAssistantSpace(ctx, tx, selection.OrganizationID, actor)
	if err != nil {
		return "", err
	}
	id, err := membership.NewID()
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO openfox.agents (
			organization_id, id, device_id, space_id, owner_user_id, name,
			description, kind, visibility, current_config_version, version, updated_at, builtin_kind
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, '', 'CODE', 'PRIVATE', 1, 1,
		          clock_timestamp(), 'ASSISTANT')
		ON CONFLICT (organization_id, owner_user_id) WHERE builtin_kind = 'ASSISTANT' DO NOTHING`,
		selection.OrganizationID, id, config.ComputerID, spaceID, actor.UserID, assistantName)
	if err != nil {
		return "", mapDatabaseError(err)
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	if err := insertConfig(ctx, tx, selection.OrganizationID, id, 1, actor.UserID, config); err != nil {
		return "", err
	}
	if err := recordAudit(ctx, tx, selection, actor, "agent.created", id, spaceID, 1); err != nil {
		return "", err
	}
	return id, nil
}

func updateAssistant(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, actor Actor, current assistantRow, config ConfigInput) error {
	same := current.ConfigVersion > 0 && current.ComputerID == config.ComputerID && current.GrantID == config.GrantID &&
		current.Runtime == config.Runtime && current.Model == config.Model && current.Instr == config.Instructions && current.DirectoryMode == config.DirectoryMode && slices.Equal(current.DirectoryRoots, config.DirectoryRoots) && current.Isolation == config.WorkdirIsolation
	if same && !current.Archived {
		return nil
	}
	nextConfig := current.ConfigVersion
	if !same {
		nextConfig++
		if err := insertConfig(ctx, tx, selection.OrganizationID, current.ID, nextConfig, actor.UserID, config); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.agents
		   SET device_id = $1::uuid, current_config_version = $2, archived_at = NULL,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = $3::uuid AND id = $4::uuid AND version = $5`,
		config.ComputerID, int64(nextConfig), selection.OrganizationID, current.ID, int64(current.Version))
	if err != nil {
		return mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return recordAudit(ctx, tx, selection, actor, "agent.config_created", current.ID, current.SpaceID, current.Version+1)
}

// pickAssistantSpace 选一个本人可管理的活跃空间放小助手：本人担任管理员的私有空间优先，其次按创建时间。
func pickAssistantSpace(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text
		  FROM openfox.spaces s
		  LEFT JOIN openfox.space_members m
		    ON m.organization_id = s.organization_id AND m.space_id = s.id
		   AND m.user_id = $2::uuid AND m.status = 'ACTIVE'
		 WHERE s.organization_id = $1::uuid AND s.status = 'ACTIVE'
		 ORDER BY COALESCE(m.role IN ('OWNER', 'EDITOR'), false) DESC,
		          (s.visibility = 'PRIVATE') DESC, s.created_at, s.id
		 LIMIT $3`, organizationID, actor.UserID, assistantSpaceScan)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, assistantSpaceScan)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		candidates = append(candidates, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, spaceID := range candidates {
		err := authorizeSpace(ctx, tx, organizationID, spaceID, actor, policy.ActionManageSpace, false)
		if err == nil {
			return spaceID, nil
		}
		if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return "", err
		}
	}
	return "", ErrNoAssistantSpace
}
