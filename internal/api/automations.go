package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/automations"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/platform/ratelimit"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 自动化接口：自动化 CRUD 与重排、立即运行与运行记录、Webhook URL / 签名密钥、投递记录与重放，
// 以及公开的 Webhook 接收端。

const (
	automationBase = "/v1/organizations/{organizationID}/spaces/{spaceID}/automations"
	// automationBodyLimit 放宽到 256 KB，容纳 20000 字提示词（多字节字符）与触发器配置。
	automationBodyLimit = 256 << 10
	webhookRatePerMin   = 60
)

func (h *Handler) registerAutomationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+automationBase, h.listAutomations)
	mux.HandleFunc("POST "+automationBase, h.createAutomation)
	mux.HandleFunc("PUT "+automationBase+"/reorder", h.reorderAutomations)
	mux.HandleFunc("GET "+automationBase+"/{automationID}", h.getAutomation)
	mux.HandleFunc("PATCH "+automationBase+"/{automationID}", h.updateAutomation)
	mux.HandleFunc("DELETE "+automationBase+"/{automationID}", h.deleteAutomation)
	mux.HandleFunc("POST "+automationBase+"/{automationID}/run", h.runAutomation)
	mux.HandleFunc("GET "+automationBase+"/{automationID}/runs", h.listAutomationRuns)
	mux.HandleFunc("GET "+automationBase+"/{automationID}/runs/{runID}", h.getAutomationRun)
	mux.HandleFunc("POST "+automationBase+"/{automationID}/triggers/{triggerID}/webhook-url", h.regenerateWebhookURL)
	mux.HandleFunc("GET "+automationBase+"/{automationID}/triggers/{triggerID}/signing-secret", h.getWebhookSigningSecret)
	mux.HandleFunc("POST "+automationBase+"/{automationID}/triggers/{triggerID}/signing-secret", h.resetWebhookSigningSecret)
	mux.HandleFunc("GET "+automationBase+"/{automationID}/deliveries", h.listWebhookDeliveries)
	mux.HandleFunc("GET "+automationBase+"/{automationID}/deliveries/{deliveryID}", h.getWebhookDelivery)
	mux.HandleFunc("POST "+automationBase+"/{automationID}/deliveries/{deliveryID}/replay", h.replayWebhookDelivery)
	// 接收端不需要登录；令牌只存哈希，访问日志不记录路径。
	mux.HandleFunc("POST /v1/hooks/{token}", h.receiveWebhook)
}

// automationService 返回带密钥箱与公开地址的自动化服务。
func (h *Handler) automationService() automations.Service {
	return automations.Service{Box: h.secrets, PublicURL: h.publicURL, Now: time.Now}
}

// canEditSpace 与写接口使用同一套授权判断，结果写入自动化的 can_edit。
func (c taskCall) canEditSpace(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string) (bool, error) {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return false, err
	}
	decision := collaborationSpaceDecision(policy.ActionUpdateObject, c.organizationID, c.selected, access, "")
	return decision.Allowed() && access.RequireWritable() == nil, nil
}

// requester 返回手动运行时的操作者快照。
func (c taskCall) requester() conversations.Actor {
	return conversations.Actor{
		UserID:               c.selected.Actor.UserID,
		Role:                 c.selected.Actor.Role,
		AuthorizationVersion: c.selected.Actor.AuthorizationVersion,
	}
}

// decodeAutomationJSON 与 decodeJSON 一样拒绝未知字段，但请求体上限为 256 KB。
func decodeAutomationJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, automationBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (h *Handler) listAutomations(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var items []automations.Automation
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		canEdit, err := call.canEditSpace(ctx, tx, service, spaceID)
		if err != nil {
			return err
		}
		items, err = h.automationService().List(ctx, tx, spaceID, canEdit)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": items})
}

func (h *Handler) createAutomation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var input automations.AutomationInput
	if err := decodeAutomationJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var result automations.SaveResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		// 对象类授权需要对象 ID：新建前先生成自动化 ID。
		automationID, err := automations.NewID()
		if err != nil {
			return err
		}
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionCreateObject, automationID); err != nil {
			return err
		}
		result, err = h.automationService().Create(ctx, tx, spaceID, automations.Actor{UserID: call.selected.Actor.UserID}, input)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) reorderAutomations(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var body struct {
		AutomationIDs []string `json:"automation_ids"`
	}
	if err := decodeAutomationJSON(w, r, &body); err != nil || body.AutomationIDs == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "automation_ids is required")
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		// 重排作用于整个空间，以空间 ID 作为对象标识。
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, spaceID); err != nil {
			return err
		}
		return h.automationService().Reorder(ctx, tx, spaceID, body.AutomationIDs)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getAutomation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	var item automations.Automation
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		canEdit, err := call.canEditSpace(ctx, tx, service, spaceID)
		if err != nil {
			return err
		}
		item, err = h.automationService().Get(ctx, tx, spaceID, automationID, canEdit)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updateAutomation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	var input automations.UpdateInput
	if err := decodeAutomationJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var result automations.SaveResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		result, err = h.automationService().Update(ctx, tx, spaceID, automationID, automations.Actor{UserID: call.selected.Actor.UserID}, input)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) deleteAutomation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	version, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("version")), 10, 64)
	if err != nil || version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionDeleteObject, automationID); err != nil {
			return err
		}
		return h.automationService().Delete(ctx, tx, spaceID, automationID, version)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) runAutomation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	var run automations.Run
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		run, err = h.automationService().RunManually(ctx, tx, call.organizationID, spaceID, automationID, call.requester())
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *Handler) listAutomationRuns(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	var runs []automations.Run
	var next string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		runs, next, err = h.automationService().ListRuns(ctx, tx, spaceID, automationID, r.URL.Query().Get("cursor"))
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "next_cursor": next})
}

