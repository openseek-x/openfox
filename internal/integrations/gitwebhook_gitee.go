package integrations

// Gitee Webhook 适配。协议出处：https://help.gitee.com/webhook/gitee-webhook-push-data-format 、
// https://help.gitee.com/webhook/how-to-verify-webhook-keys 、https://help.gitee.com/webhook/gitee-webhook-push-data-type 。
//   - 请求头：X-Gitee-Token（密码，或签名）、X-Gitee-Timestamp（毫秒时间戳）、X-Gitee-Event（如 Merge Request Hook）。
//   - 两种验证模式：「密码」时 X-Gitee-Token 是明文密码；「签名密钥」时 X-Gitee-Token = Base64(HmacSHA256(密钥, timestamp + "\n" + 密钥))，
//     文档的示例还会再 URL 编码一次，所以同时接受编码前后两种写法；时间戳与当前时间相差不得超过 1 小时。
//   - Gitee 没有投递 ID 头，去重用「事件名 + 正文摘要」（正文里带毫秒时间戳，同一次投递的重放得到同一个键）。
//   - 载荷形状：push_hooks / tag_push_hooks / merge_request_hooks / issue_hooks / note_hooks（正文 hook_name）。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitee"
)

// giteeSignatureWindow 是签名模式下时间戳允许的最大偏差。
const giteeSignatureWindow = time.Hour

type giteeWebhook struct{}

// Verify 先按「密码」模式比较，再按「签名密钥」模式验签。两种比较都是常量时间；密钥与令牌不进日志、不进错误信息。
func (giteeWebhook) Verify(secret []byte, header http.Header, _ []byte, now time.Time) bool {
	token := strings.TrimSpace(header.Get("X-Gitee-Token"))
	if len(secret) == 0 || token == "" {
		return false
	}
	if constantTimeEqual(token, string(secret)) {
		return true
	}
	stamp := strings.TrimSpace(header.Get("X-Gitee-Timestamp"))
	millis, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || millis <= 0 {
		return false
	}
	drift := now.Sub(time.UnixMilli(millis))
	if drift < 0 {
		drift = -drift
	}
	if drift > giteeSignatureWindow {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(stamp + "\n" + string(secret)))
	expected := []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	candidates := []string{token}
	if decoded, err := url.PathUnescape(token); err == nil && decoded != token {
		candidates = append(candidates, decoded)
	}
	if decoded, err := url.QueryUnescape(token); err == nil && decoded != token {
		candidates = append(candidates, decoded)
	}
	for _, candidate := range candidates {
		if hmac.Equal(expected, []byte(candidate)) {
			return true
		}
	}
	return false
}

func (giteeWebhook) Envelope(header http.Header, body []byte) (string, string) {
	event := strings.TrimSpace(header.Get("X-Gitee-Event"))
	return bodyDigestKey("ge-", event, body), event
}

type gtUser struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	Username string `json:"username"`
	UserName string `json:"user_name"`
}

func (u gtUser) login() string {
	for _, value := range []string{u.Login, u.Username, u.UserName} {
		if value != "" {
			return value
		}
	}
	return ""
}

type gtRepo struct {
	Path              string `json:"path"`
	Namespace         string `json:"namespace"`
	PathWithNamespace string `json:"path_with_namespace"`
	Private           bool   `json:"private"`
}

// repository 的 FullName 用「空间 path / 仓库 path」（payload 里的 full_name 是中文显示名，不能用）。
func (r gtRepo) repository() ghRepository {
	var repo ghRepository
	full := r.PathWithNamespace
	if full == "" && r.Namespace != "" && r.Path != "" {
		full = r.Namespace + "/" + r.Path
	}
	repo.FullName = full
	repo.Owner.Login = firstSegment(full)
	repo.Private = r.Private
	return repo
}

type gtCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
	Author  gtUser `json:"author"`
}

type gtHook struct {
	HookName    string          `json:"hook_name"`
	Action      string          `json:"action"`
	ActionDesc  string          `json:"action_desc"`
	Ref         string          `json:"ref"`
	Commits     []gtCommit      `json:"commits"`
	Repository  gtRepo          `json:"repository"`
	Project     gtRepo          `json:"project"`
	PullRequest json.RawMessage `json:"pull_request"`
	Issue       json.RawMessage `json:"issue"`
	Comment     json.RawMessage `json:"comment"`
	NoteableTyp string          `json:"noteable_type"`
	Sender      gtUser          `json:"sender"`
}

