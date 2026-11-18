package githubint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (w *world) settingsBody(mutate func(map[string]any)) map[string]any {
	values := map[string]any{"link_pull_requests": true, "link_commits": false, "pr_backlink_private": true,
		"pr_backlink_public": false, "issue_backlink_private": true, "issue_backlink_public": false, "branch_format": "username/identifier-title"}
	if mutate != nil {
		mutate(values)
	}
	return values
}

func (w *world) putSettings(c *client, want int, version uint64, mutate func(map[string]any)) {
	w.t.Helper()
	c.expect(want, "", http.MethodPut, w.path("/integrations/github/settings"),
		map[string]any{"version": version, "settings": w.settingsBody(mutate)})
}

func (w *world) settingsVersion() uint64 {
	var got struct {
		Version uint64 `json:"version"`
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/github/settings"), nil, &got)
	return got.Version
}

func (w *world) connection() connView {
	conns, _ := w.detail(w.owner)
	for _, item := range conns {
		if item.ID == w.connID {
			return item
		}
	}
	w.t.Fatalf("connection %s not found", w.connID)
	return connView{}
}

func TestGitHubIntegration(t *testing.T) {
	w := setup(t)
	identity := func() { w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-org") }
	identity()

	t.Run("not configured and no secrets key", func(t *testing.T) {
		bare := w.login(w.bare.URL+"/api", fmt.Sprintf("gh-owner-%s@example.test", w.org[:8]), w.ownerID)
		bare.expect(http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "organization"})
		var overview struct {
			Providers []struct {
				ID         string `json:"id"`
				Configured bool   `json:"configured"`
				Bindable   bool   `json:"bindable"`
			} `json:"providers"`
		}
		bare.json(http.StatusOK, http.MethodGet, w.path("/integrations"), nil, &overview)
		for _, p := range overview.Providers {
			if p.ID == "github" && (p.Configured || p.Bindable) {
				t.Fatalf("unconfigured github provider = %+v", p)
			}
		}
		nobox := w.login(w.nobox.URL+"/api", fmt.Sprintf("gh-owner-%s@example.test", w.org[:8]), w.ownerID)
		nobox.expect(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "organization"})
		w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations"), nil, &overview)
		for _, p := range overview.Providers {
			if p.ID == "github" && (!p.Configured || !p.Bindable) {
				t.Fatalf("configured github provider = %+v", p)
			}
		}
	})

	t.Run("start authorization permissions", func(t *testing.T) {
		w.member.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "organization"})
		w.guest.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "personal"})
		w.member.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/github/authorize"),
			map[string]any{"purpose": "reauthorize", "connection_id": w.newUUID()})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "other"})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, w.path("/integrations/github/authorize"), map[string]any{"purpose": "reauthorize"})
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, w.path("/integrations/github/authorize"),
			map[string]any{"purpose": "reauthorize", "connection_id": w.newUUID()})
	})

	t.Run("organization binding", func(t *testing.T) {
		sessionID, location := w.authorize(w.owner, "organization", "")
		if got := w.fake.Challenges(); len(got) != 1 || len(got[0]) < 43 {
			t.Fatalf("challenges = %v", got)
		}
		if view := w.session(w.owner, sessionID); view.Status != "PENDING" || len(view.Accounts) != 0 || view.Purpose != "organization" {
			t.Fatalf("pending session = %+v", view)
		}
		// 他人读不到会话；其他成员的登录会话访问回调不消费会话。
		w.admin.expect(http.StatusNotFound, "NOT_FOUND", http.MethodGet, w.path("/integrations/oauth/"+sessionID), nil)
		w.member.expect(http.StatusNotFound, "NOT_FOUND", http.MethodGet, w.path("/integrations/oauth/"+sessionID), nil)
		if r := w.callback(w.member, location); r.Status != "error" || r.Reason != "invalid_state" {
			t.Fatalf("callback by another member = %+v", r)
		}
		anonymous := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := anonymous.Get(location)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusFound || !strings.Contains(response.Header.Get("Location"), "reason=invalid_state") {
			t.Fatalf("anonymous callback = %d %s", response.StatusCode, response.Header.Get("Location"))
		}
		r := w.callback(w.owner, location)
		if r.Status != "ok" || r.SessionID != sessionID || r.Reason != "" {
			t.Fatalf("callback = %+v", r)
		}
		// state 只能用一次。
		if again := w.callback(w.owner, location); again.Status != "error" || again.Reason != "invalid_state" {
			t.Fatalf("replayed callback = %+v", again)
		}
		view := w.session(w.owner, sessionID)
		if view.Status != "SELECTING" || len(view.Accounts) != 3 {
			t.Fatalf("selecting session = %+v", view)
		}
		kinds := map[string]string{}
		for _, account := range view.Accounts {
			kinds[account.Login] = account.Kind
			if account.AvatarURL == "" || account.ConnectionID != nil {
				t.Fatalf("account = %+v", account)
			}
		}
		if kinds["openfox-dev"] != "organization" || kinds["other-org"] != "organization" || kinds["lin"] != "user" {
			t.Fatalf("account kinds = %v", kinds)
		}
		complete := w.path("/integrations/oauth/" + sessionID + "/complete")
		w.member.expect(http.StatusForbidden, "", http.MethodPost, complete, map[string]any{"accounts": []string{"openfox-dev"}})
		w.admin.expect(http.StatusNotFound, "", http.MethodPost, complete, map[string]any{"accounts": []string{"openfox-dev"}})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, complete, map[string]any{"accounts": []string{"not-listed"}})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, complete, map[string]any{"accounts": []string{}})
		var done struct {
			Connections []connView `json:"connections"`
		}
		w.owner.json(http.StatusOK, http.MethodPost, complete, map[string]any{"accounts": []string{"OpenFox-Dev"}}, &done)
		if len(done.Connections) != 1 {
			t.Fatalf("connections = %+v", done.Connections)
		}
		conn := done.Connections[0]
		if conn.ExternalID != "openfox-dev" || conn.DisplayName != "openfox-dev" || conn.AccountKind != "organization" ||
			conn.Status != "ACTIVE" || conn.AvatarURL == "" || conn.WebhookLastDeliveryAt != nil {
			t.Fatalf("connection = %+v", conn)
		}
		w.connID = conn.ID
		w.owner.expect(http.StatusConflict, "OAUTH_SESSION_STATE", http.MethodPost, complete, map[string]any{"accounts": []string{"openfox-dev"}})
		if view := w.session(w.owner, sessionID); view.Status != "COMPLETED" || len(view.Accounts) != 0 {
			t.Fatalf("completed session = %+v", view)
		}
		// 令牌只以密文落库，暂存的密文已清除。
		var plainPosition int
		w.scan(&plainPosition, `SELECT position(convert_to('gho_fake', 'UTF8') in credentials_sealed) FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		var cleared, hasHash bool
		w.scan(&cleared, `SELECT pending_sealed IS NULL AND code_verifier_sealed IS NULL FROM openfox.integration_oauth_states WHERE id = $1::uuid`, sessionID)
		w.scan(&hasHash, `SELECT webhook_token_hash IS NOT NULL AND credentials_sealed IS NOT NULL FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		if plainPosition != 0 || !cleared || !hasHash {
			t.Fatalf("sealed state: plain=%d cleared=%v hash=%v", plainPosition, cleared, hasHash)
		}
	})

	t.Run("binding again only updates the token", func(t *testing.T) {
		before := w.connection()
		sessionID, location := w.authorize(w.owner, "organization", "")
		if r := w.callback(w.owner, location); r.Status != "ok" {
			t.Fatalf("callback = %+v", r)
		}
		view := w.session(w.owner, sessionID)
		bound := false
		for _, account := range view.Accounts {
			if account.Login == "openfox-dev" {
				bound = account.ConnectionID != nil && *account.ConnectionID == w.connID
			}
		}
		if !bound {
			t.Fatalf("existing connection not reported: %+v", view.Accounts)
		}
		var done struct {
			Connections []connView `json:"connections"`
		}
		w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/oauth/"+sessionID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}}, &done)
		conns, _ := w.detail(w.owner)
		if len(done.Connections) != 1 || done.Connections[0].ID != w.connID || len(conns) != 1 || conns[0].Version <= before.Version {
			t.Fatalf("rebind = %+v conns=%+v", done.Connections, conns)
		}
	})

	t.Run("authorization failures", func(t *testing.T) {
		w.fake.DenyAuthorize(true)
		r := w.runOAuth(w.owner, "organization", "")
		w.fake.DenyAuthorize(false)
		if r.Status != "error" || r.Reason != "access_denied" {
			t.Fatalf("denied = %+v", r)
		}
		if view := w.session(w.owner, r.SessionID); view.Status != "FAILED" || view.ErrorCode != "access_denied" {
			t.Fatalf("denied session = %+v", view)
		}
		w.fake.FailExchange(true)
		r = w.runOAuth(w.owner, "organization", "")
		w.fake.FailExchange(false)
		if r.Status != "error" || r.Reason != "exchange_failed" {
			t.Fatalf("exchange = %+v", r)
		}
		if view := w.session(w.owner, r.SessionID); view.Status != "FAILED" || view.ErrorCode != "exchange_failed" {
			t.Fatalf("exchange session = %+v", view)
		}
		// 过期的会话在读取时变为 EXPIRED，不能再完成。
		sessionID, location := w.authorize(w.owner, "organization", "")
		w.callback(w.owner, location)
		w.exec(`UPDATE openfox.integration_oauth_states SET expires_at = clock_timestamp() - interval '1 minute' WHERE id = $1::uuid`, sessionID)
		w.owner.expect(http.StatusConflict, "OAUTH_SESSION_STATE", http.MethodPost, w.path("/integrations/oauth/"+sessionID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}})
		if view := w.session(w.owner, sessionID); view.Status != "EXPIRED" {
			t.Fatalf("expired session = %+v", view)
		}
		var cleared bool
		w.scan(&cleared, `SELECT pending_sealed IS NULL FROM openfox.integration_oauth_states WHERE id = $1::uuid`, sessionID)
		if !cleared {
			t.Fatal("expired session must clear its sealed token")
		}
		// 过期的 state 回调无效。
		_, location = w.authorize(w.owner, "organization", "")
		w.exec(`UPDATE openfox.integration_oauth_states SET expires_at = clock_timestamp() - interval '1 minute' WHERE status = 'PENDING' AND user_id = $1::uuid`, w.ownerID)
		if r := w.callback(w.owner, location); r.Reason != "invalid_state" {
			t.Fatalf("expired callback = %+v", r)
		}
	})

	t.Run("personal binding", func(t *testing.T) {
		w.fake.SetIdentity("octocat", 777, "elsewhere")
		defer identity()
		r := w.runOAuth(w.member, "personal", "")
		if r.Status != "ok" {
			t.Fatalf("personal = %+v", r)
		}
		_, personal := w.detail(w.member)
		if len(personal) != 1 || personal[0].DisplayName != "octocat" || personal[0].ExternalID != "777" ||
			personal[0].OwnerUserID == nil || *personal[0].OwnerUserID != w.memberID || personal[0].AccountKind != "user" {
			t.Fatalf("personal = %+v", personal)
		}
		view := w.session(w.member, r.SessionID)
		if view.Status != "COMPLETED" || view.ConnectionID == nil || *view.ConnectionID != personal[0].ID {
			t.Fatalf("personal session = %+v", view)
		}
		if again := w.runOAuth(w.member, "personal", ""); again.Status != "ok" {
			t.Fatalf("personal again = %+v", again)
		}
		if _, personal2 := w.detail(w.member); len(personal2) != 1 || personal2[0].ID != personal[0].ID {
			t.Fatalf("personal after rebind = %+v", personal2)
		}
		if _, ownerPersonal := w.detail(w.owner); len(ownerPersonal) != 0 {
			t.Fatalf("owner must not see member personal connection: %+v", ownerPersonal)
		}
	})

	t.Run("reauthorize", func(t *testing.T) {
		before := w.webhook(w.owner)
		w.exec(`UPDATE openfox.integration_connections SET status = 'NEEDS_ATTENTION', status_reason = 'token_revoked' WHERE id = $1::uuid`, w.connID)
		w.fake.SetIdentity("stranger", 9, "elsewhere")
		r := w.runOAuth(w.owner, "reauthorize", w.connID)
		identity()
		if r.Status != "error" || r.Reason != "account_mismatch" {
			t.Fatalf("mismatch = %+v", r)
		}
		if view := w.session(w.owner, r.SessionID); view.Status != "FAILED" || view.ErrorCode != "account_mismatch" {
			t.Fatalf("mismatch session = %+v", view)
		}
		if conn := w.connection(); conn.Status != "NEEDS_ATTENTION" || conn.StatusReason != "token_revoked" {
			t.Fatalf("connection after mismatch = %+v", conn)
		}
		r = w.runOAuth(w.owner, "reauthorize", w.connID)
		if r.Status != "ok" {
			t.Fatalf("reauthorize = %+v", r)
		}
		conn := w.connection()
		after := w.webhook(w.owner)
		if conn.Status != "ACTIVE" || conn.StatusReason != "" || after.PayloadURL != before.PayloadURL || after.Secret != before.Secret {
			t.Fatalf("after reauthorize conn=%+v", conn)
		}
		// 另一个组织的连接不能被重新授权。
		var otherConn string
		w.scan(&otherConn, `INSERT INTO openfox.integration_connections (organization_id, provider, scope, external_id, display_name, account_kind, created_by)
			VALUES ($1::uuid, 'github', 'ORGANIZATION', 'b-org', 'b-org', 'organization', $2::uuid) RETURNING id::text`, w.orgB, w.ownerBID)
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, w.path("/integrations/github/authorize"),
			map[string]any{"purpose": "reauthorize", "connection_id": otherConn})
		// 已断开的连接可以重新连接；断开后 Webhook 地址失效。
		current := w.connection()
		var disconnected connView
		w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+w.connID+"/disconnect"),
			map[string]any{"version": current.Version, "confirm_phrase": "断开 GitHub"}, &disconnected)
		if disconnected.Status != "DISCONNECTED" {
			t.Fatalf("disconnect = %+v", disconnected)
		}
		w.owner.expect(http.StatusNotFound, "", http.MethodGet, w.path("/integrations/connections/"+w.connID+"/webhook"), nil)
		if status, _ := w.post(before.PayloadURL, before.Secret, "ping", "ping-disconnected", []byte(`{"zen":"x"}`)); status != http.StatusNotFound {
			t.Fatalf("webhook for a disconnected connection = %d", status)
		}
		if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "ok" {
			t.Fatalf("reconnect = %+v", r)
		}
		if conn := w.connection(); conn.Status != "ACTIVE" {
			t.Fatalf("reconnected = %+v", conn)
		}
		if status, _ := w.post(before.PayloadURL, before.Secret, "ping", "ping-reconnected", []byte(`{"zen":"x"}`)); status != http.StatusOK {
			t.Fatalf("webhook after reconnect = %d", status)
		}
	})

	t.Run("webhook configuration and rotation", func(t *testing.T) {
		path := w.path("/integrations/connections/" + w.connID + "/webhook")
		w.member.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.guest.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.admin.expect(http.StatusOK, "", http.MethodGet, path, nil)
		view := w.webhook(w.owner)
		if !strings.HasPrefix(view.PayloadURL, w.publicURL+"/api/v1/hooks/git/github/") || len(view.Secret) != 64 || view.ContentType != "application/json" ||
			len(view.Events) != 4 || view.Events[0] != "pull_request" || view.Events[1] != "push" || view.Events[2] != "issues" || view.Events[3] != "issue_comment" || view.Version == 0 {
			t.Fatalf("webhook = %+v", view)
		}
		var audits int
		w.scan(&audits, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'integration.webhook_secret_viewed'`, w.org)
		if audits == 0 {
			t.Fatal("viewing the webhook secret must be audited")
		}
		rotate := path + "/rotate"
		w.owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, rotate, map[string]any{"version": view.Version + 5})
		w.member.expect(http.StatusForbidden, "", http.MethodPost, rotate, map[string]any{"version": view.Version})
		var rotated webhookView
		w.owner.json(http.StatusOK, http.MethodPost, rotate, map[string]any{"version": view.Version}, &rotated)
		if rotated.PayloadURL == view.PayloadURL || rotated.Secret == view.Secret || rotated.Version <= view.Version || rotated.LastDeliveryAt != nil {
			t.Fatalf("rotated = %+v", rotated)
		}
		if status, _ := w.post(view.PayloadURL, view.Secret, "ping", "old-token", []byte(`{"zen":"x"}`)); status != http.StatusNotFound {
			t.Fatalf("old token = %d", status)
		}
		// 旧密钥签名到新地址也不通过。
		if status, _ := w.post(rotated.PayloadURL, view.Secret, "ping", "old-secret", []byte(`{"zen":"x"}`)); status != http.StatusUnauthorized {
			t.Fatalf("old secret = %d", status)
		}
		if status, _ := w.post(rotated.PayloadURL, rotated.Secret, "ping", "new-token", []byte(`{"zen":"x"}`)); status != http.StatusOK {
			t.Fatalf("new token = %d", status)
		}
		w.webhookURL, w.webhookSecret = rotated.PayloadURL, rotated.Secret
		if conn := w.connection(); conn.WebhookLastDeliveryAt == nil {
			t.Fatal("webhook_last_delivery_at must be set after a valid delivery")
		}
	})

	t.Run("settings", func(t *testing.T) {
		path := w.path("/integrations/github/settings")
		var got struct {
			Provider  string         `json:"provider"`
			Settings  map[string]any `json:"settings"`
			CanManage bool           `json:"can_manage"`
			Version   uint64         `json:"version"`
		}
		w.member.json(http.StatusOK, http.MethodGet, path, nil, &got)
		if got.Version != 0 || got.CanManage || got.Settings["link_pull_requests"] != true || got.Settings["link_commits"] != false ||
			got.Settings["pr_backlink_private"] != true || got.Settings["pr_backlink_public"] != false || got.Settings["issue_backlink_private"] != true || got.Settings["issue_backlink_public"] != false || got.Settings["branch_format"] != "username/identifier-title" {
			t.Fatalf("default settings = %+v", got)
		}
		w.guest.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.owner.expect(http.StatusNotFound, "", http.MethodGet, w.path("/integrations/other/settings"), nil)
		w.member.expect(http.StatusForbidden, "", http.MethodPut, path, map[string]any{"version": 0, "settings": w.settingsBody(nil)})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPut, path, map[string]any{"version": 0, "settings": w.settingsBody(func(m map[string]any) { m["branch_format"] = "bogus" })})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPut, path, map[string]any{"version": 0, "settings": map[string]any{"link_commits": true}})
		w.putSettings(w.owner, http.StatusOK, 0, func(m map[string]any) { m["branch_format"] = "identifier" })
		w.putSettings(w.owner, http.StatusConflict, 0, nil)
		w.putSettings(w.owner, http.StatusConflict, 7, nil)
		w.owner.json(http.StatusOK, http.MethodGet, path, nil, &got)
		if got.Version != 1 || got.Settings["branch_format"] != "identifier" || !got.CanManage {
			t.Fatalf("saved settings = %+v", got)
		}
		w.putSettings(w.admin, http.StatusOK, 1, nil)
		if w.settingsVersion() != 2 {
			t.Fatal("settings version must advance")
		}
	})

	t.Run("pull request webhooks", func(t *testing.T) { runPullRequestCases(t, w) })
	t.Run("push webhooks", func(t *testing.T) { runPushCases(t, w) })
	t.Run("task git links visibility", func(t *testing.T) { runVisibilityCases(t, w) })
	t.Run("cross organization isolation", func(t *testing.T) { runIsolationCases(t, w) })

	t.Run("no token in any response", func(t *testing.T) {
		w.log.mu.Lock()
		defer w.log.mu.Unlock()
		for _, body := range w.log.bodies {
			if strings.Contains(body, "gho_fake") || strings.Contains(body, "csecret") {
				t.Fatalf("response leaks a credential: %.200s", body)
			}
		}
		if len(w.log.bodies) < 50 {
			t.Fatalf("expected many logged responses, got %d", len(w.log.bodies))
		}
	})
}

func runPullRequestCases(t *testing.T, w *world) {
	t1, t2, t3, t4 := w.task(w.foxSpace, "task one"), w.task(w.foxSpace, "task two"), w.task(w.foxSpace, "task three"), w.task(w.foxSpace, "task four")
	w.setTaskStatus(t3, "Done")
	w.setTaskStatus(t2, "Backlog")
	signed := func(delivery string, spec prSpec) (int, string) {
		return w.post(w.webhookURL, w.webhookSecret, "pull_request", delivery, spec.payload())
	}

	// 缺签名、错误签名：401 且不落库。
	unsigned, _ := http.NewRequest(http.MethodPost, w.webhookURL, bytes.NewReader(prSpec{Action: "opened", Number: 1, Title: t1.DisplayID}.payload()))
	unsigned.Header.Set("X-GitHub-Event", "pull_request")
	unsigned.Header.Set("X-GitHub-Delivery", "unsigned")
	if response, err := http.DefaultClient.Do(unsigned); err != nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned request: %v %v", response, err)
	}
	if status, _ := w.post(w.webhookURL, "wrong-secret", "pull_request", "badsig", prSpec{Action: "opened", Number: 1, Title: t1.DisplayID}.payload()); status != http.StatusUnauthorized {
		t.Fatalf("wrong signature = %d", status)
	}
	var rejected int
	w.scan(&rejected, `SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE delivery_id IN ('unsigned', 'badsig')`)
	if rejected != 0 || len(w.gitLinks(w.owner, w.foxSpace, t1)) != 0 {
		t.Fatalf("rejected deliveries must leave no trace (rows=%d)", rejected)
	}

	// ping。
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "ping", "ping-1", []byte(`{"zen":"keep it logically awesome"}`)); status != http.StatusOK {
		t.Fatalf("ping = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "PROCESSED" || detail != "ping" {
		t.Fatalf("ping delivery = %s %s", status, detail)
	}

	// PR 打开：建立关联，TODO 任务移到第一个进行中状态，写动态，私有仓库回链。
	opened := prSpec{Action: "opened", Number: 1, Title: "Implement " + t1.DisplayID, Branch: "lin/work", Private: true}
	eventsBefore := w.changedEvents(t1)
	if status, body := signed("dup-1", opened); status != http.StatusOK || strings.Contains(body, `"duplicate":true`) {
		t.Fatalf("opened = %d %s", status, body)
	}
	// 状态变更已由 tasks 表触发器发出 task.changed，不重复补发。
	if got := w.changedEvents(t1) - eventsBefore; got != 1 {
		t.Fatalf("task.changed events for a task whose status moved = %d, want 1", got)
	}
	links := w.gitLinks(w.owner, w.foxSpace, t1)
	if len(links) != 1 || links[0]["kind"] != "PULL_REQUEST" || links[0]["state"] != "OPEN" || links[0]["number"] != float64(1) ||
		links[0]["repository"] != "openfox-dev/app" || links[0]["matched_in"] != "title" || links[0]["author_user_id"] != w.memberID ||
		links[0]["author_login"] != "octocat" || links[0]["head_branch"] != "lin/work" {
		t.Fatalf("links = %+v", links)
	}
	if name, category := w.taskStatus(w.foxSpace, t1); name != "In Progress" || category != "IN_PROGRESS" {
		t.Fatalf("t1 status = %s %s", name, category)
	}
	if w.activityCount(t1, "task.pr_linked") != 1 || w.activityCount(t1, "task.status_changed") != 1 {
		t.Fatal("expected one pr_linked and one status_changed activity")
	}
	var actor string
	w.scan(&actor, `SELECT actor_id::text FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.pr_linked'`, t1.ID)
	if actor != w.memberID {
		t.Fatalf("pr_linked actor = %s, want the bound member", actor)
	}
	var diff string
	w.scan(&diff, `SELECT safe_diff::text FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.pr_linked'`, t1.ID)
	for _, want := range []string{"openfox-dev/app", `"number": 1`, "/pull/1"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("pr_linked diff = %s, missing %s", diff, want)
		}
	}
	waitFor(t, "backlink comment", func() bool { return len(w.fake.Comments()) == 1 })
	comment := w.fake.Comments()[0]
	wantLine := fmt.Sprintf("%s 已关联到 OpenFox：%s/o/%s/spaces/%s/tasks?task=%s", t1.DisplayID, w.publicURL, w.org, w.foxSpace, t1.DisplayID)
	if comment.Owner != "openfox-dev" || comment.Repo != "app" || comment.Number != 1 || comment.Body != wantLine ||
		strings.Contains(comment.Body, "task one") || strings.Contains(comment.Body, "Implement") {
		t.Fatalf("comment = %+v", comment)
	}
	waitFor(t, "backlink comment id recorded", func() bool {
		var id string
		w.scan(&id, `SELECT backlink_comment_id FROM openfox.task_git_links WHERE task_id = $1::uuid`, t1.ID)
		return id != ""
	})

	// 重复投递：只处理一次。
	if status, body := signed("dup-1", opened); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
		t.Fatalf("duplicate = %d %s", status, body)
	}
	var rows int
	w.scan(&rows, `SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE delivery_id = 'dup-1'`)
	if rows != 1 || len(w.gitLinks(w.owner, w.foxSpace, t1)) != 1 || len(w.fake.Comments()) != 1 || w.activityCount(t1, "task.pr_linked") != 1 {
		t.Fatal("duplicate delivery must not be processed again")
	}

	// 编辑标题新增任务：新增关联，该任务（待办 / 未开始类）移到进行中；PR 已有回链评论，不再评论。
	edited := prSpec{Action: "edited", Number: 1, Title: "Implement " + t1.DisplayID + " and " + t2.DisplayID, Private: true, Branch: "lin/work"}
	if status, _ := signed(nextDelivery(), edited); status != http.StatusOK {
		t.Fatalf("edited = %d", status)
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t2); len(links) != 1 || links[0]["matched_in"] != "title" {
		t.Fatalf("t2 links = %+v", links)
	}
	if name, _ := w.taskStatus(w.foxSpace, t2); name != "In Progress" {
		t.Fatalf("t2 status = %s", name)
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t1); len(links) != 1 || links[0]["title"] != edited.Title {
		t.Fatalf("t1 link after edit = %+v", links)
	}
	if w.activityCount(t1, "task.pr_linked") != 1 || w.activityCount(t1, "task.status_changed") != 1 || len(w.fake.Comments()) != 1 {
		t.Fatal("edit must not repeat activities or comments for an already linked task")
	}
	if status, detail, linked := w.lastDelivery(); status != "PROCESSED" || linked != 2 || detail == "" {
		t.Fatalf("edited delivery = %s %s %d", status, detail, linked)
	}

	// 合并：任务移到第一个已完成状态。
	merged := prSpec{Action: "closed", Number: 1, State: "closed", Merged: true, Title: edited.Title, Private: true}
	if status, _ := signed(nextDelivery(), merged); status != http.StatusOK {
		t.Fatalf("merged = %d", status)
	}
	for _, task := range []taskInfo{t1, t2} {
		if name, category := w.taskStatus(w.foxSpace, task); name != "Done" || category != "DONE" {
			t.Fatalf("%s status after merge = %s %s", task.DisplayID, name, category)
		}
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t1); links[0]["state"] != "MERGED" || links[0]["merged_at"] == nil {
		t.Fatalf("merged link = %+v", links[0])
	}
	if w.activityCount(t1, "task.status_changed") != 2 {
		t.Fatal("merge must record a status change")
	}

	// 已完成的任务再收到 opened：关联建立，状态不变，但要发 task.changed 让任务详情刷新关联列表。
	t3Events := w.changedEvents(t3)
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 2, Title: "Docs for " + t3.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("done task opened = %d", status)
	}
	if got := w.changedEvents(t3) - t3Events; got != 1 {
		t.Fatalf("task.changed events for a link-only change = %d, want 1", got)
	}
	if name, category := w.taskStatus(w.foxSpace, t3); name != "Done" || category != "DONE" || w.activityCount(t3, "task.status_changed") != 0 {
		t.Fatalf("t3 status = %s %s", name, category)
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t3); len(links) != 1 || links[0]["state"] != "OPEN" {
		t.Fatalf("t3 links = %+v", links)
	}
	waitFor(t, "second backlink", func() bool { return len(w.fake.Comments()) == 2 })

	// 关闭但没有合并：状态不变。
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 3, Title: t4.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("t4 opened = %d", status)
	}
	waitFor(t, "third backlink", func() bool { return len(w.fake.Comments()) == 3 })
	if status, _ := signed(nextDelivery(), prSpec{Action: "closed", Number: 3, State: "closed", Title: t4.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("t4 closed = %d", status)
	}
	if name, _ := w.taskStatus(w.foxSpace, t4); name != "In Progress" {
		t.Fatalf("t4 status after unmerged close = %s", name)
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t4); links[0]["state"] != "CLOSED" {
		t.Fatalf("t4 link = %+v", links[0])
	}

	// 仓库所有者不一致：IGNORED。
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 9, Owner: "intruder", Title: t4.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("owner mismatch = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "IGNORED" || detail != "owner mismatch" || len(w.gitLinks(w.owner, w.foxSpace, t4)) != 1 {
		t.Fatalf("owner mismatch delivery = %s %s", status, detail)
	}

	// 关闭「关联 Pull Request」：IGNORED。
	w.putSettings(w.owner, http.StatusOK, w.settingsVersion(), func(m map[string]any) { m["link_pull_requests"] = false })
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 10, Title: t4.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("disabled = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "IGNORED" || detail != "link_pull_requests disabled" || len(w.gitLinks(w.owner, w.foxSpace, t4)) != 1 {
		t.Fatalf("disabled delivery = %s %s", status, detail)
	}
	w.putSettings(w.owner, http.StatusOK, w.settingsVersion(), nil)

	// 公开仓库默认不回链；草稿 PR 关联为 DRAFT 且任务进入进行中。
	t5, t6 := w.task(w.foxSpace, "task five"), w.task(w.foxSpace, "task six")
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 11, Title: t5.DisplayID, Private: false}); status != http.StatusOK {
		t.Fatalf("public = %d", status)
	}
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 12, Title: t6.DisplayID, Private: true, Draft: true}); status != http.StatusOK {
		t.Fatalf("draft = %d", status)
	}
	waitFor(t, "draft backlink", func() bool { return len(w.fake.Comments()) == 4 })
	if links := w.gitLinks(w.owner, w.foxSpace, t6); links[0]["state"] != "DRAFT" {
		t.Fatalf("draft link = %+v", links[0])
	}
	if name, _ := w.taskStatus(w.foxSpace, t5); name != "In Progress" {
		t.Fatalf("t5 status = %s", name)
	}
	if len(w.fake.Comments()) != 4 {
		t.Fatal("public repositories must not get backlink comments by default")
	}

	// 分支名里的编号（分隔符是 /），不存在的编号被忽略。
	t7 := w.task(w.foxSpace, "task seven")
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 13, Title: "no ref", Branch: "lin/" + strings.ToLower(t7.DisplayID) + "-fix-it fox-99999", Private: false}); status != http.StatusOK {
		t.Fatalf("branch = %d", status)
	}
	if links := w.gitLinks(w.owner, w.foxSpace, t7); len(links) != 1 || links[0]["matched_in"] != "branch" {
		t.Fatalf("branch links = %+v", links)
	}

	// 回链遇到 401：连接变为需要重新授权。
	t8 := w.task(w.foxSpace, "task eight")
	w.fake.SetCommentStatus(http.StatusUnauthorized)
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 14, Title: t8.DisplayID, Private: true}); status != http.StatusOK {
		t.Fatalf("revoked backlink = %d", status)
	}
	waitFor(t, "connection needs attention", func() bool { return w.connection().Status == "NEEDS_ATTENTION" })
	if conn := w.connection(); conn.StatusReason != "token_revoked" {
		t.Fatalf("connection = %+v", conn)
	}
	w.fake.SetCommentStatus(http.StatusCreated)
	// 需要重新授权的连接仍然接收 Webhook；重新授权后恢复。
	if status, _ := signed(nextDelivery(), prSpec{Action: "opened", Number: 15, Title: t8.DisplayID, Private: false}); status != http.StatusOK {
		t.Fatalf("needs-attention webhook = %d", status)
	}
	if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "ok" || w.connection().Status != "ACTIVE" {
		t.Fatalf("reauthorize after revoked = %+v", r)
	}

	// 非法内容：签名正确但不是 JSON → FAILED（200，避免 GitHub 反复重试）；缺投递头 → 400；超过 5 MiB → 413。
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "pull_request", nextDelivery(), []byte(`{not json`)); status != http.StatusOK {
		t.Fatalf("invalid json = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "FAILED" || detail != "invalid payload" {
		t.Fatalf("invalid json delivery = %s %s", status, detail)
	}
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "pull_request", "", []byte(`{}`)); status != http.StatusBadRequest {
		t.Fatalf("missing delivery id = %d", status)
	}
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "star", nextDelivery(), []byte(`{"action":"created"}`)); status != http.StatusOK {
		t.Fatalf("other event = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "IGNORED" || detail != "event not handled" {
		t.Fatalf("other event delivery = %s %s", status, detail)
	}
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "push", nextDelivery(), bigBody(5<<20+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d", status)
	}

	// 接通状态与最近投递。
	view := w.webhook(w.owner)
	if view.LastDeliveryAt == nil || len(view.Recent) != 10 || view.Recent[0].Event == "" {
		t.Fatalf("recent deliveries = %+v", view)
	}
	if conn := w.connection(); conn.WebhookLastDeliveryAt == nil {
		t.Fatal("connection must report webhook_last_delivery_at")
	}
}

