package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/qq"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func qqAAD(org, id, purpose string) string { return "qq:" + org + ":" + id + ":" + purpose }
func (s Service) QQConfigured() bool {
	u, e := url.Parse(s.PublicURL)
	return s.Box != nil && e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func qqActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (collaboration.Actor, error) {
	var id string
	e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return collaboration.Actor{}, ErrForbidden
	}
	if e != nil {
		return collaboration.Actor{}, e
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, actor.UserID)
	if errors.Is(e, membership.ErrAuthorizationStale) {
		return collaboration.Actor{}, ErrForbidden
	}
	if e != nil {
		return collaboration.Actor{}, e
	}
	a := collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	if a.Role == policy.OrganizationRoleGuest || actor.AuthorizationVersion > 0 && actor.AuthorizationVersion != a.AuthorizationVersion {
		return a, ErrForbidden
	}
	return a, nil
}

const qqBotColumns = `q.bot_id::text,q.connection_id::text,b.agent_id::text,COALESCE(b.space_id::text,''),c.display_name,COALESCE(q.app_id,''),q.state,q.status_reason,q.version,q.generation,q.credential_version,q.url_verified_at IS NOT NULL,q.created_at,b.removed_at IS NOT NULL`

func scanQQBot(row pgx.Row) (QQBot, error) {
	var v QQBot
	e := row.Scan(&v.ID, &v.ConnectionID, &v.AgentID, &v.SpaceID, &v.Name, &v.AppID, &v.State, &v.StatusReason, &v.Version, &v.Generation, &v.CredentialVersion, &v.URLVerified, &v.CreatedAt, &v.removed)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func loadQQBot(ctx context.Context, tx pgx.Tx, id string) (QQBot, error) {
	if !membership.ValidateUUID(id) {
		return QQBot{}, ErrNotFound
	}
	return scanQQBot(tx.QueryRow(ctx, `SELECT `+qqBotColumns+` FROM openfox.qq_bot_controls q JOIN openfox.im_bots b ON b.organization_id=q.organization_id AND b.id=q.bot_id JOIN openfox.integration_connections c ON c.organization_id=q.organization_id AND c.id=q.connection_id WHERE q.organization_id=openfox_private.current_organization_id() AND q.bot_id=$1::uuid`, id))
}

// Every mutating path uses the execution gate's order: bot, connection, control;
// then group epoch and actual identity if needed. Never lock Run/conversation here.
func lockQQBot(ctx context.Context, tx pgx.Tx, id string) (QQBot, error) {
	v, e := loadQQBot(ctx, tx, id)
	if e != nil {
		return v, e
	}
	var got string
	for _, q := range []struct{ sql, id string }{{`SELECT id::text FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, v.ID}, {`SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, v.ConnectionID}, {`SELECT bot_id::text FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid FOR UPDATE`, v.ID}} {
		if e = tx.QueryRow(ctx, q.sql, q.id).Scan(&got); e != nil {
			return v, e
		}
	}
	return loadQQBot(ctx, tx, id)
}
func qqBotManager(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, b QQBot) error {
	return agentBindCheck(ctx, tx, actor, b.AgentID)
}

// Stopping an integration remains possible after its Agent/space is archived.
// This does not authorize a new bind, conversation, or execution.
func qqBotStopManager(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, b QQBot) error {
	var owner, visibility string
	e := tx.QueryRow(ctx, `SELECT COALESCE(owner_user_id::text,''),visibility FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.AgentID).Scan(&owner, &visibility)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if owner == actor.UserID || CanManage(actor.Role) {
		return nil
	}
	if visibility == "PRIVATE" {
		return ErrNotFound
	}
	return ErrForbidden
}

func (s Service) GetQQBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (QQBot, error) {
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBot{}, e
	}
	b, e := loadQQBot(ctx, tx, id)
	if e != nil {
		return b, e
	}
	e = qqBotManager(ctx, tx, a, b)
	b.CanManage = e == nil
	return b, e
}

func (s Service) CreateQQBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in QQCreateBotInput) (QQBot, error) {
	if !s.QQConfigured() {
		return QQBot{}, ErrNotConfigured
	}
	if !membership.ValidateUUID(in.AgentID) || !utf8.ValidString(in.Name) || len([]rune(in.Name)) < 1 || len([]rune(in.Name)) > 200 || strings.TrimSpace(in.Name) != in.Name || len(in.IdempotencyKey) < 16 || len(in.IdempotencyKey) > 128 {
		return QQBot{}, ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBot{}, e
	}
	// Serialize the same operation key across different Agent choices without
	// upgrading a member SHARE lock (two concurrent upgrades can deadlock).
	// Database uniqueness remains the final idempotency/Agent-slot constraint.
	var locked string
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "qq-create:"+org+":"+a.UserID+":"+in.IdempotencyKey); e != nil {
		return QQBot{}, e
	}
	raw, _ := json.Marshal([]string{in.AgentID, in.Name})
	sum := sha256.Sum256(raw)
	var id string
	var previous []byte
	e = tx.QueryRow(ctx, `SELECT bot_id::text,request_hash FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND created_by=$1::uuid AND operation_key=$2`, a.UserID, in.IdempotencyKey).Scan(&id, &previous)
	if e == nil {
		if !bytes.Equal(previous, sum[:]) {
			return QQBot{}, ErrVersionConflict
		}
		return s.GetQQBot(ctx, tx, org, a, id)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return QQBot{}, e
	}
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, in.AgentID).Scan(&locked); errors.Is(e, pgx.ErrNoRows) {
		return QQBot{}, ErrNotFound
	} else if e != nil {
		return QQBot{}, e
	}
	if e = agentBindCheck(ctx, tx, a, in.AgentID); e != nil {
		return QQBot{}, e
	}
	var space string
	if e = tx.QueryRow(ctx, `SELECT COALESCE(space_id::text,'') FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, in.AgentID).Scan(&space); e != nil {
		return QQBot{}, e
	}
	if space == "" {
		return QQBot{}, ErrInvalidRequest
	}
	var occupied bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid AND removed_at IS NULL)`, in.AgentID).Scan(&occupied); e != nil {
		return QQBot{}, e
	}
	if occupied {
		return QQBot{}, ErrVersionConflict
	}
	bot, e := membership.NewID()
	if e != nil {
		return QQBot{}, e
	}
	conn, e := membership.NewID()
	if e != nil {
		return QQBot{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,status_reason,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'qq','ORGANIZATION',$2,$3,'agent','NEEDS_ATTENTION','credentials_pending',$4::uuid)`, conn, "qq:"+bot, in.Name, a.UserID)
	if e != nil {
		return QQBot{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'AGENT',$3::uuid,$4::uuid,$5::uuid)`, bot, conn, in.AgentID, space, a.UserID)
	if isUniqueViolation(e) {
		return QQBot{}, ErrVersionConflict
	}
	if e != nil {
		return QQBot{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_bot_controls(organization_id,bot_id,connection_id,created_by,operation_key,request_hash) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5)`, bot, conn, a.UserID, in.IdempotencyKey, sum[:])
	if e != nil {
		return QQBot{}, e
	}
	v, e := loadQQBot(ctx, tx, bot)
	v.CanManage = true
	return v, e
}

