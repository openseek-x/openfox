package integrations

// IM worker。三件事，结构仿照 FeishuNotifyWorker（控制面函数列出组织 → 组织事务里 FOR UPDATE SKIP LOCKED 认领 + 租约 →
// 网络请求都在事务之外 → 短事务回写）：
//  1. 轮询扫码创建应用的会话，成功后建立组织级飞书连接、机器人（任务助手同时建内置 Agent）；
//  2. 处理机器人收到的飞书消息：认人、权限预检、读群上下文，以发送人身份写入会话并触发 Agent 运行；
//  3. 把 Agent / 系统在已关联飞书会话里的消息发回飞书。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/wecom"
	"openfox/internal/membership"
	"openfox/internal/platform/requestid"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	imWorkerPollInterval = 2 * time.Second
	imWorkerBackoffCap   = 30 * time.Second
	imWorkerScanLimit    = 100

	imRegLease      = 30 * time.Second
	imRegBatch      = 5
	imInboundLease  = 60 * time.Second
	imInboundBatch  = 10
	imInboundMax    = 5
	imInboundBase   = 30 * time.Second
	imInboundCap    = 10 * time.Minute
	imOutboxLease   = 60 * time.Second
	imOutboxBatch   = 20
	imOutboxMax     = 6
	imUnboundWindow = "10 minutes"

	replyUnsupported = "目前只支持文字消息。"
	replyForbidden   = "你没有这个空间的权限，无法使用这个助手。"
	replyClosed      = "这个会话已在 OpenFox 中结束，请在 OpenFox 里重新打开。"
	replyRetired     = "这个空间已停用，无法发起新的工作。"
)

// IMWorker 周期性扫描有待办的组织。单组织失败只记日志，不阻塞其他组织。
type IMWorker struct {
	Pool        SyncRunner
	Logger      *slog.Logger
	Service     Service
	Connections *LongConnManager
	// Instance 写入租约的 lease_owner；为空时用随机值。
	Instance string
	// Interval 为轮询间隔（默认 2 秒）。
	Interval time.Duration

	cursor string
}

func (w *IMWorker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *IMWorker) instance() string {
	if w.Instance == "" {
		w.Instance = "imworker-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *IMWorker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = imWorkerPollInterval
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
			for i := 1; i < failures && wait < imWorkerBackoffCap; i++ {
				wait *= 2
			}
			if wait > imWorkerBackoffCap {
				wait = imWorkerBackoffCap
			}
			w.logger().Warn("im_worker_pass_failed", "error", err.Error(), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮：列出有待办的组织并逐个处理（测试的单轮入口）。
func (w *IMWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("integrations: nil im worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_im_worker_organizations($1::uuid, $2)`, after, imWorkerScanLimit)
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
			w.logger().Warn("im_worker_organization_failed", "organization_id", id, "error", sanitizeLastError(err))
		}
		w.cursor = id
	}
	if len(ids) < imWorkerScanLimit {
		w.cursor = ""
	}
	return nil
}

// RunOrganization 处理一个组织：扫码会话 → 入站消息 → 回发。
func (w *IMWorker) RunOrganization(ctx context.Context, organizationID string) error {
	var errs []error
	for _, step := range []func(context.Context, string) error{w.pollRegistrations, w.processInbound, w.sendReplies} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := step(ctx, organizationID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *IMWorker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganizationAs(ctx, organizationID, syncWorkerActorID, 1, fn)
}

func (w *IMWorker) inOrganizationAs(ctx context.Context, organizationID, actorID string, authVersion uint64, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: actorID, AuthorizationVersion: authVersion}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func orgRole(value string) policy.OrganizationRole {
	return policy.OrganizationRole(strings.ToLower(value))
}

func recordIMAudit(ctx context.Context, tx pgx.Tx, organizationID, actorID string, authVersion uint64, action, kind, resourceID, spaceID string, meta map[string]string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta["resource_kind"] = kind
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actorID, Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: kind, OrganizationID: organizationID, SpaceID: spaceID, ID: resourceID},
		RequestID: requestid.New(), AuthorizationVersion: authVersion, SafeMetadata: meta, CreatedAt: time.Now().UTC(),
	})
}

// ---------- 1. 扫码会话 ----------

type regItem struct {
	ID, Purpose, SpaceID, AgentID, RequestedBy, Provider string
	Config                                               []byte
	Sealed                                               []byte
	ExpiresAt                                            time.Time
	Interval                                             int
	MemberRole                                           string
	MemberVersion                                        uint64
	MemberActive                                         bool
}

func (w *IMWorker) claimRegistrations(ctx context.Context, tx pgx.Tx) ([]regItem, error) {
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM openfox.im_app_registrations
			 WHERE organization_id = openfox_private.current_organization_id() AND status = 'PENDING'
			   AND next_poll_at <= clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())
			 ORDER BY next_poll_at LIMIT $2
			 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.im_app_registrations r
		   SET lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8), updated_at = clock_timestamp()
		  FROM due
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = due.id
		RETURNING r.id::text, r.purpose, COALESCE(r.space_id::text, ''), COALESCE(r.agent_id::text, ''), r.requested_by::text, r.provider,
		          r.config, r.device_code_sealed, r.expires_at, r.poll_interval,
		          COALESCE((SELECT m.role FROM openfox.organization_members m WHERE m.organization_id = r.organization_id AND m.user_id = r.requested_by), ''),
		          COALESCE((SELECT m.authorization_version FROM openfox.organization_members m WHERE m.organization_id = r.organization_id AND m.user_id = r.requested_by), 0),
		          COALESCE((SELECT m.status = 'ACTIVE' FROM openfox.organization_members m WHERE m.organization_id = r.organization_id AND m.user_id = r.requested_by), false)`,
		w.instance(), imRegBatch, imRegLease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []regItem
	for rows.Next() {
		var item regItem
		var version int64
		if err := rows.Scan(&item.ID, &item.Purpose, &item.SpaceID, &item.AgentID, &item.RequestedBy, &item.Provider, &item.Config, &item.Sealed,
			&item.ExpiresAt, &item.Interval, &item.MemberRole, &version, &item.MemberActive); err != nil {
			return nil, err
		}
		if version > 0 {
			item.MemberVersion = uint64(version)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (w *IMWorker) pollRegistrations(ctx context.Context, organizationID string) error {
	var items []regItem
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = w.claimRegistrations(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.pollRegistration(ctx, organizationID, item); err != nil {
			w.logger().Warn("im_registration_poll_failed", "organization_id", organizationID, "registration_id", item.ID, "error", sanitizeLastError(err))
		}
	}
	return nil
}

// finishRegistration 把会话置为终态（只在仍由本实例持有租约且等待中时生效）。
func (w *IMWorker) finishRegistration(ctx context.Context, organizationID, id, status, code string) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_app_registrations
			   SET status = $3, error_code = $4, device_code_sealed = NULL, lease_owner = '', lease_expires_at = NULL,
			       finished_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'PENDING' AND lease_owner = $2`,
			id, w.instance(), status, code)
		return err
	})
}

