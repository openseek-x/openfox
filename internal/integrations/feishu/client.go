// Package feishu 是飞书开放平台 REST 接口、OAuth 与长连接（WebSocket + protobuf 帧）的最小客户端。
// 地址来自运维配置而不是用户输入；长连接端点由飞书返回，使用前会做主机校验（见 longconn.go）。
// 日志与错误信息不得包含 App Secret、tenant_access_token、用户令牌和授权 code。
package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	connectTimeout = 10 * time.Second
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 20
	// tokenEarlyExpiry 是 tenant_access_token 提前过期的时间，避免用到临界过期的令牌。
	tokenEarlyExpiry = 5 * time.Minute
	// defaultRateLimitWait 是限流响应没有给出恢复时间时的等待。
	defaultRateLimitWait = 30 * time.Second
)

var (
	// ErrInvalidCredentials 表示 App ID / App Secret 无效（或应用被停用），需要管理员重新验证。
	ErrInvalidCredentials = errors.New("feishu: invalid app credentials")
	// ErrTokenExpired 表示 tenant_access_token 已失效：调用方清除缓存后可重试一次。
	ErrTokenExpired = errors.New("feishu: token expired")
	// ErrBotUnavailable 表示读不到机器人信息（应用没有开启「机器人」能力或尚未发布）。
	ErrBotUnavailable = errors.New("feishu: bot is unavailable")
	// ErrUserUnreachable 表示机器人无法给该用户发消息（不在应用可用范围、已离职等），重试没有意义。
	ErrUserUnreachable = errors.New("feishu: user is unreachable")
	// ErrUnavailable 表示网络错误、超时、5xx 或无法解析的响应，可稍后重试。
	ErrUnavailable = errors.New("feishu: service unavailable")
	// ErrAuthorizationFailed 表示授权码换取用户令牌或读取用户信息失败。
	ErrAuthorizationFailed = errors.New("feishu: authorization failed")
)

// RateLimitedError 表示触发限流，RetryAfter 为建议等待时间。
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "feishu: rate limited" }

// Is 让 errors.Is(err, ErrRateLimited) 成立。
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// ErrRateLimited 是限流的哨兵错误；需要等待时间时用 errors.As 取 *RateLimitedError。
var ErrRateLimited = errors.New("feishu: rate limited")

// APIError 带飞书返回的业务码，用于诊断；Msg 来自飞书，不含凭据。
type APIError struct {
	Code int
	Msg  string
	base error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%v (code %d: %s)", e.base, e.Code, truncate(e.Msg, 200))
}
func (e *APIError) Unwrap() error { return e.base }

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// Client 持有服务地址与 HTTP 客户端。零值不可用，用 New 构造。
type Client struct {
	BaseURL     string
	AccountsURL string
	HTTP        *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	value   string
	expires time.Time
}

// New 构造带超时的客户端：连接 10 秒，整体 10 秒。
func New(baseURL, accountsURL string) *Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSHandshakeTimeout: connectTimeout,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), AccountsURL: strings.TrimRight(accountsURL, "/"),
		HTTP: &http.Client{Timeout: requestTimeout, Transport: transport}}
}

// 飞书业务码分类（只列已知的；其余非 0 的鉴权接口错误按凭证无效处理）。
const (
	codeRateLimited      = 99991400
	codeSystemBusy       = 1
	codeInternalError    = 1000040343
	codeTokenInvalid     = 99991663
	codeTokenInvalidAlt  = 99991661
	codeTokenExpiredAlt  = 99991668
	codeUserUnreachable1 = 230013
	codeUserUnreachable2 = 230001
	codeUserUnreachable3 = 230002
)

func isTokenExpiredCode(code int) bool {
	return code == codeTokenInvalid || code == codeTokenInvalidAlt || code == codeTokenExpiredAlt
}

