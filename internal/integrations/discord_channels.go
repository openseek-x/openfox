package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
	"time"
)

type DiscordChannel struct {
	ID             string     `json:"id"`
	BotID          string     `json:"bot_id"`
	GuildID        discord.ID `json:"guild_id"`
	ChannelID      discord.ID `json:"channel_id"`
	GuildName      string     `json:"guild_name"`
	ChannelName    string     `json:"channel_name"`
	Enabled        bool       `json:"enabled"`
	Paused         bool       `json:"paused"`
	Version        uint64     `json:"version"`
	LastError      string     `json:"last_error"`
	ConversationID string     `json:"conversation_id"`
}
type DiscordChannelInput struct {
	GuildID            string `json:"guild_id"`
	ChannelID          string `json:"channel_id"`
	PublicReplyConsent bool   `json:"public_reply_consent"`
	HistoryConsent     bool   `json:"history_consent"`
}
type DiscordChannelTarget struct {
	org, actor, identity          string
	actorVersion, identityVersion uint64
	control                       discordControl
	remote                        discord.ID
	input                         DiscordChannelInput
	token                         string
}

func (DiscordChannelTarget) String() string     { return "DiscordChannelTarget{[REDACTED]}" }
func (v DiscordChannelTarget) GoString() string { return v.String() }

type DiscordChannelProof struct {
	target  DiscordChannelTarget
	channel discord.Channel
	at      time.Time
}

// PrepareDiscordBrowse returns only a server-owned target. The HTTP operations
// that consume it execute after the caller's read transaction has ended.
func (s Service) PrepareDiscordBrowse(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string) (DiscordChannelTarget, error) {
	p := DiscordChannelTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion}
	r, e := managedDiscord(ctx, tx, org, actor, bot, false)
	if e != nil {
		return p, e
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return p, ErrDiscordSessionState
	}
	p.control = r
	e = tx.QueryRow(ctx, `SELECT id::text,version,remote_user_id::text FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND active AND authorization_generation=$3`, bot, actor.UserID, r.authGeneration).Scan(&p.identity, &p.identityVersion, &p.remote)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, ErrPersonalIdentity
	}
	if e != nil {
		return p, e
	}
	cred, e := s.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if e != nil {
		return p, e
	}
	p.token = cred.AccessToken
	return p, nil
}

type DiscordGuildPage struct {
	Guilds []discord.Guild `json:"guilds"`
	Next   discord.ID      `json:"next,omitempty"`
}
type DiscordRemoteChannel struct {
	ID      discord.ID `json:"id"`
	GuildID discord.ID `json:"guild_id"`
	Name    string     `json:"name"`
}

