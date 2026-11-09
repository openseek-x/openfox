package agents

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"openfox/internal/runtimepolicy"
)

// 运行设置与 Git 提交人的取值范围，与迁移 000028 的 agent_config_versions 约束一致。
var gitEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

const (
	maxCustomArgs      = 64
	maxCustomArgBytes  = 1024
	maxCustomArgsBytes = 16000 // custom_args 的 jsonb 上限 16384 字节，留出编码余量
)

// normalizeRunSettings 补默认值并校验；缺省值与旧版本行为一致（DEFAULT / WORKTREE / FOLLOW_* / 用户邮箱）。
func normalizeRunSettings(in RunSettings) (RunSettings, error) {
	in.ChatConcurrency = upperDefault(in.ChatConcurrency, "DEFAULT")
	switch in.ChatConcurrency {
	case "DEFAULT", "SHARE_TASK":
		if in.ChatConcurrencyValue != nil {
			return RunSettings{}, ErrInvalidConfig
		}
	case "MANUAL":
		if in.ChatConcurrencyValue == nil || *in.ChatConcurrencyValue < 1 || *in.ChatConcurrencyValue > 64 {
			return RunSettings{}, ErrInvalidConfig
		}
	default:
		return RunSettings{}, ErrInvalidConfig
	}
	in.WorkdirIsolation = upperDefault(in.WorkdirIsolation, "WORKTREE")
	if in.WorkdirIsolation != "WORKTREE" && in.WorkdirIsolation != "DIRECT" {
		return RunSettings{}, ErrInvalidConfig
	}
	in.ApprovalMode = upperDefault(in.ApprovalMode, runtimepolicy.FollowRuntime)
	// 协作模式只接受 FOLLOW_RUNTIME / DEFAULT / PLAN，外加历史值 FOLLOW_AGENT（规范化为 FOLLOW_RUNTIME）。
	in.CollaborationMode = runtimepolicy.NormalizeCollaborationMode(upperDefault(in.CollaborationMode, runtimepolicy.CollaborationFollowRuntime))
	// 权限模式限定为四档（与迁移 000062 的 CHECK 一致）。
	if !runtimepolicy.ValidApprovalMode(in.ApprovalMode) || !runtimepolicy.ValidCollaborationMode(in.CollaborationMode) {
		return RunSettings{}, ErrInvalidConfig
	}
	if in.CustomArgs == nil {
		in.CustomArgs = []string{}
	}
	if len(in.CustomArgs) > maxCustomArgs {
		return RunSettings{}, ErrInvalidConfig
	}
	for _, arg := range in.CustomArgs {
		if len(arg) > maxCustomArgBytes || strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return RunSettings{}, ErrInvalidConfig
		}
	}
	if encoded, err := json.Marshal(in.CustomArgs); err != nil || len(encoded) > maxCustomArgsBytes {
		return RunSettings{}, ErrInvalidConfig
	}
	in.GitAuthorNameMode = upperDefault(in.GitAuthorNameMode, "USER_AND_AGENT")
	switch in.GitAuthorNameMode {
	case "INHERIT", "USER_AND_AGENT", "USER", "OWNER_AND_AGENT", "OWNER", "MACHINE", "MANUAL":
	default:
		return RunSettings{}, ErrInvalidConfig
	}
	in.GitAuthorName = strings.TrimSpace(in.GitAuthorName)
	if len([]rune(in.GitAuthorName)) > 128 || strings.IndexFunc(in.GitAuthorName, unicode.IsControl) >= 0 ||
		(in.GitAuthorNameMode == "MANUAL" && in.GitAuthorName == "") {
		return RunSettings{}, ErrInvalidConfig
	}
	in.GitAuthorEmailMode = upperDefault(in.GitAuthorEmailMode, "USER")
	switch in.GitAuthorEmailMode {
	case "INHERIT", "USER", "OWNER", "MACHINE", "MANUAL":
	default:
		return RunSettings{}, ErrInvalidConfig
	}
	in.GitAuthorEmail = strings.TrimSpace(in.GitAuthorEmail)
	if len([]rune(in.GitAuthorEmail)) > 254 || strings.IndexFunc(in.GitAuthorEmail, unicode.IsControl) >= 0 ||
		(in.GitAuthorEmailMode == "MANUAL" && !gitEmailPattern.MatchString(in.GitAuthorEmail)) {
		return RunSettings{}, ErrInvalidConfig
	}
	return in, nil
}

func upperDefault(value, fallback string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}
