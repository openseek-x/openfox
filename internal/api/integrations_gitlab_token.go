package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) bindGitLabToken(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.GitLabTokenInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	svc := h.integrations.ForGit(integrations.ProviderGitLab)
	var plan integrations.GitLabTokenPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = svc.PrepareGitLabToken(ctx, tx, call.organizationID, call.selected.Actor, input)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	out, base, err := svc.ValidateGitLabToken(ctx, plan)
	cancel()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var result integrations.Connection
	requestID := collaborationRequestID(w, r)
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		result, audits, err = svc.SaveGitLabToken(ctx, tx, call.organizationID, call.selected.Actor, plan, out, base)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
