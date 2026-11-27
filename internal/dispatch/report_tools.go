package dispatch

// OPE-19 · 智能体正式报告评论：task_comment 带 artifact_ids 时，把本次 Run 的产物（HTML 报告、录像、证据清单、截图）
// 复制为任务附件，挂在本次 Run 唯一的一条正式报告评论下。
//
// 流程分三步，避免在数据库事务里做大文件复制：
//  1. PrepareReport（事务 1）：与 CallTool 相同的授权，校验 artifact_ids 都是本次 Run 的可用产物，返回尚未附到本轮报告评论的产物；
//  2. API 层在事务外把这些产物复制为独立的附件对象（attachments/<组织>/<附件 ID>），文件类型按附件白名单复核；
//  3. CallTool（事务 2）：锁任务行，发布报告评论或复用本轮已有的报告评论，只补登缺失的附件。
//     ToolResult.KeptFiles 列出最终被引用的附件 ID，其余复制品由 API 层在提交后删除。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/attachments"
	"openfox/internal/execution"
	"openfox/internal/membership"
)

// MaxReportArtifacts 是一条报告评论最多附带的产物数，与每次 Run 的产物上限（executiondelivery.MaxRunArtifacts）一致。
const MaxReportArtifacts = 20

// ReportArtifact 是待复制为附件的 Run 产物。
type ReportArtifact struct {
	ID         string
	Name       string
	StorageKey string
	SizeBytes  int64
}

// ReportArtifactIDs 解析 task_comment 的 artifact_ids：未提供返回 present=false；格式不对返回 problem（给 Agent 的原因）。
func ReportArtifactIDs(arguments json.RawMessage) (ids []string, present bool, problem string) {
	var args map[string]json.RawMessage
	if len(arguments) == 0 || string(arguments) == "null" || json.Unmarshal(arguments, &args) != nil {
		return nil, false, ""
	}
	return reportArtifactIDs(args)
}

func reportArtifactIDs(args map[string]json.RawMessage) ([]string, bool, string) {
	raw, ok := args["artifact_ids"]
	if !ok || string(raw) == "null" {
		return nil, false, ""
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || len(ids) == 0 || len(ids) > MaxReportArtifacts {
		return nil, true, fmt.Sprintf("artifact_ids must be a list of 1-%d artifact IDs returned by openfox_publish_artifact", MaxReportArtifacts)
	}
	seen := map[string]bool{}
	for i, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if !membership.ValidateUUID(id) || seen[id] {
			return nil, true, "artifact_ids must be distinct artifact IDs returned by openfox_publish_artifact"
		}
		seen[id] = true
		ids[i] = id
	}
	return ids, true, ""
}

// PrepareReport 在事务 1 里校验报告评论要附带的产物，返回需要复制的产物（已附到本轮报告评论的不再复制）。
// 业务拒绝返回 problem（ToolResult 文本），授权失败与 CallTool 相同返回错误。
func (s Store) PrepareReport(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, ids []string) ([]ReportArtifact, string, error) {
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, generation)
	if err != nil {
		return nil, "", err
	}
	if run.State != execution.StateRunning {
		return nil, "", ErrStateConflict
	}
	if run.TaskID == "" {
		return nil, "", ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.name, a.storage_key, a.size_bytes,
		       EXISTS (SELECT 1 FROM openfox.attachments x
		                 JOIN openfox.task_comments c ON c.organization_id = x.organization_id AND c.id = x.comment_id
		                WHERE x.organization_id = a.organization_id AND x.source_artifact_id = a.id
		                  AND c.run_id = a.run_id AND c.run_report)
		  FROM openfox.artifacts a
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.run_id = $1::uuid AND a.id = ANY($2::uuid[]) AND a.state = 'AVAILABLE'
		 ORDER BY array_position($2::uuid[], a.id)`, runID, ids)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	found := 0
	pending := []ReportArtifact{}
	for rows.Next() {
		var item ReportArtifact
		var attached bool
		if err := rows.Scan(&item.ID, &item.Name, &item.StorageKey, &item.SizeBytes, &attached); err != nil {
			return nil, "", err
		}
		found++
		if !attached {
			pending = append(pending, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if found != len(ids) {
		return nil, "artifact_ids must reference available artifacts published by this run", nil
	}
	return pending, "", nil
}

// toolReportComment 发布本次 Run 的正式报告评论（或复用已有的那条），并登记复制好的附件。
func toolReportComment(ctx context.Context, tx pgx.Tx, run execution.Run, callID, body string, args map[string]json.RawMessage, ids []string, files []attachments.ReportFile) (ToolResult, error) {
	var task string
	err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1 AND id = $2 AND deleted_at IS NULL
		FOR UPDATE`, run.SpaceID, run.TaskID).Scan(&task)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{Text: "task or comment is unavailable"}, nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	// 本轮已有报告评论：重试复用它，只补缺失的附件，正文不变。已删除的报告评论对读者不可见，
	// 再发布会撞上每轮唯一索引，insertAgentComment 会返回明确原因。
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_comments
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid AND run_report`, run.ID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ToolResult{}, err
	}
	sources := map[string]bool{}
	if existing != "" {
		if sources, err = attachments.ReportSources(ctx, tx, existing); err != nil {
			return ToolResult{}, err
		}
	}
	prepared := map[string]attachments.ReportFile{}
	for _, file := range files {
		prepared[file.ArtifactID] = file
	}
	insert := []attachments.ReportFile{}
	for _, id := range ids {
		if sources[id] {
			continue
		}
		file, ok := prepared[id]
		if !ok {
			// 两个事务之间状态变化（极少见）：让 Agent 重试同一调用即可。
			return ToolResult{Text: "the report attachments changed while posting; retry the same call"}, nil
		}
		insert = append(insert, file)
	}
	count, err := attachments.CountTask(ctx, tx, run.TaskID)
	if err != nil {
		return ToolResult{}, err
	}
	if count+len(insert) > attachments.MaxPerTask {
		return ToolResult{Text: fmt.Sprintf("the task can hold at most %d attachments (%d already); attach fewer artifacts", attachments.MaxPerTask, count)}, nil
	}
	commentID := existing
	if commentID == "" {
		result, id, err := insertAgentComment(ctx, tx, run, callID, body, args, true)
		if err != nil || !result.OK || id == "" {
			// id 为空：同一调用 ID 已发过一条普通评论，不再附带附件。
			return result, err
		}
		commentID = id
	}
	attached, unused, err := attachments.InsertReport(ctx, tx, run.SpaceID, run.TaskID, commentID, run.AgentID, insert)
	if err != nil {
		return ToolResult{}, err
	}
	kept := []string{}
	skip := map[string]bool{}
	for _, id := range unused {
		skip[id] = true
	}
	for _, file := range insert {
		if !skip[file.AttachmentID] {
			kept = append(kept, file.AttachmentID)
		}
	}
	if err := notifyTaskChanged(ctx, tx, run); err != nil {
		return ToolResult{}, err
	}
	text := fmt.Sprintf("report comment posted (comment_id=%s) with %d attachments", commentID, attached)
	if existing != "" {
		text = fmt.Sprintf("this run already has a report comment (comment_id=%s); attached %d missing artifacts, body unchanged", commentID, attached)
	}
	return ToolResult{OK: true, Text: text, KeptFiles: kept}, nil
}
