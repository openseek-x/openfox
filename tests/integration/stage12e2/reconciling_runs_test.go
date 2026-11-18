package stage12e2

// 阶段 12e：管理员在电脑页处理待核对的运行。普通成员的私有会话卡在「待核对」时，
// 会话外的组织管理员看不到该会话；这里验证他仍能列出并把运行标记为失败，且拿不到任何会话内容。

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/reconciliation"
	"openfox/internal/tenancy"
)

type pendingRunView struct {
	RunID        string    `json:"run_id"`
	ComputerID   string    `json:"computer_id"`
	ComputerName string    `json:"computer_name"`
	AgentName    string    `json:"agent_name"`
	ActorName    string    `json:"actor_name"`
	PendingSince time.Time `json:"pending_since"`
	ReasonCode   string    `json:"reason_code"`
	CaseID       string    `json:"case_id"`
	CaseVersion  int64     `json:"case_version"`
}

func (c *qcClient) pendingRuns(query string) ([]pendingRunView, qcResponse) {
	c.env.t.Helper()
	var listed struct {
		Runs []pendingRunView `json:"runs"`
	}
	response := c.must(http.StatusOK, http.MethodGet, "reconciling-runs"+query, nil, &listed)
	if listed.Runs == nil {
		c.env.t.Fatalf("runs must be an array, got %s", response.Body)
	}
	return listed.Runs, response
}

// conversationOf 返回 Run 所在的会话 ID（用于断言响应不含会话信息）。
func (r reconcilingRun) conversationOf() string {
	e := r.fixture.env
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT conversation_id::text FROM openfox.runs WHERE id = $1::uuid`, r.run)
	return id
}

// assertNoConversationContent 断言响应体不含会话 ID、消息正文、会话标题与空间信息。
func (r reconcilingRun) assertNoConversationContent(t *testing.T, what string, body []byte) {
	t.Helper()
	e := r.fixture.env
	var title, spaceName string
	e.scalar(&title, `SELECT COALESCE(title, '') FROM openfox.conversations WHERE id = $1::uuid`, r.conversationOf())
	e.scalar(&spaceName, `SELECT name FROM openfox.spaces WHERE id = $1::uuid`, r.fixture.space)
	for _, forbidden := range []string{r.conversationOf(), runMessageBody, r.fixture.space, spaceName, "conversation", "title", "message", "task"} {
		if forbidden != "" && strings.Contains(string(body), forbidden) {
			t.Fatalf("%s leaked %q: %s", what, forbidden, body)
		}
	}
	if title != "" && strings.Contains(string(body), title) {
		t.Fatalf("%s leaked the conversation title %q: %s", what, title, body)
	}
}

func TestAdminOutsideConversationListsAndMarksFailed(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	other := env.newRunFixture()
	item := fixture.seedReconciling()
	elsewhere := other.seedReconciling()
	fixture.seedRun("RUNNING", false) // 运行中但不需要核对，不应出现在列表里
	env.exec(`UPDATE openfox.users SET full_name = '发起人甲' WHERE id = $1::uuid`, qcOwnerID)
	admin := env.admin()

	// 前提：组织管理员不在该会话里，看不到这个 Run，也不能「接受已有结果」。
	admin.expectError(http.StatusNotFound, "", http.MethodGet, "runs/"+item.run, nil)
	admin.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, item.path(""), nil)

	runs, response := admin.pendingRuns("?device_id=" + fixture.device())
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	got := runs[0]
	var agentName, deviceName string
	env.scalar(&agentName, `SELECT name FROM openfox.agents WHERE id = $1::uuid`, fixture.agent)
	env.scalar(&deviceName, `SELECT name FROM openfox.physical_devices WHERE id = $1::uuid`, fixture.device())
	if got.RunID != item.run || got.ComputerID != fixture.device() || got.ComputerName != deviceName || got.AgentName != agentName ||
		got.ActorName != "发起人甲" || got.ReasonCode != "LEASE_EXPIRED" || got.CaseID != item.caseID || got.CaseVersion != 1 ||
		got.PendingSince.IsZero() {
		t.Fatalf("pending run = %+v", got)
	}
	item.assertNoConversationContent(t, "list", response.Body)
	// 响应字段是固定的一组运维字段。
	var raw struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	response.decode(t, &raw)
	keys := make([]string, 0)
	for key := range raw.Runs[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "actor_name,agent_name,case_id,case_version,computer_id,computer_name,pending_since,reason_code,run_id" {
		t.Fatalf("fields = %v", keys)
	}
	// 不带 device_id：全组织；按另一台电脑筛选只看到那一条；非法 device_id 被拒。
	if all, _ := admin.pendingRuns(""); len(all) != 2 {
		t.Fatalf("organization-wide = %+v", all)
	}
	if only, _ := admin.pendingRuns("?device_id=" + other.device()); len(only) != 1 || only[0].RunID != elsewhere.run {
		t.Fatalf("other device = %+v", only)
	}
	if none, _ := admin.pendingRuns("?device_id=" + qcNewID(t)); len(none) != 0 {
		t.Fatalf("unknown device = %+v", none)
	}
	admin.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "reconciling-runs?device_id=not-a-uuid", nil)

	// 标记为失败：不要求会话可见性；版本号错误被拒且不改状态。
	admin.expectError(http.StatusConflict, "RECONCILIATION_CONFLICT", http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 7})
	if state, reconciling := item.runState(); state != "RUNNING" || !reconciling {
		t.Fatalf("stale version changed the run: %s %v", state, reconciling)
	}
	var marked struct {
		Case            caseView `json:"case"`
		AlreadyResolved bool     `json:"already_resolved"`
	}
	result := admin.must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": got.CaseVersion}, &marked)
	if marked.AlreadyResolved || marked.Case.Resolution != "PROCESS_EXITED_FAILED" || marked.Case.ResolvedBy != qcOrgAdminID {
		t.Fatalf("marked = %+v", marked)
	}
	item.assertNoConversationContent(t, "mark-failed", result.Body)
	if state, reconciling := item.runState(); state != "FAILED" || reconciling {
		t.Fatalf("run = %s reconciling = %v", state, reconciling)
	}
	// 对话恢复可输入：输入已消费，没有活动 Run；不自动重试（没有新 Run）。
	if got := env.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id = $1::uuid AND status <> 'CONSUMED'`, item.conversationOf()); got != 0 {
		t.Fatalf("unconsumed inputs = %d", got)
	}
	if got := env.count(`SELECT count(*) FROM openfox.runs WHERE conversation_id = $1::uuid`, item.conversationOf()); got != 1 {
		t.Fatalf("runs in conversation = %d, want 1 (no automatic retry)", got)
	}
	// 审计记在操作的管理员名下。
	if audit := item.resolutionAudit(); audit.actor != qcOrgAdminID || audit.resolution != "PROCESS_EXITED_FAILED" {
		t.Fatalf("audit = %+v", audit)
	}
	// 列表刷新后不再包含它；再点一次得到「已处理」，不重复写审计。
	if rest, _ := admin.pendingRuns("?device_id=" + fixture.device()); len(rest) != 0 {
		t.Fatalf("after mark-failed = %+v", rest)
	}
	admin.must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": got.CaseVersion}, &marked)
	if !marked.AlreadyResolved || item.resolutionAudits() != 1 {
		t.Fatalf("second = %+v audits = %d", marked, item.resolutionAudits())
	}
	// 「接受已有结果」不放开：会话外的管理员对另一条待核对运行调 resolve 仍是 404。
	confirmed := elsewhere.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result confirmed")
	admin.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, elsewhere.path("/"+elsewhere.caseID+"/resolve"),
		map[string]any{"version": 1, "resolution": "RESULT_ACCEPTED", "evidence_id": confirmed})
	if state, reconciling := elsewhere.runState(); state != "RUNNING" || !reconciling {
		t.Fatalf("resolve by an outside admin changed the run: %s %v", state, reconciling)
	}
}

