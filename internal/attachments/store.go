package attachments

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

const columns = `a.id::text, a.owner_kind, a.space_id::text, COALESCE(a.task_id::text, ''), COALESCE(a.conversation_id::text, ''),
	COALESCE(a.message_id::text, ''), a.name, a.file_key, a.content_type, a.size_bytes, a.uploaded_by::text,
	COALESCE(NULLIF(u.full_name, ''), u.email, ''), a.created_at`

const fromClause = `
	FROM openfox.attachments a
	LEFT JOIN openfox.users u ON u.id = a.uploaded_by`

func scan(row pgx.Row) (Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.OwnerKind, &a.SpaceID, &a.TaskID, &a.ConversationID, &a.MessageID, &a.Name, &a.FileKey,
		&a.ContentType, &a.SizeBytes, &a.UploadedBy, &a.UploaderName, &a.CreatedAt)
	a.Previewable = Previewable(a.ContentType)
	return a, err
}

// Load 读取一个附件（租户内）；不存在返回 ErrNotFound。
func Load(ctx context.Context, tx pgx.Tx, id string) (Attachment, error) {
	if !membership.ValidateUUID(id) {
		return Attachment{}, ErrNotFound
	}
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+fromClause+`
		WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	return a, err
}

// InsertTask 写入任务附件。先锁任务行再计数，保证并发上传不会突破每任务 100 个的上限。
func InsertTask(ctx context.Context, tx pgx.Tx, spaceID, taskID, actorID, id, name string, info FileInfo) (Attachment, error) {
	var locked string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL FOR UPDATE`, spaceID, taskID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Attachment{}, ErrNotFound
		}
		return Attachment{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.attachments
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID).Scan(&count); err != nil {
		return Attachment{}, err
	}
	if count >= MaxPerTask {
		return Attachment{}, ErrTaskLimit
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.attachments (organization_id, id, owner_kind, space_id, task_id, name, file_key, content_type, size_bytes, sha256, uploaded_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'TASK', $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9::uuid)`,
		id, spaceID, taskID, name, info.Key, info.ContentType, info.SizeBytes, info.SHA256, actorID); err != nil {
		return Attachment{}, err
	}
	return Load(ctx, tx, id)
}

// ListTask 按上传时间正序返回任务附件（上限 MaxPerTask）。
func ListTask(ctx context.Context, tx pgx.Tx, taskID string) ([]Attachment, error) {
	return list(ctx, tx, `a.task_id = $1::uuid`, taskID)
}

// ListConversation 返回会话里已随消息发送的附件，供消息列表附带；pending 的只给上传人自己。
func ListForMessages(ctx context.Context, tx pgx.Tx, messageIDs []string) (map[string][]Attachment, error) {
	result := map[string][]Attachment{}
	if len(messageIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+fromClause+`
		WHERE a.organization_id = openfox_private.current_organization_id() AND a.message_id = ANY($1::uuid[])
		ORDER BY a.created_at, a.id`, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result[a.MessageID] = append(result[a.MessageID], a)
	}
	return result, rows.Err()
}

func list(ctx context.Context, tx pgx.Tx, where string, arg any) ([]Attachment, error) {
	rows, err := tx.Query(ctx, `SELECT `+columns+fromClause+`
		WHERE a.organization_id = openfox_private.current_organization_id() AND `+where+`
		ORDER BY a.created_at, a.id LIMIT 200`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Attachment{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

// InsertConversation 写入尚未绑定消息的对话附件。同一人在同一会话最多 MaxPendingPerUser 个待发送附件。
func InsertConversation(ctx context.Context, tx pgx.Tx, spaceID, conversationID, actorID, id, name string, info FileInfo) (Attachment, error) {
	// 会话行加锁串行化同一会话的上传计数。
	if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.conversations
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, conversationID); err != nil {
		return Attachment{}, err
	}
	var pending int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.attachments
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND message_id IS NULL AND uploaded_by = $2::uuid`, conversationID, actorID).Scan(&pending); err != nil {
		return Attachment{}, err
	}
	if pending >= MaxPendingPerUser {
		return Attachment{}, ErrPendingLimit
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.attachments (organization_id, id, owner_kind, space_id, conversation_id, name, file_key, content_type, size_bytes, sha256, uploaded_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'CONVERSATION', $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9::uuid)`,
		id, spaceID, conversationID, name, info.Key, info.ContentType, info.SizeBytes, info.SHA256, actorID); err != nil {
		return Attachment{}, err
	}
	return Load(ctx, tx, id)
}

// Bind 把上传人自己、同一会话、尚未绑定的附件绑定到刚创建的消息。数量不符（不存在、他人上传、已绑定、跨会话）一律 ErrBadMessage。
func Bind(ctx context.Context, tx pgx.Tx, conversationID, messageID, actorID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > MaxPerMessage {
		return ErrBadMessage
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !membership.ValidateUUID(id) || seen[id] {
			return ErrBadMessage
		}
		seen[id] = true
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.attachments SET message_id = $1::uuid
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($2::uuid[]) AND owner_kind = 'CONVERSATION'
		   AND conversation_id = $3::uuid AND uploaded_by = $4::uuid AND message_id IS NULL`,
		messageID, ids, conversationID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return ErrBadMessage
	}
	return nil
}

// Delete 删除附件行并返回存储键；调用方在事务提交后删除存储对象。
func Delete(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var key string
	err := tx.QueryRow(ctx, `
		DELETE FROM openfox.attachments
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING file_key`, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return key, err
}
