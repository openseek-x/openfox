package feishuint

// 钉钉：应用绑定、Stream 长连接、个人授权、通知、任务助手 / Agent 机器人（AppKey + AppSecret 直连）、机器人收发消息。
// 全部对本机模拟钉钉（fakedingtalk）测试，绝不访问真实的 api.dingtalk.com / oapi.dingtalk.com / login.dingtalk.com。

import (
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/dingtalk"
	fakedingtalk "openfox/tests/support/fakedingtalk/fake"
)

// dtLink 让 c 用钉钉员工（staffID）走完整的个人授权流程。
func (w *world) dtLink(c *client, staff string) oauthResult {
	w.t.Helper()
	w.dtFake.SetAuthorizeUser(staff)
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		SessionID    string `json:"session_id"`
	}
	c.json(http.StatusOK, http.MethodPost, w.path("/integrations/dingtalk/authorize"), nil, &start)
	parsed, err := neturl.Parse(start.AuthorizeURL)
	if err != nil || !strings.HasPrefix(start.AuthorizeURL, w.dtFake.URL+"/oauth2/auth?") {
		w.t.Fatalf("authorize url = %s", start.AuthorizeURL)
	}
	q := parsed.Query()
	if q.Get("client_id") != w.appID || q.Get("scope") != "openid" || q.Get("prompt") != "consent" || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != w.publicURL+"/api/v1/integrations/dingtalk/callback" || q.Get("code_challenge") != "" ||
		strings.Contains(start.AuthorizeURL, w.secret) {
		w.t.Fatalf("authorize url = %s", start.AuthorizeURL)
	}
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := plain.Get(start.AuthorizeURL)
	if err != nil {
		w.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound {
		w.t.Fatalf("fake authorize = %d", response.StatusCode)
	}
	return w.callback(c, response.Header.Get("Location"))
}

