package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"openfox/internal/artifactreview"
	"openfox/internal/membership"
	"openfox/internal/office"
	"openfox/internal/runtime"
)

func officeFileTools() []runtime.Tool {
	return []runtime.Tool{
		{Name: toolPrefix + "file_comment_export", Description: "For requested Office formula/chart/layout changes, export the immutable original to a private local source file. Copy it to another path before editing. Requires normal write mode; does not expose file transport bytes to the model. Use file_comment_read first to understand source anchors. Return the new Office file with file_comment_replace, not a coordinate text patch.", InputSchema: json.RawMessage(`{"type":"object","properties":{"context_id":{"type":"string","format":"uuid"}},"required":["context_id"],"additionalProperties":false}`)},
		{Name: toolPrefix + "file_comment_replace", Description: "Deliver a locally edited DOCX/XLSX/PPTX as a review candidate for this file-feedback context. Use only for explicitly requested broader Office changes. path must be inside this Run's workspace or artifact directory; sha256 is the ORIGINAL file hash. Preserve unrelated content and verify the new file. The server rechecks source SHA, permissions and OOXML safety and saves a candidate without changing the original; only the user can accept it; same content/summary retry is idempotent. Never use for questions or claim success before this tool succeeds.", InputSchema: json.RawMessage(`{"type":"object","properties":{"context_id":{"type":"string","format":"uuid"},"path":{"type":"string"},"sha256":{"type":"string"},"summary":{"type":"string","maxLength":2000}},"required":["context_id","path","sha256","summary"],"additionalProperties":false}`)},
	}
}

// The model-facing schema uses a local path. Only this trusted boundary turns
// that path into bounded file bytes, or a center response into a private file.
func withOfficeFiles(base runtime.ToolCall, rc RunContext, workspace, dir string) runtime.ToolCall {
	if !rc.FileComments || dir == "" {
		return base
	}
	var mu sync.Mutex
	roots := resolveRoots([]string{workspace, dir})
	return func(ctx context.Context, name, callID string, args json.RawMessage) runtime.ToolResult {
		if name != toolPrefix+"file_comment_export" && name != toolPrefix+"file_comment_replace" {
			return base(ctx, name, callID, args)
		}
		if rc.ApprovalMode == "READ_ONLY" || rc.CollaborationMode == "PLAN" {
			return runtime.TextResult("Office file changes require normal write permissions.", false)
		}
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return runtime.TextResult("Office file operation cancelled.", false)
		}
		var input struct {
			ContextID string `json:"context_id"`
			Path      string `json:"path,omitempty"`
			SHA256    string `json:"sha256,omitempty"`
			Summary   string `json:"summary,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.DisallowUnknownFields()
		if len(args) > artifactreview.MaxToolBytes || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !membership.ValidateUUID(input.ContextID) {
			return runtime.TextResult("Invalid Office file arguments.", false)
		}
		if name == toolPrefix+"file_comment_replace" {
			file, err := inspectArtifact(input.Path, roots, office.MaxBytes)
			if err != nil || !office.MIME(file.MIME) {
				return runtime.TextResult("Choose a valid Office file inside this Run's workspace or artifact directory.", false)
			}
			wire, _ := json.Marshal(struct {
				ContextID string `json:"context_id"`
				artifactreview.OfficeReplacement
			}{input.ContextID, artifactreview.OfficeReplacement{SHA256: input.SHA256, Summary: input.Summary, Content: file.Content}})
			return base(ctx, name, callID, wire)
		}
		if input.Path != "" || input.SHA256 != "" || input.Summary != "" {
			return runtime.TextResult("Invalid export arguments.", false)
		}
		result := base(ctx, name, callID, args)
		if !result.OK {
			return result
		}
		var source struct {
			SHA256  string `json:"sha256"`
			MIME    string `json:"mime_type"`
			Content []byte `json:"content_base64"`
		}
		if len(result.Text) > artifactreview.MaxOfficeTransferBytes || json.Unmarshal([]byte(result.Text), &source) != nil || len(source.Content) > office.MaxBytes || !office.MIME(source.MIME) || artifactreview.Digest(string(source.Content)) != source.SHA256 {
			return runtime.TextResult("Original Office file is unavailable or already replaced; open the updated artifact.", false)
		}
		if _, err := office.Read(source.Content, source.MIME); err != nil {
			return runtime.TextResult("Original Office file is not supported.", false)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return runtime.TextResult("Office export directory is unavailable.", false)
		}
		defer root.Close()
		folder := filepath.Join("file-feedback", input.ContextID)
		if root.MkdirAll(folder, 0700) != nil {
			return runtime.TextResult("Office export directory is unavailable.", false)
		}
		ext := map[string]string{office.DOCX: "docx", office.XLSX: "xlsx", office.PPTX: "pptx"}[source.MIME]
		destination := filepath.Join(folder, "source."+ext)
		out, err := root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if os.IsExist(err) {
			existing, e := root.Open(destination)
			if e != nil {
				return runtime.TextResult("Existing source cannot be verified.", false)
			}
			raw, e := io.ReadAll(io.LimitReader(existing, office.MaxBytes+1))
			_ = existing.Close()
			if e != nil || artifactreview.Digest(string(raw)) != source.SHA256 {
				return runtime.TextResult("Local source changed; it will not be overwritten.", false)
			}
		} else if err != nil {
			return runtime.TextResult("Office export failed.", false)
		} else {
			_, writeErr := out.Write(source.Content)
			closeErr := out.Close()
			if writeErr != nil || closeErr != nil {
				_ = root.Remove(destination)
				return runtime.TextResult("Office export failed.", false)
			}
		}
		metadata, _ := json.Marshal(map[string]any{"path": filepath.Join(dir, destination), "sha256": source.SHA256, "mime_type": source.MIME, "note": "Immutable original copy. Edit a separate copy, preserve unrelated content, then deliver it with file_comment_replace."})
		return runtime.TextResult(string(metadata), true)
	}
}
