package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/zohobugtracker"
	"strconv"
)

func (h *Handler) listZohoProjects(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var source integrations.ZohoAccess
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		source, e = h.integrations.ZohoSource(ctx, tx, call.organizationID, call.selected.Actor, r.URL.Query().Get("connection_id"))
		return e
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	index := 1
	if raw := r.URL.Query().Get("index"); raw != "" {
		index, err = strconv.Atoi(raw)
		if err != nil || index < 1 || index > 1000000 {
			writeZohoError(w, r, integrations.ErrInvalidRequest)
			return
		}
	}
	var page zohobugtracker.ProjectPage
	_, err = h.zohoRuntime().Do(r.Context(), call.organizationID, source.ConnectionID, source.ConnectionVersion, source.CredentialGeneration, func(token string) error {
		var e error
		page, e = h.integrations.Zoho.ProjectsPage(r.Context(), token, source.PortalID, index)
		return e
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	// A completed HTTP request does not extend the actor's or connection's
	// authorization. Discard late list results after disconnect or revocation.
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		latest, e := h.integrations.ZohoSource(ctx, tx, call.organizationID, call.selected.Actor, source.ConnectionID)
		if e != nil {
			return e
		}
		if latest.ConnectionVersion != source.ConnectionVersion || latest.CredentialGeneration != source.CredentialGeneration {
			return integrations.ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	type projectView struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	projects := []projectView{}
	for _, p := range page.Projects {
		projects = append(projects, projectView{ID: string(p.ID), Name: p.Name, Key: string(p.ID)})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		Projects  []projectView `json:"projects"`
		NextIndex int           `json:"next_index"`
	}{projects, page.NextIndex})
}
func (h *Handler) createZohoImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ConnectionID string            `json:"connection_id"`
		ProjectID    string            `json:"project_id"`
		SpaceID      string            `json:"space_id"`
		NewSpace     *imports.NewSpace `json:"new_space"`
	}
	key := r.Header.Get(idempotency.HeaderName)
	if decodeJSONLimit(w, r, 16<<10, &in) != nil || !zohobugtracker.ValidID(in.ProjectID) || len(in.ProjectID) > 128 || (in.SpaceID == "") == (in.NewSpace == nil) || key == "" || !idempotency.ValidKey(key) {
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
	var source integrations.ZohoAccess
	var job imports.Job
	replay := false
	const operation = "import.zoho_bugtracker.project"
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		var e error
		source, e = h.integrations.ZohoSource(ctx, tx, call.organizationID, call.selected.Actor, in.ConnectionID)
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
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
		writeJSON(w, 200, job)
		return
	}
	var project zohobugtracker.Project
	_, err = h.zohoRuntime().Do(r.Context(), call.organizationID, in.ConnectionID, source.ConnectionVersion, source.CredentialGeneration, func(token string) error {
		var e error
		project, e = h.integrations.Zoho.Project(r.Context(), token, source.PortalID, in.ProjectID)
		return e
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	if project.Status != "active" {
		writeZohoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		claim, e := idempotency.Begin(ctx, tx, operation, key, in)
		if e != nil {
			return e
		}
		if _, e = h.integrations.ZohoSource(ctx, tx, call.organizationID, call.selected.Actor, in.ConnectionID); e != nil {
			return e
		}
		if e = target(ctx, tx, s); e != nil {
			return e
		}
		if claim.Replay {
			replay = true
			job, e = (imports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID)
			return e
		}
		job, e = (imports.Service{}).CreateZohoDraft(ctx, tx, call.organizationID, call.selected.Actor, imports.ZohoDraftInput{Source: source, Project: project, SpaceID: in.SpaceID, NewSpace: in.NewSpace})
		if e != nil {
			return e
		}
		return idempotency.Complete(ctx, tx, claim, job.ID)
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	writeJSON(w, 201, job)
}
