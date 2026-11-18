package integrations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

const telegramBindingColumns = `id::text,bot_id::text,status,version,expires_at,candidate_user_id,candidate_name,COALESCE(identity_id::text,'')`

func scanTelegramBinding(row pgx.Row) (TelegramBindingSession, error) {
	var v TelegramBindingSession
	err := row.Scan(&v.ID, &v.BotID, &v.Status, &v.Version, &v.ExpiresAt, &v.CandidateUserID, &v.CandidateName, &v.IdentityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s Service) GetTelegramBindingSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TelegramBindingSession, error) {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return TelegramBindingSession{}, err
	}
	if !membership.ValidateUUID(id) {
		return TelegramBindingSession{}, ErrNotFound
	}
	return scanTelegramBinding(tx.QueryRow(ctx, `SELECT `+telegramBindingColumns+` FROM openfox.telegram_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, id, actor.UserID))
}
func (s Service) IssueTelegramBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) (TelegramBindingSession, error) {
	var out TelegramBindingSession
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	r, err := loadTelegramControl(ctx, tx, botID, true)
	if err != nil {
		return out, err
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return out, ErrTelegramSessionState
	}
	if err = telegramCheckBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	// A new code supersedes pending candidates, not a confirmed identity.
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND status IN ('WAITING','CANDIDATE')`, botID, actor.UserID)
	if err != nil {
		return out, err
	}
	code, err := randomBase64URL(32)
	if err != nil {
		return out, err
	}
	id, err := membership.NewID()
	if err != nil {
		return out, err
	}
	out, err = scanTelegramBinding(tx.QueryRow(ctx, `INSERT INTO openfox.telegram_binding_sessions(organization_id,id,bot_id,owner_user_id,actor_version,authorization_generation,code_hash,expires_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,clock_timestamp()+interval '10 minutes') RETURNING `+telegramBindingColumns, id, botID, actor.UserID, actor.AuthorizationVersion, r.authGeneration, TokenHash(code)))
	if err == nil {
		out.StartURL = "https://t.me/" + r.bot.Username + "?start=bind_" + code
	}
	return out, err
}

