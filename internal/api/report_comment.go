package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

// callReportComment 处理带 artifact_ids 的 task_comment（OPE-19 正式报告评论）：
// 事务 1 校验产物 → 事务外把产物复制为独立的附件对象 → 事务 2 发布或复用本轮报告评论。
// 未被引用的复制品（失败、拒绝、重试时已存在）在结束后删除，不留孤立对象。
func (h *Handler) callReportComment(w http.ResponseWriter, r *http.Request, principal deviceauth.Principal, selection tenancy.OrganizationSelection, input dispatch.ToolCall, ids []string) {
	if h.delivery.Storage == nil {
		writeJSON(w, http.StatusOK, dispatch.ToolResult{Text: "file storage is not configured; post the report without attachments"})
		return
	}
	runID := r.PathValue("runID")
	var pending []dispatch.ReportArtifact
	var problem string
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		pending, problem, err = h.dispatch.PrepareReport(ctx, tx, selection.OrganizationID, principal.DeviceID, runID, input.Generation, ids)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	if problem != "" {
		writeJSON(w, http.StatusOK, dispatch.ToolResult{Text: problem})
		return
	}
	var total int64
	for _, item := range pending {
		total += item.SizeBytes
	}
	setFileTransferDeadline(w, 2*total, false)
	files, problem, err := h.copyReportArtifacts(r.Context(), selection.OrganizationID, pending)
	copied := make([]string, 0, len(files))
	for _, file := range files {
		copied = append(copied, file.Info.Key)
	}
	if err != nil || problem != "" {
		removeStorageObjects(r.Context(), h.delivery.Storage, copied...)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		writeJSON(w, http.StatusOK, dispatch.ToolResult{Text: problem})
		return
	}
	input.ReportFiles = files
	var result dispatch.ToolResult
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.dispatch.CallTool(ctx, tx, selection.OrganizationID, principal.DeviceID, runID, "task_comment", input)
		return err
	})
	kept := map[string]bool{}
	if err == nil {
		for _, id := range result.KeptFiles {
			kept[id] = true
		}
	}
	var discard []string
	for _, file := range files {
		if !kept[file.AttachmentID] {
			discard = append(discard, file.Info.Key)
		}
	}
	removeStorageObjects(r.Context(), h.delivery.Storage, discard...)
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// copyReportArtifacts 把产物逐个复制为附件对象。文件类型按附件白名单与文件头复核（HTML 等以纯文本落库），
// 不允许作为附件的产物整体拒绝，避免报告评论悄悄缺少证据。返回已写入的文件（出错时调用方负责删除）。
func (h *Handler) copyReportArtifacts(ctx context.Context, organizationID string, pending []dispatch.ReportArtifact) ([]attachments.ReportFile, string, error) {
	files := make([]attachments.ReportFile, 0, len(pending))
	for _, item := range pending {
		file, problem, err := h.copyReportArtifact(ctx, organizationID, item)
		if err != nil || problem != "" {
			return files, problem, err
		}
		files = append(files, file)
	}
	return files, "", nil
}

func (h *Handler) copyReportArtifact(ctx context.Context, organizationID string, item dispatch.ReportArtifact) (attachments.ReportFile, string, error) {
	source, err := h.delivery.Storage.Open(ctx, item.StorageKey)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return attachments.ReportFile{}, "", err
		}
		return attachments.ReportFile{}, fmt.Sprintf("artifact %q is no longer available; publish it again", item.Name), nil
	}
	defer source.Close()
	head := make([]byte, attachments.HeadBytes)
	n, err := io.ReadFull(source, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return attachments.ReportFile{}, "", err
	}
	head = head[:n]
	contentType, err := attachments.Classify(item.Name, head)
	if err != nil {
		return attachments.ReportFile{}, fmt.Sprintf("artifact %q has a file type that cannot be attached to a comment; leave it out of artifact_ids", item.Name), nil
	}
	id, err := collaboration.NewID()
	if err != nil {
		return attachments.ReportFile{}, "", err
	}
	key := attachments.StorageKey(organizationID, id)
	stored, err := h.delivery.Storage.Put(ctx, key, io.MultiReader(bytes.NewReader(head), source), attachments.MaxBytes)
	file := attachments.ReportFile{ArtifactID: item.ID, AttachmentID: id, Name: item.Name,
		Info: attachments.FileInfo{Key: key, ContentType: contentType, SizeBytes: stored.Size, SHA256: stored.SHA256}}
	if err != nil {
		// Put 失败时对象可能已部分写入：仍返回 file 让调用方按键清理。
		return file, "", err
	}
	return file, "", nil
}
