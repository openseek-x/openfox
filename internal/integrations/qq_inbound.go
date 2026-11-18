package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/qq"
	"openfox/internal/membership"
)

type qqInbound struct {
	ID, BotID, InboundID, ActorID, IdentityID, EpochID, Scope, ChatID, SubjectID, Text string
	Generation, ActorVersion, IdentityVersion, EpochVersion                            uint64
}

// QQ needs no remote history request. Each input is claimed, authorized and
// materialized in one bounded transaction; a crash rolls the whole claim back.
// Generic IM workers must exclude this provider.
func (w *IMWorker) processQQInbound(ctx context.Context, org string) error {
	var jobs []qqInbound
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT p.id::text,p.bot_id::text,p.actor_id::text,p.actor_version FROM openfox.qq_message_receipts p JOIN openfox.im_inbound_messages q ON (q.organization_id,q.id)=(p.organization_id,p.inbound_id) WHERE p.organization_id=openfox_private.current_organization_id() AND p.disposition='PENDING' AND p.actor_id IS NOT NULL AND q.status='PENDING' ORDER BY p.received_at,p.id LIMIT 20`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var j qqInbound
			if e = rows.Scan(&j.ID, &j.BotID, &j.ActorID, &j.ActorVersion); e != nil {
				return e
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	var all []error
	for _, j := range jobs {
		jobctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		e = w.inOrganizationAs(jobctx, org, j.ActorID, j.ActorVersion, func(ctx context.Context, tx pgx.Tx) error { return w.materializeQQInput(ctx, tx, org, j) })
		cancel()
		if qqAuthorityDenied(e) || errors.Is(e, conversations.ErrInvalidMention) || errors.Is(e, conversations.ErrConversationClosed) || errors.Is(e, collaboration.ErrSpaceRetired) {
			e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				if _, e := lockQQBot(ctx, tx, j.BotID); e != nil {
					return e
				}
				_, e := tx.Exec(ctx, `WITH changed AS (UPDATE openfox.qq_message_receipts SET disposition='IGNORED',status_reason='authority_changed',text='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND disposition='PENDING' RETURNING inbound_id) UPDATE openfox.im_inbound_messages q SET status='IGNORED',result='authority_changed',text='',finished_at=clock_timestamp() FROM changed WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=changed.inbound_id AND q.status='PENDING'`, j.ID)
				return e
			})
		}
		if e != nil {
			all = append(all, e)
		}
	}
	return errors.Join(all...)
}

func (w *IMWorker) materializeQQInput(ctx context.Context, tx pgx.Tx, org string, job qqInbound) error {
	b, e := lockQQBot(ctx, tx, job.BotID)
	if e != nil {
		return e
	}
	var j qqInbound
	var disposition string
	e = tx.QueryRow(ctx, `SELECT id::text,bot_id::text,inbound_id::text,actor_id::text,actor_version,identity_id::text,identity_version,COALESCE(group_epoch_id::text,''),COALESCE(epoch_version,0),generation,scope,chat_id,subject_id,text,disposition FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid`, job.ID, job.BotID).Scan(&j.ID, &j.BotID, &j.InboundID, &j.ActorID, &j.ActorVersion, &j.IdentityID, &j.IdentityVersion, &j.EpochID, &j.EpochVersion, &j.Generation, &j.Scope, &j.ChatID, &j.SubjectID, &j.Text, &disposition)
	if e != nil {
		return e
	}
	if disposition != "PENDING" {
		return nil
	}
	if b.State != "ACTIVE" || b.removed || j.Generation != b.Generation || job.ActorID != j.ActorID || job.ActorVersion != j.ActorVersion {
		return ErrForbidden
	}
	var allowed bool
	if j.EpochID != "" {
		e = tx.QueryRow(ctx, `SELECT disabled_at IS NULL AND public_reply_consent AND version=$2 AND generation=$3 AND bot_id=$4::uuid AND chat_id=$5 FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, j.EpochID, j.EpochVersion, j.Generation, j.BotID, j.ChatID).Scan(&allowed)
		if e != nil {
			return e
		}
		if !allowed {
			return ErrForbidden
		}
	}
	e = tx.QueryRow(ctx, `SELECT active AND revoked_at IS NULL AND version=$2 AND generation=$3 AND bot_id=$4::uuid AND owner_user_id=$5::uuid AND scope=$6 AND chat_id=$7 AND subject_id=$8 AND group_epoch_id IS NOT DISTINCT FROM NULLIF($9,'')::uuid FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, j.IdentityID, j.IdentityVersion, j.Generation, j.BotID, j.ActorID, j.Scope, j.ChatID, j.SubjectID, j.EpochID).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	a, e := qqActor(ctx, tx, org, collaboration.Actor{UserID: j.ActorID, AuthorizationVersion: j.ActorVersion})
	if e != nil {
		return e
	}
	if e = qqCanUse(ctx, tx, org, a, b); e != nil {
		return e
	}
	actor := conversations.Actor{UserID: a.UserID, Role: a.Role, AuthorizationVersion: a.AuthorizationVersion}
	var route, conversation string
	e = tx.QueryRow(ctx, `SELECT id::text,conversation_id::text FROM openfox.qq_routes WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND generation=$2 AND scope=$3 AND chat_id=$4 AND closed_at IS NULL`, b.ID, b.Generation, j.Scope, j.ChatID).Scan(&route, &conversation)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if route == "" {
		route, e = membership.NewID()
		if e != nil {
			return e
		}
		kind := "p2p"
		if j.Scope == "GROUP" {
			kind = "group"
		}
		var reject *inboundReject
		conversation, reject, e = w.createBinding(ctx, tx, org, inboundItem{BotID: b.ID, ChatID: route, ChatType: kind}, inboundDecision{member: actor, agentID: b.AgentID, spaceID: b.SpaceID, provider: "qq"})
		if e != nil {
			return e
		}
		if reject != nil {
			return ErrForbidden
		}
		identity := ""
		version := uint64(0)
		if j.Scope == "C2C" {
			identity = j.IdentityID
			version = j.IdentityVersion
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_routes(organization_id,id,bot_id,generation,scope,chat_id,identity_id,identity_version,group_epoch_id,conversation_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,0),NULLIF($8,'')::uuid,$9::uuid)`, route, b.ID, b.Generation, j.Scope, j.ChatID, identity, version, j.EpochID, conversation)
		if e != nil {
			return e
		}
	}
	if e = conversations.LockQQInvocation(ctx, tx, conversation, a.UserID); e != nil {
		return e
	}
	service := conversations.Service{}
	if j.Scope == "GROUP" {
		if e = service.AddIMParticipant(ctx, tx, org, conversation, actor); e != nil {
			return e
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='PROCESSING',chat_id=$2,attempts=attempts+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$3::uuid AND status='PENDING' AND remote_message_id=$4 AND sender_open_id=$5 AND sender_union_id=''`, j.InboundID, route, b.ID, j.ID, j.IdentityID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET disposition='QUEUED',route_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, route)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_inputs(organization_id,message_id,route_id,actor_id,source,actor_version,identity_id,identity_version,group_epoch_id,epoch_version,generation,receipt_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'REMOTE',$4,$5::uuid,$6,NULLIF($7,'')::uuid,NULLIF($8,0),$9,$10::uuid)`, j.InboundID, route, j.ActorID, j.ActorVersion, j.IdentityID, j.IdentityVersion, j.EpochID, j.EpochVersion, j.Generation, j.ID)
	if e != nil {
		return e
	}
	m, e := service.PostMessage(ctx, tx, org, conversation, actor, conversations.MessageInput{ID: j.InboundID, Body: j.Text, ClientMessageID: "qq:" + b.ID + ":" + j.InboundID, Mentions: []conversations.Mention{{AgentID: b.AgentID}}})
	if e != nil {
		return e
	}
	if e = recordIMAudit(ctx, tx, org, a.UserID, a.AuthorizationVersion, "integration.qq_message_received", "conversation_message", m.ID, b.SpaceID, map[string]string{"source": "qq"}); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET disposition='DONE',text='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
	if e != nil {
		return e
	}
	tag, e = tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='DONE',message_id=$2::uuid,text='',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PROCESSING'`, j.InboundID, m.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

func ignoreQQReceipt(ctx context.Context, tx pgx.Tx, id, reason string) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET disposition='IGNORED',status_reason=$2,text='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, reason)
	return e
}
func qqAuthorityDenied(e error) bool {
	return errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrNeedsReauth) || errors.Is(e, conversations.ErrForbidden) || errors.Is(e, conversations.ErrNotFound) || errors.Is(e, membership.ErrAuthorizationStale)
}

