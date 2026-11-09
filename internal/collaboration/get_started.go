package collaboration

// 开始使用进度。完成判定只读现有数据，全部在当前组织的租户事务里用一条 SQL 返回。
// 不可用步骤（依赖的功能还没上线）的完成状态恒为 false，且不计入 score 与 total。

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type GetStartedStep struct {
	Key       string `json:"key"`
	Points    int    `json:"points"`
	Completed bool   `json:"completed"`
	Available bool   `json:"available"`
}

type GetStartedCategory struct {
	Key   string           `json:"key"`
	Steps []GetStartedStep `json:"steps"`
}

type GetStartedProgress struct {
	Score      int                  `json:"score"`
	Total      int                  `json:"total"`
	Categories []GetStartedCategory `json:"categories"`
}

// getStartedStep 是一个步骤的定义。Done 是完成判定的布尔 SQL 表达式，$1 = 组织 ID，$2 = 当前用户 ID；不可用步骤没有判定。
type getStartedStep struct {
	key       string
	points    int
	available bool
	done      string
}

type getStartedCategoryDef struct {
	key   string
	steps []getStartedStep
}

// getStartedCatalog 的分类与步骤顺序即接口返回顺序。
var getStartedCatalog = []getStartedCategoryDef{
	{key: "get_started", steps: []getStartedStep{
		{key: "connect_machine", points: 30, available: true, done: `EXISTS (SELECT 1 FROM openfox.device_grants
			WHERE organization_id = $1::uuid AND status = 'ACTIVE')`},
		{key: "create_agent", points: 30, available: true, done: `EXISTS (SELECT 1 FROM openfox.agents
			WHERE organization_id = $1::uuid AND archived_at IS NULL)`},
		{key: "create_task", points: 25, available: true, done: `EXISTS (SELECT 1 FROM openfox.tasks
			WHERE organization_id = $1::uuid AND deleted_at IS NULL)`},
		{key: "delegate_task_to_agent", points: 25, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_assignees a
			JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id
			WHERE a.organization_id = $1::uuid AND a.kind = 'AGENT' AND t.deleted_at IS NULL)`},
		// 成功完成过一次导入（Jira CSV / Jira API）。
		{key: "connect_jira_or_tapd", points: 30, available: true, done: `EXISTS (SELECT 1 FROM openfox.import_jobs
			WHERE organization_id = $1::uuid AND status = 'SUCCEEDED' AND source <> 'space_clone')`},
	}},
	{key: "collaboration", steps: []getStartedStep{
		// 至少两个 ACTIVE 成员（OFFSET 1 之后还有行），或有过邀请。
		{key: "invite_member", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.organization_members
			WHERE organization_id = $1::uuid AND status = 'ACTIVE' OFFSET 1)
			OR EXISTS (SELECT 1 FROM openfox.invitations WHERE organization_id = $1::uuid)`},
		{key: "assign_task_person", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_assignees a
			JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id
			WHERE a.organization_id = $1::uuid AND a.kind = 'HUMAN' AND t.deleted_at IS NULL)`},
		{key: "comment_or_reply", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_comments c
			JOIN openfox.tasks t ON t.organization_id = c.organization_id AND t.id = c.task_id
			WHERE c.organization_id = $1::uuid AND t.deleted_at IS NULL)`},
		{key: "process_inbox", points: 10, available: true, done: `EXISTS (SELECT 1 FROM openfox.notifications
			WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND read_at IS NOT NULL)`},
		{key: "clear_inbox", points: 10, available: true, done: `EXISTS (SELECT 1 FROM openfox.notifications
			WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND cleared_at IS NOT NULL)`},
	}},
	{key: "agents", steps: []getStartedStep{
		{key: "configure_agent_instructions", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.agents a
			JOIN openfox.agent_config_versions v ON v.organization_id = a.organization_id AND v.agent_id = a.id
			 AND v.config_version = a.current_config_version
			WHERE a.organization_id = $1::uuid AND btrim(v.instructions) <> '')`},
		{key: "chat_with_agent", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.conversations
			WHERE organization_id = $1::uuid AND agent_id IS NOT NULL)`},
		{key: "share_agent_to_space", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.agents
			WHERE organization_id = $1::uuid AND visibility <> 'PRIVATE' AND archived_at IS NULL)`},
		{key: "create_crew", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.agent_crews
			WHERE organization_id = $1::uuid)`},
		{key: "install_skill", points: 15, available: true, done: `(EXISTS (SELECT 1 FROM openfox.skills WHERE organization_id=$1::uuid) OR EXISTS (SELECT 1 FROM openfox.agent_market_skill_installs WHERE organization_id=$1::uuid))`},
		{key: "attach_skill_to_agent", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.agent_skill_loadout
			WHERE organization_id = $1::uuid)`},
	}},
	{key: "tasks", steps: []getStartedStep{
		{key: "follow_task", points: 10, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_followers f
			JOIN openfox.tasks t ON t.organization_id = f.organization_id AND t.id = f.task_id
			WHERE f.organization_id = $1::uuid AND t.deleted_at IS NULL)`},
		{key: "add_label", points: 10, available: true, done: `EXISTS (SELECT 1 FROM openfox.labels
			WHERE organization_id = $1::uuid AND archived_at IS NULL)`},
		{key: "create_subtask", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.tasks
			WHERE organization_id = $1::uuid AND parent_task_id IS NOT NULL AND deleted_at IS NULL)`},
		{key: "link_related_task", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_relations r
			JOIN openfox.tasks t ON t.organization_id = r.organization_id AND t.id = r.source_task_id
			WHERE r.organization_id = $1::uuid AND t.deleted_at IS NULL)`},
		{key: "create_view", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.views
			WHERE organization_id = $1::uuid)`},
		{key: "save_org_view", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.views
			WHERE organization_id = $1::uuid AND visibility = 'space')`},
		{key: "create_project", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.projects
			WHERE organization_id = $1::uuid)`},
		{key: "link_task_to_project", points: 10, available: true, done: `EXISTS (SELECT 1 FROM openfox.tasks
			WHERE organization_id = $1::uuid AND project_id IS NOT NULL AND deleted_at IS NULL)`},
	}},
	{key: "space_management", steps: []getStartedStep{
		{key: "configure_task_type_field", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_type_fields
			WHERE organization_id = $1::uuid)`},
		// 改过状态（version > 1），或组织创建一分钟之后新建了状态。
		{key: "manage_task_status", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.task_statuses
			WHERE organization_id = $1::uuid AND (version > 1
			   OR created_at > (SELECT o.created_at FROM openfox.organizations o WHERE o.id = $1::uuid) + interval '1 minute'))`},
		{key: "create_sprint", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.iterations
			WHERE organization_id = $1::uuid)`},
		{key: "enable_space_estimation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.spaces
			WHERE organization_id = $1::uuid AND deleted_at IS NULL AND estimation_scale <> 'notUsed')`},
		{key: "connect_git", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.space_repos
			WHERE organization_id = $1::uuid)`},
		// 组织绑定了可用的飞书应用，或有未移除的飞书机器人（任务助手 / Agent）。
		{key: "connect_feishu", points: 20, available: true, done: `EXISTS (SELECT 1 FROM openfox.integration_connections
			WHERE organization_id = $1::uuid AND provider IN ('feishu', 'lark', 'dingtalk') AND scope = 'ORGANIZATION' AND account_kind = 'app' AND status = 'ACTIVE')
			OR EXISTS (SELECT 1 FROM openfox.im_bots WHERE organization_id = $1::uuid AND removed_at IS NULL)`},
	}},
	{key: "automations", steps: []getStartedStep{
		// 自动化与触发器（kind = schedule / task_status / comment / webhook）；运行过 = 有一次成功开始的运行记录。
		{key: "create_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automations
			WHERE organization_id = $1::uuid)`},
		{key: "run_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automation_runs
			WHERE organization_id = $1::uuid AND status = 'started')`},
		{key: "create_scheduled_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automation_triggers
			WHERE organization_id = $1::uuid AND kind = 'schedule')`},
		{key: "create_task_status_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automation_triggers
			WHERE organization_id = $1::uuid AND kind = 'task_status')`},
		{key: "create_comment_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automation_triggers
			WHERE organization_id = $1::uuid AND kind = 'comment')`},
		{key: "create_webhook_automation", points: 15, available: true, done: `EXISTS (SELECT 1 FROM openfox.automation_triggers
			WHERE organization_id = $1::uuid AND kind = 'webhook')`},
	}},
}

// getStartedQuery 把所有可用步骤的判定拼成一条 SELECT，每列别名为 c<下标>；列内容只来自上面的常量片段。
var getStartedQuery = buildGetStartedQuery()

func buildGetStartedQuery() string {
	columns := []string{}
	for _, category := range getStartedCatalog {
		for _, step := range category.steps {
			if step.available {
				columns = append(columns, fmt.Sprintf("(%s) AS c%d", step.done, len(columns)))
			}
		}
	}
	return "SELECT " + strings.Join(columns, ", ")
}

// LoadGetStarted 计算组织的开始使用进度。调用方须已在该组织的租户事务里。
func (s Service) LoadGetStarted(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (GetStartedProgress, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return GetStartedProgress{}, err
	}
	done, err := loadGetStartedDone(ctx, tx, organizationID, actor.UserID)
	if err != nil {
		return GetStartedProgress{}, err
	}
	progress := GetStartedProgress{Categories: make([]GetStartedCategory, 0, len(getStartedCatalog))}
	index := 0
	for _, category := range getStartedCatalog {
		item := GetStartedCategory{Key: category.key, Steps: make([]GetStartedStep, 0, len(category.steps))}
		for _, step := range category.steps {
			completed := false
			if step.available {
				completed = done[index]
				index++
				progress.Total += step.points
				if completed {
					progress.Score += step.points
				}
			}
			item.Steps = append(item.Steps, GetStartedStep{
				Key: step.key, Points: step.points, Completed: completed, Available: step.available,
			})
		}
		progress.Categories = append(progress.Categories, item)
	}
	return progress, nil
}

// loadGetStartedDone 执行 getStartedQuery，返回可用步骤的完成状态（顺序与可用步骤一致）。
func loadGetStartedDone(ctx context.Context, tx pgx.Tx, organizationID, userID string) ([]bool, error) {
	count := 0
	for _, category := range getStartedCatalog {
		for _, step := range category.steps {
			if step.available {
				count++
			}
		}
	}
	done := make([]bool, count)
	targets := make([]any, count)
	for i := range done {
		targets[i] = &done[i]
	}
	if err := tx.QueryRow(ctx, getStartedQuery, organizationID, userID).Scan(targets...); err != nil {
		return nil, err
	}
	return done, nil
}
