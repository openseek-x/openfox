package market

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"openfox/internal/skills"
)

// 导入数据解析。数据目录保存市场导出接口 data 字段的原样 JSON，结构如下：
//
//	catalog.json  templates.json  skills-meta.json  selected.json
//	templates/<templateId>.json
//	skills/<skillId>/detail.json   skills/<skillId>/files/<path>.json
//
// 解析阶段只读文件、不写库；校验不通过的技能被跳过并记录原因。文件内容只在校验时读一遍后丢弃，
// 写入存储时再读，避免把所有技能同时放进内存。

// 跳过原因代码。
const (
	ReasonIDInvalid         = "ID_INVALID"
	ReasonDetailMissing     = "DETAIL_MISSING"
	ReasonDetailInvalid     = "DETAIL_INVALID"
	ReasonVersionInvalid    = "VERSION_INVALID"
	ReasonTreeInvalid       = "TREE_INVALID"
	ReasonPathInvalid       = "PATH_INVALID"
	ReasonTooManyFiles      = "TOO_MANY_FILES"
	ReasonTooLarge          = "TOO_LARGE"
	ReasonNoSkillMD         = "NO_SKILL_MD"
	ReasonFileMissing       = "FILE_MISSING"
	ReasonFileContentMiss   = "FILE_CONTENT_MISSING"
	ReasonFileDigestMismach = "FILE_DIGEST_MISMATCH"
	ReasonTemplateInvalid   = "TEMPLATE_INVALID"
	ReasonTemplateMissing   = "TEMPLATE_MISSING"

	// 单个 JSON 文件的读取上限：5 MB 内容经转义后不会超过它。
	maxJSONFileBytes = 24 << 20
	maxInstructions  = 250000
)

// Skip 记录一个被跳过的技能或模板及原因。
type Skip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// importFile 是通过校验的一个技能文件；Source 是 files/<path>.json 的本地路径。
type importFile struct {
	Path   string
	Mime   string
	Size   int64
	SHA256 string
	Source string
}

type importSkill struct {
	ID          string
	Version     string
	Digest      string
	Name        Text
	Description Text
	Icon        *string
	CategoryIDs []string
	Tags        []Text
	Stats       map[string]int64
	Publisher   Publisher
	Origin      string
	Featured    bool
	Downloads   int64
	Files       []importFile
	TotalBytes  int64
	SearchText  string
}

type importTemplate struct {
	ID           string
	Version      string
	Name         Text
	JobTitle     Text
	Tagline      Text
	Description  Text
	CategoryIDs  []string
	Keywords     []string
	Avatar       map[string]any
	QuickPrompts []Text
	Instructions map[string]string
	SkillRefs    []templateRef
	Origin       string
	Position     int
	PublishedAt  *time.Time
	SearchText   string
}

// ImportData 是解析后的待导入数据。
type ImportData struct {
	CatalogVersion string
	Digest         string
	Categories     []map[string]any
	Packs          json.RawMessage
	Skills         []importSkill
	Templates      []importTemplate
	// SelectedIDs 是 selected.json 里的全部技能 ID（含被跳过的），--prune 只删除不在其中的技能。
	SelectedIDs     []string
	TemplateIDs     []string
	SkippedSkills   []Skip
	SkippedTemplate []Skip
}

// 以下是兼容市场数据里用到的字段。
type rawSkill struct {
	SkillID     string           `json:"skillId"`
	Version     string           `json:"version"`
	Digest      string           `json:"digest"`
	Name        Text             `json:"name"`
	Description Text             `json:"description"`
	Featured    bool             `json:"featured"`
	Icon        *string          `json:"icon"`
	CategoryIDs []string         `json:"categoryIds"`
	Tags        []Text           `json:"tags"`
	Stats       map[string]int64 `json:"stats"`
	Publisher   struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	} `json:"publisher"`
	Origin string `json:"origin"`
	Source struct {
		Origin string `json:"origin"`
	} `json:"source"`
}

