package attachments

import (
	"net/http"
	"path"
	"strings"
)

// 扩展名白名单分组。每组规定「嗅探结果必须属于哪些类型」与落库的内容类型：
// 不能只信扩展名，也不信客户端声明的 Content-Type。
type class struct {
	// sniff 是允许的 http.DetectContentType 结果（去掉参数后）；空表示不限（旧版 Office 等二进制）。
	sniff []string
	// stored 非空时固定使用该类型落库；为空使用嗅探结果。
	stored string
}

const (
	typeText   = "text/plain; charset=utf-8"
	typeZip    = "application/zip"
	typeBinary = "application/octet-stream"
)

var (
	imageClass = func(sniff string) class { return class{sniff: []string{sniff}} }
	// 文本类（含 SVG / HTML / XML）一律以纯文本落库，永远不会以可执行类型 inline 返回；
	// 嗅探到 HTML / XML 片段的 .md、.txt 也允许（落库类型仍是纯文本）。
	textClass   = class{sniff: []string{"text/plain", "text/html", "text/xml"}, stored: typeText}
	markupClass = textClass
	zipClass    = class{sniff: []string{"application/zip"}, stored: typeZip}
	// 视频（测试录像）：文件头必须是对应容器，落库为嗅探结果，可在线播放（OPE-19）。
	videoClass  = func(sniff string) class { return class{sniff: []string{sniff}} }
	legacyClass = class{stored: typeBinary}
)

var extensionClasses = map[string]class{
	".png": imageClass("image/png"), ".jpg": imageClass("image/jpeg"), ".jpeg": imageClass("image/jpeg"),
	".gif": imageClass("image/gif"), ".webp": imageClass("image/webp"),
	".pdf": {sniff: []string{"application/pdf"}},
	".txt": textClass, ".md": textClass, ".csv": textClass, ".tsv": textClass, ".json": textClass,
	".log": textClass, ".yaml": textClass, ".yml": textClass, ".toml": textClass, ".ini": textClass,
	".svg": markupClass, ".html": markupClass, ".htm": markupClass, ".xml": markupClass,
	".webm": videoClass("video/webm"), ".mp4": videoClass("video/mp4"),
	".zip": zipClass, ".docx": zipClass, ".xlsx": zipClass, ".pptx": zipClass,
	".doc": legacyClass, ".xls": legacyClass, ".ppt": legacyClass,
}

// Classify 按文件名扩展名与文件头判断附件是否允许，返回落库的内容类型。
// 扩展名不在白名单、或文件头与扩展名声明的类别不一致（例如 .png 实为 HTML）都拒绝。
func Classify(name string, head []byte) (string, error) {
	ext := strings.ToLower(path.Ext(name))
	c, ok := extensionClasses[ext]
	if !ok {
		return "", ErrTypeRejected
	}
	sniffed := http.DetectContentType(head)
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = sniffed[:i]
	}
	if len(c.sniff) > 0 {
		match := false
		for _, want := range c.sniff {
			if sniffed == want {
				match = true
				break
			}
		}
		if !match {
			return "", ErrTypeRejected
		}
	}
	if c.stored != "" {
		return c.stored, nil
	}
	return sniffed, nil
}

// Previewable 判断内容类型是否可以 inline 预览。
func Previewable(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// 预览方式（契约 Attachment.preview_kind）。
const (
	PreviewImage = "image"
	PreviewVideo = "video"
	PreviewHTML  = "html"
	PreviewNone  = "none"
)

// MaxHTMLPreviewBytes 是 HTML 附件在页面内渲染的上限（与产物 HTML 报告一致，10 MB），超过只能下载。
const MaxHTMLPreviewBytes = 10 << 20

// PreviewKind 返回附件在页面内的预览方式：图片 inline；视频按 Range 分段播放；
// 以纯文本落库的 .html/.htm 由前端做隔离静态渲染（无脚本、仅内嵌位图），且不超过 MaxHTMLPreviewBytes。
func PreviewKind(name, contentType string, size int64) string {
	switch {
	case Previewable(contentType):
		return PreviewImage
	case contentType == "video/webm" || contentType == "video/mp4":
		return PreviewVideo
	case contentType == typeText && size <= MaxHTMLPreviewBytes:
		if ext := strings.ToLower(path.Ext(name)); ext == ".html" || ext == ".htm" {
			return PreviewHTML
		}
	}
	return PreviewNone
}

// StorageKey 返回对象键：每个附件独占一个对象，删除附件即删除对象。
func StorageKey(organizationID, attachmentID string) string {
	return "attachments/" + organizationID + "/" + attachmentID
}
