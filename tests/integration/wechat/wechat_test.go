package wechatint

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	fakewechat "openfox/tests/support/fakewechat/fake"
)

type qrView struct {
	ID        string  `json:"id"`
	AgentID   string  `json:"agent_id"`
	Status    string  `json:"status"`
	QRContent string  `json:"qr_content"`
	ErrorCode string  `json:"error_code"`
	BindingID *string `json:"binding_id"`
}

type bindingView struct {
	ID    string `json:"id"`
	Agent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	WeChatUser string `json:"wechat_user"`
	Initiator  struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"initiator"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason"`
	Connected    bool   `json:"connected"`
	CanManage    bool   `json:"can_manage"`
	Version      uint64 `json:"version"`
}

type listView struct {
	Configured  bool          `json:"configured"`
	CanBind     bool          `json:"can_bind"`
	MaxBindings int           `json:"max_bindings"`
	Bindings    []bindingView `json:"bindings"`
}

func (w *world) qrPath(suffix string) string {
	return w.path("/integrations/wechat/agent-bindings/qr" + suffix)
}

func (w *world) list(c *client) listView {
	w.t.Helper()
	var v listView
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/wechat/bindings"), nil, &v)
	return v
}

// confirmSteps 是一次正常扫码的状态序列。
func (w *world) confirmSteps(token, botID, user string) []fakewechat.QRStep {
	return []fakewechat.QRStep{{Status: "wait"}, {Status: "scaned"},
		{Status: "confirmed", BotToken: token, BotID: botID, UserID: user, BaseURL: w.fake.URL}}
}

// poll 反复查询扫码会话直到离开 PENDING / SCANNED。
func (w *world) poll(c *client, id string) qrView {
	w.t.Helper()
	var v qrView
	for i := 0; i < 20; i++ {
		c.json(http.StatusOK, http.MethodGet, w.qrPath("/"+id), nil, &v)
		if v.Status != "PENDING" && v.Status != "SCANNED" {
			return v
		}
	}
	w.t.Fatalf("qr %s still %s", id, v.Status)
	return v
}

func (w *world) startQR(c *client, agent string) qrView {
	w.t.Helper()
	var v qrView
	c.json(http.StatusCreated, http.MethodPost, w.qrPath(""), map[string]any{"agent_id": agent}, &v)
	return v
}

// bind 走完整的扫码流程并返回绑定 ID。
func (w *world) bind(c *client, agent, token, botID, user string) string {
	w.t.Helper()
	w.fake.QueueQR(w.confirmSteps(token, botID, user)...)
	qr := w.startQR(c, agent)
	done := w.poll(c, qr.ID)
	if done.Status != "CONFIRMED" || done.BindingID == nil {
		w.t.Fatalf("qr = %+v", done)
	}
	return *done.BindingID
}

