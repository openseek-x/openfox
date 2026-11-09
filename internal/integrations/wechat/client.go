// Package wechat 是腾讯官方 ClawBot（iLink）协议的最小客户端：扫码登录、长轮询收消息、发送文本。
//
// 协议出处（实现依据，真实环境未验证）：
//   - https://github.com/Tencent/openclaw-weixin 的 docs/protocol.md、src/api/api.ts、src/api/types.ts、src/auth/login-qr.ts
//   - https://developers.weixin.qq.com/doc/aispeech/knowledge/openapi/Clawbotrelated.html
//
// 凭据（bot_token）只放在 Authorization 头里，错误信息与日志里不出现令牌、二维码标识和消息内容。
package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL 是 iLink 的默认基址。
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"
	// BotType 是 get_bot_qrcode 的 bot_type（协议文档固定为 3）。
	BotType = "3"

	// 官方示例客户端按 (major<<16 | minor<<8 | patch) 编码版本号放进 iLink-App-ClientVersion；OpenFox 以 1.0.0 上报。
	clientVersionHeader = "65536"
	appID               = "bot"

	maxResponseBytes = 4 << 20
	// DefaultPollTimeout 是服务端未给出 longpolling_timeout_ms 时的长轮询超时。
	DefaultPollTimeout = 35 * time.Second
	// StatusTimeout 是单次 get_qrcode_status 的超时上限（官方约 35 秒长轮询，这里取 25 秒，前端再轮询）。
	StatusTimeout = 25 * time.Second
	callTimeout   = 15 * time.Second
	pollMargin    = 10 * time.Second

	// SessionExpiredCode 是 ret / errcode 里表示会话失效的值。
	SessionExpiredCode = -14
)

var (
	ErrNotConfigured  = errors.New("wechat: client is not configured")
	ErrSessionExpired = errors.New("wechat: session expired")
	ErrUnavailable    = errors.New("wechat: upstream unavailable")
	ErrBadBaseURL     = errors.New("wechat: base url is not allowed")
)

// APIError 是 ret 非 0 的业务错误。
type APIError struct {
	Ret     int
	ErrCode int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wechat: api error ret=%d errcode=%d", e.Ret, e.ErrCode)
}

// Client 调用 iLink。零值不可用，用 New 构造。
type Client struct {
	// BaseURL 是配置的基址（OPENFOX_WECHAT_ILINK_BASE_URL）。
	BaseURL string
	// AllowConfiguredBase 为 true（开发 / 测试环境）时，服务端返回的基址或重定向主机可以等于配置的基址。
	AllowConfiguredBase bool
	// BotAgent 写入 base_info.bot_agent，形如 OpenFox/<版本>。
	BotAgent string
	HTTP     *http.Client
}

// New 构造客户端；baseURL 为空时使用默认基址。
func New(baseURL, version string, allowConfiguredBase bool) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if version == "" {
		version = "dev"
	}
	return &Client{BaseURL: baseURL, AllowConfiguredBase: allowConfiguredBase, BotAgent: "OpenFox/" + version, HTTP: &http.Client{}}
}

// NormalizeBase 校验服务端返回的基址（baseurl）：必须是 https、无用户信息，主机为 weixin.qq.com 或其子域；
// 开发 / 测试环境另允许与配置的基址同源。返回规范化后的 origin（scheme://host[:port]），不合规返回 ErrBadBaseURL。
func (c *Client) NormalizeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return "", ErrBadBaseURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return "", ErrBadBaseURL
	}
	origin := u.Scheme + "://" + strings.ToLower(u.Host)
	if c != nil && c.AllowConfiguredBase {
		if cfg, err := url.Parse(c.BaseURL); err == nil && strings.EqualFold(cfg.Scheme+"://"+cfg.Host, origin) {
			return origin, nil
		}
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.Port() != "" && u.Port() != "443" {
		return "", ErrBadBaseURL
	}
	if host != "weixin.qq.com" && !strings.HasSuffix(host, ".weixin.qq.com") {
		return "", ErrBadBaseURL
	}
	return "https://" + host, nil
}

