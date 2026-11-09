package integrations

// 飞书通知 worker。notifications 写入的同一事务里由触发器入队 im_notification_outbox，
// 这里认领到期项（FOR UPDATE SKIP LOCKED + 租约）→ 事务外发送飞书卡片 → 短事务回写。结构仿照 SyncWorker。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/wecom"
	"openfox/internal/tenancy"
)

const (
	notifyPollInterval  = 2 * time.Second
	notifyLeaseSeconds  = 60
	notifyBatchSize     = 20
	notifyScanLimit     = 100
	notifyMaxAttempts   = 6
	notifyBackoffBase   = 30 * time.Second
	notifyBackoffMax    = 30 * time.Minute
	notifyBackoffCap    = 30 * time.Second
	notifyButtonText    = "在 OpenFox 中查看"
	notifyLastErrorSize = 500
)

// FeishuNotifyWorker 周期性扫描有到期通知的组织并发送。单组织失败只记日志，不阻塞其他组织。
type FeishuNotifyWorker struct {
	Pool        SyncRunner
	Logger      *slog.Logger
	Service     Service
	Connections *LongConnManager
	// Instance 写入出站项的 lease_owner；为空时用随机值。
	Instance string
	// Interval 为轮询间隔（默认 2 秒）。
	Interval time.Duration

	cursor string
}

