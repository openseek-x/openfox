package integrations

// issues / issue_comment Webhook 的入站处理，以及「按 Issue 建任务」的共用逻辑（导入已有 Issue 也用）。
// 入站写入前设置事务级标记 openfox.sync_source，000049 的触发器因此不会把这次写入再入队发回 GitHub。
// 不设置 openfox.import_job 标记，状态变更自动化与评论通知照常触发。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/policy"
)

type ghIssueUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type ghIssue struct {
	ID          int64         `json:"id"`
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	State       string        `json:"state"`
	StateReason string        `json:"state_reason"`
	HTMLURL     string        `json:"html_url"`
	User        ghIssueUser   `json:"user"`
	Assignees   []ghIssueUser `json:"assignees"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	PullRequest *struct{}     `json:"pull_request"`
	// Ref 是 API 里引用该 Issue 的标识（Gitee 的字母数字串编号）；GitHub / GitLab 为空。适配器填写，不来自 JSON。
	Ref string `json:"-"`
}

type ghComment struct {
	ID        int64       `json:"id"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	User      ghIssueUser `json:"user"`
}

// issueData 是 Issue 的规范化内容（来自 Webhook 或 REST）。
type issueData struct {
	ID          int64
	Ref         string // API 里引用该 Issue 的标识；空表示就是 Number（Gitee 的 Issue 编号是字母数字串，GitHub / GitLab 为空）
	Number      int
	Title       string
	Body        string
	State       string // open / closed
	StateReason string
	URL         string
	Assignees   []IssueAssignee
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func issueFromWebhook(in *ghIssue) issueData {
	data := issueData{ID: in.ID, Ref: in.Ref, Number: in.Number, Title: in.Title, Body: in.Body, State: in.State, StateReason: in.StateReason,
		URL: in.HTMLURL, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
	for _, a := range in.Assignees {
		data.Assignees = append(data.Assignees, IssueAssignee{ID: a.ID, Login: a.Login})
	}
	return data
}

func issueFromREST(in gitapi.Issue) issueData {
	data := issueData{ID: in.ID, Ref: in.Ref, Number: in.Number, Title: in.Title, Body: in.Body, State: in.State, StateReason: in.StateReason,
		URL: in.HTMLURL, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
	for _, a := range in.Assignees {
		data.Assignees = append(data.Assignees, IssueAssignee{ID: a.ID, Login: a.Login})
	}
	return data
}

func (d issueData) hash() string {
	logins := make([]string, len(d.Assignees))
	for i, a := range d.Assignees {
		logins[i] = a.Login
	}
	return IssueContentHash(d.Title, d.Body, LinkStateForIssue(d.State), logins)
}

// syncLinkRow 是入站处理需要的同步关联信息。
type syncLinkRow struct {
	ConnectionID string // 所属组织连接（GitLab 系据此找个人连接）
	ID           string
	SpaceID      string
	Direction    string
	Status       string
}

// IssueBacklinkJob 是一条待发的 Issue 回链评论（事务提交后异步发出）。
type IssueBacklinkJob struct {
	OrganizationID string
	ConnectionID   string
	Owner          string
	Repo           string
	Number         int
	Ref            string // Issue 的 API 标识；空表示 Number
	IssueLinkID    string
	DisplayID      string
	SpaceID        string
}

// loadSyncActor 选出入站写入使用的操作者：优先用连接创建者，已不是活跃成员时退到组织所有者。
func loadSyncActor(ctx context.Context, tx pgx.Tx, preferredUserID string) (collaboration.Actor, error) {
	var userID, role string
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT user_id::text, role, authorization_version FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'ACTIVE'
		 ORDER BY (user_id = $1::uuid) DESC, (role = 'OWNER') DESC, created_at, user_id LIMIT 1`, preferredUserID).Scan(&userID, &role, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return collaboration.Actor{}, ErrNotFound
	}
	if err != nil {
		return collaboration.Actor{}, err
	}
	return collaboration.Actor{UserID: userID, Role: policy.OrganizationRole(strings.ToLower(role)), AuthorizationVersion: uint64(version)}, nil
}

// resolveAssignee 按 Issue 处理人的顺序找第一个绑定了个人账号、且能读该空间的成员；
// 找不到时第一个处理人作为外部账号（provider 为当前 Git 服务商）。connectionID 是所属组织连接（GitLab 系按它区分实例）。
func (s Service) resolveAssignee(ctx context.Context, tx pgx.Tx, spaceID string, actor collaboration.Actor, connectionID, repository string, assignees []IssueAssignee) (userID, externalID string, err error) {
	var failure error
	userID, external := PickAssignees(assignees, func(a IssueAssignee) (string, bool) {
		if failure != nil || a.ID == 0 {
			return "", false
		}
		var owner string
		err := tx.QueryRow(ctx, s.gq(`
			SELECT owner_user_id::text FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
			   AND external_id = $1 AND status <> 'DISCONNECTED' AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid)
			 ORDER BY created_at LIMIT 1`), strconv.FormatInt(a.ID, 10), s.personalParent(connectionID)).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false
		}
		if err != nil {
			failure = err
			return "", false
		}
		if !(collaboration.Service{}).ImportAssignable(ctx, tx, spaceID, actor, owner) {
			return "", false
		}
		return owner, true
	})
	if failure != nil {
		return "", "", failure
	}
	if external == nil {
		return userID, "", nil
	}
	err = tx.QueryRow(ctx, s.gq(`
		INSERT INTO openfox.external_accounts (organization_id, provider, external_user_id, display_name, created_by, source_label)
		VALUES (openfox_private.current_organization_id(), 'github', $1, $2, $3::uuid, $4)
		ON CONFLICT (organization_id, provider, lower(external_user_id)) DO UPDATE
		   SET display_name = EXCLUDED.display_name, updated_at = clock_timestamp(), version = openfox.external_accounts.version + 1
		RETURNING id::text`), strconv.FormatInt(external.ID, 10), FirstLine(external.Login, 200), actor.UserID, FirstLine(repository, 255)).Scan(&externalID)
	return "", externalID, err
}

// createTaskFromIssue 在同步关联的空间里按 Issue 新建任务并建立任务 ↔ Issue 关联，返回任务 ID 与显示编号（如 FOX-12）。
func (s Service) createTaskFromIssue(ctx context.Context, tx pgx.Tx, organizationID string, link syncLinkRow, actor collaboration.Actor,
	repository, activityKey string, in issueData) (issueLinkID, taskID, displayID string, err error) {
	userID, externalID, err := s.resolveAssignee(ctx, tx, link.SpaceID, actor, link.ConnectionID, repository, in.Assignees)
	if err != nil {
		return "", "", "", err
	}
	input := collaboration.ImportTaskInput{
		Title:              truncateText(strings.TrimSpace(in.Title), maxIssueTitle),
		Description:        truncateText(StripTaskMarker(in.Body), maxIssueBody),
		AssigneeUserID:     userID,
		ExternalAssigneeID: externalID,
		CreatedAt:          in.CreatedAt,
		SourceKey:          fmt.Sprintf("%s#%d", repository, in.Number),
		Source:             s.git(),
	}
	taskID, _, err = (collaboration.Service{}).CreateIssueTask(ctx, tx, organizationID, link.SpaceID, actor, input)
	if err != nil {
		return "", "", "", err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.task_issue_links (organization_id, sync_link_id, space_id, task_id, repository, issue_number, issue_id, url, state,
		       last_hash, remote_updated_at, issue_ref)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id::text`, link.ID, link.SpaceID, taskID, repository, in.Number, strconv.FormatInt(in.ID, 10), in.URL,
		LinkStateForIssue(in.State), in.hash(), nullTime(in.UpdatedAt), in.Ref).Scan(&issueLinkID); err != nil {
		return "", "", "", err
	}
	if err := tx.QueryRow(ctx, `
		SELECT sp.prefix || '-' || t.sequence::text FROM openfox.tasks t
		  JOIN openfox.spaces sp ON sp.organization_id = t.organization_id AND sp.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid`, taskID).Scan(&displayID); err != nil {
		return "", "", "", err
	}
	if err := collaboration.GitAddActivity(ctx, tx, link.SpaceID, taskID, actor.UserID, activityKey, "task.issue_linked",
		map[string]any{"repository": repository, "number": in.Number, "url": in.URL}); err != nil {
		return "", "", "", err
	}
	return issueLinkID, taskID, displayID, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func touchSyncLink(ctx context.Context, tx pgx.Tx, syncLinkID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_links SET last_synced_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, syncLinkID)
	return err
}

// ---------- Webhook 入口 ----------

func (s Service) handleIssueEvent(ctx context.Context, tx pgx.Tx, gc *gitContext, event string, payload ghPayload) (string, string, int, error) {
	link := syncLinkRow{ConnectionID: gc.connectionID}
	err := tx.QueryRow(ctx, `
		SELECT id::text, space_id::text, direction, status FROM openfox.integration_sync_links
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid
		   AND lower(external_project_id) = lower($2) AND deleted_at IS NULL`, gc.connectionID, payload.Repository.FullName).
		Scan(&link.ID, &link.SpaceID, &link.Direction, &link.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryIgnored, "no sync link", 0, nil
	}
	if err != nil {
		return "", "", 0, err
	}
	if link.Status == "PAUSED" {
		return deliveryIgnored, "paused", 0, nil
	}
	if payload.Issue == nil || payload.Issue.Number <= 0 {
		return deliveryFailed, "missing issue", 0, nil
	}
	if payload.Issue.PullRequest != nil {
		return deliveryIgnored, "pull request", 0, nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.sync_source', 'github', true)`); err != nil {
		return "", "", 0, err
	}
	if event == "issue_comment" {
		return s.handleIssueComment(ctx, tx, gc, link, payload)
	}
	return s.handleIssues(ctx, tx, gc, link, payload)
}

type issueLinkRow struct {
	ID, TaskID, SpaceID, LastHash, State string
}

func findIssueLink(ctx context.Context, tx pgx.Tx, syncLinkID string, number int) (issueLinkRow, bool, error) {
	var row issueLinkRow
	err := tx.QueryRow(ctx, `
		SELECT id::text, task_id::text, space_id::text, last_hash, state FROM openfox.task_issue_links
		 WHERE organization_id = openfox_private.current_organization_id() AND sync_link_id = $1::uuid AND issue_number = $2 AND unlinked_at IS NULL`,
		syncLinkID, number).Scan(&row.ID, &row.TaskID, &row.SpaceID, &row.LastHash, &row.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return issueLinkRow{}, false, nil
	}
	return row, err == nil, err
}

func (s Service) handleIssues(ctx context.Context, tx pgx.Tx, gc *gitContext, link syncLinkRow, payload ghPayload) (string, string, int, error) {
	issue := issueFromWebhook(payload.Issue)
	repository := payload.Repository.FullName
	switch payload.Action {
	case "opened", "edited", "closed", "reopened", "assigned", "unassigned":
	case "deleted", "transferred":
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.task_issue_links SET unlinked_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND sync_link_id = $1::uuid AND issue_number = $2 AND unlinked_at IS NULL`,
			link.ID, issue.Number)
		if err != nil {
			return "", "", 0, err
		}
		return deliveryProcessed, "unlinked", int(tag.RowsAffected()), nil
	default:
		return deliveryIgnored, "action not handled", 0, nil
	}
	existing, found, err := findIssueLink(ctx, tx, link.ID, issue.Number)
	if err != nil {
		return "", "", 0, err
	}
	if !found {
		// 开启同步之前就存在的 Issue：有了新改动（edited / reopened 且仍打开）也同步进来。
		create := payload.Action == "opened" || ((payload.Action == "edited" || payload.Action == "reopened") && issue.State == "open")
		if !create {
			return deliveryIgnored, "no issue link", 0, nil
		}
		return s.openIssue(ctx, tx, gc, link, repository, payload.Repository.Private, issue)
	}
	if issue.hash() == existing.LastHash {
		return deliveryIgnored, "echo", 0, nil
	}
	actor, err := loadSyncActor(ctx, tx, gc.createdBy)
	if err != nil {
		return "", "", 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET title = $2, description = $3, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
		existing.TaskID, truncateText(strings.TrimSpace(issue.Title), maxIssueTitle), truncateText(StripTaskMarker(issue.Body), maxIssueBody))
	if err != nil {
		return "", "", 0, err
	}
	if tag.RowsAffected() != 1 {
		return deliveryIgnored, "task deleted", 0, nil
	}
	svc := collaboration.Service{}
	newState := LinkStateForIssue(issue.State)
	if newState != existing.State || payload.Action == "closed" || payload.Action == "reopened" {
		if _, err := svc.GitSyncStatus(ctx, tx, existing.TaskID, newState == issueClosed, issue.StateReason == "not_planned", actor); err != nil {
			return "", "", 0, err
		}
	}
	// 处理人只在 assigned / unassigned 时重算，避免 GitHub 上改个标题就冲掉 OpenFox 里的多处理人。
	if payload.Action == "assigned" || payload.Action == "unassigned" {
		if err := s.replaceHumanAssignee(ctx, tx, link.SpaceID, existing.TaskID, actor, gc.connectionID, repository, issue.Assignees); err != nil {
			return "", "", 0, err
		}
	}
	if err := collaboration.GitAddActivity(ctx, tx, existing.SpaceID, existing.TaskID, actor.UserID, "gh-"+gc.deliveryID+"-"+existing.TaskID,
		"task.issue_synced", map[string]any{"repository": repository, "number": issue.Number, "action": payload.Action}); err != nil {
		return "", "", 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_issue_links SET last_hash = $2, state = $3, remote_updated_at = $4, url = $5, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		existing.ID, issue.hash(), newState, nullTime(issue.UpdatedAt), issue.URL); err != nil {
		return "", "", 0, err
	}
	if err := touchSyncLink(ctx, tx, link.ID); err != nil {
		return "", "", 0, err
	}
	return deliveryProcessed, "synced", 1, nil
}

// replaceHumanAssignee 用 Issue 的处理人替换任务的人员处理人（成员或外部账号），不动 Agent 处理人。
func (s Service) replaceHumanAssignee(ctx context.Context, tx pgx.Tx, spaceID, taskID string, actor collaboration.Actor, connectionID, repository string, assignees []IssueAssignee) error {
	userID, externalID, err := s.resolveAssignee(ctx, tx, spaceID, actor, connectionID, repository, assignees)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_assignees
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid AND kind = 'HUMAN'`, taskID); err != nil {
		return err
	}
	if userID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)`,
			spaceID, taskID, userID, actor.UserID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.tasks SET assignee_id = NULLIF($2, '')::uuid,
		       external_assignee_id = CASE WHEN $2 <> '' THEN NULL ELSE NULLIF($3, '')::uuid END,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, taskID, userID, externalID)
	return err
}

// openIssue 处理「Issue 还没有关联」：带 OpenFox 标记的只补关联，否则新建任务并按设置安排回链评论。
func (s Service) openIssue(ctx context.Context, tx pgx.Tx, gc *gitContext, link syncLinkRow, repository string, private bool, issue issueData) (string, string, int, error) {
	if taskID, ok := ParseTaskMarker(issue.Body); ok {
		var spaceTask bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.tasks WHERE organization_id = openfox_private.current_organization_id()
			                 AND id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL)`, taskID, link.SpaceID).Scan(&spaceTask); err != nil {
			return "", "", 0, err
		}
		if spaceTask {
			tag, err := tx.Exec(ctx, `
				UPDATE openfox.task_issue_links SET issue_number = $3, issue_id = $4, url = $5, last_hash = $6, state = $7,
				       remote_updated_at = $8, issue_ref = $9, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid AND sync_link_id = $2::uuid AND unlinked_at IS NULL`,
				taskID, link.ID, issue.Number, strconv.FormatInt(issue.ID, 10), issue.URL, issue.hash(), LinkStateForIssue(issue.State), nullTime(issue.UpdatedAt), issue.Ref)
			if err != nil {
				return "", "", 0, err
			}
			if tag.RowsAffected() == 0 {
				if _, err := tx.Exec(ctx, `
					INSERT INTO openfox.task_issue_links (organization_id, sync_link_id, space_id, task_id, repository, issue_number, issue_id, url, state,
					       last_hash, remote_updated_at, issue_ref)
					VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)`,
					link.ID, link.SpaceID, taskID, repository, issue.Number, strconv.FormatInt(issue.ID, 10), issue.URL,
					LinkStateForIssue(issue.State), issue.hash(), nullTime(issue.UpdatedAt), issue.Ref); err != nil {
					return "", "", 0, err
				}
			}
			if err := touchSyncLink(ctx, tx, link.ID); err != nil {
				return "", "", 0, err
			}
			return deliveryProcessed, "linked existing task", 1, nil
		}
	}
	actor, err := loadSyncActor(ctx, tx, gc.createdBy)
	if err != nil {
		return "", "", 0, err
	}
	issueLinkID, taskID, displayID, err := s.createTaskFromIssue(ctx, tx, gc.organizationID, link, actor, repository, "gh-"+gc.deliveryID+"-new", issue)
	if err != nil {
		return "", "", 0, err
	}
	_ = taskID
	if err := touchSyncLink(ctx, tx, link.ID); err != nil {
		return "", "", 0, err
	}
	if (private && gc.settings.IssueBacklinkPrivate) || (!private && gc.settings.IssueBacklinkPublic) {
		owner, repo, _ := strings.Cut(repository, "/")
		gc.issueBacklinks = append(gc.issueBacklinks, IssueBacklinkJob{OrganizationID: gc.organizationID, ConnectionID: gc.connectionID,
			Owner: owner, Repo: repo, Number: issue.Number, Ref: issue.Ref, IssueLinkID: issueLinkID, DisplayID: displayID, SpaceID: link.SpaceID})
	}
	return deliveryProcessed, "created task", 1, nil
}

