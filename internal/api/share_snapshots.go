package api

import (
	"context"
	"errors"
	"net/http"
	"openfox/internal/idempotency"
	"openfox/internal/secrets"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/platform/apierror"
	"openfox/internal/sharesnapshots"
)

// 阶段 12g · 分享快照。创建 / 列表 / 撤销走登录态 + 租户事务；匿名读取走令牌哈希入口。

// shareViewRatePerMin 是匿名读取的每 IP 每分钟上限；足够正常浏览，又让令牌枚举不可行。
const shareViewRatePerMin = 30

func (h *Handler) registerShareSnapshotRoutes(mux *http.ServeMux) {
	const base = "/v1/organizations/{organizationID}/share-snapshots"
	mux.HandleFunc("POST "+base, h.createShareSnapshot)
	mux.HandleFunc("GET "+base, h.listShareSnapshots)
	mux.HandleFunc("GET "+base+"/{snapshotID}/link", h.recoverShareSnapshot)
	mux.HandleFunc("DELETE "+base+"/{snapshotID}", h.revokeShareSnapshot)
	mux.HandleFunc("POST /v1/share-snapshots/view", h.viewShareSnapshot)
}

func (h *Handler) shareService() sharesnapshots.Service {
	return sharesnapshots.Service{Pool: h.pool, Box: h.secrets}
}

type createShareSnapshotRequest struct {
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	SpaceID        string `json:"space_id"`
	TaskID         string `json:"task_id"`
	CommentID      string `json:"comment_id"`
	Title          string `json:"title"`
	Expiry         string `json:"expiry"`
}

func (h *Handler) createShareSnapshot(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input createShareSnapshotRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	input.Title = strings.TrimSpace(input.Title)
	input.ConversationID = strings.ToLower(input.ConversationID)
	input.MessageID = strings.ToLower(input.MessageID)
	input.SpaceID = strings.ToLower(input.SpaceID)
	input.TaskID = strings.ToLower(input.TaskID)
	input.CommentID = strings.ToLower(input.CommentID)
	if input.Expiry == "" {
		input.Expiry = sharesnapshots.DefaultExpiry
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	replayed := false
	var created sharesnapshots.Created
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := h.shareService().Conversations.CheckActor(ctx, tx, selection.OrganizationID, actor); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "share_snapshot.create", key, input)
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			created, err = h.shareService().Recover(ctx, tx, selection.OrganizationID, actor, claim.ResourceID)
			return err
		}
		created, err = h.shareService().Create(ctx, tx, selection.OrganizationID, actor, sharesnapshots.CreateInput{
			Kind: input.Kind, Scope: input.Scope, ConversationID: input.ConversationID, MessageID: input.MessageID, SpaceID: input.SpaceID, TaskID: input.TaskID, CommentID: input.CommentID, Title: input.Title, Expiry: input.Expiry,
		})
		if err != nil {
			return err
		}
		expiry := input.Expiry
		if expiry == "" {
			expiry = sharesnapshots.DefaultExpiry
		}
		if err := recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor, "share_snapshot.created",
			"share_snapshot", created.ID, "", map[string]string{"scope": created.Scope, "kind": created.Kind, "expiry": expiry}); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, created.ID)
	})
	if err != nil {
		writeShareError(w, r, err)
		return
	}
	// 响应里有明文令牌，禁止任何缓存。
	w.Header().Set("Cache-Control", "no-store")
	writeCreated(w, replayed, created)
}

func (h *Handler) listShareSnapshots(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var items []sharesnapshots.Meta
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = h.shareService().List(ctx, tx, selection.OrganizationID, actor)
		return err
	})
	if err != nil {
		writeShareError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share_snapshots": items})
}

func (h *Handler) revokeShareSnapshot(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var meta sharesnapshots.Meta
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var changed bool
		var err error
		meta, changed, err = h.shareService().Revoke(ctx, tx, selection.OrganizationID, actor, r.PathValue("snapshotID"))
		if err != nil || !changed {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor, "share_snapshot.revoked",
			"share_snapshot", meta.ID, "", map[string]string{"scope": meta.Scope, "kind": meta.Kind})
	})
	if err != nil {
		writeShareError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// viewShareSnapshot 是唯一的匿名入口。令牌放在请求体里（页面把它放在 URL 片段），不进入访问日志与 Referer。
// 不存在、已撤销、已过期、范围不符、公开分享被关闭一律同一个 404。
func (h *Handler) viewShareSnapshot(w http.ResponseWriter, r *http.Request) {
	// 公开页的响应都不允许缓存或被搜索引擎收录。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !h.allowByClientIP(w, r, h.shareViewLimiter) {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeShareNotFound(w, r)
		return
	}
	// 登录态是可选的：只用于限组织快照的成员校验；无效会话等同匿名。
	var userID string
	if auth, err := h.authenticate(r); err == nil {
		userID = auth.principal.UserID
	}
	view, err := h.shareService().Resolve(r.Context(), userID, input.Token)
	if err != nil {
		if errors.Is(err, sharesnapshots.ErrNotFound) {
			writeShareNotFound(w, r)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writeShareNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "NOT_FOUND", "share snapshot is not available")
}

func writeShareError(w http.ResponseWriter, r *http.Request, err error) {
	var redaction *sharesnapshots.RedactionError
	switch {
	case errors.As(err, &redaction):
		requestID := collaborationRequestID(w, r)
		apierror.WriteDetails(w, http.StatusUnprocessableEntity, "SENSITIVE_CONTENT_DETECTED",
			"sensitive content was detected; remove it and try again", requestID, map[string]any{"findings": redaction.Findings})
	case errors.Is(err, secrets.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "share links require a secrets key")
	case errors.Is(err, secrets.ErrDecrypt):
		writeError(w, r, http.StatusServiceUnavailable, "SHARE_LINK_UNAVAILABLE", "share link cannot be recovered")
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrConflict):
		writeCollaborationError(w, r, err)
	case errors.Is(err, sharesnapshots.ErrNotFound), errors.Is(err, conversations.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, sharesnapshots.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "share request is invalid")
	case errors.Is(err, sharesnapshots.ErrPublicDisabled):
		writeError(w, r, http.StatusForbidden, "PUBLIC_SHARING_DISABLED", "public sharing is disabled for this organization")
	case errors.Is(err, sharesnapshots.ErrSourceEmpty):
		writeError(w, r, http.StatusUnprocessableEntity, "SHARE_SOURCE_EMPTY", "there is nothing to share")
	case errors.Is(err, sharesnapshots.ErrTooLarge):
		writeError(w, r, http.StatusUnprocessableEntity, "SHARE_TOO_LARGE", "the content is too large to share")
	case errors.Is(err, sharesnapshots.ErrQuotaExceeded):
		writeError(w, r, http.StatusConflict, "SHARE_QUOTA_EXCEEDED", "too many active share snapshots")
	default:
		writeConversationError(w, r, err)
	}
}

func (h *Handler) recoverShareSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var result sharesnapshots.Created
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.shareService().Recover(ctx, tx, selection.OrganizationID, actor, r.PathValue("snapshotID"))
		return err
	})
	if err != nil {
		writeShareError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
