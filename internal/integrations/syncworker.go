package integrations

// 出站 worker（OpenFox → GitHub）与「导入已有 Issue」。结构仿照 internal/imports/worker.go：
// 控制面函数找到有工作的组织，每个组织在组织 worker 身份的事务里认领出站项（FOR UPDATE SKIP LOCKED + 租约），
// 认领后关闭事务，发送放在事务外，再用短事务回写结果。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/tenancy"
)

// SyncRunner 是 worker 需要的最小数据库边界。
type SyncRunner interface {
	RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error
	RunForOrganization(ctx context.Context, organizationID string, source tenancy.Source,
		selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error
}

const (
	syncWorkerActorID    = "00000000-0000-0000-0000-000000000000"
	syncPollInterval     = 2 * time.Second
	syncMaxBackoffWait   = 30 * time.Second
	syncLeaseDuration    = 60 * time.Second
	syncBatchSize        = 20
	syncScanLimit        = 100
	syncMaxClaimRounds   = 10
	commentWaitDelay     = 30 * time.Second
	commentMaxWait       = 10 * time.Minute
	backfillPagesPerPass = 2
	backfillMaxPages     = 50
	backfillMaxAttempts  = 3
	backfillLinksPerPass = 10
	rateLimitFallback    = time.Minute
)

// SyncWorker 周期性扫描有出站项或正在导入已有 Issue 的组织。单组织失败只记日志，不阻塞其他组织。
type SyncWorker struct {
	Pool    SyncRunner
	Logger  *slog.Logger
	Service Service
	// Instance 写入出站项的 lease_owner；为空时用随机值。
	Instance string
	// Interval 为轮询间隔（默认 2 秒）。
	Interval time.Duration

	cursor string
}

func (w *SyncWorker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *SyncWorker) instance() string {
	if w.Instance == "" {
		w.Instance = "sync-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *SyncWorker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = syncPollInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			failures++
			wait := interval
			for i := 1; i < failures && wait < syncMaxBackoffWait; i++ {
				wait *= 2
			}
			if wait > syncMaxBackoffWait {
				wait = syncMaxBackoffWait
			}
			w.logger().Warn("issue_sync_pass_failed", "error", err.Error(), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮：列出有工作的组织并逐个处理（测试的单轮入口）。
func (w *SyncWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("integrations: nil sync worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_integration_sync_organizations($1::uuid, $2)`, after, syncScanLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.RunOrganization(ctx, id); err != nil {
			w.logger().Warn("issue_sync_organization_failed", "organization_id", id, "error", err.Error())
		}
		w.cursor = id
	}
	if len(ids) < syncScanLimit {
		w.cursor = ""
	}
	return nil
}

func (w *SyncWorker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

// RunOrganization 依次处理出站队列与导入已有 Issue；两个阶段互不影响，返回第一个错误。
func (w *SyncWorker) RunOrganization(ctx context.Context, organizationID string) error {
	var first error
	if err := w.processOutbox(ctx, organizationID); err != nil {
		w.logger().Warn("issue_sync_outbox_failed", "organization_id", organizationID, "error", err.Error())
		first = err
	}
	if err := w.processBackfill(ctx, organizationID); err != nil {
		w.logger().Warn("issue_sync_backfill_failed", "organization_id", organizationID, "error", err.Error())
		if first == nil {
			first = err
		}
	}
	return first
}

// ---------- 出站队列 ----------

type outboxItem struct {
	ID          string
	SyncLinkID  string
	TaskID      string
	CommentID   string
	Kind        string
	ActorUserID string
	Attempts    int
	Reclaimed   bool // 认领时状态是 SENDING（上一次租约过期），发送结果未知
	CreatedAt   time.Time
	ClaimedAt   time.Time
}

// sendResult 是一项的处理结果。commit 与结果回写在同一个短事务里执行。
type sendResult struct {
	kind   string // done / skipped / defer / fail / fatal
	note   string
	until  time.Time
	revoke string // 令牌失效的连接 ID
	commit func(ctx context.Context, tx pgx.Tx) error
}

func done(commit func(context.Context, pgx.Tx) error) sendResult {
	return sendResult{kind: "done", commit: commit}
}
func skipped(note string) sendResult { return sendResult{kind: "skipped", note: note} }
func failed(note string) sendResult  { return sendResult{kind: "fail", note: note} }
func fatal(note string) sendResult   { return sendResult{kind: "fatal", note: note} }

// classify 把 GitHub 客户端错误转成处理结果。connectionID 是本次使用的令牌所属连接。
func classify(err error, connectionID string) sendResult {
	var limited *gitapi.ErrRateLimited
	switch {
	case errors.As(err, &limited):
		until := limited.ResetAt
		if until.IsZero() || until.Before(time.Now()) {
			until = time.Now().Add(rateLimitFallback)
		}
		return sendResult{kind: "defer", note: "服务商限流，等待恢复", until: until}
	case errors.Is(err, gitapi.ErrTokenRevoked):
		return sendResult{kind: "fatal", note: "授权已失效，请重新授权后重试", revoke: connectionID}
	case errors.Is(err, gitapi.ErrNotFound):
		return failed("服务商上找不到该仓库或 Issue，或令牌无权访问")
	case errors.Is(err, gitapi.ErrUnknownResult):
		return failed("服务商没有返回明确结果，下次重试前会先对账")
	}
	return failed("服务商请求失败：" + FirstLine(err.Error(), 200))
}

func (w *SyncWorker) processOutbox(ctx context.Context, organizationID string) error {
	for round := 0; round < syncMaxClaimRounds; round++ {
		var items []outboxItem
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			items, err = w.claim(ctx, tx)
			return err
		})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			result := w.process(ctx, organizationID, item)
			if err := w.finish(ctx, organizationID, item, result); err != nil {
				// 落库失败（例如 commit 出错）：按失败重试，下次处理前对账。
				w.logger().Warn("issue_sync_finish_failed", "organization_id", organizationID, "outbox_id", item.ID, "error", err.Error())
				if err2 := w.finish(ctx, organizationID, item, failed("结果落库失败："+FirstLine(err.Error(), 200))); err2 != nil {
					return err2
				}
			}
		}
	}
	return nil
}

func (w *SyncWorker) claim(ctx context.Context, tx pgx.Tx) ([]outboxItem, error) {
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id, status AS prev_status FROM openfox.integration_sync_outbox
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND ((status = 'PENDING' AND next_attempt_at <= clock_timestamp())
			        OR (status = 'SENDING' AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.integration_sync_outbox o
		   SET status = 'SENDING', lease_owner = $2, lease_expires_at = clock_timestamp() + make_interval(secs => $3), updated_at = clock_timestamp()
		  FROM due
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = due.id
		RETURNING o.id::text, o.sync_link_id::text, o.task_id::text, COALESCE(o.comment_id::text, ''), o.kind,
		          COALESCE(o.actor_user_id::text, ''), o.attempts, due.prev_status = 'SENDING', o.created_at, o.updated_at`,
		syncBatchSize, w.instance(), syncLeaseDuration.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []outboxItem
	for rows.Next() {
		var item outboxItem
		if err := rows.Scan(&item.ID, &item.SyncLinkID, &item.TaskID, &item.CommentID, &item.Kind, &item.ActorUserID, &item.Attempts,
			&item.Reclaimed, &item.CreatedAt, &item.ClaimedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// RETURNING 不保证顺序，按创建时间排序，保证同一任务先建后改。
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].CreatedAt.Before(items[j-1].CreatedAt); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	return items, nil
}

// finish 用短事务回写结果；租约已丢失（行不再是自己持有的 SENDING）时不写。
func (w *SyncWorker) finish(ctx context.Context, organizationID string, item outboxItem, res sendResult) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		owner := w.instance()
		if res.kind == "fatal" && res.revoke != "" {
			if err := (Service{}).MarkTokenRevoked(ctx, tx, res.revoke); err != nil {
				return err
			}
		}
		if res.commit != nil {
			if err := res.commit(ctx, tx); err != nil {
				return err
			}
		}
		note := FirstLine(res.note, 900)
		var query string
		var args []any
		switch res.kind {
		case "done":
			// 发送期间任务又变了（触发器把变更合并进了这条 SENDING 的项，updated_at 被改动）：改回 PENDING 再处理一次。
			query = `
				UPDATE openfox.integration_sync_outbox
				   SET status = CASE WHEN updated_at = $3 THEN 'DONE' ELSE 'PENDING' END,
				       finished_at = CASE WHEN updated_at = $3 THEN clock_timestamp() ELSE NULL END,
				       next_attempt_at = CASE WHEN updated_at = $3 THEN next_attempt_at ELSE clock_timestamp() END,
				       last_error = '', lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`
			args = []any{item.ID, owner, item.ClaimedAt}
		case "skipped":
			query = `
				UPDATE openfox.integration_sync_outbox
				   SET status = 'SKIPPED', finished_at = clock_timestamp(), last_error = $3, lease_owner = '', lease_expires_at = NULL,
				       updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`
			args = []any{item.ID, owner, note}
		case "defer":
			// 不计入失败次数：评论等 Issue 创建、限流等恢复。
			query = `
				UPDATE openfox.integration_sync_outbox
				   SET status = 'PENDING', next_attempt_at = $4, last_error = $3, lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`
			args = []any{item.ID, owner, note, res.until}
		case "fatal":
			query = `
				UPDATE openfox.integration_sync_outbox
				   SET status = 'FAILED', finished_at = clock_timestamp(), last_error = $3, lease_owner = '', lease_expires_at = NULL,
				       updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`
			args = []any{item.ID, owner, note}
		default: // fail
			next := time.Now().Add(OutboxBackoff(item.Attempts+1, rand.Float64()*2-1))
			query = `
				UPDATE openfox.integration_sync_outbox
				   SET attempts = attempts + 1,
				       status = CASE WHEN attempts + 1 >= $5 THEN 'FAILED' ELSE 'PENDING' END,
				       finished_at = CASE WHEN attempts + 1 >= $5 THEN clock_timestamp() ELSE NULL END,
				       next_attempt_at = $4, last_error = $3, lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`
			args = []any{item.ID, owner, note, next, maxOutboxAttempts}
		}
		_, err := tx.Exec(ctx, query, args...)
		return err
	})
}

// sendData 是处理一项所需的数据（短事务里一次读出，令牌解密后只在内存里使用）。
type sendData struct {
	linkFound   bool
	linkActive  bool // 未删除、ACTIVE、BIDIRECTIONAL
	linkStatus  string
	createdBy   string
	spaceID     string
	repoFull    string
	connID      string
	connStatus  string
	orgToken    string
	provider    string
	client      gitapi.Client
	issueRef    string // task_issue_links.issue_ref：API 引用该 Issue 的标识，空表示 issueNumber
	taskFound   bool
	title       string
	description string
	category    string
	displayID   string
	assigneeLog string // 第一位人员处理人绑定的个人 GitHub 账号 login
	issue       issueLinkRow
	issueNumber int
	hasIssue    bool
	hasCreate   bool // 该任务还有未完成（PENDING / SENDING / FAILED）的 ISSUE_CREATE
	// 操作者的个人令牌（ACTIVE 的个人连接）。
	actorToken  string
	actorConnID string
	actorLogin  string
	// 评论
	commentFound  bool
	commentBody   string
	commentLinked bool
	authorName    string
	authorToken   string
	authorConnID  string
}

func (w *SyncWorker) load(ctx context.Context, organizationID string, item outboxItem) (sendData, error) {
	var d sendData
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var deleted bool
		var direction, sealed, settingsRaw = "", []byte(nil), []byte(nil)
		err := tx.QueryRow(ctx, `
			SELECT l.status, l.direction, l.deleted_at IS NOT NULL, l.created_by::text, l.space_id::text, l.external_project_name,
			       c.id::text, c.status, c.credentials_sealed, c.provider, c.settings
			  FROM openfox.integration_sync_links l
			  JOIN openfox.integration_connections c ON c.organization_id = l.organization_id AND c.id = l.connection_id
			 WHERE l.organization_id = openfox_private.current_organization_id() AND l.id = $1::uuid`, item.SyncLinkID).
			Scan(&d.linkStatus, &direction, &deleted, &d.createdBy, &d.spaceID, &d.repoFull, &d.connID, &d.connStatus, &sealed, &d.provider, &settingsRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		d.linkFound = true
		d.linkActive = !deleted && d.linkStatus == "ACTIVE" && direction == DirectionBidirectional
		if d.connStatus == StatusActive && len(sealed) > 0 {
			creds, err := w.Service.openCredentials(organizationID, d.connID, sealed)
			if err != nil {
				return err
			}
			d.orgToken = creds.AccessToken
			inst, _, err := w.Service.instanceFrom(organizationID, d.connID, settingsRaw, sealed)
			if err != nil {
				return err
			}
			if d.client, err = w.Service.gitClient(d.provider, inst); err != nil {
				return err
			}
		}
		parent := w.Service.ForGit(d.provider).personalParentArg(d.connID)
		var assigneeID string
		var taskDeleted bool
		err = tx.QueryRow(ctx, `
			SELECT t.title, t.description, ts.category, COALESCE(t.assignee_id::text, ''), t.deleted_at IS NOT NULL, sp.prefix || '-' || t.sequence::text
			  FROM openfox.tasks t
			  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
			  JOIN openfox.spaces sp ON sp.organization_id = t.organization_id AND sp.id = t.space_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid`, item.TaskID).
			Scan(&d.title, &d.description, &d.category, &assigneeID, &taskDeleted, &d.displayID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		d.taskFound = err == nil && !taskDeleted
		err = tx.QueryRow(ctx, `
			SELECT id::text, task_id::text, space_id::text, last_hash, state, issue_number, issue_ref FROM openfox.task_issue_links
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid AND unlinked_at IS NULL`, item.TaskID).
			Scan(&d.issue.ID, &d.issue.TaskID, &d.issue.SpaceID, &d.issue.LastHash, &d.issue.State, &d.issueNumber, &d.issueRef)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		d.hasIssue = err == nil
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.integration_sync_outbox
			                WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
			                  AND kind = 'ISSUE_CREATE' AND status IN ('PENDING', 'SENDING', 'FAILED'))`, item.TaskID).Scan(&d.hasCreate); err != nil {
			return err
		}
		if assigneeID != "" {
			var login string
			err := tx.QueryRow(ctx, gitQuery(`
				SELECT COALESCE(settings->>'login', '') FROM openfox.integration_connections
				 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
				   AND owner_user_id = $1::uuid AND status <> 'DISCONNECTED' AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid)
				 ORDER BY created_at LIMIT 1`, d.provider), assigneeID, parent).Scan(&login)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			d.assigneeLog = login
		}
		personal := func(userID string) (token, connID, login string, err error) {
			if userID == "" {
				return "", "", "", nil
			}
			var blob []byte
			err = tx.QueryRow(ctx, gitQuery(`
				SELECT id::text, COALESCE(settings->>'login', ''), credentials_sealed FROM openfox.integration_connections
				 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
				   AND owner_user_id = $1::uuid AND status = 'ACTIVE' AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid)
				 ORDER BY created_at LIMIT 1`, d.provider), userID, parent).Scan(&connID, &login, &blob)
			if errors.Is(err, pgx.ErrNoRows) {
				return "", "", "", nil
			}
			if err != nil {
				return "", "", "", err
			}
			creds, err := w.Service.openCredentials(organizationID, connID, blob)
			if err != nil {
				return "", "", "", err
			}
			return creds.AccessToken, connID, login, nil
		}
		if d.actorToken, d.actorConnID, d.actorLogin, err = personal(item.ActorUserID); err != nil {
			return err
		}
		if item.Kind != "COMMENT_CREATE" {
			return nil
		}
		var authorUser, authorAgent, email, agentName string
		err = tx.QueryRow(ctx, `
			SELECT c.body, COALESCE(c.author_user_id::text, ''), COALESCE(c.author_agent_id::text, ''), COALESCE(u.email, ''), COALESCE(a.name, '')
			  FROM openfox.task_comments c
			  LEFT JOIN openfox.users u ON u.id = c.author_user_id
			  LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`, item.CommentID).
			Scan(&d.commentBody, &authorUser, &authorAgent, &email, &agentName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		d.commentFound = err == nil
		d.authorName = email
		if authorAgent != "" {
			d.authorName = agentName
		}
		if d.authorName == "" {
			d.authorName = "OpenFox"
		}
		if d.authorToken, d.authorConnID, _, err = personal(authorUser); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.task_comment_links
			                WHERE organization_id = openfox_private.current_organization_id() AND comment_id = $1::uuid)`, item.CommentID).Scan(&d.commentLinked)
	})
	return d, err
}

func (w *SyncWorker) process(ctx context.Context, organizationID string, item outboxItem) sendResult {
	d, err := w.load(ctx, organizationID, item)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return fatal("服务端未配置该服务商")
		}
		return failed("读取同步数据失败：" + FirstLine(err.Error(), 200))
	}
	if !d.linkFound || !d.linkActive {
		return skipped("同步已暂停、已移除或不再是双向同步")
	}
	if !d.taskFound {
		return skipped("任务已删除")
	}
	switch item.Kind {
	case "ISSUE_CREATE":
		return w.sendCreate(ctx, organizationID, item, d)
	case "ISSUE_UPDATE":
		return w.sendUpdate(ctx, item, d)
	case "COMMENT_CREATE":
		return w.sendComment(ctx, item, d)
	}
	return skipped("未知的出站类型")
}

func splitRepo(full string) (owner, repo string) {
	owner, repo, _ = strings.Cut(full, "/")
	return owner, repo
}

// pickToken 选择发送使用的令牌：操作者有 ACTIVE 的个人连接时用个人令牌（Issue 归属到这个人），否则用组织连接令牌。
func (d sendData) pickToken() (token, connID string, ok bool) {
	if d.actorToken != "" {
		return d.actorToken, d.actorConnID, true
	}
	if d.orgToken != "" {
		return d.orgToken, d.connID, true
	}
	return "", "", false
}

// api 返回 Issue 在 API 里的引用标识（预留给 Issue 编号不是纯数字的服务商，GitHub / GitLab 为空）。
func (d sendData) api() string {
	if d.issueRef != "" {
		return d.issueRef
	}
	return strconv.Itoa(d.issueNumber)
}

func (d sendData) assignees() []string {
	if d.assigneeLog == "" {
		return nil
	}
	return []string{d.assigneeLog}
}

func (d sendData) contentHash() string {
	return IssueContentHash(d.title, d.description, LinkStateForCategory(d.category), d.assignees())
}

func (w *SyncWorker) sendCreate(ctx context.Context, organizationID string, item outboxItem, d sendData) sendResult {
	if d.hasIssue {
		return skipped("任务已关联 Issue")
	}
	token, connID, ok := d.pickToken()
	if !ok {
		return failed("连接需要重新授权")
	}
	owner, repo := splitRepo(d.repoFull)
	marker := TaskMarker(item.TaskID)
	var issue gitapi.Issue
	found := false
	// 上一次可能已经建成 Issue 但没拿到结果：先按标记对账，找到就直接关联，不再新建。
	if item.Attempts > 0 || item.Reclaimed {
		creator := d.actorLogin
		if token != d.actorToken || creator == "" {
			user, err := d.client.User(ctx, token)
			if err != nil {
				return classify(err, connID)
			}
			creator = user.Login
		}
		existing, err := d.client.ListIssuesByCreator(ctx, token, owner, repo, creator, item.CreatedAt.Add(-time.Minute))
		if err != nil {
			return classify(err, connID)
		}
		for _, candidate := range existing {
			if strings.Contains(candidate.Body, marker) {
				issue, found = candidate, true
				break
			}
		}
	}
	if !found {
		created, err := d.client.CreateIssue(ctx, token, owner, repo, d.title, IssueBodyWithMarker(d.description, item.TaskID), d.assignees())
		if err != nil {
			return classify(err, connID)
		}
		issue = created
	}
	sentHash := d.contentHash()
	return done(func(ctx context.Context, tx pgx.Tx) error {
		var stillLinked bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.integration_sync_links WHERE organization_id = openfox_private.current_organization_id()
			                 AND id = $1::uuid AND deleted_at IS NULL)`, item.SyncLinkID).Scan(&stillLinked); err != nil {
			return err
		}
		if !stillLinked {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_issue_links (organization_id, sync_link_id, space_id, task_id, repository, issue_number, issue_id, url, state,
			       last_hash, remote_updated_at, issue_ref)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, 'OPEN', $8, $9, $10)
			ON CONFLICT DO NOTHING`, item.SyncLinkID, d.spaceID, item.TaskID, d.repoFull, issue.Number, strconv.FormatInt(issue.ID, 10),
			issue.HTMLURL, sentHash, nullTime(issue.UpdatedAt), issue.Ref)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			actorID := item.ActorUserID
			if actorID == "" {
				actorID = d.createdBy
			}
			if err := collaboration.GitAddActivity(ctx, tx, d.spaceID, item.TaskID, actorID, d.provider+"-out-"+item.ID, "task.issue_linked",
				map[string]any{"repository": d.repoFull, "number": issue.Number, "url": issue.HTMLURL}); err != nil {
				return err
			}
		}
		if err := touchSyncLink(ctx, tx, item.SyncLinkID); err != nil {
			return err
		}
		// 发送期间任务又改了（此时还没有关联，触发器不会入队）：补一条更新，紧接着发出。
		var current IssueContent
		if err := tx.QueryRow(ctx, `
			SELECT t.title, t.description, ts.category, COALESCE(t.assignee_id::text, '')
			  FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL`, item.TaskID).
			Scan(&current.Title, &current.Description, &current.Category, &current.AssigneeID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		login := ""
		if current.AssigneeID != "" {
			if err := tx.QueryRow(ctx, gitQuery(`
				SELECT COALESCE((SELECT settings->>'login' FROM openfox.integration_connections
				                  WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
				                    AND owner_user_id = $1::uuid AND status <> 'DISCONNECTED'
				                    AND ($2::uuid IS NULL OR parent_connection_id = $2::uuid) ORDER BY created_at LIMIT 1), '')`, d.provider),
				current.AssigneeID, d.parentArg()).Scan(&login); err != nil {
				return err
			}
		}
		var logins []string
		if login != "" {
			logins = []string{login}
		}
		if IssueContentHash(current.Title, current.Description, LinkStateForCategory(current.Category), logins) == sentHash {
			return nil
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.integration_sync_outbox (organization_id, sync_link_id, task_id, kind, actor_user_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'ISSUE_UPDATE', NULLIF($3, '')::uuid)
			ON CONFLICT DO NOTHING`, item.SyncLinkID, item.TaskID, item.ActorUserID)
		return err
	})
}

