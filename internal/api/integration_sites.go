package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/integrations"
	"openfox/internal/platform/safehttp"
)

func (h *Handler) bindOrganizationSite(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.SiteConnectionInput
	if err := decodeJSONLimit(w, r, 16<<10, &in); err != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	provider := r.PathValue("provider")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.CheckSiteConnection(ctx, tx, call.organizationID, call.selected.Actor, provider, in)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	grant, err := integrations.VerifySiteConnection(ctx, provider, in, h.importSafe)
	if err != nil {
		writeSiteConnectionError(w, r, err)
		return
	}
	var out integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var changed bool
		var err error
		out, changed, err = h.integrations.SaveSiteConnection(ctx, tx, call.organizationID, call.selected.Actor, provider, in, grant)
		if err != nil || !changed {
			return err
		}
		action := "integration.connection_created"
		if in.ConnectionID != "" {
			action = "integration.connection_reauthorized"
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor,
			action, "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": provider})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func writeSiteConnectionError(w http.ResponseWriter, r *http.Request, err error) {
	var jiraErr *jiraapi.Error
	switch {
	case errors.As(err, &jiraErr):
		writeJiraConnectionError(w, r, err)
	case errors.Is(err, safehttp.ErrBlockedAddress):
		writeError(w, r, 400, "SOURCE_ADDRESS_BLOCKED", "private and reserved addresses are unavailable; an intranet proxy is not connected")
	case errors.Is(err, zentao.ErrInput), errors.Is(err, safehttp.ErrInvalidURL):
		writeError(w, r, 400, "INVALID_REQUEST", "site or authentication parameters are invalid")
	case errors.Is(err, zentao.ErrAuth):
		writeError(w, r, 422, "SOURCE_AUTH_FAILED", "source rejected the credentials")
	case errors.Is(err, zentao.ErrUpstream):
		writeError(w, r, 502, "UPSTREAM_UNAVAILABLE", "could not verify the source; check its address, TLS certificate and connectivity")
	default:
		writeIntegrationError(w, r, err)
	}
}
