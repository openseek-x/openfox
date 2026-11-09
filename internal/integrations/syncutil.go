package integrations

// Issue 同步的纯函数（内容摘要、标记、状态映射、退避），入站与出站共用，便于单测。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// 出站项与评论里的标记：入站据此识别 OpenFox 自己发出的内容，避免回环。
	markerPrefix  = "<!-- openfox:"
	backlinkMark  = "<!-- openfox:backlink -->"
	maxIssueTitle = 500
	maxIssueBody  = 10000
	// GitHub 评论 / Issue 正文上限是 65536 个字符，留出标记与前缀的余量。
	maxGitHubBody = 60000

	issueOpen   = "OPEN"
	issueClosed = "CLOSED"
)

var (
	taskMarker      = regexp.MustCompile(`<!-- openfox:task:([0-9a-fA-F-]{36}) -->`)
	taskMarkerStrip = regexp.MustCompile(`\s*<!-- openfox:task:[0-9a-fA-F-]{36} -->\s*`)
)

// TaskMarker 生成追加在 Issue 正文末尾的任务标记。
func TaskMarker(taskID string) string { return "<!-- openfox:task:" + taskID + " -->" }

// CommentMarker 生成追加在评论末尾的标记。
func CommentMarker(commentID string) string { return "<!-- openfox:comment:" + commentID + " -->" }

// ParseTaskMarker 从 Issue 正文里取出任务标记里的任务 ID（小写）。
func ParseTaskMarker(body string) (string, bool) {
	match := taskMarker.FindStringSubmatch(body)
	if match == nil {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// HasOpenFoxMarker 判断正文是否含有 OpenFox 的任何标记（评论入站时据此忽略自己发的评论）。
func HasOpenFoxMarker(body string) bool { return strings.Contains(body, markerPrefix) }

// StripTaskMarker 去掉 Issue 正文里的任务标记与首尾空白。
func StripTaskMarker(body string) string {
	return strings.TrimSpace(taskMarkerStrip.ReplaceAllString(body, "\n"))
}

// IssueBodyWithMarker 生成发往 GitHub 的 Issue 正文：描述 + 两个换行 + 任务标记。
func IssueBodyWithMarker(description, taskID string) string {
	description = strings.TrimSpace(description)
	if len([]rune(description)) > maxGitHubBody {
		description = string([]rune(description)[:maxGitHubBody])
	}
	if description == "" {
		return TaskMarker(taskID)
	}
	return description + "\n\n" + TaskMarker(taskID)
}

// IssueContentHash 是 Issue 内容摘要：sha256(标题 \0 正文 \0 状态 \0 排序后的处理人 login)，取 hex 前 32 位。
// 正文先去掉 OpenFox 标记并统一换行；login 统一小写，避免大小写差异造成假的变更。入站与出站使用同一个函数。
func IssueContentHash(title, body, state string, assignees []string) string {
	logins := make([]string, 0, len(assignees))
	for _, login := range assignees {
		logins = append(logins, strings.ToLower(strings.TrimSpace(login)))
	}
	sort.Strings(logins)
	body = strings.ReplaceAll(StripTaskMarker(body), "\r\n", "\n")
	sum := sha256.Sum256([]byte(strings.TrimSpace(title) + "\x00" + body + "\x00" + state + "\x00" + strings.Join(logins, ",")))
	return hex.EncodeToString(sum[:])[:32]
}

// IssueStateForCategory 把任务状态类别映射成 GitHub Issue 的开关状态与 state_reason。
// DONE → closed / completed；CANCELLED、DUPLICATE → closed / not_planned；其他 → open（不带 reason）。
func IssueStateForCategory(category string) (state, reason string) {
	switch category {
	case "DONE":
		return "closed", "completed"
	case "CANCELLED", "DUPLICATE":
		return "closed", "not_planned"
	}
	return "open", ""
}

// LinkStateForCategory 是 task_issue_links.state 的取值。
func LinkStateForCategory(category string) string {
	if state, _ := IssueStateForCategory(category); state == "closed" {
		return issueClosed
	}
	return issueOpen
}

// LinkStateForIssue 把 GitHub 的 state 转成 task_issue_links.state。
func LinkStateForIssue(state string) string {
	if strings.EqualFold(state, "closed") {
		return issueClosed
	}
	return issueOpen
}

const (
	maxOutboxAttempts = 8
	baseBackoff       = 30 * time.Second
	maxBackoff        = time.Hour
	// jitter 取值范围 [-1, 1]，对应 ±20% 抖动。
	backoffJitter = 0.2
)

// OutboxBackoff 返回第 attempts 次失败后的重试等待：30s × 2^attempts，最长 1 小时，再乘 (1 + 0.2×jitter)。
func OutboxBackoff(attempts int, jitter float64) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	wait := maxBackoff
	if attempts < 12 {
		wait = time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempts)))
		if wait > maxBackoff {
			wait = maxBackoff
		}
	}
	if jitter > 1 {
		jitter = 1
	} else if jitter < -1 {
		jitter = -1
	}
	return time.Duration(float64(wait) * (1 + backoffJitter*jitter))
}

// truncateText 按字符数截断。
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}

// inboundCommentBody 生成无法映射到成员的 GitHub 评论正文：首行「**login**（来自 GitHub · 时间）」。
func inboundCommentBody(login string, at time.Time, body string) string {
	return "**" + login + "**（来自 GitHub · " + at.UTC().Format("2006-01-02 15:04") + "）\n\n" + body
}

// outboundCommentBody 生成发往 GitHub 的评论正文。personal 为真时用原文，否则首行「**名称**（来自 OpenFox）」；末尾都追加评论标记。
func outboundCommentBody(personal bool, authorName, body, commentID string) string {
	body = strings.TrimSpace(body)
	if len([]rune(body)) > maxGitHubBody {
		body = string([]rune(body)[:maxGitHubBody])
	}
	if !personal {
		body = "**" + authorName + "**（来自 OpenFox）\n\n" + body
	}
	return body + "\n\n" + CommentMarker(commentID)
}

// IssueAssignee 是 GitHub Issue 的处理人。
type IssueAssignee struct {
	ID    int64
	Login string
}

// PickAssignees 按顺序在 Issue 处理人里找第一个能映射到成员的人（resolve 返回成员 ID 与是否可用）。
// 没有可映射的成员、但 Issue 有处理人时，返回第一个处理人作为外部账号（external 非空）。
func PickAssignees(assignees []IssueAssignee, resolve func(IssueAssignee) (string, bool)) (userID string, external *IssueAssignee) {
	for _, assignee := range assignees {
		if id, ok := resolve(assignee); ok {
			return id, nil
		}
	}
	if len(assignees) > 0 {
		first := assignees[0]
		return "", &first
	}
	return "", nil
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return data
}
