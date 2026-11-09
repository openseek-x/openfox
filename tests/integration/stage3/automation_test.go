package stage3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/secrets"
)

// 自动化集成测试：权限、校验、CRUD、手动运行、定时、事件与 Webhook。

type automationView struct {
	ID       string `json:"id"`
	Version  uint64 `json:"version"`
	CanEdit  bool   `json:"can_edit"`
	Triggers []struct {
		ID        string  `json:"id"`
		Kind      string  `json:"kind"`
		NextRunAt *string `json:"next_run_at"`
		Webhook   *struct {
			SignatureRequired bool `json:"signature_required"`
			HasSecret         bool `json:"has_secret"`
		} `json:"webhook"`
	} `json:"triggers"`
}

type automationSave struct {
	Automation  automationView `json:"automation"`
	WebhookURLs []struct {
		TriggerID string `json:"trigger_id"`
		URL       string `json:"url"`
	} `json:"webhook_urls"`
}

type runView struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	TriggerKind    string  `json:"trigger_kind"`
	Reason         string  `json:"reason"`
	ConversationID *string `json:"conversation_id"`
}

func automationsPath(spaceID string) string { return spacePath(spaceID) + "/automations" }

func scheduleTrigger(frequency string, extra map[string]any) map[string]any {
	schedule := map[string]any{"frequency": frequency, "timezone": "UTC"}
	for k, v := range extra {
		schedule[k] = v
	}
	return map[string]any{"kind": "schedule", "enabled": true, "config": map[string]any{"schedule": schedule}}
}

func automationBody(agentID string, triggers ...map[string]any) map[string]any {
	return map[string]any{
		"title": "Nightly check", "prompt": "Check the build status.",
		"assignee": map[string]any{"kind": "AGENT", "id": agentID},
		// 与界面默认一致：通知人员默认是创建者本人（manager 创建）
		"notify_user_ids": []string{s3ManagerID}, "triggers": triggers,
	}
}

func (e *s3Env) createAutomation(agentID string, triggers ...map[string]any) automationSave {
	e.t.Helper()
	var out automationSave
	e.manager().must(http.StatusCreated, http.MethodPost, automationsPath(e.space.ID), automationBody(agentID, triggers...), &out)
	return out
}

// runWorker 以固定时钟运行一次组织的自动化处理（与派发 worker 同一事务边界）。
func (e *s3Env) runWorker(now time.Time) {
	e.t.Helper()
	worker := &dispatch.Worker{Pool: e.database, Now: func() time.Time { return now }}
	if err := worker.RunAutomations(e.ctx); err != nil {
		e.t.Fatalf("RunAutomations: %v", err)
	}
}

