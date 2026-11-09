package gitlabint

// GitLab 集成测试（自部署 / 极狐共用实现）：实例地址 SSRF 防护、OAuth（PKCE、令牌续期）、Webhook（X-Gitlab-Token、去重、重放）、
// MR 与 Commit 关联任务、MR 状态自动改任务、Issue 入站 / 出站同步、导入已有 Issue、个人绑定、跨组织隔离。
// 真实 HTTP 处理器 + 真实数据库 + 本机模拟 GitLab，绝不访问真实的 GitLab / 极狐。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations"
	"openfox/internal/tenancy"
	fakegitlab "openfox/tests/support/fakegitlab/fake"
)

const (
	appID     = "app-id"
	appSecret = "app-secret"
	appRepo   = "openfox-dev/app"
	subRepo   = "openfox-dev/sub/site"
)

type oauthResult struct{ SessionID, Status, Reason string }

type connView struct {
	ID           string  `json:"id"`
	ExternalID   string  `json:"external_id"`
	DisplayName  string  `json:"display_name"`
	Status       string  `json:"status"`
	StatusReason string  `json:"status_reason"`
	Version      uint64  `json:"version"`
	Parent       *string `json:"parent_connection_id"`
	InstanceURL  string  `json:"instance_url"`
	OwnerUserID  *string `json:"owner_user_id"`
}

type sessionView struct {
	Status   string `json:"status"`
	Accounts []struct {
		Login string `json:"login"`
		Kind  string `json:"kind"`
	} `json:"accounts"`
}

type webhookView struct {
	PayloadURL string   `json:"payload_url"`
	Secret     string   `json:"secret"`
	Events     []string `json:"events"`
	Version    uint64   `json:"version"`
}

func (w *world) startBody(purpose, connectionID string) map[string]any {
	body := map[string]any{"purpose": purpose}
	if connectionID != "" {
		body["connection_id"] = connectionID
	}
	if purpose == "organization" {
		body["instance_url"] = w.fake.URL
		body["application_id"] = appID
		body["application_secret"] = appSecret
	}
	return body
}

// authorize 发起授权并走完 GitLab 授权页，返回回调地址（尚未访问）。
func (w *world) authorize(c *client, purpose, connectionID string) (string, string) {
	w.t.Helper()
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		SessionID    string `json:"session_id"`
	}
	c.json(http.StatusOK, http.MethodPost, w.path("/integrations/gitlab/authorize"), w.startBody(purpose, connectionID), &start)
	parsed, _ := neturl.Parse(start.AuthorizeURL)
	q := parsed.Query()
	if !strings.HasPrefix(start.AuthorizeURL, w.fake.URL+"/oauth/authorize?") || q.Get("scope") != "api" || q.Get("code_challenge_method") != "S256" ||
		q.Get("client_id") != appID || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != w.publicURL+"/api/v1/integrations/gitlab/callback" || strings.Contains(start.AuthorizeURL, appSecret) {
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
	return start.SessionID, response.Header.Get("Location")
}

func (w *world) callback(c *client, location string) oauthResult {
	w.t.Helper()
	response, err := c.noRedirect().Get(location)
	if err != nil {
		w.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound {
		w.t.Fatalf("callback = %d", response.StatusCode)
	}
	target, err := neturl.Parse(response.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(target.String(), w.publicURL+"/integrations/oauth/complete?") {
		w.t.Fatalf("callback redirect = %s", response.Header.Get("Location"))
	}
	q := target.Query()
	if q.Get("provider") != "gitlab" {
		w.t.Fatalf("provider = %q", q.Get("provider"))
	}
	return oauthResult{SessionID: q.Get("session"), Status: q.Get("status"), Reason: q.Get("reason")}
}

func (w *world) runOAuth(c *client, purpose, connectionID string) oauthResult {
	w.t.Helper()
	_, location := w.authorize(c, purpose, connectionID)
	return w.callback(c, location)
}

func (w *world) detail(c *client) (orgConns, personal []connView) {
	var got struct {
		Connections         []connView `json:"connections"`
		PersonalConnections []connView `json:"personal_connections"`
	}
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/gitlab"), nil, &got)
	return got.Connections, got.PersonalConnections
}

// bind 走完组织授权并勾选 openfox-dev，返回组织连接。
func (w *world) bind() connView {
	w.t.Helper()
	r := w.runOAuth(w.owner, "organization", "")
	if r.Status != "ok" {
		w.t.Fatalf("org oauth = %+v", r)
	}
	var session sessionView
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+r.SessionID), nil, &session)
	if session.Status != "SELECTING" {
		w.t.Fatalf("session = %+v", session)
	}
	var done struct {
		Connections []connView `json:"connections"`
	}
	w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/oauth/"+r.SessionID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}}, &done)
	if len(done.Connections) != 1 {
		w.t.Fatalf("connections = %+v", done.Connections)
	}
	return done.Connections[0]
}

