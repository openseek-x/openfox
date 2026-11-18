package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
)

func (h *Handler) azureSyncMappings(w http.ResponseWriter, r *http.Request) {
	h.azureSyncOperation(w, r, false)
}
func (h *Handler) azureSyncHistory(w http.ResponseWriter, r *http.Request) {
	h.azureSyncOperation(w, r, true)
}
func (h *Handler) azureSyncOperation(w http.ResponseWriter, r *http.Request, history bool) {
	call, ok := h.beginTaskCall(w, r, r.Method != "GET")
	if !ok {
		return
	}
	var in struct {
		Version    int64                     `json:"version"`
		Types      []imports.TypeMapping     `json:"types"`
		Statuses   []imports.StatusMapping   `json:"statuses"`
		Priorities []imports.PriorityMapping `json:"priorities"`
	}
	if r.Method != "GET" && (decodeJSONLimit(w, r, 512<<10, &in) != nil || in.Version < 1 || history && (len(in.Types) > 0 || len(in.Statuses) > 0 || len(in.Priorities) > 0)) {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	var out any
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		id := r.PathValue("importID")
		service := imports.Service{}
		job, e := service.Get(ctx, tx, call.organizationID, call.selected.Actor, id)
		if e != nil {
			return e
		}
		if job.Source != imports.SourceAzure || job.SpaceID == nil {
			return imports.ErrImportState
		}
		var connection string
		if e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.import_sync_links WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND state<>'DISCONNECTED'`, id).Scan(&connection); e != nil {
			return e
		}
		if _, e = h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, connection); e != nil {
			return e
		}
		if e = authorizeImportSpace(ctx, tx, s, call, *job.SpaceID); e != nil {
			return e
		}
		if e = imports.LockAzureSpaceWriter(ctx, tx, *job.SpaceID, call.selected.Actor.UserID); e != nil {
			return e
		}
		action := "mapping"
		if r.Method == "GET" {
			out, e = service.GetAzureSyncMappings(ctx, tx, call.organizationID, call.selected.Actor, id)
		} else if history {
			action = "history"
			out, e = service.RequestAzureHistory(ctx, tx, call.organizationID, call.selected.Actor, id, in.Version)
		} else {
			out, e = service.SetAzureSyncMappings(ctx, tx, call.organizationID, call.selected.Actor, id, imports.AzureSyncMappings{Version: in.Version, Types: in.Types, Statuses: in.Statuses, Priorities: in.Priorities})
		}
		if e == nil && r.Method != "GET" {
			e = recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.sync_configured", "import_job", id, *job.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops", "result": action})
		}
		return e
	})
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (h *Handler) setAzureImportSync(w http.ResponseWriter, r *http.Request, call taskCall, id, state, direction string, checkNow bool, version int64) {
	var source integrations.AzureAccess
	var project, space string
	authorize := func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		var e error
		if e = tx.QueryRow(ctx, `SELECT connection_id::text,project_id,space_id::text FROM openfox.import_sync_links WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, id).Scan(&source.ConnectionID, &project, &space); e != nil {
			return e
		}
		source, e = h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, source.ConnectionID)
		if e != nil {
			return e
		}
		return authorizeImportSpace(ctx, tx, s, call, space)
	}
	e := call.run(h, r, authorize)
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	var metadata azuredevops.HookMetadata
	if state == "ACTIVE" {
		metadata, e = h.integrations.VerifyAzureHookMetadata(r.Context(), h.azureRuntime(), call.organizationID, source)
		if e != nil {
			writeAzureImportError(w, r, e)
			return
		}
	}
	var out imports.Job
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if e := integrations.LockAzureHookConfiguration(ctx, tx, source); e != nil {
			return e
		}
		if _, e := h.integrations.AzureSource(ctx, tx, call.organizationID, call.selected.Actor, source.ConnectionID); e != nil {
			return e
		}
		if e := authorizeImportSpace(ctx, tx, s, call, space); e != nil {
			return e
		}
		if e := imports.LockAzureSpaceWriter(ctx, tx, space, call.selected.Actor.UserID); e != nil {
			return e
		}
		if state == "ACTIVE" {
			if e := h.integrations.EnsureAzureBoardsHooks(ctx, tx, call.organizationID, call.selected.Actor, source, project, metadata); e != nil {
				return e
			}
		}
		var e error
		out, e = (imports.Service{}).SetProjectSync(ctx, tx, call.organizationID, call.selected.Actor, id, state, direction, checkNow, version)
		if e != nil {
			return e
		}
		if state == "PAUSED" || state == "DISCONNECTED" {
			if e = h.integrations.ReconcileAzureBoardsHooks(ctx, tx, call.organizationID, call.selected.Actor, source, project); e != nil {
				return e
			}
		}
		result := state
		if checkNow {
			result = "check_now"
		} else if direction != "" {
			result = direction
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.sync_configured", "import_job", id, space, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops", "result": result})
	})
	if e != nil {
		writeAzureImportError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