func (w *IMWorker) rescheduleRegistration(ctx context.Context, organizationID, id string, interval int) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_app_registrations
			   SET poll_interval = $3, next_poll_at = clock_timestamp() + make_interval(secs => $4::float8),
			       lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'PENDING' AND lease_owner = $2`,
			id, w.instance(), interval, float64(interval))
		return err
	})
}

func (w *IMWorker) pollRegistration(ctx context.Context, organizationID string, item regItem) error {
	if !item.ExpiresAt.After(time.Now()) {
		return w.finishRegistration(ctx, organizationID, item.ID, "EXPIRED", RegErrSessionExpired)
	}
	svc := w.Service.For(item.Provider)
	if svc.Feishu == nil {
		return errors.New("integrations: " + item.Provider + " client is not configured")
	}
	code, err := w.Service.open(item.Sealed, registrationAAD(organizationID, item.ID))
	if err != nil {
		return w.finishRegistration(ctx, organizationID, item.ID, "FAILED", RegErrSaveFailed)
	}
	result, err := svc.Feishu.RegistrationPoll(ctx, string(code))
	var slow *feishu.SlowDownError
	switch {
	case err == nil:
		if result.TenantBrand != "" && !strings.EqualFold(result.TenantBrand, item.Provider) {
			return w.finishRegistration(ctx, organizationID, item.ID, "FAILED", RegErrRejected)
		}
		return w.completeRegistration(ctx, organizationID, item, result)
	case errors.Is(err, feishu.ErrRegistrationPending), errors.Is(err, feishu.ErrUnavailable):
		return w.rescheduleRegistration(ctx, organizationID, item.ID, item.Interval)
	case errors.As(err, &slow):
		return w.rescheduleRegistration(ctx, organizationID, item.ID, min(item.Interval+slow.Extra, 120))
	case errors.Is(err, feishu.ErrRegistrationDenied):
		return w.finishRegistration(ctx, organizationID, item.ID, "DENIED", RegErrAccessDenied)
	case errors.Is(err, feishu.ErrRegistrationExpired):
		return w.finishRegistration(ctx, organizationID, item.ID, "EXPIRED", RegErrExpired)
	}
	return w.finishRegistration(ctx, organizationID, item.ID, "FAILED", RegErrRejected)
}

// completeRegistration 在用户确认后保存新应用：事务外取 token 与机器人信息（失败时仍保存，但连接标为需要处理），
// 再用发起人身份的短事务建立连接、机器人（与任务助手的内置 Agent）。并发冲突时拿到的飞书凭证被丢弃（已知局限）。
func (w *IMWorker) completeRegistration(ctx context.Context, organizationID string, item regItem, result feishu.RegistrationResult) error {
	var bot feishu.Bot
	attention := ""
	svc := w.Service.For(item.Provider)
	token, err := svc.Feishu.TenantToken(ctx, result.ClientID, result.ClientSecret)
	if err == nil {
		bot, err = svc.Feishu.BotInfo(ctx, token)
	}
	switch {
	case err == nil:
	case errors.Is(err, feishu.ErrInvalidCredentials):
		attention = ReasonCredentialsInvalid
	default:
		attention = ReasonBotUnavailable
	}
	if !item.MemberActive || item.MemberVersion == 0 {
		return w.finishRegistration(ctx, organizationID, item.ID, "FAILED", RegErrSaveFailed)
	}
	var failure string
	err = w.inOrganizationAs(ctx, organizationID, item.RequestedBy, item.MemberVersion, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		failure, err = w.saveRegistration(ctx, tx, organizationID, item, result, bot, attention)
		return err
	})
	if err != nil {
		return err
	}
	if failure != "" {
		return w.finishRegistration(ctx, organizationID, item.ID, "FAILED", failure)
	}
	return nil
}

// saveRegistration 返回非空 failure 表示业务上无法保存（事务里的改动已回滚到保存点，调用方随后把会话标为 FAILED）。
func (w *IMWorker) saveRegistration(ctx context.Context, tx pgx.Tx, organizationID string, item regItem, result feishu.RegistrationResult,
	bot feishu.Bot, attention string) (string, error) {
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM openfox.im_app_registrations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2 FOR UPDATE`,
		item.ID, w.instance()).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if status != registrationStatusPend {
		// 等待期间被取消：丢弃拿到的凭证。
		return "", nil
	}
	actor := collaboration.Actor{UserID: item.RequestedBy, Role: orgRole(item.MemberRole), AuthorizationVersion: item.MemberVersion}
	selection := selectionFor(organizationID, actor)
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	failure, err := w.insertBot(ctx, sp, organizationID, item, actor, selection, result, bot, attention, nil)
	if err != nil || failure != "" {
		_ = sp.Rollback(ctx)
		return failure, err
	}
	return "", sp.Commit(ctx)
}