func TestDingTalkBindConnectAuthorizeNotify(t *testing.T) {
	w := setupFor(t, "dingtalk")
	if v := w.view(w.owner); v.App != nil || !v.CanManage {
		t.Fatalf("empty view = %+v", v)
	}
	// 绑定失败：AppKey 格式、AppSecret 错误、钉钉不可达，都不保存任何东西。
	put := func(c *client, want int, code string, body map[string]any) {
		t.Helper()
		c.expect(want, code, http.MethodPut, w.appPath(), body)
	}
	put(w.owner, http.StatusBadRequest, "INVALID_REQUEST", map[string]any{"app_id": "cli_abcdef123456", "app_secret": w.secret})
	put(w.owner, http.StatusUnprocessableEntity, "FEISHU_CREDENTIALS_INVALID", map[string]any{"app_id": w.appID, "app_secret": "wrong-secret-0123456789"})
	w.dtFake.SetServerError(true)
	put(w.owner, http.StatusBadGateway, "FEISHU_UNAVAILABLE", map[string]any{"app_id": w.appID, "app_secret": w.secret})
	w.dtFake.SetServerError(false)
	put(w.member, http.StatusForbidden, "", map[string]any{"app_id": w.appID, "app_secret": w.secret})
	if n := w.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id = $1::uuid AND provider = 'dingtalk'`, w.org); n != 0 {
		t.Fatalf("a failed validation must not save anything, got %d connections", n)
	}

	app := w.bind()
	if app.Status != "PENDING" || app.AppID != w.appID || app.BotName != w.appID || app.Version != 1 || app.CredentialsHint != w.secret[len(w.secret)-4:] {
		t.Fatalf("bound app = %+v", app)
	}
	if n := w.count(`SELECT strpos(encode(credentials_sealed, 'escape'), $2) FROM openfox.integration_connections WHERE id = $1::uuid`, app.ConnectionID, w.secret); n != 0 {
		t.Fatal("app secret is stored in plaintext")
	}
	member := w.view(w.member)
	if member.Guide.RedirectURI != w.publicURL+"/api/v1/integrations/dingtalk/callback" || member.Guide.OpenPlatformURL != w.dtFake.URL+"/app" ||
		member.Guide.LinkPreviewPattern != "" || len(member.Guide.Callbacks) != 1 || member.Guide.Callbacks[0] != dingtalk.TopicBotMessage ||
		!strings.Contains(member.Guide.ScopesJSON, "qyapi_robot_sendmsg") || !strings.Contains(member.Guide.ScopesJSON, "qyapi_get_member") {
		t.Fatalf("guide = %+v", member.Guide)
	}
	w.guest.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, w.appPath(), nil)
	// 另一个组织不能绑定同一个钉钉应用。
	w.ownerB.expect(http.StatusConflict, "FEISHU_APP_IN_USE", http.MethodPut, "/v1/organizations/"+w.orgB+"/integrations/dingtalk/app",
		map[string]any{"app_id": w.appID, "app_secret": w.secret})

	// Stream 长连接：连上、ping 原样回 opaque、断开后重连。
	w.startManager("dt-mgr")
	if connected := w.waitStatus("CONNECTED"); connected.LastConnectedAt == nil {
		t.Fatalf("connected app = %+v", connected)
	}
	if w.dtFake.ConnCount(w.appID) != 1 {
		t.Fatalf("connections = %d", w.dtFake.ConnCount(w.appID))
	}
	if _, err := w.dtFake.PushSystem(w.appID, "ping"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.dtFake.PushSystem(w.appID, "disconnect"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reconnect after disconnect", func() bool { return w.dtFake.TotalConnects(w.appID) >= 2 && w.dtFake.ConnCount(w.appID) == 1 })
	w.dtFake.DropConnections(w.appID)
	waitFor(t, "reconnect after drop", func() bool { return w.dtFake.TotalConnects(w.appID) >= 3 && w.dtFake.ConnCount(w.appID) == 1 })

	// 个人授权：回调、会话、员工 userid 记在个人连接里；用户令牌不落库。
	w.guest.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/dingtalk/authorize"), nil)
	linked := w.dtLink(w.member, "st_member")
	if linked.Status != "ok" {
		t.Fatalf("link = %+v", linked)
	}
	view := w.view(w.member)
	if view.Personal == nil || view.Personal.DisplayName != "Mem Ber" {
		t.Fatalf("personal = %+v", view.Personal)
	}
	if n := w.count(`SELECT count(*) FROM openfox.integration_connections WHERE id = $1::uuid AND provider = 'dingtalk' AND credentials_sealed IS NULL
		AND settings->>'union_id' = 'un_member' AND settings->>'staff_id' = 'st_member' AND NOT (settings ? 'tenant_key' AND settings->>'tenant_key' <> '') AND external_id = 'ou_member'`, view.Personal.ConnectionID); n != 1 {
		t.Fatal("personal connection must keep the unionId (matching key), the staff id (for sending) and no credential")
	}
	var session sessionInfo
	w.member.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+linked.SessionID), nil, &session)
	if session.Provider != "dingtalk" || session.Status != "COMPLETED" {
		t.Fatalf("session = %+v", session)
	}
	// 同一个钉钉员工不能绑定到本组织的另一个成员；授权被拒绝。
	if res := w.dtLink(w.admin, "st_member"); res.Reason != "account_in_use" {
		t.Fatalf("account in use = %+v", res)
	}
	w.dtFake.SetDenyAuthorize(true)
	if res := w.dtLink(w.admin, "st_admin"); res.Reason != "access_denied" {
		t.Fatalf("denied = %+v", res)
	}
	w.dtFake.SetDenyAuthorize(false)

	// 通知：机器人单聊发给员工 userid，markdown 带任务链接。
	task := w.task(w.foxSpace, "Review the *launch* <plan>", w.memberID)
	w.task(w.foxSpace, "Task for admin", w.adminID)
	if w.outboxCount(w.adminID) != 0 {
		t.Fatal("a member without a dingtalk account must not be enqueued")
	}
	w.runNotify()
	row := w.outbox(w.memberID)
	if row == nil || row.Status != "DONE" || row.Remote == "" || row.LastError != "" {
		t.Fatalf("outbox = %+v", row)
	}
	messages := w.dtFake.Messages(w.appID)
	wantLink := w.publicURL + "/o/" + w.org + "/spaces/" + w.foxSpace + "/tasks?task=" + task.DisplayID
	if len(messages) != 1 || messages[0].Kind != "user" || messages[0].Target != "st_member" || messages[0].RobotCode != w.appID ||
		messages[0].MsgKey != "sampleMarkdown" || messages[0].Title != "有任务指派给你" || !strings.Contains(messages[0].Text, wantLink) ||
		!strings.Contains(messages[0].Text, `Review the \*launch\* \<plan\>`) {
		t.Fatalf("messages = %+v", messages)
	}
	w.runNotify()
	if len(w.dtFake.Messages(w.appID)) != 1 {
		t.Fatal("a delivered notification must not be sent again")
	}
	w.assertNoSecretLeaked()
	for _, request := range w.dtFake.Requests() {
		if strings.Contains(request.Path, w.secret) || strings.Contains(request.Path, "access_token="+w.secret) {
			t.Fatalf("a request path leaked the secret: %s", request.Path)
		}
	}
}

func TestDingTalkNotificationFailures(t *testing.T) {
	w := setupFor(t, "dingtalk")
	w.bind()
	w.mustLinkDT(w.member, "st_member")
	assign := func(title string) *outboxRow {
		w.task(w.foxSpace, title, w.memberID)
		w.runNotify()
		return w.outbox(w.memberID)
	}
	// accessToken 在发送时失效：清缓存、换新 token 后重发一次，用户只收到一条。
	w.dtFake.ExpireTokens()
	if row := assign("token expiry"); row.Status != "DONE" {
		t.Fatalf("after token expiry = %+v", row)
	}
	if len(w.dtFake.Messages(w.appID)) != 1 {
		t.Fatalf("messages = %d", len(w.dtFake.Messages(w.appID)))
	}
	// 限流：延后重试，不计次数，不标记需要处理。
	w.dtFake.FailNextSends(1, http.StatusTooManyRequests)
	if row := assign("rate limited"); row.Status != "PENDING" || row.LastError != "rate_limited" || row.Attempts != 0 {
		t.Fatalf("rate limited = %+v", row)
	}
	if v := w.view(w.owner); v.App == nil || v.App.Status == "NEEDS_ATTENTION" {
		t.Fatalf("a rate limit must not flag the app: %+v", v.App)
	}
	// 服务端错误：计一次重试并退避。
	w.dtFake.FailNextSends(1, http.StatusInternalServerError)
	if row := assign("server error"); row.Status != "PENDING" || row.Attempts != 1 {
		t.Fatalf("server error = %+v", row)
	}
	// 员工无效（不在应用可用范围 / 已离职）：直接失败，不重试。
	w.dtFake.SetInvalidStaff("st_member", true)
	if row := assign("unreachable"); row.Status != "FAILED" || row.LastError != "user_unreachable" {
		t.Fatalf("unreachable = %+v", row)
	}
	w.dtFake.SetInvalidStaff("st_member", false)
	// AppSecret 被重置：凭证无效，应用标为需要处理。
	w.dtFake.SetAppSecret(w.appID, "rotated-secret-0123456789")
	w.dtFake.ExpireTokens()
	w.task(w.foxSpace, "credentials invalid", w.memberID)
	w.runNotify()
	if v := w.view(w.owner); v.App == nil || v.App.Status != "NEEDS_ATTENTION" || v.App.StatusReason != "credentials_invalid" {
		t.Fatalf("app after invalid credentials = %+v", v.App)
	}
}

// mustLinkDT 让 c 绑定钉钉员工并要求成功。
func (w *world) mustLinkDT(c *client, staff string) {
	w.t.Helper()
	if res := w.dtLink(c, staff); res.Status != "ok" {
		w.t.Fatalf("link %s = %+v", staff, res)
	}
}

// ---------- 机器人（任务助手 / Agent） ----------

type dtBots struct {
	*botWorld
	botApp, botSecret string
	n                 int
}

func (d *dtBots) botsPath(suffix string) string { return d.path("/integrations/dingtalk" + suffix) }

func setupDTBots(t *testing.T) *dtBots {
	w := setupFor(t, "dingtalk")
	b := &botWorld{world: w}
	w.bind()
	b.mgr = w.startManager("dt-bots")
	w.waitStatus("CONNECTED")
	b.deviceID = w.newUUID()
	w.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at)
		VALUES ($1::uuid, $2, 4, $3::uuid, 'dt-mac', clock_timestamp())`, b.deviceID, "dt-device-"+w.org[:8], w.ownerID)
	w.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, w.org, b.deviceID)
	w.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, w.org, b.deviceID)
	w.mustLinkDT(w.owner, "st_owner")
	w.mustLinkDT(w.member, "st_member")
	b.imw = &integrations.IMWorker{Pool: w.pool, Service: w.service, Instance: "imw-dt"}
	d := &dtBots{botWorld: b}
	d.newApp()
	return d
}

