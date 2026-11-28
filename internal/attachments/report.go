package attachments

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ReportFile 是从本次 Run 产物复制出的任务附件文件（OPE-19）：存储对象已按 StorageKey(组织, AttachmentID) 写入，
// 由智能体正式报告评论引用。附件对象独立于产物，产物过期清理不影响已发布的报告。
type ReportFile struct {
	ArtifactID   string
	AttachmentID string
	Name         string
	Info         FileInfo
}

// CountTask 返回任务当前的附件数（调用方已锁住任务行）。
func CountTask(ctx context.Context, tx pgx.Tx, taskID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.attachments
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID).Scan(&count)
	return count, err
}

// ReportSources 返回评论已附带的来源产物 ID，用于重试时只补缺失的附件。
func ReportSources(ctx context.Context, tx pgx.Tx, commentID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT source_artifact_id::text FROM openfox.attachments
		 WHERE organization_id = openfox_private.current_organization_id() AND comment_id = $1::uuid`, commentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		sources[id] = true
	}
	return sources, rows.Err()
}

// InsertReport 把复制好的文件登记为评论的任务附件（上传者为智能体）。调用方已锁住任务行并检查过每任务上限。
// 同一评论已有同一来源产物时跳过；返回实际登记的数量与未使用的附件 ID（调用方在提交后删除其存储对象）。
func InsertReport(ctx context.Context, tx pgx.Tx, spaceID, taskID, commentID, agentID string, files []ReportFile) (int, []string, error) {
	attached := 0
	unused := []string{}
	for _, file := range files {
		tag, err := tx.Exec(ctx, `
			INSERT INTO openfox.attachments (organization_id, id, owner_kind, space_id, task_id, name, file_key, content_type,
			       size_bytes, sha256, uploaded_by_agent_id, comment_id, source_artifact_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'TASK', $2::uuid, $3::uuid, $4, $5, $6, $7, $8,
			        $9::uuid, $10::uuid, $11::uuid)
			ON CONFLICT (organization_id, comment_id, source_artifact_id) WHERE comment_id IS NOT NULL DO NOTHING`,
			file.AttachmentID, spaceID, taskID, file.Name, file.Info.Key, file.Info.ContentType, file.Info.SizeBytes,
			file.Info.SHA256, agentID, commentID, file.ArtifactID)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() == 0 {
			unused = append(unused, file.AttachmentID)
			continue
		}
		attached++
	}
	return attached, unused, nil
}

// ListForComments 批量返回评论附带的附件（按评论分组，上传时间正序），供任务时间线一次查询附带。
func ListForComments(ctx context.Context, tx pgx.Tx, commentIDs []string) (map[string][]Attachment, error) {
	result := map[string][]Attachment{}
	if len(commentIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+fromClause+`
		WHERE a.organization_id = openfox_private.current_organization_id() AND a.comment_id = ANY($1::uuid[])
		ORDER BY a.created_at, a.id`, commentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result[a.CommentID] = append(result[a.CommentID], a)
	}
	return result, rows.Err()
}