// consumeTelegramBinding is reachable only after authentic webhook verification.
// It stores a candidate and never creates a trusted identity. Neither code nor
// the incoming /start text is stored in the generic inbound queue.
func consumeTelegramBinding(ctx context.Context, tx pgx.Tx, r telegramControl, m telegram.Message, arg string) error {
	if m.Chat.Type != "private" || m.From == nil || m.From.IsBot || m.Chat.ID <= 0 || m.ID <= 0 || !strings.HasPrefix(arg, "bind_") {
		return ErrTelegramSessionState
	}
	code := strings.TrimPrefix(arg, "bind_")
	if len(code) != 43 {
		return ErrTelegramSessionState
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='CANDIDATE',candidate_user_id=$3,candidate_chat_id=$4,candidate_name=$5,consumed_at=clock_timestamp(),version=version+1
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND code_hash=$2 AND status='WAITING' AND expires_at>clock_timestamp() AND authorization_generation=$6`,
		r.bot.ID, TokenHash(code), int64(m.From.ID), int64(m.Chat.ID), telegramName(m.From.FirstName, m.From.LastName), r.authGeneration)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrTelegramSessionState
	}
	return nil
}
func telegramName(first, last string) string {
	v := strings.TrimSpace(first + " " + last)
	if len([]rune(v)) > 200 {
		v = string([]rune(v)[:200])
	}
	return v
}

const telegramIdentityColumns = `id::text,bot_id::text,remote_user_id,display_name,active,version,confirmed_at`

func scanTelegramIdentity(row pgx.Row) (TelegramIdentity, error) {
	var v TelegramIdentity
	err := row.Scan(&v.ID, &v.BotID, &v.RemoteUserID, &v.DisplayName, &v.Active, &v.Version, &v.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s Service) TelegramPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) ([]TelegramIdentity, error) {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+telegramIdentityColumns+` FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND owner_user_id=$1::uuid AND active ORDER BY confirmed_at,id LIMIT 201`, actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramIdentity{}
	for rows.Next() {
		v, err := scanTelegramIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 200 {
		return nil, ErrInvalidRequest
	}
	return out, nil
}
func (s Service) ConfirmTelegramBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (TelegramIdentity, error) {
	var out TelegramIdentity
	peek, err := s.GetTelegramBindingSession(ctx, tx, org, actor, id)
	if err != nil {
		return out, err
	}
	r, err := loadTelegramControl(ctx, tx, peek.BotID, true)
	if err != nil {
		return out, err
	}
	if peek.Status == "COMPLETED" {
		return scanTelegramIdentity(tx.QueryRow(ctx, `SELECT `+telegramIdentityColumns+` FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, peek.IdentityID, actor.UserID))
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return out, ErrTelegramSessionState
	}
	if err = telegramCheckBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	if version == 0 || peek.Version != version || peek.Status != "CANDIDATE" || !time.Now().Before(peek.ExpiresAt) {
		return out, ErrTelegramSessionState
	}
	var remote, chat telegram.ID
	var name string
	var authGen int64
	var actorVersion uint64
	err = tx.QueryRow(ctx, `SELECT candidate_user_id,candidate_chat_id,candidate_name,authorization_generation,actor_version FROM openfox.telegram_binding_sessions
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid AND status='CANDIDATE' AND version=$3 AND expires_at>clock_timestamp()`, id, actor.UserID, version).Scan(&remote, &chat, &name, &authGen, &actorVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrTelegramSessionState
	}
	if err != nil {
		return out, err
	}
	if authGen != r.authGeneration || actorVersion != actor.AuthorizationVersion {
		return out, ErrTelegramSessionState
	}
	// Create before epoch/identity locks: Create takes Agent/conversation FK locks.
	// Any later conflict rolls back this fresh conversation with the transaction.
	conversationID, err := newTelegramConversation(ctx, tx, org, actor, r, false)
	if err != nil {
		return out, err
	}
	// Replacing a binding revokes every group it authorized. Lock epochs first.
	if err = closeTelegramIdentityEpochs(ctx, tx, r.bot.ID, actor.UserID, ""); err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_identities SET active=false,version=version+1,disabled_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND active`, r.bot.ID, actor.UserID)
	if err != nil {
		return out, err
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	identityID, err := membership.NewID()
	if err != nil {
		return out, err
	}
	out, err = scanTelegramIdentity(tx.QueryRow(ctx, `INSERT INTO openfox.telegram_identities(organization_id,id,bot_id,owner_user_id,remote_user_id,private_chat_id,display_name,authorization_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7) RETURNING `+telegramIdentityColumns, identityID, r.bot.ID, actor.UserID, int64(remote), int64(chat), name, r.authGeneration))
	if isUniqueViolation(err) {
		return out, ErrPersonalInUse
	}
	if err != nil {
		return out, err
	}
	if err = saveTelegramRoute(ctx, tx, r, actor.UserID, conversationID, chat, identityID, ""); err != nil {
		return out, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='COMPLETED',identity_id=$2::uuid,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='CANDIDATE' AND version=$3 AND expires_at>clock_timestamp()`, id, identityID, version)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() != 1 {
		return out, ErrVersionConflict
	}
	if err = telegramAudit(ctx, tx, org, actor, "identity_confirmed", identityID); err != nil {
		return out, err
	}
	return out, nil
}
func newTelegramConversation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r telegramControl, group bool) (string, error) {
	id, err := membership.NewID()
	if err != nil {
		return "", err
	}
	visibility := conversations.VisibilityPrivate
	if group {
		visibility = conversations.VisibilityShared
	}
	_, err = (conversations.Service{}).Create(ctx, tx, org, conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}, conversations.CreateInput{ID: id, SpaceID: r.spaceID, AgentID: r.bot.Agent.ID, Visibility: visibility, Origin: conversations.OriginIM})
	return id, err
}
func saveTelegramRoute(ctx context.Context, tx pgx.Tx, r telegramControl, actor, conversation string, chat telegram.ID, identity, epoch string) error {
	kind := "p2p"
	if epoch != "" {
		kind = "group"
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.telegram_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND closed_at IS NULL`, r.bot.ID, int64(chat))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_routes(organization_id,bot_id,chat_id,kind,conversation_id,identity_id,identity_version,epoch_id,authorization_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,NULLIF($5,'')::uuid,CASE WHEN $5='' THEN NULL ELSE 1 END,NULLIF($6,'')::uuid,$7)`, r.bot.ID, int64(chat), kind, conversation, identity, epoch, r.authGeneration)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5::uuid)
 ON CONFLICT(organization_id,bot_id,chat_id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id,created_by=EXCLUDED.created_by,created_at=clock_timestamp()`, r.bot.ID, formatID(int64(chat)), kind, conversation, actor)
	return err
}

// No existing conversation, Agent or Run is locked after these epoch locks.
func closeTelegramIdentityEpochs(ctx context.Context, tx pgx.Tx, botID, owner, identity string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled
 AND ($2='' OR authorized_by=NULLIF($2,'')::uuid) AND ($3='' OR identity_id=NULLIF($3,'')::uuid) ORDER BY id FOR UPDATE`, botID, owner, identity)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_group_epochs SET enabled=false,version=version+1,disabled_at=clock_timestamp(),background_enabled=false WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_routes r SET closed_at=clock_timestamp() WHERE r.organization_id=openfox_private.current_organization_id() AND r.bot_id=$1::uuid AND r.closed_at IS NULL
 AND (r.epoch_id=ANY($2::uuid[]) OR r.identity_id IN (SELECT i.id FROM openfox.telegram_identities i WHERE i.organization_id=r.organization_id AND i.bot_id=r.bot_id AND ($3='' OR i.owner_user_id=NULLIF($3,'')::uuid) AND ($4='' OR i.id=NULLIF($4,'')::uuid)))`, botID, ids, owner, identity)
	return err
}
func (s Service) UnbindTelegram(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return err
	}
	if !membership.ValidateUUID(id) || version == 0 {
		return ErrInvalidRequest
	}
	var botID string
	err := tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, id, actor.UserID).Scan(&botID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = loadTelegramControl(ctx, tx, botID, true); err != nil {
		return err
	}
	if err = closeTelegramIdentityEpochs(ctx, tx, botID, actor.UserID, id); err != nil {
		return err
	}
	var current uint64
	var active bool
	err = tx.QueryRow(ctx, `SELECT version,active FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&current, &active)
	if err != nil {
		return err
	}
	if current != version {
		return ErrVersionConflict
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	if !active {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_identities SET active=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND status IN ('WAITING','CANDIDATE')`, botID, actor.UserID)
	if err != nil {
		return err
	}
	return telegramAudit(ctx, tx, org, actor, "identity_unbound", id)
}