func (w *world) conversationOf(bindingID string) string {
	w.t.Helper()
	var id string
	waitFor(w.t, "binding conversation", func() bool {
		w.pump()
		w.scan(&id, `SELECT COALESCE(conversation_id::text, '') FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, bindingID)
		return id != ""
	})
	return id
}

func (w *world) inboundStatus(bindingID, remoteID string) (string, string) {
	var status, result string
	w.scan(&status, `SELECT COALESCE((SELECT status FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid AND remote_message_id = $3), '')`, w.org, bindingID, remoteID)
	w.scan(&result, `SELECT COALESCE((SELECT result FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid AND remote_message_id = $3), '')`, w.org, bindingID, remoteID)
	return status, result
}

func (w *world) waitInbound(bindingID string, id int64, want string) string {
	w.t.Helper()
	remote := fmt.Sprint(id)
	var status, result string
	waitFor(w.t, "inbound "+want, func() bool {
		w.pump()
		status, result = w.inboundStatus(bindingID, remote)
		return status == want
	})
	return result
}

// 扫码绑定：权限、状态推进、幂等、令牌不外露、跨组织隔离、列表可见性与脱敏。
func TestQRBindingFlow(t *testing.T) {
	w := setup(t)
	ownerAgent := w.createAgent(w.owner, "Owner Agent", "SHARED")
	privateAgent := w.createAgent(w.owner, "Private Agent", "PRIVATE")

	// 权限与飞书「绑定 Agents」相同：非所有者成员 403、他人私有 Agent 404、访客 403、未知 Agent 404。
	w.member.expect(http.StatusForbidden, "", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": ownerAgent})
	w.member.expect(http.StatusNotFound, "", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": privateAgent})
	w.guest.expect(http.StatusForbidden, "", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": ownerAgent})
	w.owner.expect(http.StatusNotFound, "", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": w.newUUID()})
	w.owner.expect(http.StatusBadRequest, "", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": "not-a-uuid"})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+w.org+"/integrations/wechat/agent-bindings/qr", map[string]any{"agent_id": ownerAgent})
	// 未配置 iLink 的实例：409。
	bare := w.login(w.bare.URL+"/api", fmt.Sprintf("wc-owner-%s@example.test", w.org[:8]), w.ownerID)
	bare.expect(http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": ownerAgent})

	token, botID, user := "tok_flow_secret_value", "bot_flow@im.bot", "wxid_flow_user@im.wechat"
	w.fake.QueueQR(w.confirmSteps(token, botID, user)...)
	qr := w.startQR(w.owner, ownerAgent)
	if qr.Status != "PENDING" || qr.QRContent == "" || qr.AgentID != ownerAgent {
		t.Fatalf("qr = %+v", qr)
	}
	// 只有发起人能查询；管理员、其他组织都是 404。
	w.admin.expect(http.StatusNotFound, "", http.MethodGet, w.qrPath("/"+qr.ID), nil)
	w.member.expect(http.StatusNotFound, "", http.MethodPost, w.qrPath("/"+qr.ID+"/cancel"), nil)
	w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, "/v1/organizations/"+w.org+"/integrations/wechat/agent-bindings/qr/"+qr.ID, nil)

	var step qrView
	w.owner.json(http.StatusOK, http.MethodGet, w.qrPath("/"+qr.ID), nil, &step)
	if step.Status != "PENDING" {
		t.Fatalf("after first poll = %+v", step)
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.qrPath("/"+qr.ID), nil, &step)
	if step.Status != "SCANNED" {
		t.Fatalf("after second poll = %+v", step)
	}
	done := w.poll(w.owner, qr.ID)
	if done.Status != "CONFIRMED" || done.BindingID == nil {
		t.Fatalf("confirmed = %+v", done)
	}
	// 幂等：已确认后重复查询不再建绑定，也不再访问 iLink 状态接口。
	before := len(w.fake.Requests())
	for i := 0; i < 3; i++ {
		var again qrView
		w.owner.json(http.StatusOK, http.MethodGet, w.qrPath("/"+qr.ID), nil, &again)
		if again.Status != "CONFIRMED" || *again.BindingID != *done.BindingID {
			t.Fatalf("repeat poll = %+v", again)
		}
	}
	if len(w.fake.Requests()) != before {
		t.Fatal("terminal sessions must not call iLink again")
	}
	if n := w.count(`SELECT count(*) FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND status = 'ACTIVE'`, w.org, ownerAgent); n != 1 {
		t.Fatalf("active bindings = %d", n)
	}

	// 令牌：加密落库，列表与所有响应里都没有令牌、微信标识全文。
	var sealedHasPlain bool
	w.scan(&sealedHasPlain, `SELECT position(convert_to($3, 'UTF8') in bot_token_sealed) > 0 FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, *done.BindingID, token)
	if sealedHasPlain {
		t.Fatal("bot token must be stored encrypted")
	}
	v := w.list(w.owner)
	if !v.Configured || !v.CanBind || v.MaxBindings != 20 || len(v.Bindings) != 1 {
		t.Fatalf("list = %+v", v)
	}
	b := v.Bindings[0]
	if b.Agent.ID != ownerAgent || b.Initiator.ID != w.ownerID || b.Status != "ACTIVE" || !b.CanManage || b.WeChatUser == user || !strings.Contains(b.WeChatUser, "***") {
		t.Fatalf("binding = %+v", b)
	}
	// 非管理员只看到自己的；管理员看到全部；跨组织看不到。
	if got := w.list(w.member); len(got.Bindings) != 0 {
		t.Fatalf("member sees %+v", got.Bindings)
	}
	if got := w.list(w.admin); len(got.Bindings) != 1 || !got.Bindings[0].CanManage {
		t.Fatalf("admin sees %+v", got.Bindings)
	}
	w.guest.expect(http.StatusForbidden, "", http.MethodGet, w.path("/integrations/wechat/bindings"), nil)
	for _, secret := range []string{token, botID, user} {
		if strings.Contains(w.log.all(), secret) {
			t.Fatalf("API responses leak %q", secret)
		}
	}

	// 同一 Agent、同一发起人重复扫码：旧绑定标记 REMOVED，只剩一条有效绑定。
	again := w.bind(w.owner, ownerAgent, "tok_flow_two", "bot_flow_two@im.bot", "wxid_flow_user@im.wechat")
	if again == *done.BindingID {
		t.Fatal("expected a new binding")
	}
	var oldStatus string
	w.scan(&oldStatus, `SELECT status FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, *done.BindingID)
	if oldStatus != "REMOVED" {
		t.Fatalf("old binding status = %s", oldStatus)
	}
	if got := w.list(w.owner); len(got.Bindings) != 1 || got.Bindings[0].ID != again {
		t.Fatalf("list after replace = %+v", got.Bindings)
	}
	// 管理员可以给别人的 Agent 绑定；同一个 ilink_bot_id 不能被两个绑定占用。
	w.fake.QueueQR(w.confirmSteps("tok_dup", "bot_flow_two@im.bot", "wxid_other")...)
	dup := w.startQR(w.admin, ownerAgent)
	if got := w.poll(w.admin, dup.ID); got.Status != "FAILED" || got.ErrorCode != "bot_in_use" {
		t.Fatalf("duplicate bot = %+v", got)
	}
	// 取消。
	w.fake.QueueQR(fakewechat.QRStep{Status: "wait"})
	c := w.startQR(w.owner, ownerAgent)
	var cancelled qrView
	w.owner.json(http.StatusOK, http.MethodPost, w.qrPath("/"+c.ID+"/cancel"), nil, &cancelled)
	if cancelled.Status != "CANCELLED" {
		t.Fatalf("cancel = %+v", cancelled)
	}
}

// 验证码、过期、重定向与 SSRF 白名单。
func TestQRStatesAndSSRF(t *testing.T) {
	w := setup(t)
	agent := w.createAgent(w.owner, "Agent", "SHARED")

	// need_verifycode：错误验证码留在原状态，正确后确认；三次错误 BLOCKED。
	w.fake.QueueQR(fakewechat.QRStep{Status: "scaned"}, fakewechat.QRStep{Status: "need_verifycode", VerifyCode: "123456"},
		fakewechat.QRStep{Status: "confirmed", BotToken: "tok_v", BotID: "bot_v", UserID: "wxid_v", BaseURL: w.fake.URL})
	qr := w.startQR(w.owner, agent)
	var v qrView
	for i := 0; i < 5 && v.Status != "NEED_VERIFY"; i++ {
		w.owner.json(http.StatusOK, http.MethodGet, w.qrPath("/"+qr.ID), nil, &v)
	}
	if v.Status != "NEED_VERIFY" {
		t.Fatalf("status = %s", v.Status)
	}
	w.owner.expect(http.StatusBadRequest, "", http.MethodPost, w.qrPath("/"+qr.ID+"/verify"), map[string]any{"code": "bad code!"})
	w.owner.json(http.StatusOK, http.MethodPost, w.qrPath("/"+qr.ID+"/verify"), map[string]any{"code": "000000"}, &v)
	if v.Status != "NEED_VERIFY" {
		t.Fatalf("wrong code = %+v", v)
	}
	w.owner.json(http.StatusOK, http.MethodPost, w.qrPath("/"+qr.ID+"/verify"), map[string]any{"code": "123456"}, &v)
	if v.Status != "CONFIRMED" || v.BindingID == nil {
		t.Fatalf("right code = %+v", v)
	}

	w.fake.QueueQR(fakewechat.QRStep{Status: "need_verifycode", VerifyCode: "111111"}, fakewechat.QRStep{Status: "confirmed"})
	blocked := w.startQR(w.owner, agent)
	w.owner.json(http.StatusOK, http.MethodGet, w.qrPath("/"+blocked.ID), nil, &v)
	for i := 0; i < 3; i++ {
		w.owner.json(http.StatusOK, http.MethodPost, w.qrPath("/"+blocked.ID+"/verify"), map[string]any{"code": "222222"}, &v)
	}
	if v.Status != "BLOCKED" || v.ErrorCode != "verify_code_blocked" {
		t.Fatalf("blocked = %+v", v)
	}

	// expired。
	w.fake.QueueQR(fakewechat.QRStep{Status: "expired"})
	if got := w.poll(w.owner, w.startQR(w.owner, agent).ID); got.Status != "EXPIRED" {
		t.Fatalf("expired = %+v", got)
	}
	// 本地过期：会话超过有效期后不再访问 iLink。
	w.fake.QueueQR(fakewechat.QRStep{Status: "wait"})
	old := w.startQR(w.owner, agent)
	w.exec(`UPDATE openfox.wechat_qr_sessions SET expires_at = clock_timestamp() - interval '1 second' WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, old.ID)
	if got := w.poll(w.owner, old.ID); got.Status != "EXPIRED" {
		t.Fatalf("local expiry = %+v", got)
	}

	// 重定向到白名单内（等于配置基址）的主机：继续；confirmed 的 baseurl 之后落库。
	w.fake.QueueQR(fakewechat.QRStep{Status: "scaned_but_redirect", RedirectHost: w.fake.URL},
		fakewechat.QRStep{Status: "confirmed", BotToken: "tok_r", BotID: "bot_r", UserID: "wxid_r"})
	red := w.startQR(w.owner, agent)
	if got := w.poll(w.owner, red.ID); got.Status != "CONFIRMED" {
		t.Fatalf("redirect = %+v", got)
	}
	// 重定向到非白名单主机：拒绝，不跟随。
	for _, host := range []string{"evil.example.com", "https://weixin.qq.com.evil.example.com", "http://api.weixin.qq.com", "https://user@weixin.qq.com", "https://127.0.0.1:9"} {
		w.fake.QueueQR(fakewechat.QRStep{Status: "scaned_but_redirect", RedirectHost: host}, fakewechat.QRStep{Status: "wait"})
		q := w.startQR(w.owner, agent)
		if got := w.poll(w.owner, q.ID); got.Status != "FAILED" || got.ErrorCode != "untrusted_host" {
			t.Fatalf("redirect %s = %+v", host, got)
		}
	}
	// confirmed 返回的 baseurl 不在白名单：拒绝，不建绑定。
	for _, base := range []string{"https://evil.example.com", "http://169.254.169.254", "https://weixin.qq.com.evil.example.com", "file:///etc/passwd"} {
		w.fake.QueueQR(fakewechat.QRStep{Status: "confirmed", BotToken: "tok_x", BotID: "bot_x_" + base, UserID: "wxid_x", BaseURL: base})
		q := w.startQR(w.owner, agent)
		if got := w.poll(w.owner, q.ID); got.Status != "FAILED" || got.ErrorCode != "untrusted_host" || got.BindingID != nil {
			t.Fatalf("baseurl %s = %+v", base, got)
		}
	}
	// binded_redirect。
	w.fake.QueueQR(fakewechat.QRStep{Status: "binded_redirect"})
	if got := w.poll(w.owner, w.startQR(w.owner, agent).ID); got.Status != "FAILED" || got.ErrorCode != "already_bound" {
		t.Fatalf("binded_redirect = %+v", got)
	}
	// 生产模式的白名单（不允许等于配置基址）。
	prod := integrations.Service{WeChat: wechatClientProd(w.fake.URL)}
	if _, err := prod.WeChat.NormalizeBase(w.fake.URL); err == nil {
		t.Fatal("production must not allow the configured base")
	}
	if got, err := prod.WeChat.NormalizeBase("https://ilink.weixin.qq.com/path?x=1"); err != nil || got != "https://ilink.weixin.qq.com" {
		t.Fatalf("normalize = %q %v", got, err)
	}
}

// 收消息 → 会话 → Run 输入 → 回复带 context_token；去重、过滤、非文本、长文本分段。
func TestInboundAndReplies(t *testing.T) {
	w := setup(t)
	agent := w.createAgent(w.owner, "Agent", "SHARED")
	token, user := "tok_in", "wxid_in@im.wechat"
	binding := w.bind(w.owner, agent, token, "bot_in@im.bot", user)
	w.pump() // 启动轮询

	// 正常文本。
	id := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "帮我看看 PR", ContextToken: "ctx-1"})
	w.waitInbound(binding, id, "DONE")
	conv := w.conversationOf(binding)
	var origin, visibility, owner, body, clientID string
	w.scan(&origin, `SELECT origin FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, conv)
	w.scan(&visibility, `SELECT visibility FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, conv)
	w.scan(&owner, `SELECT owner_user_id::text FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, conv)
	w.scan(&body, `SELECT body FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'USER'`, w.org, conv)
	w.scan(&clientID, `SELECT client_message_id FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'USER'`, w.org, conv)
	if origin != "IM" || visibility != "PRIVATE" || owner != w.ownerID || body != "帮我看看 PR" || clientID != fmt.Sprintf("wechat:%d", id) {
		t.Fatalf("conversation = %s %s %s %q %s", origin, visibility, owner, body, clientID)
	}
	if n := w.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid`, w.org, conv); n != 1 {
		t.Fatalf("run inputs = %d", n)
	}

	// 回复：Agent 消息经 outbox 发回，带最近一条入站消息的 context_token，发往绑定的微信用户。
	w.agentSays(conv, agent, "AGENT", "好的，已经看过了")
	waitFor(t, "reply sent", func() bool { w.pump(); return len(w.fake.Sent()) == 1 })
	sent := w.fake.Sent()[0]
	if sent.Text != "好的，已经看过了" || sent.ContextToken != "ctx-1" || sent.ToUserID != user || sent.Token != token || sent.ClientID == "" {
		t.Fatalf("sent = %+v", sent)
	}
	// 后一条入站消息更新 context_token。
	id2 := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "第二条", ContextToken: "ctx-2"})
	w.waitInbound(binding, id2, "DONE")
	w.agentSays(conv, agent, "SYSTEM", "系统提示")
	waitFor(t, "second reply", func() bool { w.pump(); return len(w.fake.Sent()) == 2 })
	if got := w.fake.Sent()[1]; got.ContextToken != "ctx-2" || got.Text != "系统提示" {
		t.Fatalf("second sent = %+v", got)
	}

	// 重复消息（相同 message_id）只处理一次。
	dupID := w.fake.Push(token, fakewechat.Message{MessageID: id, FromUserID: user, Text: "帮我看看 PR"})
	_ = dupID
	for i := 0; i < 4; i++ {
		w.pump()
		time.Sleep(100 * time.Millisecond)
	}
	if n := w.count(`SELECT count(*) FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid AND remote_message_id = $3`, w.org, binding, fmt.Sprint(id)); n != 1 {
		t.Fatalf("duplicate inbound rows = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'USER'`, w.org, conv); n != 2 {
		t.Fatalf("user messages = %d", n)
	}

	// 他人消息、群消息、非用户消息一律丢弃（不入队）。
	rows := w.count(`SELECT count(*) FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid`, w.org, binding)
	w.fake.Push(token, fakewechat.Message{FromUserID: "wxid_stranger", Text: "我是陌生人"})
	w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "群里的话", GroupID: "group_1"})
	w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "bot 自己的", MessageType: 2})
	marker := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "后面一条正常的"})
	w.waitInbound(binding, marker, "DONE")
	if got := w.count(`SELECT count(*) FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid`, w.org, binding); got != rows+1 {
		t.Fatalf("inbound rows %d -> %d, want +1", rows, got)
	}

	// 非文本消息：回复提示。
	sentBefore := len(w.fake.Sent())
	nt := w.fake.Push(token, fakewechat.Message{FromUserID: user, NoText: true, ContextToken: "ctx-nt"})
	if result := w.waitInbound(binding, nt, "REJECTED"); result != "unsupported" {
		t.Fatalf("non-text result = %q", result)
	}
	waitFor(t, "unsupported reply", func() bool { w.pump(); return len(w.fake.Sent()) > sentBefore })
	if got := w.fake.Sent()[sentBefore]; got.Text != "目前只支持文字消息。" {
		t.Fatalf("unsupported reply = %+v", got)
	}

	// 长文本按段发送，段的 client_id 各不相同。
	sentBefore = len(w.fake.Sent())
	w.agentSays(conv, agent, "AGENT", strings.Repeat("长", 4000))
	waitFor(t, "long reply", func() bool { w.pump(); return len(w.fake.Sent()) >= sentBefore+3 })
	ids := map[string]bool{}
	total := 0
	for _, s := range w.fake.Sent()[sentBefore:] {
		ids[s.ClientID] = true
		total += len([]rune(s.Text))
	}
	if len(ids) != 3 || total != 4000 {
		t.Fatalf("parts = %d total runes = %d", len(ids), total)
	}

	// 请求头与方法：getupdates 带 Bearer、AuthorizationType、X-WECHAT-UIN、iLink-App-Id；二维码申请是 POST。
	var sawUpdates, sawQR bool
	for _, r := range w.fake.Requests() {
		switch r.Path {
		case "/ilink/bot/getupdates":
			if r.Authorization != "Bearer "+token || r.AuthType != "ilink_bot_token" || r.UIN == "" || r.AppID != "bot" || r.ClientVersion == "" {
				t.Fatalf("getupdates headers = %+v", r)
			}
			sawUpdates = true
		case "/ilink/bot/get_bot_qrcode":
			if r.Method != http.MethodPost || r.Authorization != "" {
				t.Fatalf("get_bot_qrcode = %+v", r)
			}
			sawQR = true
		case "/ilink/bot/get_qrcode_status":
			if r.Authorization != "" || r.AppID != "bot" {
				t.Fatalf("get_qrcode_status = %+v", r)
			}
		}
	}
	if !sawUpdates || !sawQR {
		t.Fatalf("requests missing: updates=%v qr=%v", sawUpdates, sawQR)
	}
	// 日志与存量数据里没有消息内容以外的凭据泄露：响应体不含 token。
	if strings.Contains(w.log.all(), token) {
		t.Fatal("token leaked in API responses")
	}
}