func (s Service) handleIssueComment(ctx context.Context, tx pgx.Tx, gc *gitContext, link syncLinkRow, payload ghPayload) (string, string, int, error) {
	if payload.Action != "created" {
		return deliveryIgnored, "action not handled", 0, nil
	}
	comment := payload.Comment
	if comment == nil || comment.ID == 0 {
		return deliveryFailed, "missing comment", 0, nil
	}
	if HasOpenFoxMarker(comment.Body) {
		return deliveryIgnored, "openfox marker", 0, nil
	}
	body := strings.TrimSpace(comment.Body)
	if body == "" {
		return deliveryIgnored, "empty comment", 0, nil
	}
	existing, found, err := findIssueLink(ctx, tx, link.ID, payload.Issue.Number)
	if err != nil {
		return "", "", 0, err
	}
	if !found {
		return deliveryIgnored, "no issue link", 0, nil
	}
	var duplicate bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.task_comment_links
		                WHERE organization_id = openfox_private.current_organization_id() AND issue_link_id = $1::uuid AND remote_comment_id = $2)`,
		existing.ID, strconv.FormatInt(comment.ID, 10)).Scan(&duplicate); err != nil {
		return "", "", 0, err
	}
	if duplicate {
		return deliveryIgnored, "duplicate comment", 0, nil
	}
	var taskAlive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.tasks WHERE organization_id = openfox_private.current_organization_id()
		                 AND id = $1::uuid AND deleted_at IS NULL)`, existing.TaskID).Scan(&taskAlive); err != nil {
		return "", "", 0, err
	}
	if !taskAlive {
		return deliveryIgnored, "task deleted", 0, nil
	}
	actor, err := loadSyncActor(ctx, tx, gc.createdBy)
	if err != nil {
		return "", "", 0, err
	}
	authorID := ""
	if comment.User.ID != 0 {
		var owner string
		err := tx.QueryRow(ctx, s.gq(`
			SELECT owner_user_id::text FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
			   AND external_id = $1 AND status <> 'DISCONNECTED' AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid)
			 ORDER BY created_at LIMIT 1`), strconv.FormatInt(comment.User.ID, 10), s.personalParent(gc.connectionID)).Scan(&owner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", 0, err
		}
		if err == nil && (collaboration.Service{}).ImportAssignable(ctx, tx, existing.SpaceID, actor, owner) {
			authorID = owner
		}
	}
	text := body
	if authorID == "" {
		authorID = actor.UserID
		text = inboundCommentBody(comment.User.Login, comment.CreatedAt, body)
	}
	text = truncateText(text, 65000)
	var commentID string
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, client_comment_id, created_at)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, COALESCE($6::timestamptz, clock_timestamp()))
		ON CONFLICT DO NOTHING RETURNING id::text`,
		existing.SpaceID, existing.TaskID, authorID, text, s.git()+":"+strconv.FormatInt(comment.ID, 10), nullTime(comment.CreatedAt)).Scan(&commentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryIgnored, "duplicate comment", 0, nil
	}
	if err != nil {
		return "", "", 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_comment_links (organization_id, issue_link_id, comment_id, remote_comment_id, direction)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'INBOUND')`,
		existing.ID, commentID, strconv.FormatInt(comment.ID, 10)); err != nil {
		return "", "", 0, err
	}
	if err := touchSyncLink(ctx, tx, link.ID); err != nil {
		return "", "", 0, err
	}
	return deliveryProcessed, "comment synced", 1, nil
}

