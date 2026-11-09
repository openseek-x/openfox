package dispatch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 对话消息附件随 Run 下发。RunContext 只带清单，设备按 ID 单独下载（见 AuthorizeRunAttachment）。

// MaxRunAttachments 是单次 Run 下发的附件数上限（多条合并输入各最多 10 个，整体再限界）。
const MaxRunAttachments = 30

// RunAttachment 是 DeviceRunAttachment：不含存储键与下载地址。
type RunAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// RunAttachmentFile 是设备下载附件所需的存储信息（只在服务端使用）。
type RunAttachmentFile struct {
	Key         string
	Name        string
	ContentType string
	SizeBytes   int64
}

// loadRunAttachments 返回本次 Run 合并输入对应消息上的附件，按消息顺序、上传顺序。
func loadRunAttachments(ctx context.Context, tx pgx.Tx, runID string) ([]RunAttachment, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.name, a.content_type, a.size_bytes
		  FROM openfox.conversation_inputs i
		  JOIN openfox.attachments a ON a.organization_id = i.organization_id AND a.message_id = i.message_id
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.run_id = $1::uuid
		 ORDER BY i.created_at, i.id, a.created_at, a.id
		 LIMIT $2`, runID, MaxRunAttachments)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RunAttachment{}
	for rows.Next() {
		var item RunAttachment
		if err := rows.Scan(&item.ID, &item.Name, &item.ContentType, &item.SizeBytes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AuthorizeRunAttachment 与 Context 授权相同（租约有效、授权未撤销），附件还必须属于该 Run 的输入消息。
func (s Store) AuthorizeRunAttachment(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, attachmentID string) (RunAttachmentFile, error) {
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, generation)
	if err != nil {
		return RunAttachmentFile{}, err
	}
	if !membership.ValidateUUID(attachmentID) {
		return RunAttachmentFile{}, ErrNotFound
	}
	var file RunAttachmentFile
	err = tx.QueryRow(ctx, `
		SELECT a.file_key, a.name, a.content_type, a.size_bytes
		  FROM openfox.conversation_inputs i
		  JOIN openfox.attachments a ON a.organization_id = i.organization_id AND a.message_id = i.message_id
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.run_id = $1::uuid AND a.id = $2::uuid`, run.ID, attachmentID).
		Scan(&file.Key, &file.Name, &file.ContentType, &file.SizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunAttachmentFile{}, ErrNotFound
	}
	return file, err
}