// 发送失败重试（client_id 不变）、-14 使绑定失效并停止轮询。
func TestRetryAndSessionExpiry(t *testing.T) {
	w := setup(t)
	agent := w.createAgent(w.owner, "Agent", "SHARED")
	token, user := "tok_ex", "wxid_ex"
	binding := w.bind(w.owner, agent, token, "bot_ex", user)
	w.pump()
	id := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "hi", ContextToken: "c"})
	w.waitInbound(binding, id, "DONE")
	conv := w.conversationOf(binding)

	w.fake.FailNextSends(2)
	w.agentSays(conv, agent, "AGENT", "重试内容")
	waitFor(t, "first failure recorded", func() bool {
		w.pump()
		return w.count(`SELECT count(*) FROM openfox.wechat_reply_outbox WHERE organization_id = $1::uuid AND attempts > 0`, w.org) == 1
	})
	// 退避到期后重试成功；client_id 只记录一次（对端去重）。
	w.exec(`UPDATE openfox.wechat_reply_outbox SET next_attempt_at = clock_timestamp() WHERE organization_id = $1::uuid AND status = 'PENDING'`, w.org)
	waitFor(t, "retry delivered", func() bool {
		w.pump()
		w.exec(`UPDATE openfox.wechat_reply_outbox SET next_attempt_at = clock_timestamp() WHERE organization_id = $1::uuid AND status = 'PENDING'`, w.org)
		return len(w.fake.Sent()) == 1
	})

	// -14：绑定变为 EXPIRED，轮询停止，页面能看到失效状态。
	w.fake.Expire(token)
	waitFor(t, "binding expired", func() bool {
		w.pump()
		v := w.list(w.owner)
		return len(v.Bindings) == 1 && v.Bindings[0].Status == "EXPIRED" && !v.Bindings[0].Connected
	})
	if w.poller.Sessions() != 0 {
		t.Fatalf("sessions after expiry = %d", w.poller.Sessions())
	}
	requests := len(w.fake.Requests())
	for i := 0; i < 3; i++ {
		w.pump()
		time.Sleep(100 * time.Millisecond)
	}
	if len(w.fake.Requests()) != requests {
		t.Fatal("an expired binding must not be polled again")
	}
	// 重新扫码替换失效的绑定。
	fresh := w.bind(w.owner, agent, "tok_ex2", "bot_ex2", user)
	if got := w.list(w.owner); len(got.Bindings) != 1 || got.Bindings[0].ID != fresh || got.Bindings[0].Status != "ACTIVE" {
		t.Fatalf("after rescan = %+v", got.Bindings)
	}
}

