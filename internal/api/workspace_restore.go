package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

// Restoring history creates a reviewable candidate against an explicit current
// head. Acceptance appends a revision; it never rewinds or mutates history.
func (h *Handler) restoreArtifactProposal(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	org, conv, id := selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("proposalID")
	var input struct {
		Source string `json:"source_artifact_id"`
		Base   string `json:"base_artifact_id"`
	}
	if decodeJSON(w, r, &input) != nil || !membership.ValidateUUID(id) || !membership.ValidateUUID(input.Source) || !membership.ValidateUUID(input.Base) || input.Source == input.Base {
		writeReviewError(w, r, artifactreview.ErrInvalid)
		return
	}
	encoded, _ := json.Marshal(input)
	hash := artifactreview.Digest(actor.UserID + ":" + string(encoded))
	svc := artifactreview.Service{}
	txfn := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runConversationTransaction(r, selection, fn)
	}
	var p artifactreview.Proposal
	var base, source artifactreview.File
	var replay bool
	var limit, maxBytes int64
	load := func(ctx context.Context, tx pgx.Tx) error {
		if _, e := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conv, actor); e != nil {
			return e
		}
		prior, e := svc.GetProposal(ctx, tx, org, conv, id, actor, false)
		if e == nil {
			if prior.RequestHash != hash || prior.CreatedBy != actor.UserID {
				return artifactreview.ErrConflict
			}
			p = prior
			replay = true
			return nil
		}
		if !errors.Is(e, conversations.ErrNotFound) {
			return e
		}
		a, e := h.delivery.ResolveArtifactPreview(ctx, tx, org, conv, input.Source, "", actor)
		if e != nil {
			return e
		}
		b, e := h.delivery.ResolveArtifactPreview(ctx, tx, org, conv, input.Base, "", actor)
		if e != nil {
			return e
		}
		if a.ContentID != b.ContentID || b.ID != b.HeadArtifactID {
			return artifactreview.ErrConflict
		}
		source, e = svc.CommentFile(ctx, tx, org, conv, actor, artifactreview.Input{ArtifactID: a.ID, SHA256: a.SHA256})
		if e != nil {
			return e
		}
		base, e = svc.CommentFile(ctx, tx, org, conv, actor, artifactreview.Input{ArtifactID: b.ID, SHA256: b.SHA256})
		if e != nil {
			return e
		}
		config, e := h.delivery.Resources.GetConfig(ctx, tx, org)
		if e != nil {
			return e
		}
		maxBytes = config.Policy.ArtifactRunMaxBytes
		limit, e = h.delivery.ArtifactUploadLimit(ctx, tx, org, base.RunID, source.SizeBytes)
		if e != nil {
			return e
		}
		p = artifactreview.Proposal{ID: id, RunID: base.RunID, ConversationID: conv, RequestHash: hash, Base: base, Source: artifactreview.Input{ID: id, ArtifactID: base.ID, SHA256: base.SHA256, ConversationID: conv, Comments: []artifactreview.Snapshot{}}, Summary: fmt.Sprintf("Restore version %d", a.ContentRevision), CreatedBy: actor.UserID}
		return nil
	}
	if err := txfn(load); err != nil {
		writeReviewError(w, r, err)
		return
	}
	if !replay {
		body, err := h.readReviewFile(r.Context(), source)
		if err != nil {
			writeReviewError(w, r, err)
			return
		}
		objectID, err := newUUID()
		if err != nil {
			writeReviewError(w, r, err)
			return
		}
		key := fmt.Sprintf("%s/%s/%s", org, base.RunID, objectID)
		stored, err := h.delivery.Storage.Put(r.Context(), key, strings.NewReader(body), limit)
		if err != nil {
			writeArtifactStorageError(w, r, err)
			return
		}
		run := base.RunID
		defer removeUnregisteredStorageObject(r.Context(), h.delivery.Storage, key, func(ctx context.Context) (bool, error) {
			var registered bool
			err := h.runConversationTransaction(r.WithContext(ctx), selection, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				registered, err = artifactStorageRegistered(ctx, tx, run, key)
				return err
			})
			return registered, err
		})
		err = txfn(func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, run); err != nil {
				return err
			}
			if err := load(ctx, tx); err != nil || replay {
				return err
			}
			p.StorageKey, p.SizeBytes, p.SHA256 = key, stored.Size, stored.SHA256
			var err error
			p, err = svc.RecordProposal(ctx, tx, p, maxBytes)
			return err
		})
		if err != nil {
			writeReviewError(w, r, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, p)
}
