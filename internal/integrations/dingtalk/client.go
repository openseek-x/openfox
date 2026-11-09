// Package dingtalk 是钉钉开放平台 REST 接口、个人 OAuth 与 Stream 长连接的最小客户端。
//
// 依据官方文档（均为企业内部应用）：
//   - 获取应用 accessToken：POST {api}/v1.0/oauth2/accessToken {appKey, appSecret} → {accessToken, expireIn}
//     https://open.dingtalk.com/document/development/obtain-the-access-token-of-an-internal-app
//   - 获取用户 token：POST {api}/v1.0/oauth2/userAccessToken {clientId, clientSecret, code, grantType}
//     https://open.dingtalk.com/document/development/obtain-user-token
//   - 用户通讯录个人信息：GET {api}/v1.0/contact/users/me（头 x-acs-dingtalk-access-token）→ nick/avatarUrl/openId/unionId
//     https://open.dingtalk.com/document/personalapp/retrieve-user-information
//   - 网页登录授权页：{login}/oauth2/auth?client_id&redirect_uri&response_type=code&scope=openid&state&prompt=consent
//     https://open.dingtalk.com/document/development/tutorial-obtaining-user-personal-information
//   - unionId → userid：POST {oapi}/topapi/user/getbyunionid?access_token=
//     https://open.dingtalk.com/document/org/scan-qr-code-to-login-3rdapp
//   - 员工详情（userid → unionid、corp_id）：POST {oapi}/topapi/v2/user/get?access_token= {userid, language}
//     https://open.dingtalk.com/document/development/query-user-details
//   - 机器人单聊批量发送：POST {api}/v1.0/robot/oToMessages/batchSend
//     https://open.dingtalk.com/document/isvapp/chatbots-send-one-on-one-chat-messages-in-batches
//   - 机器人群聊发送：POST {api}/v1.0/robot/groupMessages/send
//     https://open.dingtalk.com/document/orgapp/types-of-messages-sent-by-robots
//
// 地址来自运维配置而不是用户输入。日志与错误信息不得包含 AppSecret、accessToken、用户令牌和授权 code。
package dingtalk

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
	"strings"
	"sync"
	"time"
)

const (
	connectTimeout = 10 * time.Second
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 20
	// tokenEarlyExpiry 是 accessToken 提前过期的时间（官方建议缓存时间略小于 7200 秒）。
	tokenEarlyExpiry = 5 * time.Minute
	// defaultRateLimitWait 是限流响应没有给出恢复时间时的等待。
	defaultRateLimitWait = 30 * time.Second
)

var (
	// ErrInvalidCredentials 表示 AppKey / AppSecret 无效（或应用被停用）。
	ErrInvalidCredentials = errors.New("dingtalk: invalid app credentials")
	// ErrTokenExpired 表示 accessToken 已失效：调用方清除缓存后可重试一次。
	ErrTokenExpired = errors.New("dingtalk: token expired")
	// ErrUserUnreachable 表示机器人无法给该用户发消息（不在应用可用范围、已离职等），重试没有意义。
	ErrUserUnreachable = errors.New("dingtalk: user is unreachable")
	// ErrUnavailable 表示网络错误、超时、5xx 或无法解析的响应，可稍后重试。
	ErrUnavailable = errors.New("dingtalk: service unavailable")
	// ErrAuthorizationFailed 表示授权码换取用户令牌或读取用户信息失败。
	ErrAuthorizationFailed = errors.New("dingtalk: authorization failed")
	// ErrRateLimited 是限流的哨兵错误；需要等待时间时用 errors.As 取 *RateLimitedError。
	ErrRateLimited = errors.New("dingtalk: rate limited")
)

// RateLimitedError 表示触发限流，RetryAfter 为建议等待时间。
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "dingtalk: rate limited" }

// Is 让 errors.Is(err, ErrRateLimited) 成立。
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// APIError 带钉钉返回的错误码，用于诊断；Msg 来自钉钉，不含凭据。
type APIError struct {
	Code string
	Msg  string
	base error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%v (code %s: %s)", e.base, e.Code, truncate(e.Msg, 200))
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
	// APIURL 是新版接口地址（默认 https://api.dingtalk.com），OAPIURL 是旧版接口地址（默认 https://oapi.dingtalk.com），
	// LoginURL 是网页登录授权页（默认 https://login.dingtalk.com）。
	APIURL   string
	OAPIURL  string
	LoginURL string
	// DevURL 是开发者后台地址（配置指引里给用户看的，不参与请求），默认 https://open-dev.dingtalk.com。
	DevURL string
	HTTP   *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	value   string
	expires time.Time
}