// 解绑：权限、版本、停止轮询并作废令牌。
func TestRemoveBinding(t *testing.T) {
	w := setup(t)
	agent := w.memberAgent()
	token := "tok_rm"
	binding := w.bind(w.member, agent, token, "bot_rm", "wxid_rm")
	w.pump()
	if w.poller.Sessions() != 1 {
		t.Fatalf("sessions = %d", w.poller.Sessions())
	}
	var item bindingView
	for _, b := range w.list(w.member).Bindings {
		item = b
	}
	rm := w.path("/integrations/wechat/bindings/" + binding + "/remove")
	w.guest.expect(http.StatusForbidden, "", http.MethodPost, rm, map[string]any{"version": item.Version})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+w.org+"/integrations/wechat/bindings/"+binding+"/remove", map[string]any{"version": item.Version})
	w.member.expect(http.StatusBadRequest, "", http.MethodPost, rm, map[string]any{})
	w.member.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, rm, map[string]any{"version": item.Version + 5})
	// 另一个普通成员看不到也删不了。
	other := w.user("wc-other-" + w.org[:8] + "@example.test")
	w.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, w.org, other)
	oc := w.login(w.server.URL+"/api", "wc-other-"+w.org[:8]+"@example.test", other)
	oc.expect(http.StatusNotFound, "", http.MethodPost, rm, map[string]any{"version": item.Version})
	// 管理员可以解绑别人的。
	w.admin.expect(http.StatusNoContent, "", http.MethodPost, rm, map[string]any{"version": item.Version})
	w.admin.expect(http.StatusNotFound, "", http.MethodPost, rm, map[string]any{"version": item.Version})
	if v := w.list(w.admin); len(v.Bindings) != 0 {
		t.Fatalf("after remove = %+v", v.Bindings)
	}
	var tokenLen int
	w.scan(&tokenLen, `SELECT octet_length(bot_token_sealed) FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, binding)
	if tokenLen != 0 {
		t.Fatalf("token must be wiped, length = %d", tokenLen)
	}
	waitFor(t, "polling stopped", func() bool { w.pump(); return w.poller.Sessions() == 0 })
	requests := len(w.fake.Requests())
	w.fake.Push(token, fakewechat.Message{FromUserID: "wxid_rm", Text: "解绑后"})
	for i := 0; i < 3; i++ {
		w.pump()
		time.Sleep(100 * time.Millisecond)
	}
	if len(w.fake.Requests()) != requests {
		t.Fatal("removed binding must not be polled")
	}
	if n := w.count(`SELECT count(*) FROM openfox.wechat_inbound WHERE organization_id = $1::uuid AND binding_id = $2::uuid`, w.org, binding); n != 0 {
		t.Fatalf("inbound after remove = %d", n)
	}
}

// 发起人失去成员身份 / Agent 权限 / Agent 被归档后，入站消息被拒绝并回复说明。
func TestInboundAuthorizationChanges(t *testing.T) {
	w := setup(t)
	agent := w.memberAgent()
	token, user := "tok_au", "wxid_au"
	binding := w.bind(w.member, agent, token, "bot_au", user)
	w.pump()
	ok := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "正常"})
	w.waitInbound(binding, ok, "DONE")

	// 失去 Agent 使用权限：把 Agent 所有者改成别人（成员既不是所有者也不是管理员）。
	w.exec(`UPDATE openfox.agents SET owner_user_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, agent, w.ownerID)
	lost := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "还能用吗"})
	if result := w.waitInbound(binding, lost, "REJECTED"); result != "forbidden" {
		t.Fatalf("lost permission = %q", result)
	}
	waitFor(t, "forbidden reply", func() bool {
		w.pump()
		for _, s := range w.fake.Sent() {
			if strings.Contains(s.Text, "使用权限") {
				return true
			}
		}
		return false
	})
	// Agent 被归档。
	w.exec(`UPDATE openfox.agents SET owner_user_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, agent, w.memberID)
	w.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, agent)
	gone := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "归档后"})
	if result := w.waitInbound(binding, gone, "REJECTED"); result != "agent_unavailable" {
		t.Fatalf("archived = %q", result)
	}
	// 发起人被移出组织。
	w.exec(`UPDATE openfox.agents SET archived_at = NULL WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, agent)
	w.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, w.org, w.memberID)
	revoked := w.fake.Push(token, fakewechat.Message{FromUserID: user, Text: "被移出后"})
	if result := w.waitInbound(binding, revoked, "REJECTED"); result != "member_unavailable" {
		t.Fatalf("revoked = %q", result)
	}
}

