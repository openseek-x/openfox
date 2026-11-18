package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) listWeComGroups(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view integrations.WeComGroups
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.imSvc(r).WeComGroups(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), r.URL.Query().Get("after"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}
func (h *Handler) patchWeComGroup(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.WeComGroupPatch
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var group integrations.WeComGroup
	requestID := collaborationRequestID(w, r)
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		group, err = h.imSvc(r).PatchWeComGroup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), input)
		if err != nil {
			return err
		}
		if group.Version == *input.ExpectedVersion {
			return nil
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor, "integration.wecom.group_authorization", "im_bot", r.PathValue("botID"), "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "wecom", "result": strconv.FormatBool(group.Enabled), "version": strconv.FormatInt(group.Version, 10)})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, group)
}
