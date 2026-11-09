package integrations

// -W：微信入站处理与回发 worker（结构仿照 IMWorker：控制面函数列出组织 → 组织事务里 FOR UPDATE SKIP LOCKED 认领 + 租约 →
// 网络请求都在事务之外 → 短事务回写）。
//  1. 入站：以绑定发起人的身份把微信文本写入该绑定的会话（origin = IM）并触发 Agent 运行；
//  2. 回发：Agent / 系统在绑定会话里的消息经 wechat_reply_outbox 发回微信，带最近一条入站消息的 context_token。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/wechat"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

const (
	wechatWorkerInterval = time.Second
	wechatInboundLease   = 60 * time.Second
	wechatInboundBatch   = 10
	wechatInboundMax     = 5
	wechatOutboxLease    = 90 * time.Second
	wechatOutboxBatch    = 20
	wechatOutboxMax      = 6
	// 单条微信文本的字数上限（推断值，协议文档未写明）；更长的回复按段发送。
	wechatPartRunes = 1800

	wechatInboundRetention = "7 days"
	wechatOutboxRetention  = "30 days"
	wechatQRRetention      = "1 day"
	wechatCleanupBatch     = 500

	replyWeChatForbidden = "你已没有这个 Agent 的使用权限，请联系管理员。"
	replyWeChatMember    = "你在 OpenFox 的组织成员身份已失效，无法继续使用这个 Agent。"
	replyWeChatAgentGone = "这个 Agent 已被删除或归档，无法继续对话。"
)

// WeChatWorker 周期性扫描有待办的组织。单组织失败只记日志，不阻塞其他组织。
type WeChatWorker struct {
	Pool     SyncRunner
	Logger   *slog.Logger
	Instance string
	Service  Service
	Interval time.Duration

	cursor string
}

