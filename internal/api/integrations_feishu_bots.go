package api

// 飞书任务助手、绑定 Agents 的扫码会话与机器人管理。
// 对飞书的请求都放在数据库事务之外：先短事务校验，再调用飞书，最后短事务保存。

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) registerFeishuBotRoutes(mux *http.ServeMux, provider string) {
	base := "/v1/organizations/{organizationID}/integrations/" + provider
	mux.HandleFunc("GET "+base+"/bots", imRoute(provider, h.listFeishuBots))
	if provider == integrations.ProviderWeCom {
		mux.HandleFunc("POST "+base+"/bots", imRoute(provider, h.createWeComBot))
		mux.HandleFunc("GET "+base+"/personal", imRoute(provider, h.getWeComPersonal))
		mux.HandleFunc("POST "+base+"/personal/codes", imRoute(provider, h.issueWeComBindingCode))
		mux.HandleFunc("DELETE "+base+"/personal/{botID}", imRoute(provider, h.unbindWeComPersonal))
		mux.HandleFunc("PUT "+base+"/personal/notification-target", imRoute(provider, h.setWeComNotificationTarget))
	} else if provider == integrations.ProviderDingTalk {
		// 钉钉官方没有扫码创建应用的接口：任务助手 / Agent 机器人用 AppKey + AppSecret 直接连接。
		mux.HandleFunc("POST "+base+"/bots", imRoute(provider, h.createDingTalkBot))
	} else {
		mux.HandleFunc("POST "+base+"/registrations", imRoute(provider, h.startFeishuRegistration))
		mux.HandleFunc("GET "+base+"/registrations/{registrationID}", imRoute(provider, h.getFeishuRegistration))
		mux.HandleFunc("POST "+base+"/registrations/{registrationID}/cancel", imRoute(provider, h.cancelFeishuRegistration))
	}
	mux.HandleFunc("PATCH "+base+"/bots/{botID}", imRoute(provider, h.updateFeishuBot))
	mux.HandleFunc("POST "+base+"/bots/{botID}/remove", imRoute(provider, h.removeFeishuBot))
	mux.HandleFunc("POST "+base+"/bots/{botID}/retry", imRoute(provider, h.retryFeishuBot))
}

func (h *Handler) getWeComPersonal(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view integrations.WeComPersonalView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.imSvc(r).WeComPersonal(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) issueWeComBindingCode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		BotID string `json:"bot_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var code integrations.WeComBindingCode
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		code, err = h.imSvc(r).IssueWeComBindingCode(ctx, tx, call.organizationID, call.selected.Actor, input.BotID)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, code)
}

func (h *Handler) unbindWeComPersonal(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.imSvc(r).UnbindWeCom(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"))
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setWeComNotificationTarget(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		BotID json.RawMessage `json:"bot_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil || len(input.BotID) == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var botID string
	if string(input.BotID) != "null" && json.Unmarshal(input.BotID, &botID) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var view integrations.WeComPersonalView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.imSvc(r).SetWeComNotificationTarget(ctx, tx, call.organizationID, call.selected.Actor, botID)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) createWeComBot(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		integrations.RegistrationInput
		BotID  string `json:"bot_id"`
		Secret string `json:"secret"`
	}
	if err := decodeJSON(w, r, &input); err != nil || len(input.BotID) < 1 || len(input.BotID) > 128 ||
		len(input.Secret) < 8 || len(input.Secret) > 512 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var plan integrations.RegistrationPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.imSvc(r).RegistrationPrepare(ctx, tx, call.organizationID, call.selected.Actor, input.RegistrationInput)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	_, err = h.imSvc(r).FeishuValidate(validateCtx, input.BotID, input.Secret)
	cancel()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var bot integrations.FeishuBot
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		bot, audits, err = h.imSvc(r).CreateCredentialBot(ctx, tx, call.organizationID, call.selected.Actor, plan, input.BotID, input.Secret)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, bot)
}

func (h *Handler) listFeishuBots(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view integrations.FeishuBotsView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.imSvc(r).BotsView(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) startFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.RegistrationInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var plan integrations.RegistrationPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.imSvc(r).RegistrationPrepare(ctx, tx, call.organizationID, call.selected.Actor, input)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	beginCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	begin, err := h.imSvc(r).RegistrationBegin(beginCtx)
	cancel()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var registration integrations.FeishuRegistration
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		registration, audits, err = h.imSvc(r).RegistrationSave(ctx, tx, call.organizationID, call.selected.Actor, plan, begin)
		if err != nil {
			return err
		}
		for _, entry := range audits {
			if err := recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor, entry.Action,
				"integration_oauth_session", entry.ResourceID, "", call.selected.Actor.AuthorizationVersion, entry.Metadata); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, registration)
}

func (h *Handler) getFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var registration integrations.FeishuRegistration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		registration, err = h.imSvc(r).RegistrationGet(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("registrationID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, registration)
}

func (h *Handler) cancelFeishuRegistration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var registration integrations.FeishuRegistration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		registration, err = h.imSvc(r).RegistrationCancel(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("registrationID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, registration)
}

func (h *Handler) updateFeishuBot(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Runtime     *integrations.FeishuAssistantRuntime `json:"runtime"`
		ExtraPrompt *string                              `json:"extra_prompt"`
		Version     uint64                               `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var bot integrations.FeishuBot
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		bot, audits, err = h.imSvc(r).UpdateBot(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"),
			integrations.BotUpdateInput{Runtime: input.Runtime, ExtraPrompt: input.ExtraPrompt, Version: input.Version})
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bot)
}

func (h *Handler) removeFeishuBot(w http.ResponseWriter, r *http.Request) {
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
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		audits, err := h.imSvc(r).RemoveBot(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), input.Version)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) retryFeishuBot(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	requestID := collaborationRequestID(w, r)
	var target integrations.BotRetryTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		target, err = h.imSvc(r).BotRetryPrepare(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	bot, validateErr := h.imSvc(r).FeishuValidate(validateCtx, target.AppID, target.Secret)
	cancel()
	var view integrations.FeishuBot
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		view, audits, err = h.imSvc(r).BotRetryFinish(ctx, tx, call.organizationID, call.selected.Actor, target, bot, validateErr)
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
	writeJSON(w, http.StatusOK, view)
}

// createDingTalkBot 用 AppKey + AppSecret 直接连接钉钉机器人（任务助手 / Agent）。钉钉官方没有扫码创建应用的接口，
// 所以这里取代飞书的「扫码会话」：准备（短事务）→ 事务外换 accessToken 校验 → 保存（发起人身份的短事务）。
func (h *Handler) createDingTalkBot(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		integrations.RegistrationInput
		AppKey    string `json:"app_key"`
		AppSecret string `json:"app_secret"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !appIDValid(imProvider(r), input.AppKey) ||
		len(input.AppSecret) < 16 || len(input.AppSecret) > 128 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var plan integrations.RegistrationPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.imSvc(r).RegistrationPrepare(ctx, tx, call.organizationID, call.selected.Actor, input.RegistrationInput)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), feishuCallTimeout)
	_, err = h.imSvc(r).FeishuValidate(validateCtx, input.AppKey, input.AppSecret)
	cancel()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var bot integrations.FeishuBot
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var audits []integrations.AuditEntry
		var err error
		bot, audits, err = h.imSvc(r).CreateCredentialBot(ctx, tx, call.organizationID, call.selected.Actor, plan, input.AppKey, input.AppSecret)
		if err != nil {
			return err
		}
		return h.recordIntegrationAudits(ctx, tx, requestID, call, audits)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, bot)
}
