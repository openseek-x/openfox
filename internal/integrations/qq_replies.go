package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/qq"
)

type qqReplySource struct {
	OutboxID, BotID, ConversationID, RunID, ActorID, Body, RouteID, ReceiptID, IdentityID, EpochID, Scope, ChatID, RemoteID string
	Generation, IdentityVersion                                                                                             uint64
	Deadline                                                                                                                time.Time
}

func loadQQReplySource(ctx context.Context, tx pgx.Tx, id string) (qqReplySource, error) {
	var v qqReplySource
	e := tx.QueryRow(ctx, `SELECT o.id::text,o.bot_id::text,m.conversation_id::text,run.id::text,run.actor_id::text,m.body,r.id::text,p.id::text,s.identity_id::text,COALESCE(s.group_epoch_id::text,''),r.scope,r.chat_id,p.remote_message_id,s.generation,s.identity_version,LEAST(p.sent_at,p.received_at)+interval '5 minutes'
 FROM openfox.im_reply_outbox o
 JOIN openfox.messages m ON (m.organization_id,m.id)=(o.organization_id,o.message_id)
 JOIN openfox.runs run ON (run.organization_id,run.id)=(o.organization_id,o.source_run_id) AND run.conversation_id=m.conversation_id
 JOIN openfox.qq_routes r ON (r.organization_id,r.conversation_id)=(m.organization_id,m.conversation_id) AND r.bot_id=o.bot_id
 JOIN LATERAL(SELECT i.message_id FROM openfox.conversation_inputs i JOIN openfox.messages im ON (im.organization_id,im.id)=(i.organization_id,i.message_id) WHERE i.organization_id=o.organization_id AND i.run_id=run.id ORDER BY im.seq DESC,i.id DESC LIMIT 1) latest ON true
 JOIN openfox.qq_inputs s ON (s.organization_id,s.message_id)=(o.organization_id,latest.message_id)
 JOIN openfox.qq_message_receipts p ON (p.organization_id,p.id)=(s.organization_id,s.receipt_id)
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.id=$1::uuid AND o.source_terminal
 AND m.author_kind IN ('AGENT','SYSTEM') AND m.kind='TEXT' AND s.source='REMOTE' AND s.route_id=r.id
 AND NOT EXISTS(SELECT 1 FROM openfox.conversation_inputs i LEFT JOIN openfox.qq_inputs x ON (x.organization_id,x.message_id)=(i.organization_id,i.message_id) WHERE i.organization_id=o.organization_id AND i.run_id=run.id AND (x.source IS DISTINCT FROM 'REMOTE' OR x.route_id IS DISTINCT FROM r.id))
 AND NOT EXISTS(SELECT 1 FROM openfox.conversations c WHERE c.organization_id=m.organization_id AND c.id=m.conversation_id AND m.seq<=c.fork_prefix_seq)`, id).Scan(&v.OutboxID, &v.BotID, &v.ConversationID, &v.RunID, &v.ActorID, &v.Body, &v.RouteID, &v.ReceiptID, &v.IdentityID, &v.EpochID, &v.Scope, &v.ChatID, &v.RemoteID, &v.Generation, &v.IdentityVersion, &v.Deadline)
	if e == nil && utf8.RuneCountInString(v.Body) > qq.MaxTextRunes {
		// One passive response; the marker is inside the 8,000 code-point budget.
		const marker = "\n…（QQ 回复已截断）"
		runes := []rune(v.Body)
		v.Body = string(runes[:qq.MaxTextRunes-utf8.RuneCountInString(marker)]) + marker
	}
	return v, e
}
func authorizeQQReply(ctx context.Context, tx pgx.Tx, org string, r qqReplySource) error {
	if e := conversations.LockQQInvocation(ctx, tx, r.ConversationID, r.ActorID); e != nil {
		return e
	}
	if _, e := (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, r.ConversationID, r.ActorID); e != nil {
		return e
	}
	return conversations.CheckQQRun(ctx, tx, r.ConversationID, r.ActorID, r.RunID)
}
func qqReplyHash(r qqReplySource) []byte {
	raw, _ := json.Marshal([]string{r.Scope, r.ChatID, r.RemoteID, r.Body, "1"})
	sum := sha256.Sum256(raw)
	return sum[:]
}
func finishQQOutbox(ctx context.Context, tx pgx.Tx, id, state, reason, remote string) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=$2,last_error=$3,remote_message_id=$4,lease_owner='',lease_expires_at=NULL,finished_at=CASE WHEN $2 IN ('DONE','FAILED','SKIPPED') THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, reason, remote)
	return e
}
func qqDenied(e error) bool { return qqAuthorityDenied(e) || errors.Is(e, pgx.ErrNoRows) }