func (w *world) changedEvents(task taskInfo) (count int) {
	w.scan(&count, `SELECT count(*) FROM openfox.organization_events WHERE type = 'task.changed' AND payload->'task_ids' @> to_jsonb($1::text)`, task.ID)
	return
}

func commitPayload(owner string, commits ...map[string]any) []byte {
	out, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "commits": commits,
		"repository": map[string]any{"full_name": owner + "/app", "private": true, "owner": map[string]any{"login": owner}}})
	return out
}

func runPushCases(t *testing.T, w *world) {
	task := w.task(w.foxSpace, "commit task")
	sha := strings.Repeat("a1b2c3d4", 5)
	payload := commitPayload("openfox-dev",
		map[string]any{"id": sha, "message": "fix " + task.DisplayID + " crash\n\ndetails", "url": "https://github.example.test/openfox-dev/app/commit/" + sha,
			"author": map[string]any{"username": "octocat"}},
		map[string]any{"id": strings.Repeat("b", 40), "message": "no reference", "url": "https://github.example.test/x"})
	// 默认不关联提交。
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "push", nextDelivery(), payload); status != http.StatusOK {
		t.Fatalf("push = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "IGNORED" || detail != "link_commits disabled" || len(w.gitLinks(w.owner, w.foxSpace, task)) != 0 {
		t.Fatalf("disabled push = %s %s", status, detail)
	}
	w.putSettings(w.owner, http.StatusOK, w.settingsVersion(), func(m map[string]any) { m["link_commits"] = true })
	defer w.putSettings(w.owner, http.StatusOK, w.settingsVersion(), nil)
	for i := 0; i < 2; i++ { // 第二次是新的投递，同一提交不重复建关联。
		if status, _ := w.post(w.webhookURL, w.webhookSecret, "push", nextDelivery(), payload); status != http.StatusOK {
			t.Fatalf("push = %d", status)
		}
	}
	if w.changedEvents(task) == 0 {
		t.Fatal("linking a commit must emit task.changed")
	}
	links := w.gitLinks(w.owner, w.foxSpace, task)
	if len(links) != 1 || links[0]["kind"] != "COMMIT" || links[0]["state"] != "COMMITTED" || links[0]["sha"] != sha ||
		links[0]["title"] != "fix "+task.DisplayID+" crash" || links[0]["number"] != nil || links[0]["author_login"] != "octocat" {
		t.Fatalf("commit links = %+v", links)
	}
	if name, category := w.taskStatus(w.foxSpace, task); category != "TODO" {
		t.Fatalf("commits must not change the task status, got %s %s", name, category)
	}
	if status, detail, linked := w.lastDelivery(); status != "PROCESSED" || detail != "linked" || linked != 1 {
		t.Fatalf("push delivery = %s %s %d", status, detail, linked)
	}
	// 同一任务既有 PR 又有提交：PR 在前。
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "pull_request", nextDelivery(),
		prSpec{Action: "opened", Number: 30, Title: task.DisplayID, Private: false}.payload()); status != http.StatusOK {
		t.Fatalf("pr = %d", status)
	}
	links = w.gitLinks(w.owner, w.foxSpace, task)
	if len(links) != 2 || links[0]["kind"] != "PULL_REQUEST" || links[1]["kind"] != "COMMIT" {
		t.Fatalf("ordered links = %+v", links)
	}
	// 仓库所有者不一致。
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "push", nextDelivery(), commitPayload("intruder",
		map[string]any{"id": strings.Repeat("c", 40), "message": task.DisplayID, "url": "https://x"})); status != http.StatusOK {
		t.Fatalf("push intruder = %d", status)
	}
	if status, detail, _ := w.lastDelivery(); status != "IGNORED" || detail != "owner mismatch" {
		t.Fatalf("intruder push = %s %s", status, detail)
	}
	// 最多处理 100 个提交。
	many := make([]map[string]any, 0, 105)
	for i := 0; i < 105; i++ {
		many = append(many, map[string]any{"id": fmt.Sprintf("%040x", i+1), "message": task.DisplayID, "url": "https://x"})
	}
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "push", nextDelivery(), commitPayload("openfox-dev", many...)); status != http.StatusOK {
		t.Fatalf("bulk push = %d", status)
	}
	var commits int
	w.scan(&commits, `SELECT count(*) FROM openfox.task_git_links WHERE task_id = $1::uuid AND kind = 'COMMIT'`, task.ID)
	if commits != 101 { // 原有 1 个 + 本次最多 100 个
		t.Fatalf("commit links = %d, want 101", commits)
	}
}

