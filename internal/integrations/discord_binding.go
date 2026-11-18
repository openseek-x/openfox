package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
)

const discordBindingColumns = `id::text,bot_id::text,status,version,expires_at,candidate_user_id::text,candidate_name,COALESCE(identity_id::text,'')`

func scanDiscordBinding(row pgx.Row) (DiscordBindingSession, error) {
	var v DiscordBindingSession
	err := row.Scan(&v.ID, &v.BotID, &v.Status, &v.Version, &v.ExpiresAt, &v.CandidateUserID, &v.CandidateName, &v.IdentityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s Service) GetDiscordBindingSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (DiscordBindingSession, error) {
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return DiscordBindingSession{}, err
	}
	if !membership.ValidateUUID(id) {
		return DiscordBindingSession{}, ErrNotFound
	}
	return scanDiscordBinding(tx.QueryRow(ctx, `SELECT `+discordBindingColumns+` FROM openfox.discord_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, id, actor.UserID))
}
func (s Service) IssueDiscordBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) (DiscordBindingSession, error) {
	var out DiscordBindingSession
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	r, err := loadDiscordControl(ctx, tx, botID, true)
	if err != nil {
		return out, err
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return out, ErrDiscordSessionState
	}
	if err = discordCheckBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	if err = discordFinalBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	// A new code supersedes pending candidates, not a confirmed identity.
	_, err = tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND status IN ('WAITING','CANDIDATE')`, botID, actor.UserID)
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
	out, err = scanDiscordBinding(tx.QueryRow(ctx, `INSERT INTO openfox.discord_binding_sessions(organization_id,id,bot_id,owner_user_id,actor_version,authorization_generation,code_hash,expires_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,clock_timestamp()+interval '10 minutes') RETURNING `+discordBindingColumns, id, botID, actor.UserID, actor.AuthorizationVersion, r.authGeneration, TokenHash(code)))
	if err == nil {
		out.Code = "/bind " + code
	}
	return out, err
}

// consumeDiscordBinding is called only after a fenced Gateway receipt and an
// authenticated DM channel/recipient check. It records a candidate, never an
// identity; the one-time code is only stored as a hash.
func consumeDiscordBinding(ctx context.Context, tx pgx.Tx, r discordControl, m discord.Message, codeHash []byte) error {
	if m.GuildID != nil || m.Author.Bot || m.WebhookID != nil || !m.ID.Valid() || !m.ChannelID.Valid() || !m.Author.ID.Valid() || len(codeHash) != 32 {
		return ErrDiscordSessionState
	}
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET status='CANDIDATE',candidate_user_id=$3,candidate_channel_id=$4,candidate_name=$5,consumed_at=clock_timestamp(),version=version+1
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND code_hash=$2 AND status='WAITING' AND expires_at>clock_timestamp() AND authorization_generation=$6`, r.bot.ID, codeHash, string(m.Author.ID), string(m.ChannelID), name, r.authGeneration)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrDiscordSessionState
	}
	return nil
}

const discordIdentityColumns = `id::text,bot_id::text,remote_user_id::text,display_name,active,version,confirmed_at`

func scanDiscordIdentity(row pgx.Row) (DiscordIdentity, error) {
	var v DiscordIdentity
	err := row.Scan(&v.ID, &v.BotID, &v.RemoteUserID, &v.DisplayName, &v.Active, &v.Version, &v.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s Service) DiscordPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) ([]DiscordIdentity, error) {
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+discordIdentityColumns+` FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND owner_user_id=$1::uuid AND active ORDER BY confirmed_at,id LIMIT 201`, actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscordIdentity{}
	for rows.Next() {
		v, err := scanDiscordIdentity(rows)
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
func (s Service) ConfirmDiscordBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (DiscordIdentity, error) {
	var out DiscordIdentity
	peek, err := s.GetDiscordBindingSession(ctx, tx, org, actor, id)
	if err != nil {
		return out, err
	}
	r, err := loadDiscordControl(ctx, tx, peek.BotID, true)
	if err != nil {
		return out, err
	}
	peek, err = s.GetDiscordBindingSession(ctx, tx, org, actor, id)
	if err != nil {
		return out, err
	}
	if peek.Status == "COMPLETED" {
		return scanDiscordIdentity(tx.QueryRow(ctx, `SELECT `+discordIdentityColumns+` FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, peek.IdentityID, actor.UserID))
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return out, ErrDiscordSessionState
	}
	if err = discordCheckBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	if version == 0 || peek.Version != version || peek.Status != "CANDIDATE" || !time.Now().Before(peek.ExpiresAt) {
		return out, ErrDiscordSessionState
	}
	var remote, chat discord.ID
	var name string
	var authGen int64
	var actorVersion uint64
	err = tx.QueryRow(ctx, `SELECT candidate_user_id,candidate_channel_id,candidate_name,authorization_generation,actor_version FROM openfox.discord_binding_sessions
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid AND status='CANDIDATE' AND version=$3 AND expires_at>clock_timestamp()`, id, actor.UserID, version).Scan(&remote, &chat, &name, &authGen, &actorVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDiscordSessionState
	}
	if err != nil {
		return out, err
	}
	if authGen != r.authGeneration || actorVersion != actor.AuthorizationVersion {
		return out, ErrDiscordSessionState
	}
	// Create before epoch/identity locks: Create takes Agent/conversation FK locks.
	// Any later conflict rolls back this fresh conversation with the transaction.
	var conversationID string
	if r.bot.Kind == BotKindAgent {
		conversationID, err = newDiscordConversation(ctx, tx, org, actor, r, false)
		if err != nil {
			return out, err
		}
	}
	// Replacing a binding revokes every group it authorized. Lock epochs first.
	if err = closeDiscordIdentityEpochs(ctx, tx, r.bot.ID, actor.UserID, ""); err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.discord_identities SET active=false,version=version+1,disabled_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND active`, r.bot.ID, actor.UserID)
	if err != nil {
		return out, err
	}
	if err = discordFinalBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	identityID, err := membership.NewID()
	if err != nil {
		return out, err
	}
	out, err = scanDiscordIdentity(tx.QueryRow(ctx, `INSERT INTO openfox.discord_identities(organization_id,id,bot_id,owner_user_id,remote_user_id,private_channel_id,display_name,authorization_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7) RETURNING `+discordIdentityColumns, identityID, r.bot.ID, actor.UserID, string(remote), string(chat), name, r.authGeneration))
	if isUniqueViolation(err) {
		return out, ErrPersonalInUse
	}
	if err != nil {
		return out, err
	}
	if conversationID != "" {
		if err = saveDiscordRoute(ctx, tx, r, actor.UserID, conversationID, chat, "", identityID, ""); err != nil {
			return out, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET status='COMPLETED',identity_id=$2::uuid,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='CANDIDATE' AND version=$3 AND expires_at>clock_timestamp()`, id, identityID, version)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() != 1 {
		return out, ErrVersionConflict
	}
	if err = discordAudit(ctx, tx, org, actor, "identity_confirmed", identityID); err != nil {
		return out, err
	}
	return out, nil
}
func newDiscordConversation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r discordControl, group bool) (string, error) {
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
func saveDiscordRoute(ctx context.Context, tx pgx.Tx, r discordControl, actor, conversation string, chat discord.ID, guild, identity, epoch string) error {
	kind := "p2p"
	if epoch != "" {
		kind = "group"
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.discord_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND closed_at IS NULL`, r.bot.ID, string(chat))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.discord_routes(organization_id,bot_id,channel_id,guild_id,kind,conversation_id,identity_id,identity_version,epoch_id,authorization_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,NULLIF($8,''),$3,$4::uuid,NULLIF($5,'')::uuid,CASE WHEN $5='' THEN NULL ELSE 1 END,NULLIF($6,'')::uuid,$7)`, r.bot.ID, string(chat), kind, conversation, identity, epoch, r.authGeneration, guild)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5::uuid)
 ON CONFLICT(organization_id,bot_id,chat_id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id,created_by=EXCLUDED.created_by,created_at=clock_timestamp()`, r.bot.ID, string(chat), kind, conversation, actor)
	return err
}

// No existing conversation, Agent or Run is locked after these epoch locks.
func closeDiscordIdentityEpochs(ctx context.Context, tx pgx.Tx, botID, owner, identity string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled
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
		_, err = tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET enabled=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.discord_routes r SET closed_at=clock_timestamp() WHERE r.organization_id=openfox_private.current_organization_id() AND r.bot_id=$1::uuid AND r.closed_at IS NULL
 AND (r.epoch_id=ANY($2::uuid[]) OR r.identity_id IN (SELECT i.id FROM openfox.discord_identities i WHERE i.organization_id=r.organization_id AND i.bot_id=r.bot_id AND ($3='' OR i.owner_user_id=NULLIF($3,'')::uuid) AND ($4='' OR i.id=NULLIF($4,'')::uuid)))`, botID, ids, owner, identity)
	return err
}
func (s Service) UnbindDiscord(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return err
	}
	if !membership.ValidateUUID(id) || version == 0 {
		return ErrInvalidRequest
	}
	var botID string
	err := tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid`, id, actor.UserID).Scan(&botID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = loadDiscordControl(ctx, tx, botID, true); err != nil {
		return err
	}
	if err = closeDiscordIdentityEpochs(ctx, tx, botID, actor.UserID, id); err != nil {
		return err
	}
	var current uint64
	var active bool
	err = tx.QueryRow(ctx, `SELECT version,active FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&current, &active)
	if err != nil {
		return err
	}
	if current != version {
		return ErrVersionConflict
	}
	if err = discordActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	if !active {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.discord_identities SET active=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND status IN ('WAITING','CANDIDATE')`, botID, actor.UserID)
	if err != nil {
		return err
	}
	return discordAudit(ctx, tx, org, actor, "identity_unbound", id)
}

func discordCheckBindingTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r discordControl) error {
	var visible bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.agents a JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND a.archived_at IS NULL AND a.status='ACTIVE' AND s.status='ACTIVE' AND s.deleted_at IS NULL AND (a.visibility='SHARED' OR a.owner_user_id=$2::uuid))`, r.bot.Agent.ID, actor.UserID).Scan(&visible)
	if e != nil {
		return e
	}
	if !visible {
		return ErrForbidden
	}
	visible, e = (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
	if e != nil {
		return e
	}
	if !visible {
		return ErrForbidden
	}
	return nil
}
func discordFinalBindingTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r discordControl) error {
	if e := discordActor(ctx, tx, org, actor, true); e != nil {
		return e
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, r.spaceID, actor.UserID).Scan(&id)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	return discordCheckBindingTarget(ctx, tx, org, actor, r)
}
