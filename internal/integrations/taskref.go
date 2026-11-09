package integrations

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 从 PR 标题 / 描述 / 分支名 / 提交信息里识别任务编号（<空间前缀>-<序号>，不区分大小写）。

const (
	// MaxTaskRefsPerObject 是一个 PR 或提交最多关联的任务数。
	MaxTaskRefsPerObject = 10
	// maxBodyScanBytes 是描述里参与匹配的前缀长度。
	maxBodyScanBytes = 64 << 10
	// maxRawCandidates 限制一次匹配收集的候选数，防止超长文本放大查询。
	maxRawCandidates = 50
)

var taskRefPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])([A-Za-z][A-Za-z0-9_]{1,31}|[A-Za-z0-9]{2,7})-(\d{1,9})\b`)

// TaskRef 是一个候选任务编号。Prefix 已转小写；MatchedIn 是最先命中的位置（title / body / branch / message）。
type TaskRef struct {
	Prefix    string
	Sequence  int64
	MatchedIn string
}

// RefSource 是一段待匹配的文本及其来源。
type RefSource struct {
	Name string
	Text string
}

// BodySource 截取描述的前 64 KiB（保持 UTF-8 合法）。
func BodySource(text string) RefSource {
	if len(text) > maxBodyScanBytes {
		text = strings.ToValidUTF8(text[:maxBodyScanBytes], "")
	}
	return RefSource{Name: "body", Text: text}
}

// ExtractTaskRefs 按来源顺序收集去重后的候选编号。
func ExtractTaskRefs(sources ...RefSource) []TaskRef {
	refs := make([]TaskRef, 0)
	seen := map[string]bool{}
	for _, source := range sources {
		for _, match := range taskRefPattern.FindAllStringSubmatch(source.Text, -1) {
			sequence, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil || sequence <= 0 {
				continue
			}
			prefix := strings.ToLower(match[1])
			key := fmt.Sprintf("%s-%d", prefix, sequence)
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, TaskRef{Prefix: prefix, Sequence: sequence, MatchedIn: source.Name})
			if len(refs) >= maxRawCandidates {
				return refs
			}
		}
	}
	return refs
}

// BacklinkItem 是回链评论里的一个任务。
type BacklinkItem struct {
	LinkID    string
	DisplayID string
	SpaceID   string
}

// BacklinkBody 生成回链评论正文：每个任务一行，只写编号和链接，不写任务标题，避免私有信息泄露。
func BacklinkBody(publicURL, organizationID string, items []BacklinkItem) string {
	lines := make([]string, 0, len(items))
	base := strings.TrimRight(publicURL, "/")
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("%s 已关联到 OpenFox：%s/o/%s/spaces/%s/tasks?task=%s",
			item.DisplayID, base, organizationID, item.SpaceID, item.DisplayID))
	}
	return strings.Join(lines, "\n")
}

// FirstLine 返回文本的第一行并截断到 limit 个字符。
func FirstLine(text string, limit int) string {
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		text = text[:index]
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}