// Capture the actual identity/epoch at receipt time. An unbound old message may
// never become executable because someone later links that QQ identity.
func (s Service) stageQQInput(ctx context.Context, tx pgx.Tx, org string, b QQBot, receipt string, event qq.MessageEvent) error {
	var epoch, identity, owner string
	var epochVersion, identityVersion uint64
	if event.Scope == qq.Group {
		e := tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND generation=$3 AND disabled_at IS NULL FOR SHARE`, b.ID, event.ChatID, b.Generation).Scan(&epoch, &epochVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return ignoreQQReceipt(ctx, tx, receipt, "group_disabled")
		}
		if e != nil {
			return e
		}
	}
	e := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text,version FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND generation=$2 AND scope=$3 AND chat_id=$4 AND subject_id=$5 AND active AND group_epoch_id IS NOT DISTINCT FROM NULLIF($6,'')::uuid FOR SHARE`, b.ID, b.Generation, string(event.Scope), event.ChatID, event.SenderID, epoch).Scan(&identity, &owner, &identityVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		return ignoreQQReceipt(ctx, tx, receipt, "identity_unbound")
	}
	if e != nil {
		return e
	}
	a, e := qqActor(ctx, tx, org, collaboration.Actor{UserID: owner})
	if qqAuthorityDenied(e) {
		return ignoreQQReceipt(ctx, tx, receipt, "actor_revoked")
	}
	if e != nil {
		return e
	}
	if e = qqCanUse(ctx, tx, org, a, b); qqAuthorityDenied(e) {
		return ignoreQQReceipt(ctx, tx, receipt, "target_forbidden")
	} else if e != nil {
		return e
	}
	inbound, e := membership.NewID()
	if e != nil {
		return e
	}
	kind := "p2p"
	if event.Scope == qq.Group {
		kind = "group"
	}
	// The final route UUID is filled by the owned worker before materialization.
	// These temporary aliases remain UUIDs within generic IM's 128-byte contract.
	_, e = tx.Exec(ctx, `INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,message_type,text) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$3,$4,$5,'text',$6)`, inbound, b.ID, receipt, kind, identity, event.Text)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET inbound_id=$2::uuid,actor_id=$3::uuid,actor_version=$4,identity_id=$5::uuid,identity_version=$6,group_epoch_id=NULLIF($7,'')::uuid,epoch_version=NULLIF($8,0) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, receipt, inbound, owner, a.AuthorizationVersion, identity, identityVersion, epoch, epochVersion)
	return e
}
