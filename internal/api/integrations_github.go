package api

// GitHub：OAuth 绑定（发起 / 回调 / 会话 / 勾选完成）、Webhook 配置与接收、服务商设置、任务的 Git 关联列表。

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/integrations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	gitWebhookBodyLimit = 5 << 20
	backlinkTimeout     = 15 * time.Second
	minGitWebhookToken  = 16
)

type gitProviderKey struct{}

// gitRoute 把路径里固定的 Git 服务商放进请求上下文。服务商只来自 registerGitHubIntegrationRoutes 的白名单循环，
// 所以未知服务商（例如 /integrations/foo/authorize）没有路由，直接 404；GitHub 的字面量路径行为不变。
func gitRoute(provider string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r.WithContext(context.WithValue(r.Context(), gitProviderKey{}, provider)))
	}
}

// gitProvider 返回当前请求的 Git 服务商（默认 GitHub）。
func gitProvider(r *http.Request) string {
	if value, ok := r.Context().Value(gitProviderKey{}).(string); ok && value != "" {
		return value
	}
	return integrations.ProviderGitHub
}

// gitSvc 返回绑定到当前请求服务商的 Service。
func (h *Handler) gitSvc(r *http.Request) integrations.Service {
	return h.integrations.ForGit(gitProvider(r))
}

var gitProviders = []string{integrations.ProviderGitHub, integrations.ProviderGitLab, integrations.ProviderGitLabCN, integrations.ProviderGitee}

func (h *Handler) registerGitHubIntegrationRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations"
	mux.HandleFunc("POST "+base+"/gitlab/token", h.bindGitLabToken)
	for _, provider := range gitProviders {
		mux.HandleFunc("POST "+base+"/"+provider+"/authorize", gitRoute(provider, h.startGitHubAuthorization))
		mux.HandleFunc("GET /v1/integrations/"+provider+"/callback", gitRoute(provider, h.gitHubOAuthCallback))
		mux.HandleFunc("POST /v1/hooks/git/"+provider+"/{token}", gitRoute(provider, h.receiveGitHubWebhook))
	}
	// {provider}/settings 与 oauth/{sessionID} 在 ServeMux 里互相冲突，所以合并成一个分发入口。
	mux.HandleFunc("GET "+base+"/{first}/{second}", h.getIntegrationSubresource)
	mux.HandleFunc("POST "+base+"/oauth/{sessionID}/complete", h.completeIntegrationOAuthSession)
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/webhook", h.getIntegrationWebhook)
	mux.HandleFunc("POST "+base+"/connections/{connectionID}/webhook/rotate", h.rotateIntegrationWebhook)
	mux.HandleFunc("PUT "+base+"/{provider}/settings", h.putIntegrationSettings)
	mux.HandleFunc("GET "+taskBase+"/{taskID}/git-links", h.listTaskGitLinks)
	h.registerGitHubSyncRoutes(mux)
}

func (h *Handler) getIntegrationSubresource(w http.ResponseWriter, r *http.Request) {
	first, second := r.PathValue("first"), r.PathValue("second")
	switch {
	case first == "oauth":
		h.getIntegrationOAuthSession(w, r, second)
	case second == "settings":
		h.getIntegrationSettings(w, r, first)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	}
}

// ---------- 发起授权 ----------

type startAuthorizationRequest struct {
	Purpose      string `json:"purpose"`
	ConnectionID string `json:"connection_id"`
	// 以下三项只在 GitLab / 极狐的组织绑定里使用。
	InstanceURL       string `json:"instance_url"`
	ApplicationID     string `json:"application_id"`
	ApplicationSecret string `json:"application_secret"`
}