func (w *IMWorker) insertBot(ctx context.Context, tx pgx.Tx, organizationID string, item regItem, actor collaboration.Actor,
	selection tenancy.OrganizationSelection, result feishu.RegistrationResult, bot feishu.Bot, attention string, created *string) (string, error) {
	var kind, accountKind, targetName, spaceID, agentID string
	var extraPrompt string
	switch item.Purpose {
	case PurposeTaskAssistant:
		kind, accountKind, spaceID = BotKindTaskAssistant, AccountKindTaskAssistant, item.SpaceID
		can, err := agents.CanManageSpace(ctx, tx, organizationID, spaceID, agentActor(actor))
		if err != nil {
			return "", err
		}
		if !can {
			return RegErrSaveFailed, nil
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			spaceID).Scan(&targetName); err != nil {
			return "", err
		}
		targetName = "OpenFox 任务助手 - " + targetName
		var cfg struct {
			Runtime     FeishuAssistantRuntime `json:"runtime"`
			ExtraPrompt string                 `json:"extra_prompt"`
		}
		if json.Unmarshal(item.Config, &cfg) != nil {
			return RegErrSaveFailed, nil
		}
		extraPrompt = cfg.ExtraPrompt
		config, err := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: cfg.Runtime.ComputerID, GrantID: cfg.Runtime.GrantID,
			Runtime: cfg.Runtime.Runtime, Model: cfg.Runtime.Model}, cfg.ExtraPrompt)
		if err != nil {
			return RegErrSaveFailed, nil
		}
		if err := agents.CheckRuntimeTarget(ctx, tx, organizationID, config); err != nil {
			return RegErrSaveFailed, nil
		}
		agentID, err = agents.CreateTaskAssistant(ctx, tx, selection, agentActor(actor), spaceID, config)
		if errors.Is(err, agents.ErrTaskAssistantExists) {
			return RegErrAssistantExists, nil
		}
		if err != nil {
			return "", err
		}
	default:
		kind, accountKind, agentID = BotKindAgent, AccountKindAgent, item.AgentID
		if err := agentBindCheck(ctx, tx, actor, agentID); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidRequest) {
				return RegErrSaveFailed, nil
			}
			return "", err
		}
		if err := tx.QueryRow(ctx, `SELECT name, COALESCE(space_id::text, '') FROM openfox.agents WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			agentID).Scan(&targetName, &spaceID); err != nil {
			return "", err
		}
	}
	connectionID, err := membership.NewID()
	if err != nil {
		return "", err
	}
	blob, keyID, err := w.Service.sealCredentials(organizationID, connectionID, connectionCredentials{AppSecret: result.ClientSecret})
	if err != nil {
		return "", err
	}
	settings := botSettings(bot)
	settings["scanner_open_id"] = result.OpenID
	settingsJSON, _ := json.Marshal(settings)
	displayName := targetName
	if strings.TrimSpace(bot.AppName) != "" {
		displayName = botDisplayName(bot, result.ClientID)
	}
	if _, err := tx.Exec(ctx, forProv(`
		INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, external_id, display_name, account_kind, status,
		       credentials_sealed, credentials_key_id, credentials_hint, settings, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'feishu', 'ORGANIZATION', $2, $3, $4, 'ACTIVE', $5, $6, $7, $8::jsonb, $9::uuid)`, item.Provider),
		connectionID, result.ClientID, displayName, accountKind, blob, keyID, tokenHint(result.ClientSecret), settingsJSON, item.RequestedBy); err != nil {
		if isUniqueViolation(err) {
			return RegErrSaveFailed, nil
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.im_connection_states (organization_id, connection_id, state)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'PENDING')`, connectionID); err != nil {
		return "", err
	}
	if attention != "" {
		if err := markFeishuNeedsAttention(ctx, tx, connectionID, attention); err != nil {
			return "", err
		}
	}
	botID, err := membership.NewID()
	if err != nil {
		return "", err
	}
	// im_bots.space_id 只用于任务助手；Agent 机器人的空间取自 Agent 本身。
	botSpaceID := ""
	if kind == BotKindTaskAssistant {
		botSpaceID = spaceID
	}
	// 并发下 Agent 已绑定 / 空间已有助手：唯一索引拒绝，会话标为失败。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.im_bots (organization_id, id, connection_id, kind, agent_id, space_id, extra_prompt, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4::uuid, NULLIF($5, '')::uuid, $6, $7::uuid)`,
		botID, connectionID, kind, agentID, botSpaceID, extraPrompt, item.RequestedBy); err != nil {
		if isUniqueViolation(err) {
			if kind == BotKindAgent {
				return RegErrAgentConnected, nil
			}
			return RegErrAssistantExists, nil
		}
		return "", err
	}
	if err := recordIMAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "integration.connection_created",
		"integration_connection", connectionID, spaceID, map[string]string{"source": item.Provider, "result": strings.ToLower(kind)}); err != nil {
		return "", err
	}
	if created != nil {
		*created = botID
	}
	// 凭证直连（钉钉）没有扫码会话行：item.ID 为空时跳过。
	if item.ID == "" {
		return "", nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_app_registrations
		   SET status = 'COMPLETED', error_code = '', bot_id = $2::uuid, connection_id = $3::uuid, device_code_sealed = NULL,
		       lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, item.ID, botID, connectionID); err != nil {
		return "", err
	}
	return "", nil
}

// ---------- 2. 入站消息 ----------

type inboundItem struct {
	ID, BotID, RemoteID, ChatID, ChatType, SenderOpenID, SenderUnionID, MessageType, Text, ParentID string
	Attempts                                                                                        int
}

