package feishuint

import (
	"net/http"
	"strings"
	"testing"
)

func TestPersonalAuthorization(t *testing.T) {
	w := setup(t)
	// 没有飞书应用：409；访客：403。
	w.member.expect(http.StatusConflict, "FEISHU_APP_NOT_CONNECTED", http.MethodPost, w.path("/integrations/feishu/authorize"), nil)
	w.guest.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/feishu/authorize"), nil)
	app := w.bind()

	// 成员事先自定义过飞书渠道（关闭，且关闭了「状态」事件）：绑定后渠道自动开启，已关闭的事件保留。
	w.member.json(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/feishu", map[string]any{"enabled": false, "disabled_events": []string{"status"}}, nil)
	w.fake.SetAuthorizeUser("ou_member")
	sessionID, location := w.authorize(w.member)
	// 其他成员不能使用别人的回调。
	if res := w.callback(w.owner, location); res.Status != "error" || res.Reason != "invalid_state" {
		t.Fatalf("foreign callback = %+v", res)
	}
	// 上一步没有消费会话（会话用户不符直接拒绝）：本人仍可完成。
	res := w.callback(w.member, location)
	if res.Status != "ok" || res.SessionID != sessionID {
		t.Fatalf("callback = %+v", res)
	}
	// state 只能用一次。
	if res := w.callback(w.member, location); res.Reason != "invalid_state" {
		t.Fatalf("replayed callback = %+v", res)
	}
	view := w.view(w.member)
	if view.Personal == nil || view.Personal.DisplayName != "Mem Ber" || view.Personal.Version != 1 {
		t.Fatalf("personal = %+v", view.Personal)
	}
	var session sessionInfo
	w.member.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+sessionID), nil, &session)
	if session.Provider != "feishu" || session.Purpose != "personal" || session.Status != "COMPLETED" || session.ConnectionID == nil || *session.ConnectionID != view.Personal.ConnectionID {
		t.Fatalf("session = %+v", session)
	}
	var channels struct {
		Channels []struct {
			Channel        string   `json:"channel"`
			Enabled        bool     `json:"enabled"`
			DisabledEvents []string `json:"disabled_events"`
		} `json:"channels"`
	}
	w.member.json(http.StatusOK, http.MethodGet, "/v1/me/notification-channels", nil, &channels)
	if len(channels.Channels) != 3 || channels.Channels[2].Channel != "feishu" || !channels.Channels[2].Enabled ||
		len(channels.Channels[2].DisabledEvents) != 1 || channels.Channels[2].DisabledEvents[0] != "status" {
		t.Fatalf("channels = %+v", channels.Channels)
	}
	// 用户令牌不落库：个人连接没有凭据。
	if n := w.count(`SELECT count(*) FROM openfox.integration_connections WHERE id = $1::uuid AND credentials_sealed IS NULL AND credentials_hint = ''`, view.Personal.ConnectionID); n != 1 {
		t.Fatal("personal connection must not store any credential")
	}

	// 无效 state / 用户拒绝 / 换取失败。
	if res := w.callback(w.admin, w.publicURL+"/api/v1/integrations/feishu/callback?code=x&state=bogus"); res.Reason != "invalid_state" {
		t.Fatalf("bogus state = %+v", res)
	}
	w.fake.SetDenyAuthorize(true)
	denied := w.link(w.admin, "ou_admin")
	w.fake.SetDenyAuthorize(false)
	if denied.Status != "error" || denied.Reason != "access_denied" {
		t.Fatalf("denied = %+v", denied)
	}
	var failed sessionInfo
	w.admin.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+denied.SessionID), nil, &failed)
	if failed.Status != "FAILED" || failed.ErrorCode != "access_denied" {
		t.Fatalf("failed session = %+v", failed)
	}
	// 授权页同意后飞书 App Secret 被改（换取令牌失败）。
	w.fake.SetAuthorizeUser("ou_admin")
	_, exchangeLocation := w.authorize(w.admin)
	w.fake.SetAppSecret(w.appID, "changed-secret-0123456789")
	if res := w.callback(w.admin, exchangeLocation); res.Reason != "exchange_failed" {
		t.Fatalf("exchange failure = %+v", res)
	}
	w.fake.SetAppSecret(w.appID, w.secret)

	// 同一个飞书账号不能绑定给本组织的另一个成员。
	if res := w.link(w.admin, "ou_member"); res.Status != "error" || res.Reason != "account_in_use" {
		t.Fatalf("account in use = %+v", res)
	}
	if v := w.view(w.admin); v.Personal != nil {
		t.Fatalf("admin personal = %+v", v.Personal)
	}
	// 重新授权替换为新账号（同一成员只有一个有效个人连接）。
	w.mustLink(w.member, "ou_member2")
	replaced := w.view(w.member)
	if replaced.Personal == nil || replaced.Personal.ConnectionID != view.Personal.ConnectionID || replaced.Personal.DisplayName != "Mem Ber Two" || replaced.Personal.Version != 2 {
		t.Fatalf("replaced = %+v", replaced.Personal)
	}
	if n := w.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id = $1::uuid AND provider = 'feishu' AND scope = 'PERSONAL' AND status = 'ACTIVE' AND owner_user_id = $2::uuid`, w.org, w.memberID); n != 1 {
		t.Fatalf("active personal connections = %d", n)
	}
	// 旧账号随即空出来，管理员可以绑定。
	w.mustLink(w.admin, "ou_member")

	// 解绑：本人断开个人连接，不需要确认短语。
	w.member.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+view.Personal.ConnectionID+"/disconnect"),
		map[string]any{"version": replaced.Personal.Version}, nil)
	if v := w.view(w.member); v.Personal != nil || v.App == nil || v.App.ConnectionID != app.ConnectionID {
		t.Fatalf("after unbind = %+v", v)
	}
	w.assertNoSecretLeaked("changed-secret-0123456789")
}

type sessionInfo struct {
	Provider     string  `json:"provider"`
	Purpose      string  `json:"purpose"`
	Status       string  `json:"status"`
	ErrorCode    string  `json:"error_code"`
	ConnectionID *string `json:"connection_id"`
}

func TestLinkPreview(t *testing.T) {
	w := setup(t)
	app := w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	w.mustLink(w.member, "ou_member")
	w.mustLink(w.owner, "ou_owner")
	foxTask := w.task(w.foxSpace, "Ship the 登录 page", "")
	secretTask := w.task(w.secSpace, "Confidential roadmap", "")
	const generic = `{"inline":{"title":"OpenFox 任务"}}`

	got := w.preview("ou_member", w.taskURL(w.org, w.foxSpace, foxTask.DisplayID))
	if !strings.Contains(got, strings.ToUpper(foxTask.DisplayID)+" · Ship the 登录 page · ") || !strings.Contains(got, `"inline"`) {
		t.Fatalf("member preview = %s", got)
	}
	// 前缀大小写不敏感。
	if got := w.preview("ou_member", w.taskURL(w.org, w.foxSpace, strings.ToLower(foxTask.DisplayID))); !strings.Contains(got, "Ship the 登录 page") {
		t.Fatalf("lower-case display id = %s", got)
	}
	// 未绑定的飞书用户：通用文案，不泄露标题。
	if got := w.preview("ou_stranger", w.taskURL(w.org, w.foxSpace, foxTask.DisplayID)); got != generic {
		t.Fatalf("stranger preview = %s", got)
	}
	// 成员不在私有空间：通用文案；空间所有者能看到。
	if got := w.preview("ou_member", w.taskURL(w.org, w.secSpace, secretTask.DisplayID)); got != generic || strings.Contains(got, "Confidential") {
		t.Fatalf("member on private task = %s", got)
	}
	if got := w.preview("ou_owner", w.taskURL(w.org, w.secSpace, secretTask.DisplayID)); !strings.Contains(got, "Confidential roadmap") {
		t.Fatalf("owner on private task = %s", got)
	}
	// 任务不存在：通用文案。
	if got := w.preview("ou_member", w.taskURL(w.org, w.foxSpace, "FOX-9999")); got != generic {
		t.Fatalf("missing task = %s", got)
	}
	// 其他组织的链接 / 不是本站的链接 / 不是任务链接：不渲染。
	if got := w.preview("ou_member", w.taskURL(w.orgB, w.foxSpace, foxTask.DisplayID)); got != "{}" {
		t.Fatalf("foreign org link = %s", got)
	}
	if got := w.preview("ou_member", "http://evil.example/o/"+w.org+"/spaces/"+w.foxSpace+"/tasks?task="+foxTask.DisplayID); got != "{}" {
		t.Fatalf("foreign host link = %s", got)
	}
	if got := w.preview("ou_member", w.publicURL+"/o/"+w.org+"/inbox"); got != "{}" {
		t.Fatalf("non-task link = %s", got)
	}
	// 成员被移出组织后不再看到标题。
	w.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, w.org, w.memberID)
	if got := w.preview("ou_member", w.taskURL(w.org, w.foxSpace, foxTask.DisplayID)); got != generic {
		t.Fatalf("revoked member preview = %s", got)
	}
	w.exec(`UPDATE openfox.organization_members SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, w.org, w.memberID)
	// 开关关闭：不渲染。
	w.owner.json(http.StatusOK, http.MethodPatch, w.appPath()+"/settings", map[string]any{"link_preview_enabled": false, "version": app.Version}, nil)
	if got := w.preview("ou_member", w.taskURL(w.org, w.foxSpace, foxTask.DisplayID)); got != "{}" {
		t.Fatalf("disabled preview = %s", got)
	}
}
