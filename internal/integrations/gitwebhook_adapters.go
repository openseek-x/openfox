package integrations

// 各 Git 服务商的 Webhook 适配。适配器只做三件事：验签、取出投递 ID 与事件名、把载荷转成 GitHub 形状的 ghPayload；
// 之后的关联任务、PR/MR 状态改任务、Issue 同步都走同一套公共逻辑。令牌与签名密钥不写日志、不进错误信息。

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"openfox/internal/automations"
)

type gitWebhookAdapter interface {
	// Verify 校验请求确实来自配置了该密钥的服务商 Webhook。
	Verify(secret []byte, header http.Header, body []byte, now time.Time) bool
	// Envelope 返回投递 ID（用于去重）与服务商原始事件名；取不到时返回空串。
	Envelope(header http.Header, body []byte) (deliveryKey, event string)
	// Parse 把载荷转成 ghPayload，并返回公共层识别的事件名（pull_request / push / issues / issue_comment，其余原样返回）。
	Parse(event string, body []byte) (string, ghPayload, error)
}

func webhookAdapterFor(provider string) gitWebhookAdapter {
	switch provider {
	case ProviderGitHub:
		return githubWebhook{}
	case ProviderGitLab, ProviderGitLabCN:
		return gitlabWebhook{}
	case ProviderGitee:
		return giteeWebhook{}
	}
	return nil
}

// constantTimeEqual 对两个字符串的 SHA-256 做常量时间比较，长度不同也不会提前返回。
func constantTimeEqual(a, b string) bool {
	left, right := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

// bodyDigestKey 是服务商不提供投递 ID 时的退路：事件名与原始正文的摘要（同一次投递的重放得到同一个键）。
func bodyDigestKey(prefix, event string, body []byte) string {
	sum := sha256.New()
	sum.Write([]byte(event))
	sum.Write([]byte{0})
	sum.Write(body)
	return prefix + hex.EncodeToString(sum.Sum(nil))[:48]
}

// ---------- GitHub ----------

type githubWebhook struct{}

func (githubWebhook) Verify(secret []byte, header http.Header, body []byte, now time.Time) bool {
	return automations.VerifySignature(true, secret, header, body, now) == automations.SignatureValid
}

func (githubWebhook) Envelope(header http.Header, _ []byte) (string, string) {
	return header.Get("X-GitHub-Delivery"), header.Get("X-GitHub-Event")
}

func (githubWebhook) Parse(event string, body []byte) (string, ghPayload, error) {
	var payload ghPayload
	err := json.Unmarshal(body, &payload)
	return event, payload, err
}

// ---------- GitLab ----------

type gitlabWebhook struct{}

// Verify：X-Gitlab-Token 是配置 Webhook 时填的 Secret token，明文随请求发送，用常量时间比较。
// GitLab 19.0 起另有 webhook-signature（HMAC-SHA256）；此处只支持 Secret token。
func (gitlabWebhook) Verify(secret []byte, header http.Header, _ []byte, _ time.Time) bool {
	token := header.Get("X-Gitlab-Token")
	return len(secret) > 0 && token != "" && constantTimeEqual(token, string(secret))
}

func (gitlabWebhook) Envelope(header http.Header, body []byte) (string, string) {
	event := header.Get("X-Gitlab-Event")
	key := strings.TrimSpace(header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(header.Get("webhook-id"))
	}
	if key == "" {
		key = strings.TrimSpace(header.Get("X-Gitlab-Event-UUID"))
	}
	if key == "" {
		key = bodyDigestKey("gl-", event, body)
	}
	return key, event
}

// glTime 兼容 GitLab 载荷里的两种时间格式："2013-12-03 17:15:43 UTC" 与 RFC 3339。
type glTime struct{ time.Time }

func (t *glTime) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || text == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", "2006-01-02T15:04:05.000Z"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return nil
}

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type glHook struct {
	ObjectKind       string          `json:"object_kind"`
	User             glUser          `json:"user"`
	UserUsername     string          `json:"user_username"`
	Ref              string          `json:"ref"`
	Commits          []glCommit      `json:"commits"`
	Project          glProject       `json:"project"`
	ObjectAttributes json.RawMessage `json:"object_attributes"`
	Issue            json.RawMessage `json:"issue"`
	Changes          json.RawMessage `json:"changes"`
	Assignees        []glUser        `json:"assignees"`
}

type glProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	VisibilityLevel   int    `json:"visibility_level"`
}

type glCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

