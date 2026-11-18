package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/membership"
)

func (h *Handler) registerAzureReposRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/azure_devops/connections/{connectionID}"
	mux.HandleFunc("GET "+base+"/work", h.getAzureReposStatus)
	mux.HandleFunc("POST "+base+"/backlinks/{backlinkID}/retry", h.retryAzureBacklink)
}
func (h *Handler) getAzureReposStatus(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.AzureReposStatus
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetAzureReposStatus(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("work_after"), r.URL.Query().Get("backlink_after"))
		return e
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) retryAzureBacklink(w http.ResponseWriter, r *http.Request) {
	call, access, ok := h.prepareAzureHookSource(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Action            string `json:"action"`
		Confirm           string `json:"confirm"`
		ConnectionVersion uint64 `json:"connection_version"`
		GrantGeneration   uint64 `json:"grant_generation"`
	}
	if decodeJSONLimit(w, r, 1024, &in) != nil || !membership.ValidateUUID(r.PathValue("backlinkID")) {
		writeAzureHookError(w, r, integrations.ErrInvalidRequest)
		return
	}
	if in.ConnectionVersion != access.ConnectionVersion || in.GrantGeneration != access.GrantGeneration {
		writeAzureHookError(w, r, integrations.ErrVersionConflict)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if e := h.integrations.RetryAzureBacklink(ctx, tx, call.organizationID, call.selected.Actor, access, r.PathValue("backlinkID"), in.Action, in.Confirm); e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.backlink_retry", "integration_connection", access.ConnectionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops", "action": in.Action, "backlink_id": r.PathValue("backlinkID")})
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"scheduled": true})
}
