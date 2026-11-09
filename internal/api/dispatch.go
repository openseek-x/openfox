package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/executiondelivery"
	"openfox/internal/integrations"
	"openfox/internal/integrations/dingtalk"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/github"
	"openfox/internal/integrations/wechat"
	"openfox/internal/integrations/wecom"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
)

// DispatchOptions 配置中心派发相关的设备端点与 WSS 唤醒。
// Hub 为空时端点仍可用，只是不推送唤醒，设备依赖 offer 轮询。
type DispatchOptions struct {
	Store dispatch.Store
	Hub   *dispatch.Hub
	// Secrets 加密落库的敏感配置（Agent 环境变量）；为 nil 时相关接口返回 409 SECRETS_UNAVAILABLE。
	Secrets *secrets.Box
	// PublicURL 是中心对外地址（OPENFOX_SERVER_PUBLIC_URL），写进 v2 安装令牌；为空时用开发默认值。
	PublicURL string
	// GitHubConfigured 表示服务端已配置 GitHub OAuth App，用于集成总览展示。
	GitHubConfigured bool
	// GitHubClient 是 GitHub OAuth / API 客户端；未配置时为 nil。
	GitHubClient *github.Client
	// GiteeConfigured 表示服务端已配置 Gitee 第三方应用；Gitee 客户端由 GitFactory（integrations.WithGitee）提供。
	GiteeConfigured bool
	// GitFactory / GitValidateInstance 是 GitLab（自部署 / 极狐）的客户端工厂和实例地址校验；
	// 为 nil 时这些服务商不可用（409 INTEGRATION_NOT_CONFIGURED）。
	GitFactory          integrations.GitClientFactory
	GitValidateInstance func(ctx context.Context, rawURL string) (string, error)
	// FeishuClient 是飞书开放平台客户端；为 nil 时飞书应用校验与授权返回 409 INTEGRATION_NOT_CONFIGURED。
	FeishuClient *feishu.Client
	// LarkClient 是 Lark 开放平台客户端；为 nil 时 Lark 的应用校验与授权返回 409 INTEGRATION_NOT_CONFIGURED。
	LarkClient *feishu.Client
	// DingTalkClient 是钉钉开放平台客户端；为 nil 时钉钉的应用校验与授权返回 409 INTEGRATION_NOT_CONFIGURED。
	DingTalkClient *dingtalk.Client
	// WeChatClient 是腾讯 ClawBot（iLink）客户端；为 nil 时微信扫码绑定返回 409 INTEGRATION_NOT_CONFIGURED。
	WeChatClient *wechat.Client
	WeComClient  *wecom.Client
	// ImportSafeHTTP 是 Jira API 导入出站请求的防护配置，零值即生产的严格配置（只测试注入放行地址与根证书）。
	ImportSafeHTTP safehttp.Options
}

const maxDeviceOffers = 50

func (h *Handler) registerDispatchRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("PUT /v1/device/organizations/{organizationID}/mcp-inventory", h.reportDeviceMCPInventory)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/offers", h.listDeviceOffers)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/offers/{offerID}/claim", h.claimDeviceOffer)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/runs/{runID}/context", h.getRunContext)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/start", h.startDeviceRun)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/lease", h.renewDeviceRun)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/prepare-failed", h.prepareFailedDeviceRun)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/tools/{tool}", h.callDeviceRunTool)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/runs/{runID}/cancel", h.cancelRun)
}

type deviceGenerationRequest struct {
	Generation int64 `json:"generation"`
}

type deviceLeaseRequest struct {
	Generation int64  `json:"generation"`
	Challenge  string `json:"challenge"`
}

type devicePrepareFailedRequest struct {
	Generation  int64  `json:"generation"`
	FailureCode string `json:"failure_code"`
	Detail      string `json:"detail"`
}

func (h *Handler) listDeviceOffers(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var offers []dispatch.Offer
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		offers, err = h.dispatch.ListOffers(ctx, tx, selection.OrganizationID, principal.DeviceID, maxDeviceOffers)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	w.Header().Set("X-OpenFox-MCP-Inventory-Private", "2")
	writeJSON(w, http.StatusOK, map[string]any{"offers": offers})
}