func (w *IMWorker) claimInbound(ctx context.Context, tx pgx.Tx) ([]inboundItem, error) {
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM openfox.im_inbound_messages
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND ((status = 'PENDING' AND next_attempt_at <= clock_timestamp())
			        OR (status = 'PROCESSING' AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY next_attempt_at, received_at LIMIT $2
			 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.im_inbound_messages i
		   SET status = 'PROCESSING', lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8)
		  FROM due
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.id = due.id
		RETURNING i.id::text, i.bot_id::text, i.remote_message_id, i.chat_id, i.chat_type, i.sender_open_id, i.sender_union_id,
		          i.message_type, i.text, i.parent_remote_id, i.attempts, i.received_at`,
		w.instance(), imInboundBatch, imInboundLease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []inboundItem
	for rows.Next() {
		var item inboundItem
		var received time.Time
		if err := rows.Scan(&item.ID, &item.BotID, &item.RemoteID, &item.ChatID, &item.ChatType, &item.SenderOpenID, &item.SenderUnionID,
			&item.MessageType, &item.Text, &item.ParentID, &item.Attempts, &received); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (w *IMWorker) processInbound(ctx context.Context, organizationID string) error {
	var items []inboundItem
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = w.claimInbound(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	// 按接收顺序依次处理，同一会话里的消息不会乱序。
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.processOneInbound(ctx, organizationID, item); err != nil {
			w.logger().Warn("im_inbound_failed", "organization_id", organizationID, "inbound_id", item.ID, "attempts", item.Attempts+1, "error", sanitizeLastError(err))
			w.failInbound(ctx, organizationID, item, err)
		}
	}
	return nil
}

func inboundBackoff(attempts int) time.Duration {
	wait := imInboundBase
	for i := 1; i < attempts && wait < imInboundCap; i++ {
		wait *= 2
	}
	if wait > imInboundCap {
		wait = imInboundCap
	}
	return wait
}

// failInbound 退避重试：30 秒 × 2^n，上限 10 分钟，5 次后 FAILED。
func (w *IMWorker) failInbound(ctx context.Context, organizationID string, item inboundItem, cause error) {
	attempts := item.Attempts + 1
	note := truncateNote(sanitizeLastError(cause))
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if attempts >= imInboundMax {
			_, err := tx.Exec(ctx, `
				UPDATE openfox.im_inbound_messages
				   SET status = 'FAILED', attempts = $3, last_error = $4, lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
				item.ID, w.instance(), attempts, note)
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_inbound_messages
			   SET status = 'PENDING', attempts = $3, last_error = $4, next_attempt_at = clock_timestamp() + make_interval(secs => $5::float8),
			       lease_owner = '', lease_expires_at = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
			item.ID, w.instance(), attempts, note, inboundBackoff(attempts).Seconds())
		return err
	})
	if err != nil && ctx.Err() == nil {
		w.logger().Warn("im_inbound_writeback_failed", "inbound_id", item.ID, "error", sanitizeLastError(err))
	}
}

// inboundDecision 是第一步（worker 事务）读到的现状与判断。
type inboundDecision struct {
	done      bool // 已在事务里处理完（忽略 / 拒绝）
	botKind   string
	agentID   string
	spaceID   string
	botOpenID string
	appID     string
	provider  string
	secret    string
	member    conversations.Actor
	memberID  string
}

// errIMClientMissing 表示连接所属服务商的客户端未配置。
var errIMClientMissing = errors.New("integrations: im client is not configured")

func (w *IMWorker) publicBase() string { return strings.TrimRight(w.Service.PublicURL, "/") }

func (w *IMWorker) bindURL(organizationID, provider string) string {
	return w.publicBase() + "/o/" + url.PathEscape(strings.ToLower(organizationID)) + "/settings/integrations/" + provider
}

func (w *IMWorker) conversationURL(organizationID, conversationID string) string {
	return w.publicBase() + "/o/" + url.PathEscape(strings.ToLower(organizationID)) + "/chat/" + conversationID
}

// finishInbound 写入终态（只在仍由本实例持有租约时生效）。
func (w *IMWorker) finishInbound(ctx context.Context, tx pgx.Tx, id, status, result, messageID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_inbound_messages
		   SET status = $3, result = $4, message_id = NULLIF($5, '')::uuid, lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
		id, w.instance(), status, result, messageID)
	return err
}

