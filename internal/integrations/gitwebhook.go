package integrations

// GitHub Webhook 接收（验签、去重、PR / push 关联任务、PR 状态驱动任务状态）与回链评论。
// 令牌与签名密钥不写入日志、错误信息和投递记录。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
)

const (
	deliveryProcessed = "PROCESSED"
	deliveryIgnored   = "IGNORED"
	deliveryFailed    = "FAILED"

	maxPushCommits = 100
)

// GitWebhookLookup 是令牌解析出的组织与连接。
type GitWebhookLookup struct {
	OrganizationID string
	ConnectionID   string
}

// GitWebhookOutcome 是接收结果。Found 为 false 表示连接已不存在（404）；Status 为 401 表示验签失败（不落库）。
type GitWebhookOutcome struct {
	Found     bool
	Status    int
	Duplicate bool
	// Backlinks 是事务提交后需要异步发出的回链评论。
	Backlinks []BacklinkJob
	// IssueBacklinks 是事务提交后需要异步发出的 Issue 回链评论。
	IssueBacklinks []IssueBacklinkJob
}

// BacklinkJob 是一条待发的 PR 回链评论。
type BacklinkJob struct {
	OrganizationID string
	ConnectionID   string
	Owner          string
	Repo           string
	Number         int
	Items          []BacklinkItem
}

