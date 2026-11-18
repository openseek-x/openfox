package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
)

func writeAzureImportError(w http.ResponseWriter, r *http.Request, e error) {
	var upstream *azuredevops.Error
	var retry *integrations.AzureRetry
	if errors.As(e, &upstream) || errors.As(e, &retry) || errors.Is(e, integrations.ErrAzureHookHTTPS) || errors.Is(e, integrations.ErrAzureHookCleanup) || errors.Is(e, integrations.ErrVersionConflict) || errors.Is(e, integrations.ErrNeedsReauth) || errors.Is(e, integrations.ErrAzureAppChanged) {
		writeAzureHookError(w, r, e)
		return
	}
	writeImportError(w, r, e)
}
func (h *Handler) listAzureImportProjects(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("connectionID", r.URL.Query().Get("connection_id"))
	h.listAzureHookProjects(w, r)
}
func (h *Handler) azureImportMetadata(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var access integrations.AzureAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		access, e = h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, r.URL.Query().Get("connection_id"))
		return e
	})
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	project, meta, _, e := imports.ReadAzureMetadata(r.Context(), h.azureRuntime(), call.organizationID, access, r.URL.Query().Get("project_id"), r.URL.Query().Get("priority_field"))
	if e == nil {
		e = h.checkAzureHookSource(r, call, access)
	}
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"project": project, "metadata": meta, "connection_version": access.ConnectionVersion, "grant_generation": access.GrantGeneration})
}
func (h *Handler) createAzureImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ConnectionID   string            `json:"connection_id"`
		ProjectID      string            `json:"project_id"`
		SpaceID        string            `json:"space_id"`
		NewSpace       *imports.NewSpace `json:"new_space"`
		IncludeHistory bool              `json:"include_history"`
		PriorityField  string            `json:"priority_field"`
	}
	key := r.Header.Get(idempotency.HeaderName)
	if decodeJSONLimit(w, r, 16<<10, &in) != nil || !azuredevops.ValidUUID(in.ProjectID) || len(in.PriorityField) > 256 || (in.SpaceID == "") == (in.NewSpace == nil) || !idempotency.ValidKey(key) {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	if in.NewSpace != nil {
		ns, e := imports.NormalizeNewSpace(*in.NewSpace, "")
		if e != nil {
			writeImportError(w, r, e)
			return
		}
		in.NewSpace = &ns
	}
	target := func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if in.NewSpace != nil {
			return checkNewSpacePrefix(ctx, tx, s, call, in.NewSpace.Prefix)
		}
		return authorizeImportSpace(ctx, tx, s, call, in.SpaceID)
	}
	var access integrations.AzureAccess
	var job imports.Job
	replay := false
	const operation = "import.azure.project"
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		var e error
		access, e = h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, in.ConnectionID)
		if e != nil {
			return e
		}
		if e = target(ctx, tx, s); e != nil {
			return e
		}
		claim, e := idempotency.Lookup(ctx, tx, operation, key, in)
		if e != nil {
			return e
		}
		replay = claim.Replay
		if replay {
			job, e = (imports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID)
		}
		return e
	})
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
		writeJSON(w, 200, job)
		return
	}
	project, meta, cutover, e := imports.ReadAzureMetadata(r.Context(), h.azureRuntime(), call.organizationID, access, in.ProjectID, in.PriorityField)
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	hooks, e := h.integrations.VerifyAzureHookMetadata(r.Context(), h.azureRuntime(), call.organizationID, access)
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		// Grant -> parent precedes membership, idempotency and job locks.
		if e := integrations.LockAzureHookConfiguration(ctx, tx, access); e != nil {
			return e
		}
		if _, e := h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, in.ConnectionID); e != nil {
			return e
		}
		if e := target(ctx, tx, s); e != nil {
			return e
		}
		if e := imports.LockAzureSpaceWriter(ctx, tx, in.SpaceID, call.selected.Actor.UserID); e != nil {
			return e
		}
		claim, e := idempotency.Begin(ctx, tx, operation, key, in)
		if e != nil {
			return e
		}
		if claim.Replay {
			replay = true
			job, e = (imports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID)
			return e
		}
		if e = h.integrations.EnsureAzureBoardsHooks(ctx, tx, call.organizationID, call.selected.Actor, access, in.ProjectID, hooks); e != nil {
			return e
		}
		job, e = (imports.Service{}).CreateAzureDraft(ctx, tx, call.organizationID, call.selected.Actor, imports.AzureDraftInput{Source: access, Project: project, Metadata: meta, Cutover: cutover, IncludeHistory: in.IncludeHistory, SpaceID: in.SpaceID, NewSpace: in.NewSpace})
		if e != nil {
			return e
		}
		return idempotency.Complete(ctx, tx, claim, job.ID)
	})
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	writeJSON(w, 201, job)
}