type gtPR struct {
	ID       int64   `json:"id"`
	Number   int     `json:"number"`
	State    string  `json:"state"`
	Title    string  `json:"title"`
	Body     *string `json:"body"`
	HTMLURL  string  `json:"html_url"`
	Draft    bool    `json:"draft"`
	Merged   bool    `json:"merged"`
	MergedAt glTime  `json:"merged_at"`
	ClosedAt glTime  `json:"closed_at"`
	User     gtUser  `json:"user"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
	UpdatedAt glTime `json:"updated_at"`
}

type gtIssue struct {
	ID        int64   `json:"id"`
	Number    string  `json:"number"`
	Title     string  `json:"title"`
	Body      *string `json:"body"`
	State     string  `json:"state"`
	HTMLURL   string  `json:"html_url"`
	User      gtUser  `json:"user"`
	Assignee  *gtUser `json:"assignee"`
	CreatedAt glTime  `json:"created_at"`
	UpdatedAt glTime  `json:"updated_at"`
}

type gtComment struct {
	ID        int64   `json:"id"`
	Body      *string `json:"body"`
	User      gtUser  `json:"user"`
	CreatedAt glTime  `json:"created_at"`
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// toGH 转成公共层的 Issue 形状：Number 是数字 id（放得进 issue_number 时），Ref 是字母数字串编号。
// id 超出 int32 时 Number 为 0，公共层按「missing issue」处理（已知局限）。
func (i gtIssue) toGH() *ghIssue {
	out := &ghIssue{ID: i.ID, Number: gitee.NumberFromID(i.ID), Ref: i.Number, Title: i.Title, Body: deref(i.Body),
		State: gitee.IssueState(i.State), HTMLURL: i.HTMLURL, User: ghIssueUser{ID: i.User.ID, Login: i.User.login()},
		CreatedAt: i.CreatedAt.Time, UpdatedAt: i.UpdatedAt.Time}
	if i.Assignee != nil && i.Assignee.login() != "" {
		out.Assignees = []ghIssueUser{{ID: i.Assignee.ID, Login: i.Assignee.login()}}
	}
	return out
}

func (giteeWebhook) Parse(event string, body []byte) (string, ghPayload, error) {
	var hook gtHook
	if err := json.Unmarshal(body, &hook); err != nil {
		return event, ghPayload{}, err
	}
	repo := hook.Repository
	if repo.PathWithNamespace == "" && repo.Path == "" {
		repo = hook.Project
	}
	payload := ghPayload{Repository: repo.repository()}
	kind := hook.HookName
	if kind == "" {
		switch event {
		case "Push Hook":
			kind = "push_hooks"
		case "Tag Push Hook":
			kind = "tag_push_hooks"
		case "Merge Request Hook":
			kind = "merge_request_hooks"
		case "Issue Hook":
			kind = "issue_hooks"
		case "Note Hook":
			kind = "note_hooks"
		}
	}
	switch kind {
	case "merge_request_hooks":
		var pr gtPR
		if err := json.Unmarshal(hook.PullRequest, &pr); err != nil {
			return event, payload, err
		}
		out := &ghPullRequest{ID: pr.ID, Number: pr.Number, Title: pr.Title, Body: deref(pr.Body), HTMLURL: pr.HTMLURL, Draft: pr.Draft}
		out.State = "open"
		if pr.State != "open" {
			out.State = "closed"
		}
		out.Head.Ref = pr.Head.Ref
		out.User.ID, out.User.Login = pr.User.ID, pr.User.login()
		switch {
		case pr.Merged || pr.State == "merged" || hook.Action == "merge":
			out.Merged = true
			merged := pr.MergedAt.Time
			if merged.IsZero() {
				merged = pr.UpdatedAt.Time
			}
			if merged.IsZero() {
				merged = time.Now().UTC()
			}
			out.MergedAt, out.ClosedAt = &merged, &merged
			payload.Action = "closed"
		case hook.Action == "open":
			payload.Action = "opened"
		case hook.Action == "reopen":
			payload.Action = "reopened"
		case hook.Action == "close":
			payload.Action = "closed"
			closed := pr.ClosedAt.Time
			if closed.IsZero() {
				closed = time.Now().UTC()
			}
			out.ClosedAt = &closed
		case hook.Action == "update" && hook.ActionDesc == "source_branch_changed":
			payload.Action = "synchronize"
		case hook.Action == "update":
			payload.Action = "edited"
		default:
			payload.Action = hook.Action // tested / approved 等：公共层按「action not handled」忽略
		}
		payload.PullRequest = out
		return "pull_request", payload, nil
	case "push_hooks":
		payload.Ref = hook.Ref
		for _, c := range hook.Commits {
			commit := ghCommit{ID: c.ID, Message: c.Message, URL: c.URL}
			commit.Author.Username = c.Author.login()
			if commit.Author.Username == "" {
				commit.Author.Username = hook.Sender.login()
			}
			payload.Commits = append(payload.Commits, commit)
		}
		return "push", payload, nil
	case "issue_hooks":
		var issue gtIssue
		if err := json.Unmarshal(hook.Issue, &issue); err != nil {
			return event, payload, err
		}
		payload.Issue = issue.toGH()
		switch hook.Action {
		case "open":
			payload.Action = "opened"
		case "state_change":
			payload.Action = "reopened"
			if payload.Issue.State == "closed" {
				payload.Action = "closed"
			}
		case "assign":
			payload.Action = "assigned"
		case "delete":
			payload.Action = "deleted"
		default:
			payload.Action = "edited"
		}
		return "issues", payload, nil
	case "note_hooks":
		if hook.NoteableTyp != "Issue" {
			return "ignored", payload, nil
		}
		var issue gtIssue
		var note gtComment
		if err := json.Unmarshal(hook.Issue, &issue); err != nil {
			return event, payload, err
		}
		if err := json.Unmarshal(hook.Comment, &note); err != nil {
			return event, payload, err
		}
		payload.Issue = issue.toGH()
		payload.Action = "created"
		payload.Comment = &ghComment{ID: note.ID, Body: deref(note.Body), CreatedAt: note.CreatedAt.Time,
			User: ghIssueUser{ID: note.User.ID, Login: note.User.login()}}
		return "issue_comment", payload, nil
	case "tag_push_hooks":
		return "ignored", payload, nil
	}
	return event, payload, nil
}
