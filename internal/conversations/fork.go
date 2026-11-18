package conversations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/membership"
)

const (
	MaxForkMessages     = 500
	MaxForkContentBytes = 200 * 1024
)

var (
	ErrForkContextTooLarge = errors.New("conversations: fork context too large")
	ErrForkHistoryReadOnly = errors.New("conversations: fork history is read-only")
)

type ForkInput struct {
	SourceMessageID    string              `json:"source_message_id"`
	AgentID            string              `json:"agent_id"`
	WorkspaceSelection *WorkspaceSelection `json:"workspace_selection"`
}

// forkMessage is exactly the content persisted by the bounded bulk INSERT.
// Budget its JSON encoding, including metadata, escaping and quoted snapshots.
type forkMessage struct {
	BrowserContexts []BrowserContext `json:"browser_contexts,omitempty"`
	ID              string           `json:"id"`
	Seq             int64            `json:"seq"`
	AuthorKind      string           `json:"author_kind"`
	AuthorUserID    string           `json:"author_user_id,omitempty"`
	AuthorAgentID   string           `json:"author_agent_id,omitempty"`
	Kind            string           `json:"kind"`
	Body            string           `json:"body"`
	Payload         json.RawMessage  `json:"payload,omitempty"`
	ReplyToID       string           `json:"reply_to_id,omitempty"`
	Reply           *MessageReply    `json:"reply_snapshot,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
}

// Fork never posts a message or creates runtime input. The caller owns the
// tenant transaction and idempotency claim. Source locking matches PostMessage,
// membership changes and proposal resolution.
func (s Service) Fork(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input ForkInput) (Conversation, error) {
	if !membership.ValidateUUID(input.SourceMessageID) || !membership.ValidateUUID(input.AgentID) {
		return Conversation{}, ErrInvalidRequest
	}
	source, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return Conversation{}, err
	}
	if source.Lifecycle != LifecycleOpen {
		return Conversation{}, ErrConversationClosed
	}
	var boundary int64
	var archived *time.Time
	err = tx.QueryRow(ctx, `SELECT m.seq,c.archived_at FROM openfox.messages m JOIN openfox.conversations c ON c.organization_id=m.organization_id AND c.id=m.conversation_id
 WHERE m.organization_id=openfox_private.current_organization_id() AND m.conversation_id=$1::uuid
 AND m.id=$2::uuid AND kind='TEXT' AND author_kind IN ('USER','AGENT')`, source.ID, input.SourceMessageID).Scan(&boundary, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if archived != nil {
		return Conversation{}, ErrConversationClosed
	}
	if boundary <= 0 || boundary == math.MaxInt64 {
		return Conversation{}, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `SELECT id::text,seq,author_kind,COALESCE(author_user_id::text,''),
 COALESCE(author_agent_id::text,''),kind,body,payload,COALESCE(reply_to_id::text,''),reply_snapshot,created_at
 FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id()
 AND conversation_id=$1::uuid AND seq<=$2 ORDER BY seq,id LIMIT $3`, source.ID, boundary, MaxForkMessages+1)
	if err != nil {
		return Conversation{}, err
	}
	items := make([]forkMessage, 0)
	contentFloor := 0
	for rows.Next() {
		var item forkMessage
		var reply []byte
		if err := rows.Scan(&item.ID, &item.Seq, &item.AuthorKind, &item.AuthorUserID, &item.AuthorAgentID, &item.Kind, &item.Body, &item.Payload, &item.ReplyToID, &reply, &item.CreatedAt); err != nil {
			rows.Close()
			return Conversation{}, err
		}
		if len(reply) > 0 {
			if err := json.Unmarshal(reply, &item.Reply); err != nil {
				rows.Close()
				return Conversation{}, err
			}
		}
		// A safe lower bound: copied body plus compact payload, before JSON
		// escaping/metadata/quotes. Reject without accumulating an oversized prefix.
		var compact bytes.Buffer
		if len(item.Payload) > 0 {
			if err := json.Compact(&compact, item.Payload); err != nil {
				rows.Close()
				return Conversation{}, err
			}
		}
		contentFloor += len(item.Body) + compact.Len()
		if contentFloor > MaxForkContentBytes {
			rows.Close()
			return Conversation{}, ErrForkContextTooLarge
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Conversation{}, err
	}
	// Retain immutable selected text in fork history, within the existing fork
	// budget. Attachments are not copied by Fork; never reference a source
	// conversation's screenshot attachment from a new conversation.
	for offset := 0; offset < len(items); offset += MaxPageSize {
		end := min(offset+MaxPageSize, len(items))
		batch := make([]Message, end-offset)
		for i := range batch {
			batch[i].ID = items[offset+i].ID
		}
		if err := LoadBrowserContexts(ctx, tx, batch); err != nil {
			return Conversation{}, err
		}
		for i, m := range batch {
			for ci := range m.BrowserContexts {
				for ai := range m.BrowserContexts[ci].Comments {
					m.BrowserContexts[ci].Comments[ai].ScreenshotAttachmentID = ""
				}
			}
			items[offset+i].BrowserContexts = m.BrowserContexts
		}
	}
	encoded, err := prepareForkMessages(items)
	if err != nil {
		return Conversation{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return Conversation{}, err
	}
	_, err = s.Create(ctx, tx, organizationID, actor, CreateInput{ID: id, SpaceID: source.SpaceID, AgentID: input.AgentID, TaskID: source.TaskID, Visibility: VisibilityPrivate, WorkspaceSelection: input.WorkspaceSelection})
	if err != nil {
		return Conversation{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.conversations SET parent_conversation_id=$2::uuid,
 fork_message_id=$3::uuid,fork_prefix_seq=$4::bigint,next_message_seq=$4::bigint+1,title='分支会话'
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, source.ID, input.SourceMessageID, boundary)
	if err != nil {
		return Conversation{}, err
	}
	if tag.RowsAffected() != 1 {
		return Conversation{}, ErrConflict
	}
	// Single statement also satisfies references to later rows within this prefix.
	tag, err = tx.Exec(ctx, `INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,
 author_user_id,author_agent_id,kind,body,payload,reply_to_id,reply_snapshot,created_at,authenticated_user_input)
 SELECT openfox_private.current_organization_id(),x.id,$1::uuid,x.seq,x.author_kind,
 NULLIF(x.author_user_id,'')::uuid,NULLIF(x.author_agent_id,'')::uuid,x.kind,x.body,x.payload,
 NULLIF(x.reply_to_id,'')::uuid,x.reply_snapshot,x.created_at,false
 FROM jsonb_to_recordset($2::jsonb) AS x(id uuid,seq bigint,author_kind text,author_user_id text,
 author_agent_id text,kind text,body text,payload jsonb,reply_to_id text,reply_snapshot jsonb,created_at timestamptz)
 ORDER BY x.seq`, id, encoded)
	if err != nil {
		return Conversation{}, mapError(err)
	}
	if tag.RowsAffected() != int64(len(items)) {
		return Conversation{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.message_browser_contexts(organization_id,message_id,input_snapshot,payload_hash)
 SELECT openfox_private.current_organization_id(),x.id,x.browser_contexts,encode(sha256(convert_to(x.browser_contexts::text,'UTF8')),'hex')
 FROM jsonb_to_recordset($1::jsonb) AS x(id uuid,browser_contexts jsonb)
 WHERE jsonb_array_length(x.browser_contexts)>0`, encoded); err != nil {
		return Conversation{}, err
	}
	return s.getVisible(ctx, tx, organizationID, id, actor, false)
}

