package market

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// SearchText 生成写入 search_text 的小写文本：各语言名称 + 描述 + 标签，超过上限时按字符截断。
func SearchText(name, description Text, tags []Text, extra ...string) string {
	var parts []string
	for _, text := range append([]Text{name, description}, tags...) {
		for _, key := range sortedKeys(text) {
			if value := strings.TrimSpace(text[key]); value != "" {
				parts = append(parts, value)
			}
		}
	}
	for _, value := range extra {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	return truncateRunes(strings.ToLower(strings.Join(parts, "\n")), MaxSearchRunes)
}

func sortedKeys(text Text) []string {
	keys := make([]string, 0, len(text))
	for key := range text {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

// IsText 判断内容是否为文本：前 8 KB 能按 UTF-8 解码且不含 NUL。
func IsText(content []byte) bool {
	head := content
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
		// 截断点可能落在多字节字符中间：去掉末尾不完整的字符再判断。
		for i := 0; i < utf8.UTFMax && len(head) > 0; i++ {
			if r, size := utf8.DecodeLastRune(head); r == utf8.RuneError && size <= 1 {
				head = head[:len(head)-1]
				continue
			}
			break
		}
	}
	return utf8.Valid(head) && !bytes.ContainsRune(head, 0)
}

// Preview 返回预览内容：超过 PreviewBytes 时只取前 PreviewBytes 字节（落在字符边界上）并返回 truncated = true。
func Preview(content []byte) (string, bool) {
	if len(content) <= PreviewBytes {
		return string(content), false
	}
	head := content[:PreviewBytes]
	for i := 0; i < utf8.UTFMax && len(head) > 0; i++ {
		if r, size := utf8.DecodeLastRune(head); r == utf8.RuneError && size <= 1 {
			head = head[:len(head)-1]
			continue
		}
		break
	}
	return string(head), true
}

// NormalizeName 把市场技能 ID 的末段规范化为合规的技能名称：小写、非法字符变 -、截 64。
func NormalizeName(marketID string) string {
	last := marketID
	if i := strings.LastIndex(last, "."); i >= 0 && i < len(last)-1 {
		last = last[i+1:]
	}
	var builder strings.Builder
	for _, r := range strings.ToLower(last) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('-')
		}
	}
	name := strings.TrimLeft(builder.String(), "-")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "skill"
	}
	return name
}

// pick 取当前语言的文本，缺失时取另一种（先 zh-Hans，再 en，再任意）。
func pick(text Text) string {
	for _, key := range []string{"zh-Hans", "en"} {
		if value := strings.TrimSpace(text[key]); value != "" {
			return value
		}
	}
	for _, key := range sortedKeys(text) {
		if value := strings.TrimSpace(text[key]); value != "" {
			return value
		}
	}
	return ""
}

// cursorPayload 是不透明游标的内容（base64 JSON）。A / B / T / ID 是排序键，含义随 Sort 而定。
type cursorPayload struct {
	Sort string `json:"s"`
	A    int64  `json:"a"`
	B    int64  `json:"b"`
	T    string `json:"t"`
	ID   string `json:"i"`
}

// EncodeCursor 把游标编码为 URL 安全的字符串。
func EncodeCursor(payload cursorPayload) string {
	raw, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor 解码游标；格式不对返回 ErrInvalidCursor。
func DecodeCursor(value string) (cursorPayload, error) {
	var payload cursorPayload
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &payload) != nil || payload.ID == "" || !utf8.ValidString(payload.T) {
		return cursorPayload{}, ErrInvalidCursor
	}
	return payload, nil
}