func (h *Handler) startGitHubAuthorization(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input startAuthorizationRequest
	if err := decodeJSON(w, r, &input); err != nil || len(input.ConnectionID) > 64 || len(input.InstanceURL) > 2048 ||
		len(input.ApplicationID) > 255 || len(input.ApplicationSecret) > 1024 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.gitSvc(r).StartAuthorization(ctx, tx, call.organizationID, call.selected.Actor,
			integrations.StartInput{Purpose: input.Purpose, ConnectionID: input.ConnectionID, InstanceURL: input.InstanceURL,
				ApplicationID: input.ApplicationID, ApplicationSecret: input.ApplicationSecret})
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": gitProvider(r), "result": input.Purpose})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------- 回调 ----------

// oauthCompleteURL 只拼在 PUBLIC_URL 上，不接受请求参数里的地址，避免开放重定向。
func (h *Handler) oauthCompleteURL(provider, sessionID, reason string) string {
	return h.oauthCompleteURLFor(provider, sessionID, reason)
}

func (h *Handler) oauthCompleteURLFor(provider, sessionID, reason string) string {
	query := url.Values{}
	query.Set("provider", provider)
	query.Set("session", sessionID)
	if reason == "" {
		query.Set("status", "ok")
	} else {
		query.Set("status", "error")
		query.Set("reason", reason)
	}
	return h.centerURL() + "/integrations/oauth/complete?" + query.Encode()
}

func (h *Handler) centerURL() string { return centerPublicURL(h.publicURL) }

// centerPublicURL 返回去掉末尾斜杠的中心地址；未配置时用开发默认值。
func centerPublicURL(value string) string {
	base := strings.TrimRight(strings.TrimSpace(value), "/")
	if base == "" {
		base = defaultCenterPublicURL
	}
	return base
}

func (h *Handler) gitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := gitProvider(r)
	svc := h.gitSvc(r)
	redirect := func(sessionID, reason string) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, h.oauthCompleteURL(provider, sessionID, reason), http.StatusFound)
	}
	query := r.URL.Query()
	state, code, oauthError := query.Get("state"), query.Get("code"), query.Get("error")
	auth, err := h.authenticate(r)
	if err != nil || state == "" || len(state) > 128 || h.pool == nil {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	var orgID, stateID, stateUser, stateProvider string
	found := false
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id::text, state_id::text, user_id::text, provider FROM openfox_private.resolve_integration_oauth_state($1)`,
			integrations.TokenHash(state))
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			return rows.Scan(&orgID, &stateID, &stateUser, &stateProvider)
		}
		return rows.Err()
	})
	if err != nil {
		slog.Default().Warn("git_oauth_state_lookup_failed", "provider", provider)
		redirect("", integrations.ReasonInvalidState)
		return
	}
	// 状态必须属于本回调路径对应的服务商（GitLab 的 state 不能在 GitHub 回调上使用）。
	if !found || stateUser != auth.principal.UserID || stateProvider != provider {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, orgID)
	if err != nil {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: orgID, selected: selected}
	var outcome integrations.CallbackOutcome
	switch {
	case oauthError != "":
		outcome.Failure = integrations.ReasonAccessDenied
	case code == "" || len(code) > 512 || !svc.GitProviderConfigured(provider):
		outcome.Failure = integrations.ReasonExchangeFailed
	default:
		var target integrations.CallbackTarget
		err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			target, err = svc.CallbackVerifier(ctx, tx, orgID, auth.principal.UserID, stateID)
			return err
		})
		if errors.Is(err, integrations.ErrOAuthSessionState) {
			redirect("", integrations.ReasonInvalidState)
			return
		}
		if err != nil {
			slog.Default().Warn("git_oauth_verifier_failed", "provider", provider, "session_id", stateID)
			redirect(stateID, integrations.ReasonExchangeFailed)
			return
		}
		// 令牌换取与服务商 API 调用放在事务外。
		outcome = h.exchangeGit(r.Context(), svc, target, code)
	}
	requestID := collaborationRequestID(w, r)
	var result integrations.CallbackResult
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = svc.FinishCallback(ctx, tx, orgID, selected.Actor, stateID, outcome)
		if err != nil {
			return err
		}
		for _, entry := range result.Audit {
			if err := recordCollaborationAudit(ctx, tx, requestID, orgID, selected.Actor, entry.Action, "integration_connection",
				entry.ResourceID, "", selected.Actor.AuthorizationVersion, entry.Metadata); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, integrations.ErrOAuthSessionState) {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	if err != nil {
		slog.Default().Warn("git_oauth_finish_failed", "provider", provider, "session_id", stateID)
		redirect(stateID, integrations.ReasonExchangeFailed)
		return
	}
	redirect(result.SessionID, result.Reason)
}

// exchangeGit 用授权码换取令牌并读取用户与组织；任何一步失败都按 exchange_failed 处理（日志不含令牌）。
func (h *Handler) exchangeGit(ctx context.Context, svc integrations.Service, target integrations.CallbackTarget, code string) integrations.CallbackOutcome {
	client, err := svc.CallbackClient(target)
	if err != nil {
		return integrations.CallbackOutcome{Failure: integrations.ReasonExchangeFailed}
	}
	token, err := client.Exchange(ctx, code, target.Verifier, svc.RedirectURI())
	if err != nil {
		return integrations.CallbackOutcome{Failure: integrations.ReasonExchangeFailed}
	}
	user, err := client.User(ctx, token.Access)
	if err != nil {
		return integrations.CallbackOutcome{Failure: integrations.ReasonExchangeFailed}
	}
	orgs, err := client.Orgs(ctx, token.Access)
	if err != nil {
		return integrations.CallbackOutcome{Failure: integrations.ReasonExchangeFailed}
	}
	scope := token.Scope
	if user.Scopes != "" {
		scope = user.Scopes
	}
	return integrations.CallbackOutcome{Token: token.Access, Scope: scope, Refresh: token.Refresh, ExpiresAt: token.ExpiresAt, User: user, Orgs: orgs}
}

// ---------- 授权会话 ----------

func (h *Handler) getIntegrationOAuthSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item integrations.OAuthSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.GetSession(ctx, tx, call.organizationID, call.selected.Actor, sessionID)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) completeIntegrationOAuthSession(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Accounts []string `json:"accounts"`
	}
	if err := decodeJSON(w, r, &input); err != nil || len(input.Accounts) == 0 || len(input.Accounts) > 50 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	for _, account := range input.Accounts {
		if len(account) > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
			return
		}
	}
	requestID := collaborationRequestID(w, r)
	var connections []integrations.Connection
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		connections, audits, err = h.integrations.Complete(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), input.Accounts)
		if err != nil {
			return err
		}
		for _, entry := range audits {
			if err := recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor, entry.Action,
				"integration_connection", entry.ResourceID, "", call.selected.Actor.AuthorizationVersion, entry.Metadata); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": connections})
}

// ---------- Webhook 配置 ----------

func (h *Handler) getIntegrationWebhook(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	requestID := collaborationRequestID(w, r)
	var item integrations.Webhook
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.GetWebhook(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.webhook_secret_viewed", "integration_connection", item.ConnectionID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) rotateIntegrationWebhook(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var item integrations.Webhook
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.RotateWebhook(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), input.Version)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.webhook_rotated", "integration_connection", item.ConnectionID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": "github"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, item)
}

// ---------- 服务商设置 ----------

func (h *Handler) getIntegrationSettings(w http.ResponseWriter, r *http.Request, provider string) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item integrations.Settings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.GetSettings(ctx, tx, call.organizationID, call.selected.Actor, provider)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) putIntegrationSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version  uint64 `json:"version"`
		Settings *struct {
			LinkPullRequests     *bool   `json:"link_pull_requests"`
			LinkCommits          *bool   `json:"link_commits"`
			PRBacklinkPrivate    *bool   `json:"pr_backlink_private"`
			PRBacklinkPublic     *bool   `json:"pr_backlink_public"`
			IssueBacklinkPrivate *bool   `json:"issue_backlink_private"`
			IssueBacklinkPublic  *bool   `json:"issue_backlink_public"`
			BranchFormat         *string `json:"branch_format"`
		} `json:"settings"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Settings == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	s := input.Settings
	if s.LinkPullRequests == nil || s.LinkCommits == nil || s.PRBacklinkPrivate == nil || s.PRBacklinkPublic == nil || s.IssueBacklinkPrivate == nil || s.IssueBacklinkPublic == nil || s.BranchFormat == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "all settings fields are required")
		return
	}
	values := integrations.SettingsValues{LinkPullRequests: *s.LinkPullRequests, LinkCommits: *s.LinkCommits,
		PRBacklinkPrivate: *s.PRBacklinkPrivate, PRBacklinkPublic: *s.PRBacklinkPublic, IssueBacklinkPrivate: *s.IssueBacklinkPrivate,
		IssueBacklinkPublic: *s.IssueBacklinkPublic, BranchFormat: *s.BranchFormat}
	requestID := collaborationRequestID(w, r)
	provider := r.PathValue("provider")
	var item integrations.Settings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.PutSettings(ctx, tx, call.organizationID, call.selected.Actor, provider, input.Version, values)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.settings_updated", "integration_settings", call.organizationID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": provider})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// ---------- Webhook 接收 ----------

