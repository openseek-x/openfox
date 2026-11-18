package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
)

func (h *Handler) asanaSyncTypes(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, r.Method == "PUT")
	if !ok {
		return
	}
	var in struct {
		Version int64                 `json:"version"`
		Types   []imports.TypeMapping `json:"types"`
	}
	if r.Method == "PUT" && (decodeJSONLimit(w, r, 32<<10, &in) != nil || in.Version < 1) {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	var out any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		service := imports.Service{}
		id := r.PathValue("importID")
		job, e := service.Get(ctx, tx, call.organizationID, call.selected.Actor, id)
		if e != nil {
			return e
		}
		if job.Source != imports.SourceAsana || job.SpaceID == nil {
			return imports.ErrImportState
		}
		var connection string
		if e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.import_sync_links WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND state<>'DISCONNECTED'`, id).Scan(&connection); e != nil {
			return e
		}
		if _, e = h.integrations.AsanaSource(ctx, tx, call.organizationID, call.selected.Actor, connection); e != nil {
			return e
		}
		if e = authorizeImportSpace(ctx, tx, s, call, *job.SpaceID); e != nil {
			return e
		}
		if r.Method == "GET" {
			out, e = service.GetAsanaSyncTypes(ctx, tx, call.organizationID, call.selected.Actor, id)
		} else {
			out, e = service.SetAsanaSyncTypes(ctx, tx, call.organizationID, call.selected.Actor, id, in.Version, in.Types)
			if e == nil {
				e = recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.sync_configured", "import_job", id, *job.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "asana", "result": "type_mapping"})
			}
		}
		return e
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
