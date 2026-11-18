package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/reconciliation"
)

var reconcileStore = reconciliation.PGStore{}

// 12a：daemon 进程核对的中心端自动裁决。
//
// 证据确定（进程不存在、已退出）时在同一事务内把 Run 收尾并释放对话；证据不确定
// （进程号可能被复用、取不到启动时间）时案保持 OPEN 等待人工；不做任何自动重试。

// ProcessReport 是 daemon 对自己持有的未结束 Run 的进程核对结果。
type ProcessReport struct {
	// EvidenceID 由 daemon 生成并持久化，重发时保持不变，用作幂等键。
	EvidenceID string
	Generation int64
	// Kind 只接受 PROCESS_NOT_FOUND、PROCESS_EXITED、PROCESS_RUNNING。
	Kind reconciliation.EvidenceKind
	// Certain 为 false 表示 daemon 无法确定（例如进程号被复用），中心不会据此收尾。
	Certain     bool
	PID         int
	StartedAt   time.Time
	ExitCode    *int
	Detail      string
	PayloadHash string
	Nonce       string
	ObservedAt  time.Time
}

// ProcessReportStatus 描述中心对一次核对上报的处理结果。
type ProcessReportStatus string

const (
	ReportResolvedFailed   ProcessReportStatus = "RESOLVED_FAILED"
	ReportResolvedCanceled ProcessReportStatus = "RESOLVED_CANCELED"
	ReportCleared          ProcessReportStatus = "CLEARED"
	ReportOpen             ProcessReportStatus = "OPEN"
	ReportIgnored          ProcessReportStatus = "IGNORED"
)

// ProcessReportResult 是上报的处理结果。IGNORED 表示迟到或重复的证据（已幂等忽略）。
type ProcessReportResult struct {
	Status ProcessReportStatus `json:"status"`
	CaseID string              `json:"case_id,omitempty"`
	State  execution.State     `json:"run_state,omitempty"`
}

const (
	processGoneMessage = "The Agent process on the computer is no longer running, so this run was marked as failed. You can send the message again."
	operatorFailedText = "An administrator marked this run as failed after it could not be verified. You can send the message again."
)

