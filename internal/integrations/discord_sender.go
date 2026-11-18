package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"strings"
	"time"
)

type discordReplyProof struct {
	input                        discordInbound
	snapshot, body, run, message string
	inputIDs                     []string
	notice                       bool
}
type discordSendClaim struct {
	org, id, bot, claim, token string
	tokenVersion               int64
	proof                      discordReplyProof
	intent                     discord.MessageIntent
}

func (discordSendClaim) String() string     { return "discordSendClaim{[REDACTED]}" }
func (v discordSendClaim) GoString() string { return v.String() }
func loadDiscordReplyProof(ctx context.Context, tx pgx.Tx, r discordControl, id string) (discordReplyProof, error) {
	var proof discordReplyProof
	var conversation, actor, agent, chat, remote string
	var terminal bool
	e := tx.QueryRow(ctx, `SELECT COALESCE(q.source_run_id::text,''),COALESCE(q.message_id::text,''),q.source_terminal,COALESCE(m.conversation_id::text,''),COALESCE(run.actor_id::text,''),COALESCE(run.agent_id::text,''),COALESCE(m.body,q.body),q.chat_id,q.reply_to_remote_id FROM openfox.im_reply_outbox q LEFT JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id LEFT JOIN openfox.runs run ON run.organization_id=q.organization_id AND run.id=q.source_run_id AND run.conversation_id=m.conversation_id WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid AND q.bot_id=$2::uuid AND (m.id IS NULL OR m.author_kind='SYSTEM' OR(m.author_kind='AGENT' AND m.author_agent_id=run.agent_id))`, id, r.bot.ID).Scan(&proof.run, &proof.message, &terminal, &conversation, &actor, &agent, &proof.body, &chat, &remote)
	if errors.Is(e, pgx.ErrNoRows) {
		return proof, ErrForbidden
	}
	if e != nil {
		return proof, e
	}
	if proof.message == "" {
		proof.notice = true
		p := &proof.input
		e = tx.QueryRow(ctx, `SELECT p.id::text,p.channel_id::text,COALESCE(p.guild_id::text,''),p.remote_message_id::text,p.sender_id::text,p.authorization_generation,p.body_hash,d.body FROM openfox.discord_replies d JOIN openfox.discord_message_receipts p ON p.organization_id=d.organization_id AND p.id=d.notice_receipt_id WHERE d.organization_id=openfox_private.current_organization_id() AND d.outbox_id=$1::uuid AND d.bot_id=$2::uuid AND p.bot_id=d.bot_id AND p.kind='NOTICE' AND NOT p.source_deleted AND d.authorization_generation=p.authorization_generation AND d.channel_id=p.channel_id AND d.guild_id IS NOT DISTINCT FROM p.guild_id AND d.reply_to_message_id=p.remote_message_id AND d.body_hash=sha256(convert_to(d.body,'UTF8'))`, id, r.bot.ID).Scan(&p.id, &p.channel, &p.guild, &p.remote, &p.sender, &p.generation, &p.bodyHash, &proof.body)
		if errors.Is(e, pgx.ErrNoRows) {
			return proof, ErrForbidden
		}
		if e != nil {
			return proof, e
		}
		p.bot = r.bot.ID
		p.kind = "NOTICE"
		p.chatType = "p2p"
		if p.guild != "" {
			p.chatType = "group"
		}
		if p.generation != r.authGeneration || p.channel != chat || p.remote != remote {
			return proof, ErrForbidden
		}
		return proof, nil
	}
	if proof.run != "" && (!terminal || actor == "" || agent != r.bot.Agent.ID) {
		return proof, ErrForbidden
	}
	if proof.run == "" && terminal {
		return proof, ErrForbidden
	}
	predicate := "ci.run_id=$2::uuid"
	value := proof.run
	order := "ci.created_at,ci.id"
	join := ""
	if proof.run == "" {
		value = proof.message
		predicate = "rs.message_id=$2::uuid AND ci.status='REJECTED' AND ci.run_id IS NULL"
		order = "rs.ordinal"
		join = " JOIN openfox.discord_reply_sources rs ON rs.organization_id=ci.organization_id AND rs.conversation_input_id=ci.id AND rs.snapshot_id=s.id "
	}
	rows, e := tx.Query(ctx, `SELECT ci.id::text,COALESCE(s.id::text,''),COALESCE(s.bot_id::text,''),COALESCE(s.route_id::text,''),COALESCE(s.actor_id::text,''),COALESCE(s.identity_id::text,''),COALESCE(s.epoch_id::text,''),COALESCE(s.channel_id::text,''),COALESCE(s.guild_id::text,''),COALESCE(s.remote_message_id::text,''),COALESCE(s.authorization_generation,0),COALESCE(s.actor_version,0),COALESCE(s.identity_version,0),COALESCE(s.epoch_version,0),COALESCE(p.sender_id::text,''),p.body_hash,COALESCE(rt.kind,''),COALESCE(s.source_kind='REMOTE' AND p.kind='MESSAGE' AND NOT p.source_deleted AND p.bot_id=s.bot_id AND p.route_id=s.route_id AND rt.conversation_id=ci.conversation_id,false)
 FROM openfox.conversation_inputs ci LEFT JOIN openfox.discord_inputs s ON s.organization_id=ci.organization_id AND s.message_id=ci.message_id LEFT JOIN openfox.discord_routes rt ON rt.organization_id=s.organization_id AND rt.id=s.route_id LEFT JOIN openfox.discord_message_receipts p ON p.organization_id=s.organization_id AND p.id=s.receipt_id`+join+` WHERE ci.organization_id=openfox_private.current_organization_id() AND ci.conversation_id=$1::uuid AND `+predicate+` ORDER BY `+order+` LIMIT 21`, conversation, value)
	if e != nil {
		return proof, e
	}
	defer rows.Close()
	var first discordInbound
	for rows.Next() {
		var p discordInbound
		var inputID, snapshot string
		var valid bool
		e = rows.Scan(&inputID, &snapshot, &p.bot, &p.route, &p.actor, &p.identity, &p.epoch, &p.channel, &p.guild, &p.remote, &p.generation, &p.actorVersion, &p.identityVersion, &p.epochVersion, &p.sender, &p.bodyHash, &p.chatType, &valid)
		if e != nil {
			return proof, e
		}
		p.conversation = conversation
		p.kind = "MESSAGE"
		if !valid || p.bot != r.bot.ID || actor != "" && p.actor != actor || !discord.ID(p.remote).Valid() || len(proof.inputIDs) >= 20 {
			return proof, ErrForbidden
		}
		if len(proof.inputIDs) == 0 {
			first = p
		} else if p.route != first.route || p.actor != first.actor || p.actorVersion != first.actorVersion || p.identity != first.identity || p.identityVersion != first.identityVersion || p.epoch != first.epoch || p.epochVersion != first.epochVersion || p.generation != first.generation || p.channel != first.channel || p.guild != first.guild || p.sender != first.sender {
			return proof, ErrForbidden
		}
		proof.inputIDs = append(proof.inputIDs, inputID)
		proof.input = p
		proof.snapshot = snapshot
	}
	if e = rows.Err(); e != nil {
		return proof, e
	}
	if len(proof.inputIDs) == 0 || proof.input.channel != chat || proof.input.remote != remote {
		return proof, ErrForbidden
	}
	return proof, nil
}
func checkDiscordReplyAuthority(ctx context.Context, tx pgx.Tx, org string, r discordControl, p discordReplyProof) error {
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive || p.input.generation != r.authGeneration {
		return ErrForbidden
	}
	if p.notice {
		if p.input.guild != "" {
			var enabled bool
			e := tx.QueryRow(ctx, `SELECT enabled AND NOT paused AND public_reply_consent AND history_consent AND authorization_generation=$3 FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND enabled FOR SHARE`, r.bot.ID, p.input.channel, r.authGeneration).Scan(&enabled)
			if errors.Is(e, pgx.ErrNoRows) || e == nil && !enabled {
				return ErrForbidden
			}
			return e
		}
		return nil
	}
	actor, e := checkDiscordInboundAuthority(ctx, tx, org, r, p.input)
	if e != nil {
		return e
	}
	if _, e = (conversations.Service{}).Get(ctx, tx, org, p.input.conversation, actor); e != nil {
		if errors.Is(e, conversations.ErrNotFound) || errors.Is(e, conversations.ErrForbidden) {
			return ErrForbidden
		}
		return e
	}
	if p.run != "" {
		return conversations.CheckDiscordRun(ctx, tx, p.input.conversation, p.input.actor, p.run)
	}
	return conversations.CheckDiscordInputs(ctx, tx, p.input.conversation, p.input.actor, p.inputIDs)
}
func discordReplyText(body, link string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if discord.UTF16Len(body) <= 2000 {
		return body
	}
	suffix := "\n…\n完整结果：" + link
	if discord.UTF16Len(suffix) >= 2000 {
		return ""
	}
	budget := 2000 - discord.UTF16Len(suffix)
	var b strings.Builder
	for _, r := range body {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if n > budget {
			break
		}
		b.WriteRune(r)
		budget -= n
	}
	return b.String() + suffix
}
func (w *IMWorker) claimDiscordReply(ctx context.Context, tx pgx.Tx, org, bot, id string) (*discordSendClaim, error) {
	r, e := loadDiscordControl(ctx, tx, bot, true)
	if e != nil {
		return nil, e
	}
	var status string
	var attempts int
	var expired, due bool
	e = tx.QueryRow(ctx, `SELECT status,attempts,lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp(),next_attempt_at<=clock_timestamp() FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND id=$2::uuid`, bot, id).Scan(&status, &attempts, &expired, &due)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	stop := func(state, reason string) (*discordSendClaim, error) {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=$2,last_error=$3,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status IN ('PENDING','SENDING')`, id, state, reason)
		return nil, e
	}
	if status == "SENDING" {
		if expired {
			return stop("FAILED", "delivery_unknown")
		}
		return nil, nil
	}
	if status != "PENDING" || !due {
		return nil, nil
	}
	if attempts >= 6 {
		return stop("FAILED", "retry_limit")
	}
	var ready bool
	e = tx.QueryRow(ctx, `SELECT (send_claim='' OR send_lease_until<=clock_timestamp()) AND (global_not_before IS NULL OR global_not_before<=clock_timestamp()) FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot).Scan(&ready)
	if e != nil || !ready {
		return nil, e
	}
	proof, e := loadDiscordReplyProof(ctx, tx, r, id)
	if errors.Is(e, ErrForbidden) {
		return stop("SKIPPED", "source_unavailable")
	}
	if e != nil {
		return nil, e
	}
	if e = checkDiscordReplyAuthority(ctx, tx, org, r, proof); errors.Is(e, ErrForbidden) || errors.Is(e, conversations.ErrForbidden) {
		return stop("SKIPPED", "authorization_changed")
	}
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM openfox.discord_rate_limits WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND major=$2 AND not_before>clock_timestamp())`, bot, proof.input.channel).Scan(&ready)
	if e != nil || !ready {
		return nil, e
	}
	body := proof.body
	if !proof.notice {
		body = discordReplyText(body, w.conversationURL(org, proof.input.conversation))
	}
	if body == "" || discord.UTF16Len(body) > 2000 {
		return stop("SKIPPED", "reply_text_invalid")
	}
	nonce, e := randomBase64URL(16)
	if e != nil {
		return nil, e
	}
	claim, e := randomBase64URL(16)
	if e != nil {
		return nil, e
	}
	if !proof.notice {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.discord_replies(organization_id,outbox_id,bot_id,input_id,channel_id,guild_id,reply_to_message_id,authorization_generation,token_version,body,body_hash,nonce,claim_token) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(organization_id,outbox_id) DO UPDATE SET claim_token=EXCLUDED.claim_token WHERE discord_replies.input_id=EXCLUDED.input_id AND discord_replies.channel_id=EXCLUDED.channel_id AND discord_replies.guild_id IS NOT DISTINCT FROM EXCLUDED.guild_id AND discord_replies.reply_to_message_id=EXCLUDED.reply_to_message_id AND discord_replies.authorization_generation=EXCLUDED.authorization_generation AND discord_replies.token_version=EXCLUDED.token_version AND discord_replies.body_hash=EXCLUDED.body_hash AND discord_replies.body=EXCLUDED.body`, id, bot, proof.snapshot, proof.input.channel, proof.input.guild, proof.input.remote, r.authGeneration, r.tokenVersion, body, TokenHash(body), nonce, claim)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.discord_replies SET claim_token=$2,token_version=$3 WHERE organization_id=openfox_private.current_organization_id() AND outbox_id=$1::uuid AND authorization_generation=$4`, id, claim, r.tokenVersion, r.authGeneration)
	}
	if e != nil {
		return nil, e
	}
	var storedClaim string
	e = tx.QueryRow(ctx, `SELECT nonce,claim_token FROM openfox.discord_replies WHERE organization_id=openfox_private.current_organization_id() AND outbox_id=$1::uuid`, id).Scan(&nonce, &storedClaim)
	if e != nil {
		return nil, e
	}
	if storedClaim != claim {
		return stop("FAILED", "reply_changed")
	}
	cred, e := w.Service.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET send_claim=$2,send_lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot, claim); e != nil {
		return nil, e
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='SENDING',attempts=attempts+1,lease_owner=$2,lease_expires_at=clock_timestamp()+interval '60 seconds',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PENDING'`, id, claim)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrVersionConflict
	}
	proof.input.control = r
	proof.input.token = cred.AccessToken
	c := &discordSendClaim{org: org, id: id, bot: bot, claim: claim, token: cred.AccessToken, tokenVersion: r.tokenVersion, proof: proof, intent: discord.MessageIntent{BotID: r.bot.RemoteBotID, ChannelID: discord.ID(proof.input.channel), MessageID: discord.ID(proof.input.remote), Content: body, Nonce: nonce}}
	if proof.input.guild != "" {
		guild := discord.ID(proof.input.guild)
		c.intent.GuildID = &guild
	}
	return c, nil
}
func (w *IMWorker) confirmDiscordSend(ctx context.Context, tx pgx.Tx, c discordSendClaim) error {
	r, e := loadDiscordControl(ctx, tx, c.bot, true)
	if e != nil {
		return e
	}
	if r.tokenVersion != c.tokenVersion {
		return ErrForbidden
	}
	p, e := loadDiscordReplyProof(ctx, tx, r, c.id)
	if e != nil {
		return e
	}
	if e = checkDiscordReplyAuthority(ctx, tx, c.org, r, p); e != nil {
		return e
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT q.status='SENDING' AND q.lease_owner=$2 AND q.lease_expires_at>clock_timestamp() AND d.claim_token=$2 AND d.body_hash=$3 AND d.nonce=$4 AND d.token_version=$5 FROM openfox.im_reply_outbox q JOIN openfox.discord_replies d ON d.organization_id=q.organization_id AND d.outbox_id=q.id WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid FOR UPDATE OF q`, c.id, c.claim, TokenHash(c.intent.Content), c.intent.Nonce, c.tokenVersion).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return ErrForbidden
	}
	return nil
}
func (w *IMWorker) finishDiscordReply(ctx context.Context, tx pgx.Tx, c discordSendClaim, receipt discord.Message, cause error, sent bool) error {
	r, e := loadDiscordControl(ctx, tx, c.bot, true)
	if e != nil {
		return e
	}
	status, note, remote := "DONE", "", ""
	var next *time.Time
	if cause == nil {
		remote = string(receipt.ID)
	} else {
		status, note = "FAILED", "delivery_unknown"
		switch {
		case !sent:
			status, note = "SKIPPED", "source_unavailable"
			if !errors.Is(cause, ErrForbidden) && !errors.Is(cause, conversations.ErrForbidden) && !errors.Is(cause, discord.ErrForbidden) && !errors.Is(cause, discord.ErrNotFound) && !errors.Is(cause, discord.ErrAuth) && !errors.Is(cause, ErrDiscordUnsupported) && !errors.Is(cause, discord.ErrInvalid) {
				delay := 30 * time.Second
				var de *discord.Error
				if errors.As(cause, &de) && de.RetryAfter > delay {
					delay = de.RetryAfter
				}
				v := time.Now().Add(delay)
				next = &v
				status = "PENDING"
			}
		case errors.Is(cause, discord.ErrRateLimited):
			note = "rate_limited"
			var de *discord.Error
			if errors.As(cause, &de) && de.RetryAfter > 0 {
				v := time.Now().Add(de.RetryAfter)
				next = &v
				status = "PENDING"
				if de.Global {
					if _, e = tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET global_not_before=GREATEST(global_not_before,$2) WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND token_version=$3`, c.bot, next, c.tokenVersion); e != nil {
						return e
					}
				} else {
					bucket := de.Bucket
					if bucket == "" {
						bucket = "messages"
					}
					if _, e = tx.Exec(ctx, `INSERT INTO openfox.discord_rate_limits(organization_id,bot_id,major,bucket,not_before) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4) ON CONFLICT(organization_id,bot_id,major,bucket) DO UPDATE SET not_before=GREATEST(discord_rate_limits.not_before,EXCLUDED.not_before)`, c.bot, c.proof.input.channel, bucket, next); e != nil {
						return e
					}
				}
			}
		case errors.Is(cause, discord.ErrAuth):
			note = "credentials_invalid"
		case errors.Is(cause, discord.ErrForbidden):
			note = "target_forbidden"
		case errors.Is(cause, discord.ErrNotFound):
			note = "source_missing"
		case errors.Is(cause, discord.ErrInvalid):
			note = "reply_text_invalid"
		}
	}
	// A definitive channel POST denial pauses the current scope. Do this before
	// locking the outbox, preserving bot -> epoch -> queue order. A user-specific
	// preflight denial does not revoke other members' channel authorization.
	if sent && errors.Is(cause, discord.ErrForbidden) && r.tokenVersion == c.tokenVersion && r.authGeneration == c.proof.input.generation {
		if c.proof.input.guild != "" {
			if _, e = tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET paused=true,version=version+1,last_error='target_forbidden' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND enabled AND NOT paused`, c.bot, c.proof.input.channel); e != nil {
				return e
			}
		} else {
			if _, e = tx.Exec(ctx, `UPDATE openfox.discord_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND channel_id=$2 AND kind='p2p' AND closed_at IS NULL`, c.bot, c.proof.input.channel); e != nil {
				return e
			}
		}
	}
	// A known success may finish an UNKNOWN produced by concurrent revocation;
	// it records delivery without restoring authorization or sending again.
	_, e = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox q SET status=CASE WHEN $3='PENDING' AND (q.attempts>=6 OR $7) THEN 'FAILED' ELSE $3 END,last_error=$4,remote_message_id=$5,next_attempt_at=COALESCE($6,clock_timestamp()),lease_owner='',lease_expires_at=NULL,finished_at=CASE WHEN $3='PENDING' AND q.attempts<6 AND NOT $7 THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp() FROM openfox.discord_replies d WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid AND d.organization_id=q.organization_id AND d.outbox_id=q.id AND d.claim_token=$2 AND ((q.status='SENDING' AND q.lease_owner=$2) OR($3='DONE' AND q.status='FAILED' AND q.last_error='delivery_unknown'))`, c.id, c.claim, status, note, remote, next, r.tokenVersion != c.tokenVersion || r.bot.State != "ACTIVE")
	if e != nil {
		return e
	}
	if errors.Is(cause, discord.ErrAuth) && r.tokenVersion == c.tokenVersion {
		if _, e = tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET state='ATTENTION',last_error='credentials_invalid',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, c.bot); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET send_claim='',send_lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND send_claim=$2`, c.bot, c.claim)
	return e
}
func (w *IMWorker) ProcessDiscordReplies(ctx context.Context, org string) error {
	type item struct{ id, bot string }
	var list []item
	e := w.inOrganization(ctx, org, func(c context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(c, `SELECT q.id::text,q.bot_id::text FROM openfox.im_reply_outbox q JOIN openfox.discord_bot_controls t ON t.organization_id=q.organization_id AND t.bot_id=q.bot_id WHERE q.organization_id=openfox_private.current_organization_id() AND ((q.status='PENDING' AND q.next_attempt_at<=clock_timestamp()) OR(q.status='SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at<=clock_timestamp()))) ORDER BY q.created_at,q.id LIMIT 20`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.bot); e != nil {
				return e
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, v := range list {
		var claim *discordSendClaim
		e = w.inOrganization(ctx, org, func(c context.Context, tx pgx.Tx) error {
			var e error
			claim, e = w.claimDiscordReply(c, tx, org, v.bot, v.id)
			return e
		})
		if e != nil {
			return e
		}
		if claim == nil {
			continue
		}
		// Re-read the exact original source and current Discord permissions before
		// each POST. No history/Agent execution is needed for sending a reply.
		check := claim.proof.input
		check.kind = "NOTICE"
		_, _, sendErr := w.Service.verifyDiscordInbound(ctx, check)
		sent := false
		var receipt discord.Message
		if sendErr == nil {
			sendErr = w.inOrganization(ctx, org, func(c context.Context, tx pgx.Tx) error { return w.confirmDiscordSend(c, tx, *claim) })
		}
		if sendErr == nil {
			sent = true
			receipt, sendErr = w.Service.discordClient().CreateMessage(ctx, claim.token, claim.intent)
		}
		if e = w.inOrganization(ctx, org, func(c context.Context, tx pgx.Tx) error {
			return w.finishDiscordReply(c, tx, *claim, receipt, sendErr, sent)
		}); e != nil {
			return e
		}
	}
	return nil
}
