package feishuint

// 测试夹具：任务助手 / Agent 机器人、扫码会话、IM worker。

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations"
	"openfox/internal/tenancy"
)

type botWorld struct {
	*world
	deviceID, grantID string
	imw               *integrations.IMWorker
	mgr               *manager
	owner2            string // 在飞书里的 union_id
}

type regView struct {
	ID              string  `json:"id"`
	Purpose         string  `json:"purpose"`
	SpaceID         string  `json:"space_id"`
	AgentID         string  `json:"agent_id"`
	Status          string  `json:"status"`
	ErrorCode       string  `json:"error_code"`
	VerificationURL string  `json:"verification_url"`
	PollInterval    int     `json:"poll_interval"`
	BotID           *string `json:"bot_id"`
}

type botView struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ConnectionID string `json:"connection_id"`
	AppID        string `json:"app_id"`
	BotName      string `json:"bot_name"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason"`
	Agent        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	Space *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"space"`
	ExtraPrompt string `json:"extra_prompt"`
	Runtime     *struct {
		ComputerID string `json:"computer_id"`
		GrantID    string `json:"grant_id"`
		Runtime    string `json:"runtime"`
		Model      string `json:"model"`
	} `json:"runtime"`
	CanManage bool   `json:"can_manage"`
	Version   uint64 `json:"version"`
}

type botsView struct {
	AppConnected bool `json:"app_connected"`
	Assistants   []struct {
		SpaceID               string   `json:"space_id"`
		SpaceName             string   `json:"space_name"`
		CanManage             bool     `json:"can_manage"`
		Bot                   *botView `json:"bot"`
		PendingRegistrationID *string  `json:"pending_registration_id"`
		BoundProvider         string   `json:"bound_provider"`
	} `json:"assistants"`
	Agents        []botView `json:"agents"`
	CanBindAgent  bool      `json:"can_bind_agent"`
	BuiltinPrompt string    `json:"builtin_prompt"`
}

func (b *botWorld) botsPath(suffix string) string {
	return b.path("/integrations/" + b.provider + suffix)
}

// linkUnion 让 c 用飞书用户（union_id）走完整的授权流程，个人连接里会保存 union_id。
func (w *world) linkUnion(c *client, union, name string) {
	w.t.Helper()
	w.fake.AddUnionUser(w.appID, union, name)
	w.fake.SetAuthorizeUnion(union)
	_, location := w.authorize(c)
	if res := w.callback(c, location); res.Status != "ok" {
		w.t.Fatalf("link %s = %+v", union, res)
	}
}

func setupBots(t *testing.T) *botWorld {
	return setupBotsFor(t, "feishu", true)
}

func setupBotsFor(t *testing.T, provider string, bindApp bool) *botWorld {
	w := setupFor(t, provider)
	w.fake.SetRegistrationBrand(provider)
	b := &botWorld{world: w}
	if bindApp {
		w.bind()
	}
	b.mgr = w.startManager("bots-1")
	if bindApp {
		w.waitStatus("CONNECTED")
	}
	b.deviceID, b.grantID = w.newUUID(), ""
	w.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name, last_seen_at)
		VALUES ($1::uuid, $2, 4, $3::uuid, 'bots-mac', clock_timestamp())`, b.deviceID, "bots-device-"+w.org[:8], w.ownerID)
	w.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, w.org, b.deviceID)
	w.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, w.org, b.deviceID)
	if bindApp {
		w.linkUnion(w.owner, "on_owner", "Lin Owner")
		w.linkUnion(w.member, "on_member", "Mem Ber")
		w.linkUnion(w.admin, "on_admin", "Ad Min")
	}
	b.imw = &integrations.IMWorker{Pool: w.pool, Service: w.service, Instance: "imw-test"}
	return b
}

func (b *botWorld) runtimeBody() map[string]any {
	return map[string]any{"computer_id": b.deviceID, "grant_id": b.grantID, "runtime": "codex", "model": "gpt-5"}
}

// pump 跑一轮 IM worker。
func (b *botWorld) pump() {
	b.t.Helper()
	if err := b.imw.RunOnce(b.ctx); err != nil {
		b.t.Fatal(err)
	}
}

