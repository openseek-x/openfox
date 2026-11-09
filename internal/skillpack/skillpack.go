// Package skillpack 是电脑端技能目录的扫描、打包与路径校验：
// skill_scan / skill_import 命令与执行时下发共用同一套名称、路径与大小规则。
package skillpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxFiles / MaxBytes 是单个技能的文件数与总字节上限（与中心一致）。
	MaxFiles = 100
	MaxBytes = 5 << 20
	// MaxPathBytes 是单个文件相对路径的长度上限（契约 maxLength 256）。
	MaxPathBytes = 256
	// MaxScanSkills / MaxScanResultBytes 是 skill_scan 结果上限。
	MaxScanSkills = 100
	// MaxScanResultBytes 是 skill_scan 结果 JSON 文本的上限。中心按 jsonb 存储大小（≤ 64 KiB）校验，
	// 含大量小对象时 jsonb 比文本约大 5%，这里取 56 KiB 留 12% 余量。
	MaxScanResultBytes = 56 * 1024
	// MaxDescriptionRunes 是扫描结果中描述的截断长度。
	MaxDescriptionRunes = 160
	// SkillFile 是技能根目录必须存在的入口文件。
	SkillFile = "SKILL.md"
	// 遍历时最多访问的目录项数，防止空目录海量嵌套拖慢扫描。
	maxWalkEntries = 2000
	// 解析 frontmatter 时最多读取的字节数。
	maxFrontmatterBytes = 64 * 1024
)

// 导入失败原因码（DeviceSkillBundle.error_code）。
const (
	CodeNotFound = "NOT_FOUND"
	CodeInvalid  = "SKILL_INVALID"
	CodeTooLarge = "SKILL_TOO_LARGE"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName 报告名称是否可作为技能目录名。
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidPath 校验技能内相对路径：用 / 分隔，拒绝空、绝对路径、空段、"."、".."、反斜杠、控制字符与过长路径。
func ValidPath(path string) bool {
	if path == "" || len(path) > MaxPathBytes || !utf8.ValidString(path) {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// File 是打包后的一个文件。
type File struct {
	Path    string
	Content []byte
}

// PackError 带原因码，用于 skill_import 上传 error_code。
type PackError struct{ Code string }

func (e *PackError) Error() string { return "skillpack: " + e.Code }

// ScanItem 是 skill_scan 结果中的一项（字段与 DeviceSkillScanResult.skills 一致）。
type ScanItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FileCount   int    `json:"file_count"`
	TotalBytes  int64  `json:"total_bytes"`
	Importable  bool   `json:"importable"`
}

// Scan 按优先顺序扫描多个 skills 目录的一级子目录，只列含常规文件 SKILL.md 的；跳过以 "." 开头的目录
// （例如 .system）；重名以靠前的目录为准，只出现一次。按名称排序，最多 MaxScanSkills 个，
// 序列化后整体 ≤ MaxScanResultBytes（超出则从末尾减少条目）。目录不存在时视为空。
func Scan(skillsDirs []string) ([]ScanItem, error) {
	items := []ScanItem{}
	seen := map[string]struct{}{}
	for _, skillsDir := range skillsDirs {
		entries, err := os.ReadDir(skillsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			// DirEntry 基于 Lstat：符号链接目录不是目录，不会被列出。
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dir := filepath.Join(skillsDir, entry.Name())
			if !hasSkillFile(dir) {
				continue
			}
			if _, dup := seen[entry.Name()]; dup {
				continue
			}
			seen[entry.Name()] = struct{}{}
			item := ScanItem{Name: entry.Name(), Description: readDescription(filepath.Join(dir, SkillFile))}
			item.FileCount, item.TotalBytes, item.Importable = measure(dir)
			item.Importable = item.Importable && ValidName(entry.Name())
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if len(items) > MaxScanSkills {
		items = items[:MaxScanSkills]
	}
	for len(items) > 0 {
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		// 外层对象 {"supported":true,"runtime":"codex","skills":...} 约 50 字节，留 256 字节余量。
		if len(encoded) <= MaxScanResultBytes-256 {
			break
		}
		items = items[:len(items)-1]
	}
	return items, nil
}

func hasSkillFile(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, SkillFile))
	return err == nil && info.Mode().IsRegular()
}

// measure 递归统计文件数与字节数（不跟随符号链接）。遇到符号链接、非常规文件、超过 MaxFiles 个文件、
// MaxBytes 字节或过多目录项时返回 importable = false 并尽早停止。
func measure(root string) (files int, bytesTotal int64, importable bool) {
	importable = true
	entries := 0
	stop := errors.New("stop")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			importable = false
			return stop
		}
		if path == root {
			return nil
		}
		if entries++; entries > maxWalkEntries {
			importable = false
			return stop
		}
		switch {
		case entry.IsDir():
			return nil
		case entry.Type().IsRegular():
			info, err := entry.Info()
			if err != nil {
				importable = false
				return stop
			}
			files++
			bytesTotal += info.Size()
			if files > MaxFiles || bytesTotal > MaxBytes {
				importable = false
				return stop
			}
		default:
			// 符号链接、设备、套接字等。
			importable = false
			return stop
		}
		return nil
	})
	return files, bytesTotal, importable
}