func (h *Handler) getAutomationRun(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID, runID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("runID")
	var detail automations.RunDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		detail, err = h.automationService().GetRun(ctx, tx, spaceID, automationID, runID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) regenerateWebhookURL(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID, triggerID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("triggerID")
	var url string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		url, err = h.automationService().RegenerateWebhookURL(ctx, tx, spaceID, automationID, triggerID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (h *Handler) getWebhookSigningSecret(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID, triggerID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("triggerID")
	var secret string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		secret, err = h.automationService().GetSigningSecret(ctx, tx, call.organizationID, spaceID, automationID, triggerID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (h *Handler) resetWebhookSigningSecret(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID, triggerID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("triggerID")
	var secret string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		secret, err = h.automationService().ResetSigningSecret(ctx, tx, call.organizationID, spaceID, automationID, triggerID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (h *Handler) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID := r.PathValue("spaceID"), r.PathValue("automationID")
	var deliveries []automations.Delivery
	var next string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		deliveries, next, err = h.automationService().ListDeliveries(ctx, tx, spaceID, automationID, r.URL.Query().Get("cursor"))
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries, "next_cursor": next})
}

func (h *Handler) getWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, automationID, deliveryID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("deliveryID")
	var detail automations.DeliveryDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		detail, err = h.automationService().GetDelivery(ctx, tx, spaceID, automationID, deliveryID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) replayWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, automationID, deliveryID := r.PathValue("spaceID"), r.PathValue("automationID"), r.PathValue("deliveryID")
	var delivery automations.Delivery
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, automationID); err != nil {
			return err
		}
		var err error
		delivery, err = h.automationService().ReplayDelivery(ctx, tx, call.organizationID, spaceID, automationID, deliveryID, call.requester())
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, delivery)
}

// receiveWebhook 是公开的 Webhook 接收端：先限流（按令牌哈希，每分钟 60 次），再限制请求体（256 KB），
// 然后按令牌找到组织与触发器，在组织事务里处理。令牌与密钥不写入日志与错误信息。
func (h *Handler) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" || len(token) > 128 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
		return
	}
	tokenHash := automations.TokenHash(token)
	if !h.allowKey(w, r, h.webhookLimiter, hex.EncodeToString(tokenHash)) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, automations.WebhookBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "webhook body exceeds 256 KB")
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var lookup automations.WebhookLookup
	found := false
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id::text, automation_id::text, trigger_id::text FROM openfox_private.resolve_webhook_trigger($1)`, tokenHash)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			return rows.Scan(&lookup.OrganizationID, &lookup.AutomationID, &lookup.TriggerID)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
		return
	}
	inbound := automations.Inbound{TokenHash: tokenHash, Header: r.Header, Body: body, SourceIP: ratelimit.ClientIP(r, h.trustedProxies)}
	var outcome automations.Outcome
	selection := tenancy.OrganizationSelection{OrganizationID: lookup.OrganizationID, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	err = h.pool.RunForOrganization(r.Context(), lookup.OrganizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			outcome, err = h.automationService().Receive(ctx, tx, lookup, inbound, time.Now())
			return err
		})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if outcome.Pending != nil {
		h.startWebhookRun(r, lookup, *outcome.Pending)
	}
	writeWebhookOutcome(w, r, outcome)
}

// startWebhookRun 在创建者身份的事务里为已派发的投递启动运行。投递已经记录，启动失败只记日志；
// 之后可以通过重放重新启动。日志只包含投递与自动化 ID，不包含令牌或请求内容。
func (h *Handler) startWebhookRun(r *http.Request, lookup automations.WebhookLookup, pending automations.PendingRun) {
	err := h.pool.RunForOrganization(r.Context(), lookup.OrganizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return dispatch.CreatorSelection(lookup.OrganizationID, pending), nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			return h.automationService().ApplyWebhookRun(ctx, tx, lookup.OrganizationID, pending)
		})
	if err != nil {
		slog.Default().Warn("webhook_run_start_failed", "automation_id", lookup.AutomationID, "delivery_id", pending.Source.Payload["delivery_id"], "error", err.Error())
	}
}

// writeWebhookOutcome 把接收结果写成响应；拒绝类响应只给错误码，不回显请求内容。
func writeWebhookOutcome(w http.ResponseWriter, r *http.Request, outcome automations.Outcome) {
	if !outcome.Found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "webhook is not available")
		return
	}
	switch outcome.HTTPStatus {
	case http.StatusForbidden:
		writeError(w, r, http.StatusForbidden, "IP_NOT_ALLOWED", "source address is not allowed")
	case http.StatusUnauthorized:
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REJECTED", "signature is missing, invalid, or expired")
	default:
		writeJSON(w, outcome.HTTPStatus, map[string]string{"delivery_id": outcome.Delivery.ID, "status": outcome.Delivery.Status})
	}
}
