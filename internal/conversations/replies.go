package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const MaxReplyRunes = 2000

// MessageReply is server-derived quoted text, never accepted from a client.
// Snapshot=false identifies an old reply whose current source text is displayed.
type MessageReply struct {
	MessageID     string    `json:"message_id"`
	Seq           int64     `json:"seq"`
	AuthorKind    string    `json:"author_kind"`
	AuthorUserID  string    `json:"author_user_id,omitempty"`
	AuthorAgentID string    `json:"author_agent_id,omitempty"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
	Truncated     bool      `json:"truncated"`
	Snapshot      bool      `json:"snapshot"`
}

// Called after locking and authorizing the destination conversation. The source
// must be in that conversation; missing and inaccessible IDs have the same error.
func snapshotReply(ctx context.Context, tx pgx.Tx, conversationID, id string) ([]byte, error) {
	if id == "" {
		return nil, nil
	}
	var reply MessageReply
	err := tx.QueryRow(ctx, `SELECT id::text, seq, author_kind,
		COALESCE(author_user_id::text,''), COALESCE(author_agent_id::text,''),
		left(body,$3), created_at, length(body)>$3
		FROM openfox.messages
		WHERE organization_id=openfox_private.current_organization_id()
		AND conversation_id=$1::uuid AND id=$2::uuid AND kind<>'REFERENCE' FOR SHARE`, conversationID, id, MaxReplyRunes).
		Scan(&reply.MessageID, &reply.Seq, &reply.AuthorKind, &reply.AuthorUserID,
			&reply.AuthorAgentID, &reply.Body, &reply.CreatedAt, &reply.Truncated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	reply.Snapshot = true
	return json.Marshal(reply)
}

// LoadMessageReplies loads at most one batch, after callers have authorized the
// messages' conversation(s). Dispatch calls it only for its authorized run.
// Legacy rows have no historical snapshot: their current source is explicitly
// marked Snapshot=false. The join repeats the tenant AND conversation boundary.
func LoadMessageReplies(ctx context.Context, tx pgx.Tx, messages []Message) error {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.ReplyToID != "" {
			ids = append(ids, message.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text,
		COALESCE(m.reply_snapshot, CASE WHEN source.id IS NOT NULL THEN jsonb_build_object(
			'message_id',source.id, 'seq',source.seq, 'author_kind',source.author_kind,
			'author_user_id',COALESCE(source.author_user_id::text,''),
			'author_agent_id',COALESCE(source.author_agent_id::text,''),
			'body',left(source.body,$2), 'created_at',source.created_at,
			'truncated',length(source.body)>$2, 'snapshot',false) END)
		FROM openfox.messages m
		LEFT JOIN openfox.messages source ON source.organization_id=m.organization_id
			AND source.conversation_id=m.conversation_id AND source.id=m.reply_to_id AND source.kind<>'REFERENCE'
		WHERE m.organization_id=openfox_private.current_organization_id() AND m.id=ANY($1::uuid[])`, ids, MaxReplyRunes)
	if err != nil {
		return err
	}
	defer rows.Close()
	replies := make(map[string]*MessageReply, len(ids))
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if len(raw) == 0 {
			continue
		}
		var reply MessageReply
		if err := json.Unmarshal(raw, &reply); err != nil {
			return err
		}
		replies[id] = &reply
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range messages {
		messages[i].Reply = replies[messages[i].ID]
	}
	return nil
}

// ReplyPrompt preserves the original message/input body and the existing wire
// protocol. Only the model's context receives the bounded quote, as quoted data.
func ReplyPrompt(body string, reply *MessageReply) string {
	if reply == nil {
		return body
	}
	quoted := "> " + strings.ReplaceAll(reply.Body, "\n", "\n> ")
	if reply.Truncated {
		quoted += "\n> … [quoted text truncated]"
	}
	return fmt.Sprintf("Quoted %s message #%d (reference text):\n%s\n\nReply:\n%s", reply.AuthorKind, reply.Seq, quoted, body)
}