func runVisibilityCases(t *testing.T, w *world) {
	secret := w.task(w.secSpace, "secret task")
	if status, _ := w.post(w.webhookURL, w.webhookSecret, "pull_request", nextDelivery(),
		prSpec{Action: "opened", Number: 40, Title: "Hidden " + secret.DisplayID, Private: true}.payload()); status != http.StatusOK {
		t.Fatalf("secret pr = %d", status)
	}
	if links := w.gitLinks(w.owner, w.secSpace, secret); len(links) != 1 {
		t.Fatalf("owner links = %+v", links)
	}
	path := w.path("/spaces/" + w.secSpace + "/tasks/" + secret.ID + "/git-links")
	w.member.expect(http.StatusNotFound, "", http.MethodGet, path, nil)
	w.guest.expect(http.StatusNotFound, "", http.MethodGet, path, nil)
	w.owner.expect(http.StatusNotFound, "", http.MethodGet, w.path("/spaces/"+w.secSpace+"/tasks/"+w.newUUID()+"/git-links"), nil)
	// 评论里只有编号与链接，不含标题。
	waitFor(t, "secret backlink", func() bool {
		for _, c := range w.fake.Comments() {
			if c.Number == 40 {
				return !strings.Contains(c.Body, "secret task") && strings.Contains(c.Body, secret.DisplayID)
			}
		}
		return false
	})
	_ = time.Second
}

