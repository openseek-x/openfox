package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/executiondelivery"
	"openfox/internal/membership"
	"openfox/internal/reconciliation"
)

func (h *Handler) registerReconciliationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/reconciliation", h.listReconciliationCases)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/reconciliation/evidence", h.listReconciliationEvidence)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/reconciling-runs", h.listReconcilingRuns)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/runs/{runID}/reconciliation/{caseID}/resolve", h.resolveReconciliationCase)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/runs/{runID}/reconciliation/{caseID}/mark-failed", h.markReconciliationFailed)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/reconciliation/process-report", h.reportRunProcess)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/reconciliation/fact", h.submitReconciliationFact)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/reconciliation/evidence", h.submitReconciliationEvidence)
}

type resolveReconciliationRequest struct {
	Version    int64  `json:"version"`
	Resolution string `json:"resolution"`
	EvidenceID string `json:"evidence_id"`
	Detail     string `json:"detail"`
}

type reconciliationEvidenceRequest struct {
	ID               string    `json:"id"`
	CaseID           string    `json:"case_id"`
	Generation       int64     `json:"generation"`
	DeviceGeneration int64     `json:"device_generation"`
	CenterEpoch      int64     `json:"center_epoch"`
	SupervisorNonce  string    `json:"supervisor_nonce"`
	ProcessPID       int       `json:"process_pid"`
	ProcessStartedAt time.Time `json:"process_started_at"`
	Kind             string    `json:"kind"`
	ProcessState     string    `json:"process_state"`
	ProcessExited    bool      `json:"process_exited"`
	ExitCode         *int      `json:"exit_code"`
	EventSequence    int64     `json:"event_sequence"`
	ResultID         string    `json:"result_id"`
	PayloadHash      string    `json:"payload_hash"`
	Detail           string    `json:"detail"`
	ObservedAt       time.Time `json:"observed_at"`
}

type reconciliationFactRequest struct {
	Generation        int64     `json:"generation"`
	DeviceGeneration  int64     `json:"device_generation"`
	CenterEpoch       int64     `json:"center_epoch"`
	SupervisorNonce   string    `json:"supervisor_nonce"`
	ProcessPID        int       `json:"process_pid"`
	ProcessStartedAt  time.Time `json:"process_started_at"`
	WorkdirBindingID  string    `json:"workdir_binding_id"`
	WorkdirPathHash   string    `json:"workdir_path_hash"`
	ProcessState      string    `json:"process_state"`
	ExitCode          *int      `json:"exit_code"`
	ProcessExitedAt   time.Time `json:"process_exited_at"`
	LastEventSequence int64     `json:"last_event_sequence"`
	LastResultID      string    `json:"last_result_id"`
	ObservedAt        time.Time `json:"observed_at"`
	Version           int64     `json:"version"`
}

func (h *Handler) listReconciliationCases(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var cases []reconciliation.Case
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := h.delivery.GetRun(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor); err != nil {
			return err
		}
		var err error
		cases, err = h.reconcile.ListCases(ctx, tx, selection.OrganizationID, r.PathValue("runID"))
		return err
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases})
}