// newApp 在模拟钉钉里登记机器人使用的另一个钉钉应用（每个机器人一个应用）。
func (d *dtBots) newApp() {
	d.n++
	compact := strings.ReplaceAll(d.newUUID(), "-", "")
	d.botApp, d.botSecret = "ding"+compact[:12], "botsecret"+compact[12:28]
	d.dtFake.AddApp(d.botApp, d.botSecret)
}

func (d *dtBots) createAssistant(c *client, want int, space string, extra map[string]any) botView {
	d.t.Helper()
	body := map[string]any{"purpose": "task_assistant", "space_id": space, "runtime": d.runtimeBody(), "extra_prompt": "只回答任务相关问题",
		"app_key": d.botApp, "app_secret": d.botSecret}
	for k, v := range extra {
		body[k] = v
	}
	var bot botView
	if want == http.StatusCreated {
		c.json(want, http.MethodPost, d.botsPath("/bots"), body, &bot)
	} else {
		c.expect(want, "", http.MethodPost, d.botsPath("/bots"), body)
	}
	return bot
}

func (d *dtBots) waitBot(c *client, space string) *botView {
	d.t.Helper()
	var bot *botView
	waitFor(d.t, "bot connected", func() bool {
		var v botsView
		c.json(http.StatusOK, http.MethodGet, d.botsPath("/bots"), nil, &v)
		for _, row := range v.Assistants {
			if row.SpaceID == space {
				bot = row.Bot
			}
		}
		return bot != nil && bot.Status == "CONNECTED"
	})
	return bot
}

