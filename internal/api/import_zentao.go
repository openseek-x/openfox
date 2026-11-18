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
		ConnectionID string             `json:"connection_id"`
		Credentials  zentao.Credentials `json:"credentials"`
		Project      string             `json:"project_id"`
		Space        string             `json:"space_id"`
		NewSpace     *imports.NewSpace  `json:"new_space"`
	}
	if err := decodeJSONLimit(w, r, 16<<10, &in); err != nil || (!list && (in.Project == "" || (in.Space == "") == (in.NewSpace == nil))) {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	if in.ConnectionID != "" && in.Credentials != (zentao.Credentials{}) {
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
	var site integrations.SiteSource
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if err := allow(ctx, tx, s); err != nil {
			return err
		}
		if in.ConnectionID != "" {
			var err error
			site, err = h.integrations.LoadSiteSource(ctx, tx, call.organizationID, "zentao", in.ConnectionID, call.selected.Actor, false, false)
			return err
		}
		return nil
	}); err != nil {
		writeImportError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	fail := func(err error) { writeSiteConnectionError(w, r, err) }
	var c *zentao.Client
	var err error
	identity := in.Credentials.Account
	if site.ID != "" {
		c, err = site.Grant.Zen.Client(h.importSafe)
		identity = site.Grant.Identity
	} else {
		c, err = zentao.ConnectWithOptions(ctx, in.Credentials, h.importSafe)
	}
	if err != nil {
		fail(err)
		return
	}
	// A one-off legacy import need not implement the optional profile endpoint.
	// When present it gives both entry points the server's actual account name.
	loginIdentity := identity
	if site.ID == "" && h.secrets != nil {
		if verified, e := c.Myself(ctx); e == nil {
			identity = verified
		}
		session := c.Session()
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			site, err = h.integrations.FindImportSite(ctx, tx, call.organizationID, call.selected.Actor,
				integrations.SiteGrant{Provider: "zentao", Site: session.Site, Identity: identity, LoginIdentity: loginIdentity})
			if errors.Is(err, integrations.ErrNeedsReauth) {
				site = integrations.SiteSource{}
				return nil
			}
			return err
		})
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		if site.ID != "" {
			session.Site = site.Grant.Site
			c, err = session.Client(h.importSafe)
			if err != nil {
				fail(err)
				return
			}
		}
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		fail(err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if list {
		if site.ID != "" {
			err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
				current, err := h.integrations.LoadSiteSource(ctx, tx, call.organizationID, "zentao", site.ID, call.selected.Actor, false, false)
				if err == nil && current.Version != site.Version {
					return integrations.ErrVersionConflict
				}
				return err
			})
			if err != nil {
				writeImportError(w, r, err)
				return
			}
		}
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
		parsed.Origin.Connection = site.ID
	}
	var job imports.Job
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if err := allow(ctx, tx, s); err != nil {
			return err
		}
		if site.ID != "" {
			current, err := h.integrations.LoadSiteSource(ctx, tx, call.organizationID, "zentao", site.ID, call.selected.Actor, true, false)
			if err != nil {
				return err
			}
			if current.Version != site.Version {
				return integrations.ErrVersionConflict
			}
		}
		var saveErr error
		if in.NewSpace != nil {
			job, saveErr = (imports.Service{}).UploadNewSpace(ctx, tx, call.organizationID, call.selected.Actor, *in.NewSpace, name, parsed)
		} else {
			job, saveErr = (imports.Service{}).Upload(ctx, tx, call.organizationID, call.selected.Actor, in.Space, name, parsed)
		}
		if saveErr == nil && h.secrets != nil && site.ID == "" {
			job.SyncTicket, saveErr = h.integrations.ImportSiteTicket(call.organizationID, job.ID, call.selected.Actor.UserID, integrations.SiteGrant{Provider: "zentao", Site: session.Site, Identity: identity, LoginIdentity: loginIdentity, Zen: &session})
		}
		return saveErr
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, 201, job)
}
