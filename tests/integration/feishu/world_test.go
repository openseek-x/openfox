package feishuint

import (
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/integrations"
	fakedingtalk "openfox/tests/support/fakedingtalk/fake"
	fakefeishu "openfox/tests/support/fakefeishu/fake"
)

type world struct {
	*env
	org, orgB                           string
	ownerID, adminID, memberID, guestID string
	ownerBID                            string
	owner, admin, member, guest         *client
	ownerB                              *client
	foxSpace, secSpace                  string
	appID, secret                       string
	// provider 是本用例测试的 IM 服务商（feishu / lark），fake 是它对应的本机模拟服务（遮住 env.fake）。
	provider string
	fake     *fakefeishu.Server
	// dtFake 是钉钉世界的模拟服务（provider = dingtalk 时使用），其余世界为 nil。
	dtFake *fakedingtalk.Server
}

func (w *world) path(suffix string) string { return "/v1/organizations/" + w.org + suffix }
func (w *world) appPath() string           { return w.path("/integrations/" + w.provider + "/app") }

func setup(t *testing.T) *world { return setupFor(t, "feishu") }

// setupFor 为指定服务商建立世界；lark 用独立的模拟服务，应用 ID 前缀与飞书一致（cli_）。
func setupFor(t *testing.T, provider string) *world {
	e := newEnv(t)
	// 同一个库里其他用例留下的飞书连接不应被本用例的长连接管理器接手。
	e.exec(`UPDATE openfox.integration_connections SET status = 'DISCONNECTED', disconnected_at = clock_timestamp()
	         WHERE provider IN ('feishu', 'lark', 'dingtalk') AND status <> 'DISCONNECTED'`)
	w := &world{env: e, provider: provider, fake: e.fake}
	if provider == "lark" {
		w.fake = e.fakeLark
	}
	w.org, w.orgB = e.newUUID(), e.newUUID()
	suffix := w.org[:8]
	mk := func(name string, target *string) {
		*target = e.user(fmt.Sprintf("fs-%s-%s@example.test", name, suffix))
	}
	mk("owner", &w.ownerID)
	mk("admin", &w.adminID)
	mk("member", &w.memberID)
	mk("guest", &w.guestID)
	mk("ownerb", &w.ownerBID)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Feishu A'), ($3::uuid, $4, 'Feishu B')`,
		w.org, "fs-a-"+suffix, w.orgB, "fs-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at) VALUES ($1::uuid, $2, 'Feishu A', now()), ($3::uuid, $4, 'Feishu B', now())`,
		w.org, "fs-a-"+suffix, w.orgB, "fs-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'ADMIN'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'GUEST'),
		($6::uuid, $7::uuid, 'OWNER')`, w.org, w.ownerID, w.adminID, w.memberID, w.guestID, w.orgB, w.ownerBID)
	for _, org := range []string{w.org, w.orgB} {
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Cancelled', '', 'CANCELLED', 5)`, org)
	}
	base := e.server.URL + "/api"
	w.owner = e.login(base, "fs-owner-"+suffix+"@example.test", w.ownerID)
	w.admin = e.login(base, "fs-admin-"+suffix+"@example.test", w.adminID)
	w.member = e.login(base, "fs-member-"+suffix+"@example.test", w.memberID)
	w.guest = e.login(base, "fs-guest-"+suffix+"@example.test", w.guestID)
	w.ownerB = e.login(base, "fs-ownerb-"+suffix+"@example.test", w.ownerBID)
	var space struct {
		ID string `json:"id"`
	}
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Fox", "prefix": "fox", "visibility": "SHARED"}, &space)
	w.foxSpace = space.ID
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Secret", "prefix": "sec", "visibility": "PRIVATE"}, &space)
	w.secSpace = space.ID
	compact := strings.ReplaceAll(w.org, "-", "")
	w.appID = "cli_" + compact[:12]
	w.secret = "secret" + compact[12:28]
	if provider == "dingtalk" {
		// 钉钉：AppKey 以 ding 开头；模拟服务里登记应用和员工（员工 userid 对应 staffId）。
		w.appID = "ding" + compact[:12]
		w.dtFake = e.fakeDT
		w.dtFake.AddApp(w.appID, w.secret)
		for _, u := range []fakedingtalk.User{
			{OpenID: "ou_owner", UnionID: "un_owner", StaffID: "st_owner", Name: "Lin Owner"},
			{OpenID: "ou_member", UnionID: "un_member", StaffID: "st_member", Name: "Mem Ber"},
			{OpenID: "ou_admin", UnionID: "un_admin", StaffID: "st_admin", Name: "Ad Min"},
		} {
			w.dtFake.AddUser(u)
		}
		return w
	}
	w.fake.AddApp(w.appID, w.secret, "OpenFox 助手")
	w.fake.AddUser(w.appID, "ou_owner", "Lin Owner")
	w.fake.AddUser(w.appID, "ou_member", "Mem Ber")
	w.fake.AddUser(w.appID, "ou_member2", "Mem Ber Two")
	w.fake.AddUser(w.appID, "ou_admin", "Ad Min")
	return w
}

