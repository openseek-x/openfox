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
	"openfox/internal/reconciliation"
)

func (h *Handler) registerReconciliationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/reconciliation", h.listReconciliationCases)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/runs/{runID}/reconciliation/{caseID}/resolve", h.resolveReconciliationCase)
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
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := h.delivery.GetRun(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor); err != nil {
			return err
		}
		var err error
		item, err = h.reconcile.ResolveCase(ctx, tx, reconciliation.ResolveRequest{
			OrganizationID: selection.OrganizationID,
			CaseID:         r.PathValue("caseID"), RunID: r.PathValue("runID"), ExpectedVersion: input.Version,
			Resolution: reconciliation.Resolution(input.Resolution), EvidenceID: input.EvidenceID,
			ActorID: auth.principal.UserID, SafeDetail: input.Detail, Now: time.Now().UTC(),
		})
		if err != nil {
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
	writeJSON(w, http.StatusOK, item)
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
	case errors.Is(err, reconciliation.ErrNotFound):
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