// rejectInbound 回复提示并把入站项标为 REJECTED。
func (w *IMWorker) rejectInbound(ctx context.Context, tx pgx.Tx, item inboundItem, result, reply string) error {
	if reply != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.im_reply_outbox (organization_id, bot_id, chat_id, body, reply_to_remote_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4)`, item.BotID, item.ChatID, reply, item.RemoteID); err != nil {
			return err
		}
	}
	return w.finishInbound(ctx, tx, item.ID, "REJECTED", result, "")
}

func (w *IMWorker) decideInbound(ctx context.Context, tx pgx.Tx, organizationID string, item inboundItem) (inboundDecision, error) {
	var d inboundDecision
	var removed, agentArchived bool
	var connStatus, connectionID string
	err := tx.QueryRow(ctx, `
		SELECT b.kind, b.agent_id::text, COALESCE(a.space_id::text, ''), b.removed_at IS NOT NULL, a.archived_at IS NOT NULL,
		       c.status, c.id::text, c.external_id, COALESCE(c.settings->>'bot_open_id', ''), c.provider
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.id = $1::uuid`, item.BotID).
		Scan(&d.botKind, &d.agentID, &d.spaceID, &removed, &agentArchived, &connStatus, &connectionID, &d.appID, &d.botOpenID, &d.provider)
	if errors.Is(err, pgx.ErrNoRows) {
		d.done = true
		return d, w.finishInbound(ctx, tx, item.ID, "IGNORED", "bot_missing", "")
	}
	if err != nil {
		return d, err
	}
	// Agent 被归档：忽略消息，并把机器人一并移除（入站时发现的归档处理）。
	if !removed && agentArchived {
		r, err := loadBotRow(ctx, tx, "", item.BotID, true)
		if err == nil {
			if err := retireBot(ctx, tx, organizationID, tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: syncWorkerActorID, AuthorizationVersion: 1},
				agents.Actor{UserID: syncWorkerActorID}, r, "agent archived"); err != nil {
				return d, err
			}
		}
		removed = true
	}
	if removed || connStatus != StatusActive {
		d.done = true
		return d, w.finishInbound(ctx, tx, item.ID, "IGNORED", "bot_unavailable", "")
	}
	if strings.TrimSpace(item.Text) == "" {
		d.done = true
		return d, w.rejectInbound(ctx, tx, item, "unsupported", replyUnsupported)
	}
	// 认人：本组织有效的飞书个人绑定里 union_id 相同、成员有效。
	var role string
	var version int64
	if d.provider == ProviderWeCom {
		err = tx.QueryRow(ctx, `
			SELECT p.owner_user_id::text, m.role, m.authorization_version
			  FROM openfox.integration_connections p
			  JOIN openfox.organization_members m ON m.organization_id = p.organization_id AND m.user_id = p.owner_user_id
			 WHERE p.organization_id = openfox_private.current_organization_id()
			   AND p.provider = 'wecom' AND p.scope = 'PERSONAL' AND p.status = 'ACTIVE'
			   AND p.parent_connection_id = $1::uuid AND p.external_id = $2
			   AND m.status = 'ACTIVE' AND m.role <> 'GUEST'`, connectionID, item.SenderOpenID).
			Scan(&d.memberID, &role, &version)
	} else {
		err = tx.QueryRow(ctx, forProv(`
		SELECT p.owner_user_id::text, m.role, m.authorization_version
		  FROM openfox.integration_connections p
		  JOIN openfox.integration_connections a ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
		       AND a.account_kind = 'app' AND a.status = 'ACTIVE'
		  JOIN openfox.organization_members m ON m.organization_id = p.organization_id AND m.user_id = p.owner_user_id AND m.status = 'ACTIVE'
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.provider = 'feishu' AND p.scope = 'PERSONAL'
		   AND p.status = 'ACTIVE' AND p.settings->>'union_id' = $1 AND $1 <> ''
		 ORDER BY p.created_at LIMIT 1`, d.provider), item.SenderUnionID).Scan(&d.memberID, &role, &version)
	}
	if errors.Is(err, pgx.ErrNoRows) && d.botKind == BotKindAgent && IsFeishuFamily(d.provider) {
		// 扫码身份只在当前机器人连接内有效，不能据此操作其他机器人的 Agent。
		// 使用平台验证的 open_id 和当前成员状态，不信任消息中的名字或 union_id 猜测。
		err = tx.QueryRow(ctx, `
			SELECT m.user_id::text, m.role, m.authorization_version
			  FROM openfox.integration_connections c
			  JOIN openfox.organization_members m ON m.organization_id = c.organization_id AND m.user_id = c.created_by
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid
			   AND c.provider = $2 AND c.account_kind = 'agent' AND c.status = 'ACTIVE'
			   AND c.settings->>'scanner_open_id' = $3 AND $3 <> ''
			   AND m.status = 'ACTIVE' AND m.role <> 'GUEST'`, connectionID, d.provider, item.SenderOpenID).
			Scan(&d.memberID, &role, &version)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		d.done = true
		var recent bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.im_inbound_messages
			                WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid AND chat_id = $2
			                  AND sender_open_id = $3 AND status = 'REJECTED' AND result = 'unbound' AND id <> $4::uuid
			                  AND finished_at > clock_timestamp() - interval '`+imUnboundWindow+`')`,
			item.BotID, item.ChatID, item.SenderOpenID, item.ID).Scan(&recent); err != nil {
			return d, err
		}
		reply := "请先在 OpenFox 关联你的" + providerBrand(d.provider) + "账号：" + w.bindURL(organizationID, d.provider)
		if recent {
			reply = ""
		}
		return d, w.rejectInbound(ctx, tx, item, "unbound", reply)
	}
	if err != nil {
		return d, err
	}
	d.member = conversations.Actor{UserID: d.memberID, Role: orgRole(role), AuthorizationVersion: uint64(version)}
	// 权限预检：成员能读机器人所在空间（真正的 Agent 可用性由派发时判断）。
	can, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID, d.member, d.spaceID)
	if err != nil {
		return d, err
	}
	if !can {
		d.done = true
		return d, w.rejectInbound(ctx, tx, item, "forbidden", replyForbidden)
	}
	if d.botKind == BotKindTaskAssistant && item.ChatType == "group" {
		d.secret, err = w.Service.For(d.provider).feishuSecret(ctx, tx, organizationID, connectionID)
		if err != nil {
			d.secret = ""
		}
	}
	return d, nil
}

func (w *IMWorker) processOneInbound(ctx context.Context, organizationID string, item inboundItem) error {
	var d inboundDecision
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = w.decideInbound(ctx, tx, organizationID, item)
		return err
	}); err != nil {
		return err
	}
	if d.done {
		return nil
	}
	// 群聊上下文（只限任务助手 + 群聊）：事务外读取；失败（含缺权限）不带上下文。
	chatContext, contextFailed := "", false
	// 钉钉没有读取群历史消息的接口：不带群聊上下文，也不提示「读取失败」。
	if d.botKind == BotKindTaskAssistant && item.ChatType == "group" && d.provider != ProviderDingTalk {
		chatContext, contextFailed = w.fetchContext(ctx, d, item)
	}
	body := composeInboundBody(item.Text, chatContext, contextFailed)
	return w.inOrganizationAs(ctx, organizationID, d.member.UserID, d.member.AuthorizationVersion, func(ctx context.Context, tx pgx.Tx) error {
		return w.postInbound(ctx, tx, organizationID, item, d, body)
	})
}

