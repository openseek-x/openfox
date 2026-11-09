package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/execution"
	"openfox/internal/membership"
)

// Called only inside CallTool's locked, live, authorized run transaction.
// Match a supplied address or ID without offering an organization-wide directory.
func socialRecipient(ctx context.Context, tx pgx.Tx, run execution.Run, value string, chat bool) (string, error) {
	var uid string
	err := tx.QueryRow(ctx, `SELECT m.user_id::text FROM openfox.organization_members m JOIN openfox.users u ON u.id=m.user_id
        JOIN openfox.spaces s ON s.organization_id=m.organization_id AND s.id=$1 AND s.deleted_at IS NULL
        LEFT JOIN openfox.space_members sm ON sm.organization_id=m.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id
        WHERE m.organization_id=openfox_private.current_organization_id() AND m.status='ACTIVE'
          AND (lower(u.email)=lower($2) OR m.user_id::text=lower($2))
          AND (sm.status='ACTIVE' OR (s.visibility='SHARED' AND m.role<>'GUEST' AND sm.user_id IS NULL))
          AND (NOT $4 OR EXISTS (SELECT 1 FROM openfox.conversations c WHERE c.organization_id=m.organization_id AND c.id=$3 AND c.space_id=s.id
            AND (c.owner_user_id=m.user_id OR (c.visibility='SHARED' AND EXISTS(SELECT 1 FROM openfox.conversation_members cm WHERE cm.organization_id=c.organization_id AND cm.conversation_id=c.id AND cm.user_id=m.user_id)))))`, run.SpaceID, value, run.ConversationID, chat).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return uid, err
}
func socialArg(args map[string]json.RawMessage, key string, max int) (string, bool) {
	raw, ok := args[key]
	if !ok {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	s = strings.TrimSpace(s)
	return s, s != "" && len([]rune(s)) <= max
}
func postAgentComment(ctx context.Context, tx pgx.Tx, run execution.Run, callID, body string, args map[string]json.RawMessage) (ToolResult, error) {
	target, ok := socialArg(args, "reply_to_id", 36)
	if !ok || target != "" && !membership.ValidateUUID(target) {
		return ToolResult{Text: "invalid reply target"}, nil
	}
	name, ok := socialArg(args, "mention", 254)
	if !ok {
		return ToolResult{Text: "invalid mention"}, nil
	}
	mention := ""
	var err error
	if name != "" {
		mention, err = socialRecipient(ctx, tx, run, name, false)
		if err != nil {
			return ToolResult{}, err
		}
		if mention == "" {
			return ToolResult{Text: "mention target is unavailable"}, nil
		}
	}
	clientID := "tool:" + run.ID + ":" + callID
	var same bool
	err = tx.QueryRow(ctx, `SELECT c.body=$3 AND c.reply_to_id IS NOT DISTINCT FROM nullif($4,'')::uuid AND
        (SELECT mm.mentioned_user_id FROM openfox.comment_mentions mm WHERE mm.organization_id=c.organization_id AND mm.comment_id=c.id LIMIT 1) IS NOT DISTINCT FROM nullif($5,'')::uuid
        FROM openfox.task_comments c WHERE c.organization_id=openfox_private.current_organization_id() AND c.task_id=$1 AND c.client_comment_id=$2`, run.TaskID, clientID, body, target, mention).Scan(&same)
	if err == nil {
		if same {
			return ToolResult{OK: true, Text: "comment already posted"}, nil
		}
		return ToolResult{Text: "call ID already used for different content"}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{}, err
	}
	if target != "" {
		root, err := collaboration.LockCommentThread(ctx, tx, run.SpaceID, run.TaskID, target)
		var rule *collaboration.RuleError
		if errors.As(err, &rule) {
			return ToolResult{Text: "reply target is unavailable"}, nil
		}
		if err != nil {
			return ToolResult{}, err
		}
		if root.ResolutionID != "" {
			return ToolResult{Text: "reopen the discussion before replying"}, nil
		}
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_agent_id,run_id,body,client_comment_id,reply_to_id)
        SELECT organization_id,space_id,id,$3,$4,$5,$6,nullif($7,'')::uuid FROM openfox.tasks
        WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND space_id=$2 AND deleted_at IS NULL
        ON CONFLICT DO NOTHING RETURNING id::text`, run.TaskID, run.SpaceID, run.AgentID, run.ID, body, clientID, target).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "task or comment is unavailable"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	if mention != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.comment_mentions(organization_id,comment_id,position,mentioned_user_id) VALUES(openfox_private.current_organization_id(),$1,0,$2)`, id, mention); err != nil {
			return ToolResult{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT openfox_private.enqueue_notification($1,'MENTION',NULL,NULL,$2,$3,'comment-mention:'||$3::uuid::text||':'||$1::uuid::text,left($4,200))`, mention, run.TaskID, id, body); err != nil {
			return ToolResult{}, err
		}
	}
	_, err = tx.Exec(ctx, `SELECT openfox_private.enqueue_notification(a.user_id,'TASK_COMMENT',NULL,NULL,$1::uuid,$2::uuid,
        'comment:'||$2::text||':'||a.user_id::text,left($3,200)) FROM openfox_private.task_notice_audience(openfox_private.current_organization_id(),$1::uuid,
         (SELECT author_user_id FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND id=nullif($4,'')::uuid)) a
        WHERE a.user_id IS DISTINCT FROM nullif($5,'')::uuid`, run.TaskID, id, body, target, mention)
	return ToolResult{OK: err == nil, Text: "comment posted"}, err
}
func toolReaction(ctx context.Context, tx pgx.Tx, run execution.Run, args map[string]json.RawMessage) (ToolResult, error) {
	emoji, ok := socialArg(args, "emoji", 32)
	if !ok || emoji == "" {
		return ToolResult{Text: "invalid emoji"}, nil
	}
	comment, ok := socialArg(args, "comment_id", 36)
	if !ok || comment != "" && !membership.ValidateUUID(comment) {
		return ToolResult{Text: "invalid reaction target"}, nil
	}
	var task string
	err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, run.SpaceID, run.TaskID).Scan(&task)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "task unavailable"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	if comment != "" {
		_, err := collaboration.LockCommentThread(ctx, tx, run.SpaceID, task, comment)
		var rule *collaboration.RuleError
		if errors.As(err, &rule) {
			return ToolResult{Text: "comment unavailable"}, nil
		}
		if err != nil {
			return ToolResult{}, err
		}
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT count(distinct emoji)<50 OR coalesce(bool_or(emoji=$3),false) FROM openfox.task_reactions WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1 AND comment_id IS NOT DISTINCT FROM nullif($2,'')::uuid`, task, comment, emoji).Scan(&allowed); err != nil {
		return ToolResult{}, err
	}
	if !allowed {
		return ToolResult{Text: "reaction limit reached"}, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.task_reactions(organization_id,space_id,task_id,comment_id,agent_id,run_id,emoji)
         VALUES(openfox_private.current_organization_id(),$1,$2,nullif($3,'')::uuid,$4,$5,$6) ON CONFLICT DO NOTHING`, run.SpaceID, task, comment, run.AgentID, run.ID, emoji)
	return ToolResult{OK: err == nil, Text: "reaction added"}, err
}
func toolChatMention(ctx context.Context, tx pgx.Tx, run execution.Run, callID string, args map[string]json.RawMessage) (ToolResult, error) {
	name, ok := socialArg(args, "mention", 254)
	if !ok || name == "" {
		return ToolResult{Text: "mention is required"}, nil
	}
	body, ok := socialArg(args, "body", maxToolCommentBody)
	if !ok || body == "" {
		return ToolResult{Text: "body is required"}, nil
	}
	uid, err := socialRecipient(ctx, tx, run, name, true)
	if err != nil {
		return ToolResult{}, err
	}
	if uid == "" {
		return ToolResult{Text: "mention target is unavailable"}, nil
	}
	clientID := "tool:" + run.ID + ":" + callID
	var same bool
	err = tx.QueryRow(ctx, `SELECT m.body=$3 AND EXISTS(SELECT 1 FROM openfox.message_mentions mm WHERE mm.organization_id=m.organization_id AND mm.message_id=m.id AND mentioned_user_id=$4)
        FROM openfox.messages m WHERE m.organization_id=openfox_private.current_organization_id() AND m.conversation_id=$1 AND m.client_message_id=$2 AND m.author_agent_id=$5`, run.ConversationID, clientID, body, uid, run.AgentID).Scan(&same)
	if err == nil {
		if same {
			return ToolResult{OK: true, Text: "mention already sent"}, nil
		}
		return ToolResult{Text: "call ID already used for different content"}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `WITH next AS (UPDATE openfox.conversations SET next_message_seq=next_message_seq+1,version=version+1,updated_at=clock_timestamp()
         WHERE organization_id=openfox_private.current_organization_id() AND id=$1 RETURNING next_message_seq-1 AS seq)
        INSERT INTO openfox.messages(organization_id,conversation_id,seq,author_kind,author_agent_id,body,client_message_id)
        SELECT openfox_private.current_organization_id(),$1,seq,'AGENT',$2,$3,$4 FROM next ON CONFLICT DO NOTHING RETURNING id::text`, run.ConversationID, run.AgentID, body, clientID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "message ID unavailable"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.message_mentions(organization_id,message_id,position,mentioned_user_id) VALUES(openfox_private.current_organization_id(),$1,0,$2)`, id, uid); err != nil {
		return ToolResult{}, err
	}
	_, err = tx.Exec(ctx, `SELECT openfox_private.enqueue_notification($1,'MENTION',$2,$3,NULL,NULL,'mention:'||$3::uuid::text||':'||$1::uuid::text,left($4,200))`, uid, run.ConversationID, id, body)
	return ToolResult{OK: err == nil, Text: "mention sent"}, err
}