func (h *Handler) claimDeviceOffer(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var lease dispatch.Lease
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		lease, err = h.dispatch.Claim(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("offerID"))
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (h *Handler) getRunContext(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	generation, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("generation")), 10, 64)
	if err != nil || generation < 1 {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	var result dispatch.RunContext
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.dispatch.ContextForDevice(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), generation, h.secrets, r.Header.Get("X-OpenFox-Agent-MCP") == "1")
		if err != nil {
			return err
		}
		// 环境变量在请求时解密（不固定到配置版本），只写进这次响应；不进日志、审计与事件。
		result.Environment, err = dispatch.LoadRunEnvironment(ctx, tx, h.secrets, selection.OrganizationID, result.AgentID)
		if err != nil {
			return err
		}
		for i := range result.Repositories {
			if result.Repositories[i].ConnectionID == "" {
				continue
			}
			if r.Header.Get("X-OpenFox-Repository-Auth") != "1" {
				return dispatch.ErrInvalidRequest
			}
			result.Repositories[i].Authorization, err = h.integrations.RepositoryAuthorization(ctx, tx, selection.OrganizationID, principal.DeviceID, result.Repositories[i])
			if err != nil {
				return dispatch.ErrAuthorizationRevoked
			}
		}
		return nil
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	// 响应含明文环境变量，禁止任何中间层缓存。
	w.Header().Set("Cache-Control", "no-store")
	if result.WorkspaceSelection != nil && r.Header.Get("X-OpenFox-Conversation-Workdir") != "1" {
		writeError(w, r, http.StatusConflict, "WORKSPACE_SELECTION_UNSUPPORTED", "update OpenFox on this computer to use the conversation's selected directory")
		return
	}
	if result.SharedTaskCrewID != "" && r.URL.Query().Get("shared_task_dir") != "1" {
		writeError(w, r, http.StatusConflict, "WORKSPACE_SHARING_UNSUPPORTED", "update OpenFox on this computer to use the crew's shared task directory")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) startDeviceRun(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input deviceGenerationRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	var lease dispatch.Lease
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		lease, err = h.dispatch.Start(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), input.Generation)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// maxToolCallBody 是工具调用请求体上限（评论正文 ≤ 10000 字符，留出 JSON 与转义开销）。
const maxToolCallBody = 64 * 1024

// callDeviceRunTool 执行 Agent 任务工具（见 dispatch/task_tools.go）：只作用于本次运行绑定的任务。
func (h *Handler) callDeviceRunTool(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxToolCallBody)
	var input dispatch.ToolCall
	if err := decodeDeliveryJSON(w, r, &input); err != nil || input.Generation < 1 {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	// 任务助手的空间任务工具在发起人身份的事务里执行（见 space_tools.go）。
	if tool := r.PathValue("tool"); strings.HasPrefix(tool, "space_task_") {
		h.callSpaceTool(w, r, principal.DeviceID, selection, tool, input)
		return
	}
	var result dispatch.ToolResult
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.dispatch.CallTool(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), r.PathValue("tool"), input)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) renewDeviceRun(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input deviceLeaseRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	var lease dispatch.Lease
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		lease, err = h.dispatch.Renew(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), input.Generation, input.Challenge)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (h *Handler) prepareFailedDeviceRun(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input devicePrepareFailedRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	var run execution.Run
	var detail executiondelivery.RunDetail
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = h.dispatch.PrepareFailed(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"),
			input.Generation, input.FailureCode, input.Detail)
		if err != nil {
			return err
		}
		detail, err = h.runDetail(ctx, tx, run.ID)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveryRunResponseFrom(run, detail))
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if !requireConversationCSRF(w, r, auth) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "an Idempotency-Key header is required")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var outcome dispatch.CancelOutcome
	var detail executiondelivery.RunDetail
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		outcome, err = h.dispatch.Cancel(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor, key)
		if err != nil {
			return err
		}
		detail, err = h.runDetail(ctx, tx, outcome.Run.ID)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	if outcome.NotifyDevice {
		// 提交后再唤醒；投递失败不影响结果，设备下一次续租会看到 cancel_requested。
		h.dispatchHub.PublishCancel(outcome.Run.ComputerID, outcome.Run.OrganizationID, outcome.Run.ID, outcome.Run.Generation)
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": deliveryRunResponseFrom(outcome.Run, detail), "replayed": outcome.Replayed})
}

func writeDispatchError(w http.ResponseWriter, r *http.Request, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, dispatch.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	case errors.Is(err, dispatch.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, dispatch.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "action is not allowed")
	case errors.Is(err, dispatch.ErrAuthorizationRevoked):
		writeError(w, r, http.StatusForbidden, "AUTHORIZATION_REVOKED", "the Run authorization is no longer valid")
	case errors.Is(err, dispatch.ErrOfferUnavailable):
		writeError(w, r, http.StatusConflict, "OFFER_UNAVAILABLE", "the offer can no longer be claimed")
	case errors.Is(err, dispatch.ErrGenerationConflict):
		writeError(w, r, http.StatusConflict, "GENERATION_CONFLICT", "the Run generation is stale")
	case errors.Is(err, dispatch.ErrCancelRequested):
		writeError(w, r, http.StatusConflict, "CANCEL_REQUESTED", "the Run was canceled before start")
	case errors.Is(err, dispatch.ErrReconciliationRequired):
		writeError(w, r, http.StatusConflict, "RECONCILIATION_REQUIRED", "the Run requires process reconciliation")
	case errors.Is(err, dispatch.ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "the idempotency key was used for a different request")
	case errors.Is(err, dispatch.ErrMCPUnsupported):
		writeError(w, r, http.StatusConflict, "MCP_UNSUPPORTED", "update OpenFox on this computer to use the configured MCP servers")
	case errors.Is(err, dispatch.ErrSecretsUnavailable):
		writeError(w, r, http.StatusConflict, "SECRETS_UNAVAILABLE", "agent environment variables cannot be decrypted on the center")
	case errors.Is(err, dispatch.ErrStateConflict):
		writeError(w, r, http.StatusConflict, "RUN_STATE_CONFLICT", "the Run state does not allow this operation")
	case errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03"):
		// 序列化失败、死锁检测或锁等待超时：请求可安全重试。
		writeError(w, r, http.StatusServiceUnavailable, "RETRY", "the request conflicted with concurrent work; retry")
	default:
		writeDeliveryError(w, r, err)
	}
}