func (w *IMWorker) fetchContext(ctx context.Context, d inboundDecision, item inboundItem) (string, bool) {
	client := w.Service.For(d.provider).Feishu
	if client == nil || d.secret == "" {
		return "", true
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.TenantToken(ctx, d.appID, d.secret)
		if err != nil {
			return "", true
		}
		items, err := client.ListChatMessages(ctx, token, item.ChatID, maxContextLines+1)
		if errors.Is(err, feishu.ErrTokenExpired) {
			client.ClearToken(d.appID, d.secret)
			continue
		}
		if err != nil {
			return "", true
		}
		return buildChatContext(items, item.SenderOpenID, item.RemoteID, d.botOpenID), false
	}
	return "", true
}

// postInbound 在发送人身份的事务里找到 / 创建绑定的会话并写入消息（触发 Agent 运行）。
func (w *IMWorker) postInbound(ctx context.Context, tx pgx.Tx, organizationID string, item inboundItem, d inboundDecision, body string) error {
	service := conversations.Service{}
	var conversationID string
	err := tx.QueryRow(ctx, `
		SELECT conversation_id::text FROM openfox.im_chat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid AND chat_id = $2`,
		item.BotID, item.ChatID).Scan(&conversationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if conversationID == "" {
		created, reject, err := w.createBinding(ctx, tx, organizationID, item, d)
		if err != nil {
			return err
		}
		if reject != nil {
			return w.rejectInbound(ctx, tx, item, reject.result, reject.reply)
		}
		conversationID = created
	}
	// 未绑定任务的群聊自动加入；已绑定任务的群聊须先接受邀请。
	if item.ChatType == "group" {
		if err := service.AddIMParticipant(ctx, tx, organizationID, conversationID, d.member); err != nil {
			if reject := mapInboundReject(err); reject != nil {
				return w.rejectInbound(ctx, tx, item, reject.result, reject.reply)
			}
			return err
		}
	}
	// 重放幂等：同一飞书消息已写入过就直接用它（重试时群聊记录可能不同，正文不能当去重依据）。
	clientID := "feishu:" + item.RemoteID
	var messageID string
	err = tx.QueryRow(ctx, `
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
			ID: id, Body: body, ClientMessageID: clientID, Mentions: []conversations.Mention{{AgentID: d.agentID}}})
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

type inboundReject struct{ result, reply string }

// mapInboundReject 把会话服务的业务拒绝翻译成给飞书用户的提示；系统错误返回 nil（按重试处理）。
func mapInboundReject(err error) *inboundReject {
	switch {
	case errors.Is(err, conversations.ErrInviteRequired):
		return &inboundReject{"forbidden", "请先在 OpenFox 接受此任务会话的邀请，再发送消息。"}
	case errors.Is(err, conversations.ErrForbidden), errors.Is(err, conversations.ErrNotFound), errors.Is(err, conversations.ErrInvalidMention):
		return &inboundReject{"forbidden", replyForbidden}
	case errors.Is(err, conversations.ErrConversationClosed):
		return &inboundReject{"closed", replyClosed}
	case errors.Is(err, collaboration.ErrSpaceRetired):
		return &inboundReject{"retired", replyRetired}
	}
	return nil
}

// createBinding 建立 OpenFox 会话与飞书会话的绑定。并发创建靠唯一索引兜底：冲突后回滚保存点并重读。
func (w *IMWorker) createBinding(ctx context.Context, tx pgx.Tx, organizationID string, item inboundItem, d inboundDecision) (string, *inboundReject, error) {
	service := conversations.Service{}
	brand := providerBrand(d.provider)
	visibility, title := conversations.VisibilityPrivate, brand+"单聊"
	if item.ChatType == "group" {
		visibility, title = conversations.VisibilityShared, brand+"群聊"
	}
	conversationID, err := membership.NewID()
	if err != nil {
		return "", nil, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	if _, err := service.Create(ctx, sp, organizationID, d.member, conversations.CreateInput{
		ID: conversationID, SpaceID: d.spaceID, AgentID: d.agentID, Visibility: visibility, Origin: conversations.OriginIM}); err != nil {
		_ = sp.Rollback(ctx)
		if reject := mapInboundReject(err); reject != nil {
			return "", reject, nil
		}
		return "", nil, err
	}
	if _, err := sp.Exec(ctx, `
		UPDATE openfox.conversations SET title = $2
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversationID, title); err != nil {
		_ = sp.Rollback(ctx)
		return "", nil, err
	}
	tag, err := sp.Exec(ctx, `
		INSERT INTO openfox.im_chat_bindings (organization_id, bot_id, chat_id, chat_type, conversation_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4::uuid, $5::uuid)
		ON CONFLICT DO NOTHING`, item.BotID, item.ChatID, item.ChatType, conversationID, d.member.UserID)
	if err != nil {
		_ = sp.Rollback(ctx)
		return "", nil, err
	}
	if tag.RowsAffected() == 1 {
		return conversationID, nil, sp.Commit(ctx)
	}
	// 别的实例先建好了：丢弃刚建的会话，改用它的。
	if err := sp.Rollback(ctx); err != nil {
		return "", nil, err
	}
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT conversation_id::text FROM openfox.im_chat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid AND chat_id = $2`,
		item.BotID, item.ChatID).Scan(&existing)
	return existing, nil, err
}

// ---------- 3. 回发 ----------

type imOutboxItem struct {
	ID, BotID, ChatID, MessageID, Body, ReplyTo string
	Attempts                                    int
}

// imOutboxTarget 是通过发送前检查的项及其发送所需的值。
type imOutboxTarget struct {
	item         imOutboxItem
	connectionID string
	appID        string
	provider     string
	secret       string
	card         string
}

func (w *IMWorker) claimOutbox(ctx context.Context, tx pgx.Tx) ([]imOutboxItem, error) {
	wecomLeaseOwner := ""
	if w.Connections != nil {
		wecomLeaseOwner = w.Connections.instance()
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_reply_outbox o
		   SET status = 'FAILED', last_error = 'delivery_unknown', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.bot_id = b.id
		   AND c.provider = 'wecom' AND o.status = 'SENDING'
		   AND (o.lease_expires_at IS NULL OR o.lease_expires_at <= clock_timestamp())`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT o.id FROM openfox.im_reply_outbox o
			 JOIN openfox.im_bots b ON b.organization_id = o.organization_id AND b.id = o.bot_id
			 JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
			 WHERE o.organization_id = openfox_private.current_organization_id()
			   AND ((o.status = 'PENDING' AND o.next_attempt_at <= clock_timestamp())
			        OR (o.status = 'SENDING' AND (o.lease_expires_at IS NULL OR o.lease_expires_at <= clock_timestamp())))
			   AND (c.provider <> 'wecom' OR o.status = 'PENDING')
			   AND (c.provider <> 'wecom' OR EXISTS (
			       SELECT 1 FROM openfox.im_connection_states state
			        WHERE state.organization_id = c.organization_id AND state.connection_id = c.id
			          AND state.lease_owner = $4 AND state.lease_expires_at > clock_timestamp()
			          AND state.state = 'CONNECTED'))
			 ORDER BY o.next_attempt_at, o.created_at LIMIT $2
			 FOR UPDATE OF o SKIP LOCKED)
		UPDATE openfox.im_reply_outbox o
		   SET status = 'SENDING', lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8),
		       updated_at = clock_timestamp()
		  FROM due
		 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = due.id
		RETURNING o.id::text, o.bot_id::text, o.chat_id, COALESCE(o.message_id::text, ''), o.body, o.reply_to_remote_id, o.attempts, o.created_at`,
		w.instance(), imOutboxBatch, imOutboxLease.Seconds(), wecomLeaseOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []imOutboxItem
	for rows.Next() {
		var item imOutboxItem
		var created time.Time
		if err := rows.Scan(&item.ID, &item.BotID, &item.ChatID, &item.MessageID, &item.Body, &item.ReplyTo, &item.Attempts, &created); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (w *IMWorker) finishOutbox(ctx context.Context, tx pgx.Tx, id, status, note, remoteID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_reply_outbox
		   SET status = $3, last_error = $4, remote_message_id = $5, lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`,
		id, w.instance(), status, truncateNote(note), remoteID)
	return err
}

