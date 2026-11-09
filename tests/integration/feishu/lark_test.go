package feishuint

// Lark：与飞书同一套实现，按服务商参数化。Lark 使用独立的本机模拟服务（fakeLark），
// 用来证明 Lark 连接的 REST、长连接、授权都走 Lark 客户端的域名，而不是飞书的。
// 绝不访问真实的 open.larksuite.com / accounts.larksuite.com。

import (
	"net/http"
	"strings"
	"testing"
)

func TestLarkBindConnectAuthorizeNotify(t *testing.T) {
	w := setupFor(t, "lark")
	if v := w.view(w.owner); v.App != nil || !v.CanManage {
		t.Fatalf("empty view = %+v", v)
	}
	app := w.bind()
	if app.Status != "PENDING" || app.AppID != w.appID || app.BotName != "OpenFox 助手" || app.Version != 1 {
		t.Fatalf("bound app = %+v", app)
	}
	if n := w.count(`SELECT count(*) FROM openfox.integration_connections WHERE id = $1::uuid AND provider = 'lark'`, app.ConnectionID); n != 1 {
		t.Fatal("the connection must be stored with provider lark")
	}
	member := w.view(w.member)
	if member.Guide.RedirectURI != w.publicURL+"/api/v1/integrations/lark/callback" || member.Guide.OpenPlatformURL != w.fake.URL+"/app" {
		t.Fatalf("guide = %+v", member.Guide)
	}
	// 飞书页面看不到 Lark 的应用。
	var feishuView fullView
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/feishu/app"), nil, &feishuView)
	if feishuView.App != nil {
		t.Fatalf("the feishu page must not show a lark app: %+v", feishuView.App)
	}

	w.startManager("lark-mgr")
	w.waitStatus("CONNECTED")
	if w.fake.ConnCount(w.appID) != 1 || w.env.fake.ConnCount(w.appID) != 0 {
		t.Fatalf("long connection must use the lark server: lark=%d feishu=%d", w.fake.ConnCount(w.appID), w.env.fake.ConnCount(w.appID))
	}

	// 个人授权：回调、会话、通知渠道。
	linked := w.link(w.member, "ou_member")
	if linked.Status != "ok" {
		t.Fatalf("link = %+v", linked)
	}
	view := w.view(w.member)
	if view.Personal == nil || view.Personal.DisplayName != "Mem Ber" {
		t.Fatalf("personal = %+v", view.Personal)
	}
	var session sessionInfo
	_, location := w.authorize(w.admin)
	if res := w.callback(w.owner, location); res.Status != "error" || res.Reason != "invalid_state" {
		t.Fatalf("foreign callback = %+v", res)
	}
	w.member.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+linked.SessionID), nil, &session)
	if session.Provider != "lark" || session.Status != "COMPLETED" {
		t.Fatalf("session = %+v", session)
	}

	// 通知经 Lark 的模拟服务发出。
	task := w.task(w.foxSpace, "Lark launch plan", w.memberID)
	w.runNotify()
	row := w.outbox(w.memberID)
	if row == nil || row.Status != "DONE" || row.Remote == "" {
		t.Fatalf("outbox = %+v", row)
	}
	messages := w.fake.Messages(w.appID)
	if len(messages) != 1 || messages[0].ReceiveID != "ou_member" ||
		!strings.Contains(messages[0].Content, "tasks?task="+task.DisplayID) {
		t.Fatalf("messages = %+v", messages)
	}
	if len(w.env.fake.Messages(w.appID)) != 0 {
		t.Fatal("nothing may be sent through the feishu server for a lark connection")
	}
	w.assertNoSecretLeaked()
}

func TestLarkAndFeishuAreIndependent(t *testing.T) {
	w := setupFor(t, "lark")
	// 同一个组织可以同时绑定飞书应用和 Lark 应用；同一个 App ID 字符串在两个服务商下互不冲突。
	larkApp := w.bind()
	w.env.fake.AddApp(w.appID, w.secret, "飞书侧机器人")
	var feishuApp appView
	w.owner.json(http.StatusOK, http.MethodPut, w.path("/integrations/feishu/app"), map[string]any{"app_id": w.appID, "app_secret": w.secret}, &feishuApp)
	if feishuApp.Status != "PENDING" || feishuApp.BotName != "飞书侧机器人" {
		t.Fatalf("feishu app = %+v", feishuApp)
	}
	larkView := w.view(w.owner)
	if larkView.App == nil || larkView.App.BotName != "OpenFox 助手" || larkView.App.ConnectionID == feishuApp.ConnectionID {
		t.Fatalf("lark view = %+v", larkView.App)
	}
	// 另一个组织不能绑定同一个 Lark 应用，但可以绑定同一个 App ID 的飞书应用之外的 Lark 应用。
	w.ownerB.expect(http.StatusConflict, "FEISHU_APP_IN_USE", http.MethodPut, "/v1/organizations/"+w.orgB+"/integrations/lark/app",
		map[string]any{"app_id": w.appID, "app_secret": w.secret})
	// 未知服务商没有路由。
	w.owner.expect(http.StatusNotFound, "", http.MethodGet, w.path("/integrations/foo/app"), nil)
	// POST 在 ServeMux 里命中了仅 GET 的 {first}/{second} 分发入口，所以是 405（既有行为，与飞书之前一致）。
	w.owner.expect(http.StatusMethodNotAllowed, "", http.MethodPost, w.path("/integrations/foo/authorize"), nil)
	// 断开 Lark 不影响飞书。
	w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+larkApp.ConnectionID+"/disconnect"),
		map[string]any{"version": larkApp.Version, "confirm_phrase": "断开 Lark"}, nil)
	if v := w.view(w.owner); v.App != nil {
		t.Fatalf("lark app after disconnect = %+v", v.App)
	}
	var still fullView
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/feishu/app"), nil, &still)
	if still.App == nil || still.App.Status != "PENDING" {
		t.Fatalf("feishu app after lark disconnect = %+v", still.App)
	}
}