func (s Service) PrepareQQConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in QQConnectInput) (QQConnectWork, error) {
	var out QQConnectWork
	if !s.QQConfigured() {
		return out, ErrNotConfigured
	}
	if in.Version == 0 || !(qq.Credentials{AppID: in.AppID, AppSecret: in.AppSecret}).Valid() {
		return out, ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	b, e := lockQQBot(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if e = qqBotManager(ctx, tx, a, b); e != nil {
		return out, e
	}
	if b.removed || b.Version != in.Version || b.State == "VALIDATING" || (b.AppID != "" && b.AppID != in.AppID) {
		return out, ErrVersionConflict
	}
	var qrActive bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','POLLING','WAITING_SCAN'))`, id).Scan(&qrActive); e != nil {
		return out, e
	}
	if qrActive {
		return out, ErrVersionConflict
	}
	lease, e := randomBase64URL(24)
	if e != nil {
		return out, e
	}
	if e = invalidateQQAuthorization(ctx, tx, b.ID, "credentials_changed"); e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='VALIDATING',status_reason='',version=version+1,generation=generation+1,credential_version=credential_version+1,url_verified_at=NULL,activated_at=NULL,webhook_hash=NULL,webhook_sealed=NULL,webhook_key_id='',token_sealed=NULL,token_key_id='',token_expires_at=NULL,refresh_after=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,lease_owner=$2,lease_expires_at=clock_timestamp()+interval '20 seconds',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id, lease)
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='credentials_pending',disconnected_at=NULL,credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID)
	if e != nil {
		return out, e
	}
	return QQConnectWork{OrganizationID: org, BotID: id, ConnectionID: b.ConnectionID, ActorID: a.UserID, ActorVersion: a.AuthorizationVersion, Version: b.Version + 1, Generation: b.Generation + 1, Lease: lease, Credentials: qq.Credentials{AppID: in.AppID, AppSecret: in.AppSecret}}, nil
}
func ExecuteQQConnection(ctx context.Context, client *qq.Client, work QQConnectWork) QQConnectResult {
	t, e := client.AccessToken(ctx, work.Credentials)
	return QQConnectResult{Token: t, Err: e}
}

