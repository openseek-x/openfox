package devices

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// 安装令牌 v2：`ofx_v2_` + base64url（无填充）编码的 JSON {"v":2,"c":"<中心 URL>","s":"<注册令牌明文>"}。
// 中心只存 sha256(s)；c 只给 CLI 定位中心用，中心兑换时不校验它。CLI / daemon 与中心共用本文件的编解码。
const (
	InstallTokenPrefix    = "ofx_v2_"
	InstallTokenVersion   = 2
	MaxInstallTokenLength = 512
	maxInstallSecretBytes = 128
)

// ErrInvalidInstallToken 表示安装令牌格式不对。错误信息里不包含令牌本身。
var ErrInvalidInstallToken = errors.New("devices: invalid install token")

// InstallToken 是解码后的 v2 安装令牌。Secret 是注册令牌明文，不得写日志。
type InstallToken struct {
	CenterURL string
	Secret    string
}

type installTokenPayload struct {
	V int    `json:"v"`
	C string `json:"c"`
	S string `json:"s"`
}

// IsInstallToken 只看前缀，判断字符串是否按 v2 格式解析。
func IsInstallToken(value string) bool {
	return strings.HasPrefix(value, InstallTokenPrefix)
}

// EncodeInstallToken 把中心 URL 与注册令牌明文编码为 v2 安装令牌。centerURL 必须是 http(s) 绝对地址，末尾的 / 会去掉。
func EncodeInstallToken(centerURL, secret string) (string, error) {
	centerURL, err := normalizeInstallCenterURL(centerURL)
	if err != nil {
		return "", err
	}
	if !validInstallSecret(secret) {
		return "", ErrInvalidInstallToken
	}
	payload, err := json.Marshal(installTokenPayload{V: InstallTokenVersion, C: centerURL, S: secret})
	if err != nil {
		return "", ErrInvalidInstallToken
	}
	token := InstallTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
	if len(token) > MaxInstallTokenLength {
		return "", ErrInvalidInstallToken
	}
	return token, nil
}

// DecodeInstallToken 解析 v2 安装令牌并校验版本、中心 URL 与令牌明文的形状。
func DecodeInstallToken(token string) (InstallToken, error) {
	if len(token) > MaxInstallTokenLength || !IsInstallToken(token) {
		return InstallToken{}, ErrInvalidInstallToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, InstallTokenPrefix))
	if err != nil {
		return InstallToken{}, ErrInvalidInstallToken
	}
	var payload installTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.V != InstallTokenVersion {
		return InstallToken{}, ErrInvalidInstallToken
	}
	centerURL, err := normalizeInstallCenterURL(payload.C)
	if err != nil || !validInstallSecret(payload.S) {
		return InstallToken{}, ErrInvalidInstallToken
	}
	return InstallToken{CenterURL: centerURL, Secret: payload.S}, nil
}

// RegistrationSecret 返回兑换时用于计算哈希的注册令牌明文：v2 解出 s，其他按旧的纯令牌原样返回。
func RegistrationSecret(token string) (string, error) {
	if !IsInstallToken(token) {
		return token, nil
	}
	decoded, err := DecodeInstallToken(token)
	if err != nil {
		return "", err
	}
	return decoded.Secret, nil
}

// NormalizeCenterURL 是安装令牌里中心地址的归一化规则，供「当前中心地址」接口复用。
func NormalizeCenterURL(value string) (string, error) { return normalizeInstallCenterURL(value) }

func normalizeInstallCenterURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidInstallToken
	}
	return value, nil
}

func validInstallSecret(secret string) bool {
	return secret != "" && len(secret) <= maxInstallSecretBytes &&
		strings.IndexFunc(secret, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