func TestReconcilingRunsDeniedToNonAdminsAndOtherOrganizations(t *testing.T) {
	env := newQcEnv(t)
	item := env.newRunFixture().seedReconciling()
	failBody := map[string]any{"version": 1}
	for name, client := range map[string]*qcClient{"member": env.member(), "guest": env.guest(), "space admin": env.client(qcAdminID)} {
		response := client.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, "reconciling-runs", nil)
		if strings.Contains(string(response.Body), item.run) {
			t.Fatalf("%s list leaked the run: %s", name, response.Body)
		}
		client.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), failBody)
	}
	// 其他组织的所有者：访问本组织路径被拒；在自己的组织路径下用本组织的 Run 与案 ID 也找不到。
	outsider := env.outsider()
	if response := outsider.do(http.MethodGet, "reconciling-runs", nil); response.Status == http.StatusOK || strings.Contains(string(response.Body), item.run) {
		t.Fatalf("outsider list = %d %s", response.Status, response.Body)
	}
	if response := outsider.do(http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), failBody); response.Status == http.StatusOK {
		t.Fatalf("outsider mark-failed on this organization = %s", response.Body)
	}
	foreign := "/v1/organizations/" + env.otherOrg
	var own struct {
		Runs []pendingRunView `json:"runs"`
	}
	outsider.must(http.StatusOK, http.MethodGet, foreign+"/reconciling-runs", nil, &own)
	if len(own.Runs) != 0 {
		t.Fatalf("other organization sees %+v", own.Runs)
	}
	outsider.must(http.StatusOK, http.MethodGet, foreign+"/reconciling-runs?device_id="+item.fixture.device(), nil, &own)
	if len(own.Runs) != 0 {
		t.Fatalf("other organization sees this device's runs: %+v", own.Runs)
	}
	outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost,
		foreign+"/runs/"+item.run+"/reconciliation/"+item.caseID+"/mark-failed", failBody)
	// 非法 ID 是 400 而不是 500。
	env.owner().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "runs/not-a-uuid/reconciliation/"+item.caseID+"/mark-failed", failBody)
	// 未登录。
	anonymous, err := http.Get(env.server.URL + "/v1/organizations/" + env.org + "/reconciling-runs")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", anonymous.StatusCode)
	}
	if state, reconciling := item.runState(); state != "RUNNING" || !reconciling || item.resolutionAudits() != 0 {
		t.Fatalf("rejected requests changed the run: %s %v audits=%d", state, reconciling, item.resolutionAudits())
	}
}

