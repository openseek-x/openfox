package conversations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type qqInvocation struct {
	routeID, botID, connectionID, identityID, epochID, chatID, scope string
	generation, identityVersion, epochVersion, actorVersion          int64
}

// CheckQQInvocation uses the actual actor. Dispatch can already hold a
// conversation/Agent/Run lock, so this path only locks epoch, identity and member.
// Provider revocation must not acquire those execution locks after these gates.
func CheckQQInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	_, err := checkQQInvocation(ctx, tx, conversationID, actorID)
	return err
}

func checkQQInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) (*qqInvocation, error) {
	var gate qqInvocation
	var routeIdentity string
	var routeIdentityVersion int64
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.bot_id::text,b.connection_id::text,r.chat_id,r.generation,
 COALESCE(r.group_epoch_id::text,''),COALESCE(r.identity_id::text,''),COALESCE(r.identity_version,0),r.scope,
 COALESCE(r.closed_at IS NULL AND b.removed_at IS NULL AND c.provider='qq' AND c.status='ACTIVE'
 AND t.state='ACTIVE' AND t.connection_id=b.connection_id AND t.generation=r.generation,false)
 FROM openfox.qq_routes r
 JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 LEFT JOIN openfox.qq_bot_controls t ON (t.organization_id,t.bot_id)=(r.organization_id,r.bot_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid`, conversationID).
		Scan(&gate.routeID, &gate.botID, &gate.connectionID, &gate.chatID, &gate.generation, &gate.epochID, &routeIdentity, &routeIdentityVersion, &gate.scope, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // Ordinary and other-provider conversations.
	}
	if err != nil {
		return nil, err
	}
	if !allowed || actorID == "" {
		return nil, ErrForbidden
	}
	if gate.epochID != "" {
		err = tx.QueryRow(ctx, `SELECT version,public_reply_consent AND disabled_at IS NULL
 AND bot_id=$2::uuid AND chat_id=$3 AND generation=$4
 FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`,
			gate.epochID, gate.botID, gate.chatID, gate.generation).Scan(&gate.epochVersion, &allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
	}
	err = tx.QueryRow(ctx, `SELECT id::text,version,generation=$5 AND revoked_at IS NULL
 AND COALESCE(group_epoch_id::text,'')=$6
 FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id()
 AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND scope=$3 AND chat_id=$4 AND active FOR SHARE`,
		gate.botID, actorID, gate.scope, gate.chatID, gate.generation, gate.epochID).
		Scan(&gate.identityID, &gate.identityVersion, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	// Each scope has its own identity; a rebind cannot inherit an old private route.
	if gate.scope == "C2C" && (routeIdentity != gate.identityID || routeIdentityVersion != gate.identityVersion) {
		return nil, ErrForbidden
	}
	err = tx.QueryRow(ctx, `SELECT authorization_version,status='ACTIVE' AND role<>'GUEST'
 FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actorID).
		Scan(&gate.actorVersion, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	return &gate, err
}

// LockQQInvocation precedes Run/tool locks and uses separate statements
// for the provider lock order. No network request may run inside this transaction.
func LockQQInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	var botID, connectionID string
	err := tx.QueryRow(ctx, `SELECT r.bot_id::text,b.connection_id::text FROM openfox.qq_routes r
 JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid`, conversationID).Scan(&botID, &connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, botID).Scan(&locked); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, connectionID).Scan(&locked); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid FOR SHARE`, botID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	return CheckQQInvocation(ctx, tx, conversationID, actorID)
}

// Reject the server-owned idempotency namespace before the duplicate shortcut.
func checkQQClientID(ctx context.Context, tx pgx.Tx, conversationID string, input MessageInput) error {
	if !input.AuthenticatedUserInput || !strings.HasPrefix(input.ClientMessageID, "qq:") {
		return nil
	}
	var routed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_routes WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid)`, conversationID).Scan(&routed); err != nil {
		return err
	}
	if routed {
		return ErrInvalidRequest
	}
	return nil
}

// All authorization columns must match the current gate. The app role cannot
// mutate snapshots, and queue cleanup only clears the optional inbound reference.
const qqSnapshotCurrentSQL = `s.route_id=$3::uuid AND s.actor_id=$4::uuid
 AND s.generation=$5 AND s.actor_version=$6 AND s.identity_id=$7::uuid AND s.identity_version=$8
 AND COALESCE(s.group_epoch_id::text,'')=$9 AND COALESCE(s.epoch_version,0)=$10`

func (g *qqInvocation) snapshotArgs(conversationID, objectID, actorID string) []any {
	return []any{conversationID, objectID, g.routeID, actorID, g.generation, g.actorVersion, g.identityID, g.identityVersion, g.epochID, g.epochVersion}
}

func replayQQInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, existingID string) error {
	gate, err := checkQQInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	if strings.HasPrefix(input.ClientMessageID, "qq:") && input.ID != existingID {
		return ErrDuplicateMessage
	}
	input.ID = existingID
	return snapshotQQInput(ctx, tx, conversationID, actorID, input, true)
}

func snapshotQQInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, replay bool) error {
	gate, err := checkQQInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var source string
	err = tx.QueryRow(ctx, `SELECT source FROM openfox.qq_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, input.ID).Scan(&source)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if source == "REMOTE" {
		if input.AuthenticatedUserInput || input.ClientMessageID != "qq:"+gate.botID+":"+input.ID {
			return ErrForbidden
		}
		var matches bool
		// A preinserted row alone is insufficient: prove it against the durable
		// receipt and original queue record, including the actual sender identity.
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_inputs s
 JOIN openfox.qq_routes r ON (r.organization_id,r.id)=(s.organization_id,s.route_id)
 JOIN openfox.qq_message_receipts p ON (p.organization_id,p.id)=(s.organization_id,s.receipt_id)
 JOIN openfox.im_inbound_messages q ON (q.organization_id,q.id)=(p.organization_id,p.inbound_id)
 JOIN openfox.qq_bindings bi ON (bi.organization_id,bi.id)=(s.organization_id,s.identity_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND `+qqSnapshotCurrentSQL+` AND q.id=s.message_id AND p.bot_id=$11::uuid
 AND (p.route_id,p.actor_id,p.actor_version,p.identity_id,p.identity_version,p.generation)
 = (s.route_id,s.actor_id,s.actor_version,s.identity_id,s.identity_version,s.generation)
 AND p.group_epoch_id IS NOT DISTINCT FROM s.group_epoch_id AND p.epoch_version IS NOT DISTINCT FROM s.epoch_version
 AND p.scope=r.scope AND p.chat_id=r.chat_id AND p.subject_id=bi.subject_id
 AND p.disposition IN ('QUEUED','DONE') AND q.status IN ('PROCESSING','DONE')
 AND q.bot_id=r.bot_id AND q.chat_id=r.id::text AND q.remote_message_id=p.id::text
 AND q.sender_open_id=bi.id::text AND q.sender_union_id=''
 AND q.chat_type=CASE WHEN r.scope='C2C' THEN 'p2p' ELSE 'group' END)`,
			append(gate.snapshotArgs(conversationID, input.ID, actorID), gate.botID)...).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return ErrForbidden
		}
		return nil
	}
	if source != "" || replay {
		// Native idempotent retries may return their own original OPENFOX message;
		// a missing snapshot or changed authority never creates a replacement.
		var matches bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_inputs s JOIN openfox.messages m
 ON (m.organization_id,m.id)=(s.organization_id,s.message_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND m.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND s.source='OPENFOX' AND `+qqSnapshotCurrentSQL+`)`, gate.snapshotArgs(conversationID, input.ID, actorID)...).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches || strings.HasPrefix(input.ClientMessageID, "qq:") {
			return ErrForbidden
		}
		return nil
	}
	if strings.HasPrefix(input.ClientMessageID, "qq:") {
		return ErrForbidden // REMOTE must have been inserted by the trusted worker.
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.qq_inputs(organization_id,message_id,source,route_id,actor_id,
 generation,actor_version,identity_id,identity_version,group_epoch_id,epoch_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'OPENFOX',$2::uuid,$3::uuid,$4,$5,$6::uuid,$7,NULLIF($8,'')::uuid,NULLIF($9,0))`,
		gate.snapshotArgs(conversationID, input.ID, actorID)[1:]...)
	return err
}

func CheckQQInputs(ctx context.Context, tx pgx.Tx, conversationID, actorID string, inputIDs []string) error {
	gate, err := checkQQInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	args := gate.snapshotArgs(conversationID, "", actorID)
	args[1] = inputIDs
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs i JOIN openfox.qq_inputs s
 ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.id=ANY($2::uuid[])
 AND `+qqSnapshotCurrentSQL, args...).Scan(&count)
	if err != nil {
		return err
	}
	if len(inputIDs) == 0 || count != len(inputIDs) {
		return ErrForbidden
	}
	return nil
}

// Consumed inputs are retained and checked too; a rebind cannot revive a Run.
func CheckQQRun(ctx context.Context, tx pgx.Tx, conversationID, actorID, runID string) error {
	gate, err := checkQQInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT count(*)>0 AND COALESCE(bool_and(COALESCE(`+qqSnapshotCurrentSQL+`,false)),false)
 FROM openfox.conversation_inputs i LEFT JOIN openfox.qq_inputs s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.run_id=$2::uuid`,
		gate.snapshotArgs(conversationID, runID, actorID)...).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