func runIsolationCases(t *testing.T, w *world) {
	base := "/v1/organizations/" + w.orgB
	var got struct {
		Connections []connView `json:"connections"`
	}
	w.ownerB.json(http.StatusOK, http.MethodGet, base+"/integrations/github", nil, &got)
	for _, item := range got.Connections {
		if item.ID == w.connID {
			t.Fatal("organization B must not see organization A's connection")
		}
	}
	w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/integrations/connections/"+w.connID+"/webhook", nil)
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/connections/"+w.connID+"/webhook/rotate", map[string]any{"version": 1})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/connections/"+w.connID+"/disconnect",
		map[string]any{"version": 1, "confirm_phrase": "断开 GitHub"})
	var sessionID string
	w.scan(&sessionID, `SELECT id::text FROM openfox.integration_oauth_states WHERE organization_id = $1::uuid LIMIT 1`, w.org)
	w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/integrations/oauth/"+sessionID, nil)
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/oauth/"+sessionID+"/complete", map[string]any{"accounts": []string{"openfox-dev"}})
	var settings struct {
		Version uint64 `json:"version"`
	}
	w.ownerB.json(http.StatusOK, http.MethodGet, base+"/integrations/github/settings", nil, &settings)
	if settings.Version != 0 {
		t.Fatalf("organization B settings version = %d", settings.Version)
	}
	// 组织 A 的任务在组织 B 下不可见。
	w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/spaces/"+w.foxSpace+"/tasks/"+w.newUUID()+"/git-links", nil)
}
