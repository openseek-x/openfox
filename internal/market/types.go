// Package market 实现平台自建、全平台共用的只读技能与 Agent 模板目录。
// 目录由导入工具（openfox-server market import）写入；应用只读（openfox_app 只有 SELECT）。
// 从市场添加技能是把文件复制成空间技能，复用 5c 的 skills 包。
package market

import (
	"net/http"
	"regexp"
	"time"

	"openfox/internal/collaboration"
)

const (
	// MaxListLimit 与契约 limit 上限一致。
	MaxListLimit     = 100
	DefaultListLimit = 30
	MaxIDs           = 100
	MaxQueryRunes    = 100
	// PreviewBytes 是文本预览返回内容的上限；超过时 truncated = true。
	PreviewBytes = 1 << 20
	// sniffBytes 是判断文本 / 二进制时检查的前缀长度。
	sniffBytes = 8 << 10
	// MaxSearchRunes 与迁移 search_text 的长度上限一致。
	MaxSearchRunes = 16384
)

// IDPattern 与迁移里 skill_id / template_id 的格式一致。
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

var (
	ErrNotFound      = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "market item is not available")
	ErrFileNotFound  = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "file is not available")
	ErrInvalidQuery  = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "query parameters are invalid")
	ErrInvalidCursor = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "cursor is invalid")
)

// Text 是多语言文本：{"zh-Hans": …, "en": …}。
type Text map[string]string

// Category 是 MarketCategory。
type Category struct {
	ID            string `json:"id"`
	Name          Text   `json:"name"`
	Order         int    `json:"order"`
	SkillCount    int    `json:"skill_count"`
	TemplateCount int    `json:"template_count"`
}

// PackMember 是精选包里的一个技能。
type PackMember struct {
	MarketSkillID string  `json:"market_skill_id"`
	Name          Text    `json:"name"`
	Icon          *string `json:"icon"`
	Available     bool    `json:"available"`
}

// Pack 是 MarketPack。
type Pack struct {
	ID      string       `json:"id"`
	Name    Text         `json:"name"`
	Tint    string       `json:"tint"`
	Members []PackMember `json:"members"`
}

// Counts 是目录里的技能与模板数量。
type Counts struct {
	Skills    int `json:"skills"`
	Templates int `json:"templates"`
}

// Catalog 是 MarketCatalog。
type Catalog struct {
	Source         string     `json:"source"`
	CatalogVersion string     `json:"catalog_version"`
	ImportedAt     *time.Time `json:"imported_at"`
	Categories     []Category `json:"categories"`
	Packs          []Pack     `json:"packs"`
	Counts         Counts     `json:"counts"`
}

// Publisher 是技能发布者。
type Publisher struct {
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
}

// Skill 是 MarketSkill。
type Skill struct {
	MarketSkillID string    `json:"market_skill_id"`
	Version       string    `json:"version"`
	Name          Text      `json:"name"`
	Description   Text      `json:"description"`
	Icon          *string   `json:"icon"`
	CategoryIDs   []string  `json:"category_ids"`
	Tags          []Text    `json:"tags"`
	Publisher     Publisher `json:"publisher"`
	Origin        string    `json:"origin"`
	Featured      bool      `json:"featured"`
	Downloads     int64     `json:"downloads"`
	Stars         int64     `json:"stars"`
	FileCount     int       `json:"file_count"`
	TotalBytes    int64     `json:"total_bytes"`
}

// FileEntry 是 MarketSkillFileEntry。
type FileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Mime      string `json:"mime"`
}

// SkillDetail 是 MarketSkillDetail。
type SkillDetail struct {
	Skill
	Files []FileEntry `json:"files"`
}

// FileContent 是 MarketSkillFileContent。
type FileContent struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Mime      string `json:"mime"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

// Template 是 MarketTemplate。
type Template struct {
	MarketTemplateID string         `json:"market_template_id"`
	Version          string         `json:"version"`
	Name             Text           `json:"name"`
	JobTitle         Text           `json:"job_title"`
	Tagline          Text           `json:"tagline"`
	Description      Text           `json:"description"`
	CategoryIDs      []string       `json:"category_ids"`
	Keywords         []string       `json:"keywords"`
	Avatar           map[string]any `json:"avatar"`
	SkillCount       int            `json:"skill_count"`
	Origin           string         `json:"origin"`
}

// SkillRef 是 MarketTemplateSkillRef。
type SkillRef struct {
	MarketSkillID string `json:"market_skill_id"`
	Version       string `json:"version"`
	Available     bool   `json:"available"`
	Skill         *Skill `json:"skill"`
}

// TemplateDetail 是 MarketTemplateDetail。
type TemplateDetail struct {
	Template
	Instructions Text       `json:"instructions"`
	QuickPrompts []Text     `json:"quick_prompts"`
	Skills       []SkillRef `json:"skills"`
}

// ListQuery 是技能列表的筛选。IDs 非空时忽略其他筛选与分页。
type ListQuery struct {
	Category string
	Q        string
	Sort     string // default / downloads / newest / name
	IDs      []string
	Limit    int
	Cursor   string
}

// SkillPage 是技能列表的一页。
type SkillPage struct {
	Items      []Skill `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Total      int     `json:"total"`
}

// TemplatePage 是模板列表的一页。
type TemplatePage struct {
	Items      []Template `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	Total      int        `json:"total"`
}