func (e *s3Env) runCount(automationID, kind string) int {
	return e.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid AND trigger_kind = $2`, automationID, kind)
}

func TestAutomationPermissionsAndValidation(t *testing.T) {
	env := newS3Env(t)
	agent := env.insertAgent(env.space.ID)
	owner := env.manager()

	// 跨租户：外部用户看不到组织内的自动化。
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, automationsPath(env.space.ID), nil)
	// 私有空间的非成员看不到空间（404）；空间只读成员（VIEWER）可以读，写被拒绝（403）。
	env.guest().expectError(http.StatusNotFound, "", http.MethodPost, automationsPath(env.space.ID), automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"})))
	env.addSpaceMember(env.space.ID, s3OtherID, "VIEWER")
	env.other().must(http.StatusOK, http.MethodGet, automationsPath(env.space.ID), nil, nil)
	env.other().expectError(http.StatusForbidden, "", http.MethodPost, automationsPath(env.space.ID), automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"})))

	invalid := []struct {
		name string
		body map[string]any
	}{
		{"指派 Agent 不存在", automationBody(s3NewID(t), scheduleTrigger("daily", map[string]any{"time": "09:00"}))},
		{"cron 分钟不是单个数字", automationBody(agent, map[string]any{"kind": "schedule", "enabled": true, "config": map[string]any{"schedule": map[string]any{"frequency": "custom", "cron": "*/5 * * * *", "timezone": "UTC"}}})},
		{"非法时区", automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00", "timezone": "Mars/Olympus"}))},
		{"非法筛选运算符", automationBody(agent, map[string]any{"kind": "task_status", "enabled": true, "config": map[string]any{"status_ids": []string{env.statusID(env.org, "DONE")}, "filters": []map[string]any{{"field": "label", "op": "is", "value": "x"}}}})},
		{"非法状态", automationBody(agent, map[string]any{"kind": "task_status", "enabled": true, "config": map[string]any{"status_ids": []string{s3NewID(t)}}})},
		{"非法 IP 白名单", automationBody(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}, "ip_allowlist": []string{"not-an-ip"}})},
		{"没有密钥却开启签名", automationBody(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}, "signature_required": true})},
		{"11 个触发器", automationBody(agent, repeatTrigger(11)...)},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			owner.expectError(http.StatusBadRequest, "", http.MethodPost, automationsPath(env.space.ID), tt.body)
		})
	}

	created := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	if created.Automation.Triggers[0].NextRunAt == nil {
		t.Fatal("schedule trigger has no next_run_at")
	}
	var list struct {
		Automations []automationView `json:"automations"`
	}
	env.member().must(http.StatusOK, http.MethodGet, automationsPath(env.space.ID), nil, &list)
	if len(list.Automations) != 1 || list.Automations[0].CanEdit {
		t.Fatalf("member list = %+v, want one automation with can_edit=false", list.Automations)
	}

	// 乐观锁：版本不一致返回 409。
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, automationsPath(env.space.ID)+"/"+created.Automation.ID,
		map[string]any{"version": created.Automation.Version + 5, "title": "Renamed"})
	// 重排必须恰好包含全部自动化。
	owner.expectError(http.StatusConflict, "AUTOMATION_LIST_CHANGED", http.MethodPut, automationsPath(env.space.ID)+"/reorder",
		map[string]any{"automation_ids": []string{}})
	// 每个空间最多 100 个：批量插入到 100 后，第 101 个返回 409。
	env.exec(`INSERT INTO openfox.automations (organization_id, space_id, title, prompt, assignee_kind, assignee_id, created_by)
		SELECT $1::uuid, $2::uuid, 'bulk', 'prompt', 'AGENT', $3::uuid, $4::uuid FROM generate_series(1, 99)`,
		env.org, env.space.ID, agent, s3OwnerID)
	owner.expectError(http.StatusConflict, "AUTOMATION_LIMIT", http.MethodPost, automationsPath(env.space.ID),
		automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"})))
}

func repeatTrigger(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = scheduleTrigger("daily", map[string]any{"time": "09:00"})
	}
	return out
}

func TestAutomationManualRunAndSchedule(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	owner := env.manager()
	created := env.createAutomation(agent, scheduleTrigger("hourly", map[string]any{"minute": 0}))
	base := automationsPath(env.space.ID) + "/" + created.Automation.ID

	var first runView
	owner.must(http.StatusCreated, http.MethodPost, base+"/run", nil, &first)
	if first.Status != "queued" || first.ConversationID == nil || first.TriggerKind != "manual" {
		t.Fatalf("manual run = %+v, want queued manual run with a conversation", first)
	}
	if n := env.count(`SELECT count(*) FROM openfox.conversations WHERE organization_id = $1::uuid AND agent_id = $2::uuid`, env.org, agent); n == 0 {
		t.Fatal("manual run did not create a conversation")
	}
	var second runView
	owner.must(http.StatusCreated, http.MethodPost, base+"/run", nil, &second)
	if second.Status != "skipped" || second.Reason != "上一次运行尚未结束" {
		t.Fatalf("second manual run = %+v, want skipped", second)
	}

	// 定时：错过多个周期只运行一次，并推进到未来时刻。
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '5 hours'
		WHERE organization_id = $1::uuid AND automation_id = $2::uuid`, env.org, created.Automation.ID)
	now := time.Now()
	env.runWorker(now)
	if n := env.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid AND trigger_kind = 'schedule' AND status = 'failed'`, created.Automation.ID); n != 0 {
		t.Fatalf("schedule runs failed = %d, want 0", n)
	}
	if n := env.runCount(created.Automation.ID, "schedule"); n != 1 {
		t.Fatalf("schedule runs = %d, want 1 after missing several periods", n)
	}
	var next time.Time
	env.scalar(&next, `SELECT next_run_at FROM openfox.automation_triggers WHERE organization_id = $1::uuid AND automation_id = $2::uuid`,
		env.org, created.Automation.ID)
	if !next.After(now) {
		t.Fatalf("next_run_at = %s, want after %s", next, now)
	}

	// 停用后手动运行照常受理（上一次还没结束，所以记为 skipped），定时不再建记录。
	// 停用规则的完整用例见 automation_disabled_10b_test.go。
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"version": created.Automation.Version, "enabled": false}, nil)
	var paused runView
	owner.must(http.StatusCreated, http.MethodPost, base+"/run", nil, &paused)
	if paused.TriggerKind != "manual" || paused.Status != "skipped" {
		t.Fatalf("manual run while disabled = %+v, want manual skipped (previous run unfinished)", paused)
	}
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '2 hours'
		WHERE organization_id = $1::uuid AND automation_id = $2::uuid`, env.org, created.Automation.ID)
	env.runWorker(time.Now())
	if n := env.runCount(created.Automation.ID, "schedule"); n != 1 {
		t.Fatalf("schedule runs after disabling = %d, want still 1", n)
	}
}

func TestAutomationEventsTaskStatusAndComment(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	owner := env.manager()
	done := env.statusID(env.org, "DONE")
	created := env.createAutomation(agent,
		map[string]any{"kind": "task_status", "enabled": true, "config": map[string]any{"status_ids": []string{done}}},
		map[string]any{"kind": "comment", "enabled": true, "config": map[string]any{"contains": "deploy"}})

	task := env.createTask(owner, env.space.ID, map[string]any{"title": "Ship it"})
	env.setStatus(owner, env.space.ID, task, "DONE")
	postComment(env, task, "Please DEPLOY tonight")
	postComment(env, task, "unrelated note")

	now := time.Now()
	env.runWorker(now)
	if n := env.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid AND status = 'failed'`, created.Automation.ID); n != 0 {
		t.Fatalf("event runs failed = %d, want 0", n)
	}
	if n := env.runCount(created.Automation.ID, "task_status"); n != 1 {
		t.Fatalf("task_status runs = %d, want 1", n)
	}
	if n := env.runCount(created.Automation.ID, "comment"); n != 1 {
		t.Fatalf("comment runs = %d, want 1 (keyword match is case-insensitive)", n)
	}
	// 同一事件只处理一次。
	env.runWorker(now)
	if n := env.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid`, created.Automation.ID); n != 2 {
		t.Fatalf("runs after second pass = %d, want 2", n)
	}
}

