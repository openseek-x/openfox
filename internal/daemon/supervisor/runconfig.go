package supervisor

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"openfox/internal/runtime"
)

// EnvironmentVariable 是 RunContext 中环境变量的镜像（中心 → 设备）。
type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type GitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

const (
	IsolationWorktree = "WORKTREE"
	IsolationDirect   = "DIRECT"

	// 自定义参数中值的长度上限。
	maxCustomArgValue = 512
	// 最短脱敏长度：更短的值替换会破坏正常文本，与 Runtime 适配器一致。
	minRedactLength = 4
	// 单个 Run 的环境变量数量上限（中心侧也有限制，这里兜底）。
	maxRunEnvironment = 128
)

// customArgKeys 是允许 Agent 配置覆盖的 Codex 配置键。Agent 可能绑定在别人的共享电脑上，
// 不能通过自定义参数改变沙箱、审批、shell、MCP 等策略，因此只开放与模型输出相关的键。
var customArgKeys = map[string]struct{}{
	"model_reasoning_effort":   {},
	"model_reasoning_summary":  {},
	"model_verbosity":          {},
	"model_context_window":     {},
	"model_max_output_tokens":  {},
	"hide_agent_reasoning":     {},
	"show_raw_agent_reasoning": {},
	"tools.web_search":         {},
}

// ReservedEnvironmentNames / ReservedEnvironmentPrefixes 是 Daemon 保留、Agent 环境变量不能覆盖的名字
// （大小写按原样匹配）。命中的变量被丢弃，并上报一条只含变量名的 warning 事件：
//   - CODEX_HOME / HOME / OPENFOX_*：Daemon 自己管理；
//   - CODEX_*：Codex 自身的行为开关，不允许 Agent 配置改动；
//   - DYLD_* / LD_*：动态库注入，可让未沙箱的 Runtime 进程加载工作区内的库；
//   - PATH：替换 Runtime 与其子进程查找的可执行文件（已知限制：不能用环境变量改 PATH）。
var (
	ReservedEnvironmentNames    = []string{"CODEX_HOME", "HOME", "PATH"}
	ReservedEnvironmentPrefixes = []string{"OPENFOX_", "CODEX_", "DYLD_", "LD_"}
)

