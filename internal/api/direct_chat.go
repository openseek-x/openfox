package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/directchat"
	"openfox/internal/docs"
)

func (h *Handler) registerDirectChatRoutes(mux *http.ServeMux) {
	const base = "/v1/organizations/{organizationID}/direct-chats"
	for _, route := range []string{"GET " + base, "POST " + base, "GET " + base + "/{chatID}", "GET " + base + "/{chatID}/messages", "POST " + base + "/{chatID}/messages", "POST " + base + "/{chatID}/read", "POST " + base + "/{chatID}/attachments", "GET " + base + "/{chatID}/attachments/{fileID}", "DELETE " + base + "/{chatID}/attachments/{fileID}"} {
		mux.HandleFunc(route, h.directChat)
	}
}
func (h *Handler) directChat(w http.ResponseWriter, r *http.Request) {
	auth, selection, _, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet && !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := auth.principal.UserID
	id := r.PathValue("chatID")
	fid := r.PathValue("fileID")
	run := func(fn func(context.Context, pgx.Tx) error) error {
		return h.runConversationTransaction(r, selection, fn)
	}
	if fid != "" {
		var f directchat.File
		err := run(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			f, err = directchat.LoadFile(ctx, tx, id, fid, actor, r.Method == http.MethodDelete)
			return err
		})
		if err != nil {
			writeAttachmentError(w, r, err)
			return
		}
		if r.Method == http.MethodDelete {
			if h.delivery.Storage != nil {
				_ = h.delivery.Storage.Delete(r.Context(), f.Key)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		disposition := "attachment"
		if r.URL.Query().Get("disposition") == "inline" && f.Previewable {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", docs.ContentDisposition(disposition, f.Name))
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "private, no-store")
		h.streamStoredFile(w, r, f.Key, f.ContentType, f.SizeBytes)
		return
	}
	if r.Pattern == "POST /v1/organizations/{organizationID}/direct-chats/{chatID}/attachments" {
		if h.delivery.Storage == nil {
			writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
			return
		}
		spool, name, ct, ok := h.readUpload(w, r, func() error {
			return run(func(ctx context.Context, tx pgx.Tx) error { return directchat.Authorize(ctx, tx, id, false) })
		})
		if !ok {
			return
		}
		defer spool.close()
		aid, err := collaboration.NewID()
		if err != nil {
			writeConversationError(w, r, err)
			return
		}
		key := attachments.StorageKey(selection.OrganizationID, aid)
		if _, err = h.storeFile(r, key, spool, attachments.MaxBytes); err != nil {
			writeStoreError(w, r, err, attachments.ErrTooLarge)
			return
		}
		var f directchat.File
		err = run(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			f, err = directchat.Upload(ctx, tx, id, actor, aid, name, attachments.FileInfo{Key: key, ContentType: ct, SizeBytes: spool.size, SHA256: spool.sha256})
			return err
		})
		if err != nil {
			_ = h.delivery.Storage.Delete(r.Context(), key)
			writeAttachmentError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, f)
		return
	}
	var result any
	status := http.StatusOK
	err := run(func(ctx context.Context, tx pgx.Tx) error {
		switch r.Pattern {
		case "POST /v1/organizations/{organizationID}/direct-chats":
			var in struct {
				UserID string `json:"user_id"`
			}
			if decodeJSON(w, r, &in) != nil {
				return conversations.ErrInvalidRequest
			}
			c, err := directchat.Ensure(ctx, tx, actor, in.UserID)
			result = c
			return err
		case "GET /v1/organizations/{organizationID}/direct-chats":
			items, next, err := directchat.List(ctx, tx, r.URL.Query().Get("before"), parseConversationLimit(r))
			result = map[string]any{"chats": items, "next_before": next}
			return err
		case "GET /v1/organizations/{organizationID}/direct-chats/{chatID}":
			c, err := directchat.Get(ctx, tx, id)
			result = c
			return err
		case "GET /v1/organizations/{organizationID}/direct-chats/{chatID}/messages":
			var before, after int64
			for name, dst := range map[string]*int64{"before": &before, "after": &after} {
				if raw := r.URL.Query().Get(name); raw != "" {
					n, err := strconv.ParseInt(raw, 10, 64)
					if err != nil {
						return conversations.ErrInvalidRequest
					}
					*dst = n
				}
			}
			items, next, err := directchat.MessageWindow(ctx, tx, id, before, after, r.URL.Query().Get("around"), parseConversationLimit(r))
			if after != 0 {
				result = map[string]any{"messages": items, "next_before": 0, "next_after": next}
			} else {
				result = map[string]any{"messages": items, "next_before": next}
			}
			return err
		case "POST /v1/organizations/{organizationID}/direct-chats/{chatID}/messages":
			var in directchat.SendInput
			if decodeJSON(w, r, &in) != nil {
				return conversations.ErrInvalidRequest
			}
			m, err := directchat.Send(ctx, tx, id, actor, in)
			result = m
			return err
		case "POST /v1/organizations/{organizationID}/direct-chats/{chatID}/read":
			var in struct {
				Seq int64 `json:"seq"`
			}
			if decodeJSON(w, r, &in) != nil {
				return conversations.ErrInvalidRequest
			}
			status = http.StatusNoContent
			return directchat.MarkRead(ctx, tx, id, actor, in.Seq)
		}
		return conversations.ErrInvalidRequest
	})
	if err != nil {
		writeAttachmentError(w, r, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}