func TestWebhookReceiverAndManagement(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	owner := env.manager()
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	trigger := created.Automation.Triggers[0].ID
	if len(created.WebhookURLs) != 1 {
		t.Fatalf("webhook_urls = %+v, want one URL", created.WebhookURLs)
	}
	token := lastSegment(created.WebhookURLs[0].URL)
	base := automationsPath(env.space.ID) + "/" + created.Automation.ID

	// 令牌只以哈希存在。
	if n := env.count(`SELECT count(*) FROM openfox.automation_triggers WHERE id = $1::uuid AND webhook_token_hash = sha256(convert_to($2, 'UTF8'))`, trigger, token); n != 1 {
		t.Fatal("webhook token hash is not stored as SHA-256")
	}

	// 签名密钥：密封保存，库里没有明文。
	var wrapped struct {
		Secret string `json:"secret"`
	}
	owner.must(http.StatusOK, http.MethodPost, base+"/triggers/"+trigger+"/signing-secret", nil, &wrapped)
	var fetched struct {
		Secret string `json:"secret"`
	}
	owner.must(http.StatusOK, http.MethodGet, base+"/triggers/"+trigger+"/signing-secret", nil, &fetched)
	if fetched.Secret != wrapped.Secret {
		t.Fatal("GET signing-secret does not return the secret generated by POST")
	}
	if wrapped.Secret == "" || !strings.HasPrefix(wrapped.Secret, "whsec_") {
		t.Fatalf("signing secret = %q, want whsec_ prefix", wrapped.Secret)
	}
	if n := env.count(`SELECT count(*) FROM openfox.automation_triggers WHERE id = $1::uuid AND position(convert_to($2, 'UTF8') in webhook_secret_ciphertext) = 0`, trigger, wrapped.Secret); n != 1 {
		t.Fatal("signing secret appears in plaintext in the database")
	}
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"version": created.Automation.Version + 0, "triggers": []map[string]any{{
		"id": trigger, "kind": "webhook", "enabled": true, "config": map[string]any{}, "signature_required": true,
	}}}, nil)

	body := []byte(`{"event":"build"}`)
	post := func(headers map[string]string, payload []byte) (int, []byte) {
		t.Helper()
		return env.rawPost(env.server.URL+"/v1/hooks/"+token, payload, headers)
	}
	signed := func(delivery string, payload []byte) map[string]string {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		return map[string]string{
			"X-OpenFox-Delivery": delivery, "X-OpenFox-Timestamp": stamp, "X-OpenFox-Event": "build",
			"X-OpenFox-Signature": "v1=" + hmacHexString(wrapped.Secret, stamp+"."+string(payload)),
		}
	}

	if status, raw := post(signed("d-valid", body), body); status != http.StatusAccepted {
		t.Fatalf("valid signed webhook = %d %s, want 202", status, raw)
	}
	if n := env.runCount(created.Automation.ID, "webhook"); n != 1 {
		t.Fatalf("webhook runs = %d, want 1", n)
	}
	if status, _ := post(map[string]string{"X-OpenFox-Delivery": "d-missing"}, body); status != http.StatusUnauthorized {
		t.Fatalf("missing signature = %d, want 401", status)
	}
	if status, _ := post(map[string]string{"X-OpenFox-Delivery": "d-bad", "X-OpenFox-Timestamp": strconv.FormatInt(time.Now().Unix(), 10), "X-OpenFox-Signature": "v1=00"}, body); status != http.StatusUnauthorized {
		t.Fatalf("wrong signature = %d, want 401", status)
	}
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if status, _ := post(map[string]string{"X-OpenFox-Delivery": "d-stale", "X-OpenFox-Timestamp": stale, "X-OpenFox-Signature": "v1=" + hmacHexString(wrapped.Secret, stale+"."+string(body))}, body); status != http.StatusUnauthorized {
		t.Fatalf("expired timestamp = %d, want 401", status)
	}
	if n := env.count(`SELECT count(*) FROM openfox.webhook_deliveries WHERE trigger_id = $1::uuid AND status = 'rejected'`, trigger); n != 3 {
		t.Fatalf("rejected deliveries = %d, want 3", n)
	}

	// 同一去重键 10 分钟内只处理一次。
	if status, raw := post(signed("d-valid", body), body); status != http.StatusOK || !strings.Contains(string(raw), "ignored") {
		t.Fatalf("duplicate delivery = %d %s, want 200 ignored", status, raw)
	}

	// IP 白名单：来源 127.0.0.1 不在 203.0.113.0/24 内。
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"version": created.Automation.Version + 1, "triggers": []map[string]any{{
		"id": trigger, "kind": "webhook", "enabled": true, "config": map[string]any{}, "signature_required": true,
		"ip_allowlist": []string{"203.0.113.0/24"},
	}}}, nil)
	if status, _ := post(signed("d-ip", body), body); status != http.StatusForbidden {
		t.Fatalf("blocked source IP = %d, want 403", status)
	}

	// 体积上限 256 KB。
	if status, _ := post(signed("d-big", make([]byte, 256<<10+1)), make([]byte, 256<<10+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", status)
	}

	// 重新生成 URL 后旧令牌返回 404。
	var regenerated struct {
		URL string `json:"url"`
	}
	owner.must(http.StatusOK, http.MethodPost, base+"/triggers/"+trigger+"/webhook-url", nil, &regenerated)
	if status, _ := post(signed("d-old", body), body); status != http.StatusNotFound {
		t.Fatalf("old token = %d, want 404", status)
	}

	// 投递列表：已派发的投递可以重放，被拒绝的投递不能重放。
	var deliveries struct {
		Deliveries []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	owner.must(http.StatusOK, http.MethodGet, base+"/deliveries", nil, &deliveries)
	var dispatchedID, rejectedID string
	for _, d := range deliveries.Deliveries {
		if d.Status == "dispatched" && dispatchedID == "" {
			dispatchedID = d.ID
		}
		if d.Status == "rejected" && rejectedID == "" {
			rejectedID = d.ID
		}
	}
	owner.expectError(http.StatusConflict, "NOT_REPLAYABLE", http.MethodPost, base+"/deliveries/"+rejectedID+"/replay", nil)
	var replay struct {
		ReplayOf *string `json:"replay_of"`
	}
	owner.must(http.StatusCreated, http.MethodPost, base+"/deliveries/"+dispatchedID+"/replay", nil, &replay)
	if replay.ReplayOf == nil || *replay.ReplayOf != dispatchedID {
		t.Fatalf("replay_of = %v, want %s", replay.ReplayOf, dispatchedID)
	}
}

func TestWebhookRateLimitAndConcurrentDedupe(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	token := lastSegment(created.WebhookURLs[0].URL)
	url := env.server.URL + "/v1/hooks/" + token

	// 并发 10 个相同去重键只建出 1 个运行。
	var wg sync.WaitGroup
	statuses := make([]int, 10)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], _ = env.rawPost(url, []byte(`{"n":1}`), map[string]string{"X-OpenFox-Delivery": "same-key"})
		}(i)
	}
	wg.Wait()
	accepted := 0
	for _, status := range statuses {
		if status == http.StatusAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d (%v), want exactly 1", accepted, statuses)
	}
	if n := env.runCount(created.Automation.ID, "webhook"); n != 1 {
		t.Fatalf("webhook runs = %d, want 1", n)
	}

	// 同一令牌每分钟 60 次，第 61 次返回 429。
	other := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	otherURL := env.server.URL + "/v1/hooks/" + lastSegment(other.WebhookURLs[0].URL)
	for i := 1; i <= 61; i++ {
		status, _ := env.rawPost(otherURL, []byte("{}"), map[string]string{"X-OpenFox-Delivery": "rl-" + strconv.Itoa(i)})
		if i == 61 && status != http.StatusTooManyRequests {
			t.Fatalf("request 61 = %d, want 429", status)
		}
	}
}

