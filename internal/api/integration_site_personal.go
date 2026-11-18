package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/platform/safehttp"
	"time"
)

func (h *Handler) bindSitePersonal(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.SitePersonalInput
	if e := decodeJSONLimit(w, r, 16<<10, &in); e != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var grant integrations.SitePersonalGrant
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		grant, e = h.integrations.PrepareSitePersonal(ctx, tx, call.organizationID, call.selected.Actor, in)
		return e
	})
	if e != nil {
		writeIntegrationError(w, r, e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	grant, e = integrations.ValidateSitePersonal(ctx, grant, safehttp.Options{})
	if e != nil {
		writeIntegrationError(w, r, e)
		return
	}
	var out integrations.Connection
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SaveSitePersonal(ctx, tx, call.organizationID, call.selected.Actor, in, grant)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.personal_comment_bound", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": out.Provider})
	})
	if e != nil {
		writeIntegrationError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
