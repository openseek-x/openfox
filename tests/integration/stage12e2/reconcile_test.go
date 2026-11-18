package stage12e2

// 阶段 12e：待核对面板所需接口（证据列表、「接受已有结果」）与电脑「核对中」计数的集成测试。
// Run、核对案与证据直接写库（不启动派发器与 daemon），人工裁决与设备上报走真实处理器与存储层。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/reconciliation"
	"openfox/internal/tenancy"
)

type caseView struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	Status          string `json:"status"`
	Resolution      string `json:"resolution"`
	ResolvedBy      string `json:"resolved_by"`
	Version         int64  `json:"version"`
	AlreadyResolved bool   `json:"already_resolved"`
}

type evidenceView struct {
	ID              string    `json:"id"`
	CaseID          string    `json:"case_id"`
	Kind            string    `json:"kind"`
	Source          string    `json:"source"`
	ProcessState    string    `json:"process_state"`
	AcceptedState   string    `json:"accepted_state"`
	Detail          string    `json:"detail"`
	ObservedAt      time.Time `json:"observed_at"`
	CanAcceptResult bool      `json:"can_accept_result"`
}

func TestReconciliationEvidenceListIsAdminOnlyAndRedacted(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	item := fixture.seedReconciling()
	uncertain := item.seedEvidence("PROCESS_NOT_FOUND", "ACCEPTED", false, "pid may have been reused")
	confirmed := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result uploaded before restart")
	stale := item.seedEvidence("RESULT_CONFIRMED", "STALE", true, "old supervisor")
	noResult := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", false, "no result id")

	for _, client := range []*qcClient{env.member(), env.guest()} {
		response := client.do(http.MethodGet, item.path("/evidence"), nil)
		if response.Status == http.StatusOK || strings.Contains(string(response.Body), "pid may have been reused") {
			t.Fatalf("non-admin evidence = %d %s", response.Status, response.Body)
		}
	}
	env.member().expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, item.path("/evidence"), nil)
	if response := env.outsider().do(http.MethodGet, item.path("/evidence"), nil); response.Status == http.StatusOK {
		t.Fatalf("outsider evidence = %s", response.Body)
	}
	// 组织管理员若看不到该 Run（会话属于他人）：按不存在处理（此前落入 500），不泄露证据。
	env.admin().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, item.path("/evidence"), nil)
	env.admin().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, item.path("/"+item.caseID+"/resolve"),
		map[string]any{"version": 1, "resolution": "RESULT_ACCEPTED", "evidence_id": confirmed})

	var listed struct {
		Evidence []evidenceView `json:"evidence"`
	}
	response := env.owner().must(http.StatusOK, http.MethodGet, item.path("/evidence"), nil, &listed)
	for _, forbidden := range []string{"nonce-SECRET", "supervisor_nonce", "payload_hash", "4242", "process_pid", "result_id", "device_generation"} {
		if strings.Contains(string(response.Body), forbidden) {
			t.Fatalf("evidence response leaked %q: %s", forbidden, response.Body)
		}
	}
	byID := map[string]evidenceView{}
	for _, evidence := range listed.Evidence {
		byID[evidence.ID] = evidence
	}
	if len(byID) != 4 {
		t.Fatalf("evidence = %+v", listed.Evidence)
	}
	if got := byID[uncertain]; got.Kind != "PROCESS_NOT_FOUND" || got.Source != "DAEMON" || got.Detail != "pid may have been reused" ||
		got.CaseID != item.caseID || got.ObservedAt.IsZero() || got.CanAcceptResult {
		t.Fatalf("uncertain = %+v", got)
	}
	// 只有「已受理 + 结果确认 + 带结果 + 案仍开放」才允许接受已有结果。
	if !byID[confirmed].CanAcceptResult || byID[stale].CanAcceptResult || byID[noResult].CanAcceptResult {
		t.Fatalf("can_accept_result: confirmed=%v stale=%v noResult=%v",
			byID[confirmed].CanAcceptResult, byID[stale].CanAcceptResult, byID[noResult].CanAcceptResult)
	}
}

