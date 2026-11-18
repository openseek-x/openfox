package feishuint

import (
	"net/http"
	"strings"
	"testing"
)

func TestBindAndConnect(t *testing.T) {
	w := setup(t)
	if v := w.view(w.owner); v.App != nil || !v.CanManage {
		t.Fatalf("empty view = %+v", v)
	}
	app := w.bind()
	if app.Status != "PENDING" || app.AppID != w.appID || app.BotName != "OpenFox 助手" || app.Version != 1 ||
		app.CredentialsHint != w.secret[len(w.secret)-4:] || !app.LinkPreviewEnabled || app.LastConnectedAt != nil {
		t.Fatalf("bound app = %+v", app)
	}
	// App Secret 只以密文落库。
	if n := w.count(`SELECT strpos(encode(credentials_sealed, 'escape'), $2) FROM openfox.integration_connections WHERE id = $1::uuid`, app.ConnectionID, w.secret); n != 0 {
		t.Fatal("app secret is stored in plaintext")
	}
	if n := w.count(`SELECT count(*) FROM openfox.im_connection_states WHERE connection_id = $1::uuid AND state = 'PENDING'`, app.ConnectionID); n != 1 {
		t.Fatalf("connection state rows = %d", n)
	}
	member := w.view(w.member)
	if member.CanManage || member.App == nil || member.Personal != nil || member.Guide.RedirectURI != w.publicURL+"/api/v1/integrations/feishu/callback" ||
		member.Guide.LinkPreviewPattern != w.publicURL+"/o/*" || len(member.Guide.Events) != 1 || member.Guide.Events[0] != "im.message.receive_v1" ||
		len(member.Guide.Callbacks) != 1 || member.Guide.Callbacks[0] != "url.preview.get" || !strings.Contains(member.Guide.ScopesJSON, "im:message:send_as_bot") ||
		member.Guide.OpenPlatformURL != w.fake.URL+"/app" {
		t.Fatalf("member view = %+v", member)
	}
	w.guest.expect(http.StatusForbidden, "FORBIDDEN", http.MethodGet, w.appPath(), nil)

	w.startManager("mgr-a")
	connected := w.waitStatus("CONNECTED")
	if connected.LastConnectedAt == nil || connected.LastError != "" {
		t.Fatalf("connected app = %+v", connected)
	}
	if _, err := w.fake.PushEvent(w.appID, "im.message.receive_v1", `{"message":{"message_id":"om_1"}}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "last_event_at", func() bool { return w.view(w.owner).App.LastEventAt != nil })
	w.assertNoSecretLeaked()
}

func TestBindErrors(t *testing.T) {
	w := setup(t)
	connections := func() int {
		return w.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id = $1::uuid AND provider = 'feishu'`, w.org)
	}
	put := func(c *client, want int, code string, body map[string]any) {
		t.Helper()
		c.expect(want, code, http.MethodPut, w.appPath(), body)
	}
	good := map[string]any{"app_id": w.appID, "app_secret": w.secret}

	put(w.owner, http.StatusUnprocessableEntity, "FEISHU_CREDENTIALS_INVALID", map[string]any{"app_id": w.appID, "app_secret": "wrong-secret-0123456789"})
	w.fake.SetBotAvailable(w.appID, false)
	put(w.owner, http.StatusUnprocessableEntity, "FEISHU_BOT_UNAVAILABLE", good)
	w.fake.SetBotAvailable(w.appID, true)
	w.fake.SetServerError(true)
	put(w.owner, http.StatusBadGateway, "FEISHU_UNAVAILABLE", good)
	w.fake.SetServerError(false)
	if connections() != 0 {
		t.Fatal("a failed validation must not save anything")
	}
	put(w.member, http.StatusForbidden, "", good)
	put(w.guest, http.StatusForbidden, "", good)
	put(w.owner, http.StatusBadRequest, "INVALID_REQUEST", map[string]any{"app_id": "not-an-app", "app_secret": w.secret})
	put(w.owner, http.StatusBadRequest, "INVALID_REQUEST", map[string]any{"app_id": w.appID, "app_secret": "short"})
	w.fake.AddApp(w.appID+"x", "other-secret-0123456789", "其他机器人")

	app := w.bind()
	// 另一个组织不能绑定同一个 App ID。
	w.ownerB.expect(http.StatusConflict, "FEISHU_APP_IN_USE", http.MethodPut, "/v1/organizations/"+w.orgB+"/integrations/feishu/app", good)
	// 已绑定后：必须带 version、App ID 相同、版本一致。
	put(w.owner, http.StatusBadRequest, "INVALID_REQUEST", good)
	put(w.owner, http.StatusConflict, "FEISHU_APP_MISMATCH", map[string]any{"app_id": w.appID + "x", "app_secret": "other-secret-0123456789", "version": app.Version})
	put(w.owner, http.StatusConflict, "VERSION_CONFLICT", map[string]any{"app_id": w.appID, "app_secret": w.secret, "version": app.Version + 5})
	put(w.admin, http.StatusOK, "", map[string]any{"app_id": w.appID, "app_secret": w.secret, "version": app.Version})
	// 版本已被上一次更新推进。
	put(w.owner, http.StatusConflict, "VERSION_CONFLICT", map[string]any{"app_id": w.appID, "app_secret": w.secret, "version": app.Version})
	if connections() != 1 {
		t.Fatalf("connections = %d, want 1", connections())
	}
	// 未配置飞书 / 主密钥的实例。
	bare := w.login(w.bare.URL+"/api", "fs-owner-"+w.org[:8]+"@example.test", w.ownerID)
	bare.expect(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPut, w.appPath(), good)
	bare.expect(http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", http.MethodPost, w.path("/integrations/feishu/authorize"), nil)
	w.assertNoSecretLeaked("wrong-secret-0123456789", "other-secret-0123456789")
}

