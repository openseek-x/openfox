package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/executiondelivery"
	"openfox/internal/office"
)

// The projection uses the same Run/conversation authorization as downloading
// the file. ZIP IO and parsing stay outside the database transaction.
func (h *Handler) officeArtifact(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var a executiondelivery.Artifact
	var key string
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, key, e = h.delivery.GetArtifactFile(ctx, tx, selection.OrganizationID, r.PathValue("runID"), r.PathValue("artifactID"), actor)
		return e
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if !office.MIME(a.MIMEType) {
		writeReviewError(w, r, artifactreview.ErrUnavailable)
		return
	}
	raw, err := h.readReviewFile(r.Context(), artifactreview.File{StorageKey: key, MIMEType: a.MIMEType, SHA256: a.SHA256, SizeBytes: a.SizeBytes, State: a.State, RetentionUntil: a.RetentionUntil})
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	h.writeOfficePreview(w, r, raw, a.MIMEType, a.SHA256)
}

func (h *Handler) writeOfficePreview(w http.ResponseWriter, r *http.Request, raw, mimeType, sha string) {
	p, err := office.Read([]byte(raw), mimeType)
	if err != nil {
		writeReviewError(w, r, artifactreview.ErrUnavailable)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/office-preview") {
		if format := r.URL.Query().Get("format"); format != "" && format != "pdf" {
			if format != "xlsx" || p.View().Kind != "xlsx" {
				writeReviewError(w, r, artifactreview.ErrInvalid)
				return
			}
			data, err := p.SpreadsheetPreview()
			if err != nil {
				writeReviewError(w, r, artifactreview.ErrUnavailable)
				return
			}
			w.Header().Set("Content-Type", office.XLSX)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Disposition", `inline; filename="preview.xlsx"`)
			_, _ = w.Write(data)
			return
		}
		// 转换有独立的 60 秒上限，仅放宽本响应的写期限。
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(75 * time.Second))
		pdf, err := h.officePreview.PDF(r.Context(), []byte(raw), p.View().Kind)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "OFFICE_PREVIEW_UNAVAILABLE", "Office preview renderer is unavailable; retry or download the original file")
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `inline; filename="preview.pdf"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdf)
		return
	}
	if p.View().Kind == "xlsx" {
		_, _ = p.SpreadsheetPreview()
	} // nil projection selects PDF for unsupported drawings/caches.
	writeJSON(w, http.StatusOK, struct {
		office.View
		SHA256 string `json:"sha256"`
	}{p.View(), sha})
}
