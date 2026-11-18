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
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
)

func (h *Handler) registerGitCodeRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /v1/hooks/git/gitcode/{token}", gitRoute(integrations.ProviderGitCode, h.receiveGitHubWebhook))
	root := "/v1/organizations/{organizationID}/integrations"
	m.HandleFunc("POST "+root+"/gitcode/organizations", h.discoverGitCode)
	m.HandleFunc("POST "+root+"/gitcode/connections", h.saveGitCodeToken)
	base := root + "/connections/{connectionID}/gitcode"
	m.HandleFunc("PUT "+base+"/credentials", h.saveGitCodeToken)
	m.HandleFunc("GET "+base+"/configuration", h.getGitCodeConfiguration)
	m.HandleFunc("GET "+base+"/repositories", h.listGitCodeRepositories)
	m.HandleFunc("POST "+base+"/repositories", h.createGitCodeRepository)
	m.HandleFunc("DELETE "+base+"/repositories/{repositoryID}", h.removeGitCodeRepository)
	m.HandleFunc("POST "+base+"/repositories/{repositoryID}/history", h.startGitCodeHistory)
	m.HandleFunc("POST "+base+"/work/{workID}/reconcile", h.reconcileGitCode)
}
func mapGitCodeError(e error) error {
	var limited *gitapi.ErrRateLimited
	switch {
	case e == nil:
		return nil
	case errors.Is(e, gitapi.ErrTokenRevoked):
		return integrations.ErrNeedsReauth
	case errors.Is(e, gitcode.ErrForbidden):
		return integrations.ErrForbidden
	case errors.Is(e, gitapi.ErrNotFound):
		return integrations.ErrInvalidRepository
	case errors.Is(e, gitcode.ErrUnsupported):
		return integrations.ErrInvalidRequest
	case errors.Is(e, gitcode.ErrInvalid), errors.Is(e, gitcode.ErrIncomplete), errors.Is(e, gitapi.ErrUnknownResult), errors.Is(e, gitapi.ErrRequestFailed), errors.As(e, &limited):
		return integrations.ErrUpstreamUnavailable
	default:
		return e
	}
}
func (h *Handler) gitcodeCallError(ctx context.Context, org string, a integrations.GitCodeAccess, e error) error {
	if errors.Is(e, gitapi.ErrTokenRevoked) {
		if x := h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkGitCodeRevoked(ctx, tx, a.ID, a.Version)
		}); x != nil {
			return x
		}
	}
	return mapGitCodeError(e)
}
func gitcodeAudit(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request, org string, actor collaboration.Actor, action, id string) error {
	return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), org, actor, action, "integration_connection", id, "", actor.AuthorizationVersion, map[string]string{"source": "gitcode"})
}

