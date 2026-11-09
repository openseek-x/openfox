package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/gitlabproxy"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// DeviceManagementHandler owns the WP-05 management and heartbeat routes.
// The existing deviceauth routes remain separate so device principals cannot
// accidentally reach user-only management actions.
type DeviceManagementHandler struct {
	proxy      gitlabproxy.Broker
	userAuth   identity.Service
	deviceAuth deviceauth.Service
	store      devices.Store
	hub        *dispatch.Hub
	// commands 为空时远程命令接口返回 503 NOT_READY。
	commands devices.CommandStore
	// publicURL 是写进 v2 安装令牌的中心地址（OPENFOX_SERVER_PUBLIC_URL）。
	publicURL string
}

// DeviceManagementOptions 是电脑管理路由的可选依赖。
type DeviceManagementOptions struct {
	Proxy     gitlabproxy.Broker
	Hub       *dispatch.Hub
	Commands  devices.CommandStore
	PublicURL string
}

// defaultCenterPublicURL 与 OPENFOX_SERVER_PUBLIC_URL 的默认值一致；未注入时用于开发与测试。
const defaultCenterPublicURL = "http://127.0.0.1:8080"

// hub 可选：传入时设备 WSS 连接会接收 run.dispatch / run.cancel 唤醒。
func RegisterDeviceManagementRoutes(mux *http.ServeMux, userAuth identity.Service, deviceAuth deviceauth.Service, store devices.Store, hub ...*dispatch.Hub) {
	options := DeviceManagementOptions{}
	if len(hub) > 0 {
		options.Hub = hub[0]
	}
	RegisterDeviceManagementRoutesWithOptions(mux, userAuth, deviceAuth, store, options)
}

// RegisterDeviceManagementRoutesWithOptions 与 RegisterDeviceManagementRoutes 相同，另外挂载远程命令接口。
func RegisterDeviceManagementRoutesWithOptions(mux *http.ServeMux, userAuth identity.Service, deviceAuth deviceauth.Service, store devices.Store, options DeviceManagementOptions) {
	if mux == nil {
		return
	}
	h := &DeviceManagementHandler{
		proxy: options.Proxy, userAuth: userAuth, deviceAuth: deviceAuth, store: store,
		hub: options.Hub, commands: options.Commands, publicURL: strings.TrimSpace(options.PublicURL),
	}
	if h.publicURL == "" {
		h.publicURL = defaultCenterPublicURL
	}
	var hub []*dispatch.Hub
	if options.Hub != nil {
		hub = append(hub, options.Hub)
	}
	mux.HandleFunc("POST /v1/device/register", h.register)
	mux.HandleFunc("POST /v1/device/heartbeat", h.heartbeat)
	RegisterDeviceStreamRoute(mux, deviceAuth, store, hub...)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/registration-tokens", h.issueRegistrationToken)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices", h.list)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices/center", h.center)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices/{deviceID}", h.get)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/devices/{deviceID}", h.updateSettings)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/devices/{deviceID}", h.remove)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices/{deviceID}/removal-blockers", h.removalBlockers)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/remove-from-organization", h.removeFromOrganization)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/unbind-agents", h.unbindAgents)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/replace", h.replaceDevice)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/scope", h.updateScope)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/keys/{keyID}/rotate", h.rotateKey)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/commands", h.createCommand)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices/{deviceID}/commands/{commandID}", h.getCommand)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/gitlab-proxy/{streamID}", h.gitlabProxy)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/commands", h.listDeviceCommands)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/commands/{commandID}/claim", h.claimDeviceCommand)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/commands/{commandID}/result", h.reportDeviceCommandResult)
}

type registrationTokenRequest struct {
	Name      string        `json:"name"`
	InstallID string        `json:"install_id"`
	MaxSlots  int           `json:"max_slots"`
	Scope     devices.Scope `json:"scope"`
}

type registrationTokenResponse struct {
	ID           string        `json:"id"`
	Token        string        `json:"token"`
	InstallToken string        `json:"install_token"`
	Command      string        `json:"command"`
	Scope        devices.Scope `json:"scope"`
	ExpiresAt    time.Time     `json:"expires_at"`
}

