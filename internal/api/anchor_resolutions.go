package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifactreview"
)

func (h *Handler) resolveWorkspaceAnchor(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	org, conversation, thread, id := selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("threadID"), r.PathValue("resolutionID")
	var input artifactreview.ResolveRequest
	if decodeJSON(w, r, &input) != nil || input.Version < 1 {
		writeReviewError(w, r, artifactreview.ErrInvalid)
		return
	}
	svc := artifactreview.Service{}
	txfn := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runConversationTransaction(r, selection, fn)
	}
	hash := artifactreview.ResolutionHash(conversation, actor.UserID, thread, input)
	var result artifactreview.AnchorResolution
	var replay bool
	err := txfn(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		result, replay, e = svc.ResolutionReplay(ctx, tx, org, conversation, thread, id, hash, actor)
		return e
	})
	if err == nil && !replay {
		result = artifactreview.AnchorResolution{ID: id, ThreadID: thread, ThreadVersion: input.Version}
		if input.Browser != nil {
			var t artifactreview.CommentThread
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				t, e = svc.GetComment(ctx, tx, org, conversation, thread, actor)
				return e
			})
			if err == nil && (t.Source.Browser == nil || input.Browser.Browser == nil || input.Browser.Browser.Comments == nil) {
				err = artifactreview.ErrInvalid
			}
			if err == nil {
				var source artifactreview.CommentSource
				source, err = artifactreview.NormalizeCommentSource(*input.Browser)
				if err == nil {
					result.Resolution = artifactreview.Resolution{Status: "exact", Reason: "explicit_rebind"}
					result.Target = &source
				}
			}
		} else {
			var t artifactreview.CommentThread
			var base, head artifactreview.File
			var target artifactreview.Input
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				t, base, head, target, e = svc.ResolutionFiles(ctx, tx, org, conversation, thread, actor)
				return e
			})
			if err == nil && t.Version != input.Version {
				err = artifactreview.ErrConflict
			}
			if err == nil {
				var old, next string
				old, err = h.readReviewFile(r.Context(), base)
				if err == nil {
					next, err = h.readReviewFile(r.Context(), head)
				}
				if err == nil {
					result.Resolution, err = artifactreview.ResolveAnchor(old, next, base.MIMEType, t.Source.File.Comments[0].Anchor)
				}
				if err == nil {
					result.TargetFile = &head
					if result.Anchor != nil {
						target.Comments[0].Anchor = *result.Anchor
						target, err = artifactreview.ValidateFileInput(target, head, next)
						result.Target = &artifactreview.CommentSource{Name: head.Name, File: &target}
					}
				}
			}
		}
		if err == nil {
			err = txfn(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				result, e = svc.SaveResolution(ctx, tx, org, conversation, actor, hash, result)
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
