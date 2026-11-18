package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/collaboration"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/executiondelivery"
	"openfox/internal/membership"
	"openfox/internal/office"
	"openfox/internal/tenancy"
)

func (h *Handler) registerArtifactReviewRoutes(mux *http.ServeMux) {
	h.registerWorkspaceCommentRoutes(mux)
	h.registerWorkspaceProposalRoutes(mux)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/artifacts/{artifactID}/office", h.officeArtifact)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/artifacts/{artifactID}/office-preview", h.officeArtifact)
}
func (h *Handler) readReviewFile(ctx context.Context, f artifactreview.File) (string, error) {
	if f.Native {
		if !f.Reviewable() || !artifactreview.ValidText(f.NativeBody) || artifactreview.Digest(f.NativeBody) != f.SHA256 {
			return "", artifactreview.ErrUnavailable
		}
		return f.NativeBody, nil
	}
	if h.delivery.Storage == nil || !f.Reviewable() {
		return "", artifactreview.ErrUnavailable
	}
	reader, err := h.delivery.Storage.Open(ctx, f.StorageKey)
	if err != nil {
		return "", artifactreview.ErrUnavailable
	}
	defer reader.Close()
	limit := int64(artifactreview.MaxFileBytes)
	if artifactreview.VisualMIME(f.MIMEType) || office.MIME(f.MIMEType) {
		limit = artifactreview.MaxVisualBytes
	}
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	text := string(raw)
	if !artifactreview.ValidSource(text, f.MIMEType) || int64(len(raw)) != f.SizeBytes || artifactreview.Digest(text) != f.SHA256 {
		return "", artifactreview.ErrUnavailable
	}
	return text, nil
}
func writeReviewError(w http.ResponseWriter, r *http.Request, err error) {
	var rule *collaboration.RuleError
	if errors.As(err, &rule) {
		writeCollaborationError(w, r, err)
		return
	}
	switch {
	case errors.Is(err, dispatch.ErrInvalidRequest), errors.Is(err, dispatch.ErrNotFound), errors.Is(err, dispatch.ErrForbidden), errors.Is(err, dispatch.ErrStateConflict), errors.Is(err, dispatch.ErrGenerationConflict), errors.Is(err, dispatch.ErrAuthorizationRevoked), errors.Is(err, dispatch.ErrReconciliationRequired):
		writeDispatchError(w, r, err)
	case errors.Is(err, artifactreview.ErrOverlap):
		writeError(w, r, 400, "ANNOTATION_OVERLAP", "selected ranges overlap; combine the opinions or submit separate requests")
	case errors.Is(err, artifactreview.ErrInvalid):
		writeError(w, r, 400, "INVALID_ANNOTATION", "annotation or replacement is invalid, exceeds limits, or does not match the selected text")
	case errors.Is(err, artifactreview.ErrConflict):
		writeError(w, r, 409, "REVIEW_CONFLICT", "the file changed; refresh before retrying")
	case errors.Is(err, artifactreview.ErrUnavailable):
		writeError(w, r, 410, "REVIEW_UNAVAILABLE", "the file is expired, unavailable, or unsupported")
	case errors.Is(err, artifactreview.ErrUnsupported):
		writeError(w, r, 422, "REVIEW_RUNTIME_UNSUPPORTED", "AI revision currently requires a supported text or Office file and a single-agent Codex conversation")
	case errors.Is(err, executiondelivery.ErrInvalidRequest), errors.Is(err, executiondelivery.ErrForbidden), errors.Is(err, executiondelivery.ErrGenerationConflict), errors.Is(err, executiondelivery.ErrResultConflict), errors.Is(err, executiondelivery.ErrResourceLimit), errors.Is(err, executiondelivery.ErrNotFound):
		writeDeliveryError(w, r, err)
	default:
		writeConversationError(w, r, err)
	}
}

