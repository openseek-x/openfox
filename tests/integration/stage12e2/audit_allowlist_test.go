package stage12e2

// 12a 缺陷的回归测试：核对案的人工裁决接口（mark-failed、resolve）经 HTTP 调用时要写审计，
// 审计元数据键 resolution、evidence_id 此前不在 internal/audit 的白名单里，audit.Record 校验失败，
// 事务回滚并返回 500——界面上的「标记为失败」实际不可用。12a 的集成测试直接调存储层，没有经过 HTTP。
// 这里走真实处理器，断言接口成功、Run 与核对案按预期变化、审计行带这两个键。

import (
	"net/http"
	"testing"
)

type resolutionAudit struct {
	actor, resolution, evidenceID string
}

// resolutionAudit 读取核对案的裁决审计行；没有或多于一条都算失败。
func (r reconcilingRun) resolutionAudit() resolutionAudit {
	e := r.fixture.env
	e.t.Helper()
	if got := r.resolutionAudits(); got != 1 {
		e.t.Fatalf("resolution audit rows = %d, want 1", got)
	}
	var row resolutionAudit
	if err := e.superuser.QueryRow(e.ctx, `SELECT actor_id::text, COALESCE(safe_metadata->>'resolution', ''),
			COALESCE(safe_metadata->>'evidence_id', '')
		 FROM openfox.audit_events WHERE organization_id = $1::uuid
		  AND action = 'run.reconciliation_resolved' AND resource_kind = 'reconciliation_case' AND resource_id = $2`,
		e.org, r.caseID).Scan(&row.actor, &row.resolution, &row.evidenceID); err != nil {
		e.t.Fatal(err)
	}
	return row
}

func TestMarkFailedOverHTTPWritesAudit(t *testing.T) {
	env := newQcEnv(t)
	item := env.newRunFixture().seedReconciling()
	var marked struct {
		Case struct {
			Status               string `json:"status"`
			Resolution           string `json:"resolution"`
			ResolutionEvidenceID string `json:"resolution_evidence_id"`
		} `json:"case"`
		AlreadyResolved bool `json:"already_resolved"`
	}
	env.owner().must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 1}, &marked)
	if marked.AlreadyResolved || marked.Case.Status != "RESOLVED" || marked.Case.Resolution != "PROCESS_EXITED_FAILED" {
		t.Fatalf("marked = %+v", marked)
	}
	if state, reconciling := item.runState(); state != "FAILED" || reconciling {
		t.Fatalf("run = %s reconciling = %v, want FAILED and cleared", state, reconciling)
	}
	audit := item.resolutionAudit()
	if audit.actor != qcOwnerID || audit.resolution != "PROCESS_EXITED_FAILED" ||
		audit.evidenceID == "" || audit.evidenceID != marked.Case.ResolutionEvidenceID {
		t.Fatalf("audit = %+v, case evidence = %s", audit, marked.Case.ResolutionEvidenceID)
	}
	// 再点一次：返回已处理，不重复写审计。
	env.owner().must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/mark-failed"), map[string]any{"version": 1}, &marked)
	if !marked.AlreadyResolved || item.resolutionAudits() != 1 {
		t.Fatalf("second mark-failed = %+v audits = %d", marked, item.resolutionAudits())
	}
}

func TestResolveOverHTTPWritesAudit(t *testing.T) {
	env := newQcEnv(t)
	item := env.newRunFixture().seedReconciling()
	confirmed := item.seedEvidence("RESULT_CONFIRMED", "ACCEPTED", true, "result confirmed")
	var resolved struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
		ResolvedBy string `json:"resolved_by"`
	}
	env.owner().must(http.StatusOK, http.MethodPost, item.path("/"+item.caseID+"/resolve"),
		map[string]any{"version": 1, "resolution": "RESULT_ACCEPTED", "evidence_id": confirmed}, &resolved)
	if resolved.Status != "RESOLVED" || resolved.Resolution != "RESULT_ACCEPTED" || resolved.ResolvedBy != qcOwnerID {
		t.Fatalf("resolved = %+v", resolved)
	}
	if state, reconciling := item.runState(); state != "RUNNING" || reconciling {
		t.Fatalf("run = %s reconciling = %v, want RUNNING and cleared", state, reconciling)
	}
	if audit := item.resolutionAudit(); audit.actor != qcOwnerID || audit.resolution != "RESULT_ACCEPTED" || audit.evidenceID != confirmed {
		t.Fatalf("audit = %+v", audit)
	}
}
