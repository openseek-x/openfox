// Package api contains the first WP-02/WP-03 authenticated HTTP vertical
// slice. It deliberately keeps tenant selection and policy evaluation close
// to the database transaction boundary.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"openfox/internal/artifacts"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/executiondelivery"
	"openfox/internal/gitlabproxy"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/feishu"
	"openfox/internal/platform/apierror"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/ratelimit"
	"openfox/internal/platform/requestid"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
	"openfox/internal/reconciliation"
	"openfox/internal/resourcepolicy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const (
	sessionCookieName = "openfox_session"
	csrfCookieName    = "openfox_csrf"
)

type Handler struct {
	auth       identity.Service
	deviceAuth deviceauth.Service
	pool       *postgres.Pool
	// storage 是文件存储（克隆空间的快照包等）；未配置时为 nil。
	storage      artifacts.Storage
	delivery     executiondelivery.Service
	reconcile    reconciliation.PGStore
	resources    resourcepolicy.Store
	dispatch     dispatch.Store
	dispatchHub  *dispatch.Hub
	secrets      *secrets.Box
	importSafe   safehttp.Options
	integrations integrations.Service
	// publicURL 是中心对外地址，用于邀请邮件里的链接。
	publicURL     string
	secureCookies bool
	// emailRequestLimiter / emailVerifyLimiter 按客户端 IP 限制未登录的邮箱验证码接口。
	emailRequestLimiter *ratelimit.Limiter
	emailVerifyLimiter  *ratelimit.Limiter
	// webhookLimiter 按令牌哈希限制 Webhook 接收端（每分钟 60 次）。
	webhookLimiter *ratelimit.Limiter
	trustedProxies []netip.Prefix
}

// AuthRateLimits configures per-client-IP limits for unauthenticated email
// login endpoints. A zero limit disables the corresponding limiter.
type AuthRateLimits struct {
	EmailRequestPerWindow int
	EmailVerifyPerWindow  int
	Window                time.Duration
	TrustedProxies        []netip.Prefix
}

// DefaultAuthRateLimits is applied when callers do not configure limits.
var DefaultAuthRateLimits = AuthRateLimits{EmailRequestPerWindow: 10, EmailVerifyPerWindow: 30, Window: 10 * time.Minute}

func New(auth identity.Service, pool *postgres.Pool, secureCookies bool) http.Handler {
	return NewWithStorage(auth, pool, secureCookies, nil)
}

func NewWithStorage(auth identity.Service, pool *postgres.Pool, secureCookies bool, storage artifacts.Storage) http.Handler {
	return NewWithOptions(auth, pool, secureCookies, storage, DefaultAuthRateLimits)
}

func NewWithOptions(auth identity.Service, pool *postgres.Pool, secureCookies bool, storage artifacts.Storage, limits AuthRateLimits) http.Handler {
	return NewWithDispatch(auth, pool, secureCookies, storage, limits, DispatchOptions{})
}