func (c *Client) base(override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	return c.BaseURL
}

func randomUIN() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 10)))
}

func (c *Client) baseInfo() map[string]string {
	return map[string]string{"channel_version": "1.0.0", "bot_agent": c.BotAgent}
}

func (c *Client) do(ctx context.Context, timeout time.Duration, method, base, path string, query url.Values, token string, body any, out any) error {
	if c == nil || c.HTTP == nil {
		return ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := c.base(base) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("iLink-App-Id", appID)
	req.Header.Set("iLink-App-ClientVersion", clientVersionHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-WECHAT-UIN", randomUIN())
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 不带 err 原文：url.Error 里有请求地址（含二维码标识）。
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ErrUnavailable
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrSessionExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrUnavailable
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return ErrUnavailable
	}
	return nil
}

// QRCode 是 get_bot_qrcode 的结果。
type QRCode struct {
	Code    string
	Content string
}

type qrcodeResponse struct {
	Ret           int    `json:"ret"`
	ErrCode       int    `json:"errcode"`
	QRCode        string `json:"qrcode"`
	QRCodeContent string `json:"qrcode_img_content"`
}

// GetBotQRCode 申请登录二维码。官方仓库是 POST（body {"local_token_list":[]}），社区文档写 GET，以官方为准。
func (c *Client) GetBotQRCode(ctx context.Context) (QRCode, error) {
	var out qrcodeResponse
	err := c.do(ctx, callTimeout, http.MethodPost, "", "/ilink/bot/get_bot_qrcode", url.Values{"bot_type": {BotType}}, "",
		map[string]any{"local_token_list": []string{}}, &out)
	if err != nil {
		return QRCode{}, err
	}
	if out.Ret != 0 || out.ErrCode != 0 || out.QRCode == "" || out.QRCodeContent == "" {
		return QRCode{}, ErrUnavailable
	}
	return QRCode{Code: out.QRCode, Content: out.QRCodeContent}, nil
}

// 扫码状态（get_qrcode_status 的 status）。
const (
	QRWait              = "wait"
	QRScanned           = "scaned"
	QRConfirmed         = "confirmed"
	QRExpired           = "expired"
	QRNeedVerifyCode    = "need_verifycode"
	QRVerifyCodeBlocked = "verify_code_blocked"
	QRScanedButRedirect = "scaned_but_redirect"
	QRBindedRedirect    = "binded_redirect"
)

// QRStatus 是 get_qrcode_status 的结果。BotToken 只在 confirmed 时有值，调用方负责加密保存，不得记录。
type QRStatus struct {
	Status       string
	BotToken     string
	BotID        string
	BaseURL      string
	UserID       string
	RedirectHost string
}

type qrStatusResponse struct {
	Ret          int    `json:"ret"`
	ErrCode      int    `json:"errcode"`
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	UserID       string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"`
}

// GetQRCodeStatus 查询扫码状态（服务端长轮询）。base 为空使用配置基址；调用方必须只传入已通过 NormalizeBase 的值。
func (c *Client) GetQRCodeStatus(ctx context.Context, base, qrcode, verifyCode string) (QRStatus, error) {
	q := url.Values{"qrcode": {qrcode}}
	if verifyCode != "" {
		q.Set("verify_code", verifyCode)
	}
	var out qrStatusResponse
	if err := c.do(ctx, StatusTimeout+5*time.Second, http.MethodGet, base, "/ilink/bot/get_qrcode_status", q, "", nil, &out); err != nil {
		return QRStatus{}, err
	}
	if out.Ret != 0 || out.ErrCode != 0 || out.Status == "" {
		return QRStatus{}, ErrUnavailable
	}
	return QRStatus{Status: out.Status, BotToken: out.BotToken, BotID: out.BotID, BaseURL: out.BaseURL, UserID: out.UserID, RedirectHost: out.RedirectHost}, nil
}

// Item 是消息内容项（只处理文本，type = 1）。
type Item struct {
	Type     int `json:"type"`
	TextItem *struct {
		Text string `json:"text"`
	} `json:"text_item,omitempty"`
}

// Message 是 getupdates 返回的消息（WeixinMessage）。
type Message struct {
	MessageID    json.Number `json:"message_id"`
	FromUserID   string      `json:"from_user_id"`
	ToUserID     string      `json:"to_user_id"`
	MessageType  int         `json:"message_type"`
	MessageState int         `json:"message_state"`
	ItemList     []Item      `json:"item_list"`
	ContextToken string      `json:"context_token"`
	CreateTimeMs int64       `json:"create_time_ms"`
	GroupID      string      `json:"group_id"`
}

// Text 拼接全部文本项；没有文本项时第二个返回值为 false。
func (m Message) Text() (string, bool) {
	var parts []string
	for _, item := range m.ItemList {
		if item.Type == 1 && item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
			parts = append(parts, item.TextItem.Text)
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// Updates 是 getupdates 的结果。
type Updates struct {
	Messages  []Message
	Buf       string
	TimeoutMs int
}

type updatesResponse struct {
	Ret       int       `json:"ret"`
	ErrCode   int       `json:"errcode"`
	Msgs      []Message `json:"msgs"`
	Buf       string    `json:"get_updates_buf"`
	TimeoutMs int       `json:"longpolling_timeout_ms"`
}

// GetUpdates 长轮询收消息。单次请求超时 = timeout + 余量；超时（服务端无新消息）按空结果返回，游标不变。
// ret 或 errcode 为 -14 返回 ErrSessionExpired。
func (c *Client) GetUpdates(ctx context.Context, base, token, buf string, timeout time.Duration) (Updates, error) {
	if timeout <= 0 {
		timeout = DefaultPollTimeout
	}
	var out updatesResponse
	err := c.do(ctx, timeout+pollMargin, http.MethodPost, base, "/ilink/bot/getupdates", nil, token,
		map[string]any{"get_updates_buf": buf, "base_info": c.baseInfo()}, &out)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return Updates{Buf: buf}, nil
	}
	if err != nil {
		return Updates{}, err
	}
	if out.Ret == SessionExpiredCode || out.ErrCode == SessionExpiredCode {
		return Updates{}, ErrSessionExpired
	}
	if out.Ret != 0 || out.ErrCode != 0 {
		return Updates{}, &APIError{Ret: out.Ret, ErrCode: out.ErrCode}
	}
	next := out.Buf
	if next == "" {
		next = buf
	}
	return Updates{Messages: out.Msgs, Buf: next, TimeoutMs: out.TimeoutMs}, nil
}

type sendResponse struct {
	Ret     int `json:"ret"`
	ErrCode int `json:"errcode"`
}

// SendText 发送一条文本消息；clientID 唯一（重试时保持不变以幂等），contextToken 取最近一条入站消息的 context_token。
func (c *Client) SendText(ctx context.Context, base, token, toUserID, text, contextToken, clientID string) error {
	msg := map[string]any{
		"from_user_id":  "",
		"to_user_id":    toUserID,
		"client_id":     clientID,
		"message_type":  2,
		"message_state": 2,
		"context_token": contextToken,
		"item_list":     []any{map[string]any{"type": 1, "text_item": map[string]string{"text": text}}},
	}
	var out sendResponse
	if err := c.do(ctx, callTimeout, http.MethodPost, base, "/ilink/bot/sendmessage", nil, token,
		map[string]any{"msg": msg, "base_info": c.baseInfo()}, &out); err != nil {
		return err
	}
	if out.Ret == SessionExpiredCode || out.ErrCode == SessionExpiredCode {
		return ErrSessionExpired
	}
	if out.Ret != 0 || out.ErrCode != 0 {
		return &APIError{Ret: out.Ret, ErrCode: out.ErrCode}
	}
	return nil
}
