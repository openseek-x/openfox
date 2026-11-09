package feishuint

import (
	"net/http"
	"strings"
	"testing"
)

// 任务助手：扫码、权限、冲突、取代、取消、编辑、重试、移除，以及内置 Agent 不出现在目录里。
func TestTaskAssistantRegistrationLifecycle(t *testing.T) {
	b := setupBots(t)
	regPath := b.botsPath("/registrations")
	body := map[string]any{"purpose": "task_assistant", "space_id": b.foxSpace, "runtime": b.runtimeBody(), "extra_prompt": "优先级默认 medium"}

	// 权限：成员 / 访客不能为自己不能管理的空间建助手；缺运行时、授权无效 → 400。
	b.member.expect(http.StatusForbidden, "", http.MethodPost, regPath, body)
	b.guest.expect(http.StatusForbidden, "", http.MethodPost, regPath, body)
	b.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, regPath, map[string]any{"purpose": "task_assistant", "space_id": b.foxSpace})
	bad := b.runtimeBody()
	bad["grant_id"] = b.newUUID()
	b.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, regPath, map[string]any{"purpose": "task_assistant", "space_id": b.foxSpace, "runtime": bad})
	// 组织没有飞书应用（B 组织）→ 409。
	var spaceB struct {
		ID string `json:"id"`
	}
	b.ownerB.json(http.StatusCreated, http.MethodPost, "/v1/organizations/"+b.orgB+"/spaces", map[string]any{"name": "B", "prefix": "bb", "visibility": "SHARED"}, &spaceB)
	b.ownerB.expect(http.StatusConflict, "FEISHU_APP_NOT_CONNECTED", http.MethodPost, "/v1/organizations/"+b.orgB+"/integrations/feishu/registrations",
		map[string]any{"purpose": "task_assistant", "space_id": spaceB.ID, "runtime": b.runtimeBody()})

	// 发起：返回可展示的链接；只有发起人能读。
	first := b.startAssistant(b.owner, b.foxSpace, "优先级默认 medium")
	if first.Status != "PENDING" || !strings.HasPrefix(first.VerificationURL, b.fake.URL+"/page/launcher?user_code=") || first.PollInterval < 1 || first.BotID != nil {
		t.Fatalf("first = %+v", first)
	}
	b.admin.expect(http.StatusForbidden, "", http.MethodGet, b.botsPath("/registrations/"+first.ID), nil)
	b.member.expect(http.StatusNotFound, "", http.MethodGet, b.botsPath("/registrations/"+b.newUUID()), nil)
	view := b.bots(b.owner)
	var pending *string
	for _, row := range view.Assistants {
		if row.SpaceID == b.foxSpace {
			pending = row.PendingRegistrationID
		}
	}
	if pending == nil || *pending != first.ID || !view.AppConnected || view.BuiltinPrompt == "" || !strings.Contains(view.BuiltinPrompt, "openfox_space_task_create") {
		t.Fatalf("pending = %v view = %+v", pending, view.AppConnected)
	}

	// 同一目标再发起：旧会话被取代；取消是幂等的。
	second := b.startAssistant(b.owner, b.foxSpace, "")
	if got := b.getReg(b.owner, first.ID); got.Status != "CANCELLED" || got.ErrorCode != "superseded" || got.VerificationURL != "" {
		t.Fatalf("superseded = %+v", got)
	}
	var cancelled regView
	b.owner.json(http.StatusOK, http.MethodPost, b.botsPath("/registrations/"+second.ID+"/cancel"), nil, &cancelled)
	b.owner.json(http.StatusOK, http.MethodPost, b.botsPath("/registrations/"+second.ID+"/cancel"), nil, &cancelled)
	if cancelled.Status != "CANCELLED" || cancelled.ErrorCode != "" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	b.pump()
	if b.getReg(b.owner, second.ID).Status != "CANCELLED" {
		t.Fatal("a cancelled registration must stay cancelled")
	}

	// 扫码成功：建出连接、机器人、内置 Agent，长连接连上。
	third := b.startAssistant(b.owner, b.foxSpace, "优先级默认 medium")
	done := b.complete(b.owner, third)
	if done.Status != "COMPLETED" || done.BotID == nil || done.VerificationURL != "" {
		t.Fatalf("done = %+v", done)
	}
	bot := b.makeAssistantWait(b.foxSpace)
	if bot.Kind != "TASK_ASSISTANT" || bot.Runtime == nil || bot.Runtime.ComputerID != b.deviceID || bot.ExtraPrompt != "优先级默认 medium" ||
		!bot.CanManage || bot.Space == nil || bot.Space.ID != b.foxSpace || !strings.HasPrefix(bot.AppID, "cli_fake") || bot.BotName == "" {
		t.Fatalf("bot = %+v", bot)
	}
	if b.fake.ConnCount(bot.AppID) != 1 {
		t.Fatalf("connections for the new app = %d", b.fake.ConnCount(bot.AppID))
	}
	var kind, visibility, builtin, instructions string
	var allowAssign bool
	b.scan(&kind, `SELECT account_kind FROM openfox.integration_connections WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.ConnectionID)
	b.scan(&builtin, `SELECT builtin_kind FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.Agent.ID)
	b.scan(&visibility, `SELECT visibility FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.Agent.ID)
	b.scan(&allowAssign, `SELECT allow_assign FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.Agent.ID)
	b.scan(&instructions, `SELECT c.instructions FROM openfox.agent_config_versions c JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		AND c.config_version = a.current_config_version WHERE a.organization_id = $1::uuid AND a.id = $2::uuid`, b.org, bot.Agent.ID)
	if kind != "task_assistant" || builtin != "TASK_ASSISTANT" || visibility != "SHARED" || allowAssign ||
		!strings.Contains(instructions, "你是 OpenFox 任务助手") || !strings.Contains(instructions, "优先级默认 medium") {
		t.Fatalf("kind=%s builtin=%s visibility=%s allowAssign=%v instructions=%.60s", kind, builtin, visibility, allowAssign, instructions)
	}
	// App Secret 不出现在任何响应里，device_code 不写进请求日志。
	for _, secretText := range []string{"secret_xxxxxxxx"} {
		b.assertNoSecretLeaked(secretText)
	}
	for _, req := range b.fake.Requests() {
		if strings.Contains(req.Body, "dc_") {
			t.Fatalf("device code logged by the fake: %s", req.Body)
		}
	}

	// 已有助手 → 409；内置 Agent 不出现在空间 Agent 列表与目录里；成员看不到附加提示词与运行时。
	b.owner.expect(http.StatusConflict, "FEISHU_ASSISTANT_EXISTS", http.MethodPost, regPath, body)
	for _, path := range []string{b.path("/spaces/" + b.foxSpace + "/agents"), b.path("/agent-directory")} {
		if status, payload := b.owner.do(http.MethodGet, path, nil); status != http.StatusOK || strings.Contains(payload, bot.Agent.ID) {
			t.Fatalf("GET %s = %d, assistant agent listed: %v", path, status, strings.Contains(payload, bot.Agent.ID))
		}
	}
	memberBot := b.assistantBot(b.member, b.foxSpace)
	if memberBot == nil || memberBot.CanManage || memberBot.ExtraPrompt != "" || memberBot.Runtime != nil {
		t.Fatalf("member view = %+v", memberBot)
	}

	// 编辑：追加配置版本；版本过期 409；成员 403；Agent 机器人以外才能编辑。
	var updated botView
	b.owner.json(http.StatusOK, http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"extra_prompt": "新的附加说明", "version": bot.Version}, &updated)
	if updated.Version != bot.Version+1 || updated.ExtraPrompt != "新的附加说明" {
		t.Fatalf("updated = %+v", updated)
	}
	var configs int
	b.scan(&configs, `SELECT count(*) FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND instructions LIKE '%新的附加说明%'`, b.org, bot.Agent.ID)
	if configs != 1 {
		t.Fatalf("config versions with the new prompt = %d", configs)
	}
	b.owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"extra_prompt": "x", "version": bot.Version})
	b.member.expect(http.StatusForbidden, "", http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"extra_prompt": "x", "version": updated.Version})
	b.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"version": updated.Version})

	// 重试：重新校验凭证并重建长连接；凭证被飞书停用后返回 422 并标为失败。
	var retried botView
	b.owner.json(http.StatusOK, http.MethodPost, b.botsPath("/bots/"+bot.ID+"/retry"), nil, &retried)
	waitFor(t, "assistant reconnected", func() bool { return b.assistantBot(b.owner, b.foxSpace).Status == "CONNECTED" })
	b.member.expect(http.StatusForbidden, "", http.MethodPost, b.botsPath("/bots/"+bot.ID+"/retry"), nil)
	b.fake.SetAppSecret(bot.AppID, "rotated-secret-00000000")
	b.owner.expect(http.StatusUnprocessableEntity, "FEISHU_CREDENTIALS_INVALID", http.MethodPost, b.botsPath("/bots/"+bot.ID+"/retry"), nil)
	if got := b.assistantBot(b.owner, b.foxSpace); got.Status != "FAILED" || got.StatusReason == "" {
		t.Fatalf("after failed retry = %+v", got)
	}

	// 移除：成员 403、版本过期 409；成功后连接断开、内置 Agent 归档、长连接停止；之后可以重新建。
	current := b.assistantBot(b.owner, b.foxSpace)
	b.member.expect(http.StatusForbidden, "", http.MethodPost, b.botsPath("/bots/"+bot.ID+"/remove"), map[string]any{"version": current.Version})
	b.owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, b.botsPath("/bots/"+bot.ID+"/remove"), map[string]any{"version": current.Version + 7})
	b.owner.json(http.StatusNoContent, http.MethodPost, b.botsPath("/bots/"+bot.ID+"/remove"), map[string]any{"version": current.Version}, nil)
	if b.assistantBot(b.owner, b.foxSpace) != nil {
		t.Fatal("removed bot still listed")
	}
	var connStatus string
	var archived bool
	b.scan(&connStatus, `SELECT status FROM openfox.integration_connections WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.ConnectionID)
	b.scan(&archived, `SELECT archived_at IS NOT NULL FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.Agent.ID)
	if connStatus != "DISCONNECTED" || !archived {
		t.Fatalf("after remove: connection=%s archived=%v", connStatus, archived)
	}
	waitFor(t, "long connection stopped", func() bool { return b.fake.ConnCount(bot.AppID) == 0 })
	b.owner.expect(http.StatusNotFound, "", http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"extra_prompt": "x", "version": 1})
	again := b.startAssistant(b.owner, b.foxSpace, "")
	if again.Status != "PENDING" {
		t.Fatalf("restart after remove = %+v", again)
	}
}