// NewWithDispatch 额外接收中心派发的租约参数与 WSS 唤醒 Hub。
func NewWithDispatch(auth identity.Service, pool *postgres.Pool, secureCookies bool, storage artifacts.Storage, limits AuthRateLimits, dispatchOptions DispatchOptions) http.Handler {
	if limits.Window <= 0 {
		limits.Window = DefaultAuthRateLimits.Window
	}
	deviceAuthService := deviceauth.Service{
		Store: deviceauth.PostgresStore{Pool: pool},
		Now:   time.Now,
	}
	h := &Handler{
		auth:                auth,
		deviceAuth:          deviceAuthService,
		pool:                pool,
		storage:             storage,
		delivery:            executiondelivery.Service{Pool: pool, Storage: storage, Resources: resourcepolicy.Store{}},
		reconcile:           reconciliation.PGStore{},
		resources:           resourcepolicy.Store{},
		dispatch:            withDispatchPublicURL(dispatchOptions.Store, dispatchOptions.PublicURL),
		dispatchHub:         dispatchOptions.Hub,
		secrets:             dispatchOptions.Secrets,
		importSafe:          dispatchOptions.ImportSafeHTTP,
		integrations:        integrations.Service{GitHubConfigured: dispatchOptions.GitHubConfigured},
		publicURL:           dispatchOptions.PublicURL,
		secureCookies:       secureCookies,
		emailRequestLimiter: ratelimit.New(limits.EmailRequestPerWindow, limits.Window, 10000, nil),
		emailVerifyLimiter:  ratelimit.New(limits.EmailVerifyPerWindow, limits.Window, 10000, nil),
		webhookLimiter:      ratelimit.New(webhookRatePerMin, time.Minute, 10000, nil),
		trustedProxies:      limits.TrustedProxies,
	}
	h.integrations = integrations.Service{GitHubConfigured: dispatchOptions.GitHubConfigured, GiteeConfigured: dispatchOptions.GiteeConfigured, GitHub: dispatchOptions.GitHubClient, Feishu: dispatchOptions.FeishuClient,
		GitFactory: dispatchOptions.GitFactory, ValidateInstance: dispatchOptions.GitValidateInstance,
		DingTalk: dispatchOptions.DingTalkClient, WeChat: dispatchOptions.WeChatClient, WeCom: dispatchOptions.WeComClient,
		IMClients: map[string]*feishu.Client{integrations.ProviderFeishu: dispatchOptions.FeishuClient, integrations.ProviderLark: dispatchOptions.LarkClient},
		Box:       dispatchOptions.Secrets, PublicURL: centerPublicURL(dispatchOptions.PublicURL)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/login", h.login)
	mux.HandleFunc("POST /v1/auth/email/request", h.requestEmailCode)
	mux.HandleFunc("POST /v1/auth/email/verify", h.verifyEmailCode)
	h.registerMemberRoutes(mux)
	h.registerOrganizationSettingsRoutes(mux)
	h.registerInviteLinkRoutes(mux)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/invites", h.createInvitation)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/invites", h.listInvitations)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/invites/{inviteID}/revoke", h.revokeInvitation)
	mux.HandleFunc("POST /v1/invites/{inviteID}/accept", h.acceptInvitation)
	RegisterDeviceRoutes(mux, auth, deviceAuthService, pool)
	deviceStore := devices.PostgresStore{Pool: pool}
	RegisterDeviceManagementRoutesWithOptions(mux, auth, deviceAuthService, deviceStore, DeviceManagementOptions{
		Proxy: gitlabproxy.Broker{Pool: pool, Hub: dispatchOptions.Hub}, Hub: dispatchOptions.Hub, Commands: deviceStore, PublicURL: dispatchOptions.PublicURL,
	})
	h.registerCollaborationRoutes(mux)
	h.registerSpaceRoutes(mux)
	h.registerSpaceAdminRoutes(mux)
	h.registerEstimationRoutes(mux)
	h.registerIterationRoutes(mux)
	h.registerProjectRoutes(mux)
	h.registerViewRoutes(mux)
	h.registerTaskConfigRoutes(mux)
	h.registerTaskRoutes(mux)
	h.registerTriageRoutes(mux)
	h.registerTaskSubRoutes(mux)
	h.registerAgentRoutes(mux)
	h.registerAutomationRoutes(mux)
	h.registerDocRoutes(mux)
	h.registerSkillRoutes(mux)
	h.registerMarketRoutes(mux)
	h.registerConversationRoutes(mux)
	h.registerDirectChatRoutes(mux)
	h.registerAttachmentRoutes(mux)
	h.registerQuickReplyRoutes(mux)
	h.registerQuickCommandRoutes(mux)
	h.registerInboxRoutes(mux)
	h.registerMyTaskRoutes(mux)
	h.registerSearchRoutes(mux)
	h.registerAgentDirectoryRoutes(mux)
	h.registerAgentExtRoutes(mux)
	h.registerAgentMCPRoutes(mux)
	h.registerMCPOAuthRoutes(mux)
	h.registerAssistantRoutes(mux)
	h.registerExecutionDeliveryRoutes(mux)
	h.registerDispatchRoutes(mux)
	h.registerReconciliationRoutes(mux)
	h.registerResourcePolicyRoutes(mux)
	h.registerOnboardingRoutes(mux)
	h.registerSessionRoutes(mux)
	h.registerProfileRoutes(mux)
	h.registerPreferenceRoutes(mux)
	h.registerTokenRoutes(mux)
	h.registerNotificationChannelRoutes(mux)
	h.registerIntegrationRoutes(mux)
	h.registerImportRoutes(mux)
	h.registerCloneRoutes(mux)
	h.registerExportRoutes(mux)
	h.registerBetaFeatureRoutes(mux)
	h.registerGetStartedRoutes(mux)
	return mux
}

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	ClientKind string `json:"client_kind"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	clientKind := parseClientKind(input.ClientKind)
	result, err := h.auth.Login(r.Context(), input.Email, input.Password, clientKind)
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCredentials) {
			writeError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "email or password is invalid")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	h.writeLoginSession(w, r, result)
	return
}

type emailCodeRequest struct {
	Email string `json:"email"`
}

type emailCodeVerifyRequest struct {
	Email      string `json:"email"`
	Code       string `json:"code"`
	ClientKind string `json:"client_kind"`
}

func writeRetryAfter(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
}

// allowByClientIP applies the per-IP limiter and writes a 429 when exceeded.
func (h *Handler) allowByClientIP(w http.ResponseWriter, r *http.Request, limiter *ratelimit.Limiter) bool {
	return h.allowKey(w, r, limiter, ratelimit.ClientIP(r, h.trustedProxies))
}

// allowKey applies a limiter to an arbitrary key and writes a 429 when exceeded.
func (h *Handler) allowKey(w http.ResponseWriter, r *http.Request, limiter *ratelimit.Limiter, key string) bool {
	decision := limiter.Allow(key)
	if decision.Allowed {
		return true
	}
	seconds := int(decision.RetryAfter / time.Second)
	if decision.RetryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests, please retry later")
	return false
}

func (h *Handler) requestEmailCode(w http.ResponseWriter, r *http.Request) {
	if !h.allowByClientIP(w, r, h.emailRequestLimiter) {
		return
	}
	var input emailCodeRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.auth.RequestEmailCode(r.Context(), input.Email)
	if err != nil {
		if errors.Is(err, identity.ErrInvalidEmail) {
			writeError(w, r, http.StatusBadRequest, "INVALID_EMAIL", "email address is invalid")
			return
		}
		if errors.Is(err, identity.ErrEmailLoginNotConfigured) {
			writeError(w, r, http.StatusServiceUnavailable, "EMAIL_LOGIN_UNAVAILABLE", "email login is not configured")
			return
		}
		var limited identity.EmailCodeLimitError
		if errors.As(err, &limited) {
			writeRetryAfter(w, limited.RetryAfter)
			writeError(w, r, http.StatusTooManyRequests, "EMAIL_DAILY_LIMIT", "too many verification codes for this email, please retry later")
			return
		}
		var cooldown identity.EmailCodeCooldownError
		if errors.As(err, &cooldown) {
			seconds := int(cooldown.RetryAfter / time.Second)
			if cooldown.RetryAfter%time.Second != 0 {
				seconds++
			}
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
			writeError(w, r, http.StatusTooManyRequests, "EMAIL_CODE_RATE_LIMITED", "please wait before requesting another code")
			return
		}
		if errors.Is(err, identity.ErrEmailDelivery) {
			writeError(w, r, http.StatusServiceUnavailable, "EMAIL_DELIVERY_FAILED", "verification email could not be sent")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"expires_at": result.ExpiresAt})
}

func (h *Handler) verifyEmailCode(w http.ResponseWriter, r *http.Request) {
	if !h.allowByClientIP(w, r, h.emailVerifyLimiter) {
		return
	}
	var input emailCodeVerifyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.auth.VerifyEmailCode(r.Context(), input.Email, input.Code, parseClientKind(input.ClientKind))
	if err != nil {
		var limited identity.EmailCodeLimitError
		if errors.As(err, &limited) {
			writeRetryAfter(w, limited.RetryAfter)
			writeError(w, r, http.StatusTooManyRequests, "EMAIL_DAILY_LIMIT", "too many failed attempts for this email, please retry later")
			return
		}
		if errors.Is(err, identity.ErrEmailCodeInvalid) || errors.Is(err, identity.ErrInvalidCredentials) {
			writeError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "email or verification code is invalid")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	h.writeLoginSession(w, r, result)
}

func parseClientKind(value string) identity.ClientKind {
	clientKind := identity.ClientKind(strings.ToUpper(strings.TrimSpace(value)))
	if clientKind == "" {
		clientKind = identity.ClientWeb
	}
	return clientKind
}

func (h *Handler) writeLoginSession(w http.ResponseWriter, r *http.Request, result identity.LoginResult) {
	csrf, err := newOpaqueToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	setCookie(w, sessionCookieName, result.Token, result.ExpiresAt, true, h.secureCookies)
	setCookie(w, csrfCookieName, csrf, result.ExpiresAt, false, h.secureCookies)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":    result.UserID,
		"expires_at": result.ExpiresAt,
		"new_user":   result.NewUser,
	})
}

type authContext struct {
	principal identity.Principal
	viaCookie bool
}

func (h *Handler) authenticate(r *http.Request) (authContext, error) {
	token, viaCookie := bearerToken(r), false
	if token == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			token, viaCookie = cookie.Value, true
		}
	}
	if token == "" {
		return authContext{}, identity.ErrSessionNotFound
	}
	// 个人访问令牌只通过 Authorization 头传递，不接受 cookie；令牌请求不需要 CSRF。
	if !viaCookie && identity.IsPersonalAccessToken(token) {
		principal, err := h.auth.AuthenticatePersonalAccessToken(r.Context(), token)
		if err != nil {
			return authContext{}, err
		}
		return authContext{principal: principal}, nil
	}
	principal, err := h.auth.AuthenticateSession(r.Context(), token)
	if err != nil {
		return authContext{}, err
	}
	return authContext{principal: principal, viaCookie: viaCookie}, nil
}

type spaceResponse struct {
	ID         string    `json:"id"`
	Prefix     string    `json:"prefix"`
	Name       string    `json:"name"`
	Visibility string    `json:"visibility"`
	Version    uint64    `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
}