func TestAcceptExistingResult(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	item := fixture.seedReconciling()
	uncertain := item.seedEvidence("PROCESS_NOT_FOUND", "ACCEPTED", false, "uncertain")
	confirmed := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result confirmed")
	accept := func(evidence string, version int64) map[string]any {
		return map[string]any{"version": version, "resolution": "RESULT_ACCEPTED", "evidence_id": evidence}
	}

	// 非管理员被拒。
	env.member().expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, item.path("/"+item.caseID+"/resolve"), accept(confirmed, 1))
	// 证据不支持该裁决（不是结果确认类）：拒绝，案保持开放。
	env.owner().expectError(http.StatusConflict, "RECONCILIATION_CONFLICT", http.MethodPost, item.path("/"+item.caseID+"/resolve"), accept(uncertain, 1))
	// 版本号过期。
	env.owner().expectError(http.StatusConflict, "RECONCILIATION_CONFLICT", http.MethodPost, item.path("/"+item.caseID+"/resolve"), accept(confirmed, 9))
	if state, reconciling := item.runState(); state != "RUNNING" || !reconciling || item.resolutionAudits() != 0 {
		t.Fatalf("rejected resolutions changed state: %s %v audits=%d", state, reconciling, item.resolutionAudits())
	}

	var resolved caseView
	env.owner().must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/resolve"), accept(confirmed, 1), &resolved)
	if resolved.AlreadyResolved || resolved.Status != "RESOLVED" || resolved.Resolution != "RESULT_ACCEPTED" ||
		resolved.ResolvedBy != qcOwnerID || resolved.Version != 2 {
		t.Fatalf("resolved = %+v", resolved)
	}
	// 接受结果只清除待核对标记，不改变 Run 状态（由 daemon 随后提交结果收尾）。
	if state, reconciling := item.runState(); state != "RUNNING" || reconciling {
		t.Fatalf("run after accept = %s reconciling=%v", state, reconciling)
	}
	if item.resolutionAudits() != 1 {
		t.Fatalf("audits = %d, want 1", item.resolutionAudits())
	}
	// 用旧版本号再点一次（界面未刷新时的重复提交）：返回已处理，不再写审计、不改变结果。
	var again caseView
	env.owner().must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/resolve"), accept(confirmed, 1), &again)
	if !again.AlreadyResolved || again.ResolvedBy != qcOwnerID || again.Version != 2 || item.resolutionAudits() != 1 {
		t.Fatalf("second resolve = %+v audits=%d", again, item.resolutionAudits())
	}
	// 案已关闭后证据不再可接受。
	var listed struct {
		Evidence []evidenceView `json:"evidence"`
	}
	env.owner().must(http.StatusOK, http.MethodGet, item.path("/evidence"), nil, &listed)
	for _, evidence := range listed.Evidence {
		if evidence.CanAcceptResult {
			t.Fatalf("evidence %s still acceptable after the case closed", evidence.ID)
		}
	}
}

// 两个请求同时处理同一个案：一个「接受已有结果」，一个「标记为失败」，恰有一方生效。
// （人工裁决要求能看到该 Run 的会话，所以两个请求都由会话所有者发出。）
func TestAcceptResultRacesWithMarkFailed(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	for round := 0; round < 4; round++ {
		item := fixture.seedReconciling()
		confirmed := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result confirmed")
		owner, admin := env.owner(), env.owner()
		var accept, failed qcResponse
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			accept = owner.do(http.MethodPost, item.path("/"+item.caseID+"/resolve"),
				map[string]any{"version": 1, "resolution": "RESULT_ACCEPTED", "evidence_id": confirmed})
		}()
		go func() {
			defer wait.Done()
			failed = admin.do(http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 1})
		}()
		wait.Wait()
		if accept.Status != http.StatusOK || failed.Status != http.StatusOK {
			t.Fatalf("round %d: accept = %d %s, mark-failed = %d %s", round, accept.Status, accept.Body, failed.Status, failed.Body)
		}
		var accepted caseView
		accept.decode(t, &accepted)
		var marked struct {
			Case            caseView `json:"case"`
			AlreadyResolved bool     `json:"already_resolved"`
		}
		failed.decode(t, &marked)
		if accepted.AlreadyResolved == marked.AlreadyResolved {
			t.Fatalf("round %d: exactly one side must win: accept=%+v mark=%+v", round, accepted, marked)
		}
		state, reconciling := item.runState()
		var resolution string
		env.scalar(&resolution, `SELECT resolution FROM openfox.run_reconciliation_cases WHERE id = $1::uuid`, item.caseID)
		switch {
		case !accepted.AlreadyResolved:
			if resolution != "RESULT_ACCEPTED" || state != "RUNNING" || reconciling {
				t.Fatalf("round %d accept won: resolution=%s state=%s reconciling=%v", round, resolution, state, reconciling)
			}
		default:
			if resolution != "PROCESS_EXITED_FAILED" || state != "FAILED" || reconciling {
				t.Fatalf("round %d mark-failed won: resolution=%s state=%s reconciling=%v", round, resolution, state, reconciling)
			}
		}
		if item.resolutionAudits() != 1 {
			t.Fatalf("round %d: audits = %d, want exactly 1", round, item.resolutionAudits())
		}
	}
}