// withWebhookServer 换成配置了密钥箱与公开地址的服务器，并重新登录客户端。
func withWebhookServer(env *s3Env) {
	env.t.Helper()
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		env.t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		env.t.Fatal(err)
	}
	server := httptest.NewServer(api.NewWithDispatch(env.auth, env.database, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{Secrets: box, PublicURL: "https://openfox.example"}))
	env.t.Cleanup(server.Close)
	env.server = server
	env.clients = map[string]*s3Client{}
}

// rawPost 直接发送 Webhook 请求（不带登录态）。
func (e *s3Env) rawPost(url string, body []byte, headers map[string]string) (int, []byte) {
	e.t.Helper()
	request, err := http.NewRequestWithContext(e.ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response.StatusCode, payload
}

func lastSegment(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}

func hmacHexString(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// postComment 通过 API 发表评论，评论触发器由数据库在租户上下文里写入事件。
func postComment(env *s3Env, task TaskView, body string) {
	env.t.Helper()
	env.manager().must(http.StatusCreated, http.MethodPost, spacePath(env.space.ID)+"/tasks/"+task.ID+"/comments",
		map[string]any{"body": body, "client_comment_id": s3NewID(env.t)}, nil)
}

// ensureDirectory 与应用创建组织时的做法一致：控制面目录表里要有组织，到期扫描与 Webhook 解析才能找到它。
func ensureDirectory(env *s3Env) {
	env.t.Helper()
	env.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
		SELECT id, slug, name, clock_timestamp() FROM openfox.organizations WHERE id = $1::uuid ON CONFLICT DO NOTHING`, env.org)
}

func TestAutomationDirectChatAndCreatorAccess(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	env.exec(`UPDATE openfox.agents SET allow_direct_chat = false WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	env.manager().expectError(http.StatusBadRequest, "AGENT_DIRECT_CHAT_DISABLED", http.MethodPost, automationsPath(env.space.ID),
		automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"})))

	// 保存时开启单聊，之后关闭：手动运行记为 failed 并写明原因。
	env.exec(`UPDATE openfox.agents SET allow_direct_chat = true WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	created := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	env.exec(`UPDATE openfox.agents SET allow_direct_chat = false WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	var run runView
	env.manager().must(http.StatusCreated, http.MethodPost, automationsPath(env.space.ID)+"/"+created.Automation.ID+"/run", nil, &run)
	if run.Status != "failed" || !strings.Contains(run.Reason, "单聊") {
		t.Fatalf("run after direct chat disabled = %+v, want failed with reason about direct chat", run)
	}
}

func TestAutomationCreatorInactiveAndIdempotentRetry(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	created := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	trigger := created.Automation.Triggers[0].ID

	// 创建者不在组织中：定时运行记为 failed，并写明原因。
	env.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, s3ManagerID)
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '1 hour' WHERE id = $1::uuid`, trigger)
	env.runWorker(time.Now())
	var reason, status string
	env.scalar(&status, `SELECT status FROM openfox.automation_runs WHERE automation_id = $1::uuid AND trigger_kind = 'schedule'`, created.Automation.ID)
	env.scalar(&reason, `SELECT reason FROM openfox.automation_runs WHERE automation_id = $1::uuid AND trigger_kind = 'schedule'`, created.Automation.ID)
	if status != "failed" || reason != "创建者已不在组织中" {
		t.Fatalf("creator inactive run = %s %q, want failed with creator reason", status, reason)
	}
	env.exec(`UPDATE openfox.organization_members SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, s3ManagerID)

	// 重试：推进失败等情况下，同一计划时刻再次处理不会产生第二条运行（dedupe_key 唯一约束）。
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '1 hour' WHERE id = $1::uuid`, trigger)
	var planned time.Time
	env.scalar(&planned, `SELECT next_run_at FROM openfox.automation_triggers WHERE id = $1::uuid`, trigger)
	env.runWorker(time.Now())
	before := env.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid`, created.Automation.ID)
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = $2 WHERE id = $1::uuid`, trigger, planned)
	env.runWorker(time.Now())
	if after := env.count(`SELECT count(*) FROM openfox.automation_runs WHERE automation_id = $1::uuid`, created.Automation.ID); after != before {
		t.Fatalf("schedule retry created %d extra runs", after-before)
	}

	// 事件重试：标记已处理之前失败，重新处理同一事件不产生第二条运行。
	comment := env.createAutomation(agent, map[string]any{"kind": "comment", "enabled": true, "config": map[string]any{"contains": "retry-me"}})
	task := env.createTask(env.manager(), env.space.ID, map[string]any{"title": "Retry"})
	postComment(env, task, "please retry-me")
	env.runWorker(time.Now())
	env.exec(`UPDATE openfox.automation_events SET processed_at = NULL WHERE organization_id = $1::uuid`, env.org)
	env.runWorker(time.Now())
	if n := env.runCount(comment.Automation.ID, "comment"); n != 1 {
		t.Fatalf("comment runs after retry = %d, want 1", n)
	}
}

// ---- 补充测试：权限、校验、CRUD、运行状态、Webhook 投递 ----

type runRow struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	ConversationID *string `json:"conversation_id"`
	RunID          *string `json:"run_id"`
}

type runDetailView struct {
	Status      string  `json:"status"`
	Result      *string `json:"result"`
	FailureCode string  `json:"failure_code"`
	RunID       *string `json:"run_id"`
}

// configureAgent 给 Agent 补上配置版本（派发 worker 物化 Run 所需）。
func (e *s3Env) configureAgent(agentID string) {
	e.t.Helper()
	var deviceID, grantID string
	e.scalar(&deviceID, `SELECT device_id::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, agentID)
	e.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid LIMIT 1`, e.org, deviceID)
	e.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
			runtime, model, created_by, instructions)
		VALUES ($1::uuid, $2::uuid, 1, $3::uuid, $4::uuid, 'codex', 'default', $5::uuid, 'Follow the checklist.')`,
		e.org, agentID, deviceID, grantID, s3OwnerID)
}

