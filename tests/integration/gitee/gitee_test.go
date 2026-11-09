package giteeint

// Gitee 集成测试：OAuth（无 PKCE、令牌续期）、Webhook（密码 / 签名密钥两种模式、时间戳窗口、去重、重放）、
// PR 与 Commit 关联任务、PR 状态自动改任务、Issue 入站 / 出站同步（字母数字串编号）、导入已有 Issue、个人绑定、跨组织隔离。
// 真实 HTTP 处理器 + 真实数据库 + 本机模拟 Gitee，绝不访问真实的 Gitee / GitHub / GitLab。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations"
	"openfox/internal/tenancy"
	fakegitee "openfox/tests/support/fakegitee/fake"
)

const (
	appID     = "gitee-id"
	appSecret = "gitee-secret"
	appRepo   = "openfox-dev/app"
	siteRepo  = "openfox-dev/site"
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
	return body
}

// authorize 发起授权并走完 Gitee 授权页，返回回调地址（尚未访问）。
func (w *world) authorize(c *client, purpose, connectionID string) (string, string) {
	w.t.Helper()
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		SessionID    string `json:"session_id"`
	}
	c.json(http.StatusOK, http.MethodPost, w.path("/integrations/gitee/authorize"), w.startBody(purpose, connectionID), &start)
	parsed, _ := neturl.Parse(start.AuthorizeURL)
	q := parsed.Query()
	// Gitee 没有 PKCE：授权地址不带 code_challenge；权限范围是申请的六项；Secret 不出现在授权地址里。
	if !strings.HasPrefix(start.AuthorizeURL, w.fake.URL+"/oauth/authorize?") || q.Get("scope") != "user_info projects pull_requests issues notes groups" ||
		q.Has("code_challenge") || q.Has("code_challenge_method") || q.Get("client_id") != appID || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != w.publicURL+"/api/v1/integrations/gitee/callback" || strings.Contains(start.AuthorizeURL, appSecret) {
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
	if q.Get("provider") != "gitee" {
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
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/gitee"), nil, &got)
	return got.Connections, got.PersonalConnections
}

// bind 走完组织授权并勾选 openfox-dev，返回组织连接。
func (w *world) bind() connView {
	w.t.Helper()
	r := w.runOAuth(w.owner, "organization", "")
	if r.Status != "ok" {
		w.t.Fatalf("org oauth = %+v", r)
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

// postWith 发送 Webhook（无登录会话、无 CSRF）。
func (w *world) postWith(request *http.Request) (int, string) {
	w.t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// post 用「密码」模式发送。
func (w *world) post(targetURL, password, event string, payload []byte) (int, string) {
	w.t.Helper()
	return w.postWith(fakegitee.PasswordWebhook(targetURL, password, event, time.Now(), payload))
}

// postSigned 用「签名密钥」模式发送。
func (w *world) postSigned(targetURL, secret, event string, at time.Time, urlEncode bool, payload []byte) (int, string) {
	w.t.Helper()
	return w.postWith(fakegitee.SignedWebhook(targetURL, secret, event, at, urlEncode, payload))
}

var deliverySeq int

// unique 给载荷加一个唯一的 timestamp 字段（Gitee 的正文里带毫秒时间戳；去重靠「事件 + 正文摘要」，所以每次投递必须不同）。
func unique(payload []byte) []byte {
	deliverySeq++
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		panic(err)
	}
	body["timestamp"] = strconv.Itoa(1767000000000 + deliverySeq)
	out, _ := json.Marshal(body)
	return out
}

func (w *world) deliver(event string, payload []byte) (status, detail string, linked int) {
	w.t.Helper()
	code, body := w.post(w.webhookURL, w.webhookSecret, event, unique(payload))
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

func repoJSON(repo string, private bool) map[string]any {
	owner, name, _ := strings.Cut(repo, "/")
	// full_name 在 Gitee 载荷里是中文显示名，适配器必须用 path_with_namespace。
	return map[string]any{"path_with_namespace": repo, "path": name, "namespace": owner, "full_name": "显示名/" + name, "private": private,
		"owner": map[string]any{"id": 1, "login": owner}}
}

func userJSON(id int64, login string) map[string]any {
	return map[string]any{"id": id, "login": login, "username": login, "name": login}
}

type prSpec struct {
	Action, ActionDesc, State, Title, Body, Branch, Repo string
	Number                                               int
	AuthorID                                             int64
	AuthorLogin                                          string
	Public                                               bool
}

func (p prSpec) payload() []byte {
	if p.Repo == "" {
		p.Repo = appRepo
	}
	if p.State == "" {
		p.State = "open"
	}
	if p.AuthorLogin == "" {
		p.AuthorLogin = "octocat"
	}
	if p.AuthorID == 0 {
		p.AuthorID = 777
	}
	pr := map[string]any{"id": 6000 + p.Number, "number": p.Number, "state": p.State, "title": p.Title, "body": p.Body,
		"html_url": fmt.Sprintf("https://gitee.example.test/%s/pulls/%d", p.Repo, p.Number), "user": userJSON(p.AuthorID, p.AuthorLogin),
		"head": map[string]any{"ref": p.Branch}, "merged": p.State == "merged", "updated_at": "2026-11-02T10:00:00+08:00"}
	if p.State == "merged" {
		pr["merged_at"] = "2026-11-02T10:00:00+08:00"
	}
	if p.Body == "" {
		pr["body"] = nil
	}
	out, _ := json.Marshal(map[string]any{"hook_name": "merge_request_hooks", "action": p.Action, "action_desc": p.ActionDesc, "pull_request": pr,
		"repository": repoJSON(p.Repo, !p.Public), "sender": userJSON(p.AuthorID, p.AuthorLogin)})
	return out
}

func pushPayload(repo, ref, user string, commits ...map[string]string) []byte {
	list := []map[string]any{}
	for _, c := range commits {
		list = append(list, map[string]any{"id": c["id"], "message": c["message"], "url": "https://gitee.example.test/" + repo + "/commit/" + c["id"],
			"author": userJSON(777, user)})
	}
	out, _ := json.Marshal(map[string]any{"hook_name": "push_hooks", "ref": ref, "commits": list, "repository": repoJSON(repo, true), "sender": userJSON(777, user)})
	return out
}

// geIssue 是 Gitee Issue 载荷：id 是数字，number 是字母数字串。
type geIssue struct {
	Repo, Action, State, Title, Body string
	N                                int // 决定 id 与 number
	Assignee                         string
	AssigneeID                       int64
}

func (e geIssue) number() string { return fmt.Sprintf("IG%03d", e.N) }
func (e geIssue) id() int64      { return int64(310000000 + e.N) }

func (e geIssue) issueMap() map[string]any {
	if e.Repo == "" {
		e.Repo = appRepo
	}
	if e.State == "" {
		e.State = "open"
	}
	m := map[string]any{"id": e.id(), "number": e.number(), "title": e.Title, "body": e.Body, "state": e.State,
		"html_url": fmt.Sprintf("https://gitee.example.test/%s/issues/%s", e.Repo, e.number()), "user": userJSON(5151, "lin"), "assignee": nil,
		"created_at": "2026-11-02T09:00:00+08:00", "updated_at": "2026-11-02T10:00:00+08:00"}
	if e.Assignee != "" {
		m["assignee"] = userJSON(e.AssigneeID, e.Assignee)
	}
	return m
}

func (e geIssue) payload() []byte {
	if e.Repo == "" {
		e.Repo = appRepo
	}
	out, _ := json.Marshal(map[string]any{"hook_name": "issue_hooks", "action": e.Action, "issue": e.issueMap(), "repository": repoJSON(e.Repo, true),
		"sender": userJSON(5151, "lin")})
	return out
}

func (e geIssue) notePayload(noteID int64, text string) []byte {
	if e.Repo == "" {
		e.Repo = appRepo
	}
	out, _ := json.Marshal(map[string]any{"hook_name": "note_hooks", "action": "comment", "noteable_type": "Issue",
		"comment": map[string]any{"id": noteID, "body": text, "user": userJSON(777, "octocat"), "created_at": "2026-11-02T10:30:00+08:00"},
		"issue":   e.issueMap(), "repository": repoJSON(e.Repo, true), "sender": userJSON(777, "octocat")})
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
	return integrations.Service{Box: w.box, GitFactory: w.factory, GiteeConfigured: true, PublicURL: w.publicURL}
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

func TestGitee(t *testing.T) {
	w := setup(t)
	w.fake.SetIdentity("lin", 5151, "openfox-dev", "other-group")
	w.fake.AddRepo(appRepo, true)
	w.fake.AddRepo(siteRepo, false)
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
		seen := false
		for _, p := range overview.Providers {
			if p.ID == "gitee" {
				seen = true
				if p.Availability != "available" || !p.Configured || !p.Bindable || p.OAuthRedirectURI != w.publicURL+"/api/v1/integrations/gitee/callback" || p.DefaultInstanceURL != "" {
					t.Fatalf("gitee = %+v", p)
				}
			}
		}
		if !seen {
			t.Fatalf("providers = %+v", overview.Providers)
		}
		// 未配置的实例：目录里可用但 configured / bindable 为假。
		bare := w.login(w.bare.URL+"/api", fmt.Sprintf("ge-owner-%s@example.test", w.org[:8]), w.ownerID)
		bare.json(http.StatusOK, http.MethodGet, w.path("/integrations"), nil, &overview)
		for _, p := range overview.Providers {
			if p.ID == "gitee" && (p.Availability != "available" || p.Configured || p.Bindable) {
				t.Fatalf("unconfigured gitee = %+v", p)
			}
		}
	})

	t.Run("authorize guards", func(t *testing.T) {
		path := w.path("/integrations/gitee/authorize")
		w.member.expect(http.StatusForbidden, "", http.MethodPost, path, w.startBody("organization", ""))
		w.guest.expect(http.StatusForbidden, "", http.MethodPost, path, w.startBody("personal", ""))
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "bogus"})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"purpose": "reauthorize"})
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, path, map[string]any{"purpose": "reauthorize", "connection_id": w.newUUID()})
		bare := w.login(w.bare.URL+"/api", fmt.Sprintf("ge-owner-%s@example.test", w.org[:8]), w.ownerID)
		bare.expect(http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", http.MethodPost, path, w.startBody("organization", ""))
		nobox := w.login(w.nobox.URL+"/api", fmt.Sprintf("ge-owner-%s@example.test", w.org[:8]), w.ownerID)
		nobox.expect(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPost, path, w.startBody("organization", ""))
	})

	t.Run("organization oauth", func(t *testing.T) {
		r := w.runOAuth(w.owner, "organization", "")
		if r.Status != "ok" {
			t.Fatalf("oauth = %+v", r)
		}
		if len(w.fake.Challenges()) != 0 {
			t.Fatalf("gitee has no PKCE, but the authorize page saw %v", w.fake.Challenges())
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
		if conn.ExternalID != "openfox-dev" || conn.Status != "ACTIVE" || conn.InstanceURL != "" {
			t.Fatalf("connection = %+v", conn)
		}
		// 访问令牌一天过期；令牌与 Secret 只在加密凭据里。
		var expires *time.Time
		var sealed []byte
		w.scan(&expires, `SELECT token_expires_at FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		w.scan(&sealed, `SELECT credentials_sealed FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		if expires == nil || time.Until(*expires) < 23*time.Hour || time.Until(*expires) > 25*time.Hour {
			t.Fatalf("token_expires_at = %v", expires)
		}
		if strings.Contains(string(sealed), "gtok") || strings.Contains(string(sealed), appSecret) {
			t.Fatal("credentials must be encrypted")
		}
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
		_, location := w.authorize(w.owner, "organization", "")
		if r := w.callback(w.admin, location); r.Reason != "invalid_state" {
			t.Fatalf("other user = %+v", r)
		}
		parsed, _ := neturl.Parse(location)
		for _, other := range []string{"github", "gitlab"} {
			response, err := w.owner.noRedirect().Get(w.publicURL + "/api/v1/integrations/" + other + "/callback?" + parsed.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if !strings.Contains(response.Header.Get("Location"), "reason=invalid_state") {
				t.Fatalf("cross provider callback via %s = %s", other, response.Header.Get("Location"))
			}
		}
	})

	t.Run("webhook config", func(t *testing.T) {
		view := w.webhook(w.owner)
		if !strings.HasPrefix(view.PayloadURL, w.publicURL+"/api/v1/hooks/git/gitee/") || len(view.Secret) != 64 || len(view.Events) != 4 {
			t.Fatalf("webhook = %+v", view)
		}
		w.member.expect(http.StatusForbidden, "", http.MethodGet, w.path("/integrations/connections/"+w.connID+"/webhook"), nil)
	})

	t.Run("webhook password mode", func(t *testing.T) {
		task := w.task(w.foxSpace, "Password guard")
		payload := unique(prSpec{Action: "open", Number: 1, Title: task.DisplayID + " guard", Branch: "x"}.payload())
		before := w.deliveryCount()
		if status, _ := w.post(w.webhookURL, "wrong-password", "Merge Request Hook", payload); status != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d", status)
		}
		if status, _ := w.post(w.webhookURL, "", "Merge Request Hook", payload); status != http.StatusUnauthorized {
			t.Fatalf("missing password = %d", status)
		}
		for _, token := range []string{w.webhookSecret[:len(w.webhookSecret)-1], w.webhookSecret + "0"} {
			if status, _ := w.post(w.webhookURL, token, "Merge Request Hook", payload); status != http.StatusUnauthorized {
				t.Fatalf("near password = %d", status)
			}
		}
		if w.deliveryCount() != before {
			t.Fatal("rejected deliveries must not be recorded")
		}
		// 令牌属于 Gitee 连接，在其他服务商路径上不可用；未知令牌 404。
		for _, other := range []string{"github", "gitlab"} {
			otherURL := strings.Replace(w.webhookURL, "/hooks/git/gitee/", "/hooks/git/"+other+"/", 1)
			if status, _ := w.post(otherURL, w.webhookSecret, "Merge Request Hook", payload); status != http.StatusNotFound {
				t.Fatalf("cross provider path %s = %d", other, status)
			}
		}
		unknown := strings.TrimSuffix(w.webhookURL, w.webhookURL[strings.LastIndex(w.webhookURL, "/")+1:]) + "unknowntoken1234567890"
		if status, _ := w.post(unknown, w.webhookSecret, "Merge Request Hook", payload); status != http.StatusNotFound {
			t.Fatalf("unknown token = %d", status)
		}
		// 正确密码：处理；同一份正文重放：重复，不再处理。
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", payload); status != http.StatusOK || !strings.Contains(body, `"duplicate":false`) {
			t.Fatalf("first = %d %s", status, body)
		}
		if status, body := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", payload); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
			t.Fatalf("replay = %d %s", status, body)
		}
		// 缺少事件头 400。
		if status, _ := w.post(w.webhookURL, w.webhookSecret, "", []byte(`{}`)); status != http.StatusBadRequest {
			t.Fatalf("missing event = %d", status)
		}
		var linkCount int
		w.scan(&linkCount, `SELECT count(*) FROM openfox.task_git_links WHERE task_id = $1::uuid`, task.ID)
		if linkCount != 1 || w.deliveryCount() != before+1 {
			t.Fatalf("links = %d deliveries = %d (before %d)", linkCount, w.deliveryCount(), before)
		}
	})

	t.Run("webhook signature mode", func(t *testing.T) {
		task := w.task(w.foxSpace, "Signature guard")
		mk := func(n int) []byte {
			return unique(prSpec{Action: "open", Number: n, Title: task.DisplayID + " sig", Branch: "s"}.payload())
		}
		before := w.deliveryCount()
		now := time.Now()
		// 错误的密钥、时间戳被篡改（签名与头里的时间戳对不上）、过期（2 小时前）、未来（2 小时后）一律 401。
		if status, _ := w.postSigned(w.webhookURL, "another-secret", "Merge Request Hook", now, true, mk(41)); status != http.StatusUnauthorized {
			t.Fatalf("wrong secret = %d", status)
		}
		request := fakegitee.SignedWebhook(w.webhookURL, w.webhookSecret, "Merge Request Hook", now, true, mk(42))
		request.Header.Set("X-Gitee-Timestamp", strconv.FormatInt(now.Add(time.Second).UnixMilli(), 10))
		if status, _ := w.postWith(request); status != http.StatusUnauthorized {
			t.Fatalf("tampered timestamp = %d", status)
		}
		if status, _ := w.postSigned(w.webhookURL, w.webhookSecret, "Merge Request Hook", now.Add(-2*time.Hour), true, mk(43)); status != http.StatusUnauthorized {
			t.Fatalf("expired = %d", status)
		}
		if status, _ := w.postSigned(w.webhookURL, w.webhookSecret, "Merge Request Hook", now.Add(2*time.Hour), false, mk(44)); status != http.StatusUnauthorized {
			t.Fatalf("future = %d", status)
		}
		request = fakegitee.SignedWebhook(w.webhookURL, w.webhookSecret, "Merge Request Hook", now, true, mk(45))
		request.Header.Del("X-Gitee-Timestamp")
		if status, _ := w.postWith(request); status != http.StatusUnauthorized {
			t.Fatalf("missing timestamp = %d", status)
		}
		if w.deliveryCount() != before {
			t.Fatal("rejected deliveries must not be recorded")
		}
		// 窗口边缘内（50 分钟前）的正确签名通过；URL 编码前后两种写法都接受。
		first := mk(46)
		if status, body := w.postSigned(w.webhookURL, w.webhookSecret, "Merge Request Hook", now.Add(-50*time.Minute), true, first); status != http.StatusOK || !strings.Contains(body, `"duplicate":false`) {
			t.Fatalf("encoded = %d %s", status, body)
		}
		if status, body := w.postSigned(w.webhookURL, w.webhookSecret, "Merge Request Hook", now, false, mk(47)); status != http.StatusOK || !strings.Contains(body, `"duplicate":false`) {
			t.Fatalf("plain = %d %s", status, body)
		}
		// 重放：同一份正文（换了新的有效签名）仍是重复。
		if status, body := w.postSigned(w.webhookURL, w.webhookSecret, "Merge Request Hook", now, true, first); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
			t.Fatalf("replay = %d %s", status, body)
		}
		// 跨服务商路径上签名同样无效（令牌不属于那个服务商）。
		githubURL := strings.Replace(w.webhookURL, "/hooks/git/gitee/", "/hooks/git/github/", 1)
		if status, _ := w.postSigned(githubURL, w.webhookSecret, "Merge Request Hook", now, true, mk(48)); status != http.StatusNotFound {
			t.Fatalf("cross provider signed = %d", status)
		}
		if w.deliveryCount() != before+2 {
			t.Fatalf("deliveries = %d, want %d", w.deliveryCount(), before+2)
		}
	})

	t.Run("pull request links and status", func(t *testing.T) {
		task := w.task(w.foxSpace, "PR flow")
		other := w.task(w.foxSpace, "PR other")
		if name, _ := w.taskStatus(w.foxSpace, task); name == "In Progress" || name == "Done" {
			t.Fatalf("unexpected initial status %q", name)
		}
		spec := prSpec{Action: "open", Number: 11, Title: task.DisplayID + " implement it", Body: "also " + other.DisplayID, Branch: "feature/x"}
		if status, detail, linked := w.deliver("Merge Request Hook", spec.payload()); status != "PROCESSED" || detail != "linked" || linked != 2 {
			t.Fatalf("open = %s %q %d", status, detail, linked)
		}
		if name, _ := w.taskStatus(w.foxSpace, task); name != "In Progress" {
			t.Fatalf("after open: %q", name)
		}
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["kind"] != "PULL_REQUEST" || links[0]["provider"] != "gitee" || links[0]["state"] != "OPEN" ||
			links[0]["repository"] != appRepo || int(links[0]["number"].(float64)) != 11 || links[0]["matched_in"] != "title" {
			t.Fatalf("links = %v", links)
		}
		// 源分支有新提交（action_desc）与改标题都是更新，不重复关联。
		spec.Action, spec.ActionDesc = "update", "source_branch_changed"
		spec.Title = task.DisplayID + " implement it better"
		w.deliver("Merge Request Hook", spec.payload())
		if links = w.gitLinks(w.owner, w.foxSpace, task); len(links) != 1 || links[0]["title"] != spec.Title {
			t.Fatalf("after update = %v", links)
		}
		spec.Action, spec.ActionDesc, spec.State = "close", "", "closed"
		w.deliver("Merge Request Hook", spec.payload())
		if name, _ := w.taskStatus(w.foxSpace, task); name != "In Progress" {
			t.Fatalf("after close: %q", name)
		}
		if links = w.gitLinks(w.owner, w.foxSpace, task); links[0]["state"] != "CLOSED" {
			t.Fatalf("closed = %v", links)
		}
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
		foreign := prSpec{Action: "open", Number: 12, Title: task.DisplayID, Repo: "other-group/x"}
		if status, detail, _ := w.deliver("Merge Request Hook", foreign.payload()); status != "IGNORED" || detail != "owner mismatch" {
			t.Fatalf("foreign = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Merge Request Hook", prSpec{Action: "open", Number: 13, Title: "no ref"}.payload()); status != "PROCESSED" || detail != "no task reference" {
			t.Fatalf("no ref = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Merge Request Hook", prSpec{Action: "tested", Number: 11, Title: task.DisplayID}.payload()); status != "IGNORED" || detail != "action not handled" {
			t.Fatalf("tested = %s %q", status, detail)
		}
		tag, _ := json.Marshal(map[string]any{"hook_name": "tag_push_hooks", "ref": "refs/tags/v1", "repository": repoJSON(appRepo, true)})
		if status, _, _ := w.deliver("Tag Push Hook", tag); status != "IGNORED" {
			t.Fatalf("tag push = %s", status)
		}
	})

	t.Run("pull request backlink", func(t *testing.T) {
		task := w.task(w.foxSpace, "Backlink flow")
		w.deliver("Merge Request Hook", prSpec{Action: "open", Number: 21, Title: task.DisplayID + " backlink", Branch: "b"}.payload())
		waitFor(t, "backlink note", func() bool {
			for _, note := range w.fake.Notes() {
				if note.Kind == "pulls" && note.Number == "21" && strings.Contains(note.Body, task.DisplayID) && note.Token != "" {
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
		w.owner.expect(http.StatusOK, "", http.MethodPut, w.path("/integrations/gitee/settings"),
			map[string]any{"version": 0, "settings": w.settingsBody(func(m map[string]any) { m["link_commits"] = true })})
		if status, detail, linked := w.deliver("Push Hook", payload); status != "PROCESSED" || detail != "linked" || linked != 1 {
			t.Fatalf("enabled = %s %q %d", status, detail, linked)
		}
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["kind"] != "COMMIT" || links[0]["provider"] != "gitee" || links[0]["sha"] != sha {
			t.Fatalf("links = %v", links)
		}
	})

	t.Run("personal binding", func(t *testing.T) {
		w.guest.expect(http.StatusForbidden, "", http.MethodPost, w.path("/integrations/gitee/authorize"), w.startBody("personal", ""))
		w.fake.SetIdentity("octocat", 777, "openfox-dev")
		r := w.runOAuth(w.member, "personal", "")
		if r.Status != "ok" {
			t.Fatalf("personal = %+v", r)
		}
		w.fake.SetIdentity("lin", 5151, "openfox-dev", "other-group")
		_, personal := w.detail(w.member)
		// 个人连接不挂在组织连接下（Gitee 是单一 SaaS）。
		if len(personal) != 1 || personal[0].ExternalID != "777" || personal[0].DisplayName != "octocat" || personal[0].Parent != nil {
			t.Fatalf("personal = %+v", personal)
		}
		w.fake.SetIdentity("octocat", 777, "openfox-dev")
		w.runOAuth(w.member, "personal", "")
		w.fake.SetIdentity("lin", 5151, "openfox-dev", "other-group")
		if _, again := w.detail(w.member); len(again) != 1 || again[0].ID != personal[0].ID {
			t.Fatalf("again = %+v", again)
		}
		task := w.task(w.foxSpace, "Author flow")
		w.deliver("Merge Request Hook", prSpec{Action: "open", Number: 31, Title: task.DisplayID, AuthorID: 777, AuthorLogin: "octocat"}.payload())
		links := w.gitLinks(w.owner, w.foxSpace, task)
		if len(links) != 1 || links[0]["author_user_id"] != w.memberID {
			t.Fatalf("author = %v", links)
		}
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
			map[string]any{"version": version, "confirm_phrase": "断开 Gitee"}, nil)
		if status, _ := w.post(w.webhookURL, w.webhookSecret, "Merge Request Hook", unique(prSpec{Action: "open", Number: 99, Title: "x"}.payload())); status != http.StatusNotFound {
			t.Fatalf("disconnected webhook = %d", status)
		}
	})

	t.Run("tokens never travel in write urls", func(t *testing.T) {
		if w.fake.QueryTokenWrites() != 0 || w.fake.MissingUserAgent() != 0 {
			t.Fatalf("query token writes = %d, token requests without user-agent = %d", w.fake.QueryTokenWrites(), w.fake.MissingUserAgent())
		}
	})

	t.Run("no secrets in responses", func(t *testing.T) {
		w.log.mu.Lock()
		defer w.log.mu.Unlock()
		for _, body := range w.log.bodies {
			if strings.Contains(body, appSecret) || strings.Contains(body, "gtok-fake") || strings.Contains(body, "grt-fake") {
				t.Fatalf("response leaks a secret: %.200s", body)
			}
		}
	})
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