// ReportProcess 处理 daemon 的进程核对上报。必须在设备事务内调用；
// 锁顺序与人工裁决一致：先案、后 Run。
func (s Store) ReportProcess(ctx context.Context, tx pgx.Tx, organizationID, deviceID string, deviceGeneration int64, runID string, report ProcessReport) (ProcessReportResult, error) {
	report.Detail = boundedText(report.Detail, maxDetailBytes)
	switch report.Kind {
	case reconciliation.EvidenceProcessNotFound, reconciliation.EvidenceProcessExited, reconciliation.EvidenceProcessRunning:
	default:
		return ProcessReportResult{}, ErrInvalidRequest
	}
	if !membership.ValidateUUID(report.EvidenceID) || !membership.ValidateUUID(runID) || report.Generation < 1 || report.PID < 0 ||
		len(report.PayloadHash) != 64 || len(report.Nonce) > 128 {
		return ProcessReportResult{}, ErrInvalidRequest
	}
	if report.Nonce == "" || len(report.Nonce) < 16 {
		report.Nonce = "recover-" + report.EvidenceID
	}
	if report.ObservedAt.IsZero() {
		report.ObservedAt = time.Now().UTC()
	}
	// 先锁当前代次的 OPEN 案（若有），再锁 Run，避免与人工裁决反向加锁。
	caseItem, caseErr := reconcileStore.LockOpenCase(ctx, tx, runID, report.Generation)
	if caseErr != nil && !errors.Is(caseErr, reconciliation.ErrNotFound) {
		return ProcessReportResult{}, caseErr
	}
	run, err := lockOwnedRun(ctx, tx, runID, deviceID, report.Generation)
	if errors.Is(err, ErrGenerationConflict) {
		return ProcessReportResult{Status: ReportIgnored}, nil
	}
	if err != nil {
		return ProcessReportResult{}, err
	}
	if run.State.Terminal() || !activeState(run.State) {
		// 迟到的证据：Run 已终态（或未被设备领取），不得复活。
		return ProcessReportResult{Status: ReportIgnored, State: run.State}, nil
	}
	var dbDeviceGeneration int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID).Scan(&dbDeviceGeneration); err != nil {
		return ProcessReportResult{}, err
	}
	if dbDeviceGeneration != deviceGeneration {
		return ProcessReportResult{Status: ReportIgnored, State: run.State}, nil
	}
	epoch, err := centerEpoch(ctx, tx)
	if err != nil {
		return ProcessReportResult{}, err
	}
	if caseErr != nil {
		// 没有开放案：设备确认仍在运行且中心也没有待核对标记则无事可做；其他情况按需开案。
		if report.Kind == reconciliation.EvidenceProcessRunning && !run.ReconciliationRequired {
			return ProcessReportResult{Status: ReportIgnored, State: run.State}, nil
		}
		if run.ReconciliationRequired {
			// 已有待核对标记却没有开放案（只能是历史已关闭的案）：不重复开案，交给人工。
			return ProcessReportResult{Status: ReportIgnored, State: run.State}, nil
		}
		reason := reconciliation.ReasonUnknownProcess
		if !report.Certain {
			reason = reconciliation.ReasonPIDReuse
		}
		caseItem, err = reconcileStore.OpenCaseOnDemand(ctx, tx, runID, run.Generation, run.Version, reason, "reported by daemon after restart")
		if err != nil {
			return ProcessReportResult{}, err
		}
		run, err = loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return ProcessReportResult{}, err
		}
	}
	evidence := reconciliation.Evidence{
		OrganizationID: organizationID, ID: report.EvidenceID, CaseID: caseItem.ID, RunID: runID,
		Generation: run.Generation, DeviceGeneration: deviceGeneration, CenterEpoch: epoch,
		Kind: report.Kind, Source: reconciliation.SourceDaemon, DeviceID: deviceID,
		SupervisorNonce: report.Nonce, ProcessPID: report.PID, ProcessStartedAt: report.StartedAt,
		ExitCode: report.ExitCode, PayloadHash: report.PayloadHash, SafeDetail: report.Detail, ObservedAt: report.ObservedAt,
	}
	switch {
	case !report.Certain:
		evidence.ProcessState = reconciliation.ProcessUnknown
	case report.Kind == reconciliation.EvidenceProcessRunning:
		evidence.ProcessState = reconciliation.ProcessRunning
	case report.Kind == reconciliation.EvidenceProcessExited:
		evidence.ProcessState, evidence.ProcessExited = reconciliation.ProcessExited, true
	default:
		evidence.ProcessState, evidence.ProcessExited = reconciliation.ProcessNotFound, true
	}
	stored, _, err := reconcileStore.InsertAcceptedEvidence(ctx, tx, evidence)
	if err != nil {
		return ProcessReportResult{}, err
	}
	result := ProcessReportResult{CaseID: caseItem.ID, State: run.State}
	if !report.Certain {
		// 不确定：保持 OPEN，记原因码 PID_REUSE，绝不据此收尾或终止进程。
		if err := reconcileStore.MarkCaseDetail(ctx, tx, caseItem.ID, reconciliation.ReasonPIDReuse, report.Detail); err != nil {
			return ProcessReportResult{}, err
		}
		result.Status = ReportOpen
		return result, nil
	}
	if report.Kind == reconciliation.EvidenceProcessRunning {
		if err := reconcileStore.CloseCaseProcessAlive(ctx, tx, caseItem, stored.ID, "device:"+deviceID, report.Detail); err != nil {
			return ProcessReportResult{}, err
		}
		updated, err := tx.Exec(ctx, `
			UPDATE openfox.runs
			   SET reconciliation_required = false,
			       lease_expires_at = clock_timestamp() + make_interval(secs => $3),
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND version = $2
			   AND state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')`,
			run.ID, run.Version, s.config().LeaseTTL.Seconds())
		if err != nil {
			return ProcessReportResult{}, err
		}
		if updated.RowsAffected() != 1 {
			return ProcessReportResult{}, ErrStateConflict
		}
		result.Status = ReportCleared
		return result, nil
	}
	resolution := reconciliation.ResolutionProcessExitedFailed
	if report.Kind == reconciliation.EvidenceProcessNotFound && run.StartedAt == nil && run.State == execution.StatePreparing {
		// 从未取得启动许可：进程不可能存在过，按取消处理。
		resolution = reconciliation.ResolutionProcessNotStartedCanceled
	}
	_, outcome, err := reconcileStore.ResolveCaseDetailed(ctx, tx, reconciliation.ResolveRequest{
		OrganizationID: organizationID, CaseID: caseItem.ID, RunID: runID, ExpectedVersion: caseItem.Version,
		Resolution: resolution, EvidenceID: stored.ID, ActorID: "device:" + deviceID,
		SafeDetail: report.Detail, Now: time.Now().UTC(),
	})
	if err != nil {
		return ProcessReportResult{}, err
	}
	if outcome.AlreadyResolved || !outcome.Terminated {
		result.Status = ReportIgnored
		return result, nil
	}
	if err := s.finishResolvedRun(ctx, tx, runID, processGoneMessage); err != nil {
		return ProcessReportResult{}, err
	}
	result.State = execution.State(outcome.State)
	result.Status = ReportResolvedFailed
	if outcome.State == string(execution.StateCanceled) {
		result.Status = ReportResolvedCanceled
	}
	return result, nil
}