type decisionError struct{ decision policy.Decision }

func (e decisionError) Error() string { return fmt.Sprintf("policy decision: %s", e.decision.Reason) }

func organizationRole(value string) policy.OrganizationRole {
	switch strings.ToUpper(value) {
	case "OWNER":
		return policy.OrganizationRoleOwner
	case "ADMIN":
		return policy.OrganizationRoleAdmin
	case "MEMBER":
		return policy.OrganizationRoleMember
	case "GUEST":
		return policy.OrganizationRoleGuest
	default:
		return ""
	}
}

func spaceVisibility(value string) policy.Visibility {
	if strings.EqualFold(value, "SHARED") {
		return policy.VisibilityPublic
	}
	return policy.VisibilityPrivate
}

func spaceRole(value string) policy.SpaceRole {
	switch strings.ToUpper(value) {
	case "OWNER", "EDITOR":
		return policy.SpaceRoleAdministrator
	case "MEMBER":
		return policy.SpaceRoleMember
	case "VIEWER":
		return policy.SpaceRoleVisitor
	default:
		return ""
	}
}

func spaceMembershipState(value string) policy.MembershipState {
	if strings.EqualFold(strings.TrimSpace(value), "ACTIVE") {
		return policy.MembershipActive
	}
	return policy.MembershipRevoked
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 7 || !strings.EqualFold(value[:6], "Bearer") || value[6] != ' ' {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get("X-CSRF-Token")
	if header == "" || len(header) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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

func setCookie(w http.ResponseWriter, name, value string, expires time.Time, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge(expires),
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func maxAge(expires time.Time) int {
	seconds := int(time.Until(expires).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func newOpaqueToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func newUUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := requestid.FromContext(r.Context())
	if requestID == "" {
		requestID = r.Header.Get(requestid.Header)
	}
	apierror.Write(w, status, code, message, requestID)
}

func writeOrganizationSelectionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, postgres.ErrOrganizationNotFound) || errors.Is(err, tenancy.ErrOrganizationSelection) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization is not available")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
}

// withDispatchPublicURL 把中心对外地址注入派发存储，用于任务助手 Run 上下文里的任务链接。
func withDispatchPublicURL(store dispatch.Store, publicURL string) dispatch.Store {
	if store.PublicURL == "" {
		store.PublicURL = centerPublicURL(publicURL)
	}
	return store
}
