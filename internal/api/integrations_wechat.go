package api

// -W · 微信（腾讯 ClawBot / iLink）扫码绑定 Agent。
// 对 iLink 的请求都放在数据库事务之外（见 integrations.Service.WeChatQRAdvance）。

import (
	"context"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

var wechatVerifyCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)

func (h *Handler) registerWeChatRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/wechat"
	mux.HandleFunc("GET "+base+"/bindings", h.listWeChatBindings)
	mux.HandleFunc("POST "+base+"/bindings/{bindingID}/remove", h.removeWeChatBinding)
	mux.HandleFunc("POST "+base+"/agent-bindings/qr", h.startWeChatQR)
	mux.HandleFunc("GET "+base+"/agent-bindings/qr/{sessionID}", h.getWeChatQR)
	mux.HandleFunc("POST "+base+"/agent-bindings/qr/{sessionID}/verify", h.verifyWeChatQR)
	mux.HandleFunc("POST "+base+"/agent-bindings/qr/{sessionID}/cancel", h.cancelWeChatQR)
}

func (h *Handler) listWeChatBindings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view integrations.WeChatView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = h.integrations.WeChatView(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// wechatRunner 把 taskCall.run 适配成服务层的事务执行器。
func (h *Handler) wechatRunner(call taskCall, r *http.Request) integrations.TxRunner {
	return func(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
		return call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error { return fn(ctx, tx) })
	}
}

func (h *Handler) startWeChatQR(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.AgentID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	qr, err := h.integrations.WeChatQRStart(r.Context(), h.wechatRunner(call, r), call.organizationID, call.selected.Actor, input.AgentID)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, qr)
}

// advanceWeChatQR 查询一次扫码状态（可带验证码）并推进会话；确认时在同一事务里写审计。
func (h *Handler) advanceWeChatQR(w http.ResponseWriter, r *http.Request, call taskCall, verifyCode string) {
	requestID := collaborationRequestID(w, r)
	qr, err := h.integrations.WeChatQRAdvance(r.Context(), h.wechatRunner(call, r), call.organizationID, call.selected.Actor,
		r.PathValue("sessionID"), verifyCode,
		func(ctx context.Context, tx pgx.Tx, entries []integrations.AuditEntry) error {
			return h.recordIntegrationAudits(ctx, tx, requestID, call, entries)
		})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, qr)
}

// getWeChatQR 是前端的轮询入口：每次调用向 iLink 查询一次状态（服务端长轮询，最多约 25 秒）。只有发起人能查询。
func (h *Handler) getWeChatQR(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	h.advanceWeChatQR(w, r, call, "")
}

func (h *Handler) verifyWeChatQR(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !wechatVerifyCodePattern.MatchString(input.Code) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	h.advanceWeChatQR(w, r, call, input.Code)
}

func (h *Handler) cancelWeChatQR(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var qr integrations.WeChatQR
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		qr, err = h.integrations.WeChatQRCancel(ctx, tx, call.selected.Actor, r.PathValue("sessionID"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, qr)
}

func (h *Handler) removeWeChatBinding(w http.ResponseWriter, r *http.Request) {
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
		audits, err := h.integrations.WeChatRemove(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("bindingID"), input.Version)
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