// materialize 以派发 worker 的组织流程把会话输入物化为 Run。
func (e *s3Env) materialize() {
	e.t.Helper()
	worker := &dispatch.Worker{Pool: e.database, Now: time.Now}
	if _, err := worker.RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatalf("RunOrganization: %v", err)
	}
}

// runOf 返回运行记录对应的 Run ID。
func (e *s3Env) runOf(recordID string) string {
	e.t.Helper()
	var runID string
	e.scalar(&runID, `SELECT ci.run_id::text FROM openfox.automation_runs ar
		JOIN openfox.conversation_inputs ci ON ci.organization_id = ar.organization_id AND ci.message_id = ar.message_id
		WHERE ar.id = $1::uuid`, recordID)
	return runID
}

// setRunState 以组织 worker 的租户上下文更新 Run（与执行器写回状态一致），结束态的通知触发器才能读到会话输入。
func (e *s3Env) setRunState(runID, state, failureCode string) {
	e.t.Helper()
	if err := e.inService(time.Now(), func(ctx context.Context, _ collaboration.Service, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.runs SET state = $2, finished_at = CASE WHEN $2 IN ('SUCCEEDED', 'FAILED', 'CANCELED') THEN clock_timestamp() ELSE NULL END,
			failure_code = $3 WHERE organization_id = $1::uuid AND id = $4::uuid`, e.org, state, failureCode, runID)
		return err
	}); err != nil {
		e.t.Fatalf("set run state: %v", err)
	}
}

func TestAutomationPermissionsAndCRUD(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	manager := env.manager()
	base := automationsPath(env.space.ID)

	// 1. 停用空间写入返回 409。
	created := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, env.space.ID)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base, automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"})))
	env.exec(`UPDATE openfox.spaces SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, env.space.ID)

	// 3. 通知人员不能读空间（other 不是私有空间成员）返回 400。
	body := automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	body["notify_user_ids"] = []string{s3OtherID}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, body)

	// 4. comment contains 为空 / 超长，webhook config 非空，均返回 400。
	for _, trigger := range []map[string]any{
		{"kind": "comment", "enabled": true, "config": map[string]any{"contains": ""}},
		{"kind": "comment", "enabled": true, "config": map[string]any{"contains": strings.Repeat("a", 201)}},
		{"kind": "webhook", "enabled": true, "config": map[string]any{"contains": "x"}},
	} {
		manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, automationBody(agent, trigger))
	}

	// 5. DELETE：版本冲突 409；正确版本 204 后不可见。
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, base+"/"+created.Automation.ID+"?version=99", nil)
	manager.must(http.StatusNoContent, http.MethodDelete, base+"/"+created.Automation.ID+"?version=1", nil, nil)
	manager.expectError(http.StatusNotFound, "", http.MethodGet, base+"/"+created.Automation.ID, nil)

	// 5. 重排：合法重排后顺序生效。
	first := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	second := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "10:00"}))
	manager.must(http.StatusNoContent, http.MethodPut, base+"/reorder",
		map[string]any{"automation_ids": []string{second.Automation.ID, first.Automation.ID}}, nil)
	var list struct {
		Automations []automationView `json:"automations"`
	}
	manager.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Automations) != 2 || list.Automations[0].ID != second.Automation.ID {
		t.Fatalf("order after reorder = %+v, want second first", list.Automations)
	}
}