// IssueContent 是任务的当前内容（用来判断发送期间是否又有变化）。
type IssueContent struct {
	Title, Description, Category, AssigneeID string
}

func (w *SyncWorker) sendUpdate(ctx context.Context, item outboxItem, d sendData) sendResult {
	if !d.hasIssue {
		return skipped("任务还没有关联 Issue（创建项会发送当前内容）")
	}
	hash := d.contentHash()
	if hash == d.issue.LastHash {
		return skipped("内容没有变化")
	}
	token, connID, ok := d.pickToken()
	if !ok {
		return failed("连接需要重新授权")
	}
	owner, repo := splitRepo(d.repoFull)
	state, reason := IssueStateForCategory(d.category)
	if state == "open" && d.issue.State == issueClosed {
		reason = "reopened"
	}
	updated, err := d.client.UpdateIssue(ctx, token, owner, repo, d.api(), gitapi.IssueUpdate{
		Title: d.title, Body: IssueBodyWithMarker(d.description, item.TaskID), State: state, StateReason: reason, Assignees: d.assignees()})
	if err != nil {
		return classify(err, connID)
	}
	return done(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_issue_links SET last_hash = $2, state = $3, remote_updated_at = $4, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND unlinked_at IS NULL`,
			d.issue.ID, hash, LinkStateForCategory(d.category), nullTime(updated.UpdatedAt)); err != nil {
			return err
		}
		return touchSyncLink(ctx, tx, item.SyncLinkID)
	})
}

func (w *SyncWorker) sendComment(ctx context.Context, item outboxItem, d sendData) sendResult {
	if !d.commentFound {
		return skipped("评论已不存在")
	}
	if d.commentLinked {
		return skipped("评论已同步")
	}
	if !d.hasIssue {
		if !d.hasCreate {
			return skipped("任务没有关联 Issue")
		}
		if time.Since(item.CreatedAt) > commentMaxWait {
			return fatal("等待 Issue 创建超过 10 分钟")
		}
		return sendResult{kind: "defer", note: "等待 Issue 创建", until: time.Now().Add(commentWaitDelay)}
	}
	token, connID, personal := d.authorToken, d.authorConnID, d.authorToken != ""
	if !personal {
		if d.orgToken == "" {
			return failed("连接需要重新授权")
		}
		token, connID = d.orgToken, d.connID
	}
	owner, repo := splitRepo(d.repoFull)
	remoteID, err := d.client.CommentIssue(ctx, token, owner, repo, d.api(),
		outboundCommentBody(personal, d.authorName, d.commentBody, item.CommentID))
	if err != nil {
		return classify(err, connID)
	}
	return done(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_comment_links (organization_id, issue_link_id, comment_id, remote_comment_id, direction)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'OUTBOUND') ON CONFLICT DO NOTHING`,
			d.issue.ID, item.CommentID, remoteID); err != nil {
			return err
		}
		return touchSyncLink(ctx, tx, item.SyncLinkID)
	})
}

