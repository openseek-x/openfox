package feishuint

// 每个空间只有一个任务助手（im_bots_space_assistant_unique 不分服务商）：
// 空间的任务助手已经绑定在飞书时，钉钉 / Lark 的列表要告诉前端 bound_provider，并且绑定返回 409。

import (
	"net/http"
	"strings"
	"testing"
)

func TestAssistantBoundInAnotherProvider(t *testing.T) {
	b := setupBots(t)
	// 飞书先给"Fox"空间绑定任务助手（6d-2 的扫码流程）。
	feishuBot := b.makeAssistant(b.foxSpace, "")

	// 同一个组织再绑定钉钉应用。
	dtApp := "ding" + strings.ReplaceAll(b.org, "-", "")[:12]
	b.env.fakeDT.AddApp(dtApp, "dt-org-secret-0123456789")
	b.owner.json(http.StatusOK, http.MethodPut, b.path("/integrations/dingtalk/app"),
		map[string]any{"app_id": dtApp, "app_secret": "dt-org-secret-0123456789"}, nil)

	dtBots := func(c *client) botsView {
		var v botsView
		c.json(http.StatusOK, http.MethodGet, b.path("/integrations/dingtalk/bots"), nil, &v)
		return v
	}
	rowFor := func(v botsView, space string) (provider string, hasBot bool) {
		for _, row := range v.Assistants {
			if row.SpaceID == space {
				return row.BoundProvider, row.Bot != nil
			}
		}
		t.Fatalf("space %s missing from %+v", space, v.Assistants)
		return "", false
	}
	// 钉钉的列表：Fox 空间已经在飞书绑定，另一个空间没有。
	if provider, hasBot := rowFor(dtBots(b.owner), b.foxSpace); provider != "feishu" || hasBot {
		t.Fatalf("fox row in dingtalk list: bound_provider=%q hasBot=%v", provider, hasBot)
	}
	if provider, _ := rowFor(dtBots(b.owner), b.secSpace); provider != "" {
		t.Fatalf("secret space must not be flagged, got %q", provider)
	}
	// 普通成员（能读 Fox 空间）也能看到；Lark 的列表同样。
	if provider, _ := rowFor(dtBots(b.member), b.foxSpace); provider != "feishu" {
		t.Fatalf("member sees bound_provider=%q", provider)
	}
	var larkView botsView
	b.owner.json(http.StatusOK, http.MethodGet, b.path("/integrations/lark/bots"), nil, &larkView)
	if provider, _ := rowFor(larkView, b.foxSpace); provider != "feishu" {
		t.Fatalf("lark list bound_provider=%q", provider)
	}
	// 飞书自己的列表：助手就在这一行里，响应里没有 bound_provider 字段（飞书现有响应不变）。
	status, raw := b.owner.do(http.MethodGet, b.botsPath("/bots"), nil)
	if status != http.StatusOK || strings.Contains(raw, "bound_provider") {
		t.Fatalf("feishu list must not carry bound_provider: %d %.200s", status, raw)
	}

	// 钉钉在这个空间绑定任务助手：409 FEISHU_ASSISTANT_EXISTS，什么都不保存。
	botApp, botSecret := "ding"+strings.ReplaceAll(b.newUUID(), "-", "")[:12], "dt-bot-secret-0123456789"
	b.env.fakeDT.AddApp(botApp, botSecret)
	body := map[string]any{"purpose": "task_assistant", "space_id": b.foxSpace, "runtime": b.runtimeBody(),
		"app_key": botApp, "app_secret": botSecret}
	b.owner.expect(http.StatusConflict, "FEISHU_ASSISTANT_EXISTS", http.MethodPost, b.path("/integrations/dingtalk/bots"), body)
	if n := b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id = $1::uuid AND provider = 'dingtalk' AND account_kind = 'task_assistant'`, b.org); n != 0 {
		t.Fatalf("a rejected bind must not save a connection, got %d", n)
	}

	// 先在飞书移除，钉钉就可以绑定了，列表不再带 bound_provider（改为钉钉自己的助手）。
	b.owner.json(http.StatusNoContent, http.MethodPost, b.botsPath("/bots/"+feishuBot.ID+"/remove"), map[string]any{"version": feishuBot.Version}, nil)
	if provider, _ := rowFor(dtBots(b.owner), b.foxSpace); provider != "" {
		t.Fatalf("after removal bound_provider=%q, want empty", provider)
	}
	b.owner.json(http.StatusCreated, http.MethodPost, b.path("/integrations/dingtalk/bots"), body, nil)
	if provider, hasBot := rowFor(dtBots(b.owner), b.foxSpace); provider != "" || !hasBot {
		t.Fatalf("dingtalk list after bind: bound_provider=%q hasBot=%v", provider, hasBot)
	}
	// 反方向：现在飞书的列表告诉前端该空间的助手在钉钉。
	var feishuView botsView
	b.owner.json(http.StatusOK, http.MethodGet, b.botsPath("/bots"), nil, &feishuView)
	if provider, hasBot := rowFor(feishuView, b.foxSpace); provider != "dingtalk" || hasBot {
		t.Fatalf("feishu list: bound_provider=%q hasBot=%v", provider, hasBot)
	}
}
