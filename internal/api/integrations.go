package api

// 外部应用集成：服务商总览、详情与断开连接。所有成员可读（与组织设置一致，GUEST 也可读）。

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

var integrationProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

func (h *Handler) registerIntegrationRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations"
	mux.HandleFunc("GET "+base, h.listIntegrations)
	mux.HandleFunc("GET "+base+"/{provider}", h.getIntegration)
	mux.HandleFunc("POST "+base+"/connections/{connectionID}/disconnect", h.disconnectIntegrationConnection)
	mux.HandleFunc("POST "+base+"/{provider}/instance-token", h.bindProviderToken)
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/shortcut-webhook", h.shortcutHookSettings)
	mux.HandleFunc("PUT "+base+"/connections/{connectionID}/shortcut-webhook", h.shortcutHookSettings)
	mux.HandleFunc("POST /v1/hooks/shortcut/{organizationID}/{connectionID}", h.receiveShortcutHook)
	mux.HandleFunc("PUT "+base+"/site-personal", h.bindSitePersonal)
	mux.HandleFunc("PUT "+base+"/pingcode-personal", h.bindPingCodePersonal)
	h.registerGitHubIntegrationRoutes(mux)
	h.registerFeishuIntegrationRoutes(mux)
	h.registerWeChatRoutes(mux)
}

func (h *Handler) listIntegrations(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item integrations.Overview
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.Overview(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) getIntegration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	if !integrationProviderPattern.MatchString(provider) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
		return
	}
	var item integrations.Detail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		item, err = h.integrations.Detail(ctx, tx, call.organizationID, call.selected.Actor, provider)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type disconnectIntegrationRequest struct {
	Version       uint64 `json:"version"`
	ConfirmPhrase string `json:"confirm_phrase"`
}

func (h *Handler) disconnectIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input disconnectIntegrationRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Version == 0 || len(input.ConfirmPhrase) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	connectionID := r.PathValue("connectionID")
	var result integrations.DisconnectResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.Disconnect(ctx, tx, call.organizationID, call.selected.Actor, connectionID, input.Version, input.ConfirmPhrase)
		if err != nil || !result.Changed {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"integration.connection_disconnected", "integration_connection", result.Connection.ID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"source": result.Connection.Provider, "result": result.Connection.Scope})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result.Connection)
}

func writeIntegrationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, integrations.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, integrations.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "only organization owners and admins can manage integrations")
	case errors.Is(err, integrations.ErrVersionConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "connection changed; refresh and retry")
	case errors.Is(err, integrations.ErrConfirmationMismatch):
		writeError(w, r, http.StatusBadRequest, "CONFIRMATION_MISMATCH", "confirmation phrase does not match")
	case errors.Is(err, integrations.ErrNotConfigured):
		writeError(w, r, http.StatusConflict, "INTEGRATION_NOT_CONFIGURED", "the server has not configured this integration")
	case errors.Is(err, integrations.ErrSecretsUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "server encryption key is not configured")
	case errors.Is(err, integrations.ErrOAuthSessionState):
		writeError(w, r, http.StatusConflict, "OAUTH_SESSION_STATE", "authorization session cannot be completed in its current state")
	case errors.Is(err, integrations.ErrNeedsReauth):
		writeError(w, r, http.StatusConflict, "INTEGRATION_NEEDS_REAUTH", "the provider authorization is no longer valid; reauthorize the connection")
	case errors.Is(err, integrations.ErrUpstreamUnavailable):
		writeError(w, r, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "the provider is temporarily unavailable")
	case errors.Is(err, integrations.ErrInvalidRepository):
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY", "repository is not accessible or not owned by the connection account")
	case errors.Is(err, integrations.ErrSyncLinkExists):
		writeError(w, r, http.StatusConflict, "SYNC_LINK_EXISTS", "repository is already linked to a space")
	case errors.Is(err, integrations.ErrSyncBidirectionalExists):
		writeError(w, r, http.StatusConflict, "SYNC_BIDIRECTIONAL_EXISTS", "space already has a bidirectional sync")
	case errors.Is(err, integrations.ErrFeishuCredentialsInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "FEISHU_CREDENTIALS_INVALID", "feishu rejected the App ID or App Secret")
	case errors.Is(err, integrations.ErrFeishuBotUnavailable):
		writeError(w, r, http.StatusUnprocessableEntity, "FEISHU_BOT_UNAVAILABLE", "the feishu app has no bot; enable the bot capability and publish the app")
	case errors.Is(err, integrations.ErrFeishuUnavailable):
		writeError(w, r, http.StatusBadGateway, "FEISHU_UNAVAILABLE", "feishu is temporarily unavailable")
	case errors.Is(err, integrations.ErrFeishuAppInUse):
		writeError(w, r, http.StatusConflict, "FEISHU_APP_IN_USE", "this feishu app is already connected to another organization")
	case errors.Is(err, integrations.ErrFeishuAppMismatch):
		writeError(w, r, http.StatusConflict, "FEISHU_APP_MISMATCH", "the App ID differs from the connected app; disconnect it first")
	case errors.Is(err, integrations.ErrFeishuAppNotConnected):
		writeError(w, r, http.StatusConflict, "FEISHU_APP_NOT_CONNECTED", "the organization has no active feishu app")
	case errors.Is(err, integrations.ErrFeishuAssistantExists):
		writeError(w, r, http.StatusConflict, "FEISHU_ASSISTANT_EXISTS", "the space already has a feishu task assistant")
	case errors.Is(err, integrations.ErrFeishuAgentConnected):
		writeError(w, r, http.StatusConflict, "FEISHU_AGENT_CONNECTED", "the agent is already connected to feishu")
	case errors.Is(err, integrations.ErrWeChatLimit):
		writeError(w, r, http.StatusConflict, "WECHAT_LIMIT_REACHED", "too many wechat bindings; remove one first")
	case errors.Is(err, integrations.ErrWeChatUnavailable):
		writeError(w, r, http.StatusBadGateway, "WECHAT_UNAVAILABLE", "wechat is temporarily unavailable")
	case errors.Is(err, integrations.ErrInvalidInstance):
		writeError(w, r, http.StatusBadRequest, "INVALID_INSTANCE_URL", "instance url must be a public https address")
	case errors.Is(err, integrations.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	default:
		writeCollaborationError(w, r, err)
	}
}
