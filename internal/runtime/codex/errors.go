package codex

import (
	"encoding/json"
	"strings"

	"openfox/internal/runtime"
)

// maxFailureDetail 限制失败事件 payload 中保留的原文长度（字节）。
const maxFailureDetail = 2048

// readableFailure 把 Codex 错误文本中内嵌的上游 JSON（例如
// `unexpected status 400 Bad Request: {"error":{"message":"...","code":"model_not_found"}}`）
// 提炼为一行可读说明：`前缀: message (code)`。无法识别时原样返回，changed=false。
func readableFailure(text string) (summary string, changed bool) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return text, false
	}
	var body map[string]any
	// 只解码第一个 JSON 对象，允许其后还有其他文本。
	if err := json.NewDecoder(strings.NewReader(text[start:])).Decode(&body); err != nil {
		return text, false
	}
	message, code := upstreamMessage(body)
	if message == "" {
		return text, false
	}
	if code != "" && !strings.Contains(message, code) {
		message += " (" + code + ")"
	}
	prefix := strings.TrimRight(strings.TrimSpace(text[:start]), ": ")
	if prefix == "" {
		return message, true
	}
	return prefix + ": " + message, true
}

// upstreamMessage 依次识别 OpenAI 风格 {"error":{"message","code","type"}}、
// 扁平 {"message","code"} 与 {"detail": "..."}。
func upstreamMessage(body map[string]any) (message, code string) {
	if nested, ok := body["error"].(map[string]any); ok {
		message, code = stringField(nested, "message"), stringField(nested, "code")
		if code == "" {
			code = stringField(nested, "type")
		}
		if message != "" {
			return message, code
		}
	}
	if text, ok := body["error"].(string); ok && text != "" {
		return text, stringField(body, "code")
	}
	if message = stringField(body, "message"); message != "" {
		return message, stringField(body, "code")
	}
	return stringField(body, "detail"), stringField(body, "code")
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

// failureEvent 构造失败/告警事件：Text 是提炼后的说明（会进入 Run 结果摘要），
// 提炼生效时在 payload.detail 中保留截断后的原文（已脱敏）以便排查。
func failureEvent(eventType runtime.EventType, redacted string) runtime.Event {
	summary, changed := readableFailure(redacted)
	event := runtime.Event{Type: eventType, Text: summary}
	if changed {
		event.Payload = mustJSON(map[string]string{"detail": boundBytes(redacted, maxFailureDetail)})
	}
	return event
}

// boundBytes 按字节截断且不切断 UTF-8 字符。
func boundBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// failureCodeFromInfo 把 app-server 的 codexErrorInfo（结构化枚举）映射为失败码。
// 枚举值可能是字符串（"usageLimitExceeded"）或单键对象（{"httpConnectionFailed":{"httpStatusCode":429}}），
// 大小写风格随协议版本不同（camelCase / PascalCase），按去掉大小写后的精确名称比较，不匹配自然语言文本。
// 阶段 12b：本机未安装 codex，该映射依据公开协议定义，未在真实 CLI 上验证。
func failureCodeFromInfo(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return failureCodeForInfoName(name, nil)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return ""
	}
	for key, value := range object {
		var detail struct {
			HTTPStatusCode *int `json:"httpStatusCode"`
		}
		_ = json.Unmarshal(value, &detail)
		return failureCodeForInfoName(key, detail.HTTPStatusCode)
	}
	return ""
}

func failureCodeForInfoName(name string, status *int) string {
	switch strings.ToLower(name) {
	case "unauthorized":
		return runtime.FailureCredentialRejected
	case "usagelimitexceeded":
		return runtime.FailureQuotaExhausted
	case "serveroverloaded":
		return runtime.FailureProviderOverloaded
	case "contextwindowexceeded":
		return runtime.FailureContextTooLarge
	case "httpconnectionfailed", "responsestreamconnectionfailed", "responsestreamdisconnected":
		// 带 HTTP 状态码时按状态码归类（如 429 限流）；没有状态码的连接失败视为网络不可达。
		if status != nil {
			return runtime.FailureCodeForHTTPStatus(*status)
		}
		return runtime.FailureNetworkUnreachable
	}
	return ""
}

// failureCodeFromUpstream 读取错误文本内嵌的上游 JSON 的结构化 code 字段（如 "model_not_found"）。
// 只做精确的字段值匹配（OpenAI 风格错误码），不对 message 做文本匹配。
func failureCodeFromUpstream(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	var body map[string]any
	if err := json.NewDecoder(strings.NewReader(text[start:])).Decode(&body); err != nil {
		return ""
	}
	var code string
	if nested, ok := body["error"].(map[string]any); ok {
		code = stringField(nested, "code")
	} else {
		code = stringField(body, "code")
	}
	switch code {
	case "model_not_found":
		return runtime.FailureModelNotFound
	case "invalid_api_key":
		return runtime.FailureCredentialRejected
	case "insufficient_quota":
		return runtime.FailureQuotaExhausted
	case "rate_limit_exceeded":
		return runtime.FailureRateLimited
	case "context_length_exceeded":
		return runtime.FailureContextTooLarge
	}
	return ""
}