func cacheKey(appID, secret string) string {
	sum := sha256.Sum256([]byte(appID + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// ClearToken 清除某个应用的 tenant_access_token 缓存（收到 ErrTokenExpired 后调用）。
func (c *Client) ClearToken(appID, secret string) {
	c.mu.Lock()
	delete(c.tokens, cacheKey(appID, secret))
	c.mu.Unlock()
}

// call 发送 JSON 请求并读取最多 1 MiB 的响应。HTTP 429 返回 *RateLimitedError，5xx 与网络错误返回 ErrUnavailable。
func (c *Client) call(ctx context.Context, method, target, bearer string, body any, header map[string]string) (int, http.Header, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, ErrUnavailable
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "OpenFox")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, nil, ErrUnavailable
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, resp.Header, data, rateLimited(resp.Header)
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, resp.Header, data, ErrUnavailable
	}
	return resp.StatusCode, resp.Header, data, nil
}

func rateLimited(header http.Header) error {
	wait := defaultRateLimitWait
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("x-ogw-ratelimit-reset"))); err == nil && seconds > 0 {
		wait = time.Duration(seconds) * time.Second
	}
	return &RateLimitedError{RetryAfter: wait}
}

// classifyCode 把业务码转成错误；code 为 0 返回 nil。
func classifyCode(code int, msg string, header http.Header) error {
	switch {
	case code == 0:
		return nil
	case code == codeRateLimited:
		return &RateLimitedError{RetryAfter: rateLimited(header).(*RateLimitedError).RetryAfter}
	case isTokenExpiredCode(code):
		return &APIError{Code: code, Msg: msg, base: ErrTokenExpired}
	case code == codeUserUnreachable1 || code == codeUserUnreachable2 || code == codeUserUnreachable3:
		return &APIError{Code: code, Msg: msg, base: ErrUserUnreachable}
	case code == codeSystemBusy || code == codeInternalError:
		return &APIError{Code: code, Msg: msg, base: ErrUnavailable}
	}
	return nil
}

// TenantToken 用 App ID / Secret 换取 tenant_access_token（进程内缓存，提前 5 分钟过期）。
func (c *Client) TenantToken(ctx context.Context, appID, secret string) (string, error) {
	key := cacheKey(appID, secret)
	c.mu.Lock()
	if cached, ok := c.tokens[key]; ok && time.Now().Before(cached.expires) {
		c.mu.Unlock()
		return cached.value, nil
	}
	c.mu.Unlock()
	_, header, data, err := c.call(ctx, http.MethodPost, c.BaseURL+"/open-apis/auth/v3/tenant_access_token/internal", "",
		map[string]string{"app_id": appID, "app_secret": secret}, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return "", ErrUnavailable
	}
	if payload.Code != 0 {
		if err := classifyCode(payload.Code, payload.Msg, header); err != nil && !errors.Is(err, ErrTokenExpired) {
			return "", err
		}
		return "", &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrInvalidCredentials}
	}
	if payload.Token == "" {
		return "", ErrUnavailable
	}
	ttl := time.Duration(payload.Expire)*time.Second - tokenEarlyExpiry
	if ttl > 0 {
		c.mu.Lock()
		if c.tokens == nil {
			c.tokens = map[string]cachedToken{}
		}
		c.tokens[key] = cachedToken{value: payload.Token, expires: time.Now().Add(ttl)}
		c.mu.Unlock()
	}
	return payload.Token, nil
}

// Bot 是机器人信息。
type Bot struct {
	AppName   string `json:"app_name"`
	AvatarURL string `json:"avatar_url"`
	OpenID    string `json:"open_id"`
}

