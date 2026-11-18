package sharesnapshots

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// 创建分享快照时的脱敏扫描：只做「发现并阻止」，不做静默替换。命中后调用方把位置返回给用户，由用户修改来源后重试。
// 规则只覆盖格式特征明确的凭据，目标是低误报：普通文本里出现 "password"、"token" 字样但没有具体值时不会命中。

// Finding 是一处命中。Rule 是稳定的规则名（界面按它显示文案）；位置从 1 开始，Item 为 0 表示标题。
// 不返回命中的原文，避免错误响应、日志再次扩散凭据。
type Finding struct {
	Rule   string `json:"rule"`
	Item   int    `json:"item"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type rule struct {
	name    string
	pattern *regexp.Regexp
	// group 为 0 表示整个匹配；大于 0 时只检查并定位该捕获组。
	group int
	// valid 对命中的文本做二次判断，返回 false 视为误报。
	valid func(string) bool
}

var rules = []rule{
	// PEM 私钥块头：RFC 7468 的 PRIVATE KEY 标签（含 RSA / EC / OPENSSH / ENCRYPTED 等前缀）与 PGP 私钥块。
	{name: "private_key_block", pattern: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	// AWS 访问密钥 ID：AKIA / ASIA 加 16 位大写字母数字（AWS IAM 文档的标识符格式）。
	{name: "aws_access_key_id", pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	// GitHub 令牌：ghp_ / gho_ / ghu_ / ghs_ / ghr_ 前缀与 fine-grained 的 github_pat_ 前缀（GitHub 官方令牌格式说明）。
	{name: "github_token", pattern: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{40,255})\b`)},
	// GitLab 个人访问令牌：glpat- 前缀。
	{name: "gitlab_token", pattern: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	// Anthropic API 密钥：sk-ant- 前缀。
	{name: "anthropic_api_key", pattern: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	// OpenAI 风格密钥：sk- 加 32 位以上字母数字，且必须同时含字母与数字，避免命中 "sk-learn" 之类连字符词。
	{name: "openai_api_key", pattern: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}`), valid: hasLetterAndDigit},
	// Slack 令牌：xoxb- / xoxp- / xoxa- / xoxr- / xoxs- 前缀。
	{name: "slack_token", pattern: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	// Google API 密钥：AIza 前缀加 35 位。
	{name: "google_api_key", pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	// Stripe 密钥：sk_live_ / rk_live_ 前缀。
	{name: "stripe_live_key", pattern: regexp.MustCompile(`\b[sr]k_live_[0-9A-Za-z]{16,}`)},
	// JSON Web Token：三段 base64url，前两段以 eyJ 开头（即 JSON 对象的 base64 前缀，RFC 7519）。
	{name: "jwt", pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	// Authorization 请求头里的 Bearer / Basic 凭据（RFC 6750 / 7617）。
	{name: "authorization_header", pattern: regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*["']?(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`)},
	// 单独出现的 Bearer 令牌：长度 20 以上且同时含字母与数字，避免命中 "bearer authentication" 之类的说明文字。
	{name: "bearer_token", pattern: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{20,}=*)`), group: 1, valid: hasLetterAndDigit},
	// 带用户名密码的 URL：scheme://user:password@host（RFC 3986 的 userinfo）。
	{name: "url_with_credentials", pattern: regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@<>"'` + "`" + `]+:([^\s/@<>"'` + "`" + `]{3,})@[^\s<>"'` + "`" + `]+`), group: 1, valid: notPlaceholder},
	// 形如 password=xxx / "api_key": "xxx" 的赋值：值长度 8 以上、含数字或符号、不是变量引用或占位符。
	{name: "credential_assignment", pattern: regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|client[_-]?secret|api[_-]?key|apikey|access[_-]?token|auth[_-]?token|private[_-]?key)["']?\s*[:=]\s*["']?([^\s"',;]{8,})`), group: 1, valid: plausibleSecretValue},
}

func hasLetterAndDigit(text string) bool {
	letter, digit := false, false
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}

var placeholderMarkers = []string{
	"<", "${", "$(", "{{", "%(", "***", "xxx", "...", "your", "example", "placeholder", "redacted", "changeme",
	"process.env", "os.environ", "getenv", "env[", "secrets.", "config.", "settings.", "input(", "none", "null",
}

// notPlaceholder 排除变量引用与示例占位符。
func notPlaceholder(value string) bool {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "$") || strings.HasPrefix(lower, "@") {
		return false
	}
	for _, marker := range placeholderMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// plausibleSecretValue 要求值像一个真实凭据：不是占位符、不是函数调用或属性访问、含数字或符号。
func plausibleSecretValue(value string) bool {
	if !notPlaceholder(value) || strings.ContainsAny(value, "()[]") || looksLikePath(value) {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || strings.ContainsRune("!@#$%^&*-_+=/~", r) {
			return true
		}
	}
	return false
}

var pathExtension = regexp.MustCompile(`\.[A-Za-z0-9]{1,5}$`)

// looksLikePath 排除文件路径（如 private_key: path/to/key.pem）：以 / ./ ~/ 开头，或含目录分隔符且以扩展名结尾。
func looksLikePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") {
		return true
	}
	return strings.Contains(value, "/") && pathExtension.MatchString(value)
}

// maxFindings 限制单次返回的命中数量，避免超大文本放大响应。
const maxFindings = 20

// Scan 扫描一段文本，item 为 0 表示标题，其余为条目序号（从 1 开始）。
func Scan(item int, text string) []Finding {
	var findings []Finding
	for _, r := range rules {
		for _, loc := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := loc[0], loc[1]
			if r.group > 0 {
				start, end = loc[2*r.group], loc[2*r.group+1]
			}
			if start < 0 || (r.valid != nil && !r.valid(text[start:end])) {
				continue
			}
			line, column := position(text, start)
			findings = append(findings, Finding{Rule: r.name, Item: item, Line: line, Column: column})
			if len(findings) >= maxFindings {
				return findings
			}
		}
	}
	return findings
}

// position 返回字节偏移对应的行号与列号（均从 1 开始，列按字符数）。
func position(text string, offset int) (int, int) {
	prefix := text[:offset]
	line := strings.Count(prefix, "\n") + 1
	column := utf8.RuneCountInString(prefix[strings.LastIndex(prefix, "\n")+1:]) + 1
	return line, column
}
