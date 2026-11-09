package githubint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	fakegithub "openfox/tests/support/fakegithub/fake"
)

type world struct {
	*env
	org, orgB                           string
	ownerID, adminID, memberID, guestID string
	ownerBID                            string
	owner, admin, member, guest         *client
	ownerB                              *client
	foxSpace, secSpace                  string
	prefix                              string
	connID                              string // openfox-dev 组织连接
	webhookURL, webhookSecret           string
}

func (w *world) path(suffix string) string { return "/v1/organizations/" + w.org + suffix }

func setup(t *testing.T) *world {
	e := newEnv(t)
	w := &world{env: e}
	w.org, w.orgB = e.newUUID(), e.newUUID()
	suffix := w.org[:8]
	mk := func(name string, target *string) {
		email := fmt.Sprintf("gh-%s-%s@example.test", name, suffix)
		*target = e.user(email)
	}
	mk("owner", &w.ownerID)
	mk("admin", &w.adminID)
	mk("member", &w.memberID)
	mk("guest", &w.guestID)
	mk("ownerb", &w.ownerBID)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'GitHub A'), ($3::uuid, $4, 'GitHub B')`,
		w.org, "gh-a-"+suffix, w.orgB, "gh-b-"+suffix)
	// 控制面函数（Webhook 令牌、OAuth state 解析）通过组织目录定位组织。
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at) VALUES ($1::uuid, $2, 'GitHub A', now()), ($3::uuid, $4, 'GitHub B', now())`,
		w.org, "gh-a-"+suffix, w.orgB, "gh-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'ADMIN'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'GUEST'),
		($6::uuid, $7::uuid, 'OWNER')`, w.org, w.ownerID, w.adminID, w.memberID, w.guestID, w.orgB, w.ownerBID)
	// 状态：第一个进行中是 In Progress，第一个已完成是 Done；各有第二个，用来验证「第一个」。
	for _, org := range []string{w.org, w.orgB} {
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Cancelled', '', 'CANCELLED', 5),
			($1::uuid, 'Doing', '', 'IN_PROGRESS', 6), ($1::uuid, 'Shipped', '', 'DONE', 7)`, org)
	}
	w.owner = e.login(e.server.URL+"/api", "gh-owner-"+suffix+"@example.test", w.ownerID)
	w.admin = e.login(e.server.URL+"/api", "gh-admin-"+suffix+"@example.test", w.adminID)
	w.member = e.login(e.server.URL+"/api", "gh-member-"+suffix+"@example.test", w.memberID)
	w.guest = e.login(e.server.URL+"/api", "gh-guest-"+suffix+"@example.test", w.guestID)
	w.ownerB = e.login(e.server.URL+"/api", "gh-ownerb-"+suffix+"@example.test", w.ownerBID)
	var space struct {
		ID string `json:"id"`
	}
	w.prefix = "fox"
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Fox", "prefix": w.prefix, "visibility": "SHARED"}, &space)
	w.foxSpace = space.ID
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Secret", "prefix": "sec", "visibility": "PRIVATE"}, &space)
	w.secSpace = space.ID
	return w
}

type taskInfo struct {
	ID, DisplayID string
	Sequence      int64
}

func (w *world) task(spaceID, title string) taskInfo {
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Sequence  int64  `json:"sequence"`
	}
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces/"+spaceID+"/tasks"), map[string]any{"title": title}, &task)
	return taskInfo{ID: task.ID, DisplayID: task.DisplayID, Sequence: task.Sequence}
}