func (s Service) FinishQQConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, w QQConnectWork, result QQConnectResult) (QQBot, error) {
	if org != w.OrganizationID || actor.UserID != w.ActorID {
		return QQBot{}, ErrForbidden
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBot{}, e
	}
	if a.AuthorizationVersion != w.ActorVersion {
		return QQBot{}, ErrForbidden
	}
	b, e := lockQQBot(ctx, tx, w.BotID)
	if e != nil {
		return b, e
	}
	if e = qqBotManager(ctx, tx, a, b); e != nil {
		return b, e
	}
	var current bool
	e = tx.QueryRow(ctx, `SELECT state='VALIDATING' AND version=$2 AND generation=$3 AND lease_owner=$4 AND lease_expires_at>clock_timestamp() FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, w.Version, w.Generation, w.Lease).Scan(&current)
	if e != nil {
		return b, e
	}
	if !current || b.removed {
		return b, ErrVersionConflict
	}
	if result.Err != nil || !result.Token.ExpiresAt.After(time.Now()) || result.Token.AccessToken == "" {
		reason := "credentials_unavailable"
		if errors.Is(result.Err, qq.ErrAuthentication) {
			reason = "credentials_rejected"
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='NEEDS_REAUTH',status_reason=$2,version=version+1,lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, reason)
		if e != nil {
			return b, e
		}
		return loadQQBot(ctx, tx, b.ID)
	}
	plain, _ := json.Marshal(w.Credentials)
	credential, key, e := s.seal(plain, qqAAD(org, b.ID, "credentials"))
	if e != nil {
		return b, e
	}
	plain, _ = json.Marshal(result.Token)
	token, tokenKey, e := s.seal(plain, qqAAD(org, b.ID, "token"))
	if e != nil {
		return b, e
	}
	route, e := randomBase64URL(32)
	if e != nil {
		return b, e
	}
	sealed, routeKey, e := s.seal([]byte(route), qqAAD(org, b.ID, "webhook"))
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET app_id=$2,state='WAITING_WEBHOOK',status_reason='webhook_pending',version=version+1,webhook_hash=$3,webhook_sealed=$4,webhook_key_id=$5,token_sealed=$6,token_key_id=$7,token_expires_at=$8,refresh_after=$8::timestamptz-interval '30 seconds',lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, w.Credentials.AppID, TokenHash(route), sealed, routeKey, token, tokenKey, result.Token.ExpiresAt)
	if isUniqueViolation(e) {
		return b, ErrVersionConflict
	}
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,status_reason='webhook_pending',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID, credential, key)
	if e != nil {
		return b, e
	}
	b, e = loadQQBot(ctx, tx, b.ID)
	b.CanManage = true
	return b, e
}

// Revocation touches provider state only. Never acquire Agent/conversation/Run
// locks after these epoch/identity locks; running work is fenced by its snapshot.
func invalidateQQAuthorization(ctx context.Context, tx pgx.Tx, bot, reason string) error {
	queries := []string{
		`UPDATE openfox.qq_group_epochs SET disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND disabled_at IS NULL`,
		`UPDATE openfox.qq_bindings SET active=false,revoked_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND active`,
		`UPDATE openfox.qq_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND closed_at IS NULL`,
		`UPDATE openfox.qq_binding_sessions SET status='CANCELLED',finished_at=clock_timestamp(),version=version+1,code_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','OBSERVED')`,
		`UPDATE openfox.qq_replies SET state=CASE WHEN state='SENDING' THEN 'UNKNOWN' ELSE 'SKIPPED' END,status_reason=$2,version=version+1,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND state IN ('PENDING','SENDING')`,
	}
	for i, q := range queries {
		args := []any{bot}
		if i == len(queries)-1 {
			args = append(args, reason)
		}
		if _, e := tx.Exec(ctx, q, args...); e != nil {
			return e
		}
	}
	return nil
}

func (s Service) DisconnectQQBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (QQBot, error) {
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBot{}, e
	}
	b, e := lockQQBot(ctx, tx, id)
	if e != nil {
		return b, e
	}
	if e = qqBotStopManager(ctx, tx, a, b); e != nil {
		return b, e
	}
	if b.Version != version {
		return b, ErrVersionConflict
	}
	if b.State == "DISCONNECTED" {
		b.CanManage = true
		return b, nil
	}
	if e = invalidateQQAuthorization(ctx, tx, id, "disconnected"); e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status=CASE WHEN status='PENDING' THEN 'CANCELLED' ELSE 'UNKNOWN' END,status_reason='disconnected',qr_url='',version=version+1,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','POLLING','WAITING_SCAN')`, id)
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='DISCONNECTED',status_reason='',version=version+1,generation=generation+1,credential_version=credential_version+1,webhook_hash=NULL,webhook_sealed=NULL,webhook_key_id='',url_verified_at=NULL,activated_at=NULL,token_sealed=NULL,token_key_id='',token_expires_at=NULL,refresh_after=NULL,lease_owner='',lease_expires_at=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id)
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='',disconnected_at=clock_timestamp(),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID)
	if e != nil {
		return b, e
	}
	b, e = loadQQBot(ctx, tx, id)
	b.CanManage = true
	return b, e
}

func (s Service) RevealQQWebhook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (string, error) {
	b, e := s.GetQQBot(ctx, tx, org, actor, id)
	if e != nil {
		return "", e
	}
	if b.Version != version || (b.State != "WAITING_WEBHOOK" && b.State != "ACTIVE") {
		return "", ErrVersionConflict
	}
	var blob []byte
	e = tx.QueryRow(ctx, `SELECT webhook_sealed FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id).Scan(&blob)
	if e != nil {
		return "", e
	}
	raw, e := s.open(blob, qqAAD(org, id, "webhook"))
	if e != nil {
		return "", e
	}
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/hooks/qq/" + string(raw), nil
}
