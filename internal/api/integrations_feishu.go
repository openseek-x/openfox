package api

// 飞书：组织应用绑定 / 更新凭证 / 重新验证 / 设置，以及「我的飞书账号」OAuth 绑定。
// 对飞书的请求都放在数据库事务之外：先短事务读状态与权限，再调用飞书，最后短事务保存。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

const feishuCallTimeout = 25 * time.Second

var feishuAppIDPattern = regexp.MustCompile(`^cli_[A-Za-z0-9]{8,40}$`)

// dingTalkAppKeyPattern 是钉钉 AppKey（Client ID）的格式：ding 开头加字母数字。
var dingTalkAppKeyPattern = regexp.MustCompile(`^ding[A-Za-z0-9]{8,40}$`)

// appIDValid 按服务商校验应用标识的格式。
func appIDValid(provider, appID string) bool {
	if provider == integrations.ProviderDingTalk {
		return dingTalkAppKeyPattern.MatchString(appID)
	}
	return feishuAppIDPattern.MatchString(appID)
}

type imProviderKey struct{}

// imRoute 把路径里固定的 IM 服务商放进请求上下文。服务商只来自 registerFeishuIntegrationRoutes 的白名单循环，
// 所以未知服务商（例如 /integrations/foo/app）没有路由，直接 404。
func imRoute(provider string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r.WithContext(context.WithValue(r.Context(), imProviderKey{}, provider)))
	}
}

// imProvider 返回当前请求的 IM 服务商（默认飞书）。
func imProvider(r *http.Request) string {
	if value, ok := r.Context().Value(imProviderKey{}).(string); ok && value != "" {
		return value
	}
	return integrations.ProviderFeishu
}

// imSvc 返回绑定到当前请求服务商的 Service。
func (h *Handler) imSvc(r *http.Request) integrations.Service {
	return h.integrations.For(imProvider(r))
}

// 飞书家族共用同一组 handler：服务商（feishu / lark）逐个注册字面量路径，避免与 /integrations/{provider}/... 的通配路由冲突。
func (h *Handler) registerFeishuIntegrationRoutes(mux *http.ServeMux) {
	for _, provider := range []string{integrations.ProviderFeishu, integrations.ProviderLark, integrations.ProviderDingTalk} {
		base := "/v1/organizations/{organizationID}/integrations/" + provider
		mux.HandleFunc("GET "+base+"/app", imRoute(provider, h.getFeishuApp))
		mux.HandleFunc("PUT "+base+"/app", imRoute(provider, h.putFeishuApp))
		mux.HandleFunc("POST "+base+"/app/verify", imRoute(provider, h.verifyFeishuApp))
		mux.HandleFunc("PATCH "+base+"/app/settings", imRoute(provider, h.patchFeishuAppSettings))
		mux.HandleFunc("POST "+base+"/authorize", imRoute(provider, h.startFeishuAuthorization))
		mux.HandleFunc("GET /v1/integrations/"+provider+"/callback", imRoute(provider, h.feishuOAuthCallback))
		h.registerFeishuBotRoutes(mux, provider)
	}
	h.registerFeishuBotRoutes(mux, integrations.ProviderWeCom)
}