func (w *IMWorker) processQQReplies(ctx context.Context, org string) error {
	var ids []string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT o.id::text FROM openfox.im_reply_outbox o JOIN openfox.qq_bot_controls q ON (q.organization_id,q.bot_id)=(o.organization_id,o.bot_id) WHERE o.organization_id=openfox_private.current_organization_id() AND o.status='PENDING' AND o.next_attempt_at<=clock_timestamp() ORDER BY o.created_at,o.id LIMIT 20`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var bot string
			if e := tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot); e != nil {
				return e
			}
			b, e := lockQQBot(ctx, tx, bot)
			if e != nil {
				return e
			}
			var status string
			if e = tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&status); e != nil {
				return e
			}
			if status != "PENDING" {
				return nil
			}
			r, e := loadQQReplySource(ctx, tx, id)
			if e == nil {
				if b.State != "ACTIVE" || b.Generation != r.Generation {
					e = ErrForbidden
				} else {
					e = authorizeQQReply(ctx, tx, org, r)
				}
			}
			if qqDenied(e) {
				return finishQQOutbox(ctx, tx, id, "SKIPPED", "original_input_unavailable", "")
			}
			if e != nil {
				return e
			}
			if !r.Deadline.After(time.Now()) {
				return finishQQOutbox(ctx, tx, id, "SKIPPED", "passive_window_expired", "")
			}
			if !utf8.ValidString(r.Body) || len([]rune(r.Body)) == 0 || utf8.RuneCountInString(r.Body) > qq.MaxTextRunes {
				return finishQQOutbox(ctx, tx, id, "FAILED", "reply_too_long", "")
			}
			tag, e := tx.Exec(ctx, `INSERT INTO openfox.qq_replies(organization_id,bot_id,route_id,outbox_id,run_id,receipt_id,generation,credential_version,identity_id,identity_version,group_epoch_id,body,request_hash,deadline) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8::uuid,$9,NULLIF($10,'')::uuid,$11,$12,$13) ON CONFLICT DO NOTHING`, b.ID, r.RouteID, id, r.RunID, r.ReceiptID, r.Generation, b.CredentialVersion, r.IdentityID, r.IdentityVersion, r.EpochID, r.Body, qqReplyHash(r), r.Deadline)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return finishQQOutbox(ctx, tx, id, "SKIPPED", "original_reply_already_reserved", "")
			}
			return finishQQOutbox(ctx, tx, id, "SENDING", "", "")
		})
		if e != nil {
			return e
		}
	}
	return nil
}

type qqSend struct {
	ID, Lease, Token  string
	Source            qqReplySource
	CredentialVersion uint64
	Hash              []byte
	Attempts          int
}

func (w *IMWorker) processQQDeliveries(ctx context.Context, org string) error {
	var ids []string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND state='PENDING' AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,id LIMIT 10`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		var job qqSend
		claimed := false
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			job, claimed, e = w.prepareQQSend(ctx, tx, org, id)
			return e
		})
		if e != nil {
			return e
		}
		if !claimed {
			continue
		}
		// The claim commits the exact destination/msg_id/body/seq=1 before HTTP.
		// No permission lock is held over the network. A later revoke may not
		// retract an in-flight platform request, but cannot revive future work.
		request := qq.Reply{Scope: qq.Scope(job.Source.Scope), DestinationID: job.Source.ChatID, MessageID: job.Source.RemoteID, Text: job.Source.Body, Sequence: 1}
		jobctx, cancel := context.WithDeadline(ctx, job.Source.Deadline)
		sent, cause := w.Service.qqClient().SendText(jobctx, job.Token, request)
		cancel()
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return w.finishQQSend(ctx, tx, org, job, sent, cause) })
		if e != nil {
			return e
		}
	}
	return nil
}
func (w *IMWorker) prepareQQSend(ctx context.Context, tx pgx.Tx, org, id string) (qqSend, bool, error) {
	j := qqSend{ID: id}
	var bot, outbox, state string
	var deadline time.Time
	e := tx.QueryRow(ctx, `SELECT bot_id::text,outbox_id::text FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot, &outbox)
	if e != nil {
		return j, false, e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return j, false, e
	}
	e = tx.QueryRow(ctx, `SELECT state,request_hash,attempts,deadline FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&state, &j.Hash, &j.Attempts, &deadline)
	if e != nil || state != "PENDING" {
		return j, false, e
	}
	r, e := loadQQReplySource(ctx, tx, outbox)
	if e == nil {
		if b.State != "ACTIVE" || b.Generation != r.Generation || !bytes.Equal(j.Hash, qqReplyHash(r)) {
			e = ErrForbidden
		} else {
			e = authorizeQQReply(ctx, tx, org, r)
		}
	}
	if qqDenied(e) {
		return j, false, setQQReply(ctx, tx, id, outbox, "SKIPPED", "authority_changed", "", nil)
	}
	if e != nil {
		return j, false, e
	}
	if !deadline.After(time.Now()) || !r.Deadline.Equal(deadline) {
		return j, false, setQQReply(ctx, tx, id, outbox, "EXPIRED", "passive_window_expired", "", nil)
	}
	token, e := w.Service.qqStoredToken(ctx, tx, org, bot)
	if errors.Is(e, ErrNeedsReauth) || e == nil && !token.ExpiresAt.After(time.Now().Add(time.Second)) {
		if _, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET refresh_after=LEAST(COALESCE(refresh_after,clock_timestamp()),clock_timestamp()) WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot); e != nil {
			return j, false, e
		}
		next := time.Now().Add(2 * time.Second)
		return j, false, setQQReply(ctx, tx, id, outbox, "PENDING", "token_refresh_pending", "", &next)
	}
	if e != nil {
		return j, false, e
	}
	if j.Attempts >= 3 {
		return j, false, setQQReply(ctx, tx, id, outbox, "FAILED", "retry_limit", "", nil)
	}
	j.Lease, e = randomBase64URL(24)
	if e != nil {
		return j, false, e
	}
	j.Source = r
	j.Token = token.AccessToken
	j.CredentialVersion = b.CredentialVersion
	j.Attempts++
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_replies SET state='SENDING',version=version+1,credential_version=$2,attempts=attempts+1,lease_owner=$3,lease_expires_at=clock_timestamp()+interval '20 seconds',next_attempt_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, b.CredentialVersion, j.Lease)
	return j, e == nil, e
}
func setQQReply(ctx context.Context, tx pgx.Tx, id, outbox, state, reason, remote string, next *time.Time) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.qq_replies SET state=$2,status_reason=$3,remote_message_id=$4,version=version+1,next_attempt_at=$5,lease_owner='',lease_expires_at=NULL,finished_at=CASE WHEN $2='PENDING' THEN NULL ELSE clock_timestamp() END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, reason, remote, next)
	if e != nil {
		return e
	}
	parent := state
	if state == "UNKNOWN" || state == "EXPIRED" {
		parent = "FAILED"
	}
	if state == "PENDING" {
		parent = "SENDING"
	}
	return finishQQOutbox(ctx, tx, outbox, parent, reason, remote)
}
func (w *IMWorker) finishQQSend(ctx context.Context, tx pgx.Tx, org string, j qqSend, sent qq.SentMessage, cause error) error {
	if _, e := lockQQBot(ctx, tx, j.Source.BotID); e != nil {
		return e
	}
	var state, lease string
	var hash []byte
	var generation uint64
	e := tx.QueryRow(ctx, `SELECT state,lease_owner,request_hash,generation FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.ID).Scan(&state, &lease, &hash, &generation)
	if e != nil {
		return e
	}
	if generation != j.Source.Generation || !bytes.Equal(hash, j.Hash) {
		return nil
	}
	if cause == nil && sent.ID != "" {
		// A positive late receipt is evidence even after local cancellation. It
		// records only this immutable send and never re-enables bot/other work.
		if (state == "SENDING" && lease == j.Lease) || state == "UNKNOWN" || state == "SKIPPED" {
			return setQQReply(ctx, tx, j.ID, j.Source.OutboxID, "DONE", "", sent.ID, nil)
		}
		return nil
	}
	if state != "SENDING" || lease != j.Lease {
		return nil
	}
	state = "UNKNOWN"
	reason := "delivery_unknown"
	var next *time.Time
	if errors.Is(cause, qq.ErrAuthentication) {
		if e = invalidateQQToken(ctx, tx, j.Source.BotID, j.Source.Generation, j.CredentialVersion); e != nil {
			return e
		}
		v := time.Now().Add(time.Second)
		next = &v
		state, reason = "PENDING", "token_rejected"
	} else if errors.Is(cause, qq.ErrRateLimited) {
		var upstream *qq.Error
		if errors.As(cause, &upstream) && upstream.RetryAfter > 0 {
			v := time.Now().Add(upstream.RetryAfter)
			next = &v
			state, reason = "PENDING", "rate_limited"
		}
	} else if errors.Is(cause, qq.ErrInvalid) || errors.Is(cause, qq.ErrRejected) {
		state, reason = "FAILED", "message_rejected"
	}
	if state == "PENDING" && (j.Attempts >= 3 || next == nil || !next.Before(j.Source.Deadline)) {
		state, reason, next = "EXPIRED", "passive_window_expired", nil
	}
	return setQQReply(ctx, tx, j.ID, j.Source.OutboxID, state, reason, "", next)
}