type gitCodeOrganizationOption struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (h *Handler) discoverGitCode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.GitCodeTokenInput
	if decodeJSON(w, r, &in) != nil || in.ConnectionID != "" || in.Version != 0 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	in.ExternalOrganizationID = "1" // syntax-only plan; discovery never persists a selection.
	check := func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		_, e := h.integrations.PrepareGitCodeToken(ctx, tx, call.organizationID, call.selected.Actor, in)
		return e
	}
	e := call.run(h, r, check)
	var principal gitcode.Principal
	var organizations []gitcode.Organization
	if e == nil {
		principal, organizations, e = h.integrations.DiscoverGitCode(r.Context(), in.AccessToken)
	}
	if e == nil {
		e = call.run(h, r, check)
	}
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	options := make([]gitCodeOrganizationOption, 0, len(organizations))
	for _, o := range organizations {
		options = append(options, gitCodeOrganizationOption{ID: strconv.FormatInt(o.ID, 10), Login: o.Login, Name: o.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal": principal, "organizations": options})
}
func (h *Handler) saveGitCodeToken(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.GitCodeTokenInput
	if decodeJSON(w, r, &in) != nil || in.ConnectionID != "" {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	in.ConnectionID = r.PathValue("connectionID")
	var plan integrations.GitCodeTokenPlan
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		plan, e = h.integrations.PrepareGitCodeToken(ctx, tx, call.organizationID, call.selected.Actor, in)
		return e
	})
	var verified integrations.GitCodeVerifiedToken
	if e == nil {
		verified, e = h.integrations.VerifyGitCodeToken(r.Context(), plan)
	}
	var out integrations.Connection
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveGitCodeToken(ctx, tx, call.organizationID, call.selected.Actor, plan, verified)
			if e != nil {
				return e
			}
			return gitcodeAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "integration.gitcode_credentials_saved", out.ID)
		})
	}
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) getGitCodeConfiguration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.GitCodeConfiguration
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GitCodeConfiguration(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), integrations.GitCodeConfigurationPage{RepositoryAfter: r.URL.Query().Get("repository_after"), WorkBefore: r.URL.Query().Get("work_before")})
		return e
	})
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) listGitCodeRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	page := 1
	var e error
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, e = strconv.Atoi(raw)
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if e != nil || page < 1 || page > gitcode.MaxPages || len(q) > 200 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var a integrations.GitCodeAccess
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		a, e = h.integrations.PrepareGitCode(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	var list []integrations.RepositoryItem
	var more bool
	if e == nil {
		list, more, e = a.ListRepositories(r.Context(), q, page)
		if e != nil {
			e = h.gitcodeCallError(r.Context(), call.organizationID, a, e)
		}
	}
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			current, e := h.integrations.PrepareGitCode(ctx, tx, call.organizationID, call.selected.Actor, a.ID)
			if e == nil && current.Version != a.Version {
				e = integrations.ErrVersionConflict
			}
			return e
		})
	}
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": list, "has_more": more})
}
func (h *Handler) createGitCodeRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.GitCodeCreateSyncInput
	if decodeJSON(w, r, &in) != nil || in.Version == 0 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var a integrations.GitCodeAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		a, e = h.integrations.PrepareGitCode(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e == nil && a.Version != in.Version {
		e = integrations.ErrVersionConflict
	}
	var repo gitcode.Repository
	if e == nil {
		repo, e = a.ValidateRepository(r.Context(), in.Repository, in.RepositoryID)
		if e != nil {
			e = h.gitcodeCallError(r.Context(), call.organizationID, a, e)
		}
	}
	var out integrations.SyncLink
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.InsertGitCodeSync(ctx, tx, call.organizationID, call.selected.Actor, a, in, repo)
			if e != nil {
				return e
			}
			return gitcodeAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "integration.gitcode_repository_added", a.ID)
		})
	}
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) removeGitCodeRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version    uint64 `json:"version"`
		DeleteHook bool   `json:"delete_remote_hook"`
	}
	if decodeJSON(w, r, &in) != nil || in.Version == 0 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if e := h.integrations.RemoveGitCodeRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), in.Version, in.DeleteHook); e != nil {
			return e
		}
		return gitcodeAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "integration.gitcode_repository_removed", r.PathValue("connectionID"))
	})
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) startGitCodeHistory(w http.ResponseWriter, r *http.Request) {
	h.gitcodeMaintenance(w, r, true)
}
func (h *Handler) reconcileGitCode(w http.ResponseWriter, r *http.Request) {
	h.gitcodeMaintenance(w, r, false)
}
func (h *Handler) gitcodeMaintenance(w http.ResponseWriter, r *http.Request, history bool) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version uint64 `json:"version"`
	}
	if decodeJSON(w, r, &in) != nil || in.Version == 0 {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		action := "integration.gitcode_reconcile_requested"
		if history {
			action = "integration.gitcode_history_requested"
			e = h.integrations.StartGitCodeHistory(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), in.Version)
		} else {
			e = h.integrations.ReconcileGitCodeWork(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("workID"), in.Version)
		}
		if e != nil {
			return e
		}
		return gitcodeAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, action, r.PathValue("connectionID"))
	})
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) receiveGitCode(w http.ResponseWriter, r *http.Request, lookup integrations.GitWebhookLookup, body []byte) {
	var event integrations.GitCodeEvent
	e := h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		event, e = h.integrations.PrepareGitCodeEvent(ctx, tx, lookup, r.PathValue("token"), r.Header, body)
		return e
	})
	if errors.Is(e, integrations.ErrGitCodeSignature) {
		writeError(w, r, 401, "SIGNATURE_REJECTED", "webhook password is missing or invalid")
		return
	}
	if errors.Is(e, integrations.ErrGitCodeCommitLimit) {
		writeError(w, r, 422, "GITCODE_COMMIT_LIMIT", "push is incomplete or exceeds 100 commits; no commits were linked")
		return
	}
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	if event.Duplicate {
		writeJSON(w, 200, map[string]bool{"duplicate": true})
		return
	}
	data, upstream := event.Hydrate(r.Context())
	duplicate := false
	e = h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		duplicate, e = h.integrations.CompleteGitCodeEvent(ctx, tx, lookup.OrganizationID, event, data, upstream)
		return e
	})
	if e != nil {
		writeIntegrationError(w, r, mapGitCodeError(e))
		return
	}
	if !duplicate && upstream != nil {
		writeIntegrationError(w, r, mapGitCodeError(upstream))
		return
	}
	writeJSON(w, 200, map[string]bool{"duplicate": duplicate, "accepted": true})
}
