package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/cnb"
	"openfox/internal/integrations/gitapi"
)

func (h *Handler) registerCNBRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/connections/{connectionID}/cnb"
	mux.HandleFunc("GET "+base+"/repositories", h.getCNBRepositories)
	mux.HandleFunc("PUT "+base+"/repositories", h.putCNBRepositories)
	mux.HandleFunc("POST "+base+"/reconcile", h.reconcileCNB)
	mux.HandleFunc("POST /v1/hooks/git/cnb/{token}", gitRoute(integrations.ProviderCNB, h.receiveGitHubWebhook))
}

func mapCNBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitapi.ErrTokenRevoked):
		return integrations.ErrNeedsReauth
	case errors.Is(err, cnb.ErrForbidden):
		return integrations.ErrCNBCapability
	case errors.Is(err, cnb.ErrInvalidRepository), errors.Is(err, gitapi.ErrNotFound):
		return integrations.ErrInvalidRepository
	case errors.Is(err, gitapi.ErrRequestFailed):
		return integrations.ErrUpstreamUnavailable
	default:
		var limited *gitapi.ErrRateLimited
		if errors.As(err, &limited) {
			return integrations.ErrCNBRateLimited
		}
		return err
	}
}

// Mark only the credential version used by this failed read.
func (h *Handler) cnbCallError(ctx context.Context, org string, access integrations.CNBAccess, err error) error {
	if errors.Is(err, gitapi.ErrTokenRevoked) {
		if markErr := h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkCNBTokenRevoked(ctx, tx, access.ID, access.Version)
		}); markErr != nil {
			return markErr
		}
	}
	return mapCNBError(err)
}
func (h *Handler) getCNBRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.CNBConfiguration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.GetCNBConfiguration(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) putCNBRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version      uint64                       `json:"version"`
		Repositories []integrations.CNBRepository `json:"repositories"`
	}
	if decodeJSON(w, r, &input) != nil || input.Version == 0 || len(input.Repositories) > 20 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var access integrations.CNBAccess
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		access, err = h.integrations.PrepareCNB(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return err
	})
	if err == nil && access.Version != input.Version {
		err = integrations.ErrVersionConflict
	}
	if err == nil && access.Status != integrations.StatusActive {
		err = integrations.ErrNeedsReauth
	}
	if err == nil {
		err = access.ValidateRepositories(r.Context(), input.Repositories)
		if err != nil {
			err = h.cnbCallError(r.Context(), call.organizationID, access, err)
		}
	}
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	var out integrations.CNBConfiguration
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.SaveCNBRepositories(ctx, tx, call.organizationID, call.selected.Actor, access, input.Version, input.Repositories)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.cnb_repositories_updated", "integration_connection", access.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "cnb"})
	})
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) reconcileCNB(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		RepositoryID string `json:"repository_id"`
		Number       int    `json:"number"`
	}
	if decodeJSON(w, r, &input) != nil || input.Number <= 0 || int64(input.Number) > 2147483647 || len(input.RepositoryID) > 255 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.ReconcileCNB(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), input.RepositoryID, input.Number); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.cnb_reconcile_requested", "integration_connection", r.PathValue("connectionID"), "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "cnb"})
	})
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) receiveCNB(w http.ResponseWriter, r *http.Request, lookup integrations.GitWebhookLookup, body []byte) {
	var event integrations.CNBEvent
	err := h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		event, err = h.integrations.PrepareCNBEvent(ctx, tx, lookup, r.PathValue("token"), r.Header, body)
		return err
	})
	if errors.Is(err, integrations.ErrCNBSignature) {
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REJECTED", "signature is missing or invalid")
		return
	}
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	if event.Duplicate() {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	var repo cnb.Repository
	var commits []cnb.Commit
	var upstream error
	if event.IsPush() {
		repo, commits, upstream = event.HydrateCNBPush(r.Context())
	}
	var duplicate bool
	err = h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		duplicate, err = h.integrations.CompleteCNBEvent(ctx, tx, lookup.OrganizationID, event, repo, commits, upstream)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, mapCNBError(err))
		return
	}
	if !duplicate && upstream != nil {
		if errors.Is(upstream, cnb.ErrCommitLimit) {
			writeError(w, r, http.StatusUnprocessableEntity, "CNB_COMMIT_LIMIT", "comparison is incomplete or exceeds 100 commits; no commits were linked")
			return
		}
		writeIntegrationError(w, r, mapCNBError(upstream))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"duplicate": duplicate, "accepted": true})
}