// ---------- 回链评论（事务提交后异步执行） ----------

// IssueBacklinkBody 生成回链评论正文，末尾带标记，入站时据此忽略。
func IssueBacklinkBody(publicURL, organizationID string, job IssueBacklinkJob) string {
	base := strings.TrimRight(publicURL, "/")
	return fmt.Sprintf("%s 已同步为 OpenFox 任务：%s/o/%s/spaces/%s/tasks?task=%s\n\n%s",
		job.DisplayID, base, organizationID, job.SpaceID, job.DisplayID, backlinkMark)
}

// PostIssueBacklink 在 Issue 下发回链评论，返回评论 id。
func (s Service) PostIssueBacklink(ctx context.Context, access GitAccess, job IssueBacklinkJob) (string, error) {
	if access.Client == nil {
		return "", ErrNotConfigured
	}
	ref := job.Ref
	if ref == "" {
		ref = strconv.Itoa(job.Number)
	}
	return access.Client.CommentIssue(ctx, access.Token, job.Owner, job.Repo, ref, IssueBacklinkBody(s.PublicURL, job.OrganizationID, job))
}

// RecordIssueBacklink 记录回链评论 id（只写还没有回链评论的关联）。
func (s Service) RecordIssueBacklink(ctx context.Context, tx pgx.Tx, job IssueBacklinkJob, commentID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_issue_links SET backlink_comment_id = $2, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND backlink_comment_id = ''`,
		job.IssueLinkID, FirstLine(commentID, 64))
	return err
}