func (h *Handler) resolveReconciliationCase(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	if normalizedRole := strings.ToUpper(strings.TrimSpace(role)); normalizedRole != "OWNER" && normalizedRole != "ADMIN" {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "reconciliation requires organization administration")
		return
	}
	var input resolveReconciliationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var item reconciliation.Case
	var alreadyResolved bool
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := h.delivery.GetRun(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor); err != nil {
			return err
		}
		var err error
		var outcome reconciliation.RunOutcome
		item, outcome, err = h.reconcile.ResolveCaseDetailed(ctx, tx, reconciliation.ResolveRequest{
			OrganizationID: selection.OrganizationID,
			CaseID:         r.PathValue("caseID"), RunID: r.PathValue("runID"), ExpectedVersion: input.Version,
			Resolution: reconciliation.Resolution(input.Resolution), EvidenceID: input.EvidenceID,
			ActorID: auth.principal.UserID, SafeDetail: input.Detail, Now: time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		if outcome.AlreadyResolved {
			// 案已被他人（人工或自动）裁决：本次没有任何修改，不写审计，由前端提示「已处理」。
			alreadyResolved = true
			return nil
		}
		return recordConversationAudit(ctx, tx, collaborationRequestID(w, r), selection.OrganizationID, actor,
			"run.reconciliation_resolved", "reconciliation_case", item.ID, "", map[string]string{
				"resolution": string(item.Resolution), "evidence_id": item.ResolutionEvidenceID,
			})
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	// 响应仍是核对案本身（向后兼容），只追加 already_resolved。
	writeJSON(w, http.StatusOK, resolveReconciliationResponse{Case: item, AlreadyResolved: alreadyResolved})
}

type resolveReconciliationResponse struct {
	reconciliation.Case
	AlreadyResolved bool `json:"already_resolved"`
}

// reconciliationEvidenceView 是给管理员看的证据摘要：只含来源、类型、时间与说明，
// 不返回 supervisor nonce、负载哈希、进程号等内部字段。
type reconciliationEvidenceView struct {
	ID            string    `json:"id"`
	CaseID        string    `json:"case_id"`
	Kind          string    `json:"kind"`
	Source        string    `json:"source"`
	ProcessState  string    `json:"process_state"`
	AcceptedState string    `json:"accepted_state"`
	Detail        string    `json:"detail"`
	ObservedAt    time.Time `json:"observed_at"`
	CreatedAt     time.Time `json:"created_at"`
	// CanAcceptResult 表示可据此证据对所属的开放案执行 RESULT_ACCEPTED 裁决
	// （与 reconciliation.ResolveWithEvidence 的判定一致），前端据此决定是否显示「接受已有结果」。
	CanAcceptResult bool `json:"can_accept_result"`
}

// listReconciliationEvidence 列出 Run 的核对证据（最近 100 条，时间倒序）。仅组织 Owner/Admin。
func (h *Handler) listReconciliationEvidence(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if normalizedRole := strings.ToUpper(strings.TrimSpace(role)); normalizedRole != "OWNER" && normalizedRole != "ADMIN" {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "reconciliation requires organization administration")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	views := make([]reconciliationEvidenceView, 0)
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		runID := r.PathValue("runID")
		if _, err := h.delivery.GetRun(ctx, tx, selection.OrganizationID, runID, actor); err != nil {
			return err
		}
		cases, err := h.reconcile.ListCases(ctx, tx, selection.OrganizationID, runID)
		if err != nil {
			return err
		}
		openCases := map[string]bool{}
		for _, item := range cases {
			if item.Status == reconciliation.CaseOpen {
				openCases[item.ID] = true
			}
		}
		items, err := h.reconcile.ListEvidence(ctx, tx, runID)
		if err != nil {
			return err
		}
		for _, item := range items {
			views = append(views, reconciliationEvidenceView{
				ID: item.ID, CaseID: item.CaseID, Kind: string(item.Kind), Source: string(item.Source),
				ProcessState: string(item.ProcessState), AcceptedState: item.AcceptedState,
				Detail: item.SafeDetail, ObservedAt: item.ObservedAt, CreatedAt: item.CreatedAt,
				CanAcceptResult: openCases[item.CaseID] && item.AcceptsResult() && item.StoredResult,
			})
		}
		return nil
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evidence": views})
}

// listReconcilingRuns 列出本组织待核对的运行（可用 device_id 限定到一台电脑）。仅组织 Owner/Admin。
// 不依赖会话可见性，只返回运维必要的非内容字段，供管理员在电脑页解除卡住的运行。
func (h *Handler) listReconcilingRuns(w http.ResponseWriter, r *http.Request) {
	_, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if normalizedRole := strings.ToUpper(strings.TrimSpace(role)); normalizedRole != "OWNER" && normalizedRole != "ADMIN" {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "reconciliation requires organization administration")
		return
	}
	var runs []reconciliation.PendingRun
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		runs, err = h.reconcile.ListPendingRuns(ctx, tx, strings.TrimSpace(r.URL.Query().Get("device_id")))
		return err
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

type markReconciliationFailedRequest struct {
	Version int64  `json:"version"`
	Detail  string `json:"detail"`
}

// markReconciliationFailed 是管理员的人工入口：把无法自动核对的待核对 Run 标为失败并释放对话。
// 仅组织 Owner/Admin 可用；案已被自动裁决或他人处理时返回 200 且 already_resolved=true。
func (h *Handler) markReconciliationFailed(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	if normalizedRole := strings.ToUpper(strings.TrimSpace(role)); normalizedRole != "OWNER" && normalizedRole != "ADMIN" {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "reconciliation requires organization administration")
		return
	}
	var input markReconciliationFailedRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	// 阶段 12e：不再要求能看到该 Run 所在的会话。普通成员的私有会话卡在待核对时，
	// 会话外的组织管理员也必须能解除；租户边界仍由行级安全与按 (组织, 案, Run) 的加锁保证，
	// 响应只含核对案本身，不含任何会话内容。「接受已有结果」（resolve）不放开。
	if !membership.ValidateUUID(r.PathValue("runID")) || !membership.ValidateUUID(r.PathValue("caseID")) {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	var item reconciliation.Case
	var applied bool
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, applied, err = h.dispatch.OperatorMarkFailed(ctx, tx, selection.OrganizationID, r.PathValue("runID"),
			r.PathValue("caseID"), input.Version, auth.principal.UserID, input.Detail)
		if err != nil || !applied {
			return err
		}
		return recordConversationAudit(ctx, tx, collaborationRequestID(w, r), selection.OrganizationID, actor,
			"run.reconciliation_resolved", "reconciliation_case", item.ID, "", map[string]string{
				"resolution": string(item.Resolution), "evidence_id": item.ResolutionEvidenceID,
			})
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"case": item, "already_resolved": !applied})
}