// ---------- 导入已有 Issue ----------

type backfillJob struct {
	LinkID, SpaceID, Repo, CreatedBy, ConnID, ConnStatus string
	Provider                                             string
	Client                                               gitapi.Client
	Token                                                string
	Cursor, Imported, Attempts                           int
	RetryAt                                              time.Time
}

func (w *SyncWorker) processBackfill(ctx context.Context, organizationID string) error {
	var jobs []backfillJob
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT l.id::text, l.space_id::text, l.external_project_name, l.created_by::text, c.id::text, c.status, c.credentials_sealed, c.provider, c.settings,
			       COALESCE((l.settings #>> '{backfill,cursor}')::integer, 0), COALESCE((l.settings #>> '{backfill,imported}')::integer, 0),
			       COALESCE((l.settings #>> '{backfill,attempts}')::integer, 0), COALESCE(l.settings #>> '{backfill,retry_at}', '')
			  FROM openfox.integration_sync_links l
			  JOIN openfox.integration_connections c ON c.organization_id = l.organization_id AND c.id = l.connection_id
			 WHERE l.organization_id = openfox_private.current_organization_id() AND l.deleted_at IS NULL AND l.status = 'ACTIVE'
			   AND l.settings #>> '{backfill,status}' = 'running'
			 ORDER BY l.created_at, l.id LIMIT $1`, backfillLinksPerPass)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job backfillJob
			var sealed, settingsRaw []byte
			var retryAt string
			if err := rows.Scan(&job.LinkID, &job.SpaceID, &job.Repo, &job.CreatedBy, &job.ConnID, &job.ConnStatus, &sealed, &job.Provider, &settingsRaw,
				&job.Cursor, &job.Imported, &job.Attempts, &retryAt); err != nil {
				return err
			}
			if retryAt != "" {
				job.RetryAt, _ = time.Parse(time.RFC3339, retryAt)
			}
			if job.ConnStatus == StatusActive && len(sealed) > 0 {
				creds, err := w.Service.openCredentials(organizationID, job.ConnID, sealed)
				if err != nil {
					return err
				}
				job.Token = creds.AccessToken
				inst, _, err := w.Service.instanceFrom(organizationID, job.ConnID, settingsRaw, sealed)
				if err != nil {
					return err
				}
				if job.Client, err = w.Service.gitClient(job.Provider, inst); err != nil {
					return err
				}
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !job.RetryAt.IsZero() && job.RetryAt.After(time.Now()) {
			continue
		}
		if err := w.backfillLink(ctx, organizationID, job); err != nil {
			w.logger().Warn("issue_backfill_failed", "organization_id", organizationID, "sync_link_id", job.LinkID, "error", err.Error())
		}
	}
	return nil
}

// saveBackfill 写回导入进度（只改 settings.backfill）。
func (w *SyncWorker) saveBackfill(ctx context.Context, tx pgx.Tx, linkID string, state map[string]any) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_links SET settings = jsonb_set(settings, '{backfill}', $2::jsonb), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, linkID, mustJSON(state))
	return err
}

func (w *SyncWorker) backfillLink(ctx context.Context, organizationID string, job backfillJob) error {
	state := func(status, errText string) map[string]any {
		return map[string]any{"status": status, "imported": job.Imported, "error": errText, "cursor": job.Cursor, "attempts": job.Attempts}
	}
	persist := func(m map[string]any) error {
		return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			return w.saveBackfill(ctx, tx, job.LinkID, m)
		})
	}
	if job.Token == "" || job.Client == nil {
		return persist(state("failed", "连接需要重新授权后再导入"))
	}
	owner, repo := splitRepo(job.Repo)
	for pass := 0; pass < backfillPagesPerPass; pass++ {
		page := job.Cursor + 1
		issues, more, err := job.Client.ListIssues(ctx, job.Token, owner, repo, page)
		if err != nil {
			var limited *gitapi.ErrRateLimited
			switch {
			case errors.As(err, &limited):
				until := limited.ResetAt
				if until.IsZero() || until.Before(time.Now()) {
					until = time.Now().Add(rateLimitFallback)
				}
				m := state("running", "")
				m["retry_at"] = until.UTC().Format(time.RFC3339)
				return persist(m)
			case errors.Is(err, gitapi.ErrTokenRevoked):
				if markErr := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
					return (Service{}).MarkTokenRevoked(ctx, tx, job.ConnID)
				}); markErr != nil {
					return markErr
				}
				return persist(state("failed", "授权已失效，请重新授权后再导入"))
			}
			job.Attempts++
			if job.Attempts >= backfillMaxAttempts {
				return persist(state("failed", "读取 Issue 失败："+FirstLine(err.Error(), 200)))
			}
			return persist(state("running", ""))
		}
		prevImported, prevCursor := job.Imported, job.Cursor
		err = w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			imported, err := w.importPage(ctx, tx, organizationID, job, issues)
			if err != nil {
				return err
			}
			job.Imported = prevImported + imported
			job.Cursor = page
			job.Attempts = 0
			status := "running"
			if !more || page >= backfillMaxPages {
				status = "done"
			}
			if imported > 0 {
				if err := touchSyncLink(ctx, tx, job.LinkID); err != nil {
					return err
				}
			}
			return w.saveBackfill(ctx, tx, job.LinkID, state(status, ""))
		})
		if err != nil {
			job.Imported, job.Cursor = prevImported, prevCursor
			job.Attempts++
			if job.Attempts >= backfillMaxAttempts {
				return persist(state("failed", "导入失败："+FirstLine(err.Error(), 200)))
			}
			if perr := persist(state("running", "")); perr != nil {
				return perr
			}
			return err
		}
		if !more || page >= backfillMaxPages {
			return nil
		}
	}
	return nil
}

// importPage 在一个事务里导入一页：跳过已关联的 Issue 和带 OpenFox 标记的 Issue；单个 Issue 失败只回滚它自己（保存点）。
func (w *SyncWorker) importPage(ctx context.Context, tx pgx.Tx, organizationID string, job backfillJob, issues []gitapi.Issue) (int, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.sync_source', 'github', true)`); err != nil {
		return 0, err
	}
	svc := w.Service.ForGit(job.Provider)
	actor, err := loadSyncActor(ctx, tx, job.CreatedBy)
	if err != nil {
		return 0, err
	}
	link := syncLinkRow{ConnectionID: job.ConnID, ID: job.LinkID, SpaceID: job.SpaceID, Direction: DirectionInbound, Status: "ACTIVE"}
	imported := 0
	for _, raw := range issues {
		if _, marked := ParseTaskMarker(raw.Body); marked {
			continue
		}
		if _, found, err := findIssueLink(ctx, tx, job.LinkID, raw.Number); err != nil {
			return imported, err
		} else if found {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return imported, err
		}
		_, _, _, err = svc.createTaskFromIssue(ctx, sp, organizationID, link, actor, job.Repo, fmt.Sprintf("gh-bf-%s-%d", job.LinkID, raw.Number), issueFromREST(raw))
		if err != nil {
			_ = sp.Rollback(ctx)
			w.logger().Warn("issue_backfill_item_skipped", "sync_link_id", job.LinkID, "number", raw.Number, "error", err.Error())
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}