type appView struct {
	ConnectionID       string  `json:"connection_id"`
	AppID              string  `json:"app_id"`
	BotName            string  `json:"bot_name"`
	Status             string  `json:"status"`
	StatusReason       string  `json:"status_reason"`
	LastConnectedAt    *string `json:"last_connected_at"`
	LastEventAt        *string `json:"last_event_at"`
	LastError          string  `json:"last_error"`
	LinkPreviewEnabled bool    `json:"link_preview_enabled"`
	CredentialsHint    string  `json:"credentials_hint"`
	Version            uint64  `json:"version"`
}

type personalView struct {
	ConnectionID string `json:"connection_id"`
	DisplayName  string `json:"display_name"`
	AvatarURL    string `json:"avatar_url"`
	Version      uint64 `json:"version"`
}

type fullView struct {
	CanManage bool          `json:"can_manage"`
	App       *appView      `json:"app"`
	Personal  *personalView `json:"personal"`
	Guide     struct {
		OpenPlatformURL    string   `json:"open_platform_url"`
		RedirectURI        string   `json:"redirect_uri"`
		LinkPreviewPattern string   `json:"link_preview_pattern"`
		ScopesJSON         string   `json:"scopes_json"`
		Events             []string `json:"events"`
		Callbacks          []string `json:"callbacks"`
	} `json:"guide"`
}

func (w *world) view(c *client) fullView {
	w.t.Helper()
	var v fullView
	c.json(http.StatusOK, http.MethodGet, w.appPath(), nil, &v)
	return v
}

func (w *world) bind() appView {
	w.t.Helper()
	var app appView
	w.owner.json(http.StatusOK, http.MethodPut, w.appPath(), map[string]any{"app_id": w.appID, "app_secret": w.secret}, &app)
	return app
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *world) waitStatus(want string) appView {
	w.t.Helper()
	var last appView
	waitFor(w.t, "app status "+want, func() bool {
		v := w.view(w.owner)
		if v.App != nil {
			last = *v.App
		}
		return v.App != nil && v.App.Status == want
	})
	return last
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	w.scan(&n, query, args...)
	return n
}

// ---------- 长连接管理器 ----------

type manager struct {
	*integrations.LongConnManager
	cancel func()
	done   chan struct{}
	once   sync.Once
}

func (m *manager) stop() {
	m.once.Do(func() {
		m.cancel()
		<-m.done
	})
}

// startManager 启动一个使用较短间隔的长连接管理器实例。
func (w *world) startManager(instance string) *manager {
	w.t.Helper()
	m := &integrations.LongConnManager{Pool: w.pool, Instance: instance, Service: w.service, Client: w.fs,
		ScanInterval: 150 * time.Millisecond, LeaseTTL: 2 * time.Second, RenewInterval: 400 * time.Millisecond, RetryBase: 150 * time.Millisecond}
	ctx, cancel := contextWithCancel(w.ctx)
	h := &manager{LongConnManager: m, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		_ = m.Run(ctx)
	}()
	w.t.Cleanup(h.stop)
	return h
}

func (w *world) notifyWorker() *integrations.FeishuNotifyWorker {
	return &integrations.FeishuNotifyWorker{Pool: w.pool, Service: w.service, Instance: "notify-test"}
}