func TestAutomationCrewAssignee(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	base := automationsPath(env.space.ID)
	manager := env.manager()

	// 2. 没有队长的小队：400。
	env.exec(`INSERT INTO openfox.agent_crews (organization_id, space_id, owner_user_id, name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'No leader')`, env.org, env.space.ID, s3OwnerID)
	var noLeader string
	env.scalar(&noLeader, `SELECT id::text FROM openfox.agent_crews WHERE organization_id = $1::uuid AND name = 'No leader'`, env.org)
	crewBody := func(crewID string) map[string]any {
		body := automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
		body["assignee"] = map[string]any{"kind": "CREW", "id": crewID}
		return body
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, crewBody(noLeader))

	// 2. 有队长：运行的会话使用队长 Agent。
	env.exec(`INSERT INTO openfox.agent_crews (organization_id, space_id, owner_user_id, name, leader_agent_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'With leader', $4::uuid)`, env.org, env.space.ID, s3OwnerID, agent)
	var crewID string
	env.scalar(&crewID, `SELECT id::text FROM openfox.agent_crews WHERE organization_id = $1::uuid AND name = 'With leader'`, env.org)
	var saved automationSave
	manager.must(http.StatusCreated, http.MethodPost, base, crewBody(crewID), &saved)
	var run runView
	manager.must(http.StatusCreated, http.MethodPost, base+"/"+saved.Automation.ID+"/run", nil, &run)
	if run.ConversationID == nil {
		t.Fatalf("crew run has no conversation: %+v", run)
	}
	var conversationAgent string
	env.scalar(&conversationAgent, `SELECT agent_id::text FROM openfox.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, *run.ConversationID)
	if conversationAgent != agent {
		t.Fatalf("conversation agent = %s, want leader %s", conversationAgent, agent)
	}
}

func TestAutomationRunFailureAndStatusMapping(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	env.configureAgent(agent)
	base := automationsPath(env.space.ID)
	manager := env.manager()

	// 6. 指派的 Agent 归档后运行：记为 failed 且原因非空，接口不报错。
	archived := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	var failed runView
	manager.must(http.StatusCreated, http.MethodPost, base+"/"+archived.Automation.ID+"/run", nil, &failed)
	if failed.Status != "failed" || failed.ConversationID != nil {
		t.Fatalf("run with archived agent = %+v, want failed without conversation", failed)
	}
	var reason string
	env.scalar(&reason, `SELECT reason FROM openfox.automation_runs WHERE id = $1::uuid`, failed.ID)
	if reason == "" {
		t.Fatal("failed run has empty reason")
	}

	// 9. 运行状态映射与详情：QUEUED → RUNNING → SUCCEEDED，结果截断到 4000 字；另一条运行 FAILED 带失败码。
	env.exec(`UPDATE openfox.agents SET archived_at = NULL WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	mapped := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	var queued runView
	manager.must(http.StatusCreated, http.MethodPost, base+"/"+mapped.Automation.ID+"/run", nil, &queued)
	env.materialize()
	runID := env.runOf(queued.ID)
	if runID == "" {
		t.Fatal("dispatch did not materialize the run")
	}
	detailURL := base + "/" + mapped.Automation.ID + "/runs/" + queued.ID
	var detail runDetailView
	manager.must(http.StatusOK, http.MethodGet, detailURL, nil, &detail)
	if detail.Status != "queued" {
		t.Fatalf("QUEUED run status = %s, want queued", detail.Status)
	}
	env.setRunState(runID, "RUNNING", "")
	manager.must(http.StatusOK, http.MethodGet, detailURL, nil, &detail)
	if detail.Status != "running" {
		t.Fatalf("RUNNING run status = %s, want running", detail.Status)
	}
	var conversationID string
	env.scalar(&conversationID, `SELECT conversation_id::text FROM openfox.automation_runs WHERE id = $1::uuid`, queued.ID)
	env.exec(`INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, author_agent_id, body)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 900, 'AGENT', $4::uuid, $5)`,
		env.org, s3NewID(t), conversationID, agent, strings.Repeat("好", 5000))
	env.setRunState(runID, "SUCCEEDED", "")
	manager.must(http.StatusOK, http.MethodGet, detailURL, nil, &detail)
	if detail.Status != "completed" || detail.Result == nil || utf8.RuneCountInString(*detail.Result) != 4000 {
		t.Fatalf("SUCCEEDED detail = %s result runes %d, want completed with 4000 runes", detail.Status, runeLen(detail.Result))
	}

	second := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "10:00"}))
	var failedRun runView
	manager.must(http.StatusCreated, http.MethodPost, base+"/"+second.Automation.ID+"/run", nil, &failedRun)
	env.materialize()
	env.setRunState(env.runOf(failedRun.ID), "FAILED", "RATE_LIMITED")
	var list struct {
		Runs []runRow `json:"runs"`
	}
	manager.must(http.StatusOK, http.MethodGet, base+"/"+second.Automation.ID+"/runs", nil, &list)
	if len(list.Runs) != 1 || list.Runs[0].Status != "failed" {
		t.Fatalf("list status = %+v, want one failed run", list.Runs)
	}
	manager.must(http.StatusOK, http.MethodGet, base+"/"+second.Automation.ID+"/runs/"+failedRun.ID, nil, &detail)
	if detail.Status != "failed" || detail.FailureCode != "RATE_LIMITED" {
		t.Fatalf("FAILED detail = %+v, want failed with RATE_LIMITED", detail)
	}
}

func runeLen(value *string) int {
	if value == nil {
		return -1
	}
	return utf8.RuneCountInString(*value)
}