func (w *WeChatWorker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *WeChatWorker) instance() string {
	if w.Instance == "" {
		w.Instance = "wechatworker-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *WeChatWorker) Run(ctx context.Context) error {
	interval := orDuration(w.Interval, wechatWorkerInterval)
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
			for i := 1; i < failures && wait < imWorkerBackoffCap; i++ {
				wait *= 2
			}
			if wait > imWorkerBackoffCap {
				wait = imWorkerBackoffCap
			}
			w.logger().Warn("wechat_worker_pass_failed", "error", sanitizeLastError(err), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮：列出有待办的组织并逐个处理（测试的单轮入口）。
func (w *WeChatWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("integrations: nil wechat worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_wechat_worker_organizations($1::uuid, $2)`, after, imWorkerScanLimit)
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
			w.logger().Warn("wechat_worker_organization_failed", "organization_id", id, "error", sanitizeLastError(err))
		}
		w.cursor = id
	}
	if len(ids) < imWorkerScanLimit {
		w.cursor = ""
	}
	return nil
}

// RunOrganization 处理一个组织：入站消息 → 回发 → 清理过期流水。
func (w *WeChatWorker) RunOrganization(ctx context.Context, organizationID string) error {
	var errs []error
	for _, step := range []func(context.Context, string) error{w.processInbound, w.sendReplies, w.cleanup} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := step(ctx, organizationID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *WeChatWorker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganizationAs(ctx, organizationID, syncWorkerActorID, 1, fn)
}

func (w *WeChatWorker) inOrganizationAs(ctx context.Context, organizationID, actorID string, authVersion uint64, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: actorID, AuthorizationVersion: authVersion}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// cleanup 删除过期的已完成流水（每次有限批量）。
func (w *WeChatWorker) cleanup(ctx context.Context, organizationID string) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM openfox.wechat_inbound WHERE ctid IN (SELECT ctid FROM openfox.wechat_inbound
			   WHERE organization_id = openfox_private.current_organization_id() AND finished_at < clock_timestamp() - interval '` + wechatInboundRetention + `' LIMIT $1)`,
			`DELETE FROM openfox.wechat_reply_outbox WHERE ctid IN (SELECT ctid FROM openfox.wechat_reply_outbox
			   WHERE organization_id = openfox_private.current_organization_id() AND finished_at < clock_timestamp() - interval '` + wechatOutboxRetention + `' LIMIT $1)`,
		} {
			if _, err := tx.Exec(ctx, q, wechatCleanupBatch); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- 入站 ----------

type wechatInbound struct {
	ID, BindingID, RemoteID, Text, ContextToken string
	Attempts                                    int
}

func (w *WeChatWorker) claimInbound(ctx context.Context, tx pgx.Tx) ([]wechatInbound, error) {
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM openfox.wechat_inbound
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND ((status = 'PENDING' AND next_attempt_at <= clock_timestamp())
			        OR (status = 'PROCESSING' AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY received_at LIMIT $2
			 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.wechat_inbound i
		   SET status = 'PROCESSING', lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8)
		  FROM due
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.id = due.id
		RETURNING i.id::text, i.binding_id::text, i.remote_message_id, i.text, i.context_token, i.attempts, i.received_at`,
		w.instance(), wechatInboundBatch, wechatInboundLease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []wechatInbound
	for rows.Next() {
		var item wechatInbound
		var received time.Time
		if err := rows.Scan(&item.ID, &item.BindingID, &item.RemoteID, &item.Text, &item.ContextToken, &item.Attempts, &received); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (w *WeChatWorker) processInbound(ctx context.Context, organizationID string) error {
	var items []wechatInbound
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = w.claimInbound(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.processOneInbound(ctx, organizationID, item); err != nil {
			w.failInbound(ctx, organizationID, item, err)
		}
	}
	return nil
}

func (w *WeChatWorker) failInbound(ctx context.Context, organizationID string, item wechatInbound, cause error) {
	attempts := item.Attempts + 1
	w.logger().Warn("wechat_inbound_failed", "organization_id", organizationID, "inbound_id", item.ID, "attempts", attempts, "error", sanitizeLastError(cause))
	note := truncateNote(cause.Error())
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if attempts >= wechatInboundMax {
			_, err := tx.Exec(ctx, `
				UPDATE openfox.wechat_inbound SET status = 'FAILED', attempts = $3, last_error = $4, lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`, item.ID, w.instance(), attempts, note)
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_inbound SET status = 'PENDING', attempts = $3, last_error = $4, lease_owner = '', lease_expires_at = NULL,
			       next_attempt_at = clock_timestamp() + make_interval(secs => $5::float8)
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
			item.ID, w.instance(), attempts, note, inboundBackoff(attempts).Seconds())
		return err
	})
	if err != nil && ctx.Err() == nil {
		w.logger().Warn("wechat_inbound_writeback_failed", "inbound_id", item.ID, "error", sanitizeLastError(err))
	}
}

func (w *WeChatWorker) finishInbound(ctx context.Context, tx pgx.Tx, id, status, result, messageID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.wechat_inbound
		   SET status = $3, result = $4, message_id = NULLIF($5, '')::uuid, lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
		id, w.instance(), status, result, messageID)
	return err
}

// rejectInbound 回复提示并把入站项标为 REJECTED。
func (w *WeChatWorker) rejectInbound(ctx context.Context, tx pgx.Tx, item wechatInbound, result, reply string) error {
	if reply != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.wechat_reply_outbox (organization_id, binding_id, body)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2)`, item.BindingID, reply); err != nil {
			return err
		}
	}
	return w.finishInbound(ctx, tx, item.ID, "REJECTED", result, "")
}

type wechatDecision struct {
	done     bool
	agentID  string
	spaceID  string
	member   conversations.Actor
	conv     string
	reject   string // 非空：以 REJECTED 结束，值为 result
	reply    string
	finished string // done 且为 IGNORED 的 result
}

// decide 读取绑定现状并判定能否处理（以 worker 身份）。
func (w *WeChatWorker) decide(ctx context.Context, tx pgx.Tx, organizationID string, item wechatInbound) (wechatDecision, error) {
	var d wechatDecision
	var status, creator, memberStatus, role string
	var archived bool
	var version int64
	var conv, space *string
	err := tx.QueryRow(ctx, `
		SELECT b.status, b.agent_id::text, b.created_by::text, b.conversation_id::text, a.space_id::text, a.archived_at IS NOT NULL,
		       COALESCE(m.status, ''), COALESCE(m.role, ''), COALESCE(m.authorization_version, 0)
		  FROM openfox.wechat_bindings b
		  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
		  LEFT JOIN openfox.organization_members m ON m.organization_id = b.organization_id AND m.user_id = b.created_by
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.id = $1::uuid`, item.BindingID).
		Scan(&status, &d.agentID, &creator, &conv, &space, &archived, &memberStatus, &role, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return wechatDecision{done: true, finished: "binding_missing"}, nil
	}
	if err != nil {
		return d, err
	}
	if status != WeChatActive {
		return wechatDecision{done: true, finished: "binding_unavailable"}, nil
	}
	if conv != nil {
		d.conv = *conv
	}
	if space != nil {
		d.spaceID = *space
	}
	d.member = conversations.Actor{UserID: creator, Role: orgRole(role), AuthorizationVersion: uint64(version)}
	switch {
	case memberStatus != "ACTIVE" || strings.EqualFold(role, "GUEST"):
		d.reject, d.reply = "member_unavailable", replyWeChatMember
	case archived:
		d.reject, d.reply = "agent_unavailable", replyWeChatAgentGone
	case strings.TrimSpace(item.Text) == "":
		d.reject, d.reply = "unsupported", replyUnsupported
	default:
		// 发起人必须仍有这个 Agent 的使用权限（规则与绑定时相同）。
		if err := agentBindCheck(ctx, tx, collaboration.Actor{UserID: creator, Role: orgRole(role), AuthorizationVersion: uint64(version)}, d.agentID); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidRequest) {
				d.reject, d.reply = "forbidden", replyWeChatForbidden
			} else {
				return d, err
			}
		}
	}
	return d, nil
}

func (w *WeChatWorker) processOneInbound(ctx context.Context, organizationID string, item wechatInbound) error {
	var d wechatDecision
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = w.decide(ctx, tx, organizationID, item)
		if err != nil {
			return err
		}
		switch {
		case d.done:
			return w.finishInbound(ctx, tx, item.ID, "IGNORED", d.finished, "")
		case d.reject != "":
			d.done = true
			return w.rejectInbound(ctx, tx, item, d.reject, d.reply)
		}
		return nil
	}); err != nil {
		return err
	}
	if d.done {
		return nil
	}
	return w.inOrganizationAs(ctx, organizationID, d.member.UserID, d.member.AuthorizationVersion, func(ctx context.Context, tx pgx.Tx) error {
		return w.postInbound(ctx, tx, organizationID, item, d)
	})
}

// postInbound 在发起人身份的事务里找到 / 创建绑定的会话并写入消息（触发 Agent 运行）。
func (w *WeChatWorker) postInbound(ctx context.Context, tx pgx.Tx, organizationID string, item wechatInbound, d wechatDecision) error {
	service := conversations.Service{}
	// 锁住绑定行：并发处理同一绑定的多条消息时只有一个创建会话。
	var conversationID string
	var current *string
	if err := tx.QueryRow(ctx, `
		SELECT conversation_id::text FROM openfox.wechat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE' FOR UPDATE`, item.BindingID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return w.finishInbound(ctx, tx, item.ID, "IGNORED", "binding_unavailable", "")
		}
		return err
	}
	if current != nil {
		conversationID = *current
	}
	if conversationID == "" {
		id, err := membership.NewID()
		if err != nil {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := service.Create(ctx, sp, organizationID, d.member, conversations.CreateInput{
			ID: id, SpaceID: d.spaceID, AgentID: d.agentID, Visibility: conversations.VisibilityPrivate, Origin: conversations.OriginIM}); err != nil {
			_ = sp.Rollback(ctx)
			if reject := mapInboundReject(err); reject != nil {
				return w.rejectInbound(ctx, tx, item, reject.result, reject.reply)
			}
			return err
		}
		if _, err := sp.Exec(ctx, `
			UPDATE openfox.conversations SET title = '微信单聊'
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
			_ = sp.Rollback(ctx)
			return err
		}
		if _, err := sp.Exec(ctx, `
			UPDATE openfox.wechat_bindings SET conversation_id = $2::uuid, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, item.BindingID, id); err != nil {
			_ = sp.Rollback(ctx)
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		conversationID = id
	}
	clientID := "wechat:" + item.RemoteID
	var messageID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id() AND conversation_id = $1::uuid AND client_message_id = $2`,
		conversationID, clientID).Scan(&messageID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if messageID == "" {
		id, err := membership.NewID()
		if err != nil {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		message, err := service.PostMessage(ctx, sp, organizationID, conversationID, d.member, conversations.MessageInput{
			ID: id, Body: item.Text, ClientMessageID: clientID, Mentions: []conversations.Mention{{AgentID: d.agentID}}})
		if err != nil {
			_ = sp.Rollback(ctx)
			if reject := mapInboundReject(err); reject != nil {
				return w.rejectInbound(ctx, tx, item, reject.result, reject.reply)
			}
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		messageID = message.ID
	}
	return w.finishInbound(ctx, tx, item.ID, "DONE", "", messageID)
}

// ---------- 回发 ----------

type wechatOutbox struct {
	ID, BindingID, MessageID, Body string
	Attempts, SentParts            int
}

type wechatTarget struct {
	item         wechatOutbox
	baseURL      string
	token        string
	toUserID     string
	contextToken string
	parts        []string
}

func (w *WeChatWorker) claimOutbox(ctx context.Context, tx pgx.Tx) ([]wechatOutbox, error) {
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM openfox.wechat_reply_outbox
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND ((status = 'PENDING' AND next_attempt_at <= clock_timestamp())
			        OR (status = 'SENDING' AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY next_attempt_at, created_at LIMIT $2
			 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.wechat_reply_outbox o
		   SET status = 'SENDING', lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8), updated_at = clock_timestamp()
		  FROM due
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = due.id
		RETURNING o.id::text, o.binding_id::text, COALESCE(o.message_id::text, ''), o.body, o.attempts, o.sent_parts, o.created_at`,
		w.instance(), wechatOutboxBatch, wechatOutboxLease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []wechatOutbox
	for rows.Next() {
		var item wechatOutbox
		var created time.Time
		if err := rows.Scan(&item.ID, &item.BindingID, &item.MessageID, &item.Body, &item.Attempts, &item.SentParts, &created); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (w *WeChatWorker) finishOutbox(ctx context.Context, tx pgx.Tx, id, status, note string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.wechat_reply_outbox
		   SET status = $3, last_error = $4, lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`, id, w.instance(), status, note)
	return err
}

// splitParts 把长文本按字数上限切段，优先在换行处断开。
func splitParts(text string, limit int) []string {
	runes := []rune(strings.TrimSpace(text))
	var parts []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

func (w *WeChatWorker) prepareOutbox(ctx context.Context, tx pgx.Tx, organizationID string, item wechatOutbox) (wechatTarget, string, error) {
	var status string
	var sealed []byte
	var target wechatTarget
	err := tx.QueryRow(ctx, `
		SELECT status, base_url, bot_token_sealed, ilink_user_id, last_context_token FROM openfox.wechat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, item.BindingID).
		Scan(&status, &target.baseURL, &sealed, &target.toUserID, &target.contextToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, "binding_missing", nil
	}
	if err != nil {
		return target, "", err
	}
	if status != WeChatActive {
		return target, "binding_unavailable", nil
	}
	body := item.Body
	if item.MessageID != "" {
		err := tx.QueryRow(ctx, `
			SELECT body FROM openfox.messages
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, item.MessageID).Scan(&body)
		if errors.Is(err, pgx.ErrNoRows) {
			return target, "message_missing", nil
		}
		if err != nil {
			return target, "", err
		}
	}
	if strings.TrimSpace(body) == "" {
		return target, "empty", nil
	}
	plain, err := w.Service.open(sealed, wechatAAD(organizationID, item.BindingID))
	if err != nil {
		return target, "credentials_unavailable", nil
	}
	target.item, target.token, target.parts = item, string(plain), splitParts(body, wechatPartRunes)
	return target, "", nil
}

func (w *WeChatWorker) sendReplies(ctx context.Context, organizationID string) error {
	if w.Service.WeChat == nil {
		return nil
	}
	var targets []wechatTarget
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		items, err := w.claimOutbox(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			target, reason, err := w.prepareOutbox(ctx, tx, organizationID, item)
			if err != nil {
				return err
			}
			if reason != "" {
				if err := w.finishOutbox(ctx, tx, item.ID, "SKIPPED", reason); err != nil {
					return err
				}
				continue
			}
			targets = append(targets, target)
		}
		return nil
	})
	if err != nil || len(targets) == 0 {
		return err
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.deliver(ctx, organizationID, target)
	}
	return nil
}

// deliver 逐段发送；每发出一段就记录进度，重试从未发出的分段继续，client_id = outbox ID + 分段序号，对端按它去重。
func (w *WeChatWorker) deliver(ctx context.Context, organizationID string, target wechatTarget) {
	item := target.item
	sent := item.SentParts
	var sendErr error
	for ; sent < len(target.parts); sent++ {
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sendErr = w.Service.WeChat.SendText(callCtx, target.baseURL, target.token, target.toUserID, target.parts[sent], target.contextToken,
			fmt.Sprintf("%s-%d", item.ID, sent))
		cancel()
		if sendErr != nil {
			break
		}
	}
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		switch {
		case sendErr == nil:
			return w.finishOutbox(ctx, tx, item.ID, "DONE", "")
		case errors.Is(sendErr, wechat.ErrSessionExpired):
			// 微信登录失效：绑定置为 EXPIRED，页面提示重新扫码。
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.wechat_bindings
				   SET status = 'EXPIRED', status_reason = 'session_expired', connected = false, lease_owner = '', lease_expires_at = NULL,
				       updated_at = clock_timestamp(), version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'`, item.BindingID); err != nil {
				return err
			}
			return w.finishOutbox(ctx, tx, item.ID, "FAILED", "session_expired")
		}
		note := wechatErrText(sendErr)
		attempts := item.Attempts + 1
		if attempts >= wechatOutboxMax {
			if _, err := tx.Exec(ctx, `UPDATE openfox.wechat_reply_outbox SET attempts = $3, sent_parts = $4
				WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`, item.ID, w.instance(), attempts, sent); err != nil {
				return err
			}
			return w.finishOutbox(ctx, tx, item.ID, "FAILED", note)
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_reply_outbox
			   SET status = 'PENDING', attempts = $3, sent_parts = $4, last_error = $5, lease_owner = '', lease_expires_at = NULL,
			       next_attempt_at = clock_timestamp() + make_interval(secs => $6::float8), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
			item.ID, w.instance(), attempts, sent, note, notifyBackoff(attempts).Seconds())
		return err
	})
	if err != nil && ctx.Err() == nil {
		w.logger().Warn("wechat_reply_writeback_failed", "organization_id", organizationID, "outbox_id", item.ID, "error", sanitizeLastError(err))
	}
}
