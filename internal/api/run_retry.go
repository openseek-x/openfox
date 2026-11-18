package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/idempotency"
)

// 阶段 12b：普通对话的运行重试。小助手会话沿用 /runs/{runID}/retry。
func (h *Handler) registerRunRetryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/runs/{runID}/retries", h.retryConversationRun)
}

type retryRunRequest struct {
	WorkspaceSelection *conversations.WorkspaceSelection `json:"workspace_selection"`
}

func (h *Handler) retryConversationRun(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if key == "" || !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is required and must be valid")
		return
	}
	var body retryRunRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
			return
		}
	}
	rid := collaborationRequestID(w, r)
	retryID, err := newUUID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "request could not be processed")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var result conversations.RetryRunResult
	err = h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).RetryRun(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("runID"), actor,
			conversations.RetryRunInput{RetryID: retryID, IdempotencyKey: key, WorkspaceSelection: body.WorkspaceSelection})
		if err != nil || result.Replayed {
			return err
		}
		return recordConversationAudit(ctx, tx, rid, selection.OrganizationID, actor,
			"run.retried", "run", result.RetryOf, "", nil)
	})
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrInvalidKey):
			writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid")
		case errors.Is(err, idempotency.ErrConflict):
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was used for a different request")
		case errors.Is(err, conversations.ErrRunActive):
			writeError(w, r, http.StatusConflict, "RUN_ACTIVE", "the run is still active")
		case errors.Is(err, conversations.ErrRetryInProgress):
			writeError(w, r, http.StatusConflict, "RETRY_IN_PROGRESS", "a retry of this run is already in progress")
		case errors.Is(err, conversations.ErrWorkspaceOverrideNotAllowed):
			writeError(w, r, http.StatusConflict, "WORKSPACE_OVERRIDE_NOT_ALLOWED", "the working directory can only be changed before it is locked or after a preparation failure")
		default:
			writeAssistantError(w, r, err)
		}
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"retry_id": result.RetryID, "retry_of": result.RetryOf})
}
