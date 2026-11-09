package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/membership"
)

// SiteGrant is only serialized inside an authenticated encryption envelope.
type SiteGrant struct {
	Provider, Site, Identity string
	Jira                     *jiraapi.Auth   `json:",omitempty"`
	Zen                      *zentao.Session `json:",omitempty"`
}

func (g SiteGrant) account() string {
	sum := sha256.Sum256([]byte(g.Site + "\x00" + g.Identity))
	return hex.EncodeToString(sum[:])
}
func ticketAAD(org, job, actor string) string {
	return "import-site-binding:" + org + ":" + job + ":" + actor
}

// A verified import can offer its current account to the sync step without
// retaining its password in a database draft or returning plaintext credentials.
func (s Service) ImportSiteTicket(org, job, actor string, g SiteGrant) (string, error) {
	plain, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	blob, _, err := s.seal(plain, ticketAAD(org, job, actor))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}
func (s Service) BindImportSite(ctx context.Context, tx pgx.Tx, org, job string, actor collaboration.Actor, ticket string) error {
	if !membership.ValidateUUID(job) || len(ticket) > 32<<10 {
		return ErrInvalidRequest
	}
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return e
	}
	if !CanManage(actor.Role) {
		return ErrForbidden
	}
	var src, author, status string
	var opted bool
	var created time.Time
	err := tx.QueryRow(ctx, `SELECT source,created_by::text,status,COALESCE((config->'options'->>'continuous_sync')::boolean,false),created_at FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, job).Scan(&src, &author, &status, &opted, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != actor.UserID {
		return ErrForbidden
	}
	if status != "DRAFT" {
		return nil
	} // The import start handler checks the terminal state.
	if !opted || time.Since(created) > 24*time.Hour {
		return ErrInvalidRequest
	}
	blob, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		return ErrInvalidRequest
	}
	plain, err := s.open(blob, ticketAAD(org, job, actor.UserID))
	if err != nil {
		return ErrInvalidRequest
	}
	var g SiteGrant
	if json.Unmarshal(plain, &g) != nil || !((g.Provider == "jira" && g.Jira != nil && (src == "jira_cloud" || src == "jira_server")) || (g.Provider == "zentao" && g.Zen != nil && src == "zentao_api")) {
		return ErrInvalidRequest
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider=$1 AND scope='ORGANIZATION' AND lower(external_id)=$2 AND status='ACTIVE' FOR SHARE`, g.Provider, g.account()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = membership.NewID()
		if err != nil {
			return err
		}
		sealed, key, err := s.seal(plain, connectionAAD(org, id))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,credentials_sealed,credentials_key_id,settings,created_by)
         VALUES(openfox_private.current_organization_id(),$1::uuid,$2,'ORGANIZATION',$3,left($4,255),$5,$6,jsonb_build_object('instance_url',$7::text,'auth_method','import_site'),$8::uuid)`, id, g.Provider, g.account(), g.Site, sealed, key, g.Site, actor.UserID)
		if isUniqueViolation(err) {
			return ErrVersionConflict
		}
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=jsonb_set(preview,'{project_origin,connection}',to_jsonb($2::text)) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job, id)
	return err
}
func (s Service) LoadImportSite(ctx context.Context, tx pgx.Tx, org, provider, id string, actor collaboration.Actor) (SiteGrant, error) {
	var g SiteGrant
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return g, e
	}
	if !CanManage(actor.Role) {
		return g, ErrForbidden
	}
	var blob []byte
	err := tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider=$2 AND scope='ORGANIZATION' AND status='ACTIVE' AND settings->>'auth_method'='import_site'`, id, provider).Scan(&blob)
	if err != nil {
		return g, err
	}
	raw, err := s.open(blob, connectionAAD(org, id))
	if err != nil {
		return g, err
	}
	if json.Unmarshal(raw, &g) != nil || g.Provider != provider {
		return g, ErrNeedsReauth
	}
	return g, nil
}