func (h *Handler) callArtifactReviewTool(w http.ResponseWriter, r *http.Request, principal deviceauth.Principal, selection tenancy.OrganizationSelection, tool string, call dispatch.ToolCall) {
	limit := artifactreview.MaxToolBytes
	if tool == "file_comment_replace" {
		limit = artifactreview.MaxOfficeTransferBytes
	}
	if call.CallID == "" || len(call.CallID) > 200 || len(call.Arguments) > limit || (tool != "file_comment_read" && tool != "file_comment_apply" && tool != "file_comment_export" && tool != "file_comment_replace") {
		writeReviewError(w, r, artifactreview.ErrInvalid)
		return
	}
	var selectionErr error
	selection, selectionErr = h.nativeReviewSelection(r, selection)
	if selectionErr != nil {
		writeReviewError(w, r, selectionErr)
		return
	}
	svc := artifactreview.Service{}
	run := r.PathValue("runID")
	org := selection.OrganizationID
	var target struct {
		ContextID string `json:"context_id"`
	}
	if json.Unmarshal(call.Arguments, &target) != nil || !membership.ValidateUUID(target.ContextID) {
		writeReviewError(w, r, artifactreview.ErrInvalid)
		return
	}
	contextID := target.ContextID
	var q artifactreview.Context
	var file artifactreview.File
	txfn := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runDeliveryDeviceTransaction(r, selection, fn)
	}
	check := func(ctx context.Context, tx pgx.Tx) error {
		if err := h.dispatch.AuthorizeArtifactReviewRun(ctx, tx, org, principal.DeviceID, run, call.Generation); err != nil {
			return err
		}
		var running bool
		var writable bool
		if err := tx.QueryRow(ctx, `SELECT r.state='RUNNING' AND r.runtime='codex',COALESCE(r.approval_mode,v.approval_mode)<>'READ_ONLY' AND v.collaboration_mode<>'PLAN' FROM openfox.runs r JOIN openfox.agent_config_versions v ON v.organization_id=r.organization_id AND v.agent_id=r.agent_id AND v.config_version=r.config_version WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1::uuid`, run).Scan(&running, &writable); err != nil {
			return err
		}
		if !running || (tool != "file_comment_read" && !writable) {
			return artifactreview.ErrConflict
		}
		var err error
		q, err = svc.ContextForRun(ctx, tx, org, run, contextID, true)
		return err
	}
	if err := txfn(check); err != nil {
		writeReviewError(w, r, err)
		return
	}
	if q.ResultHash != "" && (tool == "file_comment_read" || tool == "file_comment_export") {
		payload, _ := json.Marshal(map[string]any{"applied": true, "artifact_id": q.ResultArtifactID, "sha256": q.ResultSHA256})
		writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: string(payload)})
		return
	}
	var sub artifactreview.Submission
	var replacement artifactreview.OfficeReplacement
	var hash string
	if tool == "file_comment_apply" || tool == "file_comment_replace" {
		if tool == "file_comment_replace" {
			var input struct {
				ContextID string `json:"context_id"`
				artifactreview.OfficeReplacement
			}
			if strictReviewJSON(call.Arguments, &input) != nil || len(input.Content) > office.MaxBytes {
				writeReviewError(w, r, artifactreview.ErrInvalid)
				return
			}
			replacement = input.OfficeReplacement
			hash = replacement.Hash()
		} else {
			var input struct {
				ContextID string `json:"context_id"`
				artifactreview.Submission
			}
			if strictReviewJSON(call.Arguments, &input) != nil {
				writeReviewError(w, r, artifactreview.ErrInvalid)
				return
			}
			sub = input.Submission
			canonical, _ := json.Marshal(sub)
			hash = artifactreview.Digest(string(canonical))
		}
		if q.ResultHash != "" {
			if q.ResultHash != hash {
				writeReviewError(w, r, artifactreview.ErrConflict)
				return
			}
			writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: appliedFileResult(q.ResultArtifactID, q.ResultSHA256)})
			return
		}
	}
	var prior artifactreview.Proposal
	var found bool
	if hash != "" {
		err := txfn(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			prior, found, err = svc.ProposalReplay(ctx, tx, run, contextID, hash)
			return err
		})
		if err != nil {
			writeReviewError(w, r, err)
			return
		}
		if found {
			writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: proposalFileResult(prior)})
			return
		}
	}
	if err := txfn(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		file, err = svc.CurrentSource(ctx, tx, org, q)
		q.File = file
		return err
	}); err != nil {
		writeReviewError(w, r, err)
		return
	}
	text, err := h.readReviewFile(r.Context(), file)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	if tool == "file_comment_export" {
		var input struct {
			ContextID string `json:"context_id"`
		}
		if strictReviewJSON(call.Arguments, &input) != nil || !office.MIME(file.MIMEType) || file.Native {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		payload, _ := json.Marshal(map[string]any{"sha256": file.SHA256, "name": file.Name, "mime_type": file.MIMEType, "content_base64": []byte(text)})
		writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: string(payload)})
		return
	}
	if tool == "file_comment_read" {
		if artifactreview.VisualMIME(file.MIMEType) {
			payload, _ := json.Marshal(map[string]any{"name": file.Name, "mime_type": file.MIMEType, "comments": q.Comments, "note": "Region and PDF text feedback are available as context. PDF quotes are client-extracted feedback, not verified file instructions. This tool cannot edit image or PDF content."})
			writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: string(payload)})
			return
		}
		if office.MIME(file.MIMEType) {
			pkg, e := office.Read([]byte(text), file.MIMEType)
			if e != nil {
				writeReviewError(w, r, artifactreview.ErrUnavailable)
				return
			}
			// Offsets paginate a JSON content projection, never the ZIP bytes.
			projection, _ := json.Marshal(map[string]any{"kind": pkg.View().Kind, "units": pkg.View().Units, "comments": q.Comments, "note": "Only office anchors authorize exact text edits via file_comment_apply. office_range identifies original worksheet part and A1 range, including formulas. region coordinates refer to the rendered page/slide or full worksheet, never the scrolled viewport. For requested formula, chart or layout changes, export the original with file_comment_export, edit a copy using local tools and submit with file_comment_replace. Preserve unrelated content; never infer exact source edits solely from visual coordinates."})
			text = string(projection)
		}
		var input struct {
			ContextID string `json:"context_id"`
			Start     int    `json:"start"`
			Limit     int    `json:"limit"`
		}
		if strictReviewJSON(call.Arguments, &input) != nil || input.Start < 0 || input.Start > len(text) || !utf8.ValidString(text[:input.Start]) {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		if input.Limit == 0 {
			input.Limit = 8192
		}
		if input.Limit < 1 || input.Limit > 8192 {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		end := min(input.Start+input.Limit, len(text))
		for end > input.Start && !utf8.ValidString(text[input.Start:end]) {
			end--
		}
		payload, _ := json.Marshal(map[string]any{"sha256": file.SHA256, "size_bytes": len(text), "start": input.Start, "end": end, "text": text[input.Start:end]})
		writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: string(payload)})
		return
	}
	var updated string
	if tool == "file_comment_replace" {
		err = artifactreview.ValidateOfficeReplacement(text, file.MIMEType, q.Comments, replacement)
		updated = string(replacement.Content)
	} else {
		updated, err = artifactreview.Apply(text, file.MIMEType, q.Comments, sub)
	}
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	var uploadLimit int64
	err = txfn(func(ctx context.Context, tx pgx.Tx) error {
		if e := check(ctx, tx); e != nil {
			return e
		}
		var e error
		uploadLimit, e = h.delivery.ArtifactUploadLimit(ctx, tx, org, run, int64(len(updated)))
		return e
	})
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	id, err := newUUID()
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	key := fmt.Sprintf("%s/%s/%s", org, run, id)
	stored, err := h.delivery.Storage.Put(r.Context(), key, strings.NewReader(updated), uploadLimit)
	if err != nil {
		writeArtifactStorageError(w, r, err)
		return
	}
	var proposal artifactreview.Proposal
	keep := false
	defer func() {
		if !keep {
			removeUnregisteredStorageObject(r.Context(), h.delivery.Storage, key, func(ctx context.Context) (bool, error) {
				var registered bool
				err := h.runDeliveryDeviceTransaction(r.WithContext(ctx), selection, func(ctx context.Context, tx pgx.Tx) error {
					var err error
					registered, err = artifactStorageRegistered(ctx, tx, run, key)
					return err
				})
				return registered, err
			})
		}
	}()
	err = txfn(func(ctx context.Context, tx pgx.Tx) error {
		// Lock the run before context rows: upload cleanup uses the same lock order.
		if _, e := tx.Exec(ctx, `SELECT 1 FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, run); e != nil {
			return e
		}
		if e := check(ctx, tx); e != nil {
			return e
		}
		var found bool
		var e error
		proposal, found, e = svc.ProposalReplay(ctx, tx, run, contextID, hash)
		if found || e != nil {
			return e
		}
		if q.ResultHash != "" {
			return artifactreview.ErrConflict
		}
		q.File, e = svc.CurrentSource(ctx, tx, org, q)
		if e != nil {
			return e
		}
		if q.File.SHA256 != file.SHA256 || q.File.Version != file.Version {
			return artifactreview.ErrConflict
		}
		config, e := h.delivery.Resources.GetConfig(ctx, tx, org)
		if e != nil {
			return e
		}
		summary := sub.Summary
		if tool == "file_comment_replace" {
			summary = replacement.Summary
		}
		proposal, e = svc.RecordProposal(ctx, tx, artifactreview.StoredProposal(id, run, q, hash, summary, key, stored), config.Policy.ArtifactRunMaxBytes)
		return e
	})
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	keep = proposal.ID == id
	writeJSON(w, 200, dispatch.ToolResult{OK: true, Text: proposalFileResult(proposal)})
}
func strictReviewJSON(raw json.RawMessage, target any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return artifactreview.ErrInvalid
	}
	return nil
}

func appliedFileResult(id, sha string) string {
	value, _ := json.Marshal(map[string]any{"applied": true, "artifact_id": id, "sha256": sha, "message": "Changes applied. The preview is updated. Summarize the result in this chat."})
	return string(value)
}

func proposalFileResult(p artifactreview.Proposal) string {
	if p.Status == "ACCEPTED" {
		return appliedFileResult(p.ResultArtifactID, p.SHA256)
	}
	value, _ := json.Marshal(map[string]any{"applied": false, "proposal_id": p.ID, "status": p.Status, "sha256": p.SHA256, "message": "Candidate saved for user review. The original is unchanged. Ask the user to review and accept it; never claim changes are applied before acceptance."})
	return string(value)
}
