package integrations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

type telegramSendClaim struct {
	org, bot, id, claim, token string
	tokenVersion               int64
	input                      telegram.SendMessageInput
}

func (telegramSendClaim) String() string     { return "telegramSendClaim{[REDACTED]}" }
func (c telegramSendClaim) GoString() string { return c.String() }

// Original Run inputs, including already-consumed inputs, are the only reply
// provenance. The existing dispatch limit is 20; a malformed overfull or mixed
// Run is refused as a whole rather than using a convenient recent message.
func telegramReplyProof(ctx context.Context, tx pgx.Tx, r telegramControl, outbox string) (telegramInputProof, string, string, error) {
	var p telegramInputProof
	var run, conversation, actor, agent, body string
	err := tx.QueryRow(ctx, `SELECT q.source_run_id::text,m.conversation_id::text,r.actor_id::text,r.agent_id::text,m.body
 FROM openfox.im_reply_outbox q JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id
 JOIN openfox.runs r ON r.organization_id=q.organization_id AND r.id=q.source_run_id AND r.conversation_id=m.conversation_id
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid AND q.bot_id=$2::uuid
 AND m.author_kind IN ('AGENT','SYSTEM') AND (m.author_kind='SYSTEM' OR m.author_agent_id=r.agent_id)
 AND r.cancel_requested_at IS NULL`, outbox, r.bot.ID).Scan(&run, &conversation, &actor, &agent, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, "", "", ErrForbidden
	}
	if err != nil {
		return p, "", "", err
	}
	if agent != r.bot.Agent.ID {
		return p, "", "", ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT COALESCE(t.id::text,''),COALESCE(t.bot_id::text,''),COALESCE(t.route_id::text,''),COALESCE(t.actor_id::text,''),COALESCE(t.identity_id::text,''),COALESCE(t.epoch_id::text,''),
 COALESCE(t.chat_id,0),COALESCE(t.remote_message_id,0),COALESCE(t.update_id,0),COALESCE(t.authorization_generation,0),COALESCE(t.actor_version,0),COALESCE(t.identity_version,0),COALESCE(t.epoch_version,0),COALESCE(t.source_kind,''),COALESCE(rr.kind,'')
 FROM openfox.conversation_inputs i LEFT JOIN openfox.telegram_inputs t ON t.organization_id=i.organization_id AND t.message_id=i.message_id
 LEFT JOIN openfox.telegram_routes rr ON rr.organization_id=t.organization_id AND rr.id=t.route_id AND rr.conversation_id=i.conversation_id
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.run_id=$1::uuid ORDER BY i.created_at,i.id LIMIT 21`, run)
	if err != nil {
		return p, "", "", err
	}
	defer rows.Close()
	count := 0
	firstID := ""
	for rows.Next() {
		var v telegramInputProof
		var source, id string
		if err = rows.Scan(&id, &v.bot, &v.route, &v.actor, &v.identity, &v.epoch, &v.chat, &v.remoteMessage, &v.update, &v.generation, &v.actorVersion, &v.identityVersion, &v.epochVersion, &source, &v.kind); err != nil {
			return p, "", "", err
		}
		v.conversation = conversation
		count++
		if count > 20 || source != "REMOTE" || v.kind == "" || v.bot != r.bot.ID || v.actor != actor || v.remoteMessage <= 0 {
			return p, "", "", ErrForbidden
		}
		if count == 1 {
			p = v
			firstID = id
		} else if p.route != v.route || p.actor != v.actor || p.identity != v.identity || p.identityVersion != v.identityVersion || p.actorVersion != v.actorVersion || p.epoch != v.epoch || p.epochVersion != v.epochVersion || p.generation != v.generation || p.chat != v.chat {
			return p, "", "", ErrForbidden
		}
	}
	if err = rows.Err(); err != nil {
		return p, "", "", err
	}
	if count == 0 {
		return p, "", "", ErrForbidden
	}
	return p, firstID, body, nil
}

func telegramReplyText(body, link string) string {
	body = strings.TrimSpace(body)
	var b strings.Builder
	units := 0
	for _, r := range body {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 3500 {
			break
		}
		units += n
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\n\n全文在 OpenFox 查看：" + link
}

// ProcessTelegramReplies serializes each Bot across workers. There is no
// recovery POST for expired SENDING: a durable unknown result is terminal.
func (w *IMWorker) ProcessTelegramReplies(ctx context.Context, org string) error {
	type item struct {
		id, bot, actor string
		version        uint64
	}
	var list []item
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT q.id::text,q.bot_id::text,COALESCE(r.actor_id::text,''),COALESCE(m.authorization_version,0)
 FROM openfox.im_reply_outbox q JOIN openfox.telegram_bot_controls t ON t.organization_id=q.organization_id AND t.bot_id=q.bot_id
 LEFT JOIN openfox.runs r ON r.organization_id=q.organization_id AND r.id=q.source_run_id
 LEFT JOIN openfox.organization_members m ON m.organization_id=r.organization_id AND m.user_id=r.actor_id
 WHERE q.organization_id=openfox_private.current_organization_id() AND ((q.status='PENDING' AND q.next_attempt_at<=clock_timestamp()) OR(q.status='SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at<=clock_timestamp())))
 ORDER BY q.created_at,q.id LIMIT 20`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.bot, &v.actor, &v.version); e != nil {
				return e
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, v := range list {
		var claim *telegramSendClaim
		actor, version := v.actor, v.version
		if actor == "" || version == 0 {
			actor, version = syncWorkerActorID, 1
		}
		err = w.inOrganizationAs(ctx, org, actor, version, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			claim, e = w.claimTelegramReply(ctx, tx, org, v.bot, v.id)
			return e
		})
		if err != nil {
			return err
		}
		if claim == nil {
			continue
		}
		// This claim is the send authorization linearization point. Revocation
		// cannot retract a request already started, but prevents subsequent claims.
		receipt, sendErr := w.Service.telegramClient().SendMessage(ctx, claim.token, claim.input)
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return w.finishTelegramReply(ctx, tx, *claim, receipt, sendErr)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *IMWorker) claimTelegramReply(ctx context.Context, tx pgx.Tx, org, bot, id string) (*telegramSendClaim, error) {
	r, err := loadTelegramControl(ctx, tx, bot, true)
	if err != nil {
		return nil, err
	}
	var status string
	var attempts int
	var expired bool
	err = tx.QueryRow(ctx, `SELECT status,attempts,lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp() FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid`, id, bot).Scan(&status, &attempts, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stop := func(state, reason string) (*telegramSendClaim, error) {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=$2,last_error=$3,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status IN ('PENDING','SENDING')`, id, state, reason)
		return nil, e
	}
	if status == "SENDING" {
		if expired {
			return stop("FAILED", "delivery_unknown")
		}
		return nil, nil
	}
	if status != "PENDING" {
		return nil, nil
	}
	if attempts >= 8 {
		return stop("FAILED", "retry_limit")
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT (send_claim='' OR send_lease_until<=clock_timestamp()) AND send_not_before<=clock_timestamp() FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot).Scan(&ready)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	p, notice, err := telegramNoticeProof(ctx, tx, r, id)
	if errors.Is(err, ErrForbidden) {
		return stop("SKIPPED", "notice_expired")
	}
	if err != nil {
		return nil, err
	}
	inputID, body := "", telegramBindingNotice
	if !notice {
		p, inputID, body, err = telegramReplyProof(ctx, tx, r, id)
		if errors.Is(err, ErrForbidden) {
			return stop("SKIPPED", "source_unavailable")
		}
		if err != nil {
			return nil, err
		}
		actor, e := checkTelegramInputAuthority(ctx, tx, org, r, p)
		if errors.Is(e, ErrForbidden) {
			return stop("SKIPPED", "authorization_changed")
		}
		if e != nil {
			return nil, e
		}
		if _, e = (conversations.Service{}).Get(ctx, tx, org, p.conversation, actor); e != nil {
			if errors.Is(e, conversations.ErrForbidden) || errors.Is(e, conversations.ErrNotFound) {
				return stop("SKIPPED", "authorization_changed")
			}
			return nil, e
		}
		body = telegramReplyText(body, w.conversationURL(org, p.conversation))
	}
	if body == "" || telegram.UTF16Len(body) > 4096 {
		return stop("SKIPPED", "reply_text_invalid")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_chat_limits(organization_id,bot_id,chat_id)VALUES(openfox_private.current_organization_id(),$1::uuid,$2) ON CONFLICT DO NOTHING`, bot, p.chat); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT not_before<=clock_timestamp() FROM openfox.telegram_chat_limits WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 FOR UPDATE`, bot, p.chat).Scan(&ready); err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	creds, err := w.Service.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if err != nil {
		return nil, err
	}
	if creds.AccessToken == "" {
		return stop("FAILED", "credentials_unavailable")
	}
	claim, err := membership.NewID()
	if err != nil {
		return nil, err
	}
	if notice {
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_replies SET claim_token=$2,token_version=$3 WHERE organization_id=openfox_private.current_organization_id() AND outbox_id=$1::uuid AND notice AND body_hash=$4`, id, claim, r.tokenVersion, TokenHash(body))
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_replies(organization_id,outbox_id,input_id,bot_id,chat_id,reply_to_message_id,authorization_generation,body,body_hash,claim_token,token_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10)
 ON CONFLICT(organization_id,outbox_id)DO UPDATE SET claim_token=EXCLUDED.claim_token,token_version=EXCLUDED.token_version
 WHERE openfox.telegram_replies.input_id=EXCLUDED.input_id AND openfox.telegram_replies.body_hash=EXCLUDED.body_hash AND openfox.telegram_replies.authorization_generation=EXCLUDED.authorization_generation`,
			id, inputID, bot, p.chat, p.remoteMessage, p.generation, body, TokenHash(body), claim, r.tokenVersion)
	}
	if err != nil {
		return nil, err
	}
	// CAS body/provenance is also checked when retrying a known rejected request.
	var snapshot bool
	if err = tx.QueryRow(ctx, `SELECT claim_token=$2 FROM openfox.telegram_replies WHERE organization_id=openfox_private.current_organization_id() AND outbox_id=$1::uuid`, id, claim).Scan(&snapshot); err != nil {
		return nil, err
	}
	if !snapshot {
		return stop("FAILED", "reply_changed")
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET send_claim=$2,send_lease_until=clock_timestamp()+interval '60 seconds',send_not_before=clock_timestamp()+interval '100 milliseconds' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot, claim); err != nil {
		return nil, err
	}
	delay := 1
	if p.kind == "group" {
		delay = 3
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.telegram_chat_limits SET not_before=clock_timestamp()+make_interval(secs=>$3::float8) WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2`, bot, p.chat, delay); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='SENDING',lease_owner=$2,lease_expires_at=clock_timestamp()+interval '60 seconds',chat_id=$3,reply_to_remote_id=$4,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PENDING'`, id, claim, formatID(p.chat), formatID(p.remoteMessage))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrVersionConflict
	}
	return &telegramSendClaim{org: org, bot: bot, id: id, claim: claim, token: creds.AccessToken, tokenVersion: r.tokenVersion, input: telegram.SendMessageInput{ChatID: telegram.ID(p.chat), ReplyToMessageID: telegram.ID(p.remoteMessage), Text: body}}, nil
}