func TestAutomationTaskStatusFilterAndCreatorRemoved(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	manager := env.manager()
	done := env.statusID(env.org, "DONE")
	created := env.createAutomation(agent, map[string]any{"kind": "task_status", "enabled": true,
		"config": map[string]any{"status_ids": []string{done}, "filters": []map[string]any{{"field": "priority", "op": "is", "value": "HIGH"}}}})

	// 8. 筛选不满足（优先级 LOW）不触发；满足（HIGH）才触发。
	low := env.createTask(manager, env.space.ID, map[string]any{"title": "Low", "priority": "LOW"})
	env.setStatus(manager, env.space.ID, low, "DONE")
	env.runWorker(time.Now())
	if n := env.runCount(created.Automation.ID, "task_status"); n != 0 {
		t.Fatalf("filter-mismatched task triggered %d runs", n)
	}
	high := env.createTask(manager, env.space.ID, map[string]any{"title": "High", "priority": "HIGH"})
	env.setStatus(manager, env.space.ID, high, "DONE")
	env.runWorker(time.Now())
	if n := env.runCount(created.Automation.ID, "task_status"); n != 1 {
		t.Fatalf("filter-matched task runs = %d, want 1", n)
	}

	// 7. 创建者被移出组织后，定时运行记为 failed（原因写明）。
	removed := env.createAutomation(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '1 hour' WHERE automation_id = $1::uuid`, removed.Automation.ID)
	env.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, s3ManagerID)
	env.runWorker(time.Now())
	var status string
	env.scalar(&status, `SELECT status FROM openfox.automation_runs WHERE automation_id = $1::uuid AND trigger_kind = 'schedule'`, removed.Automation.ID)
	if status != "failed" {
		t.Fatalf("run after creator removed = %s, want failed", status)
	}
	env.exec(`UPDATE openfox.organization_members SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, s3ManagerID)
}

func TestWebhookIgnoredProviderAndDeliveryDetail(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	base := automationsPath(env.space.ID)
	manager := env.manager()
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	trigger := created.Automation.Triggers[0].ID
	token := lastSegment(created.WebhookURLs[0].URL)
	url := env.server.URL + "/v1/hooks/" + token
	automation := base + "/" + created.Automation.ID

	// 12. GitHub 签名（X-Hub-Signature-256）+ X-GitHub-Event：202，provider 为 github。
	var wrapped struct {
		Secret string `json:"secret"`
	}
	manager.must(http.StatusOK, http.MethodPost, automation+"/triggers/"+trigger+"/signing-secret", nil, &wrapped)
	manager.must(http.StatusOK, http.MethodPatch, automation, map[string]any{"version": created.Automation.Version, "triggers": []map[string]any{{
		"id": trigger, "kind": "webhook", "enabled": true, "config": map[string]any{}, "signature_required": true,
	}}}, nil)
	body := []byte(`{"ref":"main"}`)
	github := map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-1",
		"X-Hub-Signature-256": "sha256=" + hmacHexString(wrapped.Secret, string(body)),
	}
	if status, raw := env.rawPost(url, body, github); status != http.StatusAccepted {
		t.Fatalf("GitHub signed webhook = %d %s, want 202", status, raw)
	}
	var list struct {
		Deliveries []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"deliveries"`
	}
	manager.must(http.StatusOK, http.MethodGet, automation+"/deliveries", nil, &list)
	if len(list.Deliveries) == 0 || list.Deliveries[0].Provider != "github" {
		t.Fatalf("deliveries = %+v, want github provider", list.Deliveries)
	}

	// 12. 投递详情：签名头遮挡；dispatched 可重放。
	var detail struct {
		Replayable bool              `json:"replayable"`
		Reason     *string           `json:"not_replayable_reason"`
		Headers    map[string]string `json:"headers"`
	}
	manager.must(http.StatusOK, http.MethodGet, automation+"/deliveries/"+list.Deliveries[0].ID, nil, &detail)
	if detail.Headers["X-Hub-Signature-256"] != "••••" || !detail.Replayable || detail.Reason != nil {
		t.Fatalf("delivery detail = %+v, want masked signature and replayable", detail)
	}

	// last_source_ip 已更新。
	var lastIP string
	env.scalar(&lastIP, `SELECT last_source_ip FROM openfox.automation_triggers WHERE id = $1::uuid`, trigger)
	if lastIP == "" {
		t.Fatal("last_source_ip was not updated")
	}

	// 11. 自动化停用：投递记为 ignored，返回 200。签名要求先关闭，停用判断在签名之后。
	manager.must(http.StatusOK, http.MethodPatch, automation, map[string]any{"version": currentVersion(manager, automation), "triggers": []map[string]any{{
		"id": trigger, "kind": "webhook", "enabled": true, "config": map[string]any{}, "signature_required": false,
	}}}, nil)
	manager.must(http.StatusOK, http.MethodPatch, automation, map[string]any{"version": currentVersion(manager, automation), "enabled": false}, nil)
	if status, raw := env.rawPost(url, []byte(`{"n":2}`), map[string]string{"X-OpenFox-Delivery": "off-1"}); status != http.StatusOK || !strings.Contains(string(raw), "ignored") {
		t.Fatalf("disabled automation webhook = %d %s, want 200 ignored", status, raw)
	}
	// 11. 触发器停用：投递记为 ignored。
	manager.must(http.StatusOK, http.MethodPatch, automation, map[string]any{"version": currentVersion(manager, automation), "enabled": true}, nil)
	manager.must(http.StatusOK, http.MethodPatch, automation, map[string]any{"version": currentVersion(manager, automation), "triggers": []map[string]any{{
		"id": trigger, "kind": "webhook", "enabled": false, "config": map[string]any{},
	}}}, nil)
	if status, raw := env.rawPost(url, []byte(`{"n":3}`), map[string]string{"X-OpenFox-Delivery": "off-2"}); status != http.StatusOK || !strings.Contains(string(raw), "ignored") {
		t.Fatalf("disabled trigger webhook = %d %s, want 200 ignored", status, raw)
	}
}

func TestWebhookNonUTF8BodyAsBase64(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	token := lastSegment(created.WebhookURLs[0].URL)
	automation := automationsPath(env.space.ID) + "/" + created.Automation.ID
	payload := []byte{0xff, 0xfe, 0x00, 0x81}
	if status, raw := env.rawPost(env.server.URL+"/v1/hooks/"+token, payload, map[string]string{"X-OpenFox-Delivery": "bin-1"}); status != http.StatusAccepted {
		t.Fatalf("binary webhook = %d %s, want 202", status, raw)
	}
	var list struct {
		Deliveries []struct {
			ID string `json:"id"`
		} `json:"deliveries"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, automation+"/deliveries", nil, &list)
	var detail struct {
		Body string `json:"body"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, automation+"/deliveries/"+list.Deliveries[0].ID, nil, &detail)
	if detail.Body != base64.StdEncoding.EncodeToString(payload) {
		t.Fatalf("body = %q, want base64 of binary payload", detail.Body)
	}
}

func TestWebhookRejectedDeliveryNotReplayable(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	token := lastSegment(created.WebhookURLs[0].URL)
	automation := automationsPath(env.space.ID) + "/" + created.Automation.ID
	// 来源 127.0.0.1 不在白名单内，投递记为 rejected（ip_blocked）。
	env.manager().must(http.StatusOK, http.MethodPatch, automationsPath(env.space.ID)+"/"+created.Automation.ID, map[string]any{"version": created.Automation.Version, "triggers": []map[string]any{{
		"id": created.Automation.Triggers[0].ID, "kind": "webhook", "enabled": true, "config": map[string]any{}, "ip_allowlist": []string{"203.0.113.0/24"},
	}}}, nil)
	env.rawPost(env.server.URL+"/v1/hooks/"+token, []byte("{}"), map[string]string{"X-OpenFox-Delivery": "x-1"})
	var list struct {
		Deliveries []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, automation+"/deliveries", nil, &list)
	var detail struct {
		Replayable bool    `json:"replayable"`
		Reason     *string `json:"not_replayable_reason"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, automation+"/deliveries/"+list.Deliveries[0].ID, nil, &detail)
	if list.Deliveries[0].Status != "rejected" || detail.Replayable || detail.Reason == nil || *detail.Reason != "rejected" {
		t.Fatalf("rejected detail = %+v, want not replayable with reason rejected", detail)
	}
}