func (w *world) webhook(c *client) webhookView {
	var view webhookView
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/connections/"+w.connID+"/webhook"), nil, &view)
	return view
}

// post 发送 Webhook（无登录会话、无 CSRF）。key 为空表示不带 Idempotency-Key。
func (w *world) post(targetURL, token, event, key string, payload []byte) (int, string) {
	w.t.Helper()
	request := fakegitlab.SignedWebhook(targetURL, token, event, key, payload)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

var deliverySeq int

func nextKey() string {
	deliverySeq++
	return fmt.Sprintf("gl-delivery-%d", deliverySeq)
}

func (w *world) deliver(event string, payload []byte) (status, detail string, linked int) {
	w.t.Helper()
	code, body := w.post(w.webhookURL, w.webhookSecret, event, nextKey(), payload)
	if code != http.StatusOK {
		w.t.Fatalf("webhook %s = %d %s", event, code, body)
	}
	return w.lastDelivery()
}

func (w *world) lastDelivery() (status, detail string, linked int) {
	w.scan(&status, `SELECT status FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	w.scan(&detail, `SELECT detail FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	w.scan(&linked, `SELECT linked_count FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	return
}

func (w *world) deliveryCount() (count int) {
	w.scan(&count, `SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid`, w.connID)
	return
}

func projectJSON(project string, private bool) map[string]any {
	level := 20
	if private {
		level = 0
	}
	return map[string]any{"path_with_namespace": project, "visibility_level": level}
}

type mrSpec struct {
	Action, State, Title, Body, Branch, Project, OldRev string
	IID                                                 int
	AuthorID                                            int64
	AuthorLogin                                         string
	Public                                              bool
}

func (m mrSpec) payload() []byte {
	if m.Project == "" {
		m.Project = appRepo
	}
	if m.State == "" {
		m.State = "opened"
	}
	if m.AuthorLogin == "" {
		m.AuthorLogin = "octocat"
	}
	if m.AuthorID == 0 {
		m.AuthorID = 777
	}
	attrs := map[string]any{"id": 6000 + m.IID, "iid": m.IID, "title": m.Title, "description": m.Body, "state": m.State, "action": m.Action,
		"source_branch": m.Branch, "author_id": m.AuthorID, "updated_at": "2026-11-02 10:00:00 UTC",
		"url": fmt.Sprintf("https://gitlab.example.test/%s/-/merge_requests/%d", m.Project, m.IID)}
	if m.OldRev != "" {
		attrs["oldrev"] = m.OldRev
	}
	out, _ := json.Marshal(map[string]any{"object_kind": "merge_request", "event_type": "merge_request",
		"user": map[string]any{"id": m.AuthorID, "username": m.AuthorLogin}, "project": projectJSON(m.Project, !m.Public), "object_attributes": attrs})
	return out
}

func pushPayload(project, ref, user string, commits ...map[string]string) []byte {
	list := []map[string]any{}
	for _, c := range commits {
		list = append(list, map[string]any{"id": c["id"], "message": c["message"], "url": "https://gitlab.example.test/" + project + "/-/commit/" + c["id"]})
	}
	out, _ := json.Marshal(map[string]any{"object_kind": "push", "ref": ref, "user_username": user, "project": projectJSON(project, true), "commits": list})
	return out
}

type glIssueEvent struct {
	Project, Action, State, Title, Body string
	IID                                 int
	AuthorID                            int64
	Assignees                           []map[string]any
	Changes                             map[string]any
}

func (e glIssueEvent) payload() []byte {
	if e.Project == "" {
		e.Project = appRepo
	}
	if e.State == "" {
		e.State = "opened"
	}
	if e.AuthorID == 0 {
		e.AuthorID = 4242
	}
	attrs := map[string]any{"id": 8000 + e.IID, "iid": e.IID, "title": e.Title, "description": e.Body, "state": e.State, "action": e.Action,
		"author_id": e.AuthorID, "type": "Issue", "created_at": "2026-11-02 09:00:00 UTC", "updated_at": "2026-11-02 10:00:00 UTC",
		"url": fmt.Sprintf("https://gitlab.example.test/%s/-/issues/%d", e.Project, e.IID)}
	body := map[string]any{"object_kind": "issue", "user": map[string]any{"id": e.AuthorID, "username": "lin"},
		"project": projectJSON(e.Project, true), "object_attributes": attrs, "assignees": e.Assignees}
	if e.Changes != nil {
		body["changes"] = e.Changes
	}
	out, _ := json.Marshal(body)
	return out
}

func notePayload(project string, iid int, noteID int64, text string) []byte {
	out, _ := json.Marshal(map[string]any{"object_kind": "note", "user": map[string]any{"id": 777, "username": "octocat"},
		"project":           projectJSON(project, true),
		"object_attributes": map[string]any{"id": noteID, "note": text, "noteable_type": "Issue", "author_id": 777, "created_at": "2026-11-02 10:30:00 UTC"},
		"issue":             map[string]any{"id": 8000 + iid, "iid": iid, "title": "x", "state": "opened", "created_at": "2026-11-02 09:00:00 UTC", "updated_at": "2026-11-02 10:00:00 UTC"}})
	return out
}

func (w *world) orgExec(query string, args ...any) {
	w.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: w.org, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
	err := w.pool.RunForOrganization(w.ctx, w.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		})
	if err != nil {
		w.t.Fatalf("orgExec %s: %v", query, err)
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (w *world) service() integrations.Service {
	return integrations.Service{Box: w.box, GitFactory: w.factory, PublicURL: w.publicURL}
}

func (w *world) setDue(taskID string) {
	w.exec(`UPDATE openfox.integration_sync_outbox SET next_attempt_at = now() - interval '1 second' WHERE task_id = $1::uuid AND status = 'PENDING'`, taskID)
}

func (w *world) settingsBody(mutate func(map[string]any)) map[string]any {
	m := map[string]any{"link_pull_requests": true, "link_commits": false, "pr_backlink_private": true, "pr_backlink_public": false,
		"issue_backlink_private": true, "issue_backlink_public": false, "branch_format": "username/identifier-title"}
	if mutate != nil {
		mutate(m)
	}
	return m
}

func TestGitLab(t *testing.T) {
	w := setup(t)
	w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-group")
	w.fake.AddRepo(appRepo, true)
	w.fake.AddRepo(subRepo, false)
	w.fake.AddRepo("openfox-dev/old", false)
	w.fake.AddRepo("other-group/x", false)
	w.fake.AddUser(777, "octocat")

	t.Run("catalog", func(t *testing.T) {
		var overview struct {
			Providers []struct {
				ID                 string `json:"id"`
				Availability       string `json:"availability"`
				Configured         bool   `json:"configured"`
				Bindable           bool   `json:"bindable"`
				OAuthRedirectURI   string `json:"oauth_redirect_uri"`
				DefaultInstanceURL string `json:"default_instance_url"`
			} `json:"providers"`
		}
		w.member.json(http.StatusOK, http.MethodGet, w.path("/integrations"), nil, &overview)
		seen := map[string]bool{}
		for _, p := range overview.Providers {
			switch p.ID {
			case "gitlab":
				seen["gitlab"] = true
				if p.Availability != "available" || !p.Configured || !p.Bindable || p.OAuthRedirectURI != w.publicURL+"/api/v1/integrations/gitlab/callback" || p.DefaultInstanceURL != "" {
					t.Fatalf("gitlab = %+v", p)
				}
			case "gitlab_cn":
				seen["gitlab_cn"] = true
				if p.Availability != "available" || p.OAuthRedirectURI != w.publicURL+"/api/v1/integrations/gitlab_cn/callback" || p.DefaultInstanceURL != "https://jihulab.com" {
					t.Fatalf("gitlab_cn = %+v", p)
				}
			case "github":
				if p.OAuthRedirectURI != w.publicURL+"/api/v1/integrations/github/callback" {
					t.Fatalf("github redirect = %q", p.OAuthRedirectURI)
				}
			}
		}
		if !seen["gitlab"] || !seen["gitlab_cn"] {
			t.Fatalf("providers = %+v", overview.Providers)
		}
	})

	t.Run("authorize guards", func(t *testing.T) {
		path := w.path("/integrations/gitlab/authorize")
		w.member.expect(http.StatusForbidden, "", http.MethodPost, path, w.startBody("organization", ""))
		w.guest.expect(http.StatusForbidden, "", http.MethodPost, path, w.startBody("personal", w.newUUID()))
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "organization"})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "organization", "instance_url": w.fake.URL, "application_id": appID})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "bogus"})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "organization", "instance_url": "ftp://x", "application_id": appID, "application_secret": appSecret})
		// 个人绑定必须指明是哪个实例的组织连接。
		w.member.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "personal"})
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, path, map[string]any{"purpose": "reauthorize", "connection_id": w.newUUID()})
		// 未配置 / 无主密钥。
		bare := w.login(w.bare.URL+"/api", fmt.Sprintf("gl-owner-%s@example.test", w.org[:8]), w.ownerID)
		bare.expect(http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", http.MethodPost, path, w.startBody("organization", ""))
		nobox := w.login(w.nobox.URL+"/api", fmt.Sprintf("gl-owner-%s@example.test", w.org[:8]), w.ownerID)
		nobox.expect(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPost, path, w.startBody("organization", ""))
	})

	t.Run("instance url ssrf", func(t *testing.T) {
		strict := w.login(w.strict.URL+"/api", fmt.Sprintf("gl-owner-%s@example.test", w.org[:8]), w.ownerID)
		path := w.path("/integrations/gitlab/authorize")
		for _, target := range []string{w.fake.URL, "http://gitlab.example.test", "https://127.0.0.1", "https://localhost", "https://10.0.0.5", "https://169.254.169.254",
			"https://[::1]", "https://user:pass@gitlab.example.test", "https://gitlab.example.test/?x=1"} {
			body := w.startBody("organization", "")
			body["instance_url"] = target
			strict.expect(http.StatusBadRequest, "INVALID_INSTANCE_URL", http.MethodPost, path, body)
		}
		var count int
		w.scan(&count, `SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id = $1::uuid AND provider = 'gitlab'`, w.org)
		if count != 0 {
			t.Fatalf("rejected instance urls must not create sessions: %d", count)
		}
	})

	t.Run("organization oauth", func(t *testing.T) {
		before := len(w.fake.Challenges())
		r := w.runOAuth(w.owner, "organization", "")
		if r.Status != "ok" || len(w.fake.Challenges()) != before+1 {
			t.Fatalf("oauth = %+v", r)
		}
		var session sessionView
		w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+r.SessionID), nil, &session)
		logins := map[string]string{}
		for _, a := range session.Accounts {
			logins[a.Login] = a.Kind
		}
		if session.Status != "SELECTING" || logins["openfox-dev"] != "organization" || logins["other-group"] != "organization" || logins["lin"] != "user" || len(logins) != 3 {
			t.Fatalf("session = %+v", session)
		}
		w.member.expect(http.StatusNotFound, "", http.MethodGet, w.path("/integrations/oauth/"+r.SessionID), nil)
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, w.path("/integrations/oauth/"+r.SessionID+"/complete"), map[string]any{"accounts": []string{"nobody"}})
		var done struct {
			Connections []connView `json:"connections"`
		}
		w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/oauth/"+r.SessionID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}}, &done)
		conn := done.Connections[0]
		w.connID = conn.ID
		if conn.ExternalID != "openfox-dev" || conn.Status != "ACTIVE" || conn.InstanceURL != w.fake.URL {
			t.Fatalf("connection = %+v", conn)
		}
		// 实例地址与应用 ID 保存在连接 settings，Secret 与令牌只在加密凭据里。
		var instance, application string
		var expires *time.Time
		var sealed []byte
		w.scan(&instance, `SELECT settings->>'instance_url' FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		w.scan(&application, `SELECT settings->>'application_id' FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		w.scan(&expires, `SELECT token_expires_at FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		w.scan(&sealed, `SELECT credentials_sealed FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		if instance != w.fake.URL || application != appID || expires == nil || time.Until(*expires) < time.Hour || time.Until(*expires) > 3*time.Hour {
			t.Fatalf("settings = %q %q %v", instance, application, expires)
		}
		if strings.Contains(string(sealed), "glpat") || strings.Contains(string(sealed), appSecret) {
			t.Fatal("credentials must be encrypted")
		}
		// 授权状态不能被第二次使用。
		w.owner.expect(http.StatusConflict, "OAUTH_SESSION_STATE", http.MethodPost, w.path("/integrations/oauth/"+r.SessionID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}})
		view := w.webhook(w.owner)
		w.webhookURL, w.webhookSecret = view.PayloadURL, view.Secret
	})

	t.Run("denied and failed exchange", func(t *testing.T) {
		w.fake.DenyAuthorize(true)
		if r := w.runOAuth(w.owner, "organization", ""); r.Status != "error" || r.Reason != "access_denied" {
			t.Fatalf("denied = %+v", r)
		}
		w.fake.DenyAuthorize(false)
		w.fake.FailExchange(true)
		if r := w.runOAuth(w.owner, "organization", ""); r.Status != "error" || r.Reason != "exchange_failed" {
			t.Fatalf("exchange = %+v", r)
		}
		w.fake.FailExchange(false)
		// 另一个用户拿不到别人的授权会话；GitHub 回调路径不认 GitLab 的 state。
		_, location := w.authorize(w.owner, "organization", "")
		if r := w.callback(w.admin, location); r.Reason != "invalid_state" {
			t.Fatalf("other user = %+v", r)
		}
		parsed, _ := neturl.Parse(location)
		cross := w.publicURL + "/api/v1/integrations/github/callback?" + parsed.RawQuery
		response, err := w.owner.noRedirect().Get(cross)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if !strings.Contains(response.Header.Get("Location"), "reason=invalid_state") {
			t.Fatalf("cross provider callback = %s", response.Header.Get("Location"))
		}
	})

	t.Run("webhook config", func(t *testing.T) {
		view := w.webhook(w.owner)
		if !strings.HasPrefix(view.PayloadURL, w.publicURL+"/api/v1/hooks/git/gitlab/") || len(view.Secret) != 64 || len(view.Events) != 4 {
			t.Fatalf("webhook = %+v", view)
		}
		w.member.expect(http.StatusForbidden, "", http.MethodGet, w.path("/integrations/connections/"+w.connID+"/webhook"), nil)
	})

	t.Run("webhook signature and replay", func(t *testing.T) {
		task := w.task(w.foxSpace, "Webhook guard")
		payload := mrSpec{Action: "open", IID: 1, Title: task.DisplayID + " guard", Branch: "x"}.payload()
		before := w.deliveryCount()
		if status, _ := w.post(w.webhookURL, "wrong-token", "Merge Request Hook", nextKey(), payload); status != http.StatusUnauthorized {
			t.Fatalf("wrong token = %d", status)
		}
		if status, _ := w.post(w.webhookURL, "", "Merge Request Hook", nextKey(), payload); status != http.StatusUnauthorized {
			t.Fatalf("missing token = %d", status)
		}
		// 令牌前缀相同但更短 / 更长的密钥一律不通过（常量时间比较，不存在前缀匹配）。
		for _, token := range []string{w.webhookSecret[:len(w.webhookSecret)-1], w.webhookSecret + "0"} {
			if status, _ := w.post(w.webhookURL, token, "Merge Request Hook", nextKey(), payload); status != http.StatusUnauthorized {
				t.Fatalf("near token = %d", status)
			}
		}
		if w.deliveryCount() != before {
			t.Fatal("rejected deliveries must not be recorded")
		}
		// 令牌属于 GitLab 连接，在 GitHub 路径上不可用。
		githubURL := strings.Replace(w.webhookURL, "/hooks/git/gitlab/", "/hooks/git/github/", 1)
		if status, _ := w.post(githubURL, w.webhookSecret, "Merge Request Hook", nextKey(), payload); status != http.StatusNotFound {
			t.Fatalf("cross provider path = %d", status)
		}
		if status, _ := w.post(strings.TrimSuffix(w.webhookURL, w.webhookURL[strings.LastIndex(w.webhookURL, "/")+1:])+"unknowntoken1234567890", w.webhookSecret,
			"Merge Request Hook", nextKey(), payload); status != http.StatusNotFound {
			t.Fatalf("unknown token = %d", status)
		}
		key := nextKey()
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", key, payload); status != http.StatusOK || !strings.Contains(body, `"duplicate":false`) {
			t.Fatalf("first = %d %s", status, body)
		}
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", key, payload); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
			t.Fatalf("replay = %d %s", status, body)
		}
		// 没有 Idempotency-Key 时按「事件 + 正文摘要」去重。
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", "", []byte(`{"object_kind":"x"}`)); status != http.StatusOK || !strings.Contains(body, `"duplicate":false`) {
			t.Fatalf("no key first = %d %s", status, body)
		}
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", "", []byte(`{"object_kind":"x"}`)); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
			t.Fatalf("no key replay = %d %s", status, body)
		}
		if status, _ := w.post(w.webhookURL, w.webhookSecret, "", "", []byte(`{}`)); status != http.StatusBadRequest {
			t.Fatalf("missing event = %d", status)
		}
		var linkCount int
		w.scan(&linkCount, `SELECT count(*) FROM openfox.task_git_links WHERE task_id = $1::uuid`, task.ID)
		if linkCount != 1 || w.deliveryCount() != before+2 {
			t.Fatalf("links = %d deliveries = %d (before %d)", linkCount, w.deliveryCount(), before)
		}
	})

	t.Run("merge request links and status", func(t *testing.T) {
		task := w.task(w.foxSpace, "MR flow")
		other := w.task(w.foxSpace, "MR other")
		if name, _ := w.taskStatus(w.foxSpace, task); name == "In Progress" || name == "Done" {
			t.Fatalf("unexpected initial status %q", name)
		}
		spec := mrSpec{Action: "open", IID: 11, Title: task.DisplayID + " implement it", Body: "also " + other.DisplayID, Branch: "feature/x"}
		if status, detail, linked := w.deliver("Merge Request Hook", spec.payload()); status != "PROCESSED" || detail != "linked" || linked != 2 {
			t.Fatalf("open = %s %q %d", status, detail, linked)
		}
		if name, _ := w.taskStatus(w.foxSpace, task); name != "In Progress" {
			t.Fatalf("after open: %q", name)
		}
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["kind"] != "PULL_REQUEST" || links[0]["provider"] != "gitlab" || links[0]["state"] != "OPEN" ||
			links[0]["repository"] != appRepo || int(links[0]["number"].(float64)) != 11 || links[0]["matched_in"] != "title" {
			t.Fatalf("links = %v", links)
		}
		// 草稿（draft）仍算进行中；编辑标题不重复关联。
		spec.Action, spec.OldRev = "update", ""
		spec.Title = task.DisplayID + " implement it better"
		w.deliver("Merge Request Hook", spec.payload())
		if links = w.gitLinks(w.owner, w.foxSpace, task); len(links) != 1 || links[0]["title"] != spec.Title {
			t.Fatalf("after update = %v", links)
		}
		// 关闭未合并：状态不动，关联变 CLOSED。
		spec.Action, spec.State = "close", "closed"
		w.deliver("Merge Request Hook", spec.payload())
		if name, _ := w.taskStatus(w.foxSpace, task); name != "In Progress" {
			t.Fatalf("after close: %q", name)
		}
		if links = w.gitLinks(w.owner, w.foxSpace, task); links[0]["state"] != "CLOSED" {
			t.Fatalf("closed = %v", links)
		}
		// 合并：第一个「已完成」状态。
		spec.Action, spec.State = "merge", "merged"
		w.deliver("Merge Request Hook", spec.payload())
		if name, category := w.taskStatus(w.foxSpace, task); name != "Done" || category != "DONE" {
			t.Fatalf("after merge: %q %q", name, category)
		}
		if links = w.gitLinks(w.owner, w.foxSpace, task); links[0]["state"] != "MERGED" || links[0]["merged_at"] == nil {
			t.Fatalf("merged = %v", links)
		}
		if name, _ := w.taskStatus(w.foxSpace, other); name != "Done" {
			t.Fatalf("second task after merge: %q", name)
		}
		// 仓库所有者（项目路径第一段）不是连接账号：忽略。
		foreign := mrSpec{Action: "open", IID: 12, Title: task.DisplayID, Project: "other-group/x"}
		if status, detail, _ := w.deliver("Merge Request Hook", foreign.payload()); status != "IGNORED" || detail != "owner mismatch" {
			t.Fatalf("foreign = %s %q", status, detail)
		}
		// 没有任务编号。
		if status, detail, _ := w.deliver("Merge Request Hook", mrSpec{Action: "open", IID: 13, Title: "no ref"}.payload()); status != "PROCESSED" || detail != "no task reference" {
			t.Fatalf("no ref = %s %q", status, detail)
		}
		// 审批类动作忽略。
		if status, detail, _ := w.deliver("Merge Request Hook", mrSpec{Action: "approval", IID: 11, Title: task.DisplayID}.payload()); status != "IGNORED" || detail != "action not handled" {
			t.Fatalf("approval = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Pipeline Hook", []byte(`{"object_kind":"pipeline","project":{"path_with_namespace":"openfox-dev/app"}}`)); status != "IGNORED" {
			t.Fatalf("pipeline = %s %q", status, detail)
		}
	})

	t.Run("merge request backlink", func(t *testing.T) {
		task := w.task(w.foxSpace, "Backlink flow")
		w.deliver("Merge Request Hook", mrSpec{Action: "open", IID: 21, Title: task.DisplayID + " backlink", Branch: "b"}.payload())
		waitFor(t, "backlink note", func() bool {
			for _, note := range w.fake.Notes() {
				if note.Kind == "merge_requests" && note.IID == 21 && strings.Contains(note.Body, task.DisplayID) {
					return true
				}
			}
			return false
		})
		var comment string
		w.scan(&comment, `SELECT backlink_comment_id FROM openfox.task_git_links WHERE task_id = $1::uuid`, task.ID)
		if comment == "" {
			t.Fatal("backlink comment id must be recorded")
		}
	})

	t.Run("commit links", func(t *testing.T) {
		task := w.task(w.foxSpace, "Commit flow")
		sha := "0123456789abcdef0123456789abcdef01234567"
		payload := pushPayload(appRepo, "refs/heads/main", "octocat", map[string]string{"id": sha, "message": task.DisplayID + " fix"})
		if status, detail, _ := w.deliver("Push Hook", payload); status != "IGNORED" || detail != "link_commits disabled" {
			t.Fatalf("disabled = %s %q", status, detail)
		}
		w.owner.expect(http.StatusOK, "", http.MethodPut, w.path("/integrations/gitlab/settings"),
			map[string]any{"version": 0, "settings": w.settingsBody(func(m map[string]any) { m["link_commits"] = true })})
		if status, detail, linked := w.deliver("Push Hook", payload); status != "PROCESSED" || detail != "linked" || linked != 1 {
			t.Fatalf("enabled = %s %q %d", status, detail, linked)
		}
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["kind"] != "COMMIT" || links[0]["provider"] != "gitlab" || links[0]["sha"] != sha {
			t.Fatalf("links = %v", links)
		}
	})

	t.Run("personal binding", func(t *testing.T) {
		w.member.expect(http.StatusBadRequest, "", http.MethodPost, w.path("/integrations/gitlab/authorize"), map[string]any{"purpose": "personal"})
		w.fake.SetIdentity("octocat", 777, "openfox-dev")
		r := w.runOAuth(w.member, "personal", w.connID)
		if r.Status != "ok" {
			t.Fatalf("personal = %+v", r)
		}
		w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-group")
		_, personal := w.detail(w.member)
		if len(personal) != 1 || personal[0].ExternalID != "777" || personal[0].DisplayName != "octocat" || personal[0].Parent == nil || *personal[0].Parent != w.connID || personal[0].InstanceURL != w.fake.URL {
			t.Fatalf("personal = %+v", personal)
		}
		// 再绑定一次是替换，不会多出一条。
		w.fake.SetIdentity("octocat", 777, "openfox-dev")
		w.runOAuth(w.member, "personal", w.connID)
		w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-group")
		if _, again := w.detail(w.member); len(again) != 1 || again[0].ID != personal[0].ID {
			t.Fatalf("again = %+v", again)
		}
		// MR 作者认人：octocat 的 MR 把关联人记成成员。
		task := w.task(w.foxSpace, "Author flow")
		w.deliver("Merge Request Hook", mrSpec{Action: "open", IID: 31, Title: task.DisplayID, AuthorID: 777, AuthorLogin: "octocat"}.payload())
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["author_user_id"] != w.memberID {
			t.Fatalf("author = %v", links)
		}
		// 别的成员看不到这个个人连接。
		if _, other := w.detail(w.admin); len(other) != 0 {
			t.Fatalf("admin personal = %+v", other)
		}
	})

	runIssueSync(t, w)
	runTokenLifecycle(t, w)
	runIsolation(t, w)

	t.Run("disconnect", func(t *testing.T) {
		conns, _ := w.detail(w.owner)
		var version uint64
		for _, c := range conns {
			if c.ID == w.connID {
				version = c.Version
			}
		}
		w.owner.expect(http.StatusBadRequest, "CONFIRMATION_MISMATCH", http.MethodPost, w.path("/integrations/connections/"+w.connID+"/disconnect"),
			map[string]any{"version": version, "confirm_phrase": "断开 GitHub"})
		w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+w.connID+"/disconnect"),
			map[string]any{"version": version, "confirm_phrase": "断开 GitLab 自部署版"}, nil)
		if status, _ := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", nextKey(), mrSpec{Action: "open", IID: 99, Title: "x"}.payload()); status != http.StatusNotFound {
			t.Fatalf("disconnected webhook = %d", status)
		}
	})

	t.Run("no secrets in responses", func(t *testing.T) {
		w.log.mu.Lock()
		defer w.log.mu.Unlock()
		for _, body := range w.log.bodies {
			if strings.Contains(body, appSecret) || strings.Contains(body, "glpat-fake") || strings.Contains(body, "glrt-fake") {
				t.Fatalf("response leaks a secret: %.200s", body)
			}
		}
	})
}