// ReservedEnvironment 报告 Agent 环境变量名是否被 Daemon 保留。
func ReservedEnvironment(name string) bool {
	for _, reserved := range ReservedEnvironmentNames {
		if name == reserved {
			return true
		}
	}
	for _, prefix := range ReservedEnvironmentPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// runSettings 是从 RunContext 解析出的、交给 Runtime 的设置与需要上报的告警。
type runSettings struct {
	environment map[string]string
	allowlist   []string
	args        []string
	// secrets 是本 Run 需要脱敏的值（全部 Agent 环境变量值，长度 ≥ 4）。
	secrets []string
	// warnings 是告警事件文本：只含变量名 / 配置键，不含值。
	warnings []runWarning
}

type runWarning struct {
	kind string
	name string
	text string
}

// buildRunSettings 按保留名单与白名单筛选环境变量、自定义参数，并加入 Git 提交人。
func buildRunSettings(runContext RunContext) runSettings {
	settings := runSettings{environment: map[string]string{}}
	for index, variable := range runContext.Environment {
		name := variable.Name
		if index >= maxRunEnvironment {
			settings.warnings = append(settings.warnings, runWarning{kind: "environment_dropped", name: boundUTF8(name, 128),
				text: "too many environment variables; the rest were not applied"})
			break
		}
		// 无论是否采用，值都加入脱敏列表（被丢弃的值也可能出现在输出中）。
		if len(variable.Value) >= minRedactLength {
			settings.secrets = append(settings.secrets, variable.Value)
		}
		switch {
		case !validEnvironmentName(name):
			settings.warnings = append(settings.warnings, runWarning{kind: "environment_dropped", name: "",
				text: "an environment variable with an invalid name was not applied"})
		case ReservedEnvironment(name):
			settings.warnings = append(settings.warnings, runWarning{kind: "environment_reserved", name: name,
				text: "environment variable " + name + " is reserved on this computer and was not applied"})
		case strings.IndexFunc(variable.Value, unsafeEnvRune) >= 0:
			settings.warnings = append(settings.warnings, runWarning{kind: "environment_dropped", name: name,
				text: "environment variable " + name + " contains control characters and was not applied"})
		default:
			settings.environment[name] = variable.Value
		}
	}
	if author := runContext.GitAuthor; author != nil {
		// 中心约定：某一项为空串表示不覆盖电脑上的 Git 配置；只设置非空的那一项。
		// 作者与提交人只经 Runtime 进程环境传给其 git 子进程，不改 Daemon 自身环境。
		name, email := strings.TrimSpace(author.Name), strings.TrimSpace(author.Email)
		valid := (name == "" || validAuthorField(name)) && (email == "" || (validAuthorField(email) && strings.Contains(email, "@")))
		switch {
		case !valid:
			settings.warnings = append(settings.warnings, runWarning{kind: "git_author_dropped",
				text: "the configured Git author is invalid and was not applied"})
		default:
			if name != "" {
				settings.environment["GIT_AUTHOR_NAME"], settings.environment["GIT_COMMITTER_NAME"] = name, name
			}
			if email != "" {
				settings.environment["GIT_AUTHOR_EMAIL"], settings.environment["GIT_COMMITTER_EMAIL"] = email, email
			}
		}
	}
	for name := range settings.environment {
		settings.allowlist = append(settings.allowlist, name)
	}
	sort.Strings(settings.allowlist)
	args, dropped := sanitizeCustomArgs(runContext.CustomArgs)
	settings.args = args
	for _, key := range dropped {
		text := "custom argument " + key + " is not allowed and was not applied"
		if key == "" {
			text = "a custom argument that is not a -c key=value override was not applied"
		}
		settings.warnings = append(settings.warnings, runWarning{kind: "custom_arg_dropped", name: key, text: text})
	}
	return settings
}

// sanitizeCustomArgs 只接受 `-c key=value` / `--config key=value`（也接受写在同一个字符串里的
// "-c key=value"），key 必须在白名单内。返回规范化的 ["-c", "key=value", ...] 与被丢弃的 key
// （无法解析出 key 的参数记为空串，不回显参数内容）。
func sanitizeCustomArgs(args []string) ([]string, []string) {
	var accepted, dropped []string
	for index := 0; index < len(args); index++ {
		token := strings.TrimSpace(args[index])
		var pair string
		switch {
		case token == "-c" || token == "--config":
			if index+1 >= len(args) {
				dropped = append(dropped, "")
				continue
			}
			index++
			pair = strings.TrimSpace(args[index])
		case strings.HasPrefix(token, "-c ") || strings.HasPrefix(token, "--config "):
			_, rest, _ := strings.Cut(token, " ")
			pair = strings.TrimSpace(rest)
		default:
			dropped = append(dropped, "")
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if _, allowed := customArgKeys[key]; !ok || !allowed {
			if _, allowed := customArgKeys[key]; !allowed && validConfigKey(key) {
				dropped = append(dropped, key)
			} else {
				dropped = append(dropped, "")
			}
			continue
		}
		if value == "" || len(value) > maxCustomArgValue || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			dropped = append(dropped, key)
			continue
		}
		accepted = append(accepted, "-c", key+"="+value)
	}
	return accepted, dropped
}

// validConfigKey 判断是否像一个配置键（只用于决定能否在告警里回显 key）。
func validConfigKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for index, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// unsafeEnvRune 与 runtime.RunRequest.Normalize 一致：值允许换行与制表符，拒绝其他控制字符。
func unsafeEnvRune(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
}

func validAuthorField(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0 && !strings.ContainsAny(value, "<>")
}

// redactor 把本 Run 的机密值替换为 [REDACTED]：作用于上传前的事件文本、事件 data 与结果文本，
// 是 Runtime 适配器自身脱敏之外的第二道防线。
type redactor struct {
	plain   []string
	escaped []string
}

func newRedactor(secrets []string) *redactor {
	values := make([]string, 0, len(secrets))
	seen := map[string]struct{}{}
	for _, secret := range secrets {
		if len(secret) < minRedactLength {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		values = append(values, secret)
	}
	// 长的先替换，避免一个值是另一个值的子串时只替换掉一部分。
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	r := &redactor{plain: values}
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		r.escaped = append(r.escaped, strings.Trim(string(encoded), `"`))
	}
	return r
}

func (r *redactor) text(value string) string {
	if r == nil {
		return value
	}
	for _, secret := range r.plain {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

// json 在 JSON 编码后的字节里替换机密值的转义形式；替换文本不含需要转义的字符，结果仍是合法 JSON。
func (r *redactor) json(payload []byte) []byte {
	if r == nil || len(r.plain) == 0 || len(payload) == 0 {
		return payload
	}
	text := string(payload)
	for index, secret := range r.escaped {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
		if plain := r.plain[index]; plain != secret {
			text = strings.ReplaceAll(text, plain, "[REDACTED]")
		}
	}
	if !json.Valid([]byte(text)) {
		// 理论上不会发生；无法保证合法时宁可丢弃 data。
		return nil
	}
	return []byte(text)
}

// warningEvent 构造 Daemon 告警事件：data 只含类型与名字。
func warningEvent(warning runWarning) runtime.Event {
	data := map[string]string{"kind": "daemon_" + warning.kind}
	if warning.name != "" {
		data["name"] = warning.name
	}
	payload, _ := json.Marshal(data)
	return runtime.Event{Type: runtime.EventWarning, Text: warning.text, Payload: payload}
}
