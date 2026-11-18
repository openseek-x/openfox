package conversations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/membership"
)

type discordReplyAuthority struct {
	botID, routeID, actorID, identityID, epochID, channelID, guildID string
	generation, actorVersion, identityVersion, epochVersion          int64
}

type discordReplyInput struct {
	inputID, snapshotID, remoteMessageID string
	authority                            discordReplyAuthority
}

// loadDiscordReplyBatch reads only the explicitly rejected batch or assigned
// Run, never the latest message in a conversation. The two predicates are
// internal SQL constants. A missing or mixed source deliberately stays local.
func loadDiscordReplyBatch(ctx context.Context, tx pgx.Tx, conversationID, predicate string, value any) ([]discordReplyInput, error) {
	rows, err := tx.Query(ctx, `SELECT i.id::text,COALESCE(s.id::text,''),COALESCE(s.remote_message_id,''),
 COALESCE(s.bot_id::text,''),COALESCE(s.route_id::text,''),COALESCE(s.actor_id::text,''),
 COALESCE(s.identity_id::text,''),COALESCE(s.epoch_id::text,''),COALESCE(s.channel_id,''),COALESCE(s.guild_id,''),
 COALESCE(s.authorization_generation,0),COALESCE(s.actor_version,0),COALESCE(s.identity_version,0),COALESCE(s.epoch_version,0),
 COALESCE(s.source_kind='REMOTE' AND p.kind='MESSAGE' AND NOT p.source_deleted AND r.conversation_id=i.conversation_id,false)
 FROM openfox.conversation_inputs i
 LEFT JOIN openfox.discord_inputs s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 LEFT JOIN openfox.discord_routes r ON (r.organization_id,r.id)=(s.organization_id,s.route_id)
 LEFT JOIN openfox.discord_message_receipts p ON (p.organization_id,p.id)=(s.organization_id,s.receipt_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND `+predicate+`
 ORDER BY i.created_at,i.id LIMIT 21`, conversationID, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []discordReplyInput
	valid := true
	for rows.Next() {
		var in discordReplyInput
		var remote bool
		a := &in.authority
		if err := rows.Scan(&in.inputID, &in.snapshotID, &in.remoteMessageID,
			&a.botID, &a.routeID, &a.actorID, &a.identityID, &a.epochID, &a.channelID, &a.guildID,
			&a.generation, &a.actorVersion, &a.identityVersion, &a.epochVersion, &remote); err != nil {
			return nil, err
		}
		valid = valid && remote && (len(batch) == 0 || batch[0].authority == in.authority)
		batch = append(batch, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !valid || len(batch) == 0 || len(batch) > 20 {
		return nil, nil
	}
	return batch, nil
}

func isDiscordConversation(ctx context.Context, tx pgx.Tx, conversationID string) (bool, error) {
	var routed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.discord_routes
 WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid)`, conversationID).Scan(&routed)
	return routed, err
}

// RecordDiscordRejectedInputs is called only by dispatch's no-Run rejection
// path, after it writes the exact REJECTED inputs and SYSTEM message. Persisting
// the source never grants send permission; the sender must recheck all inputs.
func RecordDiscordRejectedInputs(ctx context.Context, tx pgx.Tx, conversationID, messageID string, inputIDs []string) error {
	routed, err := isDiscordConversation(ctx, tx, conversationID)
	if err != nil || !routed {
		return err
	}
	if len(inputIDs) < 1 || len(inputIDs) > 20 {
		return ErrInvalidRequest
	}
	seen := make(map[string]bool, len(inputIDs))
	for _, id := range inputIDs {
		if !membership.ValidateUUID(id) || seen[id] {
			return ErrInvalidRequest
		}
		seen[id] = true
	}
	batch, err := loadDiscordReplyBatch(ctx, tx, conversationID, "i.id=ANY($2::uuid[]) AND i.status='REJECTED' AND i.run_id IS NULL", inputIDs)
	if err != nil || len(batch) != len(inputIDs) {
		return err // Incomplete or mixed sources have no external reply.
	}
	ordered, snapshots := make([]string, len(batch)), make([]string, len(batch))
	for i, in := range batch {
		ordered[i], snapshots[i] = in.inputID, in.snapshotID
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.discord_reply_sources
 (organization_id,message_id,conversation_input_id,snapshot_id,ordinal,batch_size)
 SELECT openfox_private.current_organization_id(),$1::uuid,x.input_id,x.snapshot_id,x.ordinal,$4
 FROM unnest($2::uuid[],$3::uuid[]) WITH ORDINALITY AS x(input_id,snapshot_id,ordinal)
 ON CONFLICT DO NOTHING`, messageID, ordered, snapshots, len(batch))
	if err != nil {
		return err
	}
	// Idempotent repeats must retain the same complete membership and ordering.
	var matches bool
	err = tx.QueryRow(ctx, `SELECT count(*)=$4 AND COALESCE(bool_and(
 s.conversation_input_id=($2::uuid[])[s.ordinal] AND s.snapshot_id=($3::uuid[])[s.ordinal] AND s.batch_size=$4),false)
 FROM openfox.discord_reply_sources s
 WHERE s.organization_id=openfox_private.current_organization_id() AND s.message_id=$1::uuid`,
		messageID, ordered, snapshots, len(batch)).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrDuplicateMessage
	}
	return enqueueDiscordSource(ctx, tx, conversationID, messageID, "", batch)
}