type ghRepository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type ghPullRequest struct {
	ID       int64      `json:"id"`
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	HTMLURL  string     `json:"html_url"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	ClosedAt *time.Time `json:"closed_at"`
	User     struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type ghCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
	Author  struct {
		Username string `json:"username"`
	} `json:"author"`
}

type ghPayload struct {
	Action      string         `json:"action"`
	Repository  ghRepository   `json:"repository"`
	PullRequest *ghPullRequest `json:"pull_request"`
	Ref         string         `json:"ref"`
	Commits     []ghCommit     `json:"commits"`
	Issue       *ghIssue       `json:"issue"`
	Comment     *ghComment     `json:"comment"`
}

type gitContext struct {
	instanceToken  bool
	organizationID string
	connectionID   string
	deliveryID     string // 投递记录主键，用来生成唯一的动态 source_id
	createdBy      string
	settings       SettingsValues
	backlinks      []BacklinkJob
	issueBacklinks []IssueBacklinkJob
}

// ReceiveGit 在组织事务里处理一次 GitHub / GitLab / Gitee 共用的 Webhook 投递。
// 验签、投递 ID 与事件名、载荷解析由各服务商的 gitWebhookAdapter 负责；之后的关联与同步逻辑完全相同。
func (s Service) ReceiveGit(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, header http.Header, body []byte, now time.Time) (GitWebhookOutcome, error) {
	adapter := webhookAdapterFor(s.git())
	if adapter == nil {
		return GitWebhookOutcome{}, nil
	}
	var externalID, createdBy string
	var sealed []byte
	var instanceToken bool
	err := tx.QueryRow(ctx, s.gq(`
		SELECT external_id, created_by::text, credentials_sealed, (provider = 'gitlab' AND COALESCE(settings->>'auth_method','') = 'personal_token') FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND provider = 'github' AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED'`), lookup.ConnectionID).Scan(&externalID, &createdBy, &sealed, &instanceToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitWebhookOutcome{}, nil
	}
	if err != nil {
		return GitWebhookOutcome{}, err
	}
	creds, err := s.openCredentials(lookup.OrganizationID, lookup.ConnectionID, sealed)
	if err != nil {
		return GitWebhookOutcome{}, err
	}
	if !adapter.Verify([]byte(creds.WebhookSecret), header, body, now) {
		return GitWebhookOutcome{Found: true, Status: http.StatusUnauthorized}, nil
	}
	deliveryKey, event := adapter.Envelope(header, body)
	deliveryKey, event = strings.TrimSpace(deliveryKey), strings.TrimSpace(event)
	if deliveryKey == "" || len(deliveryKey) > 128 || event == "" || len(event) > 64 {
		return GitWebhookOutcome{Found: true, Status: http.StatusBadRequest}, nil
	}
	handledEvent, payload, parseErr := adapter.Parse(event, body)
	action := payload.Action
	if len(action) > 64 {
		action = action[:64]
	}
	var deliveryID string
	err = tx.QueryRow(ctx, s.gq(`
		INSERT INTO openfox.integration_webhook_deliveries (organization_id, connection_id, provider, delivery_id, event, action, signature_status, status)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'github', $2, $3, $4, 'valid', 'PROCESSED')
		ON CONFLICT (organization_id, connection_id, delivery_id) DO NOTHING
		RETURNING id::text`), lookup.ConnectionID, deliveryKey, event, action).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitWebhookOutcome{Found: true, Status: http.StatusOK, Duplicate: true}, nil
	}
	if err != nil {
		return GitWebhookOutcome{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections SET webhook_last_delivery_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, lookup.ConnectionID); err != nil {
		return GitWebhookOutcome{}, err
	}
	gc := &gitContext{instanceToken: instanceToken, organizationID: lookup.OrganizationID, connectionID: lookup.ConnectionID, deliveryID: deliveryID, createdBy: createdBy}
	status, detail, linked := deliveryFailed, "invalid payload", 0
	if parseErr == nil {
		// 处理放在保存点里：出错时回滚本次处理的写入，投递记录仍保留为 FAILED（响应 200，避免服务商反复重试）。
		sp, err := tx.Begin(ctx)
		if err != nil {
			return GitWebhookOutcome{}, err
		}
		var handleErr error
		status, detail, linked, handleErr = s.handleGitEvent(ctx, sp, gc, externalID, handledEvent, payload)
		if handleErr != nil {
			_ = sp.Rollback(ctx)
			status, detail, linked, gc.backlinks, gc.issueBacklinks = deliveryFailed, "processing failed: "+errorSummary(handleErr), 0, nil, nil
		} else if err := sp.Commit(ctx); err != nil {
			return GitWebhookOutcome{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_webhook_deliveries SET status = $2, detail = $3, linked_count = $4
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		deliveryID, status, FirstLine(detail, 500), linked); err != nil {
		return GitWebhookOutcome{}, err
	}
	return GitWebhookOutcome{Found: true, Status: http.StatusOK, Backlinks: gc.backlinks, IssueBacklinks: gc.issueBacklinks}, nil
}

func errorSummary(err error) string {
	return FirstLine(err.Error(), 200)
}

func (s Service) handleGitEvent(ctx context.Context, tx pgx.Tx, gc *gitContext, externalID, event string, payload ghPayload) (string, string, int, error) {
	if event == "ping" {
		return deliveryProcessed, "ping", 0, nil
	}
	if event != "pull_request" && event != "push" && event != "issues" && event != "issue_comment" {
		return deliveryIgnored, "event not handled", 0, nil
	}
	if !gc.instanceToken && !strings.EqualFold(payload.Repository.Owner.Login, externalID) {
		return deliveryIgnored, "owner mismatch", 0, nil
	}
	if payload.Repository.FullName == "" || !strings.Contains(payload.Repository.FullName, "/") {
		return deliveryFailed, "missing repository", 0, nil
	}
	settings, _, _, err := loadSettingsValues(ctx, tx, s.git())
	if err != nil {
		return "", "", 0, err
	}
	gc.settings = settings
	if event == "issues" || event == "issue_comment" {
		return s.handleIssueEvent(ctx, tx, gc, event, payload)
	}
	if event == "push" {
		return s.handlePush(ctx, tx, gc, payload)
	}
	return s.handlePullRequest(ctx, tx, gc, payload)
}

type resolvedTask struct {
	TaskID    string
	SpaceID   string
	DisplayID string
}

// resolveTaskRefs 一次性按（空间前缀、序号）查出存在且未删除的任务，key 为 "<小写前缀>-<序号>"。
func resolveTaskRefs(ctx context.Context, tx pgx.Tx, refs []TaskRef) (map[string]resolvedTask, error) {
	result := map[string]resolvedTask{}
	if len(refs) == 0 {
		return result, nil
	}
	prefixes := make([]string, len(refs))
	sequences := make([]int64, len(refs))
	for i, ref := range refs {
		prefixes[i], sequences[i] = ref.Prefix, ref.Sequence
	}
	// 依次按空间当前前缀、历史前缀（改过标识符）、移动前的旧编号（task_display_aliases）匹配，
	// 每个候选编号取优先级最高的一条。GitHub / GitLab / Gitee 共用。DisplayID 是任务现在的编号。
	rows, err := tx.Query(ctx, `
		WITH c AS (SELECT DISTINCT lower(prefix) AS prefix, seq FROM unnest($1::text[], $2::bigint[]) AS u(prefix, seq)),
		     sp AS (
		        SELECT c.prefix, c.seq, s.id AS space_id, 0 AS rank FROM c
		          JOIN openfox.spaces s ON s.organization_id = openfox_private.current_organization_id()
		               AND lower(s.prefix) = c.prefix AND s.deleted_at IS NULL
		        UNION ALL
		        SELECT c.prefix, c.seq, s.id, 1 FROM c
		          JOIN openfox.space_prefix_history h ON h.organization_id = openfox_private.current_organization_id()
		               AND lower(h.prefix) = c.prefix
		          JOIN openfox.spaces s ON s.organization_id = h.organization_id AND s.id = h.space_id AND s.deleted_at IS NULL),
		     hit AS (
		        SELECT sp.prefix, sp.seq, t.id, sp.rank FROM sp
		          JOIN openfox.tasks t ON t.organization_id = openfox_private.current_organization_id()
		               AND t.space_id = sp.space_id AND t.sequence = sp.seq AND t.deleted_at IS NULL
		        UNION ALL
		        SELECT sp.prefix, sp.seq, a.task_id, sp.rank + 2 FROM sp
		          JOIN openfox.task_display_aliases a ON a.organization_id = openfox_private.current_organization_id()
		               AND a.space_id = sp.space_id AND a.sequence = sp.seq),
		     best AS (SELECT DISTINCT ON (prefix, seq) prefix, seq, id FROM hit ORDER BY prefix, seq, rank)
		SELECT best.prefix, best.seq, t.id::text, t.space_id::text, s.prefix || '-' || t.sequence::text
		  FROM best
		  JOIN openfox.tasks t ON t.organization_id = openfox_private.current_organization_id() AND t.id = best.id
		       AND t.deleted_at IS NULL
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id AND s.deleted_at IS NULL`,
		prefixes, sequences)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var prefix string
		var sequence int64
		var item resolvedTask
		if err := rows.Scan(&prefix, &sequence, &item.TaskID, &item.SpaceID, &item.DisplayID); err != nil {
			return nil, err
		}
		result[prefix+"-"+strconv.FormatInt(sequence, 10)] = item
	}
	return result, rows.Err()
}

type matchedTask struct {
	resolvedTask
	MatchedIn string
}

// matchTasks 按候选顺序保留能解析到任务的编号，最多 MaxTaskRefsPerObject 个。
func matchTasks(refs []TaskRef, resolved map[string]resolvedTask) []matchedTask {
	matched := make([]matchedTask, 0, MaxTaskRefsPerObject)
	seen := map[string]bool{}
	for _, ref := range refs {
		item, ok := resolved[ref.Prefix+"-"+strconv.FormatInt(ref.Sequence, 10)]
		// 旧编号与新编号可能指向同一任务，每个任务只关联一次。
		if !ok || seen[item.TaskID] {
			continue
		}
		seen[item.TaskID] = true
		matched = append(matched, matchedTask{resolvedTask: item, MatchedIn: ref.MatchedIn})
		if len(matched) >= MaxTaskRefsPerObject {
			break
		}
	}
	return matched
}

func prState(pr *ghPullRequest) string {
	switch {
	case pr.Merged || pr.MergedAt != nil:
		return "MERGED"
	case pr.State == "closed":
		return "CLOSED"
	case pr.Draft:
		return "DRAFT"
	default:
		return "OPEN"
	}
}

var pullRequestActions = map[string]bool{"opened": true, "edited": true, "reopened": true, "closed": true, "synchronize": true,
	"ready_for_review": true, "converted_to_draft": true}

func (s Service) handlePullRequest(ctx context.Context, tx pgx.Tx, gc *gitContext, payload ghPayload) (string, string, int, error) {
	pr := payload.PullRequest
	if pr == nil || pr.Number <= 0 || pr.HTMLURL == "" {
		return deliveryFailed, "missing pull_request", 0, nil
	}
	if !pullRequestActions[payload.Action] {
		return deliveryIgnored, "action not handled", 0, nil
	}
	if !gc.settings.LinkPullRequests {
		return deliveryIgnored, "link_pull_requests disabled", 0, nil
	}
	refs := ExtractTaskRefs(RefSource{Name: "title", Text: pr.Title}, BodySource(pr.Body), RefSource{Name: "branch", Text: pr.Head.Ref})
	resolved, err := resolveTaskRefs(ctx, tx, refs)
	if err != nil {
		return "", "", 0, err
	}
	matched := matchTasks(refs, resolved)
	if len(matched) == 0 {
		return deliveryProcessed, "no task reference", 0, nil
	}
	state := prState(pr)
	var authorUserID *string
	if pr.User.ID != 0 {
		err := tx.QueryRow(ctx, s.gq(`
			SELECT owner_user_id::text FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
			   AND external_id = $1 AND status <> 'DISCONNECTED' AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid)
			 ORDER BY created_at LIMIT 1`), strconv.FormatInt(pr.User.ID, 10), s.personalParent(gc.connectionID)).Scan(&authorUserID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", 0, err
		}
	}
	actorID := gc.createdBy
	if authorUserID != nil {
		actorID = *authorUserID
	}
	actor := collaboration.Actor{UserID: actorID}
	repoPrivate := payload.Repository.Private
	var fresh []matchedTask
	for _, task := range matched {
		var inserted bool
		if err := tx.QueryRow(ctx, s.gq(`
			INSERT INTO openfox.task_git_links (organization_id, space_id, task_id, connection_id, provider, kind, repository,
			       repository_private, number, external_id, title, url, state, author_login, author_user_id, head_branch, matched_in,
			       merged_at, closed_at)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'github', 'PULL_REQUEST', $4,
			        $5, $6, $7, $8, $9, $10, $11, $12::uuid, $13, $14, $15, $16)
			ON CONFLICT (organization_id, task_id, provider, lower(repository), number) WHERE kind = 'PULL_REQUEST'
			DO UPDATE SET title = EXCLUDED.title, url = EXCLUDED.url, state = EXCLUDED.state,
			       repository_private = EXCLUDED.repository_private, external_id = EXCLUDED.external_id,
			       author_login = EXCLUDED.author_login, author_user_id = COALESCE(EXCLUDED.author_user_id, openfox.task_git_links.author_user_id),
			       head_branch = EXCLUDED.head_branch, merged_at = EXCLUDED.merged_at, closed_at = EXCLUDED.closed_at,
			       updated_at = clock_timestamp()
			RETURNING (xmax = 0)`),
			task.SpaceID, task.TaskID, gc.connectionID, payload.Repository.FullName, repoPrivate, pr.Number,
			strconv.FormatInt(pr.ID, 10), FirstLine(pr.Title, 500), pr.HTMLURL, state, FirstLine(pr.User.Login, 255), authorUserID,
			FirstLine(pr.Head.Ref, 255), task.MatchedIn, pr.MergedAt, pr.ClosedAt).Scan(&inserted); err != nil {
			return "", "", 0, err
		}
		if inserted {
			fresh = append(fresh, task)
			if err := collaboration.GitAddActivity(ctx, tx, task.SpaceID, task.TaskID, actorID, "gh-"+gc.deliveryID+"-"+task.TaskID,
				"task.pr_linked", map[string]any{"repository": payload.Repository.FullName, "number": pr.Number, "url": pr.HTMLURL}); err != nil {
				return "", "", 0, err
			}
		}
	}
	moved, err := s.applyPullRequestStatus(ctx, tx, payload.Action, state, matched, fresh, actor)
	if err != nil {
		return "", "", 0, err
	}
	// 状态变更由 tasks 表触发器发出 task.changed；只改了关联的任务补发一条，任务详情靠它刷新「关联的 Pull Request」。
	unmoved := make([]matchedTask, 0, len(matched))
	for _, task := range matched {
		if !moved[task.TaskID] {
			unmoved = append(unmoved, task)
		}
	}
	if err := emitTaskChanged(ctx, tx, unmoved); err != nil {
		return "", "", 0, err
	}
	if err := s.planBacklinks(ctx, tx, gc, payload, fresh); err != nil {
		return "", "", 0, err
	}
	return deliveryProcessed, "linked", len(matched), nil
}

// applyPullRequestStatus 按用户确认的规则改任务状态：
// PR 打开（opened / reopened，以及本次新关联到仍打开的 PR）时移到第一个进行中状态；合并时移到第一个已完成状态；关闭未合并不改。
func (s Service) applyPullRequestStatus(ctx context.Context, tx pgx.Tx, action, state string, matched, fresh []matchedTask, actor collaboration.Actor) (map[string]bool, error) {
	svc := collaboration.Service{}
	moved := map[string]bool{}
	switch {
	case state == "MERGED":
		for _, task := range matched {
			changed, err := svc.GitApplyStatus(ctx, tx, task.TaskID, "DONE", actor)
			if err != nil {
				return nil, err
			}
			moved[task.TaskID] = changed
		}
	case state == "OPEN" || state == "DRAFT":
		targets := fresh
		if action == "opened" || action == "reopened" {
			targets = matched
		}
		for _, task := range targets {
			changed, err := svc.GitApplyStatus(ctx, tx, task.TaskID, "IN_PROGRESS", actor)
			if err != nil {
				return nil, err
			}
			moved[task.TaskID] = changed
		}
	}
	return moved, nil
}

// emitTaskChanged 按空间合并写入 task.changed 组织事件（格式与 000029 的任务触发器一致，每条至多 100 个任务）。
func emitTaskChanged(ctx context.Context, tx pgx.Tx, tasks []matchedTask) error {
	bySpace := map[string][]string{}
	order := make([]string, 0)
	for _, task := range tasks {
		if _, ok := bySpace[task.SpaceID]; !ok {
			order = append(order, task.SpaceID)
		}
		bySpace[task.SpaceID] = append(bySpace[task.SpaceID], task.TaskID)
	}
	for _, spaceID := range order {
		ids := bySpace[spaceID]
		if len(ids) > 100 {
			ids = ids[:100]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
			VALUES (openfox_private.current_organization_id(), 'task.changed', 'task', $1::uuid,
			        jsonb_build_object('task_ids', to_jsonb($2::text[]), 'count', cardinality($2::text[])))`, spaceID, ids); err != nil {
			return err
		}
	}
	return nil
}

// planBacklinks 决定是否在 PR 下回链：新建了关联、对应的私有 / 公开开关打开，且该 PR 还没有任何回链评论。
func (s Service) planBacklinks(ctx context.Context, tx pgx.Tx, gc *gitContext, payload ghPayload, fresh []matchedTask) error {
	if len(fresh) == 0 {
		return nil
	}
	if (payload.Repository.Private && !gc.settings.PRBacklinkPrivate) || (!payload.Repository.Private && !gc.settings.PRBacklinkPublic) {
		return nil
	}
	var commented bool
	if err := tx.QueryRow(ctx, s.gq(`
		SELECT EXISTS (SELECT 1 FROM openfox.task_git_links
		                WHERE organization_id = openfox_private.current_organization_id() AND kind = 'PULL_REQUEST' AND provider = 'github'
		                  AND lower(repository) = lower($1) AND number = $2 AND backlink_comment_id <> '')`),
		payload.Repository.FullName, payload.PullRequest.Number).Scan(&commented); err != nil {
		return err
	}
	if commented {
		return nil
	}
	owner, repo, _ := strings.Cut(payload.Repository.FullName, "/")
	job := BacklinkJob{OrganizationID: gc.organizationID, ConnectionID: gc.connectionID, Owner: owner, Repo: repo, Number: payload.PullRequest.Number}
	for _, task := range fresh {
		var linkID string
		if err := tx.QueryRow(ctx, s.gq(`
			SELECT id::text FROM openfox.task_git_links
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid AND kind = 'PULL_REQUEST'
			   AND provider = 'github' AND lower(repository) = lower($2) AND number = $3`),
			task.TaskID, payload.Repository.FullName, payload.PullRequest.Number).Scan(&linkID); err != nil {
			return err
		}
		job.Items = append(job.Items, BacklinkItem{LinkID: linkID, DisplayID: task.DisplayID, SpaceID: task.SpaceID})
	}
	gc.backlinks = append(gc.backlinks, job)
	return nil
}

func (s Service) handlePush(ctx context.Context, tx pgx.Tx, gc *gitContext, payload ghPayload) (string, string, int, error) {
	if !gc.settings.LinkCommits {
		return deliveryIgnored, "link_commits disabled", 0, nil
	}
	commits := payload.Commits
	if len(commits) > maxPushCommits {
		commits = commits[:maxPushCommits]
	}
	perCommit := make([][]TaskRef, len(commits))
	var all []TaskRef
	for i, commit := range commits {
		perCommit[i] = ExtractTaskRefs(RefSource{Name: "message", Text: commit.Message})
		all = append(all, perCommit[i]...)
	}
	resolved, err := resolveTaskRefs(ctx, tx, all)
	if err != nil {
		return "", "", 0, err
	}
	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	linked := 0
	touched := map[string]matchedTask{}
	for i, commit := range commits {
		sha := strings.ToLower(commit.ID)
		if !validSHA(sha) || commit.URL == "" {
			continue
		}
		for _, task := range matchTasks(perCommit[i], resolved) {
			tag, err := tx.Exec(ctx, s.gq(`
				INSERT INTO openfox.task_git_links (organization_id, space_id, task_id, connection_id, provider, kind, repository,
				       repository_private, sha, title, url, state, author_login, head_branch, matched_in)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'github', 'COMMIT', $4,
				        $5, $6, $7, $8, 'COMMITTED', $9, $10, 'message')
				ON CONFLICT (organization_id, task_id, provider, lower(repository), sha) WHERE kind = 'COMMIT'
				DO UPDATE SET title = EXCLUDED.title, url = EXCLUDED.url, updated_at = clock_timestamp()`),
				task.SpaceID, task.TaskID, gc.connectionID, payload.Repository.FullName, payload.Repository.Private, sha,
				FirstLine(commit.Message, 500), commit.URL, FirstLine(commit.Author.Username, 255), FirstLine(branch, 255))
			if err != nil {
				return "", "", 0, err
			}
			linked += int(tag.RowsAffected())
			touched[task.TaskID] = task
		}
	}
	if linked == 0 {
		return deliveryProcessed, "no task reference", 0, nil
	}
	changed := make([]matchedTask, 0, len(touched))
	for _, task := range touched {
		changed = append(changed, task)
	}
	if err := emitTaskChanged(ctx, tx, changed); err != nil {
		return "", "", 0, err
	}
	return deliveryProcessed, "linked", linked, nil
}

func validSHA(sha string) bool {
	if len(sha) < 7 || len(sha) > 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ---------- 回链评论（事务提交后异步执行） ----------

// GitAccess 是一个连接的访问令牌与对应的服务商客户端（令牌只在内存里使用）。
type GitAccess struct {
	Client gitapi.Client
	Token  string
}

// BacklinkAccess 读取连接的访问令牌并构造客户端（组织事务内）。连接已断开或密文无法解密时返回错误。
func (s Service) BacklinkAccess(ctx context.Context, tx pgx.Tx, organizationID, connectionID string) (GitAccess, error) {
	if !membership.ValidateUUID(connectionID) {
		return GitAccess{}, ErrNotFound
	}
	org, err := s.loadOrgConnection(ctx, tx, organizationID, connectionID, true)
	if err != nil {
		return GitAccess{}, err
	}
	return GitAccess{Client: org.Client, Token: org.Token}, nil
}

// PostBacklink 在 PR（MR）下发评论，返回评论 id。
func (s Service) PostBacklink(ctx context.Context, access GitAccess, job BacklinkJob) (string, error) {
	if access.Client == nil {
		return "", ErrNotConfigured
	}
	return access.Client.CommentPullRequest(ctx, access.Token, job.Owner, job.Repo, job.Number, BacklinkBody(s.PublicURL, job.OrganizationID, job.Items))
}

// RecordBacklink 记录回链评论 id（只写还没有回链评论的关联）。
func (s Service) RecordBacklink(ctx context.Context, tx pgx.Tx, job BacklinkJob, commentID string) error {
	ids := make([]string, len(job.Items))
	for i, item := range job.Items {
		ids[i] = item.LinkID
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_git_links SET backlink_comment_id = $2, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[]) AND backlink_comment_id = ''`,
		ids, FirstLine(commentID, 64))
	return err
}

// MarkTokenRevoked 把连接标为需要重新授权（界面显示「需要重新授权」）。
func (s Service) MarkTokenRevoked(ctx context.Context, tx pgx.Tx, connectionID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET status = 'NEEDS_ATTENTION', status_reason = 'token_revoked', updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'`, connectionID)
	return err
}

// IsTokenRevoked 判断服务商客户端错误是否为令牌失效。
func IsTokenRevoked(err error) bool { return errors.Is(err, gitapi.ErrTokenRevoked) }

// ---------- 任务的 Git 关联列表 ----------

// TaskGitLink 对应契约 TaskGitLink。
type TaskGitLink struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Provider     string     `json:"provider"`
	Repository   string     `json:"repository"`
	Number       *int       `json:"number"`
	SHA          string     `json:"sha"`
	Title        string     `json:"title"`
	URL          string     `json:"url"`
	State        string     `json:"state"`
	AuthorLogin  string     `json:"author_login"`
	AuthorUserID *string    `json:"author_user_id"`
	HeadBranch   string     `json:"head_branch"`
	MatchedIn    string     `json:"matched_in"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	MergedAt     *time.Time `json:"merged_at"`
}

const maxGitLinks = 100

// ListTaskGitLinks 返回任务的 PR（创建时间倒序）与 Commit（创建时间倒序）关联，最多 100 条。
// 调用方已完成任务读取的鉴权。
func (s Service) ListTaskGitLinks(ctx context.Context, tx pgx.Tx, spaceID, taskID string) ([]TaskGitLink, error) {
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return nil, collaboration.ErrTaskNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.tasks WHERE organization_id = openfox_private.current_organization_id()
		                  AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL)`, spaceID, taskID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, collaboration.ErrTaskNotFound
	}
	items := make([]TaskGitLink, 0)
	// ISSUE 排在最前（一个任务至多一个未解除的 Issue 关联）。
	issueRows, err := tx.Query(ctx, `
		SELECT il.id::text, il.repository, il.issue_number, t.title, il.url, il.state, il.created_at, il.updated_at, sc.provider
		  FROM openfox.task_issue_links il
		  JOIN openfox.tasks t ON t.organization_id = il.organization_id AND t.id = il.task_id
		  JOIN openfox.integration_sync_links sl ON sl.organization_id = il.organization_id AND sl.id = il.sync_link_id
		  JOIN openfox.integration_connections sc ON sc.organization_id = sl.organization_id AND sc.id = sl.connection_id
		 WHERE il.organization_id = openfox_private.current_organization_id() AND il.task_id = $1::uuid AND il.unlinked_at IS NULL
		 ORDER BY il.created_at DESC, il.id LIMIT $2`, taskID, maxGitLinks)
	if err != nil {
		return nil, err
	}
	for issueRows.Next() {
		item := TaskGitLink{Kind: "ISSUE"}
		var number int32
		if err := issueRows.Scan(&item.ID, &item.Repository, &number, &item.Title, &item.URL, &item.State, &item.CreatedAt, &item.UpdatedAt, &item.Provider); err != nil {
			issueRows.Close()
			return nil, err
		}
		value := int(number)
		item.Number = &value
		items = append(items, item)
	}
	issueRows.Close()
	if err := issueRows.Err(); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, kind, provider, repository, number, sha, title, url, state, author_login, author_user_id::text,
		       head_branch, matched_in, created_at, updated_at, merged_at
		  FROM openfox.task_git_links
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
		 ORDER BY (kind = 'PULL_REQUEST') DESC, created_at DESC, id
		 LIMIT $2`, taskID, maxGitLinks-len(items))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item TaskGitLink
		var number *int32
		if err := rows.Scan(&item.ID, &item.Kind, &item.Provider, &item.Repository, &number, &item.SHA, &item.Title, &item.URL,
			&item.State, &item.AuthorLogin, &item.AuthorUserID, &item.HeadBranch, &item.MatchedIn, &item.CreatedAt, &item.UpdatedAt,
			&item.MergedAt); err != nil {
			return nil, err
		}
		if number != nil {
			value := int(*number)
			item.Number = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