func (w *IMWorker) finishTelegramReply(ctx context.Context, tx pgx.Tx, c telegramSendClaim, receipt telegram.MessageReceipt, cause error) error {
	// Fixed parent-first order also permits disconnect to settle its own queue.
	if _, err := loadTelegramControl(ctx, tx, c.bot, true); err != nil {
		return err
	}
	status, note, remote := "DONE", "", ""
	var next *time.Time
	if cause == nil {
		remote = receipt.MessageID.String()
	} else {
		status, note = "FAILED", "delivery_unknown"
		switch {
		case errors.Is(cause, telegram.ErrRateLimited):
			var e *telegram.Error
			if errors.As(cause, &e) && e.RetryAfter > 0 {
				v := time.Now().UTC().Add(e.RetryAfter)
				next = &v
				status, note = "PENDING", "rate_limited"
			}
		case errors.Is(cause, telegram.ErrAuth):
			note = "credentials_invalid"
		case errors.Is(cause, telegram.ErrForbidden):
			note = "target_forbidden"
		case errors.Is(cause, telegram.ErrRejected):
			note = "send_rejected"
		case errors.Is(cause, telegram.ErrInvalid):
			note = "reply_text_invalid"
		}
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=CASE WHEN $3='PENDING' AND attempts+1>=8 THEN 'FAILED' ELSE $3 END,last_error=$4,remote_message_id=$5,
 attempts=attempts+1,next_attempt_at=COALESCE($6,clock_timestamp()),lease_owner='',lease_expires_at=NULL,
 finished_at=CASE WHEN $3='PENDING' AND attempts+1<8 THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='SENDING' AND lease_owner=$2`, c.id, c.claim, status, note, remote, next)
	if err != nil {
		return err
	}
	if errors.Is(cause, telegram.ErrAuth) {
		if _, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='ATTENTION',last_error='credentials_invalid',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND token_version=$2 AND state='ACTIVE'`, c.bot, c.tokenVersion); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET send_claim='',send_lease_until=NULL,send_not_before=GREATEST(send_not_before,COALESCE($3,send_not_before))
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND send_claim=$2`, c.bot, c.claim, next)
	return err
}