// pushDT 让机器人应用收到一条机器人消息回调。
func (d *dtBots) pushDT(app, conversationType, conversationID, msgID, staff, text string) {
	d.t.Helper()
	payload := map[string]any{"conversationId": conversationID, "conversationType": conversationType, "msgId": msgID, "msgtype": "text",
		"senderStaffId": staff, "senderNick": "someone", "isInAtList": true, "robotCode": app, "text": map[string]string{"content": text}}
	if _, err := d.dtFake.PushCallback(app, dingtalk.TopicBotMessage, payload); err != nil {
		d.t.Fatal(err)
	}
}

func (d *dtBots) sentText(app, kind, target string) []string {
	var out []string
	for _, m := range d.dtFake.Messages(app) {
		if m.Kind == kind && m.Target == target {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestDingTalkAssistantBot(t *testing.T) {
	d := setupDTBots(t)
	// 创建失败：格式、权限、凭证、重复的 AppKey。
	d.createAssistant(d.owner, http.StatusBadRequest, d.foxSpace, map[string]any{"app_key": "cli_abcdef123456"})
	d.createAssistant(d.member, http.StatusForbidden, d.foxSpace, nil)
	d.createAssistant(d.owner, http.StatusUnprocessableEntity, d.foxSpace, map[string]any{"app_secret": "wrong-secret-0123456789"})
	if n := d.count(`SELECT count(*) FROM openfox.im_bots WHERE organization_id = $1::uuid`, d.org); n != 0 {
		t.Fatalf("failed creations must not save a bot, got %d", n)
	}
	created := d.createAssistant(d.owner, http.StatusCreated, d.foxSpace, nil)
	if created.Kind != "TASK_ASSISTANT" || created.AppID != d.botApp {
		t.Fatalf("created bot = %+v", created)
	}
	bot := d.waitBot(d.owner, d.foxSpace)
	// 同一个空间只能有一个助手；同一个 AppKey 不能再连接。
	d.createAssistant(d.owner, http.StatusConflict, d.foxSpace, nil)
	d.createAssistant(d.owner, http.StatusConflict, d.secSpace, nil)
	if n := d.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id = $1::uuid AND provider = 'dingtalk' AND account_kind = 'task_assistant'`, d.org); n != 1 {
		t.Fatalf("assistant connections = %d", n)
	}
	// 机器人密钥只以密文落库。
	if n := d.count(`SELECT strpos(encode(credentials_sealed, 'escape'), $2) FROM openfox.integration_connections WHERE id = $1::uuid`, bot.ConnectionID, d.botSecret); n != 0 {
		t.Fatal("bot secret is stored in plaintext")
	}
	d.waitFor(func() bool { return d.dtFake.ConnCount(d.botApp) == 1 })

	// 未绑定的人单聊：提示先关联钉钉账号，10 分钟内只提示一次（他是企业员工，只是没有在 OpenFox 里绑定钉钉账号）。
	d.dtFake.AddUser(fakedingtalk.User{OpenID: "ou_stranger", UnionID: "un_stranger", StaffID: "st_stranger", Name: "Stranger"})
	d.pushDT(d.botApp, "1", "cid_stranger", "msg-s1", "st_stranger", "你好")
	d.pushDT(d.botApp, "1", "cid_stranger", "msg-s2", "st_stranger", "再来一条")
	if row := d.waitInbound(bot.ID, "msg-s1"); row.Status != "REJECTED" || row.Result != "unbound" {
		t.Fatalf("stranger = %+v", row)
	}
	if row := d.waitInbound(bot.ID, "msg-s2"); row.Status != "REJECTED" || row.Result != "unbound" {
		t.Fatalf("stranger 2 = %+v", row)
	}
	waitFor(t, "unbound hint", func() bool {
		d.pump()
		for _, text := range d.sentText(d.botApp, "user", "st_stranger") {
			if strings.Contains(text, "关联你的钉钉账号") && strings.Contains(text, d.publicURL+"/o/"+strings.ToLower(d.org)+"/settings/integrations/dingtalk") {
				return true
			}
		}
		return false
	})
	d.pump()
	if hints := len(d.sentText(d.botApp, "user", "st_stranger")); hints != 1 {
		t.Fatalf("unbound hints = %d, want 1", hints)
	}

	// 同一条回调重复推送：只入队一次。
	d.pushDT(d.botApp, "1", "cid_member", "msg-m1", "st_member", "帮我建个任务")
	d.pushDT(d.botApp, "1", "cid_member", "msg-m1", "st_member", "帮我建个任务")
	row := d.waitInbound(bot.ID, "msg-m1")
	if row.Status != "DONE" || row.MessageID == "" || row.Text != "帮我建个任务" {
		t.Fatalf("member message = %+v", row)
	}
	if n := d.count(`SELECT count(*) FROM openfox.im_inbound_messages WHERE bot_id = $1::uuid AND remote_message_id = 'msg-m1'`, bot.ID); n != 1 {
		t.Fatalf("duplicate callback rows = %d", n)
	}
	conv := d.conversationFor(bot.ID, "u:st_member")
	if conv.Visibility != "PRIVATE" || conv.Origin != "IM" || conv.Owner != d.memberID || conv.Title != "钉钉单聊" {
		t.Fatalf("p2p conversation = %+v", conv)
	}
	// 群里 @ 机器人：共享会话；上下文不读取（钉钉没有读取群历史的接口），消息里没有「读取失败」提示。
	d.pushDT(d.botApp, "2", "cid_group", "msg-g1", "st_member", "@助手 看看这个")
	grow := d.waitInbound(bot.ID, "msg-g1")
	if grow.Status != "DONE" {
		t.Fatalf("group message = %+v", grow)
	}
	group := d.conversationFor(bot.ID, "g:cid_group")
	if group.Visibility != "SHARED" || group.Title != "钉钉群聊" {
		t.Fatalf("group conversation = %+v", group)
	}
	for _, m := range d.messages(group.ID) {
		if strings.Contains(m.Body, "未能读取") || strings.Contains(m.Body, "无法读取") {
			t.Fatalf("a dingtalk group message must not carry a context failure note: %q", m.Body)
		}
	}
	// 非文本：提示只支持文字。
	payload := map[string]any{"conversationId": "cid_member", "conversationType": "1", "msgId": "msg-img", "msgtype": "picture", "senderStaffId": "st_member", "robotCode": d.botApp}
	if _, err := d.dtFake.PushCallback(d.botApp, dingtalk.TopicBotMessage, payload); err != nil {
		t.Fatal(err)
	}
	if r := d.waitInbound(bot.ID, "msg-img"); r.Status != "REJECTED" || r.Result != "unsupported" {
		t.Fatalf("picture = %+v", r)
	}

	// Agent / 系统在已关联会话里发言：单聊发给员工，群聊发到群；markdown 末尾带会话链接。
	p2pMsg := d.agentSays(conv.ID, conv.AgentID, "AGENT", "已创建任务 **FOX-1**")
	d.waitOutbox(p2pMsg, "DONE")
	groupMsg := d.agentSays(group.ID, group.AgentID, "AGENT", "群里的回复")
	d.waitOutbox(groupMsg, "DONE")
	p2p := strings.Join(d.sentText(d.botApp, "user", "st_member"), "\n---\n")
	groupSent := d.sentText(d.botApp, "group", "cid_group")
	if !strings.Contains(p2p, "目前只支持文字消息") || strings.Count(p2p, "已创建任务 **FOX-1**") != 1 ||
		!strings.Contains(p2p, d.publicURL+"/o/"+strings.ToLower(d.org)+"/chat/"+conv.ID) ||
		len(groupSent) != 1 || !strings.Contains(groupSent[0], "群里的回复") {
		t.Fatalf("replies: p2p=%v group=%v", p2p, groupSent)
	}
	// 回发失败：限流延后重试（不计次数）、令牌失效后重发成功。
	d.dtFake.FailNextSends(1, http.StatusTooManyRequests)
	limited := d.agentSays(conv.ID, conv.AgentID, "AGENT", "限流后重发")
	d.pump()
	if status, _, attempts, ok := d.outboxStatus(limited); !ok || status != "PENDING" || attempts != 0 {
		t.Fatalf("rate limited reply = %s attempts=%d ok=%v", status, attempts, ok)
	}
	d.dtFake.ExpireTokens()
	expired := d.agentSays(conv.ID, conv.AgentID, "AGENT", "令牌失效后重发")
	d.waitOutbox(expired, "DONE")

	// 删除机器人：长连接断开，之后的消息不再入队。
	d.owner.json(http.StatusNoContent, http.MethodPost, d.botsPath("/bots/"+bot.ID+"/remove"), map[string]any{"version": bot.Version}, nil)
	waitFor(t, "bot session stop", func() bool { return d.dtFake.ConnCount(d.botApp) == 0 })
	d.assertNoSecretLeaked(d.botSecret)
}

func TestDingTalkAgentBotAndSecretRotation(t *testing.T) {
	d := setupDTBots(t)
	agentID := d.createAgent(d.owner, d.foxSpace, "Dt Agent", "SHARED")
	var bot botView
	d.owner.json(http.StatusCreated, http.MethodPost, d.botsPath("/bots"), map[string]any{"purpose": "agent", "agent_id": agentID,
		"app_key": d.botApp, "app_secret": d.botSecret}, &bot)
	if bot.Kind != "AGENT" || bot.Agent.ID != agentID {
		t.Fatalf("agent bot = %+v", bot)
	}
	// 一个 Agent 只能绑定一个机器人。
	d.newApp()
	d.owner.expect(http.StatusConflict, "FEISHU_AGENT_CONNECTED", http.MethodPost, d.botsPath("/bots"), map[string]any{"purpose": "agent", "agent_id": agentID,
		"app_key": d.botApp, "app_secret": d.botSecret})
	// 飞书的扫码接口在钉钉下不存在。
	d.owner.expect(http.StatusNotFound, "", http.MethodGet, d.botsPath("/registrations/"+d.newUUID()), nil)
	d.owner.expect(http.StatusMethodNotAllowed, "", http.MethodPost, d.botsPath("/registrations"), map[string]any{"purpose": "agent", "agent_id": agentID})

	// 机器人应用的 AppSecret 被重置：长连接握手被拒绝，机器人标为需要处理。
	waitFor(t, "agent bot connected", func() bool {
		var v botsView
		d.owner.json(http.StatusOK, http.MethodGet, d.botsPath("/bots"), nil, &v)
		return len(v.Agents) == 1 && v.Agents[0].Status == "CONNECTED"
	})
	d.dtFake.SetAppSecret(bot.AppID, "rotated-secret-0123456789")
	d.dtFake.DropConnections(bot.AppID)
	waitFor(t, "needs attention", func() bool {
		var v botsView
		d.owner.json(http.StatusOK, http.MethodGet, d.botsPath("/bots"), nil, &v)
		return len(v.Agents) == 1 && v.Agents[0].Status == "FAILED"
	})
}

// waitFor 的小包装，避免测试里重复写 t。
func (d *dtBots) waitFor(cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			d.t.Fatal("timed out waiting for condition")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 入站消息只按 unionId 匹配成员：员工 userid 在不同钉钉企业里可以相同，不能拿它认人。
// 场景：组织绑定的钉钉应用在企业 X，成员 Alice 的员工 userid 是 st_dup；机器人应用在另一个企业 Y，
// 那里的 Bob 的员工 userid 同样是 st_dup。Bob 给机器人发消息，不能被归到 Alice 名下。
func TestDingTalkInboundMatchesByUnionIDNotStaffID(t *testing.T) {
	w := setupFor(t, "dingtalk")
	b := &botWorld{world: w}
	w.dtFake.AddUser(fakedingtalk.User{OpenID: "ou_alice", UnionID: "un_alice", StaffID: "st_dup", Name: "Alice"})
	w.bind()
	b.mgr = w.startManager("dt-union")
	w.waitStatus("CONNECTED")
	b.deviceID = w.newUUID()
	w.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at)
		VALUES ($1::uuid, $2, 4, $3::uuid, 'dt-mac2', clock_timestamp())`, b.deviceID, "dt-device2-"+w.org[:8], w.ownerID)
	w.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, w.org, b.deviceID)
	w.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, w.org, b.deviceID)
	w.mustLinkDT(w.member, "st_dup") // 成员（OpenFox）绑定的是企业 X 的 Alice
	b.imw = &integrations.IMWorker{Pool: w.pool, Service: w.service, Instance: "imw-union"}
	d := &dtBots{botWorld: b}
	d.newApp()
	// 机器人应用在另一个企业 Y；那里有同样 userid 的 Bob，以及一个没有员工记录的人。
	w.dtFake.SetAppCorp(d.botApp, "corp_y")
	w.dtFake.AddCorpUser("corp_y", fakedingtalk.User{OpenID: "ou_bob", UnionID: "un_bob", StaffID: "st_dup", Name: "Bob"})
	w.dtFake.AddCorpUser("corp_y", fakedingtalk.User{OpenID: "ou_carol", UnionID: "un_carol", StaffID: "st_carol", Name: "Carol"})
	d.createAssistant(d.owner, http.StatusCreated, d.foxSpace, nil)
	bot := d.waitBot(d.owner, d.foxSpace)
	userGets := func() int {
		n := 0
		for _, request := range w.dtFake.Requests() {
			if request.Path == "/topapi/v2/user/get" {
				n++
			}
		}
		return n
	}

	// Bob（企业 Y，userid 同为 st_dup）：unionId 是 un_bob，没有绑定 → 拒绝并提示绑定，不会归到 Alice（OpenFox 成员）名下。
	d.pushDT(d.botApp, "1", "cid_bob", "msg-bob-1", "st_dup", "我是 Bob")
	if row := d.waitInbound(bot.ID, "msg-bob-1"); row.Status != "REJECTED" || row.Result != "unbound" || row.MessageID != "" {
		t.Fatalf("bob's message = %+v", row)
	}
	if n := d.count(`SELECT count(*) FROM openfox.im_chat_bindings WHERE organization_id = $1::uuid AND bot_id = $2::uuid`, d.org, bot.ID); n != 0 {
		t.Fatalf("no conversation may be created for an unbound unionId, got %d bindings", n)
	}
	if n := d.count(`SELECT count(*) FROM openfox.messages m JOIN openfox.conversations c ON c.organization_id = m.organization_id AND c.id = m.conversation_id
		WHERE m.organization_id = $1::uuid AND c.origin = 'IM'`, d.org); n != 0 {
		t.Fatalf("no IM message may be written, got %d", n)
	}
	waitFor(t, "bind hint for bob", func() bool {
		d.pump()
		for _, text := range d.sentText(d.botApp, "user", "st_dup") {
			if strings.Contains(text, "关联你的钉钉账号") {
				return true
			}
		}
		return false
	})
	// 同一个人的 unionId 查询有缓存：再发一条不会再问钉钉。
	before := userGets()
	d.pushDT(d.botApp, "1", "cid_bob", "msg-bob-2", "st_dup", "再来一条")
	if row := d.waitInbound(bot.ID, "msg-bob-2"); row.Status != "REJECTED" || row.Result != "unbound" {
		t.Fatalf("bob's second message = %+v", row)
	}
	if after := userGets(); after != before {
		t.Fatalf("user/get calls %d -> %d, the union id must be cached", before, after)
	}

	// 换不到 unionId（企业 Y 里没有这个员工）：按未绑定拒绝，不会回退到按 staffId 匹配。
	d.pushDT(d.botApp, "1", "cid_ghost", "msg-ghost", "st_ghost", "你好")
	if row := d.waitInbound(bot.ID, "msg-ghost"); row.Status != "REJECTED" || row.Result != "unbound" || row.MessageID != "" {
		t.Fatalf("unknown staff = %+v", row)
	}

	// 回调里发送人企业与机器人企业不一致：直接丢弃，不入队、不回复。
	rows := d.inboundCount(bot.ID)
	payload := map[string]any{"conversationId": "cid_x", "conversationType": "2", "msgId": "msg-corp", "msgtype": "text", "senderStaffId": "st_carol",
		"chatbotCorpId": "corp_y", "senderCorpId": "corp_other", "robotCode": d.botApp, "text": map[string]string{"content": "跨企业"}}
	if _, err := w.dtFake.PushCallback(d.botApp, dingtalk.TopicBotMessage, payload); err != nil {
		t.Fatal(err)
	}
	d.pump()
	if after := d.inboundCount(bot.ID); after != rows {
		t.Fatalf("a corp-mismatched message must be dropped, rows %d -> %d", rows, after)
	}

	// 对照：企业 Y 里的 Carol 同样没有绑定 → 拒绝；而 Alice（企业 X，unionId 与绑定一致）若在同一企业则能通过（见 TestDingTalkAssistantBot）。
	d.pushDT(d.botApp, "1", "cid_carol", "msg-carol", "st_carol", "你好")
	if row := d.waitInbound(bot.ID, "msg-carol"); row.Status != "REJECTED" || row.Result != "unbound" {
		t.Fatalf("carol = %+v", row)
	}
}