type glMR struct {
	ID             int64  `json:"id"`
	IID            int    `json:"iid"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Action         string `json:"action"`
	SourceBranch   string `json:"source_branch"`
	Draft          bool   `json:"draft"`
	WorkInProgress bool   `json:"work_in_progress"`
	AuthorID       int64  `json:"author_id"`
	OldRev         string `json:"oldrev"`
	UpdatedAt      glTime `json:"updated_at"`
}

type glIssue struct {
	ID          int64  `json:"id"`
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Action      string `json:"action"`
	URL         string `json:"url"`
	AuthorID    int64  `json:"author_id"`
	Type        string `json:"type"`
	CreatedAt   glTime `json:"created_at"`
	UpdatedAt   glTime `json:"updated_at"`
	Assignees   []glUser
}

type glNote struct {
	ID           int64  `json:"id"`
	Note         string `json:"note"`
	NoteableType string `json:"noteable_type"`
	Action       string `json:"action"`
	AuthorID     int64  `json:"author_id"`
	CreatedAt    glTime `json:"created_at"`
}

func firstSegment(path string) string {
	owner, _, _ := strings.Cut(path, "/")
	return owner
}

func (p glProject) repository() ghRepository {
	var repo ghRepository
	repo.FullName = p.PathWithNamespace
	repo.Owner.Login = firstSegment(p.PathWithNamespace)
	// visibility_level：0 私有、10 内部、20 公开。内部仓库只对登录用户可见，按私有处理（回链开关用「私有」那一项，更保守）。
	repo.Private = p.VisibilityLevel < 20
	return repo
}

func glIssueState(state string) string {
	if state == "closed" {
		return "closed"
	}
	return "open"
}

func (g glHook) issueAssignees() []ghIssueUser {
	out := make([]ghIssueUser, 0, len(g.Assignees))
	for _, a := range g.Assignees {
		out = append(out, ghIssueUser{ID: a.ID, Login: a.Username})
	}
	return out
}

func (g glIssue) toGH(assignees []ghIssueUser) *ghIssue {
	return &ghIssue{ID: g.ID, Number: g.IID, Title: g.Title, Body: g.Description, State: glIssueState(g.State), HTMLURL: g.URL,
		User: ghIssueUser{ID: g.AuthorID}, Assignees: assignees, CreatedAt: g.CreatedAt.Time, UpdatedAt: g.UpdatedAt.Time}
}

func (gitlabWebhook) Parse(event string, body []byte) (string, ghPayload, error) {
	var hook glHook
	if err := json.Unmarshal(body, &hook); err != nil {
		return event, ghPayload{}, err
	}
	payload := ghPayload{Repository: hook.Project.repository()}
	kind := hook.ObjectKind
	switch kind {
	case "merge_request":
		var mr glMR
		if err := json.Unmarshal(hook.ObjectAttributes, &mr); err != nil {
			return event, payload, err
		}
		pr := &ghPullRequest{ID: mr.ID, Number: mr.IID, Title: mr.Title, Body: mr.Description, HTMLURL: mr.URL,
			Draft: mr.Draft || mr.WorkInProgress}
		pr.State = "open"
		if mr.State != "opened" {
			pr.State = "closed"
		}
		pr.Head.Ref = mr.SourceBranch
		pr.User.ID = mr.AuthorID
		if hook.User.ID == mr.AuthorID {
			pr.User.Login = hook.User.Username
		}
		switch {
		case mr.State == "merged" || mr.Action == "merge":
			pr.Merged = true
			merged := mr.UpdatedAt.Time
			if merged.IsZero() {
				merged = time.Now().UTC()
			}
			pr.MergedAt, pr.ClosedAt = &merged, &merged
			payload.Action = "closed"
		case mr.Action == "open":
			payload.Action = "opened"
		case mr.Action == "reopen":
			payload.Action = "reopened"
		case mr.Action == "close":
			payload.Action = "closed"
			closed := mr.UpdatedAt.Time
			if closed.IsZero() {
				closed = time.Now().UTC()
			}
			pr.ClosedAt = &closed
		case mr.Action == "update" && mr.OldRev != "":
			payload.Action = "synchronize"
		case mr.Action == "update":
			payload.Action = "edited"
		default:
			payload.Action = mr.Action // approval 等：公共层按「action not handled」忽略
		}
		payload.PullRequest = pr
		return "pull_request", payload, nil
	case "push":
		payload.Ref = hook.Ref
		for _, c := range hook.Commits {
			commit := ghCommit{ID: c.ID, Message: c.Message, URL: c.URL}
			commit.Author.Username = hook.UserUsername
			payload.Commits = append(payload.Commits, commit)
		}
		return "push", payload, nil
	case "issue":
		var issue glIssue
		if err := json.Unmarshal(hook.ObjectAttributes, &issue); err != nil {
			return event, payload, err
		}
		if issue.Type != "" && issue.Type != "Issue" {
			return "ignored", payload, nil
		}
		payload.Issue = issue.toGH(hook.issueAssignees())
		switch issue.Action {
		case "open":
			payload.Action = "opened"
		case "close":
			payload.Action = "closed"
		case "reopen":
			payload.Action = "reopened"
		case "update":
			payload.Action = "edited"
			var changes struct {
				Assignees json.RawMessage `json:"assignees"`
			}
			if json.Unmarshal(hook.Changes, &changes) == nil && len(changes.Assignees) > 0 && string(changes.Assignees) != "null" {
				payload.Action = "assigned"
			}
		default:
			payload.Action = issue.Action
		}
		return "issues", payload, nil
	case "note":
		var note glNote
		if err := json.Unmarshal(hook.ObjectAttributes, &note); err != nil {
			return event, payload, err
		}
		if note.NoteableType != "Issue" {
			return "ignored", payload, nil
		}
		var issue glIssue
		if err := json.Unmarshal(hook.Issue, &issue); err != nil {
			return event, payload, err
		}
		payload.Issue = issue.toGH(nil)
		payload.Action = "created"
		if note.Action == "update" {
			payload.Action = "updated"
		}
		comment := &ghComment{ID: note.ID, Body: note.Note, CreatedAt: note.CreatedAt.Time}
		comment.User.ID = note.AuthorID
		if hook.User.ID == note.AuthorID {
			comment.User.Login = hook.User.Username
		}
		payload.Comment = comment
		return "issue_comment", payload, nil
	}
	return event, payload, nil
}