// 会话外的管理员「标记为失败」与 daemon 的确定证据（进程不存在）同时到达：恰有一方裁决，
// Run 只收尾一次（run.finished 只入队一次），审计只在管理员生效时写一条。
func TestOutsideAdminMarkFailedRacesWithDaemonEvidence(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	device := fixture.device()
	var deviceGeneration int64
	env.scalar(&deviceGeneration, `SELECT generation FROM openfox.physical_devices WHERE id = $1::uuid`, device)
	deviceTx := func(fn func(context.Context, pgx.Tx) error) error {
		selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: device, AuthorizationVersion: 1}
		return env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceDeviceRequest,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
				return selection, nil
			}), fn)
	}
	admin := env.admin()
	adminWins, daemonWins := 0, 0
	for round := 0; round < 6; round++ {
		item := fixture.seedReconciling()
		var failed qcResponse
		var report dispatch.ProcessReportResult
		var reportErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			failed = admin.do(http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 1})
		}()
		go func() {
			defer wait.Done()
			// HTTP 请求比进程内调用慢；奇数轮让设备上报稍晚出发，使两种先后顺序都有机会出现。
			if round%2 == 1 {
				time.Sleep(8 * time.Millisecond)
			}
			evidenceID := qcNewID(t)
			reportErr = deviceTx(func(ctx context.Context, tx pgx.Tx) error {
				var err error
				report, err = store.ReportProcess(ctx, tx, env.org, device, deviceGeneration, item.run, dispatch.ProcessReport{
					EvidenceID: evidenceID, Generation: 1, Kind: reconciliation.EvidenceProcessNotFound, Certain: true,
					PayloadHash: hashOf(evidenceID), ObservedAt: time.Now().UTC(),
				})
				return err
			})
		}()
		wait.Wait()
		if reportErr != nil || failed.Status != http.StatusOK {
			t.Fatalf("round %d: report err = %v, mark-failed = %d %s", round, reportErr, failed.Status, failed.Body)
		}
		var marked struct {
			AlreadyResolved bool `json:"already_resolved"`
		}
		failed.decode(t, &marked)
		daemonWon := report.Status == dispatch.ReportResolvedFailed
		if daemonWon == !marked.AlreadyResolved {
			t.Fatalf("round %d: exactly one side must win: report = %+v already_resolved = %v", round, report, marked.AlreadyResolved)
		}
		if state, reconciling := item.runState(); state != "FAILED" || reconciling {
			t.Fatalf("round %d: run = %s reconciling = %v", round, state, reconciling)
		}
		if got := env.count(`SELECT count(*) FROM openfox.execution_outbox WHERE organization_id = $1::uuid
			AND kind = 'run.finished' AND aggregate_id = $2::uuid`, env.org, item.run); got != 1 {
			t.Fatalf("round %d: run.finished enqueued %d times, want 1", round, got)
		}
		wantAudits := 1
		if daemonWon {
			wantAudits = 0
			daemonWins++
		} else {
			adminWins++
		}
		if got := item.resolutionAudits(); got != wantAudits {
			t.Fatalf("round %d: audits = %d, want %d (daemon won = %v)", round, got, wantAudits, daemonWon)
		}
	}
	t.Logf("admin won %d rounds, daemon won %d rounds", adminWins, daemonWins)
	// 确定顺序：管理员先处理，设备的证据随后才到——迟到的证据被幂等忽略，Run 不被再次收尾。
	item := fixture.seedReconciling()
	admin.must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 1}, nil)
	var late dispatch.ProcessReportResult
	evidenceID := qcNewID(t)
	if err := deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		late, err = store.ReportProcess(ctx, tx, env.org, device, deviceGeneration, item.run, dispatch.ProcessReport{
			EvidenceID: evidenceID, Generation: 1, Kind: reconciliation.EvidenceProcessNotFound, Certain: true,
			PayloadHash: hashOf(evidenceID), ObservedAt: time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if late.Status != dispatch.ReportIgnored {
		t.Fatalf("late daemon evidence = %+v, want IGNORED", late)
	}
	if got := env.count(`SELECT count(*) FROM openfox.execution_outbox WHERE organization_id = $1::uuid
AND kind = 'run.finished' AND aggregate_id = $2::uuid`, env.org, item.run); got != 1 || item.resolutionAudits() != 1 {
		t.Fatalf("after late evidence: run.finished = %d audits = %d, want 1 and 1", got, item.resolutionAudits())
	}
}
