package conversations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type discordInvocation struct {
	routeID, botID, connectionID, identityID, epochID, channelID, guildID string
	generation, identityVersion, epochVersion, actorVersion               int64
}

// CheckDiscordInvocation uses the actual actor. Dispatch can already hold a
// conversation/Agent/Run lock, so this path only locks epoch, identity and member.
// Provider revocation must not acquire those execution locks after these gates.
func CheckDiscordInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	_, err := checkDiscordInvocation(ctx, tx, conversationID, actorID)
	return err
}

func checkDiscordInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) (*discordInvocation, error) {
	var gate discordInvocation
	var routeIdentity, kind string
	var routeIdentityVersion int64
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.bot_id::text,b.connection_id::text,r.channel_id,COALESCE(r.guild_id,''),r.authorization_generation,
 COALESCE(r.epoch_id::text,''),COALESCE(r.identity_id::text,''),COALESCE(r.identity_version,0),r.kind,
 COALESCE(r.closed_at IS NULL AND b.removed_at IS NULL AND c.provider='discord' AND c.status='ACTIVE'
 AND t.state='ACTIVE' AND t.connection_id=b.connection_id AND t.authorization_generation=r.authorization_generation,false)
 FROM openfox.discord_routes r
 JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 LEFT JOIN openfox.discord_bot_controls t ON (t.organization_id,t.bot_id)=(r.organization_id,r.bot_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid`, conversationID).
		Scan(&gate.routeID, &gate.botID, &gate.connectionID, &gate.channelID, &gate.guildID, &gate.generation, &gate.epochID, &routeIdentity, &routeIdentityVersion, &kind, &allowed)
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
		err = tx.QueryRow(ctx, `SELECT version,enabled AND NOT paused AND public_reply_consent AND history_consent AND disabled_at IS NULL
 AND bot_id=$2::uuid AND channel_id=$3 AND authorization_generation=$4 AND guild_id=$5
 FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`,
			gate.epochID, gate.botID, gate.channelID, gate.generation, gate.guildID).Scan(&gate.epochVersion, &allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
	}
	var privateChat string
	err = tx.QueryRow(ctx, `SELECT id::text,version,private_channel_id,authorization_generation=$3 AND disabled_at IS NULL
 FROM openfox.discord_identities WHERE organization_id=openfox_private.current_organization_id()
 AND bot_id=$1::uuid AND owner_user_id=$2::uuid AND active FOR SHARE`, gate.botID, actorID, gate.generation).
		Scan(&gate.identityID, &gate.identityVersion, &privateChat, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	// Rebinding always creates a new DM; an old private route cannot be inherited.
	if kind == "p2p" && (routeIdentity != gate.identityID || routeIdentityVersion != gate.identityVersion || privateChat != gate.channelID) {
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

// LockDiscordInvocation precedes Run/tool locks and uses separate statements
// for the provider lock order. No network request may run inside this transaction.
func LockDiscordInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	var botID, connectionID string
	err := tx.QueryRow(ctx, `SELECT r.bot_id::text,b.connection_id::text FROM openfox.discord_routes r
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
	if err = tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid FOR SHARE`, botID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	return CheckDiscordInvocation(ctx, tx, conversationID, actorID)
}

// Reject the server-owned idempotency namespace before the duplicate shortcut.
func checkDiscordClientID(ctx context.Context, tx pgx.Tx, conversationID string, input MessageInput) error {
	if !input.AuthenticatedUserInput || !strings.HasPrefix(input.ClientMessageID, "discord:") {
		return nil
	}
	var routed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.discord_routes WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid)`, conversationID).Scan(&routed); err != nil {
		return err
	}
	if routed {
		return ErrInvalidRequest
	}
	return nil
}

// All authorization columns must match the current gate. The app role cannot
// mutate snapshots, and queue cleanup only clears the optional inbound reference.
const discordSnapshotCurrentSQL = `s.route_id=$3::uuid AND s.actor_id=$4::uuid AND s.bot_id=$5::uuid
 AND s.channel_id=$6 AND s.authorization_generation=$7 AND s.actor_version=$8
 AND s.identity_id=$9::uuid AND s.identity_version=$10
 AND COALESCE(s.epoch_id::text,'')=$11 AND COALESCE(s.epoch_version,0)=$12
 AND COALESCE(s.guild_id,'')=$13`

func (g *discordInvocation) snapshotArgs(conversationID, objectID, actorID string) []any {
	return []any{conversationID, objectID, g.routeID, actorID, g.botID, g.channelID, g.generation, g.actorVersion, g.identityID, g.identityVersion, g.epochID, g.epochVersion, g.guildID}
}

func replayDiscordInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, existingID string) error {
	gate, err := checkDiscordInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	if strings.HasPrefix(input.ClientMessageID, "discord:") && input.ID != existingID {
		return ErrDuplicateMessage
	}
	input.ID = existingID
	return snapshotDiscordInput(ctx, tx, conversationID, actorID, input, true)
}

func snapshotDiscordInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, replay bool) error {
	gate, err := checkDiscordInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var source string
	err = tx.QueryRow(ctx, `SELECT source_kind FROM openfox.discord_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, input.ID).Scan(&source)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if source == "REMOTE" {
		if input.AuthenticatedUserInput || input.ClientMessageID != "discord:"+gate.botID+":"+input.ID {
			return ErrForbidden
		}
		var matches bool
		// A preinserted row alone is insufficient: prove it against the durable
		// receipt and original queue record, including the actual sender identity.
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.discord_inputs s
 JOIN openfox.discord_routes r ON (r.organization_id,r.id)=(s.organization_id,s.route_id)
 JOIN openfox.discord_message_receipts p ON (p.organization_id,p.id)=(s.organization_id,s.receipt_id)
 JOIN openfox.im_inbound_messages q ON (q.organization_id,q.id)=(s.organization_id,s.inbound_id)
 JOIN openfox.discord_identities di ON (di.organization_id,di.id)=(s.organization_id,s.identity_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND `+discordSnapshotCurrentSQL+` AND s.inbound_id=s.message_id AND p.id=s.message_id AND p.connection_id=$14::uuid
 AND (p.bot_id,p.route_id,p.channel_id,p.remote_message_id,p.actor_id,p.actor_version,p.identity_id,p.identity_version,p.authorization_generation)
 = (s.bot_id,s.route_id,s.channel_id,s.remote_message_id,s.actor_id,s.actor_version,s.identity_id,s.identity_version,s.authorization_generation)
 AND p.inbound_id=s.inbound_id AND p.kind='MESSAGE' AND NOT p.source_deleted
 AND p.guild_id IS NOT DISTINCT FROM s.guild_id AND p.epoch_id IS NOT DISTINCT FROM s.epoch_id AND p.epoch_version IS NOT DISTINCT FROM s.epoch_version
 AND p.sender_id=di.remote_user_id AND q.bot_id=s.bot_id AND q.chat_id=s.channel_id
 AND q.remote_message_id=s.remote_message_id AND q.parent_remote_id=s.remote_message_id
 AND q.sender_open_id=di.remote_user_id AND q.sender_union_id='' AND q.chat_type=r.kind
 AND ((q.status='PROCESSING' AND p.result='MATERIALIZING') OR(q.status='DONE' AND p.result='MATERIALIZED')))`,
			append(gate.snapshotArgs(conversationID, input.ID, actorID), gate.connectionID)...).Scan(&matches)
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
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.discord_inputs s JOIN openfox.messages m
 ON (m.organization_id,m.id)=(s.organization_id,s.message_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND m.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND s.source_kind='OPENFOX' AND `+discordSnapshotCurrentSQL+`)`, gate.snapshotArgs(conversationID, input.ID, actorID)...).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches || strings.HasPrefix(input.ClientMessageID, "discord:") {
			return ErrForbidden
		}
		return nil
	}
	if strings.HasPrefix(input.ClientMessageID, "discord:") {
		return ErrForbidden // REMOTE must have been inserted by the trusted worker.
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.discord_inputs(organization_id,message_id,source_kind,route_id,actor_id,bot_id,channel_id,
 authorization_generation,actor_version,identity_id,identity_version,epoch_id,epoch_version,guild_id)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'OPENFOX',$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid,$9,NULLIF($10,'')::uuid,NULLIF($11,0),NULLIF($12,''))`,
		gate.snapshotArgs(conversationID, input.ID, actorID)[1:]...)
	return err
}

// The receipt survives queue cleanup and carries known source deletions.
// Native OpenFox inputs do not have a remote receipt.
const discordSourceAvailableSQL = `(s.source_kind='OPENFOX' OR (s.source_kind='REMOTE' AND EXISTS(
 SELECT 1 FROM openfox.discord_message_receipts p
 WHERE (p.organization_id,p.id)=(s.organization_id,s.receipt_id)
 AND p.kind='MESSAGE' AND NOT p.source_deleted)))`

func CheckDiscordInputs(ctx context.Context, tx pgx.Tx, conversationID, actorID string, inputIDs []string) error {
	gate, err := checkDiscordInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	args := gate.snapshotArgs(conversationID, "", actorID)
	args[1] = inputIDs
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs i JOIN openfox.discord_inputs s
 ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.id=ANY($2::uuid[])
 AND `+discordSnapshotCurrentSQL+` AND `+discordSourceAvailableSQL, args...).Scan(&count)
	if err != nil {
		return err
	}
	if len(inputIDs) == 0 || count != len(inputIDs) {
		return ErrForbidden
	}
	return nil
}

// Consumed inputs are retained and checked too; a rebind cannot revive a Run.
func CheckDiscordRun(ctx context.Context, tx pgx.Tx, conversationID, actorID, runID string) error {
	gate, err := checkDiscordInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT count(*)>0 AND COALESCE(bool_and(COALESCE((`+discordSnapshotCurrentSQL+`) AND `+discordSourceAvailableSQL+`,false)),false)
 FROM openfox.conversation_inputs i LEFT JOIN openfox.discord_inputs s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
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