// OperatorMarkFailed 是管理员人工入口：以 OPERATOR 证据把待核对的 Run 标为失败并释放对话。
// 案已被他人裁决时返回 ok=false（调用方提示“已处理”）。
func (s Store) OperatorMarkFailed(ctx context.Context, tx pgx.Tx, organizationID, runID, caseID string, expectedVersion int64, actorID, detail string) (reconciliation.Case, bool, error) {
	detail = boundedText(detail, maxDetailBytes)
	caseItem, err := reconcileStore.LockCase(ctx, tx, runID, caseID)
	if err != nil {
		return reconciliation.Case{}, false, err
	}
	if caseItem.Status != reconciliation.CaseOpen {
		return caseItem, false, nil
	}
	if caseItem.Version != expectedVersion {
		return reconciliation.Case{}, false, reconciliation.ErrVersionConflict
	}
	var deviceGeneration int64
	if err := tx.QueryRow(ctx, `
		SELECT d.generation FROM openfox.runs r JOIN openfox.physical_devices d ON d.id = r.computer_id
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = $1::uuid`, runID).Scan(&deviceGeneration); err != nil {
		return reconciliation.Case{}, false, err
	}
	epoch, err := centerEpoch(ctx, tx)
	if err != nil {
		return reconciliation.Case{}, false, err
	}
	evidenceID, err := newUUID()
	if err != nil {
		return reconciliation.Case{}, false, err
	}
	digest := sha256.Sum256([]byte("operator-mark-failed:" + caseID + ":" + actorID))
	stored, _, err := reconcileStore.InsertAcceptedEvidence(ctx, tx, reconciliation.Evidence{
		OrganizationID: organizationID, ID: evidenceID, CaseID: caseID, RunID: runID,
		Generation: caseItem.Generation, DeviceGeneration: deviceGeneration, CenterEpoch: epoch,
		Kind: reconciliation.EvidenceProcessExited, Source: reconciliation.SourceOperator,
		SupervisorNonce: "operator-" + evidenceID, ProcessState: reconciliation.ProcessExited, ProcessExited: true,
		PayloadHash: hex.EncodeToString(digest[:]), SafeDetail: detail, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		return reconciliation.Case{}, false, err
	}
	resolved, outcome, err := reconcileStore.ResolveCaseDetailed(ctx, tx, reconciliation.ResolveRequest{
		OrganizationID: organizationID, CaseID: caseID, RunID: runID, ExpectedVersion: expectedVersion,
		Resolution: reconciliation.ResolutionProcessExitedFailed, EvidenceID: stored.ID, ActorID: actorID,
		SafeDetail: detail, Now: time.Now().UTC(),
	})
	if err != nil {
		return reconciliation.Case{}, false, err
	}
	if outcome.AlreadyResolved {
		return resolved, false, nil
	}
	if outcome.Terminated {
		if err := s.finishResolvedRun(ctx, tx, runID, operatorFailedText); err != nil {
			return reconciliation.Case{}, false, err
		}
	}
	return resolved, true, nil
}

// finishResolvedRun 在 Run 被裁决为终态的同一事务内释放对话（输入置 CONSUMED、追加系统消息、通知发起人）并写 run.finished。
func (s Store) finishResolvedRun(ctx context.Context, tx pgx.Tx, runID, message string) error {
	finished, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return err
	}
	canceled := finished.State == execution.StateCanceled
	if canceled {
		message = "The Agent run was canceled."
	}
	if _, err := FinalizeConversation(ctx, tx, finished.Run, AuthorSystem, message, !canceled); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"state": string(finished.State), "failure_code": finished.FailureCode})
	if err := enqueueOutbox(ctx, tx, "run.finished", runID, string(payload)); err != nil {
		return fmt.Errorf("enqueue run.finished: %w", err)
	}
	return nil
}