// BotInfo 读取机器人信息；code 非 0 或没有 bot 返回 ErrBotUnavailable。
func (c *Client) BotInfo(ctx context.Context, token string) (Bot, error) {
	_, header, data, err := c.call(ctx, http.MethodGet, c.BaseURL+"/open-apis/bot/v3/info", token, nil, nil)
	if err != nil {
		return Bot{}, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  *Bot   `json:"bot"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return Bot{}, ErrUnavailable
	}
	if err := classifyCode(payload.Code, payload.Msg, header); err != nil {
		return Bot{}, err
	}
	if payload.Code != 0 || payload.Bot == nil {
		return Bot{}, &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrBotUnavailable}
	}
	return *payload.Bot, nil
}

// SendCard 给 open_id 发送一条交互卡片；uuid 用于飞书 1 小时内的去重。返回 message_id。
func (c *Client) SendCard(ctx context.Context, token, openID, cardJSON, uuid string) (string, error) {
	body := map[string]string{"receive_id": openID, "msg_type": "interactive", "content": cardJSON, "uuid": uuid}
	_, header, data, err := c.call(ctx, http.MethodPost, c.BaseURL+"/open-apis/im/v1/messages?receive_id_type=open_id", token, body, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return "", ErrUnavailable
	}
	if payload.Code != 0 {
		if err := classifyCode(payload.Code, payload.Msg, header); err != nil {
			return "", err
		}
		return "", &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrUnavailable}
	}
	if payload.Data.MessageID == "" {
		return "", ErrUnavailable
	}
	return payload.Data.MessageID, nil
}

// AuthorizeURL 生成用户授权页地址（PKCE S256）。
func (c *Client) AuthorizeURL(appID, redirectURI, state, challenge string) string {
	query := url.Values{}
	query.Set("client_id", appID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	return c.AccountsURL + "/open-apis/authen/v1/authorize?" + query.Encode()
}

// ExchangeCode 用授权码与 PKCE verifier 换取用户令牌（只用来读身份，调用方不得保存）。
func (c *Client) ExchangeCode(ctx context.Context, appID, secret, code, redirectURI, verifier string) (string, error) {
	body := map[string]string{"grant_type": "authorization_code", "client_id": appID, "client_secret": secret,
		"code": code, "redirect_uri": redirectURI, "code_verifier": verifier}
	status, _, data, err := c.call(ctx, http.MethodPost, c.BaseURL+"/open-apis/authen/v2/oauth/token", "", body, nil)
	if err != nil {
		return "", ErrAuthorizationFailed
	}
	var payload struct {
		Code        int    `json:"code"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil || payload.Code != 0 || payload.Error != "" || payload.AccessToken == "" {
		return "", ErrAuthorizationFailed
	}
	return payload.AccessToken, nil
}

// User 是授权用户的身份。
type User struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	OpenID    string `json:"open_id"`
	UnionID   string `json:"union_id"`
	TenantKey string `json:"tenant_key"`
}

