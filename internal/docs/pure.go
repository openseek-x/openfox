package docs

import (
	"mime"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 无副作用的输入规则：名称与标题规范化、文件名清理、搜索转义、上传类型判断、下载头。

// NormalizeSetName 去掉首尾空白后要求 1–64 个字符。
func NormalizeSetName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxSetNameRunes {
		return "", ErrSetNameInvalid
	}
	return name, nil
}

// NormalizeTitle 去掉首尾空白后要求最多 200 个字符（允许空串，文件夹由调用方另行检查）。
func NormalizeTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		return "", ErrTitleInvalid
	}
	return title, nil
}

// CleanFileName 清理上传的文件名：去掉路径成分（含 Windows 分隔符），拒绝空名、「.」「..」与控制字符。
func CleanFileName(raw string) (string, error) {
	name := raw
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", ErrFileName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrFileName
		}
	}
	if utf8.RuneCountInString(name) > MaxAttachmentNameRunes {
		return "", ErrFileName
	}
	return name, nil
}

// SplitExtension 把文件名拆成标题与扩展名（扩展名含点号）。以点开头的名字（如 .bashrc）没有扩展名。
func SplitExtension(name string) (title, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

// TextKind 按扩展名（不区分大小写）判断文本类上传：.md 为 doc，.csv 为 sheet，其余返回空串。
func TextKind(ext string) string {
	switch strings.ToLower(ext) {
	case ".md":
		return KindDoc
	case ".csv":
		return KindSheet
	}
	return ""
}

// IsTextBody 判断上传内容能否作为文档正文：不超过 2 MB 且是合法 UTF-8。
func IsTextBody(content []byte) bool {
	return len(content) <= MaxBodyBytes && utf8.Valid(content)
}

// EscapeLike 转义 LIKE 通配符（% _ \），使搜索词按字面匹配；查询使用 ESCAPE '\'。
func EscapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// DownloadName 生成下载文件名：doc 为 <标题>.md，sheet 为 <标题>.csv；file 优先用保存的原扩展名，
// 没有时按内容类型推断。标题为空时使用「未命名文档」。
func DownloadName(kind, title, ext, contentType string) string {
	if title == "" {
		title = untitledLabel
	}
	switch kind {
	case KindDoc:
		return title + ".md"
	case KindSheet:
		return title + ".csv"
	}
	if ext != "" {
		return title + "." + ext
	}
	return title + extensionForType(contentType)
}

// fileExtPattern 是保存到 file_ext 的扩展名格式（与迁移的 CHECK 一致）。
var fileExtPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// StoredExt 把原扩展名（可带点号、任意大小写）规范为 file_ext：不合格式时返回空串，即没有保存扩展名。
func StoredExt(ext string) string {
	value := strings.ToLower(strings.TrimPrefix(ext, "."))
	if !fileExtPattern.MatchString(value) {
		return ""
	}
	return value
}

// extensionForType 由内容类型反推扩展名。文件节点没有保存原扩展名，所以这是有损的推断（例如 .jpg 与 .jpeg 无法区分）。
func extensionForType(contentType string) string {
	base, _, _ := strings.Cut(contentType, ";")
	extensions, _ := mime.ExtensionsByType(strings.TrimSpace(base))
	if len(extensions) == 0 {
		return ""
	}
	return extensions[0]
}

// inlineTypes 是允许以 inline 返回的附件类型。SVG 与 HTML 会执行脚本，一律不 inline。
var inlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// IsInlineType 判断附件是否可以 inline 返回。
func IsInlineType(contentType string) bool { return inlineTypes[contentType] }

// ContentDisposition 生成 Content-Disposition：ASCII 回退名 filename 加上 RFC 5987 的 filename*=UTF-8”...。
func ContentDisposition(disposition, name string) string {
	fallback := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return disposition + `; filename="` + fallback + `"; filename*=UTF-8''` + encodeRFC5987(name)
}

// encodeRFC5987 按 RFC 5987 的 attr-char 规则做百分号编码（UTF-8 字节，十六进制大写）。
func encodeRFC5987(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