// EnqueueDiscordRunReply is called only at the authoritative terminal message
// insertion. Intermediate tool output and native OPENFOX inputs stay local.
func EnqueueDiscordRunReply(ctx context.Context, tx pgx.Tx, conversationID, messageID, runID string) error {
	routed, err := isDiscordConversation(ctx, tx, conversationID)
	if err != nil || !routed {
		return err
	}
	batch, err := loadDiscordReplyBatch(ctx, tx, conversationID, "i.run_id=$2::uuid", runID)
	if err != nil || len(batch) == 0 {
		return err
	}
	var belongs bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.runs r JOIN openfox.messages m
 ON m.organization_id=r.organization_id AND m.conversation_id=r.conversation_id
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1::uuid
 AND r.conversation_id=$2::uuid AND r.actor_id=$3::uuid AND m.id=$4::uuid
 AND (m.author_kind='SYSTEM' OR(m.author_kind='AGENT' AND m.author_agent_id=r.agent_id)))`,
		runID, conversationID, batch[0].authority.actorID, messageID).Scan(&belongs)
	if err != nil {
		return err
	}
	if !belongs {
		return ErrForbidden
	}
	return enqueueDiscordSource(ctx, tx, conversationID, messageID, runID, batch)
}

func enqueueDiscordSource(ctx context.Context, tx pgx.Tx, conversationID, messageID, runID string, batch []discordReplyInput) error {
	last := batch[len(batch)-1]
	// This weak gate is safe after the caller has locked the Run/conversation.
	// A revoked authorization must not roll back the local terminal message.
	gate, err := checkDiscordInvocation(ctx, tx, conversationID, last.authority.actorID)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if gate == nil || (discordReplyAuthority{gate.botID, gate.routeID, last.authority.actorID, gate.identityID, gate.epochID,
		gate.channelID, gate.guildID, gate.generation, gate.actorVersion, gate.identityVersion, gate.epochVersion}) != last.authority {
		return nil
	}
	// 149's per-provider INSERT trigger serializes the queue budget and records
	// FAILED/queue_full without losing the terminal message. No reverse bot lock.
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox
 (organization_id,bot_id,chat_id,message_id,reply_to_remote_id,source_run_id,source_terminal)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,NULLIF($5,'')::uuid,$6)
 ON CONFLICT DO NOTHING`, last.authority.botID, last.authority.channelID, messageID, last.remoteMessageID, runID, runID != "")
	if err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT bot_id=$2::uuid AND chat_id=$3 AND reply_to_remote_id=$4
 AND source_run_id IS NOT DISTINCT FROM NULLIF($5,'')::uuid AND source_terminal=$6
 FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`,
		messageID, last.authority.botID, last.authority.channelID, last.remoteMessageID, runID, runID != "").Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrDuplicateMessage
	}
	return nil
}
