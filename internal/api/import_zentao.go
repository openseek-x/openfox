package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/imports/zentao"
	"openfox/internal/integrations"
)

func (h *Handler) zentaoImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if !imports.CanManage(call.selected.Actor.Role) {
		writeImportError(w, r, imports.ErrForbidden)
		return
	}
	list := strings.HasSuffix(r.URL.Path, "/zentao-sources")
	var in struct {
		Credentials zentao.Credentials `json:"credentials"`
		Project     string             `json:"project_id"`
		Space       string             `json:"space_id"`
		NewSpace    *imports.NewSpace  `json:"new_space"`
	}
	if err := decodeJSONLimit(w, r, 16<<10, &in); err != nil || (!list && (in.Project == "" || (in.Space == "") == (in.NewSpace == nil))) {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	if in.NewSpace != nil {
		v, err := imports.NormalizeNewSpace(*in.NewSpace, "")
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		in.NewSpace = &v
	}
	allow := func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if err := s.CheckActor(ctx, tx, call.organizationID, call.selected.Actor); err != nil {
			return err
		}
		if list {
			return nil
		}
		if in.NewSpace != nil {
			return checkNewSpacePrefix(ctx, tx, s, call, in.NewSpace.Prefix)
		}
		return authorizeImportSpace(ctx, tx, s, call, in.Space)
	}
	if err := call.run(h, r, allow); err != nil {
		writeImportError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	fail := func(err error) {
		if errors.Is(err, zentao.ErrInput) {
			writeImportError(w, r, imports.ErrInvalidRequest)
			return
		}
		if errors.Is(err, zentao.ErrAuth) {
			writeError(w, r, 422, "SOURCE_AUTH_FAILED", "source rejected the credentials")
			return
		}
		writeError(w, r, 502, "UPSTREAM_UNAVAILABLE", "could not read the ZenTao instance")
	}
	c, err := zentao.Connect(ctx, in.Credentials)
	if err != nil {
		fail(err)
		return
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		fail(err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if list {
		writeJSON(w, 200, projects)
		return
	}
	name := ""
	for _, p := range projects {
		if p.ID == in.Project {
			name = p.Name
			break
		}
	}
	if name == "" {
		writeError(w, r, 404, "NOT_FOUND", "source is unavailable")
		return
	}
	rows, err := c.Rows(ctx, in.Project)
	if err != nil {
		fail(err)
		return
	}
	parsed, err := parseProjectRows("zentao_api", rows)
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	session := c.Session()
	if h.secrets != nil {
		parsed.Origin = imports.ImportSiteOrigin(session.Site, in.Project)
	}
	var job imports.Job
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if err := allow(ctx, tx, s); err != nil {
			return err
		}
		var saveErr error
		if in.NewSpace != nil {
			job, saveErr = (imports.Service{}).UploadNewSpace(ctx, tx, call.organizationID, call.selected.Actor, *in.NewSpace, name, parsed)
		} else {
			job, saveErr = (imports.Service{}).Upload(ctx, tx, call.organizationID, call.selected.Actor, in.Space, name, parsed)
		}
		if saveErr == nil && h.secrets != nil {
			job.SyncTicket, saveErr = h.integrations.ImportSiteTicket(call.organizationID, job.ID, call.selected.Actor.UserID, integrations.SiteGrant{Provider: "zentao", Site: session.Site, Identity: in.Credentials.Account, Zen: &session})
		}
		return saveErr
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, 201, job)
}
