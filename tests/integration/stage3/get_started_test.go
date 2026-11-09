package stage3

// 开始使用进度的集成测试：夹具的初始进度、写入真实数据后步骤完成、组织成员访问控制。
// 夹具（newS3Env）里组织有 6 个 ACTIVE 成员，因此「邀请成员」开始时已完成。

import (
	"net/http"
	"testing"
)

type getStartedView struct {
	Score      int `json:"score"`
	Total      int `json:"total"`
	Categories []struct {
		Key   string `json:"key"`
		Steps []struct {
			Key       string `json:"key"`
			Points    int    `json:"points"`
			Completed bool   `json:"completed"`
			Available bool   `json:"available"`
		} `json:"steps"`
	} `json:"categories"`
}

// step 返回指定 key 的步骤；找不到时测试失败。
func (v getStartedView) step(t *testing.T, key string) (completed, available bool) {
	t.Helper()
	for _, category := range v.Categories {
		for _, step := range category.Steps {
			if step.Key == key {
				return step.Completed, step.Available
			}
		}
	}
	t.Fatalf("step %s is missing", key)
	return false, false
}

// availablePoints 返回所有可用步骤的分值和（不依赖完成状态）。
func (v getStartedView) availablePoints() (total int) {
	for _, category := range v.Categories {
		for _, step := range category.Steps {
			if step.Available {
				total += step.Points
			}
		}
	}
	return total
}

// completedPoints 返回已完成且可用步骤的分值和。
func (v getStartedView) completedPoints() (total int) {
	for _, category := range v.Categories {
		for _, step := range category.Steps {
			if step.Available && step.Completed {
				total += step.Points
			}
		}
	}
	return total
}

func getStartedFor(t *testing.T, client *s3Client) getStartedView {
	t.Helper()
	var view getStartedView
	client.must(http.StatusOK, http.MethodGet, "get-started", nil, &view)
	return view
}

func TestGetStartedFixtureProgress(t *testing.T) {
	env := newS3Env(t)
	view := getStartedFor(t, env.owner())

	wantCategories := []string{"get_started", "collaboration", "agents", "tasks", "space_management", "automations"}
	wantSteps := []int{5, 5, 6, 8, 6, 6}
	if len(view.Categories) != len(wantCategories) {
		t.Fatalf("categories = %d, want %d", len(view.Categories), len(wantCategories))
	}
	for index, category := range view.Categories {
		if category.Key != wantCategories[index] || len(category.Steps) != wantSteps[index] {
			t.Fatalf("category %d = %s with %d steps, want %s with %d",
				index, category.Key, len(category.Steps), wantCategories[index], wantSteps[index])
		}
	}

	// 可用分值：140 + 70 + 95 + 110 + 105 + 90 = 610。
	// install_skill / attach_skill_to_agent 可用（各 15 分），总分 440 → 470；
	// 导入（connect_jira_or_tapd 30）、飞书（connect_feishu 20）、自动化 6 步（90）可用后，总分 470 → 610。
	if view.Total != 610 || view.availablePoints() != 610 {
		t.Fatalf("total = %d, want 610", view.Total)
	}
	// 夹具只有 6 个成员（邀请成员 20 分已完成），没有任务、Agent、设备、视图、迭代等数据。
	if view.Score != 20 || view.completedPoints() != 20 {
		t.Fatalf("score = %d, want 20 from the invite-member step only", view.Score)
	}
	if completed, available := view.step(t, "invite_member"); !completed || !available {
		t.Fatal("invite_member must be completed by the six-member fixture")
	}
	for _, key := range []string{"connect_machine", "create_agent", "create_task", "manage_task_status", "enable_space_estimation", "install_skill", "attach_skill_to_agent"} {
		if completed, available := view.step(t, key); completed || !available {
			t.Fatalf("%s = completed %v available %v, want not completed and available", key, completed, available)
		}
	}
	for _, key := range []string{"connect_jira_or_tapd", "connect_feishu", "create_automation", "run_automation",
		"create_scheduled_automation", "create_task_status_automation", "create_comment_automation", "create_webhook_automation"} {
		if completed, available := view.step(t, key); completed || !available {
			t.Fatalf("%s = completed %v available %v, want available and not completed", key, completed, available)
		}
	}
}

