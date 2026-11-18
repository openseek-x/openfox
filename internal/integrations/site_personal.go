package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
)

type SitePersonalInput struct {
	ConnectionID string              `json:"connection_id"`
	Version      uint64              `json:"version"`
	Jira         *jiraapi.Auth       `json:"jira,omitempty"`
	Zen          *zentao.Credentials `json:"zentao,omitempty"`
}
type SitePersonalGrant struct {
	ConnectionID                         string `json:"-"`
	Version                              uint64 `json:"-"`
	Provider, Site, Identity, Deployment string
	Jira                                 *jiraapi.Auth
	Zen                                  *zentao.Credentials
}

func (s Service) PrepareSitePersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in SitePersonalInput) (SitePersonalGrant, error) {
	var g SitePersonalGrant
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return g, e
	}
	if actor.Role == policy.OrganizationRoleGuest || !membership.ValidateUUID(in.ConnectionID) {
		return g, ErrForbidden
	}
	var sealed []byte
	e := tx.QueryRow(ctx, `SELECT provider,credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND scope='ORGANIZATION' AND status='ACTIVE' AND settings->>'auth_method'='import_site' FOR SHARE`, in.ConnectionID).Scan(&g.Provider, &sealed)
	if e != nil {
		return g, ErrNotFound
	}
	raw, e := s.open(sealed, connectionAAD(org, in.ConnectionID))
	if e != nil {
		return g, e
	}
	var parent SiteGrant
	if json.Unmarshal(raw, &parent) != nil {
		return g, ErrNeedsReauth
	}
	g.Site = parent.Site
	switch g.Provider {
	case "jira":
		if in.Jira == nil || in.Zen != nil || parent.Jira == nil {
			return g, ErrInvalidRequest
		}
		if len(in.Jira.Secret) > 2048 || strings.ContainsAny(in.Jira.Secret, "\r\n") || len(in.Jira.Email) > 320 || len(in.Jira.Username) > 200 {
			return g, ErrInvalidRequest
		}
		g.Jira = in.Jira
		g.Deployment = "server"
		if parent.Jira.Kind == jiraapi.AuthAPIToken {
			g.Deployment = "cloud"
		}
	case "zentao":
		if in.Zen == nil || in.Jira != nil || parent.Zen == nil {
			return g, ErrInvalidRequest
		}
		v := *in.Zen
		v.Site = g.Site
		v.BasicUser = parent.Zen.BasicUser
		v.BasicPassword = parent.Zen.BasicPassword
		g.Zen = &v
	default:
		return g, ErrInvalidRequest
	}
	return g, nil
}
func ValidateSitePersonal(ctx context.Context, g SitePersonalGrant, opts safehttp.Options) (SitePersonalGrant, error) {
	if g.Jira != nil {
		c, e := jiraapi.New(jiraapi.Config{Deployment: g.Deployment, SiteURL: g.Site, Auth: *g.Jira}, safehttp.Client(opts))
		if e != nil {
			return g, ErrInvalidRequest
		}
		u, e := c.Myself(ctx)
		if e != nil {
			return g, ErrNeedsReauth
		}
		g.Identity = u.Identity()
	} else if g.Zen != nil {
		if _, e := zentao.ConnectPersonal(ctx, *g.Zen, opts); e != nil {
			return g, ErrNeedsReauth
		}
		g.Identity = g.Zen.Account
	} else {
		return g, ErrInvalidRequest
	}
	if g.Identity == "" || len(g.Identity) > 255 {
		return g, ErrNeedsReauth
	}
	return g, nil
}
func (s Service) SaveSitePersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in SitePersonalInput, g SitePersonalGrant) (Connection, error) {
	current, e := s.PrepareSitePersonal(ctx, tx, org, actor, in)
	if e != nil {
		return Connection{}, e
	}
	if current.Site != g.Site || current.Provider != g.Provider {
		return Connection{}, ErrVersionConflict
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":site-comment:"+in.ConnectionID+":"+actor.UserID); e != nil {
		return Connection{}, e
	}
	var id string
	var version uint64
	e = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND scope='PERSONAL' AND status<>'DISCONNECTED' FOR UPDATE`, in.ConnectionID, actor.UserID).Scan(&id, &version)
	create := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !create {
		return Connection{}, e
	}
	if version != in.Version {
		return Connection{}, ErrVersionConflict
	}
	if create {
		id, e = membership.NewID()
		if e != nil {
			return Connection{}, e
		}
	}
	raw, e := json.Marshal(g)
	if e != nil {
		return Connection{}, e
	}
	sealed, key, e := s.seal(raw, connectionAAD(org, id))
	if e != nil {
		return Connection{}, e
	}
	if create {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,'PERSONAL',$3::uuid,$4::uuid,$5,$5,'sync_comments',$6,$7,jsonb_build_object('instance_url',$8::text,'auth_method','site_comments'),$4::uuid)`, id, g.Provider, in.ConnectionID, actor.UserID, g.Identity, sealed, key, g.Site)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET external_id=$2,display_name=$2,credentials_sealed=$3,credentials_key_id=$4,account_kind='sync_comments',settings=jsonb_build_object('instance_url',$5::text,'auth_method','site_comments'),status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, g.Identity, sealed, key, g.Site)
	}
	if e != nil {
		return Connection{}, e
	}
	return loadConnection(ctx, tx, id)
}

// A worker may only use the author's own binding for the selected parent.
func (s Service) LoadSitePersonal(ctx context.Context, tx pgx.Tx, org, parent, author string) (SitePersonalGrant, error) {
	var id string
	var sealed []byte
	var g SitePersonalGrant
	e := tx.QueryRow(ctx, `SELECT c.id::text,c.credentials_sealed,c.version FROM openfox.integration_connections c JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id JOIN openfox.integration_connections p ON p.organization_id=c.organization_id AND p.id=c.parent_connection_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.parent_connection_id=$1::uuid AND c.owner_user_id=$2::uuid AND c.scope='PERSONAL' AND c.status='ACTIVE' AND c.settings->>'auth_method'='site_comments' AND m.status='ACTIVE' AND p.status='ACTIVE' FOR SHARE OF c,p,m`, parent, author).Scan(&id, &sealed, &g.Version)
	if e != nil {
		return g, e
	}
	g.ConnectionID = id
	raw, e := s.open(sealed, connectionAAD(org, id))
	if e != nil {
		return g, e
	}
	if json.Unmarshal(raw, &g) != nil {
		return g, ErrNeedsReauth
	}
	return g, nil
}
