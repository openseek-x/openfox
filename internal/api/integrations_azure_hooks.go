package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
)

func (h *Handler) registerAzureHookRoutes(mux *http.ServeMux) {
	h.registerAzureReposRoutes(mux)
	base := "/v1/organizations/{organizationID}/integrations/azure_devops/connections/{connectionID}"
	mux.HandleFunc("GET "+base+"/repositories", h.getAzureRepositorySelection)
	mux.HandleFunc("PUT "+base+"/repositories", h.saveAzureRepositorySelection)
	mux.HandleFunc("GET "+base+"/projects", h.listAzureHookProjects)
	mux.HandleFunc("GET "+base+"/repositories/available", h.listAzureHookRepositories)
	mux.HandleFunc("POST "+base+"/hooks/{hookID}/repair", h.repairAzureHook)
	mux.HandleFunc("POST /v1/integrations/azure_devops/hooks/{hookID}/{generation}", h.receiveAzureHook)
}
func writeAzureHookError(w http.ResponseWriter, r *http.Request, e error) {
	switch {
	case errors.Is(e, integrations.ErrAzureHookHTTPS):
		writeError(w, r, 409, "AZURE_HOOK_HTTPS_REQUIRED", "configure an HTTPS public URL before enabling service hooks")
	case errors.Is(e, integrations.ErrAzureHookCleanup):
		writeError(w, r, 409, "AZURE_HOOK_CLEANUP_REQUIRED", "the previous owned hook must be reconciled or cleaned up before it can be enabled again")
	default:
		writeAzureError(w, r, e)
	}
}
func (h *Handler) getAzureRepositorySelection(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.AzureHooksConfig
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetAzureHooksConfig(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) prepareAzureHookSource(w http.ResponseWriter, r *http.Request, mutation bool) (taskCall, integrations.AzureAccess, bool) {
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return call, integrations.AzureAccess{}, false
	}
	var access integrations.AzureAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		access, e = h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return call, access, false
	}
	return call, access, true
}
func (h *Handler) checkAzureHookSource(r *http.Request, call taskCall, access integrations.AzureAccess) error {
	return call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		current, e := h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, access.ConnectionID)
		if e != nil {
			return e
		}
		if current.ConnectionVersion != access.ConnectionVersion || current.GrantGeneration != access.GrantGeneration {
			return integrations.ErrVersionConflict
		}
		return nil
	})
}
func (h *Handler) saveAzureRepositorySelection(w http.ResponseWriter, r *http.Request) {
	call, access, ok := h.prepareAzureHookSource(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version           uint64                            `json:"version"`
		ConnectionVersion uint64                            `json:"connection_version"`
		GrantGeneration   uint64                            `json:"grant_generation"`
		Repositories      []integrations.AzureRepositoryRef `json:"repositories"`
	}
	if decodeJSONLimit(w, r, 16<<10, &in) != nil {
		writeAzureHookError(w, r, integrations.ErrInvalidRequest)
		return
	}
	if in.ConnectionVersion != access.ConnectionVersion || in.GrantGeneration != access.GrantGeneration {
		writeAzureHookError(w, r, integrations.ErrVersionConflict)
		return
	}
	verified, e := h.integrations.VerifyAzureRepositorySelection(r.Context(), h.azureRuntime(), call.organizationID, access, in.Repositories)
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	var out integrations.AzureHooksConfig
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SaveAzureRepositories(ctx, tx, call.organizationID, call.selected.Actor, access, in.Version, verified)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.repositories_updated", "integration_connection", access.ConnectionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops"})
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) listAzureHookProjects(w http.ResponseWriter, r *http.Request) {
	call, access, ok := h.prepareAzureHookSource(w, r, false)
	if !ok {
		return
	}
	var page azuredevops.ProjectPage
	_, e := h.azureRuntime().Do(r.Context(), call.organizationID, access.ConnectionID, access.ConnectionVersion, access.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		page, e = c.Projects(ctx, token, access.OrganizationName, r.URL.Query().Get("offset"))
		return e
	})
	if e == nil {
		e = h.checkAzureHookSource(r, call, access)
	}
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"projects": page.Projects, "next_offset": page.Next, "connection_version": access.ConnectionVersion, "grant_generation": access.GrantGeneration})
}
func (h *Handler) listAzureHookRepositories(w http.ResponseWriter, r *http.Request) {
	call, access, ok := h.prepareAzureHookSource(w, r, false)
	if !ok {
		return
	}
	var repos []azuredevops.Repository
	project := r.URL.Query().Get("project_id")
	_, e := h.azureRuntime().Do(r.Context(), call.organizationID, access.ConnectionID, access.ConnectionVersion, access.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		if _, e := c.Project(ctx, token, access.OrganizationName, project); e != nil {
			return e
		}
		var e error
		repos, e = c.Repositories(ctx, token, access.OrganizationName, project)
		return e
	})
	if e == nil {
		e = h.checkAzureHookSource(r, call, access)
	}
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"repositories": repos, "connection_version": access.ConnectionVersion, "grant_generation": access.GrantGeneration})
}
func (h *Handler) repairAzureHook(w http.ResponseWriter, r *http.Request) {
	call, access, ok := h.prepareAzureHookSource(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Generation uint64 `json:"generation"`
	}
	if decodeJSONLimit(w, r, 1024, &in) != nil || in.Generation == 0 || !membership.ValidateUUID(r.PathValue("hookID")) {
		writeAzureHookError(w, r, integrations.ErrInvalidRequest)
		return
	}
	metadata, e := h.integrations.VerifyAzureHookMetadata(r.Context(), h.azureRuntime(), call.organizationID, access)
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.RepairAzureHook(ctx, tx, call.organizationID, call.selected.Actor, access, r.PathValue("hookID"), in.Generation, metadata)
	})
	if e != nil {
		writeAzureHookError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"scheduled": true})
}
func (h *Handler) receiveAzureHook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("hookID")
	generation, e := strconv.ParseUint(r.PathValue("generation"), 10, 63)
	if e != nil || generation == 0 || !membership.ValidateUUID(id) {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	if !h.allowKey(w, r, h.webhookLimiter, id) {
		return
	}
	user, password, ok := r.BasicAuth()
	if !ok || len(user) > 256 || len(password) > 256 {
		writeError(w, r, 401, "UNAUTHORIZED", "webhook authentication failed")
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, integrations.AzureWebhookBodyLimit))
	if e != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(e, &tooLarge) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "webhook body exceeds 2 MiB")
		} else {
			writeError(w, r, 400, "INVALID_REQUEST", "invalid webhook body")
		}
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "NOT_READY", "database is not ready")
		return
	}
	var org, hook string
	e = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,hook_id::text FROM openfox_private.resolve_azure_hook($1::uuid,$2)`, id, generation).Scan(&org, &hook)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	if e != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var outcome integrations.GitWebhookOutcome
	e = h.runAsWorker(r.Context(), org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		outcome, e = h.integrations.ReceiveAzureHook(ctx, tx, org, hook, generation, user, password, body)
		return e
	})
	if e != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if !outcome.Found {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	if outcome.Status != 200 {
		writeError(w, r, outcome.Status, "WEBHOOK_REJECTED", "webhook authentication or event envelope is invalid")
		return
	}
	writeJSON(w, 200, map[string]bool{"accepted": true, "duplicate": outcome.Duplicate})
}