func TestGetStartedStepsCompleteFromData(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	suffix := s3Suffix(t)

	// 任务、子任务、人工指派、关注者、评论、关联。
	task := env.createTask(owner, env.space.ID, map[string]any{"assignee_user_id": s3MemberID})
	child := env.createTask(owner, env.space.ID, map[string]any{"parent_task_id": task.ID})
	second := env.createTask(owner, env.space.ID, nil)
	// 创建人与指派人会自动成为关注者（产品行为），因此「关注任务」由创建任务直接完成，这里不再手动插入。
	env.exec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'looks good')`, env.org, env.space.ID, task.ID, s3OwnerID)
	env.exec(`INSERT INTO openfox.task_relations (organization_id, source_space_id, source_task_id, target_space_id, target_task_id, builtin_type, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $2::uuid, $4::uuid, 'RELATES', $5::uuid)`, env.org, env.space.ID, task.ID, second.ID, s3OwnerID)

	// 标签、项目（并关联任务）、视图（个人与空间）、迭代、状态变更、估算、代码仓库。
	env.exec(`INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, $2::uuid, $3)`,
		env.org, env.space.ID, "label "+suffix)
	var projectID string
	env.scalar(&projectID, `INSERT INTO openfox.projects (organization_id, name, created_by)
		VALUES ($1::uuid, $2, $3::uuid) RETURNING id::text`, env.org, "Project "+suffix, s3OwnerID)
	env.exec(`UPDATE openfox.tasks SET project_id = $2::uuid WHERE id = $1::uuid`, second.ID, projectID)
	env.exec(`INSERT INTO openfox.views (organization_id, space_id, name, created_by) VALUES ($1::uuid, $2::uuid, $3, $4::uuid)`,
		env.org, env.space.ID, "Personal "+suffix, s3OwnerID)
	env.exec(`INSERT INTO openfox.views (organization_id, space_id, name, created_by, visibility)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, 'space')`, env.org, env.space.ID, "Shared "+suffix, s3OwnerID)
	env.exec(`INSERT INTO openfox.iterations (organization_id, space_id, number, starts_at, ends_at)
		VALUES ($1::uuid, $2::uuid, 1, clock_timestamp(), clock_timestamp() + interval '14 days')`, env.org, env.space.ID)
	env.exec(`UPDATE openfox.task_statuses SET version = 2 WHERE organization_id = $1::uuid AND category = 'TODO'`, env.org)
	env.exec(`UPDATE openfox.spaces SET estimation_scale = 'fibonacci' WHERE organization_id = $1::uuid AND id = $2::uuid`,
		env.org, env.space.ID)
	env.exec(`INSERT INTO openfox.space_repos (organization_id, space_id, url, created_by)
		VALUES ($1::uuid, $2::uuid, 'https://example.test/repo.git', $3::uuid)`, env.org, env.space.ID, s3OwnerID)

	// 设备、Agent（含指令）、Agent 委派、与 Agent 的会话、Agent 团队。
	deviceID := s3NewID(t)
	env.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES ($1::uuid, $2, 1)`,
		deviceID, "gs-device-"+suffix)
	env.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, env.org, deviceID)
	var grantID string
	env.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`,
		env.org, deviceID)
	agentID := s3NewID(t)
	env.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'Agent', 'SHARED', 1)`,
		env.org, agentID, deviceID, env.space.ID, s3OwnerID)
	env.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
			runtime, model, created_by, instructions)
		VALUES ($1::uuid, $2::uuid, 1, $3::uuid, $4::uuid, 'codex', 'default', $5::uuid, 'Follow the checklist.')`,
		env.org, agentID, deviceID, grantID, s3OwnerID)
	env.exec(`INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, agent_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'AGENT', $4::uuid, $5::uuid)`, env.org, env.space.ID, child.ID, agentID, s3OwnerID)
	env.exec(`INSERT INTO openfox.conversations (organization_id, space_id, agent_id, owner_user_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`, env.org, env.space.ID, agentID, s3OwnerID)
	env.exec(`INSERT INTO openfox.agent_crews (organization_id, space_id, owner_user_id, name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`, env.org, env.space.ID, s3OwnerID, "Crew "+suffix)

	// 技能：空间里有一个技能（安装技能），并装配到 Agent（装配技能到 Agent）。
	var skillID string
	env.scalar(&skillID, `INSERT INTO openfox.skills (organization_id, space_id, name, source_kind, created_by, updated_by)
		VALUES ($1::uuid, $2::uuid, $3, 'manual', $4::uuid, $4::uuid) RETURNING id::text`, env.org, env.space.ID, "gs-"+suffix, s3OwnerID)
	env.exec(`INSERT INTO openfox.agent_skills (organization_id, agent_id, skill_id, space_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)`, env.org, agentID, skillID, env.space.ID, s3OwnerID)

	// 收件箱：一条已读，一条已清除。
	env.exec(`INSERT INTO openfox.notifications (organization_id, recipient_user_id, kind, dedupe_key, safe_summary, task_id, read_at)
		VALUES ($1::uuid, $2::uuid, 'TASK_ASSIGNED', 'read-' || $3, 'assigned', $4::uuid, clock_timestamp())`,
		env.org, s3OwnerID, suffix, task.ID)
	env.exec(`INSERT INTO openfox.notifications (organization_id, recipient_user_id, kind, dedupe_key, safe_summary, task_id, cleared_at)
		VALUES ($1::uuid, $2::uuid, 'MENTION', 'cleared-' || $3, 'mentioned', $4::uuid, clock_timestamp())`,
		env.org, s3OwnerID, suffix, task.ID)

	// 导入：一次成功的 Jira 导入。
	env.exec(`INSERT INTO openfox.import_jobs (organization_id, space_id, source, status, operation_id, created_by, started_by, started_at, finished_at)
		VALUES ($1::uuid, $2::uuid, 'jira_csv', 'SUCCEEDED', $3, $4::uuid, $4::uuid, clock_timestamp(), clock_timestamp())`,
		env.org, env.space.ID, "op_getstarted01", s3OwnerID)
	// 飞书：组织绑定了可用的飞书应用。
	env.exec(`INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, created_by)
		VALUES ($1::uuid, 'feishu', 'ORGANIZATION', $2, 'OpenFox', 'app', $3::uuid)`, env.org, "cli_gs"+suffix, s3OwnerID)
	// 自动化：一个自动化，带定时 / 任务状态 / 评论 / Webhook 四种触发器（「运行一次自动化」需要真实运行，不在这里覆盖）。
	var automationID string
	env.scalar(&automationID, `INSERT INTO openfox.automations (organization_id, space_id, title, prompt, assignee_kind, assignee_id, created_by)
		VALUES ($1::uuid, $2::uuid, 'Daily', 'Summarize.', 'AGENT', $3::uuid, $4::uuid) RETURNING id::text`,
		env.org, env.space.ID, agentID, s3OwnerID)
	for _, kind := range []string{"schedule", "task_status", "comment"} {
		env.exec(`INSERT INTO openfox.automation_triggers (organization_id, automation_id, space_id, kind)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`, env.org, automationID, env.space.ID, kind)
	}
	env.exec(`INSERT INTO openfox.automation_triggers (organization_id, automation_id, space_id, kind, webhook_token_hash)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'webhook', sha256(convert_to($4, 'UTF8')))`, env.org, automationID, env.space.ID, "gs-hook-"+suffix)

	view := getStartedFor(t, owner)
	// 只有「配置任务类型字段」未覆盖（需要先建任务类型与自定义字段，见本文件说明），其余可用步骤都应完成。
	for _, key := range []string{
		"connect_machine", "create_agent", "create_task", "delegate_task_to_agent", "invite_member",
		"assign_task_person", "comment_or_reply", "process_inbox", "clear_inbox",
		"configure_agent_instructions", "chat_with_agent", "share_agent_to_space", "create_crew",
		"follow_task", "add_label", "create_subtask", "link_related_task", "create_view", "save_org_view",
		"create_project", "link_task_to_project",
		"manage_task_status", "create_sprint", "enable_space_estimation", "connect_git",
		"install_skill", "attach_skill_to_agent",
		"connect_jira_or_tapd", "connect_feishu", "create_automation", "create_scheduled_automation",
		"create_task_status_automation", "create_comment_automation", "create_webhook_automation",
	} {
		if completed, available := view.step(t, key); !completed || !available {
			t.Fatalf("%s = completed %v available %v, want completed", key, completed, available)
		}
	}
	for _, key := range []string{"configure_task_type_field", "run_automation"} {
		if completed, _ := view.step(t, key); completed {
			t.Fatalf("%s must stay open without matching data", key)
		}
	}
	// 未覆盖：配置任务类型字段（15）与运行一次自动化（15）。
	if view.Score != view.Total-30 || view.completedPoints() != view.Score {
		t.Fatalf("score = %d total = %d, want total minus the two open 15-point steps", view.Score, view.Total)
	}
}

func TestGetStartedRequiresOrganizationMembership(t *testing.T) {
	env := newS3Env(t)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, "get-started", nil)
	for _, client := range []*s3Client{env.owner(), env.member(), env.guest()} {
		client.must(http.StatusOK, http.MethodGet, "get-started", nil, nil)
	}
	// 组织外的账号即使带了本组织的 ID 也拿不到进度。
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, "/v1/organizations/"+env.org+"/get-started", nil)
}