func prepareForkMessages(items []forkMessage) ([]byte, error) {
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	if len(items) > MaxForkMessages {
		return nil, ErrForkContextTooLarge
	}
	originals := make(map[string]forkMessage, len(items))
	ids := make(map[string]string, len(items))
	for _, item := range items {
		originals[item.ID] = item
		id, err := membership.NewID()
		if err != nil {
			return nil, err
		}
		ids[item.ID] = id
	}
	for i := range items {
		item := &items[i]
		item.ID = ids[item.ID]
		quoted, ok := originals[item.ReplyToID]
		if !ok || quoted.Kind == "REFERENCE" {
			item.ReplyToID = ""
			item.Reply = nil
			continue
		}
		item.ReplyToID = ids[quoted.ID]
		if item.Reply == nil {
			body := []rune(quoted.Body)
			truncated := len(body) > MaxReplyRunes
			if truncated {
				body = body[:MaxReplyRunes]
			}
			item.Reply = &MessageReply{Seq: quoted.Seq, AuthorKind: quoted.AuthorKind, AuthorUserID: quoted.AuthorUserID,
				AuthorAgentID: quoted.AuthorAgentID, Body: string(body), CreatedAt: quoted.CreatedAt, Truncated: truncated}
		} else {
			copied := *item.Reply
			item.Reply = &copied
		}
		item.Reply.MessageID = item.ReplyToID
		item.Reply.Snapshot = true
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxForkContentBytes {
		return nil, ErrForkContextTooLarge
	}
	return encoded, nil
}

// NormalizeForkInput makes UUID spelling irrelevant to the idempotency hash.
func NormalizeForkInput(input ForkInput) ForkInput {
	input.SourceMessageID = strings.ToLower(strings.TrimSpace(input.SourceMessageID))
	input.AgentID = strings.ToLower(strings.TrimSpace(input.AgentID))
	if input.WorkspaceSelection != nil {
		copy := *input.WorkspaceSelection
		copy.SourceConversationID = strings.ToLower(strings.TrimSpace(copy.SourceConversationID))
		input.WorkspaceSelection = &copy
	}
	return input
}