func (s Service) DiscordGuilds(ctx context.Context, p DiscordChannelTarget, after string) (DiscordGuildPage, error) {
	if p.token == "" {
		return DiscordGuildPage{}, ErrDiscordSessionState
	}
	page, e := s.discordClient().ListGuilds(ctx, p.token, discord.ID(after), 199)
	return DiscordGuildPage{Guilds: page.Guilds, Next: page.Next}, e
}
func (s Service) DiscordRemoteChannels(ctx context.Context, p DiscordChannelTarget, guild string) ([]DiscordRemoteChannel, error) {
	if p.token == "" {
		return nil, ErrDiscordSessionState
	}
	channels, e := s.discordClient().ListChannels(ctx, p.token, discord.ID(guild))
	if e != nil {
		return nil, e
	}
	out := []DiscordRemoteChannel{}
	for _, ch := range channels {
		if ch.Type == 0 && ch.Supported() {
			out = append(out, DiscordRemoteChannel{ID: ch.ID, GuildID: *ch.GuildID, Name: ch.Name})
		}
	}
	return out, nil
}
func (s Service) PrepareDiscordChannel(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string, in DiscordChannelInput) (DiscordChannelTarget, error) {
	if !discord.ID(in.GuildID).Valid() || !discord.ID(in.ChannelID).Valid() || !in.PublicReplyConsent || !in.HistoryConsent {
		return DiscordChannelTarget{}, ErrInvalidRequest
	}
	p, e := s.PrepareDiscordBrowse(ctx, tx, org, actor, bot)
	p.input = in
	return p, e
}
func (s Service) VerifyDiscordChannel(ctx context.Context, p DiscordChannelTarget) (DiscordChannelProof, error) {
	var proof DiscordChannelProof
	if p.token == "" || !p.input.PublicReplyConsent || !p.input.HistoryConsent {
		return proof, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ch, e := s.discordClient().GetChannel(ctx, p.token, discord.ID(p.input.ChannelID))
	if e != nil {
		return proof, e
	}
	if !ch.Supported() || ch.Type != 0 || ch.GuildID == nil || string(*ch.GuildID) != p.input.GuildID {
		return proof, ErrDiscordUnsupported
	}
	if p.control.bot.Kind == BotKindTaskAssistant && !p.control.bot.MessageContentEnabled {
		return proof, ErrDiscordUnsupported
	}
	for _, user := range []discord.ID{p.control.bot.RemoteBotID, p.remote} {
		member, e := s.discordClient().GetGuildMember(ctx, p.token, *ch.GuildID, user)
		if e != nil {
			return proof, e
		}
		if member.Pending || member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
			return proof, ErrForbidden
		}
		perms, e := s.discordClient().GetMemberPermissions(ctx, p.token, ch.ID, user)
		if e != nil {
			return proof, e
		}
		if !perms.Has(discord.PermissionViewChannel | discord.PermissionSendMessages | discord.PermissionReadHistory) {
			return proof, ErrForbidden
		}
		if user == p.remote && !perms.Has(discord.PermissionAdministrator) && !perms.Has(discord.PermissionManageChannels) && !perms.Has(1<<5) {
			return proof, ErrForbidden
		}
	}
	return DiscordChannelProof{target: p, channel: ch, at: time.Now()}, nil
}
func (s Service) EnableDiscordChannel(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p DiscordChannelTarget, proof DiscordChannelProof) (DiscordChannel, error) {
	var out DiscordChannel
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || proof.target.org != p.org || proof.target.identity != p.identity || proof.target.input != p.input || proof.target.control.bot.ID != p.control.bot.ID || proof.target.control.authGeneration != p.control.authGeneration || proof.at.IsZero() || time.Since(proof.at) < 0 || time.Since(proof.at) > time.Minute {
		return out, ErrDiscordSessionState
	}
	r, e := managedDiscord(ctx, tx, org, actor, p.control.bot.ID, true)
	if e != nil {
		return out, e
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive || r.bot.Version != p.control.bot.Version || r.authGeneration != p.control.authGeneration || r.tokenVersion != p.control.tokenVersion {
		return out, ErrVersionConflict
	}
	// New conversations take Agent and space locks before any epoch/identity lock.
	conversation, e := newDiscordConversation(ctx, tx, org, actor, r, true)
	if e != nil {
		return out, e
	}
	var existing string
	e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND enabled FOR UPDATE`, r.bot.ID, p.input.ChannelID).Scan(&existing)
	if e == nil {
		return out, ErrVersionConflict
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	var identityVersion uint64
	var generation int64
	var active bool
	var remote string
	e = tx.QueryRow(ctx, `SELECT version,authorization_generation,active,remote_user_id::text FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid AND owner_user_id=$3::uuid FOR SHARE`, p.identity, r.bot.ID, actor.UserID).Scan(&identityVersion, &generation, &active, &remote)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrForbidden
	}
	if e != nil {
		return out, e
	}
	if !active || generation != r.authGeneration || identityVersion != p.identityVersion || remote != string(p.remote) {
		return out, ErrForbidden
	}
	if e = discordFinalManager(ctx, tx, org, actor, r); e != nil {
		return out, e
	}
	id, e := membership.NewID()
	if e != nil {
		return out, e
	}
	name := proof.channel.Name
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.discord_channel_epochs(organization_id,id,bot_id,guild_id,channel_id,guild_name,channel_name,authorization_generation,public_reply_consent,history_consent,authorized_by,identity_id,identity_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::text,$4,$3::text,$5,$6,true,true,$7::uuid,$8::uuid,$9)`, id, r.bot.ID, p.input.GuildID, p.input.ChannelID, name, r.authGeneration, actor.UserID, p.identity, p.identityVersion)
	if e != nil {
		return out, e
	}
	if e = saveDiscordRoute(ctx, tx, r, actor.UserID, conversation, discord.ID(p.input.ChannelID), p.input.GuildID, "", id); e != nil {
		return out, e
	}
	if e = discordAudit(ctx, tx, org, actor, "channel_enabled", id); e != nil {
		return out, e
	}
	return DiscordChannel{ID: id, BotID: r.bot.ID, GuildID: discord.ID(p.input.GuildID), ChannelID: discord.ID(p.input.ChannelID), GuildName: p.input.GuildID, ChannelName: name, Enabled: true, Version: 1, ConversationID: conversation}, nil
}
func (s Service) DiscordChannels(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string) ([]DiscordChannel, error) {
	if _, e := managedDiscord(ctx, tx, org, actor, bot, false); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT e.id::text,e.bot_id::text,e.guild_id::text,e.channel_id::text,e.guild_name,e.channel_name,e.enabled,e.paused,e.version,e.last_error,COALESCE(r.conversation_id::text,'') FROM openfox.discord_channel_epochs e LEFT JOIN openfox.discord_routes r ON r.organization_id=e.organization_id AND r.epoch_id=e.id WHERE e.organization_id=openfox_private.current_organization_id() AND e.bot_id=$1::uuid ORDER BY e.created_at DESC,e.id LIMIT 201`, bot)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscordChannel{}
	for rows.Next() {
		var v DiscordChannel
		if e = rows.Scan(&v.ID, &v.BotID, &v.GuildID, &v.ChannelID, &v.GuildName, &v.ChannelName, &v.Enabled, &v.Paused, &v.Version, &v.LastError, &v.ConversationID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(out) > 200 {
		return nil, ErrInvalidRequest
	}
	return out, nil
}
func (s Service) DisableDiscordChannel(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot, id string, version uint64) error {
	if !membership.ValidateUUID(id) || version == 0 {
		return ErrInvalidRequest
	}
	r, e := managedDiscord(ctx, tx, org, actor, bot, true)
	if e != nil {
		return e
	}
	var current uint64
	var enabled bool
	e = tx.QueryRow(ctx, `SELECT version,enabled FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND id=$2::uuid FOR UPDATE`, bot, id).Scan(&current, &enabled)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if current != version {
		return ErrVersionConflict
	}
	if e = discordFinalManager(ctx, tx, org, actor, r); e != nil {
		return e
	}
	if !enabled {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET enabled=false,paused=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=$1::uuid AND closed_at IS NULL`, id); e != nil {
		return e
	}
	return discordAudit(ctx, tx, org, actor, "channel_disabled", id)
}

func (s Service) CheckDiscordBrowse(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p DiscordChannelTarget) error {
	current, e := s.PrepareDiscordBrowse(ctx, tx, org, actor, p.control.bot.ID)
	if e != nil {
		return e
	}
	if current.actor != p.actor || current.actorVersion != p.actorVersion || current.identity != p.identity || current.identityVersion != p.identityVersion || current.control.authGeneration != p.control.authGeneration || current.control.tokenVersion != p.control.tokenVersion {
		return ErrVersionConflict
	}
	return nil
}