func TestVerifyAndSettings(t *testing.T) {
	w := setup(t)
	w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	verify := func(c *client, want int, code string) {
		t.Helper()
		c.expect(want, code, http.MethodPost, w.appPath()+"/verify", nil)
	}
	w.fake.SetBotAvailable(w.appID, false)
	verify(w.owner, http.StatusUnprocessableEntity, "FEISHU_BOT_UNAVAILABLE")
	app := w.waitStatus("NEEDS_ATTENTION")
	if app.StatusReason != "bot_unavailable" {
		t.Fatalf("app = %+v", app)
	}
	waitFor(t, "session stop", func() bool { return w.fake.ConnCount(w.appID) == 0 })
	w.fake.SetBotAvailable(w.appID, true)
	verify(w.owner, http.StatusOK, "")
	app = w.waitStatus("CONNECTED")
	if app.StatusReason != "" || app.LastError != "" {
		t.Fatalf("recovered app = %+v", app)
	}
	w.fake.SetAppSecret(w.appID, "rotated-secret-0123456789")
	verify(w.owner, http.StatusUnprocessableEntity, "FEISHU_CREDENTIALS_INVALID")
	if app = w.waitStatus("NEEDS_ATTENTION"); app.StatusReason != "credentials_invalid" {
		t.Fatalf("app = %+v", app)
	}
	w.fake.SetServerError(true)
	verify(w.owner, http.StatusBadGateway, "FEISHU_UNAVAILABLE")
	w.fake.SetServerError(false)
	if app = w.waitStatus("NEEDS_ATTENTION"); app.StatusReason != "credentials_invalid" {
		t.Fatalf("an unreachable feishu must not change the state: %+v", app)
	}
	verify(w.member, http.StatusForbidden, "")
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+w.orgB+"/integrations/feishu/app/verify", nil)

	// 恢复：改回密钥后重新验证。
	w.fake.SetAppSecret(w.appID, w.secret)
	verify(w.owner, http.StatusOK, "")
	app = w.waitStatus("CONNECTED")

	patch := func(c *client, want int, code string, body map[string]any) {
		t.Helper()
		c.expect(want, code, http.MethodPatch, w.appPath()+"/settings", body)
	}
	var patched appView
	w.owner.json(http.StatusOK, http.MethodPatch, w.appPath()+"/settings", map[string]any{"link_preview_enabled": false, "version": app.Version}, &patched)
	if patched.LinkPreviewEnabled || patched.Version != app.Version+1 {
		t.Fatalf("patched = %+v", patched)
	}
	patch(w.owner, http.StatusConflict, "VERSION_CONFLICT", map[string]any{"link_preview_enabled": true, "version": app.Version})
	patch(w.member, http.StatusForbidden, "", map[string]any{"link_preview_enabled": true, "version": patched.Version})
	patch(w.owner, http.StatusBadRequest, "INVALID_REQUEST", map[string]any{"version": patched.Version})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPatch, "/v1/organizations/"+w.orgB+"/integrations/feishu/app/settings",
		map[string]any{"link_preview_enabled": true, "version": 1})
	// 切换预览开关不应重建长连接。
	if n := w.fake.TotalConnects(w.appID); n > 3 {
		t.Fatalf("connects = %d", n)
	}
	w.assertNoSecretLeaked("rotated-secret-0123456789")
}

