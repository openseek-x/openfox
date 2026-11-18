package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifactreview"
)

func (h *Handler) registerWorkspaceCommentRoutes(mux *http.ServeMux) {
	const path = "/v1/organizations/{organizationID}/conversations/{conversationID}/comments"
	mux.HandleFunc("PUT "+path+"/{threadID}/resolutions/{resolutionID}", h.resolveWorkspaceAnchor)
	mux.HandleFunc("GET "+path, h.workspaceComments)
	mux.HandleFunc("GET "+path+"/{threadID}", h.workspaceComments)
	mux.HandleFunc("PUT "+path+"/{threadID}", h.workspaceComments)
	mux.HandleFunc("PATCH "+path+"/{threadID}", h.workspaceComments)
}
func (h *Handler) workspaceComments(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || (r.Method != "GET" && !requireConversationCSRF(w, r, auth)) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	org, conversation, id := selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("threadID")
	svc := artifactreview.Service{}
	txfn := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runConversationTransaction(r, selection, fn)
	}
	var result any
	var err error
	switch r.Method {
	case "GET":
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			if id == "" {
				result, e = svc.ListComments(ctx, tx, org, conversation, r.URL.Query().Get("cursor"), actor)
			} else {
				result, e = svc.GetComment(ctx, tx, org, conversation, id, actor)
			}
			return e
		})
	case "PATCH":
		var input artifactreview.CommentMutation
		if decodeJSON(w, r, &input) != nil {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			result, e = svc.MutateComment(ctx, tx, org, conversation, id, actor, input)
			return e
		})
	case "PUT":
		var source artifactreview.CommentSource
		if decodeJSON(w, r, &source) != nil {
			writeReviewError(w, r, artifactreview.ErrInvalid)
			return
		}
		source, err = artifactreview.NormalizeCommentSource(source)
		if err != nil {
			writeReviewError(w, r, err)
			return
		}
		var replay bool
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			var t artifactreview.CommentThread
			var e error
			t, replay, e = svc.ReplayCommentCreation(ctx, tx, org, conversation, id, actor, source)
			if replay {
				result = t
			}
			return e
		})
		if err == nil && !replay && source.File != nil {
			var f artifactreview.File
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				f, e = svc.CommentFile(ctx, tx, org, conversation, actor, *source.File)
				return e
			})
			if err == nil {
				var text string
				text, err = h.readReviewFile(r.Context(), f)
				if err == nil {
					var validated artifactreview.Input
					validated, err = artifactreview.ValidateFileInput(*source.File, f, text)
					source.File = &validated
				}
			}
		}
		if err == nil && !replay {
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				result, e = svc.CreateComment(ctx, tx, org, conversation, id, actor, source)
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