// New 构造带超时的客户端：连接 10 秒，整体 10 秒。
func New(apiURL, oapiURL, loginURL string) *Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSHandshakeTimeout: connectTimeout,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{APIURL: strings.TrimRight(apiURL, "/"), OAPIURL: strings.TrimRight(oapiURL, "/"),
		LoginURL: strings.TrimRight(loginURL, "/"), DevURL: "https://open-dev.dingtalk.com", HTTP: &http.Client{Timeout: requestTimeout, Transport: transport}}
}

func cacheKey(appKey, secret string) string {
	sum := sha256.Sum256([]byte(appKey + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// ClearToken 清除某个应用的 accessToken 缓存（收到 ErrTokenExpired 或需要强制重新验证时调用）。
func (c *Client) ClearToken(appKey, secret string) {
	c.mu.Lock()
	delete(c.tokens, cacheKey(appKey, secret))
	c.mu.Unlock()
}

// v1Error 是新版接口的错误体：{"code": "...", "message": "...", "requestid": "..."}。
type v1Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// call 发送 JSON 请求并读取最多 1 MiB 的响应。HTTP 429 返回 *RateLimitedError，5xx 与网络错误返回 ErrUnavailable。
func (c *Client) call(ctx context.Context, method, target string, header map[string]string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: read failed", ErrUnavailable)
	}
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return response.StatusCode, data, &RateLimitedError{RetryAfter: retryAfter(response.Header)}
	case response.StatusCode >= 500:
		return response.StatusCode, data, fmt.Errorf("%w: http status %d", ErrUnavailable, response.StatusCode)
	}
	return response.StatusCode, data, nil
}

func retryAfter(header http.Header) time.Duration {
	if value := header.Get("Retry-After"); value != "" {
		var seconds int
		if _, err := fmt.Sscanf(value, "%d", &seconds); err == nil && seconds > 0 && seconds <= 3600 {
			return time.Duration(seconds) * time.Second
		}
	}
	return defaultRateLimitWait
}

func decodeV1Error(data []byte) v1Error {
	var e v1Error
	_ = json.Unmarshal(data, &e)
	return e
}

// AccessToken 取得（或使用缓存的）应用 accessToken。400 / 401 / 403 视为凭证无效。
func (c *Client) AccessToken(ctx context.Context, appKey, secret string) (string, error) {
	key := cacheKey(appKey, secret)
	c.mu.Lock()
	cached, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.value, nil
	}
	status, data, err := c.call(ctx, http.MethodPost, c.APIURL+"/v1.0/oauth2/accessToken", nil,
		map[string]string{"appKey": appKey, "appSecret": secret})
	if err != nil {
		return "", err
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		e := decodeV1Error(data)
		return "", &APIError{Code: e.Code, Msg: e.Message, base: ErrInvalidCredentials}
	}
	var payload struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil || payload.AccessToken == "" {
		return "", fmt.Errorf("%w: unexpected token response (status %d)", ErrUnavailable, status)
	}
	ttl := time.Duration(payload.ExpireIn)*time.Second - tokenEarlyExpiry
	if ttl < time.Minute {
		ttl = time.Minute
	}
	c.mu.Lock()
	if c.tokens == nil {
		c.tokens = map[string]cachedToken{}
	}
	c.tokens[key] = cachedToken{value: payload.AccessToken, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
	return payload.AccessToken, nil
}

// ---------- 个人 OAuth ----------

// AuthorizeURL 构造网页登录授权页地址（scope=openid，prompt=consent）。
func (c *Client) AuthorizeURL(appKey, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", appKey)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "openid")
	q.Set("state", state)
	q.Set("prompt", "consent")
	return c.LoginURL + "/oauth2/auth?" + q.Encode()
}

// ExchangeCode 用授权码换取用户 accessToken（只用于读取身份，调用方不保存）。
func (c *Client) ExchangeCode(ctx context.Context, appKey, secret, code string) (string, error) {
	status, data, err := c.call(ctx, http.MethodPost, c.APIURL+"/v1.0/oauth2/userAccessToken", nil, map[string]string{
		"clientId": appKey, "clientSecret": secret, "code": code, "grantType": "authorization_code"})
	if err != nil {
		return "", err
	}
	var payload struct {
		AccessToken string `json:"accessToken"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil || payload.AccessToken == "" {
		e := decodeV1Error(data)
		return "", &APIError{Code: e.Code, Msg: e.Message, base: ErrAuthorizationFailed}
	}
	return payload.AccessToken, nil
}

// User 是授权用户的身份。StaffID 是企业内的员工 userid，机器人发单聊消息要用；由 UnionID 经 StaffIDByUnionID 得到。
type User struct {
	Name      string `json:"nick"`
	AvatarURL string `json:"avatarUrl"`
	OpenID    string `json:"openId"`
	UnionID   string `json:"unionId"`
	StaffID   string `json:"-"`
}

// UserInfo 用用户令牌读取身份（unionId 传 me 表示当前授权人）。
func (c *Client) UserInfo(ctx context.Context, userToken string) (User, error) {
	status, data, err := c.call(ctx, http.MethodGet, c.APIURL+"/v1.0/contact/users/me",
		map[string]string{"x-acs-dingtalk-access-token": userToken}, nil)
	if err != nil {
		return User{}, err
	}
	var user User
	if status != http.StatusOK || json.Unmarshal(data, &user) != nil || user.OpenID == "" {
		e := decodeV1Error(data)
		return User{}, &APIError{Code: e.Code, Msg: e.Message, base: ErrAuthorizationFailed}
	}
	return user, nil
}

// StaffIDByUnionID 用应用 accessToken 把 unionId 换成企业内的员工 userid（企业内部应用的员工才有；外部用户返回 ErrUserUnreachable）。
func (c *Client) StaffIDByUnionID(ctx context.Context, token, unionID string) (string, error) {
	status, data, err := c.call(ctx, http.MethodPost, c.OAPIURL+"/topapi/user/getbyunionid?access_token="+url.QueryEscape(token), nil,
		map[string]string{"unionid": unionID})
	if err != nil {
		return "", err
	}
	var payload struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UserID string `json:"userid"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil {
		return "", fmt.Errorf("%w: unexpected getbyunionid response (status %d)", ErrUnavailable, status)
	}
	switch {
	case payload.ErrCode == 0 && payload.Result.UserID != "":
		return payload.Result.UserID, nil
	case isTokenErrCode(payload.ErrCode):
		return "", &APIError{Code: fmt.Sprint(payload.ErrCode), Msg: payload.ErrMsg, base: ErrTokenExpired}
	case payload.ErrCode == 0:
		return "", &APIError{Code: "0", Msg: "no userid", base: ErrUserUnreachable}
	default:
		return "", &APIError{Code: fmt.Sprint(payload.ErrCode), Msg: payload.ErrMsg, base: ErrUserUnreachable}
	}
}

// StaffProfile 是员工详情里用到的字段：unionId（开发者账号范围内唯一）与所属企业 corpId。
type StaffProfile struct {
	UnionID string
	CorpID  string
}

// UnionIDByStaffID 用应用 accessToken 查员工详情（需要「成员信息读权限」qyapi_get_member），得到 unionId 与 corpId。
// 员工 userid 只在一个企业内唯一，入站消息只能靠它换出的 unionId 去匹配 OpenFox 成员。找不到员工返回 ErrUserUnreachable。
func (c *Client) UnionIDByStaffID(ctx context.Context, token, staffID string) (StaffProfile, error) {
	status, data, err := c.call(ctx, http.MethodPost, c.OAPIURL+"/topapi/v2/user/get?access_token="+url.QueryEscape(token), nil,
		map[string]string{"userid": staffID, "language": "zh_CN"})
	if err != nil {
		return StaffProfile{}, err
	}
	var payload struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UnionID string `json:"unionid"`
			CorpID  string `json:"corp_id"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil {
		return StaffProfile{}, fmt.Errorf("%w: unexpected user/get response (status %d)", ErrUnavailable, status)
	}
	switch {
	case payload.ErrCode == 0 && payload.Result.UnionID != "":
		return StaffProfile{UnionID: payload.Result.UnionID, CorpID: payload.Result.CorpID}, nil
	case isTokenErrCode(payload.ErrCode):
		return StaffProfile{}, &APIError{Code: fmt.Sprint(payload.ErrCode), Msg: payload.ErrMsg, base: ErrTokenExpired}
	default:
		return StaffProfile{}, &APIError{Code: fmt.Sprint(payload.ErrCode), Msg: payload.ErrMsg, base: ErrUserUnreachable}
	}
}

// isTokenErrCode 判断旧版接口的 accessToken 失效错误码（40014 不合法的 access_token / 42001 access_token 已过期）。
func isTokenErrCode(code int) bool { return code == 40014 || code == 42001 }

// ---------- 机器人消息 ----------

// SendUserMarkdown 让机器人给一个员工发单聊 Markdown 消息（oToMessages/batchSend，一次一个人）。返回 processQueryKey。
func (c *Client) SendUserMarkdown(ctx context.Context, token, robotCode, staffID, title, text string) (string, error) {
	param, _ := json.Marshal(map[string]string{"title": title, "text": text})
	status, data, err := c.call(ctx, http.MethodPost, c.APIURL+"/v1.0/robot/oToMessages/batchSend",
		map[string]string{"x-acs-dingtalk-access-token": token},
		map[string]any{"robotCode": robotCode, "userIds": []string{staffID}, "msgKey": "sampleMarkdown", "msgParam": string(param)})
	if err != nil {
		return "", err
	}
	var payload struct {
		ProcessQueryKey       string   `json:"processQueryKey"`
		InvalidStaffIDList    []string `json:"invalidStaffIdList"`
		FlowControlledStaffID []string `json:"flowControlledStaffIdList"`
	}
	if status == http.StatusOK && json.Unmarshal(data, &payload) == nil {
		switch {
		case len(payload.InvalidStaffIDList) > 0:
			return "", &APIError{Code: "invalidStaffId", Msg: "staff id is invalid", base: ErrUserUnreachable}
		case len(payload.FlowControlledStaffID) > 0:
			return "", &RateLimitedError{RetryAfter: defaultRateLimitWait}
		}
		return payload.ProcessQueryKey, nil
	}
	return "", classifySend(status, data)
}

// SendGroupMarkdown 让机器人给一个群（openConversationId）发 Markdown 消息。返回 processQueryKey。
func (c *Client) SendGroupMarkdown(ctx context.Context, token, robotCode, conversationID, title, text string) (string, error) {
	param, _ := json.Marshal(map[string]string{"title": title, "text": text})
	status, data, err := c.call(ctx, http.MethodPost, c.APIURL+"/v1.0/robot/groupMessages/send",
		map[string]string{"x-acs-dingtalk-access-token": token},
		map[string]any{"robotCode": robotCode, "openConversationId": conversationID, "msgKey": "sampleMarkdown", "msgParam": string(param)})
	if err != nil {
		return "", err
	}
	var payload struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	if status == http.StatusOK && json.Unmarshal(data, &payload) == nil {
		return payload.ProcessQueryKey, nil
	}
	return "", classifySend(status, data)
}

// classifySend 把发送接口的非 200 响应分类：令牌失效（401 或 InvalidAuthentication）、鉴权失败（凭证无效）、
// 其余 4xx（参数 / 用户不可达）视为不可重试，不会走到这里的 429 / 5xx 已在 call 里处理。
func classifySend(status int, data []byte) error {
	e := decodeV1Error(data)
	switch {
	case status == http.StatusUnauthorized || strings.EqualFold(e.Code, "InvalidAuthentication"):
		return &APIError{Code: e.Code, Msg: e.Message, base: ErrTokenExpired}
	case status == http.StatusForbidden:
		return &APIError{Code: e.Code, Msg: e.Message, base: ErrInvalidCredentials}
	case status >= 400 && status < 500:
		return &APIError{Code: e.Code, Msg: e.Message, base: ErrUserUnreachable}
	}
	return fmt.Errorf("%w: unexpected send response (status %d)", ErrUnavailable, status)
}
