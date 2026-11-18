package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

type TelegramGroup struct {
	ID                string      `json:"id"`
	BotID             string      `json:"bot_id"`
	ChatID            telegram.ID `json:"chat_id"`
	Title             string      `json:"title"`
	Enabled           bool        `json:"enabled"`
	BackgroundEnabled bool        `json:"background_enabled"`
	Version           uint64      `json:"version"`
	CreatedAt         time.Time   `json:"created_at"`
}
type TelegramGroupDiscovery struct {
	ID        string      `json:"id"`
	BotID     string      `json:"bot_id"`
	ChatID    telegram.ID `json:"chat_id"`
	Title     string      `json:"title"`
	Version   uint64      `json:"version"`
	ExpiresAt time.Time   `json:"expires_at"`
}
type TelegramGroupInput struct {
	DiscoveryID        string `json:"discovery_id,omitempty"`
	EpochID            string `json:"epoch_id,omitempty"`
	ConnectionVersion  uint64 `json:"connection_version"`
	ExpectedVersion    uint64 `json:"expected_version"`
	Enabled            bool   `json:"enabled"`
	PublicReplyConsent bool   `json:"public_reply_consent"`
	BackgroundEnabled  bool   `json:"background_enabled"`
}
type TelegramGroupTarget struct {
	org, actor, botID, identityID string
	actorVersion, identityVersion uint64
	input                         TelegramGroupInput
	chat                          telegram.ID
	title                         string
	generation                    int64
	bot                           telegram.ID
	token                         string
	verifiedAt                    time.Time
}

func (TelegramGroupTarget) String() string     { return "TelegramGroupTarget{[REDACTED]}" }
func (v TelegramGroupTarget) GoString() string { return v.String() }

const telegramGroupColumns = `id::text,bot_id::text,chat_id,title,enabled,background_enabled,version,created_at`