func (w *IMWorker) deferOutbox(ctx context.Context, tx pgx.Tx, id string, until time.Time, countAttempt bool, note string) error {
	increment := 0
	if countAttempt {
		increment = 1
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_reply_outbox
		   SET status = 'PENDING', attempts = attempts + $4, next_attempt_at = $3, last_error = $5, lease_owner = '', lease_expires_at = NULL,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'SENDING' AND lease_owner = $2`,
		id, w.instance(), until, increment, truncateNote(note))
	return err
}

// prepareOutbox 做发送前检查；不满足返回跳过原因，满足返回发送目标（含卡片）。
func (w *IMWorker) prepareOutbox(ctx context.Context, tx pgx.Tx, organizationID string, item imOutboxItem, secrets map[string]string) (imOutboxTarget, string, error) {
	var removed bool
	var connStatus, connectionID, appID, provider string
	err := tx.QueryRow(ctx, `
		SELECT b.removed_at IS NOT NULL, c.status, c.id::text, c.external_id, c.provider
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.id = $1::uuid`, item.BotID).
		Scan(&removed, &connStatus, &connectionID, &appID, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return imOutboxTarget{}, "bot_missing", nil
	}
	if err != nil {
		return imOutboxTarget{}, "", err
	}
	if removed {
		return imOutboxTarget{}, "bot_removed", nil
	}
	if connStatus != StatusActive {
		return imOutboxTarget{}, "app_unavailable", nil
	}
	if provider == ProviderWeCom && (item.MessageID != "" || item.ReplyTo == "") {
		var bound bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM openfox.integration_connections p
			JOIN openfox.organization_members m
			  ON m.organization_id = p.organization_id AND m.user_id = p.owner_user_id
			WHERE p.organization_id = openfox_private.current_organization_id()
			  AND p.provider = 'wecom' AND p.scope = 'PERSONAL' AND p.status = 'ACTIVE'
			  AND p.parent_connection_id = $1::uuid AND p.external_id = $2
			  AND m.status = 'ACTIVE' AND m.role <> 'GUEST')`, connectionID, item.ChatID).Scan(&bound)
		if err != nil {
			return imOutboxTarget{}, "", err
		}
		if !bound {
			return imOutboxTarget{}, "account_unbound", nil
		}
	}
	body, link := item.Body, ""
	if item.MessageID != "" {
		var conversationID string
		err := tx.QueryRow(ctx, `
			SELECT body, conversation_id::text FROM openfox.messages
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, item.MessageID).Scan(&body, &conversationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return imOutboxTarget{}, "message_missing", nil
		}
		if err != nil {
			return imOutboxTarget{}, "", err
		}
		link = w.conversationURL(organizationID, conversationID)
	}
	if strings.TrimSpace(body) == "" {
		return imOutboxTarget{}, "empty", nil
	}
	if provider != ProviderWeCom {
		if _, ok := secrets[connectionID]; !ok {
			secret, err := w.Service.For(provider).feishuSecret(ctx, tx, organizationID, connectionID)
			if err != nil {
				return imOutboxTarget{}, "credentials_unavailable", nil
			}
			secrets[connectionID] = secret
		}
	}
	card := buildReplyCard(body, link)
	if provider == ProviderDingTalk {
		card = buildDingTalkReply(body, link)
	}
	if provider == ProviderWeCom {
		card = buildWeComReply(body, link)
	}
	return imOutboxTarget{item: item, connectionID: connectionID, appID: appID, provider: provider, secret: secrets[connectionID], card: card}, "", nil
}

func (w *IMWorker) sendReplies(ctx context.Context, organizationID string) error {
	var targets []imOutboxTarget
	secrets := map[string]string{}
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		items, err := w.claimOutbox(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			target, reason, err := w.prepareOutbox(ctx, tx, organizationID, item, secrets)
			if err != nil {
				return err
			}
			if reason != "" {
				if err := w.finishOutbox(ctx, tx, item.ID, "SKIPPED", reason, ""); err != nil {
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
	limited := map[string]time.Time{}
	invalid := map[string]bool{}
	tokens := map[string]string{}
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item := target.item
		var apply func(ctx context.Context, tx pgx.Tx) error
		revoke := false
		switch until, isLimited := limited[target.connectionID]; {
		case invalid[target.connectionID]:
			apply = func(ctx context.Context, tx pgx.Tx) error {
				return w.finishOutbox(ctx, tx, item.ID, "FAILED", "credentials_invalid", "")
			}
		case isLimited:
			apply = func(ctx context.Context, tx pgx.Tx) error {
				return w.deferOutbox(ctx, tx, item.ID, until, false, "rate_limited")
			}
		default:
			remoteID, err := w.sendReply(ctx, organizationID, tokens, target)
			switch {
			case errors.Is(err, errIMClientMissing):
				// 该服务商的客户端未配置（例如运维移除了配置）：稍后重试，不计次数。
				until := time.Now().Add(notifyBackoff(1))
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.deferOutbox(ctx, tx, item.ID, until, false, "client_unavailable")
				}
			case err == nil:
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.finishOutbox(ctx, tx, item.ID, "DONE", "", remoteID)
				}
			case errors.Is(err, wecom.ErrDeliveryUnknown), errors.Is(err, wecom.ErrSendRejected):
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.finishOutbox(ctx, tx, item.ID, "FAILED", sanitizeLastError(err), "")
				}
			case errors.Is(err, wecom.ErrUnavailable):
				until := time.Now().Add(notifyBackoff(1))
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.deferOutbox(ctx, tx, item.ID, until, false, "connection_unavailable")
				}
			case errors.Is(err, feishu.ErrRateLimited):
				wait := 30 * time.Second
				var rl *feishu.RateLimitedError
				if errors.As(err, &rl) && rl.RetryAfter > 0 {
					wait = rl.RetryAfter
				}
				until := time.Now().Add(wait)
				limited[target.connectionID] = until
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.deferOutbox(ctx, tx, item.ID, until, false, "rate_limited")
				}
			case errors.Is(err, feishu.ErrInvalidCredentials):
				invalid[target.connectionID] = true
				revoke = true
				apply = func(ctx context.Context, tx pgx.Tx) error {
					return w.finishOutbox(ctx, tx, item.ID, "FAILED", "credentials_invalid", "")
				}
			default:
				note := sanitizeLastError(err)
				attempts := item.Attempts + 1
				if attempts >= imOutboxMax {
					apply = func(ctx context.Context, tx pgx.Tx) error {
						if _, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET attempts = $3 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`,
							item.ID, w.instance(), attempts); e != nil {
							return e
						}
						return w.finishOutbox(ctx, tx, item.ID, "FAILED", note, "")
					}
				} else {
					until := time.Now().Add(notifyBackoff(attempts))
					apply = func(ctx context.Context, tx pgx.Tx) error {
						return w.deferOutbox(ctx, tx, item.ID, until, true, note)
					}
				}
			}
		}
		connectionID := target.connectionID
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			if revoke {
				if err := markFeishuNeedsAttention(ctx, tx, connectionID, ReasonCredentialsInvalid); err != nil {
					return err
				}
			}
			return apply(ctx, tx)
		})
		if err != nil {
			w.logger().Warn("im_reply_writeback_failed", "organization_id", organizationID, "outbox_id", item.ID, "error", sanitizeLastError(err))
		}
	}
	return nil
}