// makeAssistantWait 等待空间里的任务助手连上长连接。
func (b *botWorld) makeAssistantWait(space string) botView {
	b.t.Helper()
	var bot *botView
	waitFor(b.t, "assistant connected", func() bool {
		bot = b.assistantBot(b.owner, space)
		return bot != nil && bot.Status == "CONNECTED"
	})
	return *bot
}

// 扫码失败的各种结局：被拒绝、飞书侧过期、会话过期、slow_down、保存时并发冲突。
func TestRegistrationOutcomes(t *testing.T) {
	b := setupBots(t)
	deny := b.startAssistant(b.owner, b.foxSpace, "")
	if !b.fake.DenyRegistration(b.userCode(deny)) {
		t.Fatal("deny")
	}
	if got := b.waitReg(b.owner, deny.ID); got.Status != "DENIED" || got.ErrorCode != "access_denied" {
		t.Fatalf("denied = %+v", got)
	}
	expired := b.startAssistant(b.owner, b.foxSpace, "")
	b.fake.ExpireRegistration(b.userCode(expired))
	if got := b.waitReg(b.owner, expired.ID); got.Status != "EXPIRED" || got.ErrorCode != "expired" {
		t.Fatalf("expired = %+v", got)
	}

	// 会话本身过期：读取时直接落库为 EXPIRED。
	session := b.startAssistant(b.owner, b.foxSpace, "")
	b.exec(`UPDATE openfox.im_app_registrations SET expires_at = clock_timestamp() - interval '1 second' WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, session.ID)
	if got := b.getReg(b.owner, session.ID); got.Status != "EXPIRED" || got.ErrorCode != "session_expired" || got.VerificationURL != "" {
		t.Fatalf("session expired = %+v", got)
	}

	// slow_down：轮询间隔增加 5 秒，随后仍可完成。
	b.fake.SetSlowDown(1)
	slow := b.startAssistant(b.owner, b.foxSpace, "")
	b.fake.SetAuthorizeUnion("on_owner")
	b.fake.ApproveRegistration(b.userCode(slow), "")
	b.exec(`UPDATE openfox.im_app_registrations SET next_poll_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, slow.ID)
	b.pump()
	var interval int
	b.scan(&interval, `SELECT poll_interval FROM openfox.im_app_registrations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, slow.ID)
	if got := b.getReg(b.owner, slow.ID); got.Status != "PENDING" || interval != 6 {
		t.Fatalf("after slow_down: %+v interval=%d", got, interval)
	}
	b.exec(`UPDATE openfox.im_app_registrations SET next_poll_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, slow.ID)
	if got := b.waitReg(b.owner, slow.ID); got.Status != "COMPLETED" {
		t.Fatalf("slow completed = %+v", got)
	}
	var secretSealed bool
	b.scan(&secretSealed, `SELECT device_code_sealed IS NULL FROM openfox.im_app_registrations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, slow.ID)
	if !secretSealed {
		t.Fatal("device code must be cleared after completion")
	}

	// 保存时并发冲突：扫码期间别处已经给该空间建了任务助手 → FAILED assistant_exists。
	conflict := b.startAssistant(b.owner, b.secSpace, "")
	b.fake.ApproveRegistration(b.userCode(conflict), "")
	b.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, current_config_version, version, builtin_kind)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'other assistant', 'CODE', 'SHARED', 1, 1, 'TASK_ASSISTANT')`,
		b.org, b.newUUID(), b.deviceID, b.secSpace, b.ownerID)
	if got := b.waitReg(b.owner, conflict.ID); got.Status != "FAILED" || got.ErrorCode != "assistant_exists" || got.BotID != nil {
		t.Fatalf("conflict = %+v", got)
	}
	if n := b.count(`SELECT count(*) FROM openfox.im_bots WHERE organization_id = $1::uuid AND space_id = $2::uuid`, b.org, b.secSpace); n != 0 {
		t.Fatalf("bots after conflict = %d", n)
	}
}

