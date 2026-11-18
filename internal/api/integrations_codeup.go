package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/codeup"
	"openfox/internal/integrations/gitapi"
)

func (h *Handler) registerCodeupRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/connections/{connectionID}/codeup"
	mux.HandleFunc("GET "+base+"/configuration", h.getCodeupRepositories)
	mux.HandleFunc("GET "+base+"/work", h.listCodeupWork)
	mux.HandleFunc("GET "+base+"/repositories", h.listCodeupRepositories)
	mux.HandleFunc("PUT "+base+"/credentials", h.updateCodeupCredentials)
	mux.HandleFunc("PUT "+base+"/repositories", h.putCodeupRepositories)
	mux.HandleFunc("POST "+base+"/reconcile", h.reconcileCodeup)
	mux.HandleFunc("POST /v1/hooks/git/codeup/{token}", gitRoute(integrations.ProviderCodeup, h.receiveGitHubWebhook))
}

func mapCodeupError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitapi.ErrTokenRevoked):
		return integrations.ErrNeedsReauth
	case errors.Is(err, codeup.ErrUnsupported):
		return integrations.ErrCodeupUnsupported
	case errors.Is(err, codeup.ErrForbidden):
		return integrations.ErrCodeupCapability
	case errors.Is(err, codeup.ErrInvalidRepository), errors.Is(err, gitapi.ErrNotFound):
		return integrations.ErrInvalidRepository
	case errors.Is(err, gitapi.ErrRequestFailed):
		return integrations.ErrUpstreamUnavailable
	default:
		var limited *gitapi.ErrRateLimited
		if errors.As(err, &limited) {
			return integrations.ErrCodeupRateLimited
		}
		return err
	}
}

// Mark only the credential version used by this failed read.
func (h *Handler) codeupCallError(ctx context.Context, org string, access integrations.CodeupAccess, err error) error {
	if errors.Is(err, gitapi.ErrTokenRevoked) {
		if markErr := h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkCodeupTokenRevoked(ctx, tx, access.ID, access.Version)
		}); markErr != nil {
			return markErr
		}
	}
	return mapCodeupError(err)
}
func (h *Handler) getCodeupRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.CodeupConfiguration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.GetCodeupConfiguration(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) putCodeupRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version      uint64                          `json:"version"`
		Repositories []integrations.CodeupRepository `json:"repositories"`
	}
	if decodeJSON(w, r, &input) != nil || input.Version == 0 || len(input.Repositories) > 20 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var access integrations.CodeupAccess
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		access, err = h.integrations.PrepareCodeup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
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
			err = h.codeupCallError(r.Context(), call.organizationID, access, err)
		}
	}
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	var out integrations.CodeupConfiguration
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.SaveCodeupRepositories(ctx, tx, call.organizationID, call.selected.Actor, access, input.Version, input.Repositories)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.codeup_repositories_updated", "integration_connection", access.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "codeup"})
	})
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) reconcileCodeup(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version      uint64 `json:"version"`
		RepositoryID string `json:"repository_id"`
		Number       int    `json:"number"`
	}
	if decodeJSON(w, r, &input) != nil || input.Version == 0 || input.Number <= 0 || int64(input.Number) > 2147483647 || len(input.RepositoryID) > 255 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.ReconcileCodeup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), input.RepositoryID, input.Number, input.Version); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.codeup_reconcile_requested", "integration_connection", r.PathValue("connectionID"), "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "codeup"})
	})
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) receiveCodeup(w http.ResponseWriter, r *http.Request, lookup integrations.GitWebhookLookup, body []byte) {
	var event integrations.CodeupEvent
	err := h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		event, err = h.integrations.PrepareCodeupEvent(ctx, tx, lookup, r.PathValue("token"), r.Header, body)
		return err
	})
	if errors.Is(err, integrations.ErrCodeupSignature) {
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REJECTED", "signature is missing or invalid")
		return
	}
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	if event.Duplicate() {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	var repo codeup.Repository
	var commits []codeup.Commit
	var upstream error
	if event.IsPush() {
		repo, commits, upstream = event.HydrateCodeupPush(r.Context())
	}
	var duplicate bool
	err = h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		duplicate, err = h.integrations.CompleteCodeupEvent(ctx, tx, lookup.OrganizationID, event, repo, commits, upstream)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	if !duplicate && upstream != nil {
		if errors.Is(upstream, codeup.ErrCommitLimit) {
			writeError(w, r, http.StatusUnprocessableEntity, "CODEUP_COMMIT_LIMIT", "comparison is incomplete or exceeds 100 commits; no commits were linked")
			return
		}
		writeIntegrationError(w, r, mapCodeupError(upstream))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"duplicate": duplicate, "accepted": true})
}

func (h *Handler) listCodeupRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 150 {
			writeIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
		page = n
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var a integrations.CodeupAccess
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		a, err = h.integrations.PrepareCodeup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return err
	})
	if err == nil && a.Status != integrations.StatusActive {
		err = integrations.ErrNeedsReauth
	}
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	rows, more, err := a.List(r.Context(), q, page)
	if err != nil {
		writeIntegrationError(w, r, h.codeupCallError(r.Context(), call.organizationID, a, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": rows, "has_more": more})
}
func (h *Handler) updateCodeupCredentials(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
		integrations.ProviderTokenInput
	}
	if decodeJSON(w, r, &input) != nil || input.Version == 0 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.CheckProviderToken(ctx, tx, call.organizationID, call.selected.Actor, integrations.ProviderCodeup, input.ProviderTokenInput); err != nil {
			return err
		}
		a, err := h.integrations.PrepareCodeup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		if err != nil {
			return err
		}
		if a.Version != input.Version {
			return integrations.ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	identity, _, err := h.integrations.VerifyProviderToken(r.Context(), integrations.ProviderCodeup, input.ProviderTokenInput)
	if err != nil {
		writeIntegrationError(w, r, mapCodeupError(err))
		return
	}
	var out integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.SaveCodeupCredentials(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), input.Version, input.ProviderTokenInput, identity)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.codeup_credentials_updated", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "codeup"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listCodeupWork(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 50 {
			writeIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
		limit = n
	}
	var out integrations.CodeupWorkPage
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.ListCodeupWork(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("after"), limit)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