func (w *FeishuNotifyWorker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *FeishuNotifyWorker) instance() string {
	if w.Instance == "" {
		w.Instance = "fsnotify-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *FeishuNotifyWorker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = notifyPollInterval
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
			for i := 1; i < failures && wait < notifyBackoffCap; i++ {
				wait *= 2
			}
			if wait > notifyBackoffCap {
				wait = notifyBackoffCap
			}
			w.logger().Warn("feishu_notify_pass_failed", "error", err.Error(), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮：列出有到期通知的组织并逐个处理（测试的单轮入口）。
func (w *FeishuNotifyWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("integrations: nil feishu notify worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_im_notification_organizations($1::uuid, $2)`, after, notifyScanLimit)
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
			w.logger().Warn("feishu_notify_organization_failed", "organization_id", id, "error", err.Error())
		}
		w.cursor = id
	}
	if len(ids) < notifyScanLimit {
		w.cursor = ""
	}
	return nil
}

func (w *FeishuNotifyWorker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

type notifyItem struct {
	DirectChatID, DirectMessageID *string
	ID                            string
	RecipientUserID               string
	PersonalConnection            string
	AppConnection                 string
	Kind                          string
	Summary                       string
	TaskID                        *string
	ConversationID                *string
	Attempts                      int
}

// notifyTarget 是通过发送前检查的项及其发送所需的值。
type notifyTarget struct {
	item     notifyItem
	openID   string
	appID    string
	provider string
	card     string
}

// RunOrganization 认领一批到期项并发送。
func (w *FeishuNotifyWorker) RunOrganization(ctx context.Context, organizationID string) error {
	var targets []notifyTarget
	secrets := map[string]string{} // 应用连接 ID → App Secret
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		items, err := w.claim(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			target, reason, err := w.prepare(ctx, tx, organizationID, item, secrets)
			if err != nil {
				return err
			}
			if reason != "" {
				if err := finishItem(ctx, tx, item.ID, w.instance(), "SKIPPED", reason, ""); err != nil {
					return err
				}
				continue
			}
			targets = append(targets, target)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	return w.send(ctx, organizationID, targets, secrets)
}

func (w *FeishuNotifyWorker) claim(ctx context.Context, tx pgx.Tx) ([]notifyItem, error) {
	wecomLeaseOwner := ""
	if w.Connections != nil {
		wecomLeaseOwner = w.Connections.instance()
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_notification_outbox o
		   SET status = 'FAILED', last_error = 'delivery_unknown', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		  FROM openfox.integration_connections a
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.app_connection_id = a.id
		   AND a.organization_id = o.organization_id AND a.provider = 'wecom' AND o.status = 'SENDING'
		   AND (o.lease_expires_at IS NULL OR o.lease_expires_at <= clock_timestamp())`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT o.id FROM openfox.im_notification_outbox o
			 JOIN openfox.integration_connections a ON a.organization_id = o.organization_id AND a.id = o.app_connection_id
			 WHERE o.organization_id = openfox_private.current_organization_id()
			   AND ((o.status = 'PENDING' AND o.next_attempt_at <= clock_timestamp())
			        OR (o.status = 'SENDING' AND (o.lease_expires_at IS NULL OR o.lease_expires_at <= clock_timestamp())))
			   AND (a.provider <> 'wecom' OR o.status = 'PENDING')
			   AND (a.provider <> 'wecom' OR EXISTS (
			       SELECT 1 FROM openfox.im_connection_states state
			        WHERE state.organization_id = a.organization_id AND state.connection_id = a.id
			          AND state.lease_owner = $4 AND state.lease_expires_at > clock_timestamp()
			          AND state.state = 'CONNECTED'))
			 ORDER BY o.next_attempt_at, o.created_at
			 LIMIT $2
			 FOR UPDATE OF o SKIP LOCKED)
		UPDATE openfox.im_notification_outbox o
		   SET status = 'SENDING', lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8),
		       updated_at = clock_timestamp()
		  FROM due
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = due.id
		RETURNING o.id::text, o.recipient_user_id::text, o.personal_connection_id::text, o.app_connection_id::text, o.kind,
		          o.safe_summary, o.task_id::text, o.conversation_id::text, o.direct_chat_id::text, o.direct_message_id::text, o.attempts, o.created_at`,
		w.instance(), notifyBatchSize, float64(notifyLeaseSeconds), wecomLeaseOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []notifyItem
	for rows.Next() {
		var item notifyItem
		var created time.Time
		if err := rows.Scan(&item.ID, &item.RecipientUserID, &item.PersonalConnection, &item.AppConnection, &item.Kind,
			&item.Summary, &item.TaskID, &item.ConversationID, &item.DirectChatID, &item.DirectMessageID, &item.Attempts, &created); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// eventForKind 把通知类型映射到通知设置里的事件类型（与前端 eventForKind 一致）。
func eventForKind(kind string) string {
	switch kind {
	case "DIRECT_MESSAGE":
		return "human_chat"
	case "TASK_ASSIGNED", "TRIAGE_NEW":
		return "assign"
	case "TASK_UPDATED":
		return "status"
	case "TASK_REACTION", "TASK_COMMENT", "REPLY":
		return "comment"
	case "MENTION":
		return "mention"
	case "RUN_FAILED", "HANDOFF", "HANDOFF_REVIEW":
		return "agent"
	case "AUTOMATION_RESULT":
		return "automation"
	default:
		return "agent"
	}
}

// prepare 做发送前检查；不满足返回跳过原因，满足返回发送目标（含卡片）。
func (w *FeishuNotifyWorker) prepare(ctx context.Context, tx pgx.Tx, organizationID string, item notifyItem, secrets map[string]string) (notifyTarget, string, error) {
	var personalOK, appOK, member bool
	var openID, appID, provider, staffID string
	err := tx.QueryRow(ctx, `
		SELECT p.status = 'ACTIVE' AND p.parent_connection_id = a.id,
		       a.status = 'ACTIVE' AND a.scope = 'ORGANIZATION' AND a.provider = p.provider
		       AND (a.account_kind = 'app' OR (a.provider = 'wecom' AND a.account_kind IN ('task_assistant', 'agent'))),
		       p.external_id, a.external_id, a.provider, COALESCE(p.settings->>'staff_id', ''),
		       EXISTS (SELECT 1 FROM openfox.organization_members m
		                WHERE m.organization_id = p.organization_id AND m.user_id = $3::uuid AND m.status = 'ACTIVE')
		  FROM openfox.integration_connections p
		  JOIN openfox.integration_connections a ON a.organization_id = p.organization_id AND a.id = $2::uuid
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.id = $1::uuid AND p.owner_user_id = $3::uuid`,
		item.PersonalConnection, item.AppConnection, item.RecipientUserID).Scan(&personalOK, &appOK, &openID, &appID, &provider, &staffID, &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return notifyTarget{}, "account_unbound", nil
	}
	if err != nil {
		return notifyTarget{}, "", err
	}
	switch {
	case !personalOK:
		return notifyTarget{}, "account_unbound", nil
	case !appOK:
		return notifyTarget{}, "app_unavailable", nil
	case !member:
		return notifyTarget{}, "not_a_member", nil
	}
	if provider == ProviderWeCom {
		var selected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM openfox.wecom_notification_targets target
			 WHERE target.organization_id = openfox_private.current_organization_id()
			   AND target.user_id = $1::uuid AND target.personal_connection_id = $2::uuid)`,
			item.RecipientUserID, item.PersonalConnection).Scan(&selected); err != nil {
			return notifyTarget{}, "", err
		}
		if !selected {
			return notifyTarget{}, "target_changed", nil
		}
		allowed, err := canDeliverWeComNotification(ctx, tx, organizationID, item)
		if err != nil {
			return notifyTarget{}, "", err
		}
		if !allowed {
			return notifyTarget{}, "source_unavailable", nil
		}
	}
	var enabled, direct bool
	var disabled []string
	channel := provider
	if provider == ProviderLark || provider == ProviderDingTalk {
		channel = ProviderFeishu
	}
	err = tx.QueryRow(ctx, `SELECT enabled, disabled_events, COALESCE(direct_messages,false) FROM openfox.user_notification_channels WHERE user_id = $1::uuid AND channel = $2`,
		item.RecipientUserID, channel).Scan(&enabled, &disabled, &direct)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		enabled, disabled = provider != ProviderWeCom, nil
	case err != nil:
		return notifyTarget{}, "", err
	}
	if !enabled {
		return notifyTarget{}, "channel_disabled", nil
	}
	if (item.Kind == "DIRECT_MESSAGE" && !direct) || slices.Contains(disabled, eventForKind(item.Kind)) {
		return notifyTarget{}, "event_disabled", nil
	}
	if provider != ProviderWeCom {
		if _, ok := secrets[item.AppConnection]; !ok {
			secret, err := w.Service.For(provider).feishuSecret(ctx, tx, organizationID, item.AppConnection)
			if err != nil {
				return notifyTarget{}, "credentials_unavailable", nil
			}
			secrets[item.AppConnection] = secret
		}
	}
	link, heading, err := w.Service.notificationLink(ctx, tx, organizationID, item)
	if err != nil {
		return notifyTarget{}, "", err
	}
	card := buildNotificationCard(item.Kind, heading, item.Summary, link)
	if provider == ProviderDingTalk {
		// 钉钉：机器人单聊发给员工 userid（个人连接的 settings.staff_id），消息是 markdown。
		openID, card = staffID, buildDingTalkNotification(item.Kind, heading, item.Summary, link)
	}
	if provider == ProviderWeCom {
		card = buildWeComNotification(item.Kind, heading, item.Summary, link)
	}
	return notifyTarget{item: item, openID: openID, appID: appID, provider: provider, card: card}, "", nil
}

func canDeliverWeComNotification(ctx context.Context, tx pgx.Tx, organizationID string, item notifyItem) (bool, error) {
	var role string
	var version int64
	if err := tx.QueryRow(ctx, `
		SELECT role, authorization_version FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = $1::uuid AND status = 'ACTIVE' AND role <> 'GUEST'`,
		item.RecipientUserID).Scan(&role, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	actor := conversations.Actor{UserID: item.RecipientUserID, Role: orgRole(role), AuthorizationVersion: uint64(version)}
	var botSpaceID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(b.space_id::text, agent.space_id::text, '')
		  FROM openfox.im_bots b
		  JOIN openfox.agents agent ON agent.organization_id = b.organization_id AND agent.id = b.agent_id
		 WHERE b.organization_id = openfox_private.current_organization_id()
		   AND b.connection_id = $1::uuid AND b.removed_at IS NULL AND agent.archived_at IS NULL`,
		item.AppConnection).Scan(&botSpaceID)
	if errors.Is(err, pgx.ErrNoRows) || botSpaceID == "" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	canReadBot, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID, actor, botSpaceID)
	if err != nil || !canReadBot {
		return false, err
	}
	if item.TaskID != nil {
		var spaceID string
		err := tx.QueryRow(ctx, `SELECT space_id::text FROM openfox.tasks
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
			*item.TaskID).Scan(&spaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		canRead, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID, actor, spaceID)
		if err != nil || !canRead {
			return false, err
		}
	}
	if item.ConversationID != nil {
		if _, err := (conversations.Service{}).Get(ctx, tx, organizationID, *item.ConversationID, actor); err != nil {
			if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
	}
	if item.DirectChatID != nil {
		var participant bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.direct_chats
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			  AND $2::uuid IN (user_low, user_high))`, *item.DirectChatID, item.RecipientUserID).Scan(&participant); err != nil {
			return false, err
		}
		return participant, nil
	}
	return true, nil
}

// notificationLink 生成卡片按钮的链接：任务 → 任务详情；会话 → 会话；其他或任务已删除 → 收件箱。
// heading 是任务的「编号 · 标题」（与收件箱给收件人看到的内容相同），没有任务时为空。
func (s Service) notificationLink(ctx context.Context, tx pgx.Tx, organizationID string, item notifyItem) (string, string, error) {
	base := strings.TrimRight(s.PublicURL, "/") + "/o/" + url.PathEscape(strings.ToLower(organizationID))
	if item.DirectChatID != nil && item.DirectMessageID != nil {
		return base + "/chat/direct/" + *item.DirectChatID + "?message=" + *item.DirectMessageID, "", nil
	}
	if item.TaskID != nil {
		var spaceID, prefix, title string
		var sequence int64
		err := tx.QueryRow(ctx, `
			SELECT s.id::text, s.prefix, t.sequence, t.title
			  FROM openfox.tasks t
			  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid
			   AND t.deleted_at IS NULL AND s.deleted_at IS NULL`, *item.TaskID).Scan(&spaceID, &prefix, &sequence, &title)
		if err == nil {
			display := prefix + "-" + strconv.FormatInt(sequence, 10)
			if r := []rune(strings.TrimSpace(title)); len(r) > previewTitleLimit {
				title = string(r[:previewTitleLimit]) + "…"
			}
			return base + "/spaces/" + spaceID + "/tasks?task=" + url.QueryEscape(display), display + " · " + strings.TrimSpace(title), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
		return base + "/inbox", "", nil
	}
	if item.ConversationID != nil {
		return base + "/chat/" + *item.ConversationID, "", nil
	}
	return base + "/inbox", "", nil
}

// summaryTexts 把写通知时使用的固定英文摘要换成中文（收件箱按类型自行显示文案，飞书卡片直接展示摘要）。
var summaryTexts = map[string]string{
	"New direct message.":                   "你收到了一条真人对话消息。",
	"A task was assigned to you.":           "你被指派了这个任务。",
	"A new task entered triage.":            "分诊箱里来了新任务。",
	"You were mentioned in a conversation.": "你在对话里被提到了。",
}

func cardSummary(summary string) string {
	if text, ok := summaryTexts[strings.TrimSpace(summary)]; ok {
		return text
	}
	return summary
}

func cardTitle(kind string) string {
	switch kind {
	case "DIRECT_MESSAGE":
		return "真人对话"
	case "TASK_ASSIGNED":
		return "有任务指派给你"
	case "TRIAGE_NEW":
		return "分诊箱有新任务"
	case "TASK_COMMENT", "REPLY":
		return "新评论"
	case "MENTION":
		return "有人提到了你"
	case "RUN_FAILED":
		return "Agent 运行失败"
	case "HANDOFF", "HANDOFF_REVIEW":
		return "Agent 需要你处理"
	case "AUTOMATION_RESULT":
		return "自动化结果"
	default:
		return "OpenFox 通知"
	}
}

var larkMDEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
	"\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#")

// escapeLarkMD 转义 lark_md 里的 HTML 与 markdown 特殊字符，避免通知摘要被渲染成链接或格式。
func escapeLarkMD(text string) string { return larkMDEscaper.Replace(text) }

// buildNotificationCard 生成交互卡片（JSON 1.0 格式）。
func buildNotificationCard(kind, heading, summary, link string) string {
	content := escapeLarkMD(cardSummary(summary))
	if heading != "" {
		content = "**" + escapeLarkMD(heading) + "**\n" + content
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{"template": "blue", "title": map[string]any{"tag": "plain_text", "content": cardTitle(kind)}},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}},
			map[string]any{"tag": "action", "actions": []any{
				map[string]any{"tag": "button", "type": "primary", "url": link,
					"text": map[string]any{"tag": "plain_text", "content": notifyButtonText}},
			}},
		},
	}
	encoded, _ := json.Marshal(card)
	return string(encoded)
}

// finishItem 回写终态（DONE / FAILED / SKIPPED）；只在仍由本实例持有租约时生效。
func finishItem(ctx context.Context, tx pgx.Tx, id, instance, status, note, remoteID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_notification_outbox
		   SET status = $3, last_error = $4, remote_message_id = $5, lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'SENDING' AND lease_owner = $2`, id, instance, status, truncateNote(note), remoteID)
	return err
}

// deferItem 把项放回队列：until 为下次尝试时间；countAttempt 为 false 时不计次数（限流）。
func deferItem(ctx context.Context, tx pgx.Tx, id, instance string, until time.Time, countAttempt bool, note string) error {
	increment := 0
	if countAttempt {
		increment = 1
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_notification_outbox
		   SET status = 'PENDING', attempts = attempts + $4, next_attempt_at = $3, last_error = $5, lease_owner = '', lease_expires_at = NULL,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'SENDING' AND lease_owner = $2`, id, instance, until, increment, truncateNote(note))
	return err
}

func truncateNote(note string) string {
	note = strings.TrimSpace(strings.SplitN(note, "\n", 2)[0])
	if r := []rune(note); len(r) > notifyLastErrorSize {
		return string(r[:notifyLastErrorSize])
	}
	return note
}

func notifyBackoff(attempts int) time.Duration {
	wait := notifyBackoffBase
	for i := 1; i < attempts && wait < notifyBackoffMax; i++ {
		wait *= 2
	}
	if wait > notifyBackoffMax {
		wait = notifyBackoffMax
	}
	return time.Duration(float64(wait) * (0.8 + rand.Float64()*0.4))
}

func (w *FeishuNotifyWorker) send(ctx context.Context, organizationID string, targets []notifyTarget, secrets map[string]string) error {
	instance := w.instance()
	limited := map[string]time.Time{} // 应用连接 → 限流恢复时间
	invalid := map[string]bool{}      // 凭证失效的应用连接
	tokens := map[string]string{}     // 同一个应用在一批里只取一次 token
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item := target.item
		var apply func(ctx context.Context, tx pgx.Tx) error
		revoke := false
		switch until, isLimited := limited[item.AppConnection]; {
		case invalid[item.AppConnection]:
			apply = func(ctx context.Context, tx pgx.Tx) error {
				return finishItem(ctx, tx, item.ID, instance, "FAILED", "credentials_invalid", "")
			}
		case isLimited:
			apply = func(ctx context.Context, tx pgx.Tx) error {
				return deferItem(ctx, tx, item.ID, instance, until, false, "rate_limited")
			}
		default:
			secret := secrets[item.AppConnection]
			var messageID string
			var err error
			if target.provider == ProviderWeCom {
				if w.Connections == nil {
					err = errIMClientMissing
				} else {
					err = w.Connections.SendWeCom(ctx, organizationID, item.AppConnection, target.openID, target.card)
				}
			} else if target.provider == ProviderDingTalk {
				messageID, err = w.sendDingTalk(ctx, tokens, item.AppConnection, target, secret)
			} else if client := w.Service.For(target.provider).Feishu; client == nil {
				err = errIMClientMissing
			} else {
				messageID, err = w.sendCard(ctx, client, tokens, item.AppConnection, target, secret)
			}
			switch {
			case errors.Is(err, errIMClientMissing):
				// 该服务商的客户端未配置：稍后重试，不计次数。
				until := time.Now().Add(notifyBackoff(1))
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return deferItem(ctx, tx, item.ID, instance, until, false, "client_unavailable")
				}
			case err == nil:
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return finishItem(ctx, tx, item.ID, instance, "DONE", "", messageID)
				}
			case errors.Is(err, wecom.ErrDeliveryUnknown), errors.Is(err, wecom.ErrSendRejected):
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return finishItem(ctx, tx, item.ID, instance, "FAILED", sanitizeLastError(err), "")
				}
			case errors.Is(err, wecom.ErrUnavailable):
				until := time.Now().Add(notifyBackoff(1))
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return deferItem(ctx, tx, item.ID, instance, until, false, "connection_unavailable")
				}
			case errors.Is(err, feishu.ErrRateLimited):
				wait := 30 * time.Second
				var rl *feishu.RateLimitedError
				if errors.As(err, &rl) && rl.RetryAfter > 0 {
					wait = rl.RetryAfter
				}
				until := time.Now().Add(wait)
				limited[item.AppConnection] = until
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return deferItem(ctx, tx, item.ID, instance, until, false, "rate_limited")
				}
			case errors.Is(err, feishu.ErrInvalidCredentials):
				invalid[item.AppConnection] = true
				revoke = true
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return finishItem(ctx, tx, item.ID, instance, "FAILED", "credentials_invalid", "")
				}
			case errors.Is(err, feishu.ErrUserUnreachable):
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return finishItem(ctx, tx, item.ID, instance, "FAILED", "user_unreachable", "")
				}
			default:
				note := sanitizeLastError(err)
				attempts := item.Attempts + 1
				if attempts >= notifyMaxAttempts {
					apply = func(ctx context.Context, tx pgx.Tx) error {
						_, e := tx.Exec(ctx, `UPDATE openfox.im_notification_outbox SET attempts = $3 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
							item.ID, instance, attempts)
						if e != nil {
							return e
						}
						return finishItem(ctx, tx, item.ID, instance, "FAILED", note, "")
					}
				} else {
					until := time.Now().Add(notifyBackoff(attempts))
					apply = func(ctx context.Context, tx pgx.Tx) error {
						return deferItem(ctx, tx, item.ID, instance, until, true, note)
					}
				}
			}
		}
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			if revoke {
				if err := markFeishuNeedsAttention(ctx, tx, item.AppConnection, ReasonCredentialsInvalid); err != nil {
					return err
				}
			}
			return apply(ctx, tx)
		})
		if err != nil {
			w.logger().Warn("feishu_notify_writeback_failed", "organization_id", organizationID, "outbox_id", item.ID, "error", err.Error())
		}
	}
	return nil
}

// sendCard 发送一张卡片；tenant_access_token 失效时清除缓存并重试一次。
func (w *FeishuNotifyWorker) sendCard(ctx context.Context, client *feishu.Client, tokens map[string]string, appConnection string,
	target notifyTarget, secret string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, ok := tokens[appConnection]
		if !ok {
			var err error
			if token, err = client.TenantToken(ctx, target.appID, secret); err != nil {
				return "", err
			}
			tokens[appConnection] = token
		}
		messageID, err := client.SendCard(ctx, token, target.openID, target.card, target.item.ID)
		if errors.Is(err, feishu.ErrTokenExpired) {
			client.ClearToken(target.appID, secret)
			delete(tokens, appConnection)
			continue
		}
		return messageID, err
	}
	return "", feishu.ErrUnavailable
}