// taskStatus 通过 API 读取任务的状态名与类别。
func (w *world) taskStatus(spaceID string, task taskInfo) (name, category string) {
	var got struct {
		StatusName     string `json:"status_name"`
		StatusCategory string `json:"status_category"`
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/spaces/"+spaceID+"/tasks/"+task.ID), nil, &got)
	return got.StatusName, got.StatusCategory
}

func (w *world) setTaskStatus(task taskInfo, statusName string) {
	w.exec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2),
		version = version + 1 WHERE organization_id = $1::uuid AND id = $3::uuid`, w.org, statusName, task.ID)
}

func (w *world) gitLinks(c *client, spaceID string, task taskInfo) []map[string]any {
	var got struct {
		Links []map[string]any `json:"links"`
	}
	c.json(http.StatusOK, http.MethodGet, w.path("/spaces/"+spaceID+"/tasks/"+task.ID+"/git-links"), nil, &got)
	return got.Links
}

// ---------- OAuth ----------

type oauthResult struct {
	SessionID, Status, Reason string
}

// authorize 发起授权并走完 GitHub 授权页，返回 GitHub 回调到本服务的地址（尚未访问）。
func (w *world) authorize(c *client, purpose, connectionID string) (sessionID, callback string) {
	w.t.Helper()
	body := map[string]any{"purpose": purpose}
	if connectionID != "" {
		body["connection_id"] = connectionID
	}
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		SessionID    string `json:"session_id"`
		ExpiresAt    string `json:"expires_at"`
	}
	c.json(http.StatusOK, http.MethodPost, w.path("/integrations/github/authorize"), body, &start)
	if start.SessionID == "" || start.ExpiresAt == "" || !strings.HasPrefix(start.AuthorizeURL, w.fake.URL+"/login/oauth/authorize?") {
		w.t.Fatalf("start = %+v", start)
	}
	parsed, _ := neturl.Parse(start.AuthorizeURL)
	q := parsed.Query()
	if q.Get("scope") != "repo read:org" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" ||
		q.Get("redirect_uri") != w.publicURL+"/api/v1/integrations/github/callback" || strings.Contains(start.AuthorizeURL, "csecret") {
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

// callback 以 c 的登录会话访问回调地址，解析跳转到前端完成页的参数。
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
	if q.Get("provider") != "github" {
		w.t.Fatalf("provider = %q", q.Get("provider"))
	}
	return oauthResult{SessionID: q.Get("session"), Status: q.Get("status"), Reason: q.Get("reason")}
}

func (w *world) runOAuth(c *client, purpose, connectionID string) oauthResult {
	w.t.Helper()
	_, location := w.authorize(c, purpose, connectionID)
	return w.callback(c, location)
}

type sessionView struct {
	ID           string  `json:"id"`
	Purpose      string  `json:"purpose"`
	Status       string  `json:"status"`
	ErrorCode    string  `json:"error_code"`
	ConnectionID *string `json:"connection_id"`
	Accounts     []struct {
		Login        string  `json:"login"`
		Kind         string  `json:"kind"`
		AvatarURL    string  `json:"avatar_url"`
		ConnectionID *string `json:"connection_id"`
	} `json:"accounts"`
}

func (w *world) session(c *client, id string) sessionView {
	var view sessionView
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/oauth/"+id), nil, &view)
	return view
}

type connView struct {
	ID                    string  `json:"id"`
	ExternalID            string  `json:"external_id"`
	DisplayName           string  `json:"display_name"`
	AccountKind           string  `json:"account_kind"`
	Status                string  `json:"status"`
	StatusReason          string  `json:"status_reason"`
	AvatarURL             string  `json:"avatar_url"`
	WebhookLastDeliveryAt *string `json:"webhook_last_delivery_at"`
	Version               uint64  `json:"version"`
	OwnerUserID           *string `json:"owner_user_id"`
}

func (w *world) detail(c *client) (orgConns, personal []connView) {
	var got struct {
		Connections         []connView `json:"connections"`
		PersonalConnections []connView `json:"personal_connections"`
	}
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/github"), nil, &got)
	return got.Connections, got.PersonalConnections
}

type webhookView struct {
	ConnectionID   string   `json:"connection_id"`
	PayloadURL     string   `json:"payload_url"`
	ContentType    string   `json:"content_type"`
	Secret         string   `json:"secret"`
	Events         []string `json:"events"`
	LastDeliveryAt *string  `json:"last_delivery_at"`
	Recent         []struct {
		Event       string `json:"event"`
		Action      string `json:"action"`
		Status      string `json:"status"`
		Detail      string `json:"detail"`
		LinkedCount int    `json:"linked_count"`
	} `json:"recent"`
	Version uint64 `json:"version"`
}

func (w *world) webhook(c *client) webhookView {
	var view webhookView
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/connections/"+w.connID+"/webhook"), nil, &view)
	return view
}

// ---------- Webhook ----------

// post 发送带签名的 Webhook（无登录会话、无 CSRF）。
func (w *world) post(targetURL, secret, event, delivery string, payload []byte) (int, string) {
	w.t.Helper()
	request := fakegithub.SignedWebhook(targetURL, secret, event, delivery, payload)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

type prSpec struct {
	Action, Title, Body, Branch, Owner, Repo, State string
	Number                                          int
	Merged, Draft, Private                          bool
	AuthorID                                        int64
	AuthorLogin                                     string
}

func (p prSpec) payload() []byte {
	if p.Owner == "" {
		p.Owner = "openfox-dev"
	}
	if p.Repo == "" {
		p.Repo = "app"
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
	pr := map[string]any{"id": 5000 + p.Number, "number": p.Number, "title": p.Title, "body": p.Body, "html_url": fmt.Sprintf("https://github.example.test/%s/%s/pull/%d", p.Owner, p.Repo, p.Number),
		"state": p.State, "draft": p.Draft, "merged": p.Merged, "user": map[string]any{"id": p.AuthorID, "login": p.AuthorLogin},
		"head": map[string]any{"ref": p.Branch}}
	if p.Merged {
		pr["merged_at"] = "2026-10-31T10:00:00Z"
	}
	if p.State == "closed" {
		pr["closed_at"] = "2026-10-31T10:00:00Z"
	}
	out, _ := json.Marshal(map[string]any{"action": p.Action, "pull_request": pr,
		"repository": map[string]any{"full_name": p.Owner + "/" + p.Repo, "private": p.Private, "owner": map[string]any{"login": p.Owner}}})
	return out
}

var deliverySeq int

func nextDelivery() string {
	deliverySeq++
	return fmt.Sprintf("delivery-%d", deliverySeq)
}

func (w *world) sendPR(spec prSpec) (int, string) {
	return w.post(w.webhookURL, w.webhookSecret, "pull_request", nextDelivery(), spec.payload())
}

func (w *world) lastDelivery() (status, detail string, linked int) {
	w.scan(&status, `SELECT status FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	w.scan(&detail, `SELECT detail FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	w.scan(&linked, `SELECT linked_count FROM openfox.integration_webhook_deliveries WHERE connection_id = $1::uuid ORDER BY received_at DESC LIMIT 1`, w.connID)
	return
}

func (w *world) activityCount(task taskInfo, kind string) (count int) {
	w.scan(&count, `SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = $2`, task.ID, kind)
	return
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

func bigBody(size int) []byte { return bytes.Repeat([]byte("a"), size) }