func (b *botWorld) getReg(c *client, id string) regView {
	b.t.Helper()
	var reg regView
	c.json(http.StatusOK, http.MethodGet, b.botsPath("/registrations/"+id), nil, &reg)
	return reg
}

func (b *botWorld) startAssistant(c *client, space string, extra string) regView {
	b.t.Helper()
	var reg regView
	c.json(http.StatusCreated, http.MethodPost, b.botsPath("/registrations"),
		map[string]any{"purpose": "task_assistant", "space_id": space, "runtime": b.runtimeBody(), "extra_prompt": extra}, &reg)
	return reg
}

func (b *botWorld) userCode(reg regView) string {
	b.t.Helper()
	u, err := neturl.Parse(reg.VerificationURL)
	if err != nil || u.Query().Get("user_code") == "" {
		b.t.Fatalf("verification url = %q", reg.VerificationURL)
	}
	return u.Query().Get("user_code")
}

// complete 在模拟飞书里确认授权并等 worker 处理完。
func (b *botWorld) complete(c *client, reg regView) regView {
	b.t.Helper()
	b.fake.SetAuthorizeUnion("on_owner")
	if !b.fake.ApproveRegistration(b.userCode(reg), "") {
		b.t.Fatal("fake did not accept the approval")
	}
	return b.waitReg(c, reg.ID)
}

func (b *botWorld) waitReg(c *client, id string) regView {
	b.t.Helper()
	var last regView
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		b.pump()
		last = b.getReg(c, id)
		if last.Status != "PENDING" {
			return last
		}
		time.Sleep(150 * time.Millisecond)
	}
	b.t.Fatalf("registration %s still PENDING", id)
	return last
}

func (b *botWorld) bots(c *client) botsView {
	b.t.Helper()
	var v botsView
	c.json(http.StatusOK, http.MethodGet, b.botsPath("/bots"), nil, &v)
	return v
}

func (b *botWorld) assistantBot(c *client, space string) *botView {
	b.t.Helper()
	for _, row := range b.bots(c).Assistants {
		if row.SpaceID == space {
			return row.Bot
		}
	}
	return nil
}

// makeAssistant 为空间建好任务助手并等它连上长连接，返回机器人。
func (b *botWorld) makeAssistant(space, extra string) botView {
	b.t.Helper()
	reg := b.complete(b.owner, b.startAssistant(b.owner, space, extra))
	if reg.Status != "COMPLETED" || reg.BotID == nil {
		b.t.Fatalf("registration = %+v", reg)
	}
	var bot *botView
	waitFor(b.t, "assistant connected", func() bool {
		bot = b.assistantBot(b.owner, space)
		return bot != nil && bot.Status == "CONNECTED"
	})
	return *bot
}

func (b *botWorld) createAgent(c *client, space, name, visibility string) string {
	b.t.Helper()
	var agent struct {
		ID string `json:"id"`
	}
	c.json(http.StatusCreated, http.MethodPost, b.path("/spaces/"+space+"/agents"),
		map[string]any{"name": name, "kind": "CODE", "visibility": visibility, "config": b.runtimeBody()}, &agent)
	return agent.ID
}

func (b *botWorld) makeAgentBot(c *client, agentID string) botView {
	b.t.Helper()
	var reg regView
	c.json(http.StatusCreated, http.MethodPost, b.botsPath("/registrations"), map[string]any{"purpose": "agent", "agent_id": agentID}, &reg)
	reg = b.complete(c, reg)
	if reg.Status != "COMPLETED" {
		b.t.Fatalf("agent registration = %+v", reg)
	}
	var found *botView
	waitFor(b.t, "agent bot connected", func() bool {
		for _, bot := range b.bots(c).Agents {
			if bot.Agent.ID == agentID && bot.Status == "CONNECTED" {
				bot := bot
				found = &bot
			}
		}
		return found != nil
	})
	return *found
}

// ---------- 入站消息 / 回发观察 ----------

type inboundRow struct {
	ID, Status, Result, MessageID, Text string
	Attempts                            int
}