// 管理员「接受已有结果」与 daemon 的确定证据（进程不存在）同时到达：恰有一方裁决，Run 不会既被接受又被判失败。
func TestAcceptResultRacesWithDaemonEvidence(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	device := fixture.device()
	deviceTx := func(fn func(context.Context, pgx.Tx) error) error {
		selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: device, AuthorizationVersion: 1}
		return env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceDeviceRequest,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
				return selection, nil
			}), fn)
	}
	var deviceGeneration int64
	env.scalar(&deviceGeneration, `SELECT generation FROM openfox.physical_devices WHERE id = $1::uuid`, device)
	for round := 0; round < 4; round++ {
		item := fixture.seedReconciling()
		confirmed := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result confirmed")
		owner := env.owner()
		var accept qcResponse
		var report dispatch.ProcessReportResult
		var reportErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			accept = owner.do(http.MethodPost, item.path("/"+item.caseID+"/resolve"),
				map[string]any{"version": 1, "resolution": "RESULT_ACCEPTED", "evidence_id": confirmed})
		}()
		go func() {
			defer wait.Done()
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
		if reportErr != nil {
			t.Fatalf("round %d: daemon report: %v", round, reportErr)
		}
		if accept.Status != http.StatusOK {
			t.Fatalf("round %d: accept = %d %s", round, accept.Status, accept.Body)
		}
		var accepted caseView
		accept.decode(t, &accepted)
		state, reconciling := item.runState()
		var resolution string
		env.scalar(&resolution, `SELECT resolution FROM openfox.run_reconciliation_cases WHERE id = $1::uuid`, item.caseID)
		daemonWon := report.Status == dispatch.ReportResolvedFailed
		if daemonWon == !accepted.AlreadyResolved {
			t.Fatalf("round %d: exactly one side must win: report=%+v accept=%+v", round, report, accepted)
		}
		if daemonWon {
			if resolution != "PROCESS_EXITED_FAILED" || state != "FAILED" || reconciling || item.resolutionAudits() != 0 {
				t.Fatalf("round %d daemon won: resolution=%s state=%s reconciling=%v audits=%d", round, resolution, state, reconciling, item.resolutionAudits())
			}
		} else {
			// 管理员先生效：案已关闭、标记已清除；daemon 随后的证据因没有开放案而被幂等忽略，Run 不被判失败。
			if resolution != "RESULT_ACCEPTED" || state != "RUNNING" || reconciling || item.resolutionAudits() != 1 {
				t.Fatalf("round %d accept won: resolution=%s state=%s reconciling=%v audits=%d report=%+v",
					round, resolution, state, reconciling, item.resolutionAudits(), report)
			}
		}
	}
}

type deviceView struct {
	ID              string `json:"id"`
	ReconcilingRuns int    `json:"reconciling_runs"`
}

// 电脑列表与详情的「核对中」计数：只计本组织、该电脑、未结束且待核对的 Run。
func TestDeviceReconcilingRunCount(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	other := env.newRunFixture()
	device, otherDevice := fixture.device(), other.device()
	count := func(client *qcClient, id string) (listed, detail int) {
		t.Helper()
		var list struct {
			Devices []deviceView `json:"devices"`
		}
		client.must(http.StatusOK, http.MethodGet, "devices", nil, &list)
		listed = -1
		for _, item := range list.Devices {
			if item.ID == id {
				listed = item.ReconcilingRuns
			}
		}
		var one deviceView
		client.must(http.StatusOK, http.MethodGet, "devices/"+id, nil, &one)
		return listed, one.ReconcilingRuns
	}
	if listed, detail := count(env.owner(), device); listed != 0 || detail != 0 {
		t.Fatalf("initial = %d/%d", listed, detail)
	}
	first := fixture.seedReconciling()
	fixture.seedReconciling()
	fixture.seedRun("RUNNING", false) // 运行中但不需要核对
	fixture.seedRun("QUEUED", false)
	// 已结束的 Run 即使残留标记也不计。
	ended := fixture.seedRun("RUNNING", true)
	env.exec(`UPDATE openfox.runs SET state = 'FAILED', finished_at = clock_timestamp(), failure_code = 'UNKNOWN_PROCESS' WHERE id = $1::uuid`, ended)
	if listed, detail := count(env.owner(), device); listed != 2 || detail != 2 {
		t.Fatalf("reconciling = %d/%d, want 2/2", listed, detail)
	}
	if listed, detail := count(env.owner(), otherDevice); listed != 0 || detail != 0 {
		t.Fatalf("other device = %d/%d, want 0/0", listed, detail)
	}
	// 管理员处理一个后计数减少。
	env.owner().must(http.StatusOK, http.MethodPost, first.path("/"+first.caseID+"/mark-failed"), map[string]any{"version": 1}, nil)
	if listed, detail := count(env.owner(), device); listed != 1 || detail != 1 {
		t.Fatalf("after resolve = %d/%d, want 1/1", listed, detail)
	}
}