func scanTelegramGroup(row pgx.Row) (TelegramGroup, error) {
	var v TelegramGroup
	err := row.Scan(&v.ID, &v.BotID, &v.ChatID, &v.Title, &v.Enabled, &v.BackgroundEnabled, &v.Version, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s Service) TelegramGroups(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID, after string) ([]TelegramGroup, error) {
	if _, err := managedTelegram(ctx, tx, org, actor, botID, false); err != nil {
		return nil, err
	}
	if after != "" && !membership.ValidateUUID(after) {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `SELECT `+telegramGroupColumns+` FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 50`, botID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramGroup{}
	for rows.Next() {
		v, err := scanTelegramGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s Service) TelegramGroupDiscoveries(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) ([]TelegramGroupDiscovery, error) {
	if _, err := managedTelegram(ctx, tx, org, actor, botID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,bot_id::text,chat_id,title,version,expires_at FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND expires_at>clock_timestamp() ORDER BY created_at DESC,id LIMIT 50`, botID, actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramGroupDiscovery{}
	for rows.Next() {
		var v TelegramGroupDiscovery
		if err = rows.Scan(&v.ID, &v.BotID, &v.ChatID, &v.Title, &v.Version, &v.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s Service) PrepareTelegramGroup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, in TelegramGroupInput) (TelegramGroupTarget, error) {
	p := TelegramGroupTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, botID: botID, input: in}
	r, err := managedTelegram(ctx, tx, org, actor, botID, false)
	if err != nil {
		return p, err
	}
	if in.ConnectionVersion == 0 || in.ConnectionVersion != r.bot.Version {
		return p, ErrVersionConflict
	}
	if !in.Enabled {
		if !membership.ValidateUUID(in.EpochID) || in.ExpectedVersion == 0 || in.BackgroundEnabled {
			return p, ErrInvalidRequest
		}
		return p, nil
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return p, ErrTelegramSessionState
	}
	if !in.PublicReplyConsent || !membership.ValidateUUID(in.DiscoveryID) || in.EpochID != "" || (in.BackgroundEnabled && r.bot.Kind != BotKindTaskAssistant) {
		return p, ErrInvalidRequest
	}
	err = tx.QueryRow(ctx, `SELECT chat_id,title,identity_id::text,identity_version,authorization_generation FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid AND owner_user_id=$3::uuid AND expires_at>clock_timestamp()`, in.DiscoveryID, botID, actor.UserID).
		Scan(&p.chat, &p.title, &p.identityID, &p.identityVersion, &p.generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrTelegramSessionState
	}
	if err != nil {
		return p, err
	}
	if p.generation != r.authGeneration {
		return p, ErrTelegramSessionState
	}
	var current uint64
	err = tx.QueryRow(ctx, `SELECT version FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND enabled`, botID, int64(p.chat)).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	if current != in.ExpectedVersion {
		return p, ErrVersionConflict
	}
	creds, err := s.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if err != nil {
		return p, err
	}
	p.token = creds.AccessToken
	p.bot = r.bot.RemoteBotID
	return p, nil
}
func (s Service) VerifyTelegramGroup(ctx context.Context, p TelegramGroupTarget) (TelegramGroupTarget, error) {
	if !p.input.Enabled {
		p.verifiedAt = time.Now()
		return p, nil
	}
	if p.token == "" || p.chat >= 0 || p.bot <= 0 {
		return p, ErrInvalidRequest
	}
	chat, err := s.telegramClient().GetChat(ctx, p.token, p.chat)
	if err != nil {
		return p, err
	}
	if (chat.Type != "group" && chat.Type != "supergroup") || chat.IsForum || chat.IsDirectMessages {
		return p, ErrTelegramUnsupported
	}
	member, err := s.telegramClient().GetChatMember(ctx, p.token, p.chat, p.bot)
	if err != nil {
		return p, err
	}
	if !telegramBotCanParticipate(member) {
		return p, ErrForbidden
	}
	p.title = chat.Title
	if len([]rune(p.title)) > 255 {
		p.title = string([]rune(p.title)[:255])
	}
	p.verifiedAt = time.Now()
	return p, nil
}
func telegramBotCanParticipate(m telegram.ChatMember) bool {
	if !m.User.IsBot {
		return false
	}
	switch m.Status {
	case "member":
		return true
	case "administrator", "creator":
		return m.IsAnonymous != nil && !*m.IsAnonymous
	case "restricted":
		return m.IsMember != nil && *m.IsMember && m.CanSendMessages != nil && *m.CanSendMessages
	}
	return false
}
func (s Service) SaveTelegramGroup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p TelegramGroupTarget) (TelegramGroup, error) {
	var out TelegramGroup
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || p.verifiedAt.IsZero() || time.Since(p.verifiedAt) < 0 || time.Since(p.verifiedAt) > time.Minute {
		return out, ErrTelegramSessionState
	}
	r, err := managedTelegram(ctx, tx, org, actor, p.botID, true)
	if err != nil {
		return out, err
	}
	if r.bot.Version != p.input.ConnectionVersion {
		return out, ErrVersionConflict
	}
	if !p.input.Enabled {
		current, err := scanTelegramGroup(tx.QueryRow(ctx, `SELECT `+telegramGroupColumns+` FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid FOR UPDATE`, p.input.EpochID, p.botID))
		if err != nil {
			return out, err
		}
		if current.Version != p.input.ExpectedVersion {
			return out, ErrVersionConflict
		}
		if !current.Enabled {
			return current, nil
		}
		if err = telegramActor(ctx, tx, org, actor, true); err != nil {
			return out, err
		}
		if err = disableTelegramEpoch(ctx, tx, current.ID); err != nil {
			return out, err
		}
		if err = telegramAudit(ctx, tx, org, actor, "group_disabled", current.ID); err != nil {
			return out, err
		}
		return scanTelegramGroup(tx.QueryRow(ctx, `SELECT `+telegramGroupColumns+` FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, current.ID))
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive || r.authGeneration != p.generation {
		return out, ErrTelegramSessionState
	}
	// All Agent/conversation work precedes epoch and identity locks.
	conversationID, err := newTelegramConversation(ctx, tx, org, actor, r, true)
	if err != nil {
		return out, err
	}
	var oldID string
	var oldVersion uint64
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND enabled FOR UPDATE`, p.botID, int64(p.chat)).Scan(&oldID, &oldVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if oldVersion != p.input.ExpectedVersion {
		return out, ErrVersionConflict
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT active AND version=$2 AND authorization_generation=$3 AND owner_user_id=$4::uuid FROM openfox.telegram_identities
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$5::uuid FOR SHARE`, p.identityID, p.identityVersion, p.generation, actor.UserID, p.botID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrTelegramSessionState
	}
	if err != nil {
		return out, err
	}
	if !valid {
		return out, ErrTelegramSessionState
	}
	var discovery bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid AND owner_user_id=$3::uuid AND chat_id=$4 AND identity_id=$5::uuid AND identity_version=$6 AND authorization_generation=$7 AND expires_at>clock_timestamp())`, p.input.DiscoveryID, p.botID, actor.UserID, int64(p.chat), p.identityID, p.identityVersion, p.generation).Scan(&discovery)
	if err != nil {
		return out, err
	}
	if !discovery {
		return out, ErrTelegramSessionState
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	if oldID != "" {
		if err = disableTelegramEpoch(ctx, tx, oldID); err != nil {
			return out, err
		}
	}
	id, err := membership.NewID()
	if err != nil {
		return out, err
	}
	out, err = scanTelegramGroup(tx.QueryRow(ctx, `INSERT INTO openfox.telegram_group_epochs(organization_id,id,bot_id,chat_id,title,authorization_generation,public_reply_consent,background_enabled,authorized_by,identity_id,identity_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,true,$6,$7::uuid,$8::uuid,$9) RETURNING `+telegramGroupColumns, id, p.botID, int64(p.chat), p.title, p.generation, p.input.BackgroundEnabled, actor.UserID, p.identityID, p.identityVersion))
	if err != nil {
		return out, err
	}
	if err = saveTelegramRoute(ctx, tx, r, actor.UserID, conversationID, p.chat, "", id); err != nil {
		return out, err
	}
	// A discovery authorizes one explicit enable only; another epoch needs a new command.
	_, err = tx.Exec(ctx, `DELETE FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.input.DiscoveryID)
	if err != nil {
		return out, err
	}
	return out, telegramAudit(ctx, tx, org, actor, "group_enabled", id)
}
func disableTelegramEpoch(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.telegram_group_epochs SET enabled=false,background_enabled=false,disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND enabled`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=$1::uuid AND closed_at IS NULL`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=$1::uuid`, id)
	return err
}
