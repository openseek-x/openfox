package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

const ProviderGongfeng = "gongfeng"

var defaultGongfengClient = gongfeng.New()

func (s Service) gongfengClient() *gongfeng.Client {
	if s.Gongfeng != nil {
		return s.Gongfeng
	}
	return defaultGongfengClient
}

// GongfengAccess is never serialized to an API response. The current numeric
// identity, credential generation and server-owned client travel together.
type GongfengAccess struct {
	ID                   string
	Version              uint64
	CredentialGeneration int64
	IdentityID           int64
	Status, CreatedBy    string
	Client               *gongfeng.Client
	credentials          connectionCredentials
}

func (s Service) loadGongfeng(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (GongfengAccess, error) {
	return s.loadGongfengTarget(ctx, tx, org, id, lock, false)
}

// Only an explicit token replacement with the currently observed version may
// read a disconnected target. Ordinary execution still requires credentials.
func (s Service) loadGongfengTarget(ctx context.Context, tx pgx.Tx, org, id string, lock, allowDisconnected bool) (GongfengAccess, error) {
	var a GongfengAccess
	if !membership.ValidateUUID(id) {
		return a, ErrNotFound
	}
	query := `SELECT id::text,version,external_id,status,created_by::text,credentials_sealed,
 COALESCE((settings->>'gongfeng_credential_generation')::bigint,0)
 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
 AND id=$1::uuid AND provider='gongfeng' AND scope='ORGANIZATION'`
	if !allowDisconnected {
		query += " AND status<>'DISCONNECTED'"
	}
	if lock {
		query += " FOR UPDATE"
	}
	var identity string
	var blob []byte
	if err := tx.QueryRow(ctx, query, id).Scan(&a.ID, &a.Version, &identity, &a.Status, &a.CreatedBy, &blob, &a.CredentialGeneration); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	var err error
	a.IdentityID, err = strconv.ParseInt(identity, 10, 64)
	if err != nil || a.IdentityID <= 0 || a.CredentialGeneration < 1 {
		return a, ErrSecretsUnavailable
	}
	if a.Status == StatusDisconnected && allowDisconnected {
		a.Client = s.gongfengClient()
		return a, nil
	}
	a.credentials, err = s.openCredentials(org, a.ID, blob)
	if err != nil {
		return a, err
	}
	if !validGongfengPAT(a.credentials.AccessToken) || a.credentials.WebhookToken == "" || a.credentials.WebhookSecret == "" {
		return a, ErrSecretsUnavailable
	}
	a.Client = s.gongfengClient()
	return a, nil
}

func validGongfengPAT(token string) bool {
	if len(token) < 1 || len(token) > 4096 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}

type GongfengTokenInput struct {
	ConnectionID string `json:"connection_id,omitempty"`
	Version      uint64 `json:"version,omitempty"`
	AccessToken  string `json:"access_token"`
}
type GongfengTokenPlan struct {
	input    GongfengTokenInput
	previous *GongfengAccess
}

// Prepare/Validate/Save deliberately separate authorization, upstream I/O and
// the final lock+CAS. A late successful identity response cannot revive a
// disconnected connection or overwrite credentials changed concurrently.
func (s Service) PrepareGongfengToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in GongfengTokenInput) (GongfengTokenPlan, error) {
	plan := GongfengTokenPlan{input: in}
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return plan, err
	}
	if s.Box == nil {
		return plan, ErrSecretsUnavailable
	}
	if !validGongfengPAT(in.AccessToken) || (in.ConnectionID == "") != (in.Version == 0) {
		return plan, ErrInvalidRequest
	}
	if in.ConnectionID != "" {
		a, err := s.loadGongfengTarget(ctx, tx, org, in.ConnectionID, false, true)
		if err != nil {
			return plan, err
		}
		if a.Version != in.Version {
			return plan, ErrVersionConflict
		}
		plan.previous = &a
	}
	return plan, nil
}
func (s Service) ValidateGongfengToken(ctx context.Context, plan GongfengTokenPlan) (gongfeng.User, error) {
	user, err := s.gongfengClient().Identity(ctx, plan.input.AccessToken)
	if err != nil {
		return user, err
	}
	if len(user.Username) > 200 || strings.TrimSpace(user.Username) == "" {
		return gongfeng.User{}, ErrUpstreamUnavailable
	}
	if plan.previous != nil && user.ID != plan.previous.IdentityID {
		return gongfeng.User{}, ErrNeedsReauth
	}
	return user, nil
}
func (s Service) SaveGongfengToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, plan GongfengTokenPlan, user gongfeng.User) (Connection, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	if !validGongfengPAT(plan.input.AccessToken) || user.ID <= 0 || user.State != "active" || strings.TrimSpace(user.Username) == "" || len(user.Username) > 200 {
		return Connection{}, ErrInvalidRequest
	}
	id := plan.input.ConnectionID
	creds := connectionCredentials{AccessToken: plan.input.AccessToken, Scope: "api"}
	generation := int64(1)
	if plan.previous != nil {
		a, err := s.loadGongfengTarget(ctx, tx, org, id, true, true)
		if err != nil {
			return Connection{}, err
		}
		if a.Version != plan.input.Version || a.Version != plan.previous.Version || a.Status != plan.previous.Status {
			return Connection{}, ErrVersionConflict
		}
		if a.IdentityID != user.ID {
			return Connection{}, ErrNeedsReauth
		}
		creds.WebhookToken, creds.WebhookSecret = a.credentials.WebhookToken, a.credentials.WebhookSecret
		generation = a.CredentialGeneration + 1
	} else {
		if id != "" || plan.input.Version != 0 {
			return Connection{}, ErrInvalidRequest
		}
		var err error
		id, err = membership.NewID()
		if err != nil {
			return Connection{}, err
		}
	}
	if creds.WebhookToken == "" {
		var err error
		creds.WebhookToken, err = randomBase64URL(32)
		if err != nil {
			return Connection{}, err
		}
		creds.WebhookSecret, err = randomBase64URL(32)
		if err != nil {
			return Connection{}, err
		}
	}
	blob, key, err := s.sealCredentials(org, id, creds)
	if err != nil {
		return Connection{}, err
	}
	if plan.previous == nil {
		settings, _ := json.Marshal(map[string]any{"auth_method": "personal_token", "instance_url": gongfeng.Origin, "gongfeng_credential_generation": generation})
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections
   (organization_id,id,provider,scope,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,settings,created_by,webhook_token_hash)
   VALUES(openfox_private.current_organization_id(),$1::uuid,'gongfeng','ORGANIZATION',$2,$3,'user',$4,$5,$6::jsonb,$7::uuid,$8)`, id, formatID(user.ID), user.Username, blob, key, settings, actor.UserID, TokenHash(creds.WebhookToken))
	} else {
		tag, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,
   settings=jsonb_set(settings,'{gongfeng_credential_generation}',to_jsonb($4::bigint)),display_name=$5,status='ACTIVE',status_reason='',disconnected_at=NULL,webhook_token_hash=$7,
   version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id()
   AND id=$1::uuid AND version=$6 AND status=$8`, id, blob, key, generation, user.Username, plan.input.Version, TokenHash(creds.WebhookToken), plan.previous.Status)
		if e != nil {
			return Connection{}, e
		}
		if tag.RowsAffected() != 1 {
			return Connection{}, ErrVersionConflict
		}
		// Freeze existing in-flight sends rather than reset their original author
		// or body. The shared parent lock fences their eventual local completion.
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET status='PAUSED',status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'credential_changed' END,
   generation=generation+1,hook_state=CASE WHEN hook_state IN ('SENDING','UNKNOWN') THEN 'UNKNOWN' ELSE 'PENDING' END,
   hook_next_attempt_at=clock_timestamp(),hook_scan='{}'::jsonb,
   lease_token=CASE WHEN hook_state='SENDING' THEN lease_token ELSE NULL END,
   lease_expires_at=CASE WHEN hook_state='SENDING' THEN lease_expires_at ELSE NULL END,updated_at=clock_timestamp()
   WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND removed_at IS NULL`, id)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'credential_changed' END,
   version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id()
   AND connection_id=$1::uuid AND deleted_at IS NULL`, id)
		}
		if err == nil && plan.previous.Status == StatusDisconnected {
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='PENDING',next_attempt_at=clock_timestamp(),last_error_code='',consecutive_failures=0 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status='CANCELLED' AND backlink_state='UNKNOWN'`, id)
		}
	}
	if isUniqueViolation(err) {
		return Connection{}, ErrVersionConflict
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (s Service) PrepareGongfeng(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (GongfengAccess, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return GongfengAccess{}, err
	}
	return s.loadGongfeng(ctx, tx, org, id, false)
}
