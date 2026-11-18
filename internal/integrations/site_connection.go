package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
)

// SiteConnectionInput only carries credentials on the write request.
type SiteConnectionInput struct {
	ConnectionID string              `json:"connection_id"`
	Version      uint64              `json:"version"`
	Site         string              `json:"site_url"`
	Deployment   string              `json:"deployment"`
	Jira         *jiraapi.Auth       `json:"jira,omitempty"`
	Zen          *zentao.Credentials `json:"zen,omitempty"`
}

type SiteSource struct {
	ID      string
	Version uint64
	Grant   SiteGrant
}

// CanonicalSite compares only URL-equivalent host/default port spellings. It
// never merges path case, non-default ports, redirects or DNS aliases.
// Existing platform clients discard RawPath, so reject encoded alternate path
// spellings they cannot preserve instead of verifying a different deployment.
func CanonicalSite(raw string) (string, error) {
	u, err := safehttp.CheckURL(raw)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" {
		return "", ErrInvalidRequest
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "443" {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func siteAccount(site, identity string) string {
	sum := sha256.Sum256([]byte(site + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}

func (s Service) CheckSiteConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, provider string, in SiteConnectionInput) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return err
	}
	if !CanManage(actor.Role) {
		return ErrForbidden
	}
	if s.Box == nil {
		return ErrSecretsUnavailable
	}
	if (in.ConnectionID == "") != (in.Version == 0) || in.ConnectionID != "" && !membership.ValidateUUID(in.ConnectionID) {
		return ErrInvalidRequest
	}
	if _, err := CanonicalSite(in.Site); err != nil || len(in.Site) > 255 {
		return ErrInvalidRequest
	}
	switch provider {
	case "jira":
		if in.Jira == nil || in.Zen != nil || len(in.Jira.Secret) > 2048 || strings.ContainsAny(in.Jira.Secret, "\r\n") || len(in.Jira.Email) > 320 || len(in.Jira.Username) > 200 {
			return ErrInvalidRequest
		}
		if _, err := jiraapi.New(jiraapi.Config{Deployment: in.Deployment, SiteURL: in.Site, Auth: *in.Jira}, nil); err != nil {
			return ErrInvalidRequest
		}
	case "zentao":
		if in.Zen == nil || in.Jira != nil || in.Deployment != "" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	if in.ConnectionID != "" {
		_, err := s.LoadSiteSource(ctx, tx, org, provider, in.ConnectionID, actor, false, true)
		return err
	}
	return nil
}

func VerifySiteConnection(ctx context.Context, provider string, in SiteConnectionInput, opts safehttp.Options) (SiteGrant, error) {
	site, err := CanonicalSite(in.Site)
	if err != nil {
		return SiteGrant{}, err
	}
	g := SiteGrant{Provider: provider, Site: site}
	if provider == "jira" && in.Jira != nil {
		client := safehttp.Client(opts)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		c, err := jiraapi.New(jiraapi.Config{Deployment: in.Deployment, SiteURL: site, Auth: *in.Jira}, client)
		if err != nil {
			return g, err
		}
		u, err := c.Myself(ctx)
		if err != nil {
			return g, err
		}
		g.Identity, g.Jira = u.Identity(), in.Jira
	} else if provider == "zentao" && in.Zen != nil {
		credentials := *in.Zen
		credentials.Site = site
		c, err := zentao.ConnectWithOptions(ctx, credentials, opts)
		if err != nil {
			return g, err
		}
		g.Identity, err = c.Myself(ctx)
		if err != nil {
			return g, err
		}
		g.LoginIdentity = credentials.Account
		session := c.Session()
		g.Zen = &session
	} else {
		return g, ErrInvalidRequest
	}
	if g.Identity == "" || len(g.Identity) > 255 {
		return g, ErrNeedsReauth
	}
	return g, nil
}

// LoadSiteSource keeps credentials internal. The optional SHARE lock is acquired
// before any organization priority lock and blocks credential/disconnect races.
func (s Service) LoadSiteSource(ctx context.Context, tx pgx.Tx, org, provider, id string, actor collaboration.Actor, lock, attention bool) (SiteSource, error) {
	var out SiteSource
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	if !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	if !membership.ValidateUUID(id) || provider != "jira" && provider != "zentao" {
		return out, ErrInvalidRequest
	}
	query := `SELECT id::text,version,status,credentials_sealed FROM openfox.integration_connections
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider=$2
 AND scope='ORGANIZATION' AND status<>'DISCONNECTED' AND settings->>'auth_method'='import_site'`
	if lock {
		query += " FOR SHARE"
	}
	var status string
	var blob []byte
	err := tx.QueryRow(ctx, query, id, provider).Scan(&out.ID, &out.Version, &status, &blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if status != StatusActive && !attention {
		return out, ErrNeedsReauth
	}
	plain, err := s.open(blob, connectionAAD(org, out.ID))
	if err != nil {
		return out, err
	}
	if json.Unmarshal(plain, &out.Grant) != nil || out.Grant.Provider != provider || out.Grant.Identity == "" ||
		provider == "jira" && out.Grant.Jira == nil || provider == "zentao" && (out.Grant.Zen == nil || out.Grant.Site != out.Grant.Zen.Site) {
		return out, ErrNeedsReauth
	}
	return out, nil
}

// FindImportSite matches legacy URL hashes without rewriting identifiers used by
// existing imports. More than one legacy match requires explicit selection.
func (s Service) FindImportSite(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, grant SiteGrant) (SiteSource, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return SiteSource{}, err
	}
	if !CanManage(actor.Role) {
		return SiteSource{}, ErrForbidden
	}
	canonical, err := CanonicalSite(grant.Site)
	if err != nil {
		return SiteSource{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,external_id,settings->>'instance_url' FROM openfox.integration_connections
 WHERE organization_id=openfox_private.current_organization_id() AND provider=$1 AND scope='ORGANIZATION'
 AND status<>'DISCONNECTED' AND settings->>'auth_method'='import_site' ORDER BY id LIMIT 501`, grant.Provider)
	if err != nil {
		return SiteSource{}, err
	}
	var match string
	n := 0
	for rows.Next() {
		n++
		var id, account, site string
		if err = rows.Scan(&id, &account, &site); err != nil {
			break
		}
		key, e := CanonicalSite(site)
		if e != nil || key != canonical {
			continue
		}
		matches := account == siteAccount(site, grant.Identity) || account == siteAccount(key, grant.Identity)
		if grant.LoginIdentity != "" {
			matches = matches || account == siteAccount(site, grant.LoginIdentity) || account == siteAccount(key, grant.LoginIdentity)
		}
		if matches {
			if match != "" {
				err = ErrVersionConflict
				break
			}
			match = id
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return SiteSource{}, err
	}
	if rowErr != nil {
		return SiteSource{}, rowErr
	}
	if n > maxDetailRows {
		return SiteSource{}, ErrVersionConflict
	}
	if match == "" {
		return SiteSource{}, nil
	}
	return s.LoadSiteSource(ctx, tx, org, grant.Provider, match, actor, false, false)
}

func (s Service) insertSiteConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, g SiteGrant) (Connection, error) {
	id, err := membership.NewID()
	if err != nil {
		return Connection{}, err
	}
	key, err := CanonicalSite(g.Site)
	if err != nil {
		return Connection{}, err
	}
	plain, err := json.Marshal(g)
	if err != nil {
		return Connection{}, err
	}
	sealed, keyID, err := s.seal(plain, connectionAAD(org, id))
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,credentials_sealed,credentials_key_id,settings,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,'ORGANIZATION',$3,left($4,200),$5,$6,
 jsonb_build_object('instance_url',$7::text,'auth_method','import_site','site_identity',$8::text,'site_auth',$9::text),$10::uuid)`,
		id, g.Provider, siteAccount(key, g.Identity), g.Site+" · "+g.Identity, sealed, keyID, g.Site, g.Identity, g.authKind(), actor.UserID)
	if isUniqueViolation(err) {
		return Connection{}, ErrVersionConflict
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (g SiteGrant) authKind() string {
	if g.Jira != nil {
		return g.Jira.Kind
	}
	return "password"
}

func (s Service) SaveSiteConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, provider string, in SiteConnectionInput, g SiteGrant) (Connection, bool, error) {
	if err := s.CheckSiteConnection(ctx, tx, org, actor, provider, in); err != nil {
		return Connection{}, false, err
	}
	if in.ConnectionID == "" {
		existing, err := s.FindImportSite(ctx, tx, org, actor, g)
		if err != nil {
			return Connection{}, false, err
		}
		if existing.ID != "" {
			if _, err := s.LoadSiteSource(ctx, tx, org, provider, existing.ID, actor, true, false); err != nil {
				return Connection{}, false, err
			}
			out, err := loadConnection(ctx, tx, existing.ID)
			return out, false, err
		}
		out, err := s.insertSiteConnection(ctx, tx, org, actor, g)
		return out, err == nil, err
	}
	var version uint64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
 AND id=$1::uuid AND provider=$2 AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR UPDATE`, in.ConnectionID, provider).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, false, ErrNotFound
	}
	if err != nil {
		return Connection{}, false, err
	}
	if version != in.Version {
		return Connection{}, false, ErrVersionConflict
	}
	old, err := s.LoadSiteSource(ctx, tx, org, provider, in.ConnectionID, actor, false, true)
	if err != nil {
		return Connection{}, false, err
	}
	oldSite, _ := CanonicalSite(old.Grant.Site)
	newSite, _ := CanonicalSite(g.Site)
	if oldSite != newSite || (old.Grant.Identity != g.Identity && old.Grant.Identity != g.LoginIdentity) {
		return Connection{}, false, ErrInvalidRequest
	}
	g.Site, g.Identity = old.Grant.Site, old.Grant.Identity
	if g.Zen != nil {
		g.Zen.Site = g.Site
	}
	plain, _ := json.Marshal(g)
	blob, key, err := s.seal(plain, connectionAAD(org, old.ID))
	if err != nil {
		return Connection{}, false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$3,credentials_key_id=$4,
 status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp(),
 settings=settings||jsonb_build_object('site_identity',$5::text,'site_auth',$6::text)
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2`, old.ID, in.Version, blob, key, g.Identity, g.authKind())
	if err != nil {
		return Connection{}, false, err
	}
	if tag.RowsAffected() != 1 {
		return Connection{}, false, ErrVersionConflict
	}
	out, err := loadConnection(ctx, tx, old.ID)
	return out, err == nil, err
}
