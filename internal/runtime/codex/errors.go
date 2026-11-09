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
