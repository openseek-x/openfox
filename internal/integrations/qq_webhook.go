package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/qq"
	"openfox/internal/membership"
)

var qqAck = json.RawMessage(`{"op":12,"d":0}`)

// The HTTP caller sends this response only AFTER its transaction commits.
// The opaque route locates a tenant, while current raw Ed25519 proves QQ origin.
func (s Service) ReceiveQQWebhook(ctx context.Context, tx pgx.Tx, org, bot string, routeHash []byte, headers http.Header, raw []byte) (json.RawMessage, error) {
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return nil, e
	}
	if b.removed || (b.State != "WAITING_WEBHOOK" && b.State != "ACTIVE") {
		return nil, ErrNotFound
	}
	var stored, blob []byte
	var activated *time.Time
	e = tx.QueryRow(ctx, `SELECT q.webhook_hash,c.credentials_sealed,q.activated_at FROM openfox.qq_bot_controls q JOIN openfox.integration_connections c ON c.organization_id=q.organization_id AND c.id=q.connection_id WHERE q.organization_id=openfox_private.current_organization_id() AND q.bot_id=$1::uuid`, bot).Scan(&stored, &blob, &activated)
	if e != nil {
		return nil, e
	}
	if len(stored) != 32 || subtle.ConstantTimeCompare(stored, routeHash) != 1 {
		return nil, ErrNotFound
	}
	plain, e := s.open(blob, qqAAD(org, bot, "credentials"))
	if e != nil {
		return nil, e
	}
	var credential qq.Credentials
	if json.Unmarshal(plain, &credential) != nil || credential.AppID != b.AppID {
		return nil, ErrNeedsReauth
	}
	if e = qq.VerifySignature(b.AppID, credential.AppSecret, headers, raw); e != nil {
		return nil, e
	}
	var op struct {
		Op *int `json:"op"`
	}
	if json.Unmarshal(raw, &op) != nil || op.Op == nil {
		return nil, ErrInvalidRequest
	}
	if *op.Op == 13 {
		challenge, e := qq.Challenge(credential.AppSecret, raw)
		if e != nil {
			return nil, e
		}
		if !b.URLVerified {
			_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET url_verified_at=clock_timestamp(),activated_at=clock_timestamp(),status_reason='waiting_signed_event',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot)
			if e != nil {
				return nil, e
			}
		}
		return json.Marshal(challenge)
	}
	if !b.URLVerified || activated == nil {
		return qqAck, nil
	}
	lifecycle, e := qq.ParseLifecycle(raw)
	if e == nil {
		if lifecycle.SentAt.Before(activated.Truncate(time.Second)) {
			return qqAck, nil
		}
		return qqAck, s.receiveQQLifecycle(ctx, tx, b, lifecycle)
	}
	if !errors.Is(e, qq.ErrUnsupported) {
		return nil, e
	}
	message, e := qq.ParseMessage(raw)
	if errors.Is(e, qq.ErrUnsupported) {
		return qqAck, nil
	}
	if e != nil {
		return nil, e
	}
	if message.SentAt.Before(activated.Truncate(time.Second)) {
		return qqAck, nil
	}
	if b.State != "ACTIVE" {
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot)
		if e != nil {
			return qqAck, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID)
		if e != nil {
			return qqAck, e
		}
	}
	// Hash only the normalized event facts. Different transport IDs for the same
	// business message do not create another execution; conflicts are rejected.
	canonical, _ := json.Marshal([]any{message.Type, message.Scope, message.ChatID, message.SenderID, message.MessageID, message.Text, message.SentAt, message.MemberRole, message.MessageIndex, message.HasAttachments})
	sum := sha256.Sum256(canonical)
	id, e := membership.NewID()
	if e != nil {
		return qqAck, e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO openfox.qq_message_receipts(organization_id,id,bot_id,generation,event_id,event_type,scope,chat_id,subject_id,remote_message_id,message_index,member_role,body_hash,text,sent_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT DO NOTHING`, id, b.ID, b.Generation, message.EventID, message.Type, string(message.Scope), message.ChatID, message.SenderID, message.MessageID, message.MessageIndex, message.MemberRole, sum[:], message.Text, message.SentAt)
	if e != nil {
		return qqAck, e
	}
	if tag.RowsAffected() == 0 {
		var old []byte
		e = tx.QueryRow(ctx, `SELECT body_hash FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND (event_id=$2 OR (scope=$3 AND chat_id=$4 AND remote_message_id=$5)) ORDER BY received_at LIMIT 1`, b.ID, message.EventID, string(message.Scope), message.ChatID, message.MessageID).Scan(&old)
		if e != nil {
			return qqAck, e
		}
		if !bytes.Equal(old, sum[:]) {
			return nil, ErrVersionConflict
		}
		return qqAck, nil
	}
	observed, e := observeQQBinding(ctx, tx, b, id, message.Text, message.SentAt, string(message.Scope), message.MemberRole)
	if e != nil {
		return qqAck, e
	}
	if observed {
		return qqAck, nil
	}
	return qqAck, s.stageQQInput(ctx, tx, org, b, id, message)
}

func (s Service) receiveQQLifecycle(ctx context.Context, tx pgx.Tx, b QQBot, event qq.LifecycleEvent) error {
	id, e := membership.NewID()
	if e != nil {
		return e
	}
	scope, subject := "C2C", event.SubjectID
	if event.Type == "GROUP_DEL_ROBOT" {
		scope, subject = "GROUP", event.ChatID
	}
	raw, _ := json.Marshal(event)
	hash := sha256.Sum256(raw)
	business := sha256.Sum256([]byte(event.Type + ":" + event.EventID))
	tag, e := tx.Exec(ctx, `INSERT INTO openfox.qq_message_receipts(organization_id,id,bot_id,generation,event_id,event_type,scope,chat_id,subject_id,remote_message_id,body_hash,sent_at,disposition,status_reason) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,'IGNORED','lifecycle') ON CONFLICT DO NOTHING`, id, b.ID, b.Generation, event.EventID, event.Type, scope, event.ChatID, subject, "lifecycle:"+hex.EncodeToString(business[:]), hash[:], event.SentAt)
	if e != nil || tag.RowsAffected() == 0 {
		return e
	}
	if scope == "GROUP" {
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_group_epochs SET disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND disabled_at IS NULL AND created_at<($3::timestamptz+interval '1 second')`, b.ID, event.ChatID, event.SentAt)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bindings i SET active=false,revoked_at=clock_timestamp(),version=version+1 WHERE i.organization_id=openfox_private.current_organization_id() AND i.bot_id=$1::uuid AND i.scope='GROUP' AND i.chat_id=$2 AND i.active AND EXISTS(SELECT 1 FROM openfox.qq_group_epochs g WHERE g.organization_id=i.organization_id AND g.id=i.group_epoch_id AND g.disabled_at IS NOT NULL)`, b.ID, event.ChatID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_routes r SET closed_at=clock_timestamp() WHERE r.organization_id=openfox_private.current_organization_id() AND r.bot_id=$1::uuid AND r.scope='GROUP' AND r.chat_id=$2 AND r.closed_at IS NULL AND EXISTS(SELECT 1 FROM openfox.qq_group_epochs g WHERE g.organization_id=r.organization_id AND g.id=r.group_epoch_id AND g.disabled_at IS NOT NULL)`, b.ID, event.ChatID)
		return e
	}
	// Friend deletion revokes that C2C owner and their group authorizations. The
	// timestamp bound prevents a delayed pre-binding deletion revoking a new one.
	rows, e := tx.Query(ctx, `SELECT owner_user_id::text FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND scope='C2C' AND subject_id=$2 AND active AND accepted_at<($3::timestamptz+interval '1 second') FOR UPDATE`, b.ID, event.SubjectID, event.SentAt)
	if e != nil {
		return e
	}
	var owners []string
	for rows.Next() {
		var owner string
		if e = rows.Scan(&owner); e != nil {
			rows.Close()
			return e
		}
		owners = append(owners, owner)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, owner := range owners {
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bindings SET active=false,revoked_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND active`, b.ID, owner)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_routes r SET closed_at=clock_timestamp() WHERE r.organization_id=openfox_private.current_organization_id() AND r.bot_id=$1::uuid AND r.scope='C2C' AND r.closed_at IS NULL AND EXISTS(SELECT 1 FROM openfox.qq_bindings i WHERE i.organization_id=r.organization_id AND i.id=r.identity_id AND i.owner_user_id=$2::uuid AND NOT i.active)`, b.ID, owner)
		if e != nil {
			return e
		}
	}
	return nil
}