type runProcessReportRequest struct {
	EvidenceID       string    `json:"evidence_id"`
	Generation       int64     `json:"generation"`
	DeviceGeneration int64     `json:"device_generation"`
	Kind             string    `json:"kind"`
	Certain          bool      `json:"certain"`
	ProcessPID       int       `json:"process_pid"`
	ProcessStartedAt time.Time `json:"process_started_at"`
	ExitCode         *int      `json:"exit_code"`
	Detail           string    `json:"detail"`
	PayloadHash      string    `json:"payload_hash"`
	ObservedAt       time.Time `json:"observed_at"`
}

// reportRunProcess 接收 daemon 重启或重连后对自己持有的未结束 Run 的进程核对结果，
// 确定证据在同一事务内自动裁决；迟到、重复的证据幂等忽略。
func (h *Handler) reportRunProcess(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input runProcessReportRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	// 以认证主体的设备代次为准；daemon 不必知道自己的代次，传入则必须一致。
	deviceGeneration := principal.DeviceGeneration
	if input.DeviceGeneration != 0 && input.DeviceGeneration != deviceGeneration {
		writeReconciliationError(w, r, reconciliation.ErrStaleEvidence)
		return
	}
	var result dispatch.ProcessReportResult
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.dispatch.ReportProcess(ctx, tx, selection.OrganizationID, principal.DeviceID, deviceGeneration, r.PathValue("runID"), dispatch.ProcessReport{
			EvidenceID: input.EvidenceID, Generation: input.Generation, Kind: reconciliation.EvidenceKind(input.Kind),
			Certain: input.Certain, PID: input.ProcessPID, StartedAt: input.ProcessStartedAt, ExitCode: input.ExitCode,
			Detail: input.Detail, PayloadHash: input.PayloadHash, ObservedAt: input.ObservedAt,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, dispatch.ErrInvalidRequest) {
			err = reconciliation.ErrInvalidRequest
		} else if errors.Is(err, dispatch.ErrNotFound) {
			err = reconciliation.ErrNotFound
		}
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) submitReconciliationEvidence(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input reconciliationEvidenceRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	if input.DeviceGeneration != principal.DeviceGeneration {
		writeReconciliationError(w, r, reconciliation.ErrStaleEvidence)
		return
	}
	var item reconciliation.Evidence
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = h.reconcile.SubmitEvidence(ctx, tx, reconciliation.Evidence{
			OrganizationID: selection.OrganizationID, ID: input.ID, CaseID: input.CaseID,
			RunID: r.PathValue("runID"), Generation: input.Generation, DeviceGeneration: input.DeviceGeneration,
			CenterEpoch: input.CenterEpoch, Kind: reconciliation.EvidenceKind(input.Kind),
			Source: reconciliation.SourceDaemon, DeviceID: principal.DeviceID, SupervisorNonce: input.SupervisorNonce,
			ProcessPID: input.ProcessPID, ProcessStartedAt: input.ProcessStartedAt,
			ProcessState: reconciliation.ProcessState(input.ProcessState), ProcessExited: input.ProcessExited,
			ExitCode: input.ExitCode, EventSequence: input.EventSequence, ResultID: input.ResultID,
			PayloadHash: input.PayloadHash, SafeDetail: input.Detail, ObservedAt: input.ObservedAt,
		})
		return err
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (h *Handler) submitReconciliationFact(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input reconciliationFactRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeReconciliationError(w, r, reconciliation.ErrInvalidRequest)
		return
	}
	if input.DeviceGeneration != principal.DeviceGeneration {
		writeReconciliationError(w, r, reconciliation.ErrStaleEvidence)
		return
	}
	var item reconciliation.SupervisorFact
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = h.reconcile.UpsertFact(ctx, tx, reconciliation.FactRequest{
			Manifest: reconciliation.Manifest{
				OrganizationID: selection.OrganizationID, RunID: r.PathValue("runID"), DeviceID: principal.DeviceID,
				Generation: input.Generation, DeviceGeneration: input.DeviceGeneration, CenterEpoch: input.CenterEpoch,
				SupervisorNonce: input.SupervisorNonce, ProcessPID: input.ProcessPID,
				ProcessStartedAt: input.ProcessStartedAt, WorkdirBindingID: input.WorkdirBindingID,
				WorkdirPathHash: input.WorkdirPathHash, LastEventSequence: input.LastEventSequence, ResultID: input.LastResultID,
			},
			ProcessState: reconciliation.ProcessState(input.ProcessState), ExitCode: input.ExitCode, ProcessExitedAt: pointerTime(input.ProcessExitedAt),
			ObservedAt: input.ObservedAt, ExpectedVersion: input.Version,
		})
		return err
	})
	if err != nil {
		writeReconciliationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func writeReconciliationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reconciliation.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "reconciliation request is invalid")
	case errors.Is(err, reconciliation.ErrNotFound), errors.Is(err, executiondelivery.ErrNotFound):
		// 看不到该 Run（例如不在其私有空间里的组织管理员）与不存在同样返回 404，此前落入 500。
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "reconciliation resource is not available")
	case errors.Is(err, reconciliation.ErrVersionConflict), errors.Is(err, reconciliation.ErrEvidenceConflict),
		errors.Is(err, reconciliation.ErrEvidenceNotAccepted), errors.Is(err, reconciliation.ErrStaleEvidence):
		writeError(w, r, http.StatusConflict, "RECONCILIATION_CONFLICT", "reconciliation fact is stale or not sufficient")
	case errors.Is(err, deviceauth.ErrSessionNotFound), errors.Is(err, deviceauth.ErrDeviceUnauthenticated):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
	case errors.Is(err, conversations.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "resource is not available")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func pointerTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