// currentVersion 读取自动化当前版本，供连续 PATCH 使用。
func currentVersion(client *s3Client, automationPath string) uint64 {
	client.env.t.Helper()
	var item automationView
	client.must(http.StatusOK, http.MethodGet, automationPath, nil, &item)
	return item.Version
}

func TestAutomationResultNotification(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	env.configureAgent(agent)
	body := automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	body["notify_user_ids"] = []string{s3MemberID}
	base := automationsPath(env.space.ID)
	var saved automationSave
	env.manager().must(http.StatusCreated, http.MethodPost, base, body, &saved)
	var run runView
	env.manager().must(http.StatusCreated, http.MethodPost, base+"/"+saved.Automation.ID+"/run", nil, &run)
	if run.ConversationID == nil {
		t.Fatalf("manual run has no conversation: %+v", run)
	}
	// A skipped attempt has no conversation, but still has a visible automation target.
	var skip runView
	env.manager().must(201, "POST", base+"/"+saved.Automation.ID+"/run", nil, &skip)
	if skip.Status != "skipped" {
		t.Fatal(skip)
	}
	var skipped struct {
		Groups []struct {
			AutomationID   string `json:"automation_id"`
			ConversationID string `json:"conversation_id"`
		}
	}
	env.member().must(200, "GET", "inbox?importance=primary", nil, &skipped)
	if len(skipped.Groups) != 1 || skipped.Groups[0].AutomationID != saved.Automation.ID || skipped.Groups[0].ConversationID != "" {
		t.Fatalf("skipped target: %+v", skipped)
	}
	env.member().must(200, "PATCH", "/v1/me/inbox-preferences", map[string]string{"key": "automation_run_completed_recipient", "level": "normal"}, nil)
	env.materialize()
	env.setRunState(env.runOf(run.ID), "SUCCEEDED", "")

	// 通知人员可以打开会话，并在收件箱看到 AUTOMATION_RESULT。
	conversation := "conversations/" + *run.ConversationID
	env.member().must(http.StatusOK, http.MethodGet, conversation, nil, nil)
	var inbox json.RawMessage
	env.member().must(http.StatusOK, http.MethodGet, "inbox?importance=other", nil, &inbox)
	if !strings.Contains(string(inbox), "AUTOMATION_RESULT") {
		t.Fatalf("notified member inbox has no AUTOMATION_RESULT: %s", inbox)
	}

	// 非通知人员既打不开会话，收件箱也没有该通知。
	env.other().expectError(http.StatusNotFound, "", http.MethodGet, conversation, nil)
	var otherInbox json.RawMessage
	env.other().must(http.StatusOK, http.MethodGet, "inbox", nil, &otherInbox)
	if strings.Contains(string(otherInbox), "AUTOMATION_RESULT") {
		t.Fatalf("non-notified user has AUTOMATION_RESULT: %s", otherInbox)
	}
}