// 绑定 Agents：权限、内置 Agent、冲突、归档后忽略消息并移除机器人。
func TestAgentBotRegistration(t *testing.T) {
	b := setupBots(t)
	ownerAgent := b.createAgent(b.owner, b.foxSpace, "Owner Agent", "SHARED")
	adminAgent := b.createAgent(b.admin, b.foxSpace, "Admin Agent", "SHARED")
	regPath := b.botsPath("/registrations")

	// 非所有者的成员不能绑定别人的 Agent；访客 403；未知 Agent 404；内置 Agent 400。
	b.member.expect(http.StatusForbidden, "", http.MethodPost, regPath, map[string]any{"purpose": "agent", "agent_id": ownerAgent})
	b.guest.expect(http.StatusForbidden, "", http.MethodPost, regPath, map[string]any{"purpose": "agent", "agent_id": ownerAgent})
	b.owner.expect(http.StatusNotFound, "", http.MethodPost, regPath, map[string]any{"purpose": "agent", "agent_id": b.newUUID()})
	builtin := b.createAgent(b.owner, b.secSpace, "Builtin Looking", "PRIVATE")
	b.exec(`UPDATE openfox.agents SET builtin_kind = 'ASSISTANT' WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, builtin)
	b.owner.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, regPath, map[string]any{"purpose": "agent", "agent_id": builtin})
	if v := b.bots(b.member); v.CanBindAgent {
		t.Fatal("a member without agents must not be able to bind")
	}
	if v := b.bots(b.admin); !v.CanBindAgent {
		t.Fatal("admins can bind agents")
	}

	// 管理员可以绑定任意 Agent（也包括所有者的）；所有者本人也可以。
	bot := b.makeAgentBot(b.admin, ownerAgent)
	if bot.Kind != "AGENT" || bot.Space != nil {
		t.Fatalf("bot = %+v", bot)
	}
	own := b.makeAgentBot(b.admin, adminAgent)
	var kind string
	b.scan(&kind, `SELECT account_kind FROM openfox.integration_connections WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, bot.ConnectionID)
	if kind != "agent" {
		t.Fatalf("account_kind = %s", kind)
	}
	// 已连接 → 409；Agent 机器人不能按任务助手编辑；成员不能移除。
	b.admin.expect(http.StatusConflict, "FEISHU_AGENT_CONNECTED", http.MethodPost, regPath, map[string]any{"purpose": "agent", "agent_id": ownerAgent})
	b.admin.expect(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, b.botsPath("/bots/"+bot.ID), map[string]any{"extra_prompt": "x", "version": bot.Version})
	b.member.expect(http.StatusForbidden, "", http.MethodPost, b.botsPath("/bots/"+bot.ID+"/remove"), map[string]any{"version": bot.Version})
	// 其他人（成员）看得到列表但不能管理。
	for _, item := range b.bots(b.member).Agents {
		if item.ID == bot.ID && item.CanManage {
			t.Fatal("member must not manage someone else's agent bot")
		}
	}

	// 机器人发来的消息：Agent 被归档后，入站时发现并忽略，同时把机器人移除。
	chat := "oc_agent_" + b.org[:6]
	b.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, own.Agent.ID)
	msgID, err := b.fake.PushMessage(own.AppID, chat, "p2p", "on_member", "你好", false)
	if err != nil {
		t.Fatal(err)
	}
	if row := b.waitInbound(own.ID, msgID); row.Status != "IGNORED" {
		t.Fatalf("inbound after archive = %+v", row)
	}
	waitFor(t, "archived agent bot removed", func() bool {
		for _, item := range b.bots(b.admin).Agents {
			if item.ID == own.ID {
				return false
			}
		}
		return true
	})
	var connStatus string
	b.scan(&connStatus, `SELECT status FROM openfox.integration_connections WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, own.ConnectionID)
	if connStatus != "DISCONNECTED" {
		t.Fatalf("connection after archive = %s", connStatus)
	}
}

// 个人绑定保存 union_id；其他组织看不到、也操作不了本组织的机器人与扫码会话。
func TestBotsCrossOrganization(t *testing.T) {
	b := setupBots(t)
	var union string
	b.scan(&union, `SELECT settings->>'union_id' FROM openfox.integration_connections WHERE organization_id = $1::uuid AND scope = 'PERSONAL' AND owner_user_id = $2::uuid AND status = 'ACTIVE'`, b.org, b.memberID)
	if union != "on_member" {
		t.Fatalf("union_id = %q", union)
	}
	bot := b.makeAssistant(b.foxSpace, "")
	reg := b.startAssistant(b.owner, b.secSpace, "")
	otherBots := "/v1/organizations/" + b.orgB + "/integrations/feishu"
	b.ownerB.expect(http.StatusNotFound, "", http.MethodPost, otherBots+"/bots/"+bot.ID+"/remove", map[string]any{"version": bot.Version})
	b.ownerB.expect(http.StatusNotFound, "", http.MethodPatch, otherBots+"/bots/"+bot.ID, map[string]any{"extra_prompt": "x", "version": bot.Version})
	b.ownerB.expect(http.StatusNotFound, "", http.MethodPost, otherBots+"/bots/"+bot.ID+"/retry", nil)
	b.ownerB.expect(http.StatusNotFound, "", http.MethodGet, otherBots+"/registrations/"+reg.ID, nil)
	b.ownerB.expect(http.StatusNotFound, "", http.MethodPost, otherBots+"/registrations/"+reg.ID+"/cancel", nil)
	var view botsView
	b.ownerB.json(http.StatusOK, http.MethodGet, otherBots+"/bots", nil, &view)
	if view.AppConnected || len(view.Agents) != 0 {
		t.Fatalf("org B view = %+v", view)
	}
	for _, row := range view.Assistants {
		if row.Bot != nil {
			t.Fatalf("org B sees a bot: %+v", row.Bot)
		}
	}
	// 本组织的机器人与扫码会话没有被动过。
	if got := b.assistantBot(b.owner, b.foxSpace); got == nil || got.ID != bot.ID {
		t.Fatalf("bot after foreign calls = %+v", got)
	}
	if got := b.getReg(b.owner, reg.ID); got.Status != "PENDING" {
		t.Fatalf("registration after foreign cancel = %+v", got)
	}
}