// UserInfo 用用户令牌读取身份。
func (c *Client) UserInfo(ctx context.Context, userToken string) (User, error) {
	status, _, data, err := c.call(ctx, http.MethodGet, c.BaseURL+"/open-apis/authen/v1/user_info", userToken, nil, nil)
	if err != nil {
		return User{}, ErrAuthorizationFailed
	}
	var payload struct {
		Code int  `json:"code"`
		Data User `json:"data"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil || payload.Code != 0 || payload.Data.OpenID == "" {
		return User{}, ErrAuthorizationFailed
	}
	return payload.Data, nil
}

// ---------- 扫码创建应用（设备码流程）----------

var (
	// ErrRegistrationUnsupported 表示飞书注册接口不支持 client_secret 认证方式，无法扫码创建应用。
	ErrRegistrationUnsupported = errors.New("feishu: app registration is unavailable")
	// ErrRegistrationPending 表示用户还没有在飞书里确认，继续轮询。
	ErrRegistrationPending = errors.New("feishu: registration pending")
	// ErrRegistrationDenied 表示用户在飞书里拒绝了授权。
	ErrRegistrationDenied = errors.New("feishu: registration denied")
	// ErrRegistrationExpired 表示设备码已过期。
	ErrRegistrationExpired = errors.New("feishu: registration expired")
	// ErrRegistrationRejected 表示飞书拒绝了注册（其他错误码或不支持的租户品牌）。
	ErrRegistrationRejected = errors.New("feishu: registration rejected")
)

// SlowDownError 表示轮询过快：调用方把间隔增加 Extra 秒。
type SlowDownError struct{ Extra int }

func (e *SlowDownError) Error() string { return "feishu: registration slow down" }

// RegistrationBegin 是开始扫码后飞书返回的值。
type RegistrationBegin struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpireIn                int    `json:"expire_in"`
}

// RegistrationResult 是扫码确认后新建的应用凭证。
type RegistrationResult struct {
	ClientID     string
	ClientSecret string
	OpenID       string
	TenantBrand  string
}

// registrationCall 发送 form 请求并返回 HTTP 状态与 JSON 体（4xx 也带 JSON 体）。
func (c *Client) registrationCall(ctx context.Context, form url.Values) (int, map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AccountsURL+"/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "OpenFox")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, nil, ErrUnavailable
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return resp.StatusCode, nil, ErrUnavailable
	}
	return resp.StatusCode, payload, nil
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func intField(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

// RegistrationInit 确认注册接口可用且支持 client_secret 认证方式。
func (c *Client) RegistrationInit(ctx context.Context) error {
	_, payload, err := c.registrationCall(ctx, url.Values{"action": {"init"}})
	if err != nil {
		return err
	}
	methods, _ := payload["supported_auth_methods"].([]any)
	for _, m := range methods {
		if s, _ := m.(string); s == "client_secret" {
			return nil
		}
	}
	return ErrRegistrationUnsupported
}

// RegistrationBeginFlow 开始设备码流程（archetype=PersonalAgent）。
func (c *Client) RegistrationBeginFlow(ctx context.Context) (RegistrationBegin, error) {
	status, payload, err := c.registrationCall(ctx, url.Values{"action": {"begin"}, "archetype": {"PersonalAgent"},
		"auth_method": {"client_secret"}, "request_user_info": {"open_id"}})
	if err != nil {
		return RegistrationBegin{}, err
	}
	out := RegistrationBegin{DeviceCode: stringField(payload, "device_code"), UserCode: stringField(payload, "user_code"),
		VerificationURI: stringField(payload, "verification_uri"), VerificationURIComplete: stringField(payload, "verification_uri_complete"),
		Interval: intField(payload, "interval"), ExpireIn: intField(payload, "expire_in")}
	if status != http.StatusOK || out.DeviceCode == "" || (out.VerificationURIComplete == "" && out.VerificationURI == "") {
		return RegistrationBegin{}, ErrUnavailable
	}
	if out.VerificationURIComplete == "" {
		out.VerificationURIComplete = out.VerificationURI
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	if out.ExpireIn <= 0 {
		out.ExpireIn = 600
	}
	return out, nil
}

// RegistrationPoll 轮询确认结果：成功返回凭证；等待中返回 ErrRegistrationPending；
// slow_down 返回 *SlowDownError；其余分别映射为 Denied / Expired / Rejected。
func (c *Client) RegistrationPoll(ctx context.Context, deviceCode string) (RegistrationResult, error) {
	_, payload, err := c.registrationCall(ctx, url.Values{"action": {"poll"}, "device_code": {deviceCode}})
	if err != nil {
		return RegistrationResult{}, err
	}
	if code := stringField(payload, "error"); code != "" {
		switch code {
		case "authorization_pending":
			return RegistrationResult{}, ErrRegistrationPending
		case "slow_down":
			return RegistrationResult{}, &SlowDownError{Extra: 5}
		case "access_denied":
			return RegistrationResult{}, ErrRegistrationDenied
		case "expired_token":
			return RegistrationResult{}, ErrRegistrationExpired
		}
		return RegistrationResult{}, ErrRegistrationRejected
	}
	result := RegistrationResult{ClientID: stringField(payload, "client_id"), ClientSecret: stringField(payload, "client_secret")}
	if info, ok := payload["user_info"].(map[string]any); ok {
		result.OpenID = stringField(info, "open_id")
		result.TenantBrand = stringField(info, "tenant_brand")
	}
	if result.ClientID == "" || result.ClientSecret == "" {
		return RegistrationResult{}, ErrRegistrationRejected
	}
	// 品牌由持有当前服务商身份的调用方核对；本客户端同时服务飞书与 Lark。
	return result, nil
}

// ---------- 群消息与回复 ----------

var (
	// ErrNoPermission 表示应用缺少读取群消息的权限（im:message.group_msg）。
	ErrNoPermission = errors.New("feishu: missing permission")
	// ErrMessageGone 表示被回复的消息已撤回或不存在，调用方改发群消息。
	ErrMessageGone = errors.New("feishu: message not found")
)

const (
	codeMessageRecalled = 230011
	codeMessageNotFound = 230020
	codeNoPermission    = 99991672
	codeNoPermissionAlt = 230027
)

// sendMessage 是 SendChatCard / ReplyCard 共用的请求与响应处理。
func (c *Client) sendMessage(ctx context.Context, method, target, token string, body any) (string, error) {
	_, header, data, err := c.call(ctx, method, target, token, body, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return "", ErrUnavailable
	}
	if payload.Code != 0 {
		if payload.Code == codeMessageRecalled || payload.Code == codeMessageNotFound {
			return "", &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrMessageGone}
		}
		if err := classifyCode(payload.Code, payload.Msg, header); err != nil {
			return "", err
		}
		return "", &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrUnavailable}
	}
	if payload.Data.MessageID == "" {
		return "", ErrUnavailable
	}
	return payload.Data.MessageID, nil
}

// SendChatCard 给会话（单聊 / 群）发送交互卡片；uuid 用于飞书 1 小时内去重。返回 message_id。
func (c *Client) SendChatCard(ctx context.Context, token, chatID, cardJSON, uuid string) (string, error) {
	body := map[string]string{"receive_id": chatID, "msg_type": "interactive", "content": cardJSON, "uuid": uuid}
	return c.sendMessage(ctx, http.MethodPost, c.BaseURL+"/open-apis/im/v1/messages?receive_id_type=chat_id", token, body)
}

// ReplyCard 回复一条消息；被回复消息已撤回或不存在时返回 ErrMessageGone。
func (c *Client) ReplyCard(ctx context.Context, token, messageID, cardJSON, uuid string) (string, error) {
	body := map[string]string{"msg_type": "interactive", "content": cardJSON, "uuid": uuid}
	return c.sendMessage(ctx, http.MethodPost, c.BaseURL+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", token, body)
}

// ChatMessage 是读取到的一条群消息（只保留任务助手需要的字段）。
type ChatMessage struct {
	MessageID  string `json:"message_id"`
	MsgType    string `json:"msg_type"`
	CreateTime string `json:"create_time"`
	Sender     struct {
		ID         string `json:"id"`
		IDType     string `json:"id_type"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []struct {
		Key  string `json:"key"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"mentions"`
}

// ListChatMessages 读取会话最近的消息（按时间倒序，最多 pageSize 条）。缺少权限返回 ErrNoPermission。
func (c *Client) ListChatMessages(ctx context.Context, token, chatID string, pageSize int) ([]ChatMessage, error) {
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 20
	}
	query := url.Values{"container_id_type": {"chat"}, "container_id": {chatID}, "sort_type": {"ByCreateTimeDesc"},
		"page_size": {strconv.Itoa(pageSize)}}
	status, header, data, err := c.call(ctx, http.MethodGet, c.BaseURL+"/open-apis/im/v1/messages?"+query.Encode(), token, nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []ChatMessage `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return nil, ErrUnavailable
	}
	if payload.Code != 0 {
		if payload.Code == codeNoPermission || payload.Code == codeNoPermissionAlt || status == http.StatusForbidden {
			return nil, &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrNoPermission}
		}
		if err := classifyCode(payload.Code, payload.Msg, header); err != nil {
			return nil, err
		}
		return nil, &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrUnavailable}
	}
	return payload.Data.Items, nil
}