func (b *botWorld) inbound(botID, remoteID string) *inboundRow {
	b.t.Helper()
	var row inboundRow
	err := b.super.QueryRow(b.ctx, `
		SELECT id::text, status, result, COALESCE(message_id::text, ''), text, attempts FROM openfox.im_inbound_messages
		 WHERE organization_id = $1::uuid AND bot_id = $2::uuid AND remote_message_id = $3`, b.org, botID, remoteID).
		Scan(&row.ID, &row.Status, &row.Result, &row.MessageID, &row.Text, &row.Attempts)
	if err != nil {
		return nil
	}
	return &row
}

// waitInbound 等入站消息入队，再驱动 worker 直到离开 PENDING / PROCESSING。
func (b *botWorld) waitInbound(botID, remoteID string) inboundRow {
	b.t.Helper()
	var row *inboundRow
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b.pump()
		if row = b.inbound(botID, remoteID); row != nil && row.Status != "PENDING" && row.Status != "PROCESSING" {
			return *row
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.t.Fatalf("inbound %s not finished: %+v", remoteID, row)
	return inboundRow{}
}

func (b *botWorld) inboundCount(botID string) int {
	return b.count(`SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id = $1::uuid AND bot_id = $2::uuid`, b.org, botID)
}

type convRow struct {
	ID, Visibility, Origin, Title, Owner, AgentID string
}

func (b *botWorld) conversationFor(botID, chatID string) convRow {
	b.t.Helper()
	var row convRow
	b.scan(&row.ID, `SELECT conversation_id::text FROM openfox.im_chat_bindings WHERE organization_id = $1::uuid AND bot_id = $2::uuid AND chat_id = $3`, b.org, botID, chatID)
	b.scan(&row.Visibility, `SELECT visibility FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, row.ID)
	b.scan(&row.Origin, `SELECT origin FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, row.ID)
	b.scan(&row.Title, `SELECT COALESCE(title, '') FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, row.ID)
	b.scan(&row.Owner, `SELECT owner_user_id::text FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, row.ID)
	b.scan(&row.AgentID, `SELECT agent_id::text FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, row.ID)
	return row
}

// agentSays 模拟 Agent / 系统在会话里发言（触发器入队回发）。消息要在租户事务里写，触发器才满足行级安全。
func (b *botWorld) agentSays(conversationID, agentID, kind, body string) string {
	b.t.Helper()
	id := b.newUUID()
	selection := tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.memberID, AuthorizationVersion: 1}
	err := b.pool.RunForOrganization(b.ctx, b.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			var seq int64
			if err := tx.QueryRow(ctx, `UPDATE openfox.conversations SET next_message_seq = next_message_seq + 1
				WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING next_message_seq - 1`, conversationID).Scan(&seq); err != nil {
				return err
			}
			if kind == "AGENT" {
				_, err := tx.Exec(ctx, `INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, author_agent_id, body)
					VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'AGENT', $4::uuid, $5)`, id, conversationID, seq, agentID, body)
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, body)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'SYSTEM', $4)`, id, conversationID, seq, body)
			return err
		})
	if err != nil {
		b.t.Fatal(err)
	}
	return id
}

func (b *botWorld) outboxStatus(messageID string) (status, replyTo string, attempts int, ok bool) {
	err := b.super.QueryRow(b.ctx, `SELECT status, reply_to_remote_id, attempts FROM openfox.im_reply_outbox WHERE organization_id = $1::uuid AND message_id = $2::uuid`,
		b.org, messageID).Scan(&status, &replyTo, &attempts)
	return status, replyTo, attempts, err == nil
}

func (b *botWorld) waitOutbox(messageID, want string) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		b.pump()
		if status, _, _, ok := b.outboxStatus(messageID); ok && status == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	status, _, _, ok := b.outboxStatus(messageID)
	b.t.Fatalf("outbox for %s = %q (present=%v), want %s", messageID, status, ok, want)
}

func sentTo(b *botWorld, appID string) []string {
	var out []string
	for _, m := range b.fake.Messages(appID) {
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", m.ReceiveIDType, m.ReceiveID, m.ReplyTo, m.Content))
	}
	return out
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
