package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func (h *Handler) reorderTaskTypes(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateCollaboration(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input struct {
		Types []collaboration.TaskTypeOrderItem `json:"types"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID, spaceID := r.PathValue("organizationID"), r.PathValue("spaceID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	requestID := collaborationRequestID(w, r)
	var items []collaboration.TaskType
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest, staticCollaborationSelector(selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
		service := collaboration.Service{}
		access, err := service.LoadSpaceAccess(ctx, tx, organizationID, spaceID, selected.Actor.UserID)
		if err != nil {
			return err
		}
		decision := collaborationSpaceDecision(policy.ActionManageSpace, organizationID, selected, access, "")
		if !decision.Allowed() {
			return decisionError{decision: decision}
		}
		items, err = service.ReorderTaskTypes(ctx, tx, organizationID, spaceID, selected.Actor, input.Types)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, organizationID, selected.Actor, "task_types.reordered", "space", spaceID, spaceID, selected.Actor.AuthorizationVersion, nil)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_types": items})
}
