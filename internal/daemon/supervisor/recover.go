package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"openfox/internal/runtime"
)

// 阶段 12a：daemon 重启后的进程核对。
//
// 规则（宁可交给人工，也不误杀）：
//   - 记录里没有进程号：从未启动进程 -> PROCESS_NOT_FOUND（确定）。
//   - 进程号不存在 -> PROCESS_NOT_FOUND（确定）。
//   - 进程号存在且启动时间与记录一致 -> 分级终止进程组，确认退出后 PROCESS_EXITED（确定）。
//   - 进程号存在但启动时间不一致、取不到启动时间、旧格式记录、或终止后仍存在 -> 不确定，
//     不再发送任何信号，上报 PROCESS_NOT_FOUND 并标明不确定（中心记 PID_REUSE，留给人工）。
// 已知限制：只保证进程组；逃出进程组的子进程不在核对范围内。

const reportRequestTimeout = 15 * time.Second

// ProcessReportInput 是上报给中心的核对结论。
type ProcessReportInput struct {
	EvidenceID  string    `json:"evidence_id"`
	Generation  int64     `json:"generation"`
	Kind        string    `json:"kind"`
	Certain     bool      `json:"certain"`
	ProcessPID  int       `json:"process_pid,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	PayloadHash string    `json:"payload_hash"`
	ObservedAt  time.Time `json:"observed_at"`
}

// newProcessReport 构造上报体；payload_hash 由稳定字段推出，同一证据 ID 重发时哈希一致。
func newProcessReport(evidenceID string, generation int64, kind string, certain bool, pid int, detail string) ProcessReportInput {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%t|%d|%s", evidenceID, generation, kind, certain, pid, detail)))
	return ProcessReportInput{
		EvidenceID: evidenceID, Generation: generation, Kind: kind, Certain: certain, ProcessPID: pid,
		Detail: detail, PayloadHash: hex.EncodeToString(digest[:]), ObservedAt: time.Now().UTC(),
	}
}

// ProcessReport 向中心上报进程核对结论。
func (c *Client) ProcessReport(ctx context.Context, organizationID, runID string, input ProcessReportInput) error {
	return c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "reconciliation", "process-report"), input, nil)
}

// recoverProcesses 在启动时对上次未结束的 Run 逐条核对并上报。
// 结论先落本地（PENDING）再上报，因此上报失败或再次重启都不会重复终止进程。
func (s *Supervisor) recoverProcesses(ctx context.Context) error {
	journal := s.config.Journal
	runs, err := journal.AbandonUnfinished(ctx)
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		s.config.Logger.Warn("supervisor_abandoned_runs", "count", len(runs))
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			break
		}
		kind, certain, detail := s.checkProcess(ctx, run)
		if err := journal.SaveReport(ctx, run.RunID, run.Generation, kind, certain, detail); err != nil {
			return err
		}
		s.config.Logger.Info("supervisor_process_checked", "run_id", run.RunID, "generation", run.Generation,
			"kind", kind, "certain", certain)
	}
	s.flushResults(ctx)
	s.flushReports(ctx)
	_, err = journal.Prune(ctx)
	return err
}

// flushResults 先补传固定 ID 的结果，活动运行仍由自己的上传器处理。
func (s *Supervisor) flushResults(ctx context.Context) {
	results, err := s.config.Journal.PendingResults(ctx)
	if err != nil {
		s.config.Logger.Error("supervisor_pending_results_failed", "error", err.Error())
		return
	}
	for _, result := range results {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		active := s.active[result.RunID] != nil
		s.mu.Unlock()
		if active {
			continue
		}
		request, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
		err := s.config.Client.AppendResult(request, result.OrganizationID, result.RunID, result.Input)
		cancel()
		if err != nil {
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "RESULT_FINALIZED" {
				s.config.Logger.Warn("supervisor_result_retry_failed", "run_id", result.RunID, "error", err.Error())
				continue
			}
			if err := s.config.Journal.RejectFinalResult(ctx, result.RunID, result.Input); err != nil {
				s.config.Logger.Error("supervisor_result_ack_failed", "error", err.Error())
				continue
			}
			s.config.Logger.Warn("supervisor_result_permanently_rejected", "run_id", result.RunID, "generation", result.Input.Generation, "result_id", result.Input.ID, "code", apiErr.Code)
			continue
		}
		if err := s.config.Journal.DeleteResult(ctx, result.RunID, result.Input.Generation); err != nil {
			s.config.Logger.Error("supervisor_result_ack_failed", "error", err.Error())
		}
	}
}

// checkProcess 核对单条记录，返回（证据类型, 是否确定, 说明）。
func (s *Supervisor) checkProcess(ctx context.Context, run RecoveredRun) (string, bool, string) {
	if !run.ProcKnown {
		return "PROCESS_NOT_FOUND", false, "journal record predates process tracking"
	}
	if run.Identity.PID <= 0 {
		return "PROCESS_NOT_FOUND", true, "no runtime process was recorded for this run"
	}
	existed := runtime.InspectProcess(run.Identity.PID).Exists
	terminateContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch runtime.TerminateRecordedProcess(terminateContext, run.Identity) {
	case runtime.TerminateGone:
		if existed {
			return "PROCESS_EXITED", true, "the previous runtime process was stopped after daemon restart"
		}
		return "PROCESS_NOT_FOUND", true, "the previous runtime process is no longer running"
	case runtime.TerminateMismatch:
		return "PROCESS_NOT_FOUND", false, "process id exists with a different start time; it may have been reused"
	case runtime.TerminateUnverifiable:
		if !existed && run.Identity.PGID != 0 {
			// 组长已退出但进程组仍有成员：组号可能已被无关进程组复用，不发信号，留给人工。
			return "PROCESS_NOT_FOUND", false, "group leader exited but the process group still has members; ownership cannot be confirmed"
		}
		return "PROCESS_NOT_FOUND", false, "process start time could not be verified"
	default:
		return "PROCESS_NOT_FOUND", false, "the previous runtime process did not stop after escalation"
	}
}

// flushReports 上报全部待上报结论；网络类失败保留待下次重试，中心的永久拒绝视为已处理。
func (s *Supervisor) flushReports(ctx context.Context) {
	journal := s.config.Journal
	reports, err := journal.PendingReports(ctx)
	if err != nil {
		s.config.Logger.Error("supervisor_pending_reports_failed", "error", err.Error())
		return
	}
	for _, report := range reports {
		if ctx.Err() != nil {
			return
		}
		requestContext, cancel := context.WithTimeout(ctx, reportRequestTimeout)
		err := s.config.Client.ProcessReport(requestContext, report.OrganizationID, report.RunID,
			newProcessReport(report.EvidenceID, report.Generation, report.Kind, report.Certain, report.PID, report.Detail))
		cancel()
		if err != nil && !permanent(err) {
			s.config.Logger.Warn("supervisor_process_report_failed", "run_id", report.RunID, "error", err.Error())
			continue
		}
		if err != nil {
			s.config.Logger.Warn("supervisor_process_report_rejected", "run_id", report.RunID, "code", errorCode(err))
		}
		if err := journal.MarkReportDone(ctx, report.RunID, report.Generation); err != nil {
			s.config.Logger.Error("supervisor_mark_report_failed", "error", err.Error())
		}
	}
}

// reportAlive 在中心因租约过期把仍在运行的 Run 置为待核对时，由本机活动 Run 上报 PROCESS_RUNNING。
func (r *runState) reportAlive(ctx context.Context) bool {
	requestContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportRequestTimeout)
	defer cancel()
	err := r.supervisor.config.Client.ProcessReport(requestContext, r.offer.OrganizationID, r.offer.RunID,
		newProcessReport(newUUID(), r.offer.Generation, "PROCESS_RUNNING", true, 0, "daemon reconnected; the runtime process is still running"))
	if err != nil {
		r.logger.Warn("supervisor_alive_report_failed", "error", err.Error())
		return false
	}
	return true
}