func TestUpdateCredentialsRestartsSession(t *testing.T) {
	w := setup(t)
	app := w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	if w.fake.TotalConnects(w.appID) != 1 {
		t.Fatalf("connects = %d", w.fake.TotalConnects(w.appID))
	}
	newSecret := "rotated" + w.secret[6:]
	w.fake.SetAppSecret(w.appID, newSecret)
	var updated appView
	w.owner.json(http.StatusOK, http.MethodPut, w.appPath(), map[string]any{"app_id": w.appID, "app_secret": newSecret, "version": app.Version}, &updated)
	if updated.Version != app.Version+1 || updated.Status != "PENDING" || updated.CredentialsHint != newSecret[len(newSecret)-4:] {
		t.Fatalf("updated = %+v", updated)
	}
	waitFor(t, "restart with the new secret", func() bool { return w.fake.TotalConnects(w.appID) == 2 })
	w.waitStatus("CONNECTED")
	if w.fake.ConnCount(w.appID) != 1 {
		t.Fatalf("open connections = %d", w.fake.ConnCount(w.appID))
	}
	w.assertNoSecretLeaked(newSecret)
}

func TestDisconnectStopsSession(t *testing.T) {
	w := setup(t)
	app := w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	body := map[string]any{"version": app.Version, "confirm_phrase": "断开 飞书"}
	w.member.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/connections/"+app.ConnectionID+"/disconnect"), body)
	w.owner.expect(http.StatusBadRequest, "CONFIRMATION_MISMATCH", http.MethodPost, w.path("/integrations/connections/"+app.ConnectionID+"/disconnect"),
		map[string]any{"version": app.Version, "confirm_phrase": "断开"})
	w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+app.ConnectionID+"/disconnect"), body, nil)
	waitFor(t, "session stop", func() bool { return w.fake.ConnCount(w.appID) == 0 })
	waitFor(t, "state STOPPED", func() bool {
		return w.count(`SELECT count(*) FROM openfox.im_connection_states WHERE connection_id = $1::uuid AND state = 'STOPPED' AND lease_owner = ''`, app.ConnectionID) == 1
	})
	if v := w.view(w.owner); v.App != nil {
		t.Fatalf("app after disconnect = %+v", v.App)
	}
	connects := w.fake.TotalConnects(w.appID)
	// 断开后可以用同一个 App ID 重新绑定，并建立新的长连接。
	again := w.bind()
	if again.ConnectionID == app.ConnectionID {
		t.Fatal("rebinding must create a new connection")
	}
	waitFor(t, "reconnect", func() bool { return w.fake.TotalConnects(w.appID) == connects+1 })
	w.waitStatus("CONNECTED")
	// GET /integrations/feishu 仍走通用详情。
	var detail struct {
		Provider struct {
			ID       string `json:"id"`
			Bindable bool   `json:"bindable"`
		} `json:"provider"`
		Connections []map[string]any `json:"connections"`
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/feishu"), nil, &detail)
	if detail.Provider.ID != "feishu" || !detail.Provider.Bindable || len(detail.Connections) != 2 {
		t.Fatalf("generic detail = %+v", detail)
	}
	for _, c := range detail.Connections {
		if _, ok := c["credentials_sealed"]; ok {
			t.Fatal("credentials must not be returned")
		}
	}
}