// sendReply 发送一张卡片：有被回复的消息先回复，被回复的消息不存在（已撤回）时改发群消息；tenant_access_token 失效时清缓存重试一次。
func (w *IMWorker) sendReply(ctx context.Context, organizationID string, tokens map[string]string, target imOutboxTarget) (string, error) {
	if target.provider == ProviderWeCom {
		if w.Connections == nil {
			return "", errIMClientMissing
		}
		return "", w.Connections.SendWeCom(ctx, organizationID, target.connectionID, target.item.ChatID, target.card)
	}
	if target.provider == ProviderDingTalk {
		return w.sendDingTalkReply(ctx, tokens, target)
	}
	client := w.Service.For(target.provider).Feishu
	if client == nil {
		return "", errIMClientMissing
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, ok := tokens[target.connectionID]
		if !ok {
			var err error
			if token, err = client.TenantToken(ctx, target.appID, target.secret); err != nil {
				return "", err
			}
			tokens[target.connectionID] = token
		}
		var messageID string
		var err error
		if target.item.ReplyTo != "" {
			messageID, err = client.ReplyCard(ctx, token, target.item.ReplyTo, target.card, target.item.ID)
			if errors.Is(err, feishu.ErrMessageGone) {
				messageID, err = client.SendChatCard(ctx, token, target.item.ChatID, target.card, target.item.ID)
			}
		} else {
			messageID, err = client.SendChatCard(ctx, token, target.item.ChatID, target.card, target.item.ID)
		}
		if errors.Is(err, feishu.ErrTokenExpired) {
			client.ClearToken(target.appID, target.secret)
			delete(tokens, target.connectionID)
			continue
		}
		return messageID, err
	}
	return "", feishu.ErrUnavailable
}
