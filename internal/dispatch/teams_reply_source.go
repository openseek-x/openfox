package dispatch

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"openfox/internal/execution"
	"openfox/internal/integrations/teams"
)

// Called only beside insertTerminalMessage, never by social/proposal messages.
// Authorization changes must not prevent storing the local Run result; the
// sender rechecks the retained original inputs and current authority before I/O.
func recordTeamsTerminalReply(ctx context.Context, tx pgx.Tx, run execution.Run, messageID string) error {
	return recordTeamsReplySource(ctx, tx, run.ConversationID, run.ActorID, run.ID, messageID, nil)
}
func recordTeamsRejectedReply(ctx context.Context, tx pgx.Tx, conversationID string, inputs []pendingInput, messageID string) error {
	if len(inputs) == 0 || len(inputs) > 20 {
		return nil
	}
	ids, _ := inputKeys(inputs)
	return recordTeamsReplySource(ctx, tx, conversationID, inputs[0].AuthorUserID, "", messageID, ids)
}
func recordTeamsReplySource(ctx context.Context, tx pgx.Tx, conversationID, actorID, runID, messageID string, rejected []string) error {
	var botID, bindingID, chatID string
	err := tx.QueryRow(ctx, `SELECT b.bot_id::text,b.id::text,b.chat_id FROM openfox.im_chat_bindings b
 JOIN openfox.im_bots bot ON (bot.organization_id,bot.id)=(b.organization_id,b.bot_id)
 JOIN openfox.integration_connections c ON(c.organization_id,c.id)=(bot.organization_id,bot.connection_id)
 WHERE b.organization_id=openfox_private.current_organization_id() AND b.conversation_id=$1::uuid AND c.provider='teams'`, conversationID).Scan(&botID, &bindingID, &chatID)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT COALESCE(s.message_id::text,''),COALESCE(s.source_kind,''),COALESCE(s.bot_id::text,''),COALESCE(s.binding_id::text,''),
 COALESCE(s.actor_id::text,''),COALESCE(s.actor_version,0),COALESCE(s.identity_id::text,''),COALESCE(s.identity_version,0),
 COALESCE(s.scope_id::text,''),COALESCE(s.scope_generation,0),s.route,COALESCE(s.event_id,''),COALESCE(encode(s.payload_hash,'hex'),'')
 FROM openfox.conversation_inputs i JOIN openfox.messages m ON (m.organization_id,m.id)=(i.organization_id,i.message_id)
 LEFT JOIN openfox.teams_inputs s ON(s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$2::uuid AND((NULLIF($1,'')::uuid IS NOT NULL AND i.run_id=NULLIF($1,'')::uuid) OR($1='' AND i.id=ANY($3::uuid[]) AND i.run_id IS NULL AND i.status='REJECTED'))
 ORDER BY m.seq,i.id LIMIT 21`, runID, conversationID, rejected)
	if err != nil {
		return err
	}
	var chosen, first teams.Context
	count := 0
	valid := true
	for rows.Next() {
		var c teams.Context
		var source, bot, binding string
		var raw []byte
		if err = rows.Scan(&c.InputID, &source, &bot, &binding, &c.ActorID, &c.ActorVersion, &c.IdentityID, &c.IdentityVersion, &c.ScopeID, &c.ScopeGeneration, &raw, &c.EventID, &c.BodyHash); err != nil {
			rows.Close()
			return err
		}
		count++
		c.BindingID = binding
		if count > 20 || c.InputID == "" || bot != botID || binding != bindingID || c.ActorID != actorID || c.IdentityID == "" || source != "REMOTE" {
			valid = false
		}
		if count == 1 {
			first = c
		} else if c.IdentityID != first.IdentityID || c.IdentityVersion != first.IdentityVersion || c.ActorVersion != first.ActorVersion || c.ScopeID != first.ScopeID || c.ScopeGeneration != first.ScopeGeneration {
			valid = false
		}
		if source == "REMOTE" {
			if json.Unmarshal(raw, &c.Route) != nil || c.Route.Validate() != nil {
				valid = false
			}
			chosen = c
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !valid || count == 0 || chosen.InputID == "" || (runID == "" && count != len(rejected)) {
		return nil
	} // Native-only or incomplete source stays local.
	chosen.RejectedInputs = rejected
	raw, err := json.Marshal(chosen)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,message_id,reply_to_remote_id,teams_context,group_authorization_version,source_run_id,source_terminal)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,$5::jsonb,NULLIF($6,0),NULLIF($7,'')::uuid,$7<>'')
 ON CONFLICT(organization_id,message_id) WHERE message_id IS NOT NULL DO NOTHING`, botID, chatID, messageID, chosen.EventID, raw, chosen.ScopeGeneration, runID)
	return err
}