func (h *Handler) recordIntegrationAudits(ctx context.Context, tx pgx.Tx, requestID string, call taskCall, audits []integrations.AuditEntry) error {
	for _, entry := range audits {
		if err := recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor, entry.Action,
			"integration_connection", entry.ResourceID, "", call.selected.Actor.AuthorizationVersion, entry.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) getFeishuApp(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view integrations.FeishuAppView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.imSvc(r).FeishuView(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) putFeishuApp(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
		Version   uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !appIDValid(imProvider(r), input.AppID) ||
		len(input.AppSecret) < 16 || len(input.AppSecret) > 128 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var plan integrations.FeishuPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.imSvc(r).FeishuPrepare(ctx, tx, call.organizationID, call.selected.Actor, input.AppID, input.Version)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	// 校验在事务之外：tenant_access_token → 机器人信息。
	validateCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	bot, err := h.imSvc(r).FeishuValidate(validateCtx, input.AppID, input.AppSecret)
	cancel()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var app integrations.FeishuApp
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		app, audits, err = h.imSvc(r).FeishuSave(ctx, tx, call.organizationID, call.selected.Actor, plan, input.AppID, input.AppSecret, bot)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) verifyFeishuApp(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	requestID := collaborationRequestID(w, r)
	var target integrations.FeishuVerifyTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		target, err = h.imSvc(r).FeishuVerifyPrepare(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	bot, validateErr := h.imSvc(r).FeishuValidate(validateCtx, target.Plan.AppID, target.Secret)
	cancel()
	var app integrations.FeishuApp
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		app, audits, err = h.imSvc(r).FeishuVerifyFinish(ctx, tx, call.organizationID, call.selected.Actor, target.Plan, bot, validateErr)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	// 失败结果（连接已标为 NEEDS_ATTENTION）在提交之后才返回 422 / 502。
	if validateErr != nil {
		writeIntegrationError(w, r, validateErr)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) patchFeishuAppSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		LinkPreviewEnabled *bool  `json:"link_preview_enabled"`
		Version            uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.LinkPreviewEnabled == nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var app integrations.FeishuApp
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		app, audits, err = h.imSvc(r).FeishuPatchSettings(ctx, tx, call.organizationID, call.selected.Actor, *input.LinkPreviewEnabled, input.Version)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) startFeishuAuthorization(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	requestID := collaborationRequestID(w, r)
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.imSvc(r).StartFeishuAuthorization(ctx, tx, call.organizationID, call.selected.Actor)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": imProvider(r), "result": "personal"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// feishuOAuthCallback 与 gitHubOAuthCallback 同样：校验 state 与会话用户 → 事务外换取身份 → 事务内建立 / 替换个人连接。
func (h *Handler) feishuOAuthCallback(w http.ResponseWriter, r *http.Request) {
	redirect := func(sessionID, reason string) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, h.oauthCompleteURLFor(imProvider(r), sessionID, reason), http.StatusFound)
	}
	query := r.URL.Query()
	state, code, oauthError := query.Get("state"), query.Get("code"), query.Get("error")
	auth, err := h.authenticate(r)
	if err != nil || state == "" || len(state) > 128 || h.pool == nil {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	var orgID, stateID, stateUser string
	found := false
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id::text, state_id::text, user_id::text FROM openfox_private.resolve_integration_oauth_state($1)`,
			integrations.TokenHash(state))
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			return rows.Scan(&orgID, &stateID, &stateUser)
		}
		return rows.Err()
	})
	if err != nil {
		slog.Default().Warn("feishu_oauth_state_lookup_failed")
		redirect("", integrations.ReasonInvalidState)
		return
	}
	if !found || stateUser != auth.principal.UserID {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, orgID)
	if err != nil {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: orgID, selected: selected}
	var outcome integrations.FeishuCallbackOutcome
	switch {
	case oauthError != "":
		outcome.Failure = integrations.ReasonAccessDenied
	case code == "" || len(code) > 512 || !h.imSvc(r).IMConfigured():
		outcome.Failure = integrations.ReasonExchangeFailed
	default:
		var target integrations.FeishuCallbackTarget
		err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			target, err = h.imSvc(r).FeishuCallbackPrepare(ctx, tx, orgID, auth.principal.UserID, stateID)
			return err
		})
		switch {
		case errors.Is(err, integrations.ErrOAuthSessionState):
			redirect("", integrations.ReasonInvalidState)
			return
		case errors.Is(err, integrations.ErrFeishuAppNotConnected):
			outcome.Failure = integrations.ReasonExchangeFailed
		case err != nil:
			slog.Default().Warn("feishu_oauth_prepare_failed", "session_id", stateID)
			redirect(stateID, integrations.ReasonExchangeFailed)
			return
		default:
			exchangeCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
			outcome = h.imSvc(r).FeishuExchange(exchangeCtx, target, code)
			cancel()
		}
	}
	requestID := collaborationRequestID(w, r)
	var result integrations.CallbackResult
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.imSvc(r).FeishuFinishCallback(ctx, tx, orgID, selected.Actor, stateID, outcome)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, result.Audit)
	})
	if errors.Is(err, integrations.ErrOAuthSessionState) {
		redirect("", integrations.ReasonInvalidState)
		return
	}
	if err != nil {
		slog.Default().Warn("feishu_oauth_finish_failed", "session_id", stateID)
		redirect(stateID, integrations.ReasonExchangeFailed)
		return
	}
	redirect(result.SessionID, result.Reason)
}