// 租约与并发上限：同一绑定只被一个实例轮询；超过上限的排队。
func TestLeaseAndCapacity(t *testing.T) {
	w := setup(t)
	a1 := w.createAgent(w.owner, "A1", "SHARED")
	a2 := w.createAgent(w.owner, "A2", "SHARED")
	w.bind(w.owner, a1, "tok_l1", "bot_l1", "wxid_l1")
	w.bind(w.owner, a2, "tok_l2", "bot_l2", "wxid_l2")
	second := &integrations.WeChatPoller{Pool: w.pool, Service: w.service, Instance: "poller-2", ScanInterval: time.Hour, RenewInterval: time.Hour}
	t.Cleanup(second.Stop)
	w.pump()
	if err := second.Scan(w.ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.poller.Sessions() + second.Sessions(); got != 2 {
		t.Fatalf("total sessions = %d, want 2 (one holder per binding)", got)
	}
	// 每个绑定只有一个持有者。
	if n := w.count(`SELECT count(DISTINCT lease_owner) FROM openfox.wechat_bindings WHERE organization_id = $1::uuid AND status = 'ACTIVE'`, w.org); n != 1 {
		t.Fatalf("distinct holders = %d (first instance should hold both)", n)
	}
	w.poller.Stop()
	// 租约释放后由另一个实例接手。
	if err := second.Scan(w.ctx); err != nil {
		t.Fatal(err)
	}
	if second.Sessions() != 2 {
		t.Fatalf("second instance sessions = %d", second.Sessions())
	}
	second.Stop()

	// 并发上限 1：另一个绑定排队，下一轮有空位才轮询。
	capped := &integrations.WeChatPoller{Pool: w.pool, Service: w.service, Instance: "poller-3", MaxSessions: 1, ScanInterval: time.Hour, RenewInterval: time.Hour}
	t.Cleanup(capped.Stop)
	if err := capped.Scan(w.ctx); err != nil {
		t.Fatal(err)
	}
	if capped.Sessions() != 1 {
		t.Fatalf("capped sessions = %d", capped.Sessions())
	}
}

// 每个用户的有效绑定上限。
func TestBindingLimit(t *testing.T) {
	w := setup(t)
	agent := w.createAgent(w.owner, "Agent", "SHARED")
	for i := 0; i < 20; i++ {
		other := w.createAgent(w.owner, fmt.Sprintf("Filler %d", i), "SHARED")
		w.exec(`INSERT INTO openfox.wechat_bindings (organization_id, agent_id, created_by, ilink_bot_id, ilink_user_id, bot_token_sealed, base_url)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'wxid_fill', '\x01'::bytea, 'https://ilinkai.weixin.qq.com')`,
			w.org, other, w.ownerID, fmt.Sprintf("bot_fill_%d_%s", i, w.org[:8]))
	}
	w.owner.expect(http.StatusConflict, "WECHAT_LIMIT_REACHED", http.MethodPost, w.qrPath(""), map[string]any{"agent_id": agent})
	if v := w.list(w.owner); len(v.Bindings) != 20 {
		t.Fatalf("bindings = %d", len(v.Bindings))
	}
}

// memberAgent 建一个归成员所有的 Agent（成员自己在空间里建 Agent 需要空间权限，与本测试无关，所以由所有者建好后改归属）。
func (w *world) memberAgent() string {
	w.t.Helper()
	id := w.createAgent(w.owner, "Member Agent", "SHARED")
	w.exec(`UPDATE openfox.agents SET owner_user_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, w.org, id, w.memberID)
	return id
}
