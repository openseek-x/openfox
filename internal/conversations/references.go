package conversations

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/docs"
	"openfox/internal/membership"
)

const TaskReferenceBody = "任务引用"
const DocReferenceBody = "文档引用"

type ReferenceInput struct {
	ID              string `json:"-"`
	Type            string `json:"type"`
	TaskID          string `json:"task_id"`
	DocID           string `json:"doc_id"`
	ClientMessageID string `json:"client_message_id"`
}

type referencePointer struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// MessageReference is derived for the current reader, never stored in messages.
// Unavailable references deliberately omit every source display field.
type MessageReference struct {
	Type      string `json:"type"`
	Available bool   `json:"available"`
	TaskID    string `json:"task_id,omitempty"`
	DocID     string `json:"doc_id,omitempty"`
	DocKind   string `json:"doc_kind,omitempty"`
	SpaceID   string `json:"space_id,omitempty"`
	DisplayID string `json:"display_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

// PostReference appends a passive message. It must not call PostMessage, whose
// assistant/crew behavior creates execution inputs, or enqueue any notification.
// The conversation lock serializes membership changes, seq and idempotency.
func (s Service) PostReference(ctx context.Context, tx pgx.Tx, org, conversationID string, actor Actor, input ReferenceInput) (Message, bool, error) {
	sourceID, body := input.TaskID, TaskReferenceBody
	switch input.Type {
	case "TASK":
		if input.DocID != "" {
			return Message{}, false, ErrInvalidRequest
		}
	case "DOC":
		if input.TaskID != "" {
			return Message{}, false, ErrInvalidRequest
		}
		sourceID, body = input.DocID, DocReferenceBody
	default:
		return Message{}, false, ErrInvalidRequest
	}
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if !membership.ValidateUUID(input.ID) || !membership.ValidateUUID(sourceID) || input.ClientMessageID == "" || len(input.ClientMessageID) > 256 {
		return Message{}, false, ErrInvalidRequest
	}
	conversation, err := s.loadVisible(ctx, tx, org, conversationID, actor, true, true)
	if err != nil {
		return Message{}, false, err
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Message{}, false, ErrConversationClosed
	}
	pointer := referencePointer{Type: input.Type, ID: sourceID}
	if existing, found, err := s.findMessageByClientID(ctx, tx, org, conversationID, input.ClientMessageID); err != nil {
		return Message{}, false, err
	} else if found {
		var previous referencePointer
		if existing.Kind != "REFERENCE" || existing.AuthorUserID != actor.UserID || json.Unmarshal(existing.Payload, &previous) != nil || previous != pointer {
			return Message{}, false, ErrDuplicateMessage
		}
		items := []Message{existing}
		if err := loadMessageReferences(ctx, tx, actor, items); err != nil {
			return Message{}, false, err
		}
		if err := loadMessageAttachments(ctx, tx, items); err != nil {
			return Message{}, false, err
		}
		return items[0], false, nil
	}
	payload, err := json.Marshal(pointer)
	if err != nil {
		return Message{}, false, err
	}
	// This same bounded ACL query is used for display; GetTask alone does not
	// enforce space visibility. No private title or body is copied into the row.
	items := []Message{{Kind: "REFERENCE", Payload: payload}}
	if err := loadMessageReferences(ctx, tx, actor, items); err != nil {
		return Message{}, false, err
	}
	if !items[0].Reference.Available {
		return Message{}, false, ErrNotFound
	}
	var seq int64
	if err := tx.QueryRow(ctx, `UPDATE openfox.conversations
		SET next_message_seq=next_message_seq+1, version=version+1, updated_at=clock_timestamp()
		WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
		RETURNING next_message_seq-1`, conversationID).Scan(&seq); err != nil {
		return Message{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.messages
		(organization_id,id,conversation_id,seq,author_kind,author_user_id,body,client_message_id,kind,payload)
		VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'USER',openfox_private.current_actor_id(),$4,$5,'REFERENCE',$6::jsonb)`,
		input.ID, conversationID, seq, body, input.ClientMessageID, payload)
	if err != nil {
		return Message{}, false, mapError(err)
	}
	message, err := s.loadMessage(ctx, tx, org, input.ID)
	message.Reference = items[0].Reference
	message.Payload = nil
	return message, true, err
}

// A page contains at most 100 messages. Resolve each supported source type once,
// always under the viewer's current ACL and without fetching source contents.
func loadMessageReferences(ctx context.Context, tx pgx.Tx, actor Actor, messages []Message) error {
	taskIDs, docIDs := []string{}, []string{}
	pointers := make(map[int]referencePointer)
	seen := make(map[referencePointer]bool)
	for i := range messages {
		if messages[i].Kind != "REFERENCE" {
			continue
		}
		payload := messages[i].Payload
		messages[i].Reference = unavailableReference(payload)
		messages[i].Payload = nil
		var pointer referencePointer
		if json.Unmarshal(payload, &pointer) != nil || !membership.ValidateUUID(pointer.ID) {
			continue
		}
		if pointer.Type != "TASK" && pointer.Type != "DOC" {
			continue
		}
		pointers[i] = pointer
		if !seen[pointer] {
			if pointer.Type == "TASK" {
				taskIDs = append(taskIDs, pointer.ID)
			} else {
				docIDs = append(docIDs, pointer.ID)
			}
			seen[pointer] = true
		}
	}
	role := strings.ToUpper(string(actor.Role))
	resolved, err := loadTaskReferences(ctx, tx, role, taskIDs)
	if err != nil {
		return err
	}
	documents, err := docs.LoadReadableReferences(ctx, tx, role, docIDs)
	if err != nil {
		return err
	}
	for id, item := range documents {
		resolved[referencePointer{Type: "DOC", ID: id}] = &MessageReference{Type: "DOC", Available: true, DocID: id, SpaceID: item.SpaceID, DocKind: item.Kind, Title: item.Title}
	}
	for i, pointer := range pointers {
		if item := resolved[pointer]; item != nil {
			messages[i].Reference = item
		}
	}
	return nil
}

func unavailableReference(payload json.RawMessage) *MessageReference {
	var pointer referencePointer
	if json.Unmarshal(payload, &pointer) == nil && pointer.Type == "DOC" {
		return &MessageReference{Type: "DOC"}
	}
	return &MessageReference{Type: "TASK"}
}

func loadTaskReferences(ctx context.Context, tx pgx.Tx, role string, ids []string) (map[referencePointer]*MessageReference, error) {
	resolved := make(map[referencePointer]*MessageReference, len(ids))
	if len(ids) == 0 {
		return resolved, nil
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.space_id::text,upper(s.prefix)||'-'||t.sequence::text,t.title
		FROM openfox.tasks t JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id
		WHERE t.organization_id=openfox_private.current_organization_id() AND t.deleted_at IS NULL
		AND t.id=ANY($2::uuid[]) AND `+collaboration.SpaceReadableSQL, role, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item := &MessageReference{Type: "TASK", Available: true}
		if err := rows.Scan(&item.TaskID, &item.SpaceID, &item.DisplayID, &item.Title); err != nil {
			return nil, err
		}
		resolved[referencePointer{Type: "TASK", ID: item.TaskID}] = item
	}
	return resolved, rows.Err()
}
