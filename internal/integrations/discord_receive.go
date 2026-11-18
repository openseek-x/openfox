package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
	"strings"
)

func receiveDiscordDispatch(ctx context.Context, tx pgx.Tx, r discordControl, event discord.Dispatch) error {
	switch event.Type {
	case "MESSAGE_CREATE":
		m, e := discord.DecodeMessage(event.Data)
		if e != nil {
			return nil
		}
		if m.Author.Bot {
			if m.Author.ID == r.bot.RemoteBotID {
				return acknowledgeDiscordReply(ctx, tx, r, m)
			}
			return nil
		}
		if m.WebhookID != nil || (m.Type != 0 && m.Type != 19) || m.Flags&(1<<6) != 0 || strings.TrimSpace(m.Content) == "" {
			return nil
		}
		if m.GuildID != nil {
			if _, mentioned := discord.MentionText(m, r.bot.RemoteBotID); !mentioned {
				return nil
			}
		}
		return receiveDiscordMessage(ctx, tx, r, m)
	case "MESSAGE_DELETE", "MESSAGE_DELETE_BULK":
		var payload struct {
			ID        discord.ID   `json:"id"`
			IDs       []discord.ID `json:"ids"`
			ChannelID discord.ID   `json:"channel_id"`
		}
		if json.Unmarshal(event.Data, &payload) != nil || !payload.ChannelID.Valid() {
			return nil
		}
		if event.Type == "MESSAGE_DELETE" {
			payload.IDs = []discord.ID{payload.ID}
		}
		if len(payload.IDs) > 100 {
			return nil
		}
		ids := []string{}
		for _, id := range payload.IDs {
			if !id.Valid() {
				return nil
			}
			ids = append(ids, string(id))
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.discord_message_receipts SET source_deleted=true WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND remote_message_id::text=ANY($3::text[])`, r.bot.ID, string(payload.ChannelID), ids)
		return e
	case "CHANNEL_DELETE", "CHANNEL_UPDATE", "GUILD_DELETE", "GUILD_CREATE":
		var p struct {
			ID          discord.ID  `json:"id"`
			GuildID     *discord.ID `json:"guild_id"`
			Type        int         `json:"type"`
			Flags       uint64      `json:"flags"`
			Unavailable bool        `json:"unavailable"`
		}
		if json.Unmarshal(event.Data, &p) != nil || !p.ID.Valid() {
			return nil
		}
		if event.Type == "CHANNEL_UPDATE" && p.Type == 0 && p.Flags&(1<<17) == 0 {
			return nil
		}
		guild := event.Type == "GUILD_DELETE" || event.Type == "GUILD_CREATE"
		closeScope := event.Type == "CHANNEL_DELETE" || event.Type == "CHANNEL_UPDATE" || event.Type == "GUILD_DELETE" && !p.Unavailable
		if closeScope {
			if _, e := tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET enabled=false,paused=false,version=version+1,disabled_at=clock_timestamp(),last_error='scope_removed' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled AND ((NOT $3 AND channel_id=$2) OR($3 AND guild_id=$2))`, r.bot.ID, string(p.ID), guild); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.discord_routes rt SET closed_at=clock_timestamp() FROM openfox.discord_channel_epochs ep WHERE rt.organization_id=openfox_private.current_organization_id() AND ep.organization_id=rt.organization_id AND ep.id=rt.epoch_id AND rt.bot_id=$1::uuid AND rt.closed_at IS NULL AND NOT ep.enabled`, r.bot.ID)
			return e
		}
		paused := event.Type == "GUILD_DELETE"
		reason := ""
		if paused {
			reason = "guild_unavailable"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET paused=$3,version=version+1,last_error=$4 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND guild_id=$2 AND enabled AND paused IS DISTINCT FROM $3 AND ($3 OR last_error='guild_unavailable')`, r.bot.ID, string(p.ID), paused, reason)
		return e

	}
	return nil
}
func receiveDiscordMessage(ctx context.Context, tx pgx.Tx, r discordControl, m discord.Message) error {
	kind, body := "MESSAGE", m.Content
	var bindingHash []byte
	if m.GuildID == nil && strings.HasPrefix(body, "/bind ") {
		code := strings.TrimPrefix(body, "/bind ")
		raw, e := base64.RawURLEncoding.DecodeString(code)
		if e == nil && len(raw) == 32 && len(code) == 43 {
			kind = "BIND"
			bindingHash = TokenHash(code)
			body = ""
		} else {
			return nil
		}
	}
	var route, identity, actor, epoch string
	var identityVersion, actorVersion, epochVersion int64
	chatType, guild := "p2p", ""
	if m.GuildID != nil {
		chatType = "group"
		guild = string(*m.GuildID)
	}
	if kind == "MESSAGE" {
		// Capture current identity and scope at receipt time, before any future
		// rebinding. The worker verifies all facts again before materialization.
		e := tx.QueryRow(ctx, `SELECT i.id::text,i.owner_user_id::text,i.version,om.authorization_version FROM openfox.discord_identities i JOIN openfox.organization_members om ON om.organization_id=i.organization_id AND om.user_id=i.owner_user_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.bot_id=$1::uuid AND i.remote_user_id=$2 AND i.active AND i.authorization_generation=$3 AND om.status='ACTIVE' AND om.role<>'GUEST'`, r.bot.ID, string(m.Author.ID), r.authGeneration).Scan(&identity, &actor, &identityVersion, &actorVersion)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil {
			e = tx.QueryRow(ctx, `SELECT rt.id::text,COALESCE(ep.id::text,''),COALESCE(ep.version,0) FROM openfox.discord_routes rt LEFT JOIN openfox.discord_channel_epochs ep ON ep.organization_id=rt.organization_id AND ep.id=rt.epoch_id WHERE rt.organization_id=openfox_private.current_organization_id() AND rt.bot_id=$1::uuid AND rt.channel_id=$2 AND rt.closed_at IS NULL AND rt.authorization_generation=$3 AND ((rt.kind='p2p' AND $4='' AND rt.identity_id=$5::uuid AND rt.identity_version=$6) OR(rt.kind='group' AND rt.guild_id=NULLIF($4,'') AND ep.enabled AND NOT ep.paused AND ep.public_reply_consent AND ep.history_consent AND ep.authorization_generation=$3))`, r.bot.ID, string(m.ChannelID), r.authGeneration, guild, identity, identityVersion).Scan(&route, &epoch, &epochVersion)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		if e != nil || r.bot.Kind == BotKindTaskAssistant && chatType == "p2p" {
			kind = "NOTICE"
			route = ""
			identity = ""
			actor = ""
			epoch = ""
			body = ""
		}
		// An unconfigured guild channel is never an unsolicited notice target.
		if chatType == "group" && route == "" {
			var enabled bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND guild_id=$3 AND enabled AND NOT paused AND authorization_generation=$4)`, r.bot.ID, string(m.ChannelID), guild, r.authGeneration).Scan(&enabled)
			if e != nil {
				return e
			}
			if !enabled {
				return nil
			}
		}
	}
	id, e := membership.NewID()
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO openfox.discord_message_receipts(organization_id,id,bot_id,connection_id,remote_message_id,channel_id,guild_id,sender_id,body_hash,kind,binding_code_hash,route_id,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version,authorization_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,NULLIF($6,''),$7,$8,$9,$10,NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,NULLIF($13,0),NULLIF($14,'')::uuid,NULLIF($15,0),NULLIF($16,'')::uuid,NULLIF($17,0),$18) ON CONFLICT(organization_id,bot_id,remote_message_id) DO NOTHING`, id, r.bot.ID, r.bot.ConnectionID, string(m.ID), string(m.ChannelID), guild, string(m.Author.ID), TokenHash(m.Content), kind, bindingHash, route, actor, actorVersion, identity, identityVersion, epoch, epochVersion, r.authGeneration)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM(SELECT 1 FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING') LIMIT 1000) q`, r.bot.ID).Scan(&count)
	if e != nil {
		return e
	}
	if count >= 1000 {
		_, e = tx.Exec(ctx, `UPDATE openfox.discord_message_receipts SET result='queue_full' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,sender_union_id,message_type,text,parent_remote_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,'',$7,$8,$3)`, id, r.bot.ID, string(m.ID), string(m.ChannelID), chatType, string(m.Author.ID), kind, body)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.discord_message_receipts SET inbound_id=id,result='PENDING' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return e
}
func acknowledgeDiscordReply(ctx context.Context, tx pgx.Tx, r discordControl, m discord.Message) error {
	if m.Nonce == "" || m.Reference == nil || m.Reference.Type != 0 {
		return nil
	}
	guild := ""
	if m.GuildID != nil {
		guild = string(*m.GuildID)
	}
	refGuild := ""
	if m.Reference.GuildID != nil {
		refGuild = string(*m.Reference.GuildID)
	}
	if guild != refGuild || m.Reference.ChannelID != m.ChannelID {
		return nil
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox q SET status='DONE',remote_message_id=$6,last_error='',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() FROM openfox.discord_replies d WHERE q.organization_id=openfox_private.current_organization_id() AND d.organization_id=q.organization_id AND d.outbox_id=q.id AND d.bot_id=$1::uuid AND d.nonce=$2 AND d.channel_id=$3 AND d.guild_id IS NOT DISTINCT FROM NULLIF($4,'') AND d.reply_to_message_id=$5 AND d.body_hash=$7 AND d.body=$8 AND (q.status='SENDING' OR(q.status='FAILED' AND q.last_error='delivery_unknown'))`, r.bot.ID, string(m.Nonce), string(m.ChannelID), guild, string(m.Reference.MessageID), string(m.ID), TokenHash(m.Content), m.Content)
	return e
}