func (h *Handler) receiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) < minGitWebhookToken || len(token) > 128 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
		return
	}
	tokenHash := integrations.TokenHash(token)
	if !h.allowKey(w, r, h.webhookLimiter, hex.EncodeToString(tokenHash)) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, gitWebhookBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "webhook body exceeds 5 MiB")
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var lookup integrations.GitWebhookLookup
	var lookupProvider string
	found := false
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id::text, connection_id::text, provider FROM openfox_private.resolve_git_webhook($1)`, tokenHash)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			return rows.Scan(&lookup.OrganizationID, &lookup.ConnectionID, &lookupProvider)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	// 令牌属于哪个服务商的连接，就只能在哪个服务商的路径上使用。
	if !found || lookupProvider != gitProvider(r) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
		return
	}
	var outcome integrations.GitWebhookOutcome
	err = h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		outcome, err = h.gitSvc(r).ReceiveGit(ctx, tx, lookup, r.Header, body, time.Now())
		return err
	})
	if err != nil {
		slog.Default().Warn("git_webhook_failed", "provider", gitProvider(r), "connection_id", lookup.ConnectionID)
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	switch {
	case !outcome.Found:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
	case outcome.Status == http.StatusUnauthorized:
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REJECTED", "signature is missing or invalid")
	case outcome.Status == http.StatusBadRequest:
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "delivery headers are missing or invalid")
	case outcome.Duplicate:
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
	default:
		for _, job := range outcome.Backlinks {
			go h.postBacklink(job)
		}
		for _, job := range outcome.IssueBacklinks {
			go h.postIssueBacklink(job)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": false})
	}
}

// runAsWorker 在组织 worker 身份的事务里执行 fn（Webhook 没有登录用户）。
func (h *Handler) runAsWorker(ctx context.Context, organizationID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	return h.pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

// postBacklink 在事务提交之后异步发 PR 回链评论：独立 context（15 秒），失败只记日志不重试，令牌失效时把连接标为需要重新授权。
func (h *Handler) postBacklink(job integrations.BacklinkJob) {
	ctx, cancel := context.WithTimeout(context.Background(), backlinkTimeout)
	defer cancel()
	var access integrations.GitAccess
	err := h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		access, err = h.integrations.BacklinkAccess(ctx, tx, job.OrganizationID, job.ConnectionID)
		return err
	})
	if err != nil {
		slog.Default().Warn("github_backlink_skipped", "connection_id", job.ConnectionID)
		return
	}
	commentID, err := h.integrations.PostBacklink(ctx, access, job)
	switch {
	case integrations.IsTokenRevoked(err):
		slog.Default().Warn("github_backlink_token_revoked", "connection_id", job.ConnectionID)
		if markErr := h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkTokenRevoked(ctx, tx, job.ConnectionID)
		}); markErr != nil {
			slog.Default().Warn("github_backlink_mark_failed", "connection_id", job.ConnectionID)
		}
	case err != nil:
		slog.Default().Warn("github_backlink_failed", "connection_id", job.ConnectionID, "error", err.Error())
	default:
		if recordErr := h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.RecordBacklink(ctx, tx, job, commentID)
		}); recordErr != nil {
			slog.Default().Warn("github_backlink_record_failed", "connection_id", job.ConnectionID)
		}
	}
}

// postIssueBacklink 与 postBacklink 相同，评论发在同步成任务的 Issue 下。
func (h *Handler) postIssueBacklink(job integrations.IssueBacklinkJob) {
	ctx, cancel := context.WithTimeout(context.Background(), backlinkTimeout)
	defer cancel()
	var access integrations.GitAccess
	err := h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		access, err = h.integrations.BacklinkAccess(ctx, tx, job.OrganizationID, job.ConnectionID)
		return err
	})
	if err != nil {
		slog.Default().Warn("github_issue_backlink_skipped", "connection_id", job.ConnectionID)
		return
	}
	commentID, err := h.integrations.PostIssueBacklink(ctx, access, job)
	switch {
	case integrations.IsTokenRevoked(err):
		slog.Default().Warn("github_issue_backlink_token_revoked", "connection_id", job.ConnectionID)
		_ = h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.MarkTokenRevoked(ctx, tx, job.ConnectionID)
		})
	case err != nil:
		slog.Default().Warn("github_issue_backlink_failed", "connection_id", job.ConnectionID, "error", err.Error())
	default:
		if recordErr := h.runAsWorker(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
			return h.integrations.RecordIssueBacklink(ctx, tx, job, commentID)
		}); recordErr != nil {
			slog.Default().Warn("github_issue_backlink_record_failed", "connection_id", job.ConnectionID)
		}
	}
}

// ---------- 任务的 Git 关联 ----------

func (h *Handler) listTaskGitLinks(w http.ResponseWriter, r *http.Request) {
	var links []integrations.TaskGitLink
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		var err error
		links, err = h.integrations.ListTaskGitLinks(ctx, tx, sub.spaceID, sub.taskID)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"links": links})
	}
}
