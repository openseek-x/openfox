package automations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/secrets"
)

// Service 管理自动化的增删改查与重排。调用方负责空间授权（读 / 写）并传入 canEdit。
// 所有方法都在租户事务里执行。
type Service struct {
	// Box 为 nil 时签名密钥相关操作返回 SECRETS_UNAVAILABLE。
	Box *secrets.Box
	// PublicURL 是中心对外地址，用于拼接 Webhook URL。
	PublicURL string
	Now       func() time.Time
}

// Actor 是发起写操作的成员；自动化的创建者与会话所有者都是他。
type Actor struct {
	UserID string
}

// UpdateInput 是 PATCH 请求体。nil 表示不修改；Triggers 出现时整体替换。
type UpdateInput struct {
	Version       uint64          `json:"version"`
	Title         *string         `json:"title"`
	Prompt        *string         `json:"prompt"`
	Assignee      *AssigneeInput  `json:"assignee"`
	NotifyUserIDs *[]string       `json:"notify_user_ids"`
	Enabled       *bool           `json:"enabled"`
	Triggers      *[]TriggerInput `json:"triggers"`
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// List 返回空间全部自动化。
func (s Service) List(ctx context.Context, tx pgx.Tx, spaceID string, canEdit bool) ([]Automation, error) {
	items, err := loadAutomations(ctx, tx, `AND a.space_id = $1::uuid`, spaceID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].CanEdit = canEdit
	}
	return items, nil
}

// Get 返回单个自动化。
func (s Service) Get(ctx context.Context, tx pgx.Tx, spaceID, automationID string, canEdit bool) (Automation, error) {
	item, err := loadOneAutomation(ctx, tx, spaceID, automationID)
	if err != nil {
		return Automation{}, err
	}
	item.CanEdit = canEdit
	return item, nil
}

