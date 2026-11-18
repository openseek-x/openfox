package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/docs"
	"openfox/internal/office"
)

func (h *Handler) registerWorkspaceProposalRoutes(mux *http.ServeMux) {
	const path = "/v1/organizations/{organizationID}/conversations/{conversationID}/proposals"
	mux.HandleFunc("GET "+path, h.workspaceProposals)
	mux.HandleFunc("GET "+path+"/{proposalID}", h.workspaceProposals)
	mux.HandleFunc("POST "+path+"/{proposalID}/decision", h.workspaceProposals)
	for _, suffix := range []string{"content", "office", "office-preview"} {
		mux.HandleFunc("GET "+path+"/{proposalID}/"+suffix, h.workspaceProposalContent)
	}
	mux.HandleFunc("PUT "+path+"/{proposalID}/restore", h.restoreArtifactProposal)
}

func (h *Handler) workspaceProposals(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || (r.Method != "GET" && !requireConversationCSRF(w, r, auth)) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	org, conversation, id := selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("proposalID")
	svc := artifactreview.Service{}
	txfn := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runConversationTransaction(r, selection, fn)
	}
	var result any
	var err error
	if r.Method == "GET" {
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			if id == "" {
				result, e = svc.ListProposals(ctx, tx, org, conversation, r.URL.Query().Get("cursor"), actor)
			} else {
				result, e = svc.GetProposal(ctx, tx, org, conversation, id, actor, false)
			}
			return e
		})
	} else {
		var d artifactreview.ProposalDecision
		if decodeJSON(w, r, &d) != nil {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		var p artifactreview.Proposal
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			p, e = svc.GetProposal(ctx, tx, org, conversation, id, actor, false)
			return e
		})
		var body string
		// File IO and OOXML parsing never hold database locks. Terminal retries use
		// the durable receipt even if the candidate has subsequently expired.
		if err == nil && p.Status == "PENDING" && d.Action == "accept" {
			body, err = h.readReviewFile(r.Context(), p.CandidateFile())
		}
		if err == nil {
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var decided artifactreview.Proposal
				var e error
				decided, e = svc.DecideProposal(ctx, tx, org, conversation, id, actor, d, body)
				result = decided
				return e
			})
		}
	}
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) workspaceProposalContent(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var f artifactreview.File
	side := r.URL.Query().Get("side")
	if side != "base" && side != "candidate" {
		writeReviewError(w, r, artifactreview.ErrInvalid)
		return
	}
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		svc := artifactreview.Service{}
		p, e := svc.GetProposal(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("proposalID"), actor, false)
		if e != nil {
			return e
		}
		if side == "base" {
			f, e = svc.ProposalBase(ctx, tx, p)
		} else {
			f = p.CandidateFile()
		}
		return e
	})
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	raw, err := h.readReviewFile(r.Context(), f)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if !strings.HasSuffix(r.URL.Path, "/content") {
		if !office.MIME(f.MIMEType) {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		h.writeOfficePreview(w, r, raw, f.MIMEType, f.SHA256)
		return
	}
	w.Header().Set("Content-Type", f.MIMEType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", docs.ContentDisposition("attachment", f.Name))
	_, _ = w.Write([]byte(raw))
}
