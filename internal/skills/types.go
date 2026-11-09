// Package skills 实现空间技能库、技能文件、Agent 装配与从 Runtime 导入。
// 授权由 API 层完成（沿用文档库的空间读写判断）；这里负责规则校验、并发控制与数据读写。
// 文件内容放存储目录，数据库只记路径、大小与 sha256（迁移 000042）。
package skills

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"openfox/internal/collaboration"
)

const (
	// SkillFile 是技能必须有的根目录入口文件。
	SkillFile = "SKILL.md"

	MaxPerSpace         = 500
	MaxFiles            = 100
	MaxTotalBytes       = 5 << 20
	MaxDescriptionRunes = 1024
	MaxPathRunes        = 256
	MaxAgentSkills      = 20
	MaxImportNames      = 20
	MaxSearchRunes      = 100
	// MaxInstructionBytes 与契约 SkillCreateInput.instructions 的 maxLength 一致。
	MaxInstructionBytes = 262144
)

// namePattern 与迁移 skills_name_valid 一致。
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidName 判断技能名称是否合规。
func ValidName(name string) bool { return namePattern.MatchString(name) }

var (
	ErrNotFound       = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "skill is not available")
	ErrNameTaken      = collaboration.NewRuleError(http.StatusConflict, "SKILL_NAME_TAKEN", "a skill with this name already exists in the space")
	ErrNameInvalid    = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_NAME_INVALID", "skill name must be 1-64 characters of lowercase letters, digits and hyphens")
	ErrInvalid        = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_INVALID", "skill content is invalid")
	ErrTooLarge       = collaboration.NewRuleError(http.StatusRequestEntityTooLarge, "SKILL_TOO_LARGE", "a skill can have at most 100 files and 5 MB in total")
	ErrLimit          = collaboration.NewRuleError(http.StatusConflict, "SKILL_LIMIT", "each space can have at most 500 skills")
	ErrForbidden      = collaboration.NewRuleError(http.StatusForbidden, "FORBIDDEN", "only the creator or an organization admin can change this skill")
	ErrQueryInvalid   = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "search query must be at most 100 characters")
	ErrSkillRefs      = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "skill_ids must be distinct skills of this space")
	ErrAgentLimit     = collaboration.NewRuleError(http.StatusConflict, "AGENT_SKILL_LIMIT", "the Agent already has 20 skills")
	ErrAgentNameTaken = collaboration.NewRuleError(http.StatusConflict, "AGENT_SKILL_NAME_TAKEN", "a skill with this name is already attached to the Agent")
	ErrTooManySkills  = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "an Agent can have at most 20 skills")
	ErrFileNotFound   = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "file is not available")
	ErrDeleteSkillMD  = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_INVALID", "SKILL.md cannot be deleted")

	// 市场技能。
	ErrMarketExists  = collaboration.NewRuleError(http.StatusConflict, "SKILL_MARKET_EXISTS", "the space already has this market skill")
	ErrUpToDate      = collaboration.NewRuleError(http.StatusConflict, "SKILL_UP_TO_DATE", "the skill is already at the latest market version")
	ErrNotFromMarket = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_NOT_FROM_MARKET", "the skill was not added from the market")

	ErrURLInvalid   = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_URL_INVALID", "the URL is invalid")
	ErrURLForbidden = collaboration.NewRuleError(http.StatusBadRequest, "SKILL_URL_FORBIDDEN", "the URL target is not allowed")

	ErrImportBusy     = collaboration.NewRuleError(http.StatusConflict, "COMMAND_BUSY", "this computer already has an unfinished import")
	ErrImportCanceled = collaboration.NewRuleError(http.StatusConflict, "IMPORT_CANCELED", "the import was canceled")
	ErrImportNotOpen  = collaboration.NewRuleError(http.StatusConflict, "COMMAND_CONFLICT", "the command is not an active skill import")
	ErrImportName     = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "the skill name is not part of this import")
)

// FetchFailed 返回下载失败的 422，message 为可展示的原因。
func FetchFailed(message string) error {
	return collaboration.NewRuleError(http.StatusUnprocessableEntity, "SKILL_URL_FETCH_FAILED", message)
}

// File 是内存里的一个技能文件。
type File struct {
	Path    string
	Content []byte
}

// Package 是通过校验的技能内容：名称与描述来自 SKILL.md 的 frontmatter。
type Package struct {
	Name        string
	Description string
	Files       []File
	TotalBytes  int64
}

// Entry 是已写入存储的文件记录（skill_files 的一行）。
type Entry struct {
	Path   string
	Key    string
	Size   int64
	SHA256 string
}

// User 是 SkillUser。
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentRef 是 SkillAgentRef。
type AgentRef struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Avatar json.RawMessage `json:"avatar"`
}

// Skill 是契约 Skill。CanEdit 由 API 层按调用者填写。
type Skill struct {
	AgentOwned  bool    `json:"-"`
	ID          string  `json:"id"`
	SpaceID     string  `json:"space_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	SourceKind  string  `json:"source_kind"`
	SourceLabel string  `json:"source_label"`
	SourceURL   *string `json:"source_url"`
	// 来自市场的技能记住来源技能与版本；MarketLatestVersion 是市场当前版本（市场里已没有时为 nil）。
	MarketSkillID       *string    `json:"market_skill_id"`
	MarketVersion       *string    `json:"market_version"`
	MarketLatestVersion *string    `json:"market_latest_version"`
	FileCount           int        `json:"file_count"`
	TotalBytes          int64      `json:"total_bytes"`
	CreatedBy           User       `json:"created_by"`
	UsedBy              []AgentRef `json:"used_by"`
	UsedCount           int        `json:"used_count"`
	CanEdit             bool       `json:"can_edit"`
	Version             int64      `json:"version"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// FileEntry 是 SkillFileEntry。
type FileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// Detail 是 SkillDetail。
type Detail struct {
	Skill
	Files  []FileEntry `json:"files"`
	Agents []AgentRef  `json:"agents"`
}

// ListQuery 是列表的筛选与排序。
type ListQuery struct {
	Q         string
	Sort      string // usage / updated / created
	CreatedBy string
}

// ImportItem 是 SkillImportItem。
type ImportItem struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Reason  string  `json:"reason"`
	SkillID *string `json:"skill_id"`
}

// RuntimeImport 是 SkillRuntimeImport。
type RuntimeImport struct {
	CommandID string       `json:"command_id"`
	DeviceID  string       `json:"device_id"`
	Status    string       `json:"status"`
	Items     []ImportItem `json:"items"`
}

// Source 描述技能的来源。MarketSkillID / MarketVersion 只用于 Kind = market。
type Source struct {
	Kind          string
	Label         string
	URL           *string
	MarketSkillID *string
	MarketVersion *string
}