// Create 新建自动化。锁空间行后计数，保证并发创建不会超过每个空间 100 个的上限。
func (s Service) Create(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, input AutomationInput) (SaveResult, error) {
	input.Title = strings.TrimSpace(input.Title)
	if err := validateFields(input.Title, input.Prompt, input.Assignee.Kind, input.Assignee.ID, input.NotifyUserIDs, input.Triggers); err != nil {
		return SaveResult{}, err
	}
	if err := lockSpace(ctx, tx, spaceID); err != nil {
		return SaveResult{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.automations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID).Scan(&count); err != nil {
		return SaveResult{}, err
	}
	if count >= maxAutomationsPerSpace {
		return SaveResult{}, ErrLimitReached
	}
	if err := s.checkAssignee(ctx, tx, spaceID, actor.UserID, input.Assignee); err != nil {
		return SaveResult{}, err
	}
	if err := checkExecutorDirectChat(ctx, tx, input.Assignee); err != nil {
		return SaveResult{}, err
	}
	if err := checkNotifyUsers(ctx, tx, spaceID, input.NotifyUserIDs); err != nil {
		return SaveResult{}, err
	}
	if err := checkStatusIDs(ctx, tx, spaceID, input.Triggers); err != nil {
		return SaveResult{}, err
	}
	automationID, err := newUUID()
	if err != nil {
		return SaveResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.automations (
			organization_id, id, space_id, title, prompt, assignee_kind, assignee_id,
			notify_user_ids, enabled, position, created_by
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid[], $8,
		          (SELECT COALESCE(MAX(position) + 1, 0) FROM openfox.automations
		            WHERE organization_id = openfox_private.current_organization_id() AND space_id = $2::uuid),
		          $9::uuid)`,
		automationID, spaceID, input.Title, input.Prompt, input.Assignee.Kind, input.Assignee.ID,
		uniqueStrings(input.NotifyUserIDs), true, actor.UserID); err != nil {
		return SaveResult{}, err
	}
	var urls []WebhookURLItem
	for position, t := range input.Triggers {
		item, err := s.insertTrigger(ctx, tx, spaceID, automationID, position, t)
		if err != nil {
			return SaveResult{}, err
		}
		if item != nil {
			urls = append(urls, *item)
		}
	}
	automation, err := loadOneAutomation(ctx, tx, spaceID, automationID)
	if err != nil {
		return SaveResult{}, err
	}
	automation.CanEdit = true
	return SaveResult{Automation: automation, WebhookURLs: nonNilURLs(urls)}, nil
}

// Update 修改自动化（乐观锁 version）。triggers 出现时整体替换：带 id 的保留（Webhook URL 与密钥不变），
// 不带 id 的新建，缺少的删除。
func (s Service) Update(ctx context.Context, tx pgx.Tx, spaceID, automationID string, actor Actor, input UpdateInput) (SaveResult, error) {
	current, err := lockAutomation(ctx, tx, spaceID, automationID)
	if err != nil {
		return SaveResult{}, err
	}
	if current.Version != input.Version {
		return SaveResult{}, ErrVersionConflict
	}
	if input.Title != nil {
		trimmed := strings.TrimSpace(*input.Title)
		input.Title = &trimmed
	}
	title, prompt := current.Title, current.Prompt
	if input.Title != nil {
		title = *input.Title
	}
	if input.Prompt != nil {
		prompt = *input.Prompt
	}
	assignee := AssigneeInput{Kind: current.Assignee.Kind, ID: current.Assignee.ID}
	if input.Assignee != nil {
		assignee = *input.Assignee
	}
	notify := current.NotifyUserIDs
	if input.NotifyUserIDs != nil {
		notify = *input.NotifyUserIDs
	}
	triggers := []TriggerInput(nil)
	if input.Triggers != nil {
		triggers = *input.Triggers
	}
	if err := validateHead(title, prompt, assignee.Kind, assignee.ID, notify); err != nil {
		return SaveResult{}, err
	}
	if input.Triggers != nil {
		if err := validateTriggerList(triggers); err != nil {
			return SaveResult{}, err
		}
	}
	if input.Assignee != nil {
		if err := s.checkAssignee(ctx, tx, spaceID, actor.UserID, assignee); err != nil {
			return SaveResult{}, err
		}
	}
	// 每次保存都校验当前执行者的单聊开关，关闭单聊的执行者不能再保存。
	if err := checkExecutorDirectChat(ctx, tx, assignee); err != nil {
		return SaveResult{}, err
	}
	if input.NotifyUserIDs != nil {
		if err := checkNotifyUsers(ctx, tx, spaceID, notify); err != nil {
			return SaveResult{}, err
		}
	}
	var urls []WebhookURLItem
	if input.Triggers != nil {
		if err := checkStatusIDs(ctx, tx, spaceID, triggers); err != nil {
			return SaveResult{}, err
		}
		urls, err = s.replaceTriggers(ctx, tx, spaceID, automationID, triggers)
		if err != nil {
			return SaveResult{}, err
		}
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.automations
		   SET title = $3, prompt = $4, assignee_kind = $5, assignee_id = $6::uuid, notify_user_ids = $7::uuid[],
		       enabled = $8, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		spaceID, automationID, title, prompt, assignee.Kind, assignee.ID, uniqueStrings(notify), enabled); err != nil {
		return SaveResult{}, err
	}
	automation, err := loadOneAutomation(ctx, tx, spaceID, automationID)
	if err != nil {
		return SaveResult{}, err
	}
	automation.CanEdit = true
	return SaveResult{Automation: automation, WebhookURLs: nonNilURLs(urls)}, nil
}

// Delete 删除自动化，连同触发器、运行记录与投递记录（外键级联）；已创建的会话保留。
func (s Service) Delete(ctx context.Context, tx pgx.Tx, spaceID, automationID string, version uint64) error {
	current, err := lockAutomation(ctx, tx, spaceID, automationID)
	if err != nil {
		return err
	}
	if current.Version != version {
		return ErrVersionConflict
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM openfox.automations
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		spaceID, automationID)
	return err
}

// Reorder 调整顺序。automationIDs 必须恰好是空间的全部自动化；锁空间行后比较集合。
func (s Service) Reorder(ctx context.Context, tx pgx.Tx, spaceID string, automationIDs []string) error {
	if err := lockSpace(ctx, tx, spaceID); err != nil {
		return err
	}
	if len(automationIDs) > maxAutomationsPerSpace {
		return invalidf("自动化最多 %d 个", maxAutomationsPerSpace)
	}
	seen := make(map[string]bool, len(automationIDs))
	for _, id := range automationIDs {
		if !membership.ValidateUUID(id) || seen[id] {
			return invalidf("自动化 ID 无效或重复")
		}
		seen[id] = true
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM openfox.automations
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if !seen[id] {
			return ErrListChanged
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if count != len(automationIDs) {
		return ErrListChanged
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.automations a
		   SET position = (u.ord - 1)::int
		  FROM unnest($1::uuid[]) WITH ORDINALITY AS u(id, ord)
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.space_id = $2::uuid AND a.id = u.id`,
		automationIDs, spaceID)
	return err
}

// replaceTriggers 按 id 对齐现有触发器：保留的更新配置，新建的插入，缺少的删除。
func (s Service) replaceTriggers(ctx context.Context, tx pgx.Tx, spaceID, automationID string, inputs []TriggerInput) ([]WebhookURLItem, error) {
	existing := map[string]string{}
	rows, err := tx.Query(ctx, `
		SELECT id::text, kind FROM openfox.automation_triggers
		 WHERE organization_id = openfox_private.current_organization_id() AND automation_id = $1::uuid`, automationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		existing[id] = kind
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	keep := make([]string, 0, len(inputs))
	for _, t := range inputs {
		if t.ID == "" {
			continue
		}
		kind, ok := existing[t.ID]
		if !ok || kind != t.Kind {
			return nil, invalidf("触发器 %s 不属于该自动化或类型不一致", t.ID)
		}
		keep = append(keep, t.ID)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.automation_triggers
		 WHERE organization_id = openfox_private.current_organization_id() AND automation_id = $1::uuid
		   AND id <> ALL($2::uuid[])`, automationID, keep); err != nil {
		return nil, err
	}
	var urls []WebhookURLItem
	for position, t := range inputs {
		if t.ID == "" {
			item, err := s.insertTrigger(ctx, tx, spaceID, automationID, position, t)
			if err != nil {
				return nil, err
			}
			if item != nil {
				urls = append(urls, *item)
			}
			continue
		}
		if err := s.updateTrigger(ctx, tx, automationID, position, t); err != nil {
			return nil, err
		}
	}
	return urls, nil
}

// insertTrigger 新建触发器。新建的 Webhook 触发器生成新令牌，返回的 URL 只这一次可见。
func (s Service) insertTrigger(ctx context.Context, tx pgx.Tx, spaceID, automationID string, position int, t TriggerInput) (*WebhookURLItem, error) {
	if t.Kind == "webhook" && t.SignatureRequired != nil && *t.SignatureRequired {
		return nil, ErrSignatureNeedKey
	}
	config, err := json.Marshal(t.Config)
	if err != nil {
		return nil, err
	}
	nextRun, err := nextRunFor(t, s.now())
	if err != nil {
		return nil, err
	}
	triggerID, err := newUUID()
	if err != nil {
		return nil, err
	}
	var tokenHash []byte
	var item *WebhookURLItem
	if t.Kind == "webhook" {
		token, hash, err := newWebhookToken()
		if err != nil {
			return nil, err
		}
		tokenHash = hash
		item = &WebhookURLItem{TriggerID: triggerID, URL: webhookURL(s.PublicURL, token)}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.automation_triggers (
			organization_id, id, automation_id, space_id, kind, enabled, position, config,
			next_run_at, webhook_token_hash, ip_allowlist
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb,
		          $8, $9, $10::text[])`,
		triggerID, automationID, spaceID, t.Kind, t.Enabled, position, string(config), nextRun, tokenHash, nonNilStrings(t.IPAllowlist)); err != nil {
		return nil, err
	}
	return item, nil
}

// updateTrigger 更新保留的触发器。Webhook 的令牌与密钥不变；签名开关只能在已有密钥时打开。
func (s Service) updateTrigger(ctx context.Context, tx pgx.Tx, automationID string, position int, t TriggerInput) error {
	config, err := json.Marshal(t.Config)
	if err != nil {
		return err
	}
	nextRun, err := nextRunFor(t, s.now())
	if err != nil {
		return err
	}
	signature := false
	if t.Kind == "webhook" && t.SignatureRequired != nil {
		signature = *t.SignatureRequired
	}
	if signature {
		var hasSecret bool
		if err := tx.QueryRow(ctx, `
			SELECT webhook_secret_ciphertext IS NOT NULL FROM openfox.automation_triggers
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, t.ID).Scan(&hasSecret); err != nil {
			return err
		}
		if !hasSecret {
			return ErrSignatureNeedKey
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.automation_triggers
		   SET enabled = $3, position = $4, config = $5::jsonb, next_run_at = $6,
		       signature_required = $7, ip_allowlist = $8::text[], version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND automation_id = $1::uuid AND id = $2::uuid`,
		automationID, t.ID, t.Enabled, position, string(config), nextRun, signature, nonNilStrings(t.IPAllowlist))
	return err
}

// nextRunFor 只对定时触发器计算下一次运行时刻；其他类型为 nil。
func nextRunFor(t TriggerInput, now time.Time) (*time.Time, error) {
	if t.Kind != "schedule" || t.Config.Schedule == nil {
		return nil, nil
	}
	next, err := NextRunAfter(*t.Config.Schedule, now)
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// checkAssignee 要求 AGENT 是本空间可指派、未归档的 Agent；CREW 是未归档的小队且有队长。
func (s Service) checkAssignee(ctx context.Context, tx pgx.Tx, spaceID, userID string, assignee AssigneeInput) error {
	var ok bool
	var err error
	switch assignee.Kind {
	case "AGENT":
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM openfox.agents a
			     WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $1::uuid
			       AND a.space_id = $2::uuid AND a.status = 'ACTIVE' AND a.archived_at IS NULL AND a.allow_assign
			       AND a.builtin_kind IS DISTINCT FROM 'TASK_ASSISTANT'
			       AND (a.visibility = 'SHARED' OR a.owner_user_id = $3::uuid)
			       AND NOT EXISTS (
			           SELECT 1 FROM openfox.agent_crew_members m
			            WHERE m.organization_id = a.organization_id AND m.agent_id = a.id AND m.exclusive))`,
			assignee.ID, spaceID, userID).Scan(&ok)
	case "CREW":
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM openfox.agent_crews cr
			     WHERE cr.organization_id = openfox_private.current_organization_id() AND cr.id = $1::uuid
			       AND cr.space_id = $2::uuid AND cr.archived_at IS NULL AND cr.leader_agent_id IS NOT NULL
			       AND (cr.visibility = 'SHARED' OR cr.owner_user_id = $3::uuid))`,
			assignee.ID, spaceID, userID).Scan(&ok)
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrAssigneeInvalid
	}
	return nil
}

// checkExecutorDirectChat 要求执行 Agent（AGENT 本身，CREW 为队长）开启单聊。与 conversations.Service.Create 的规则一致：
// allow_direct_chat 为真且不是任何小队的专属成员。自动化会话不挂任务，因此必须满足这一条。
func checkExecutorDirectChat(ctx context.Context, tx pgx.Tx, assignee AssigneeInput) error {
	var ok bool
	query := `
		SELECT a.allow_direct_chat AND NOT EXISTS (
		    SELECT 1 FROM openfox.agent_crew_members m
		     WHERE m.organization_id = a.organization_id AND m.agent_id = a.id AND m.exclusive)
		  FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $1::uuid`
	if assignee.Kind == "CREW" {
		query = `
			SELECT a.allow_direct_chat AND NOT EXISTS (
			    SELECT 1 FROM openfox.agent_crew_members m
			     WHERE m.organization_id = a.organization_id AND m.agent_id = a.id AND m.exclusive)
			  FROM openfox.agent_crews cr
			  JOIN openfox.agents a ON a.organization_id = cr.organization_id AND a.id = cr.leader_agent_id
			 WHERE cr.organization_id = openfox_private.current_organization_id() AND cr.id = $1::uuid`
	}
	if err := tx.QueryRow(ctx, query, assignee.ID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrAgentDirectChatDisabled
	}
	return nil
}

// checkNotifyUsers 要求每个通知人员都是 ACTIVE 成员且能读该空间（与 SpaceReadableSQL 的规则一致）。
func checkNotifyUsers(ctx context.Context, tx pgx.Tx, spaceID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	var matched int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.organization_members m
		  JOIN openfox.spaces s ON s.organization_id = m.organization_id AND s.id = $2::uuid
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.user_id = ANY($1::uuid[])
		   AND m.status = 'ACTIVE' AND s.deleted_at IS NULL
		   AND (EXISTS (
		        SELECT 1 FROM openfox.space_members sm
		         WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
		           AND sm.user_id = m.user_id AND sm.status = 'ACTIVE'
		   ) OR (s.visibility = 'SHARED' AND m.role <> 'GUEST' AND NOT EXISTS (
		        SELECT 1 FROM openfox.space_members sm
		         WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
		           AND sm.user_id = m.user_id AND sm.status <> 'ACTIVE'
		   )))`, uniqueStrings(userIDs), spaceID).Scan(&matched); err != nil {
		return err
	}
	if matched != len(uniqueStrings(userIDs)) {
		return ErrNotifyInvalid
	}
	return nil
}

// checkStatusIDs 要求任务状态触发器的状态都存在于该空间的工作流中。
func checkStatusIDs(ctx context.Context, tx pgx.Tx, spaceID string, triggers []TriggerInput) error {
	for _, t := range triggers {
		if t.Kind != "task_status" {
			continue
		}
		var matched int
		if err := tx.QueryRow(ctx, `
			SELECT count(DISTINCT status_id) FROM openfox.workflow_statuses
			 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
			   AND status_id = ANY($2::uuid[])`, spaceID, t.Config.StatusIDs).Scan(&matched); err != nil {
			return err
		}
		if matched != len(t.Config.StatusIDs) {
			return invalidf("状态不属于该空间的工作流")
		}
	}
	return nil
}

// lockSpace 锁住空间行，串行化同一空间内的自动化数量与重排。
func lockSpace(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL
		 FOR UPDATE`, spaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return collaboration.ErrSpaceNotFound
	}
	return err
}

// lockAutomation 锁住自动化行并返回当前版本与基本信息。
func lockAutomation(ctx context.Context, tx pgx.Tx, spaceID, automationID string) (Automation, error) {
	if err := lockSpace(ctx, tx, spaceID); err != nil {
		return Automation{}, err
	}
	var a Automation
	err := tx.QueryRow(ctx, `
		SELECT title, prompt, assignee_kind, assignee_id::text, notify_user_ids::text[], enabled, version
		  FROM openfox.automations
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid
		 FOR UPDATE`, spaceID, automationID).Scan(&a.Title, &a.Prompt, &a.Assignee.Kind, &a.Assignee.ID,
		&a.NotifyUserIDs, &a.Enabled, &a.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Automation{}, ErrNotFound
	}
	return a, err
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilURLs(values []WebhookURLItem) []WebhookURLItem {
	if values == nil {
		return []WebhookURLItem{}
	}
	return values
}

// ErrListChanged 表示重排提交的集合与当前空间的自动化不一致（有人新增或删除）。
var ErrListChanged = collaboration.NewRuleError(http.StatusConflict, "AUTOMATION_LIST_CHANGED", "automation list changed; reload and retry")