func (w *world) runNotify() {
	w.t.Helper()
	if err := w.notifyWorker().RunOnce(w.ctx); err != nil {
		w.t.Fatal(err)
	}
}

// ---------- OAuth ----------

type oauthResult struct{ SessionID, Status, Reason string }

func (w *world) authorize(c *client) (sessionID, callback string) {
	w.t.Helper()
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		SessionID    string `json:"session_id"`
		ExpiresAt    string `json:"expires_at"`
	}
	c.json(http.StatusOK, http.MethodPost, w.path("/integrations/"+w.provider+"/authorize"), nil, &start)
	if start.SessionID == "" || start.ExpiresAt == "" || !strings.HasPrefix(start.AuthorizeURL, w.fake.URL+"/open-apis/authen/v1/authorize?") {
		w.t.Fatalf("start = %+v", start)
	}
	parsed, _ := neturl.Parse(start.AuthorizeURL)
	q := parsed.Query()
	if q.Get("client_id") != w.appID || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") != w.publicURL+"/api/v1/integrations/"+w.provider+"/callback" || strings.Contains(start.AuthorizeURL, w.secret) {
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
	if q.Get("provider") != w.provider {
		w.t.Fatalf("provider = %q", q.Get("provider"))
	}
	return oauthResult{SessionID: q.Get("session"), Status: q.Get("status"), Reason: q.Get("reason")}
}

// link 让 c 用飞书用户 openID 走完整的授权流程。
func (w *world) link(c *client, openID string) oauthResult {
	w.t.Helper()
	w.fake.SetAuthorizeUser(openID)
	_, location := w.authorize(c)
	return w.callback(c, location)
}

func (w *world) mustLink(c *client, openID string) {
	w.t.Helper()
	if res := w.link(c, openID); res.Status != "ok" {
		w.t.Fatalf("link %s = %+v", openID, res)
	}
}

// ---------- 事件 ----------

func (w *world) taskURL(org, space, display string) string {
	return w.publicURL + "/o/" + org + "/spaces/" + space + "/tasks?task=" + neturl.QueryEscape(display)
}

// preview 推送 url.preview.get 并返回回包。
func (w *world) preview(openID, link string) string {
	w.t.Helper()
	event, _ := json.Marshal(map[string]any{"operator": map[string]string{"open_id": openID}, "context": map[string]string{"url": link}})
	data, err := w.fake.PushEvent(w.appID, "url.preview.get", string(event))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(data)
}

// ---------- 任务与通知 ----------

type taskInfo struct{ ID, DisplayID string }

func (w *world) task(space, title string, assignee string) taskInfo {
	w.t.Helper()
	body := map[string]any{"title": title}
	if assignee != "" {
		body["assignees"] = []map[string]any{{"kind": "HUMAN", "id": assignee}}
	}
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces/"+space+"/tasks"), body, &task)
	return taskInfo{ID: task.ID, DisplayID: task.DisplayID}
}

type outboxRow struct {
	Status, LastError, Remote string
	Attempts                  int
	NextInFuture              bool
}

// outbox 返回某成员最近一条出站项；没有返回 nil。
func (w *world) outbox(userID string) *outboxRow {
	w.t.Helper()
	var row outboxRow
	err := w.super.QueryRow(w.ctx, `
		SELECT status, last_error, remote_message_id, attempts, next_attempt_at > clock_timestamp() + interval '5 seconds'
		  FROM openfox.im_notification_outbox WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid
		 ORDER BY created_at DESC, id DESC LIMIT 1`, w.org, userID).Scan(&row.Status, &row.LastError, &row.Remote, &row.Attempts, &row.NextInFuture)
	if err != nil {
		return nil
	}
	return &row
}

func (w *world) outboxCount(userID string) int {
	return w.count(`SELECT count(*) FROM openfox.im_notification_outbox WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid`, w.org, userID)
}

func (w *world) assertNoSecretLeaked(secrets ...string) {
	w.t.Helper()
	w.log.mu.Lock()
	defer w.log.mu.Unlock()
	for _, body := range w.log.bodies {
		for _, secret := range append(secrets, w.secret) {
			if strings.Contains(body, secret) {
				w.t.Fatalf("a response body leaked a credential: %.200s", body)
			}
		}
	}
}
