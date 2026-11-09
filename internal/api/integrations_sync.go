package api

// GitHub Issue ↔ 空间同步：列仓库、新建 / 修改 / 移除 / 重试同步关联。
// 调用 GitHub 的请求都在数据库事务之外；令牌失效时把连接标为需要重新授权并返回 409。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func (h *Handler) registerGitHubSyncRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations"
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/repositories", h.listIntegrationRepositories)
	mux.HandleFunc("POST "+base+"/sync-links", h.createIntegrationSyncLink)
	mux.HandleFunc("PATCH "+base+"/sync-links/{syncLinkID}", h.updateIntegrationSyncLink)
	mux.HandleFunc("POST "+base+"/sync-links/{syncLinkID}/remove", h.removeIntegrationSyncLink)
	mux.HandleFunc("POST "+base+"/sync-links/{syncLinkID}/retry", h.retryIntegrationSyncLink)
}

// githubCallError 把 GitHub 客户端错误转成接口错误：令牌失效时标记连接并返回 ErrNeedsReauth，其余按上游不可用处理。
func (h *Handler) githubCallError(ctx context.Context, organizationID, connectionID string, err error) error {
	if integrations.IsTokenRevoked(err) {
		if markErr := h.runAsWorker(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkTokenRevoked(ctx, tx, connectionID)
		}); markErr != nil {
			slog.Default().Warn("git_mark_revoked_failed", "connection_id", connectionID)
		}
		return integrations.ErrNeedsReauth
	}
	slog.Default().Warn("git_call_failed", "connection_id", connectionID, "error", err.Error())
	return integrations.ErrUpstreamUnavailable
}

func (h *Handler) listIntegrationRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	page := 1
	if raw := query.Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 20 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "page is invalid")
			return
		}
		page = value
	}
	if len(q) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "q is too long")
		return
	}
	connectionID := r.PathValue("connectionID")
	var target integrations.RepositoryTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		target, err = h.integrations.PrepareRepositories(ctx, tx, call.organizationID, call.selected.Actor, connectionID)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	repos, more, err := target.Client.ListRepos(r.Context(), target.Token, target.UserAccount, target.Login, q, page)
	if err != nil {
		writeIntegrationError(w, r, h.githubCallError(r.Context(), call.organizationID, connectionID, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": target.Annotate(repos), "has_more": more})
}

func (h *Handler) createIntegrationSyncLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var body struct {
		ConnectionID   string `json:"connection_id"`
		Repository     string `json:"repository"`
		SpaceID        string `json:"space_id"`
		Direction      string `json:"direction"`
		ImportExisting bool   `json:"import_existing"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	input := integrations.CreateSyncInput{ConnectionID: body.ConnectionID, Repository: body.Repository, SpaceID: body.SpaceID,
		Direction: body.Direction, ImportExisting: body.ImportExisting}
	if !input.Valid() {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	linkID, err := membership.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	// 先生成对象 ID 再授权空间写入（与导入上传接口一致）。
	var target integrations.CreateSyncTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		target, err = h.integrations.PrepareCreateSync(ctx, tx, call.organizationID, call.selected.Actor, input)
		if err != nil {
			return err
		}
		return call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, linkID)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	// 仓库校验在事务外：必须能访问，且所有者就是连接账号。
	ownerName, repoName, _ := strings.Cut(input.Repository, "/")
	repo, err := target.Client.GetRepo(r.Context(), target.Token, ownerName, repoName)
	switch {
	case errors.Is(err, gitapi.ErrNotFound):
		writeIntegrationError(w, r, integrations.ErrInvalidRepository)
		return
	case err != nil:
		writeIntegrationError(w, r, h.githubCallError(r.Context(), call.organizationID, input.ConnectionID, err))
		return
	case !target.InstanceToken && !strings.EqualFold(repo.Owner.Login, target.Owner):
		writeIntegrationError(w, r, integrations.ErrInvalidRepository)
		return
	}
	requestID := collaborationRequestID(w, r)
	var link integrations.SyncLink
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, linkID); err != nil {
			return err
		}
		var err error
		link, err = h.integrations.InsertSyncLink(ctx, tx, call.organizationID, call.selected.Actor, linkID, input, repo)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.sync_link_created", "integration_sync_link", linkID, input.SpaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github", "result": input.Direction})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (h *Handler) updateIntegrationSyncLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Version   uint64 `json:"version"`
		Direction string `json:"direction"`
		Status    string `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var link integrations.SyncLink
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		link, err = h.integrations.UpdateSyncLink(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("syncLinkID"),
			integrations.UpdateSyncInput{Version: body.Version, Direction: body.Direction, Status: body.Status})
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.sync_link_updated", "integration_sync_link", link.ID, link.SpaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github", "result": link.Direction, "status": link.Status})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *Handler) removeIntegrationSyncLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	syncLinkID := r.PathValue("syncLinkID")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.RemoveSyncLink(ctx, tx, call.organizationID, call.selected.Actor, syncLinkID, body.Version); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.sync_link_removed", "integration_sync_link", syncLinkID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) retryIntegrationSyncLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	requestID := collaborationRequestID(w, r)
	syncLinkID := r.PathValue("syncLinkID")
	var requeued int
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		requeued, err = h.integrations.RetrySyncLink(ctx, tx, call.organizationID, call.selected.Actor, syncLinkID)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.sync_link_retried", "integration_sync_link", syncLinkID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github", "result": strconv.Itoa(requeued)})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"requeued": requeued})
}