type deviceRegisterRequest struct {
	Token     string `json:"token"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

type deviceRegisterResponse struct {
	DeviceID       string `json:"device_id"`
	OrganizationID string `json:"organization_id"`
	OwnerUserID    string `json:"owner_user_id"`
	KeyGeneration  int64  `json:"key_generation"`
	CenterEpoch    int64  `json:"center_epoch"`
}

type deviceHeartbeatRequest struct {
	CenterEpoch     int64          `json:"center_epoch"`
	ProtocolMajor   int            `json:"protocol_major"`
	ProtocolMinor   int            `json:"protocol_minor"`
	DaemonVersion   string         `json:"daemon_version"`
	OperatingSystem string         `json:"operating_system"`
	Architecture    string         `json:"architecture"`
	Capabilities    map[string]any `json:"capabilities"`
}

type deviceHeartbeatResponse struct {
	DeviceID    string    `json:"device_id"`
	CenterEpoch int64     `json:"center_epoch"`
	Version     int64     `json:"version"`
	ReceivedAt  time.Time `json:"received_at"`
}

type deviceScopeRequest struct {
	Scope   devices.Scope `json:"scope"`
	UserID  string        `json:"user_id"`
	SpaceID string        `json:"space_id"`
	Version int64         `json:"version"`
}

type deviceRotateKeyRequest struct {
	OldKeyID  string `json:"old_key_id"`
	NewKeyID  string `json:"new_key_id"`
	PublicKey string `json:"public_key"`
	Version   int64  `json:"version"`
}

func (h *DeviceManagementHandler) register(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return
	}
	var input deviceRegisterRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	publicKey, err := deviceauth.DecodeBase64URL(input.PublicKey, 32)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.store.RegisterWithToken(r.Context(), devices.RegistrationExchange{
		Token: input.Token, KeyID: input.KeyID, PublicKey: publicKey,
	})
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceRegisterResponse{
		DeviceID: result.DeviceID, OrganizationID: result.OrganizationID,
		OwnerUserID: result.OwnerUserID, KeyGeneration: result.KeyGeneration,
		CenterEpoch: result.CenterEpoch,
	})
}

func (h *DeviceManagementHandler) issueRegistrationToken(w http.ResponseWriter, r *http.Request) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return
	}
	if authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return
	}
	var input registrationTokenRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID := r.PathValue("organizationID")
	selection, role, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	scope, allowed := registrationScope(role, input.Scope)
	if scope == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "scope is invalid")
		return
	}
	if !allowed {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "action is not allowed")
		return
	}
	result, err := h.store.IssueRegistrationToken(r.Context(), selection, devices.RegistrationTokenInput{
		Name: input.Name, InstallID: input.InstallID, MaxSlots: input.MaxSlots, Scope: scope,
	}, time.Now().UTC().Add(10*time.Minute))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	installToken, err := devices.EncodeInstallToken(h.publicURL, result.Token)
	if err != nil {
		// 只可能是中心地址配置不合法；令牌已落库但未返回，10 分钟后自然过期。
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	resultScope := result.Scope
	if resultScope == "" {
		resultScope = scope
	}
	writeJSON(w, http.StatusCreated, registrationTokenResponse{
		ID: result.ID, Token: result.Token, InstallToken: installToken,
		Command: "openfox computer register --install-token " + installToken,
		Scope:   resultScope, ExpiresAt: result.ExpiresAt,
	})
}

// registrationScope 按组织角色决定注册令牌范围与是否允许签发。返回空 scope 表示请求的范围不合法。
// ORGANIZATION 仅 OWNER / ADMIN；PERSONAL 任何非 GUEST 成员；省略时 OWNER / ADMIN 为 ORGANIZATION，其他为 PERSONAL。
func registrationScope(role string, requested devices.Scope) (devices.Scope, bool) {
	manager := canManageDevice(role)
	guest := strings.EqualFold(strings.TrimSpace(role), "GUEST")
	scope := devices.Scope(strings.ToUpper(strings.TrimSpace(string(requested))))
	switch scope {
	case "":
		if manager {
			return devices.ScopeOrganization, true
		}
		return devices.ScopePersonal, !guest
	case devices.ScopeOrganization:
		return scope, manager
	case devices.ScopePersonal:
		return scope, !guest
	default:
		return "", false
	}
}

func (h *DeviceManagementHandler) list(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	options := devices.ListOptions{Cursor: strings.TrimSpace(r.URL.Query().Get("cursor"))}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		options.Limit = limit
	}
	page, err := h.store.ListVisible(r.Context(), selection, viewer, options)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// center 返回当前中心地址：与签发安装令牌时写进令牌的地址完全相同（同一份 publicURL、同一套归一化）。
// 本机客户端据此判断本机注册的中心地址是否已过期。组织成员即可读取，与电脑列表一致。
func (h *DeviceManagementHandler) center(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.selectViewer(w, r, false); !ok {
		return
	}
	centerURL, err := devices.NormalizeCenterURL(h.publicURL)
	if err != nil {
		// 只可能是中心地址配置不合法，与签发安装令牌时的失败一致。
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"center_url": centerURL})
}

func (h *DeviceManagementHandler) get(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	detail, err := h.store.Get(r.Context(), selection, viewer, r.PathValue("deviceID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type deviceSettingsRequest struct {
	Name            *string `json:"name"`
	MaxSlots        *int    `json:"max_slots"`
	SettingsVersion int64   `json:"settings_version"`
}

func (h *DeviceManagementHandler) updateSettings(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	var input deviceSettingsRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	device, err := h.store.UpdateSettings(r.Context(), selection, viewer, r.PathValue("deviceID"), devices.SettingsInput{
		Name: input.Name, MaxSlots: input.MaxSlots, ExpectedVersion: input.SettingsVersion,
	})
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (h *DeviceManagementHandler) remove(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	if err := h.store.Remove(r.Context(), selection, viewer, r.PathValue("deviceID"), time.Now().UTC()); err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DeviceManagementHandler) removalBlockers(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	blockers, err := h.store.RemovalBlockers(r.Context(), selection, viewer, r.PathValue("deviceID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, blockers)
}

func (h *DeviceManagementHandler) removeFromOrganization(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	result, err := h.store.RemoveFromOrganization(r.Context(), selection, viewer, r.PathValue("deviceID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	h.publishCancels(result.CancelNotices)
	writeJSON(w, http.StatusOK, result)
}

type unbindAgentsRequest struct {
	AgentIDs []string `json:"agent_ids"`
}

func (h *DeviceManagementHandler) unbindAgents(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	var input unbindAgentsRequest
	// 请求体可省略：空体等同 agent_ids 为空（全部）。
	if err := decodeJSON(w, r, &input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.store.UnbindAgents(r.Context(), selection, viewer, r.PathValue("deviceID"), input.AgentIDs)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	h.publishCancels(result.CancelNotices)
	writeJSON(w, http.StatusOK, result)
}

type replaceDeviceRequest struct {
	ReplacedDeviceID string `json:"replaced_device_id"`
}

// replaceDevice 把旧电脑记录上的 Agent 迁到 {deviceID}（新记录），并吊销旧记录在本组织的授权。
func (h *DeviceManagementHandler) replaceDevice(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	var input replaceDeviceRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.store.ReplaceDevice(r.Context(), selection, viewer, r.PathValue("deviceID"), input.ReplacedDeviceID)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	h.publishCancels(result.CancelNotices)
	writeJSON(w, http.StatusOK, result)
}

// publishCancels 在事务提交后唤醒电脑处理取消；投递失败不影响结果，电脑下一次续租会看到 CANCEL_REQUESTED。
func (h *DeviceManagementHandler) publishCancels(notices []dispatch.CancelNotice) {
	if h.hub == nil {
		return
	}
	for _, notice := range notices {
		h.hub.PublishCancel(notice.DeviceID, notice.OrganizationID, notice.RunID, notice.Generation)
	}
}

// selectViewer 完成用户认证、可选的 CSRF 校验与组织选择，返回租户选择与查看者角色。
func (h *DeviceManagementHandler) selectViewer(w http.ResponseWriter, r *http.Request, write bool) (tenancy.OrganizationSelection, devices.Viewer, bool) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return tenancy.OrganizationSelection{}, devices.Viewer{}, false
	}
	if write && authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return tenancy.OrganizationSelection{}, devices.Viewer{}, false
	}
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return tenancy.OrganizationSelection{}, devices.Viewer{}, false
	}
	selection, role, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, r.PathValue("organizationID"))
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return tenancy.OrganizationSelection{}, devices.Viewer{}, false
	}
	return selection, devices.Viewer{Role: role}, true
}

func (h *DeviceManagementHandler) updateScope(w http.ResponseWriter, r *http.Request) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return
	}
	if authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return
	}
	var input deviceScopeRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	selection, _, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, r.PathValue("organizationID"))
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	result, err := h.store.UpdateGrantScope(r.Context(), selection, r.PathValue("deviceID"), devices.GrantScopeInput{
		Scope: input.Scope, UserID: input.UserID, SpaceID: input.SpaceID, ExpectedVersion: input.Version,
	})
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *DeviceManagementHandler) rotateKey(w http.ResponseWriter, r *http.Request) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return
	}
	if authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return
	}
	var input deviceRotateKeyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	publicKey, err := deviceauth.DecodeBase64URL(input.PublicKey, 32)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	selection, _, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, r.PathValue("organizationID"))
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	result, err := h.store.RotateKey(r.Context(), selection, r.PathValue("deviceID"), devices.KeyRotationInput{
		OldKeyID: input.OldKeyID, NewKeyID: input.NewKeyID, PublicKey: publicKey, ExpectedVersion: input.Version,
	}, time.Now().UTC())
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *DeviceManagementHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	if h.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device management is not ready")
		return
	}
	var input deviceHeartbeatRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.store.RecordHeartbeat(r.Context(), principal, devices.Heartbeat{
		CenterEpoch: input.CenterEpoch, ProtocolMajor: input.ProtocolMajor, ProtocolMinor: input.ProtocolMinor,
		DaemonVersion: input.DaemonVersion, OperatingSystem: input.OperatingSystem,
		Architecture: input.Architecture, Capabilities: mustJSON(input.Capabilities),
	}, time.Now().UTC())
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceHeartbeatResponse{
		DeviceID: result.DeviceID, CenterEpoch: result.CenterEpoch,
		Version: result.Version, ReceivedAt: result.ReceivedAt,
	})
}

func mustJSON(value map[string]any) []byte {
	if value == nil {
		return []byte("{}")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

func writeDeviceManagementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, devices.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	case errors.Is(err, devices.ErrInvalidCursor):
		writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "cursor is invalid")
	case errors.Is(err, devices.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "action is not allowed")
	case errors.Is(err, devices.ErrHasActiveAgents):
		writeError(w, r, http.StatusConflict, "DEVICE_HAS_ACTIVE_AGENTS", "archive or unbind the Agents on this device first")
	case errors.Is(err, devices.ErrDeviceOnline):
		writeError(w, r, http.StatusConflict, "DEVICE_ONLINE", "the replaced device is still online")
	case errors.Is(err, devices.ErrGrantScopeMismatch):
		writeError(w, r, http.StatusConflict, "GRANT_SCOPE_MISMATCH", "the two devices are granted with different scopes")
	case errors.Is(err, devices.ErrTooManyAgents):
		writeError(w, r, http.StatusConflict, "TOO_MANY_AGENTS", "too many Agents to move at once")
	case errors.Is(err, devices.ErrRegistrationConflict):
		writeError(w, r, http.StatusConflict, "REGISTRATION_CONFLICT", "registration token is stale or already consumed")
	case errors.Is(err, devices.ErrVersionConflict), errors.Is(err, devices.ErrEpochConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "device state changed; refresh and retry")
	case errors.Is(err, devices.ErrOwnerRequired):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "only the device owner may manage this device")
	case errors.Is(err, devices.ErrCommandExpired):
		writeError(w, r, http.StatusConflict, "COMMAND_EXPIRED", "the command has expired")
	case errors.Is(err, devices.ErrCommandClaimed):
		writeError(w, r, http.StatusConflict, "COMMAND_CLAIMED", "the command was already claimed")
	case errors.Is(err, devices.ErrCommandConflict):
		writeError(w, r, http.StatusConflict, "COMMAND_CONFLICT", "the command state does not allow this result")
	case errors.Is(err, devices.ErrGrantNotFound), errors.Is(err, devices.ErrDeviceNotFound),
		errors.Is(err, devices.ErrCommandNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, deviceauth.ErrDeviceUnauthenticated), errors.Is(err, deviceauth.ErrSessionNotFound):
		writeError(w, r, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED", "device authentication is required")
	case errors.Is(err, postgres.ErrNilPool):
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func (h *DeviceManagementHandler) authenticateDevice(r *http.Request) (deviceauth.Principal, error) {
	token := bearerToken(r)
	if token == "" {
		return deviceauth.Principal{}, deviceauth.ErrSessionNotFound
	}
	return h.deviceAuth.Authenticate(r.Context(), token)
}

func (h *DeviceManagementHandler) authenticateUser(r *http.Request) (userAuthContext, error) {
	token, viaCookie := bearerToken(r), false
	if token == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			token, viaCookie = cookie.Value, true
		}
	}
	if token == "" {
		return userAuthContext{}, identity.ErrSessionNotFound
	}
	principal, err := h.userAuth.AuthenticateSession(r.Context(), token)
	if err != nil {
		return userAuthContext{}, err
	}
	return userAuthContext{principal: principal, viaCookie: viaCookie}, nil
}