type rawNode struct {
	Type     string    `json:"type"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Mime     string    `json:"mime"`
	SHA256   string    `json:"sha256"`
	Children []rawNode `json:"children"`
}

type rawDetail struct {
	Meta rawSkill `json:"meta"`
	Tree struct {
		Nodes []rawNode `json:"nodes"`
	} `json:"tree"`
}

type rawFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

type rawTemplate struct {
	TemplateID   string         `json:"templateId"`
	Version      string         `json:"version"`
	Name         Text           `json:"name"`
	JobTitle     Text           `json:"jobTitle"`
	Tagline      Text           `json:"tagline"`
	Description  Text           `json:"description"`
	CategoryIDs  []string       `json:"categoryIds"`
	Keywords     []string       `json:"keywords"`
	Avatar       map[string]any `json:"avatar"`
	QuickPrompts []Text         `json:"quickPrompts"`
	SkillRefs    []templateRef  `json:"skillRefs"`
	Order        int            `json:"order"`
	PublishedAt  string         `json:"publishedAt"`
	Origin       string         `json:"origin"`
}

type rawTemplateFile struct {
	Meta         rawTemplate       `json:"meta"`
	Instructions map[string]string `json:"instructions"`
}

func readJSON(path string, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxJSONFileBytes {
		return fmt.Errorf("%s is too large", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// LoadDir 读取并校验数据目录。catalog.json、templates.json、selected.json、skills-meta.json 必须存在且可解析。
func LoadDir(dir string) (*ImportData, error) {
	var catalog struct {
		CatalogVersion string           `json:"catalogVersion"`
		Digest         string           `json:"digest"`
		Categories     []map[string]any `json:"categories"`
		Packs          json.RawMessage  `json:"packs"`
	}
	if err := readJSON(filepath.Join(dir, "catalog.json"), &catalog); err != nil {
		return nil, fmt.Errorf("read catalog.json: %w", err)
	}
	if n := utf8.RuneCountInString(catalog.CatalogVersion); n < 1 || n > 64 || len(catalog.Digest) > 128 {
		return nil, errors.New("catalog.json: catalogVersion or digest is invalid")
	}
	if len(catalog.Packs) == 0 || bytes.Equal(catalog.Packs, []byte("null")) {
		catalog.Packs = json.RawMessage("[]")
	}
	var templates []rawTemplate
	if err := readJSON(filepath.Join(dir, "templates.json"), &templates); err != nil {
		return nil, fmt.Errorf("read templates.json: %w", err)
	}
	var selected []string
	if err := readJSON(filepath.Join(dir, "selected.json"), &selected); err != nil {
		return nil, fmt.Errorf("read selected.json: %w", err)
	}
	var metas map[string]rawSkill
	if err := readJSON(filepath.Join(dir, "skills-meta.json"), &metas); err != nil {
		return nil, fmt.Errorf("read skills-meta.json: %w", err)
	}
	data := &ImportData{CatalogVersion: catalog.CatalogVersion, Digest: catalog.Digest, Categories: catalog.Categories, Packs: catalog.Packs}

	seen := map[string]bool{}
	for _, id := range selected {
		if seen[id] {
			continue
		}
		seen[id] = true
		data.SelectedIDs = append(data.SelectedIDs, id)
		skill, skip := loadSkill(dir, id, metas[id])
		if skip != nil {
			data.SkippedSkills = append(data.SkippedSkills, *skip)
			continue
		}
		data.Skills = append(data.Skills, skill)
	}
	seen = map[string]bool{}
	for _, item := range templates {
		if seen[item.TemplateID] {
			continue
		}
		seen[item.TemplateID] = true
		data.TemplateIDs = append(data.TemplateIDs, item.TemplateID)
		template, skip := loadTemplate(dir, item)
		if skip != nil {
			data.SkippedTemplate = append(data.SkippedTemplate, *skip)
			continue
		}
		data.Templates = append(data.Templates, template)
	}
	return data, nil
}

// truncateText 把每种语言的文本截到 limit 个字符。
func truncateText(text Text, limit int) Text {
	result := Text{}
	for key, value := range text {
		result[key] = truncateRunes(value, limit)
	}
	return result
}

func loadSkill(dir, id string, listed rawSkill) (importSkill, *Skip) {
	skip := func(reason, detail string) (importSkill, *Skip) {
		return importSkill{}, &Skip{ID: id, Reason: reason, Detail: detail}
	}
	if !IDPattern.MatchString(id) {
		return skip(ReasonIDInvalid, "")
	}
	skillDir := filepath.Join(dir, "skills", id)
	var detail rawDetail
	if _, err := os.Stat(filepath.Join(skillDir, "detail.json")); err != nil {
		return skip(ReasonDetailMissing, "")
	}
	if err := readJSON(filepath.Join(skillDir, "detail.json"), &detail); err != nil {
		return skip(ReasonDetailInvalid, "")
	}
	// 列表项带有最新的统计，详情带有与文件对应的版本；列表项缺失时用详情的元数据。
	meta := detail.Meta
	if listed.SkillID == id {
		meta.Name, meta.Description, meta.Featured, meta.Icon = listed.Name, listed.Description, listed.Featured, listed.Icon
		meta.CategoryIDs, meta.Tags, meta.Stats, meta.Publisher, meta.Origin = listed.CategoryIDs, listed.Tags, listed.Stats, listed.Publisher, listed.Origin
	}
	if n := utf8.RuneCountInString(detail.Meta.Version); n < 1 || n > 64 || len(detail.Meta.Digest) > 128 {
		return skip(ReasonVersionInvalid, "")
	}
	if meta.Origin == "" {
		meta.Origin = detail.Meta.Source.Origin
	}

	// 文件清单：递归展开目录节点。
	var nodes []rawNode
	var walk func(items []rawNode, depth int) bool
	walk = func(items []rawNode, depth int) bool {
		if depth > 16 {
			return false
		}
		for _, node := range items {
			switch node.Type {
			case "dir":
				if !walk(node.Children, depth+1) {
					return false
				}
			case "file":
				nodes = append(nodes, node)
			default:
				return false
			}
		}
		return true
	}
	if !walk(detail.Tree.Nodes, 0) {
		return skip(ReasonTreeInvalid, "")
	}
	if len(nodes) > skills.MaxFiles {
		return skip(ReasonTooManyFiles, fmt.Sprintf("%d files", len(nodes)))
	}
	var total int64
	files := make([]importFile, 0, len(nodes))
	paths := map[string]bool{}
	hasEntry := false
	for _, node := range nodes {
		if err := skills.ValidatePath(node.Path); err != nil || paths[node.Path] || node.Size < 0 {
			return skip(ReasonPathInvalid, node.Path)
		}
		paths[node.Path] = true
		total += node.Size
		if total > skills.MaxTotalBytes {
			return skip(ReasonTooLarge, "")
		}
		if node.Path == skills.SkillFile {
			hasEntry = true
		}
		files = append(files, importFile{
			Path: node.Path, Mime: truncateRunes(node.Mime, 128), Size: node.Size, SHA256: node.SHA256,
			Source: filepath.Join(skillDir, "files", filepath.FromSlash(node.Path)+".json"),
		})
	}
	if !hasEntry {
		return skip(ReasonNoSkillMD, "")
	}
	// 文件与目录不能同名。
	for path := range paths {
		for i := 0; i < len(path); i++ {
			if path[i] == '/' && paths[path[:i]] {
				return skip(ReasonPathInvalid, path)
			}
		}
	}
	for _, file := range files {
		if _, reason, err := readImportFile(file); err != nil {
			return skip(reason, file.Path)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	icon := meta.Icon
	if icon != nil && (!strings.HasPrefix(*icon, "https://") || len(*icon) > 1024) {
		icon = nil
	}
	categories := meta.CategoryIDs
	if len(categories) > 20 {
		categories = categories[:20]
	}
	tags := make([]Text, 0, len(meta.Tags))
	for _, tag := range meta.Tags {
		tags = append(tags, truncateText(tag, 64))
	}
	for len(tags) > 0 {
		raw, _ := json.Marshal(tags)
		if len(raw) <= 8000 {
			break
		}
		tags = tags[:len(tags)-1]
	}
	stats := map[string]int64{}
	for _, key := range []string{"downloads", "installs", "stars"} {
		stats[key] = max(meta.Stats[key], 0)
	}
	name, description := truncateText(meta.Name, 200), truncateText(meta.Description, 4000)
	origin := meta.Origin
	if len(origin) > 32 {
		origin = ""
	}
	result := importSkill{
		ID: id, Version: detail.Meta.Version, Digest: detail.Meta.Digest, Name: name, Description: description,
		Icon: icon, CategoryIDs: nonNilStrings(categories), Tags: tags, Stats: stats,
		Publisher: Publisher{Name: truncateRunes(meta.Publisher.Name, 200), Verified: meta.Publisher.Verified},
		Origin:    origin, Featured: meta.Featured, Downloads: stats["downloads"], Files: files, TotalBytes: total,
	}
	result.SearchText = SearchText(name, description, tags)
	return result, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// readImportFile 读出一个文件的内容并核对大小与 sha256。content 为空且 size > 0 表示源数据没有返回内容。
// （大文件不可预览）；size = 0 的空文件是合法的。content 按 UTF-8 文本处理，不尝试 base64 解码。
func readImportFile(file importFile) ([]byte, string, error) {
	var raw rawFile
	if err := readJSON(file.Source, &raw); err != nil {
		return nil, ReasonFileMissing, err
	}
	if raw.Content == "" && file.Size > 0 {
		return nil, ReasonFileContentMiss, errors.New("content is empty")
	}
	content := []byte(raw.Content)
	sum := sha256.Sum256(content)
	if int64(len(content)) != file.Size || hex.EncodeToString(sum[:]) != file.SHA256 {
		return nil, ReasonFileDigestMismach, errors.New("size or sha256 does not match")
	}
	return content, "", nil
}

func loadTemplate(dir string, item rawTemplate) (importTemplate, *Skip) {
	skip := func(reason string) (importTemplate, *Skip) {
		return importTemplate{}, &Skip{ID: item.TemplateID, Reason: reason}
	}
	if !IDPattern.MatchString(item.TemplateID) {
		return skip(ReasonIDInvalid)
	}
	var file rawTemplateFile
	path := filepath.Join(dir, "templates", item.TemplateID+".json")
	if _, err := os.Stat(path); err != nil {
		return skip(ReasonTemplateMissing)
	}
	if err := readJSON(path, &file); err != nil {
		return skip(ReasonTemplateInvalid)
	}
	meta := file.Meta
	version := meta.Version
	if version == "" {
		version = item.Version
	}
	if n := utf8.RuneCountInString(version); n < 1 || n > 64 {
		return skip(ReasonVersionInvalid)
	}
	size := 0
	for key, value := range file.Instructions {
		size += len(key) + len(value)
	}
	if size > maxInstructions {
		return skip(ReasonTooLarge)
	}
	refs := meta.SkillRefs
	valid := refs[:0:0]
	for _, ref := range refs {
		if IDPattern.MatchString(ref.SkillID) && len(valid) < 50 {
			valid = append(valid, ref)
		}
	}
	keywords := meta.Keywords
	if len(keywords) > 50 {
		keywords = keywords[:50]
	}
	categories := item.CategoryIDs
	if len(categories) == 0 {
		categories = meta.CategoryIDs
	}
	if len(categories) > 20 {
		categories = categories[:20]
	}
	firstText := func(a, b Text) Text {
		if len(a) > 0 {
			return a
		}
		return b
	}
	avatar := item.Avatar
	if len(avatar) == 0 {
		avatar = meta.Avatar
	}
	if avatar == nil {
		avatar = map[string]any{}
	}
	quick := meta.QuickPrompts
	for len(quick) > 0 {
		raw, _ := json.Marshal(quick)
		if len(raw) <= 30000 {
			break
		}
		quick = quick[:len(quick)-1]
	}
	template := importTemplate{
		ID: item.TemplateID, Version: version,
		Name: truncateText(firstText(item.Name, meta.Name), 200), JobTitle: truncateText(firstText(item.JobTitle, meta.JobTitle), 200),
		Tagline: truncateText(firstText(item.Tagline, meta.Tagline), 500), Description: truncateText(firstText(item.Description, meta.Description), 4000),
		CategoryIDs: nonNilStrings(categories), Keywords: nonNilStrings(keywords), Avatar: avatar, QuickPrompts: quick,
		Instructions: file.Instructions, SkillRefs: valid, Origin: item.Origin, Position: max(item.Order, 0),
	}
	if len(template.Origin) > 32 {
		template.Origin = ""
	}
	if template.Instructions == nil {
		template.Instructions = map[string]string{}
	}
	if template.SkillRefs == nil {
		template.SkillRefs = []templateRef{}
	}
	if parsed, err := time.Parse(time.RFC3339, item.PublishedAt); err == nil {
		template.PublishedAt = &parsed
	}
	template.SearchText = SearchText(template.Name, template.Description, nil,
		append([]string{pick(template.JobTitle), pick(template.Tagline)}, template.Keywords...)...)
	return template, nil
}

// readAll 读出已通过校验的文件内容（写入存储前再核对一遍）。
func readAll(file importFile) ([]byte, error) {
	content, _, err := readImportFile(file)
	return content, err
}
