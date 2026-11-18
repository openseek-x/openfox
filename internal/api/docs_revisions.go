package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/docs"
)

func (h *Handler) docRevisionRequest(w http.ResponseWriter, r *http.Request, restore bool) {
	call, ok := h.beginTaskCall(w, r, restore)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var revision, before uint64
	if raw := r.PathValue("revision"); raw != "" {
		revision, err = docVersion(raw)
	} else if raw := r.URL.Query().Get("before"); raw != "" {
		before, err = docVersion(raw)
	}
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var input struct {
		Version uint64 `json:"version"`
	}
	if restore {
		if err := decodeDocJSON(w, r, &input); err != nil || input.Version == 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
			return
		}
	}
	spaceID := r.PathValue("spaceID")
	var result any
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		ref, err := docs.LoadRef(ctx, tx, spaceID, nodeID)
		if err != nil {
			return err
		}
		if ref.Kind != docs.KindDoc && ref.Kind != docs.KindSheet {
			return docs.ErrNotFound
		}
		if restore {
			if err := access.RequireWrite(ref.Container); err != nil {
				return err
			}
			item, err := docs.GetRevision(ctx, tx, nodeID, revision)
			if err != nil {
				return err
			}
			if err := docs.UpdateNode(ctx, tx, ref, call.selected.Actor.UserID, input.Version, &item.Title, item.Body); err != nil {
				return err
			}
			if err := docs.SnapshotRestoredRevision(ctx, tx, nodeID); err != nil {
				return err
			}
			detail, err := docs.GetNodeDetail(ctx, tx, spaceID, nodeID)
			detail.CanEdit = true
			result = detail
			return err
		}
		if revision != 0 {
			item, err := docs.GetRevision(ctx, tx, nodeID, revision)
			result = item
			return err
		}
		items, next, err := docs.ListRevisions(ctx, tx, nodeID, before)
		result = map[string]any{"revisions": items, "next_before": next}
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listDocRevisions(w http.ResponseWriter, r *http.Request) {
	h.docRevisionRequest(w, r, false)
}
func (h *Handler) restoreDocRevision(w http.ResponseWriter, r *http.Request) {
	h.docRevisionRequest(w, r, true)
}
