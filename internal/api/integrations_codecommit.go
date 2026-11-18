package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/codecommit"
)

func (h *Handler) registerCodeCommitRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /v1/hooks/git/codecommit/{bridgeID}", h.receiveCodeCommitBridge)
	base := "/v1/organizations/{organizationID}/integrations"
	m.HandleFunc("GET "+base+"/codecommit/regions", h.codeCommitRegions)
	m.HandleFunc("POST "+base+"/codecommit/connections", h.saveCodeCommitCredentials)
	c := base + "/connections/{connectionID}/codecommit"
	m.HandleFunc("PUT "+c+"/credentials", h.saveCodeCommitCredentials)
	m.HandleFunc("GET "+c+"/configuration", h.codeCommitConfiguration)
	m.HandleFunc("GET "+c+"/repositories", h.listCodeCommitRepositories)
	m.HandleFunc("POST "+c+"/repositories", h.saveCodeCommitRepository)
	m.HandleFunc("POST "+c+"/repositories/{repositoryID}/{action}", h.manageCodeCommitRepository)
	m.HandleFunc("GET "+c+"/work", h.listCodeCommitWork)
	m.HandleFunc("POST "+c+"/work/{workID}/reconcile", h.reconcileCodeCommitWork)
}
func writeCodeCommitError(w http.ResponseWriter, r *http.Request, e error) {
	var limited *codecommit.Error
	switch {
	case errors.Is(e, codecommit.ErrCredentials):
		writeError(w, r, 422, "CODECOMMIT_CREDENTIALS_INVALID", "AWS credentials are invalid or expired")
	case errors.Is(e, codecommit.ErrForbidden):
		writeError(w, r, 403, "CODECOMMIT_FORBIDDEN", "AWS denied the requested permission")
	case errors.Is(e, codecommit.ErrKMS):
		writeError(w, r, 422, "CODECOMMIT_KMS_UNAVAILABLE", "repository encryption key is unavailable")
	case errors.Is(e, codecommit.ErrRateLimited):
		wait := time.Minute
		if errors.As(e, &limited) && limited.RetryAfter > 0 {
			wait = limited.RetryAfter
		}
		if wait > 5*time.Minute {
			wait = 5 * time.Minute
		}
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeError(w, r, 429, "CODECOMMIT_RATE_LIMITED", "AWS request limit reached; retry later")
	case errors.Is(e, codecommit.ErrIdentityMismatch), errors.Is(e, codecommit.ErrNotFound):
		writeError(w, r, 400, "CODECOMMIT_REPOSITORY_INVALID", "repository does not match the connected account and region")
	case errors.Is(e, codecommit.ErrInvalid):
		writeError(w, r, 400, "INVALID_REQUEST", "request or response is invalid")
	case errors.Is(e, codecommit.ErrUnavailable), errors.Is(e, codecommit.ErrIncomplete), errors.Is(e, codecommit.ErrUnknownResult):
		writeError(w, r, 502, "CODECOMMIT_UNAVAILABLE", "AWS could not complete the request")
	default:
		writeIntegrationError(w, r, e)
	}
}
func (h *Handler) codeCommitCallError(ctx context.Context, org string, a integrations.CodeCommitAccess, e error) error {
	if errors.Is(e, codecommit.ErrCredentials) {
		if x := h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkCodeCommitRevoked(ctx, tx, a.ID, a.Version)
		}); x != nil {
			return x
		}
	}
	return e
}
func codeCommitAudit(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request, org string, actor collaboration.Actor, action, id string) error {
	return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), org, actor, "integration.codecommit_"+action, "integration_connection", id, "", actor.AuthorizationVersion, nil)
}
func (h *Handler) codeCommitRegions(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, svc collaboration.Service) error {
		if e := svc.CheckActor(ctx, tx, call.organizationID, call.selected.Actor); e != nil {
			return e
		}
		if !integrations.CanManage(call.selected.Actor.Role) {
			return integrations.ErrForbidden
		}
		return nil
	})
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"regions": codecommit.Regions()})
}
func (h *Handler) saveCodeCommitCredentials(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var in integrations.CodeCommitCredentialInput
	if decodeJSON(w, r, &in) != nil {
		writeCodeCommitError(w, r, integrations.ErrInvalidRequest)
		return
	}
	id := r.PathValue("connectionID")
	var p integrations.CodeCommitCredentialPlan
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		p, e = h.integrations.PrepareCodeCommitCredentials(ctx, tx, call.organizationID, call.selected.Actor, id, in)
		return e
	})
	var v integrations.CodeCommitVerifiedCredentials
	if e == nil {
		v, e = h.integrations.VerifyCodeCommitCredentials(r.Context(), p)
	}
	var out integrations.Connection
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveCodeCommitCredentials(ctx, tx, call.organizationID, call.selected.Actor, p, v)
			if e != nil {
				return e
			}
			return codeCommitAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "credentials_saved", out.ID)
		})
	}
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	status := 200
	if id == "" {
		status = 201
	}
	writeJSON(w, status, out)
}
func (h *Handler) codeCommitConfiguration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.CodeCommitConfiguration
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.CodeCommitConfiguration(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("repository_after"))
		return e
	})
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) listCodeCommitRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var a integrations.CodeCommitAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		a, e = h.integrations.PrepareCodeCommit(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	var cursor string
	var page codecommit.RepositoryPage
	if e == nil {
		cursor, e = h.integrations.DecodeCodeCommitCursor(call.organizationID, a, r.URL.Query().Get("cursor"))
	}
	if e == nil {
		page, e = a.ListRepositories(r.Context(), cursor)
		if e != nil {
			e = h.codeCommitCallError(r.Context(), call.organizationID, a, e)
		}
	}
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			return h.integrations.CheckCodeCommitVersion(ctx, tx, call.organizationID, call.selected.Actor, a)
		})
	}
	if e == nil {
		cursor, e = h.integrations.EncodeCodeCommitCursor(call.organizationID, a, page.NextToken)
	}
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	type option struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	items := []option{}
	for _, repo := range page.Repositories {
		items = append(items, option{repo.ID, repo.Name})
	}
	writeJSON(w, 200, map[string]any{"repositories": items, "next_cursor": cursor, "connection_version": a.Version})
}
func (h *Handler) saveCodeCommitRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	var in integrations.CodeCommitRepositoryInput
	if decodeJSON(w, r, &in) != nil || !in.ConfirmInitialization || !codecommit.ValidRepositoryName(in.Name) {
		writeCodeCommitError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var a integrations.CodeCommitAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		a, e = h.integrations.PrepareCodeCommit(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		if e == nil && a.Version != in.ConnectionVersion {
			return integrations.ErrVersionConflict
		}
		return e
	})
	var verified codecommit.Repository
	if e == nil {
		verified, e = a.VerifyRepository(r.Context(), in.Name)
		if e != nil {
			e = h.codeCommitCallError(r.Context(), call.organizationID, a, e)
		}
	}
	var out integrations.CodeCommitBridgeResult
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveCodeCommitRepository(ctx, tx, call.organizationID, call.selected.Actor, a, in, verified)
			if e != nil {
				return e
			}
			return codeCommitAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "repository_enabled", a.ID)
		})
	}
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 201, out)
}
func (h *Handler) manageCodeCommitRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	action := r.PathValue("action")
	if action == "bridge-token" {
		action = "rotate"
	}
	if action != "rotate" && action != "disable" && action != "reconcile" {
		writeCodeCommitError(w, r, integrations.ErrNotFound)
		return
	}
	var in integrations.CodeCommitVersionInput
	if decodeJSON(w, r, &in) != nil {
		writeCodeCommitError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.CodeCommitBridgeResult
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		id := r.PathValue("connectionID")
		out, e = h.integrations.ManageCodeCommitRepository(ctx, tx, call.organizationID, call.selected.Actor, id, r.PathValue("repositoryID"), action, in)
		if e != nil {
			return e
		}
		return codeCommitAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "repository_"+action, id)
	})
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) listCodeCommitWork(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.CodeCommitJobsPage
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.ListCodeCommitJobs(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("before"))
		return e
	})
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) reconcileCodeCommitWork(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.CodeCommitReconcileInput
	if decodeJSON(w, r, &in) != nil {
		writeCodeCommitError(w, r, integrations.ErrInvalidRequest)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		id := r.PathValue("connectionID")
		if e := h.integrations.RetryCodeCommitWork(ctx, tx, call.organizationID, call.selected.Actor, id, r.PathValue("workID"), in); e != nil {
			return e
		}
		return codeCommitAudit(ctx, tx, w, r, call.organizationID, call.selected.Actor, "work_reconciled", id)
	})
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"reconciled": true})
}