// Pack 在多个 skills 目录（按优先顺序）中找到 name 并读取其全部文件：取第一个含 SKILL.md 的目录，
// 都没有时取第一个存在的目录（得到 SKILL_INVALID），都不存在为 NOT_FOUND。
// 失败时返回 *PackError（NOT_FOUND / SKILL_INVALID / SKILL_TOO_LARGE）。
func Pack(skillsDirs []string, name string) ([]File, error) {
	if !ValidName(name) {
		return nil, &PackError{CodeInvalid}
	}
	root, first := "", ""
	for _, skillsDir := range skillsDirs {
		candidate := filepath.Join(skillsDir, name)
		// 名称不含路径分隔符且不以 "." 开头，Join 后必然仍在 skillsDir 内；这里再确认一次。
		if rel, err := filepath.Rel(skillsDir, candidate); err != nil || rel != name {
			return nil, &PackError{CodeInvalid}
		}
		if _, err := os.Lstat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, &PackError{CodeInvalid}
		}
		if first == "" {
			first = candidate
		}
		if hasSkillFile(candidate) {
			root = candidate
			break
		}
	}
	if root == "" {
		root = first
	}
	if root == "" {
		return nil, &PackError{CodeNotFound}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, &PackError{CodeInvalid}
	}
	var files []File
	var total int64
	entries := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return &PackError{CodeInvalid}
		}
		if path == root {
			return nil
		}
		if entries++; entries > maxWalkEntries {
			return &PackError{CodeTooLarge}
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return &PackError{CodeInvalid}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return &PackError{CodeInvalid}
		}
		slashed := filepath.ToSlash(rel)
		if !ValidPath(slashed) {
			return &PackError{CodeInvalid}
		}
		if len(files) >= MaxFiles {
			return &PackError{CodeTooLarge}
		}
		content, err := readRegular(path, MaxBytes-total)
		if err != nil {
			return err
		}
		total += int64(len(content))
		files = append(files, File{Path: slashed, Content: content})
		return nil
	})
	if err != nil {
		var packErr *PackError
		if errors.As(err, &packErr) {
			return nil, packErr
		}
		return nil, &PackError{CodeInvalid}
	}
	hasSkillFile := false
	for _, file := range files {
		if file.Path == SkillFile {
			hasSkillFile = true
		}
	}
	if !hasSkillFile {
		return nil, &PackError{CodeInvalid}
	}
	return files, nil
}

// readRegular 读取常规文件，最多 budget 字节（按实际读出计）；打开后核对与 Lstat 是同一文件，防止替换成符号链接。
func readRegular(path string, budget int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, &PackError{CodeInvalid}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, &PackError{CodeInvalid}
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, &PackError{CodeInvalid}
	}
	content, err := io.ReadAll(io.LimitReader(file, budget+1))
	if err != nil {
		return nil, &PackError{CodeInvalid}
	}
	if int64(len(content)) > budget {
		return nil, &PackError{CodeTooLarge}
	}
	return content, nil
}

// readDescription 读取 SKILL.md frontmatter 的 description，截到 MaxDescriptionRunes 个字符。
func readDescription(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	content, _ := io.ReadAll(io.LimitReader(file, maxFrontmatterBytes))
	return truncateRunes(ParseDescription(content), MaxDescriptionRunes)
}

// ParseDescription 从 SKILL.md 内容的 frontmatter（--- 包住的简单 YAML）取 description。
// 支持单行、引号包裹，以及 > / | 块标量（折成单行）。没有则返回空串。
func ParseDescription(content []byte) string {
	text := strings.ReplaceAll(strings.TrimPrefix(string(content), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return ""
	}
	for index := 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimRight(line, " \t") == "---" {
			return ""
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "description" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == ">" || value == "|" || value == ">-" || value == "|-" {
			parts := []string{}
			for next := index + 1; next < len(lines); next++ {
				if !strings.HasPrefix(lines[next], " ") && !strings.HasPrefix(lines[next], "\t") {
					break
				}
				if part := strings.TrimSpace(lines[next]); part != "" {
					parts = append(parts, part)
				}
			}
			return strings.Join(parts, " ")
		}
		return cleanLine(unquote(value))
	}
	return ""
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		inner := value[1 : len(value)-1]
		if value[0] == '\'' {
			return strings.ReplaceAll(inner, "''", "'")
		}
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
	}
	return value
}

func cleanLine(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}

func truncateRunes(value string, limit int) string {
	value = cleanLine(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string(bytes.Runes([]byte(value))[:limit])
}
