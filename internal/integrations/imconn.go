package integrations

// 飞书长连接管理器。每个组织级飞书应用连接只由一个中心实例（租约持有者）建立长连接；
// 多实例部署时租约存放在 im_connection_states。结构仿照 SyncWorker：控制面函数列出连接，租户事务里拿租约，
// 对飞书的网络请求都在事务之外。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/wecom"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

// 飞书连接的类型：组织应用、任务助手机器人、Agent 机器人（integration_connections.account_kind）。
const (
	AccountKindApp           = "app"
	AccountKindTaskAssistant = "task_assistant"
	AccountKindAgent         = "agent"
)

const (
	imScanLimit          = 100
	imEventTouchInterval = 30 * time.Second
	imCleanupInterval    = time.Hour
	imCleanupBatch       = 1000
	imInboundRetention   = "7 days"
	imOutboxRetention    = "30 days"
	imMaxBackoff         = 2 * time.Minute
	imStopWait           = 8 * time.Second
	imLastErrorLimit     = 500
)

// EventDispatcher 是长连接消息事件的分发点。
// 事件已按 (连接, event_id) 去重；返回的错误只记日志，不影响 ACK。
type EventDispatcher func(ctx context.Context, organizationID, connectionID, eventType string, payload []byte) error

// LongConnManager 管理本实例持有租约的飞书长连接。
type LongConnManager struct {
	Pool     SyncRunner
	Logger   *slog.Logger
	Instance string
	Service  Service
	Client   *feishu.Client
	// Dispatch 为 nil 时除链接预览外的事件只记录去重。
	Dispatch EventDispatcher

	// ScanInterval 扫描新连接的间隔（默认 10 秒）；LeaseTTL 租约有效期（60 秒）；RenewInterval 续租间隔（20 秒）。
	ScanInterval  time.Duration
	LeaseTTL      time.Duration
	RenewInterval time.Duration
	// MaxSessions 是本实例最多同时持有的长连接数（默认 200）。
	MaxSessions int
	// RetryBase 是没有服务端建议时的重连退避起点（默认 5 秒）。
	RetryBase time.Duration

	mu        sync.Mutex
	sessions  map[sessionKey]*localSession
	touchedAt map[sessionKey]time.Time
	// unionCache 缓存钉钉发送人的 userid → unionId（见 dingTalkUnionID），有界。
	unionCache    map[string]dingTalkUnionEntry
	wecomSessions map[sessionKey]*wecom.Session
	cleanedAt     time.Time
}

type sessionKey struct{ organizationID, connectionID string }

type localSession struct {
	cancel      context.CancelFunc
	done        chan struct{}
	fingerprint string
}

func (m *LongConnManager) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *LongConnManager) instance() string {
	if m.Instance == "" {
		m.Instance = "imconn-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return m.Instance
}

func orDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func (m *LongConnManager) scanInterval() time.Duration {
	return orDuration(m.ScanInterval, 10*time.Second)
}
func (m *LongConnManager) leaseTTL() time.Duration { return orDuration(m.LeaseTTL, 60*time.Second) }
func (m *LongConnManager) renewInterval() time.Duration {
	return orDuration(m.RenewInterval, 20*time.Second)
}
func (m *LongConnManager) retryBase() time.Duration { return orDuration(m.RetryBase, 5*time.Second) }
func (m *LongConnManager) maxSessions() int {
	if m.MaxSessions <= 0 {
		return 200
	}
	return m.MaxSessions
}

// Run 循环扫描与续租，直到 ctx 取消；返回前关闭全部会话并释放租约。
func (m *LongConnManager) Run(ctx context.Context) error {
	if m.Pool == nil || (m.Client == nil && len(m.Service.IMClients) == 0 && m.Service.DingTalk == nil && m.Service.WeCom == nil) {
		return errors.New("integrations: long connection manager is not configured")
	}
	scan := time.NewTicker(m.scanInterval())
	renew := time.NewTicker(m.renewInterval())
	defer scan.Stop()
	defer renew.Stop()
	m.runScan(ctx)
	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return nil
		case <-scan.C:
			m.runScan(ctx)
		case <-renew.C:
			if m.renewAll(ctx) {
				m.runScan(ctx)
			}
		}
	}
}

func (m *LongConnManager) runScan(ctx context.Context) {
	if err := m.Scan(ctx); err != nil && ctx.Err() == nil {
		m.logger().Warn("feishu_longconn_scan_failed", "error", err.Error())
	}
	m.maybeCleanup(ctx)
}

func (m *LongConnManager) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	return m.inOrganizationAs(ctx, organizationID, syncWorkerActorID, 1, fn)
}

func (m *LongConnManager) inOrganizationAs(ctx context.Context, organizationID, actorID string, authVersion uint64, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: actorID, AuthorizationVersion: authVersion}
	return m.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func (m *LongConnManager) listConnections(ctx context.Context) ([]sessionKey, error) {
	var keys []sessionKey
	var afterOrg, afterConn any
	for {
		var page []sessionKey
		err := m.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT organization_id::text, connection_id::text FROM openfox_private.list_im_connections($1::uuid, $2::uuid, $3)`,
				afterOrg, afterConn, imScanLimit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var key sessionKey
				if err := rows.Scan(&key.organizationID, &key.connectionID); err != nil {
					return err
				}
				page = append(page, key)
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
		keys = append(keys, page...)
		if len(page) < imScanLimit {
			return keys, nil
		}
		last := page[len(page)-1]
		afterOrg, afterConn = last.organizationID, last.connectionID
	}
}

// leaseResult 是拿租约时读到的连接现状。
type leaseResult struct {
	owned       bool
	status      string
	state       string
	appID       string
	secret      string
	fingerprint string
	// accountKind 是连接类型（app / task_assistant / agent），botOpenID 是机器人的 open_id，只对机器人连接有值。
	accountKind string
	botOpenID   string
	provider    string
}

// acquire 尝试拿到（或续上）某个连接的租约，拿到时一并读取连接的 App ID、Secret 与凭证指纹。
func (m *LongConnManager) acquire(ctx context.Context, key sessionKey, needSecret bool) (leaseResult, error) {
	var res leaseResult
	ttl := m.leaseTTL().Seconds()
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var owner string
		err := tx.QueryRow(ctx, `
			INSERT INTO openfox.im_connection_states (organization_id, connection_id, lease_owner, lease_expires_at, state)
			SELECT openfox_private.current_organization_id(), c.id, $2, clock_timestamp() + make_interval(secs => $3::float8), 'CONNECTING'
			  FROM openfox.integration_connections c
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid
			   AND c.provider IN ('feishu', 'lark', 'dingtalk', 'wecom') AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
			ON CONFLICT (organization_id, connection_id) DO UPDATE
			   SET lease_owner = EXCLUDED.lease_owner, lease_expires_at = EXCLUDED.lease_expires_at, updated_at = clock_timestamp()
			 WHERE im_connection_states.lease_owner = EXCLUDED.lease_owner
			    OR im_connection_states.lease_owner = '' OR im_connection_states.lease_expires_at IS NULL
			    OR im_connection_states.lease_expires_at < clock_timestamp()
			RETURNING lease_owner, state`, key.connectionID, m.instance(), ttl).Scan(&owner, &res.state)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		res.owned = true
		var sealed []byte
		if err := tx.QueryRow(ctx, `
			SELECT status, external_id, credentials_sealed, encode(sha256(COALESCE(credentials_sealed, ''::bytea)), 'hex'),
			       account_kind, COALESCE(settings->>'bot_open_id', ''), provider
			  FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, key.connectionID).
			Scan(&res.status, &res.appID, &sealed, &res.fingerprint, &res.accountKind, &res.botOpenID, &res.provider); err != nil {
			return err
		}
		if needSecret {
			creds, err := m.Service.openCredentials(key.organizationID, key.connectionID, sealed)
			if err != nil {
				return err
			}
			res.secret = creds.AppSecret
		}
		return nil
	})
	return res, err
}

func (m *LongConnManager) local(key sessionKey) (*localSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	return s, ok
}

func (m *LongConnManager) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.sessions {
		select {
		case <-s.done:
			delete(m.sessions, key)
			delete(m.touchedAt, key)
		default:
		}
	}
}

// Scan 执行一轮：列出连接，拿租约，启动缺失的会话，停止已经消失或被抢走租约的会话。
func (m *LongConnManager) Scan(ctx context.Context) error {
	m.reap()
	keys, err := m.listConnections(ctx)
	if err != nil {
		return err
	}
	listed := make(map[sessionKey]bool, len(keys))
	for _, key := range keys {
		listed[key] = true
	}
	// 列表里消失的本地会话：停止并释放租约。
	m.mu.Lock()
	var gone []sessionKey
	for key := range m.sessions {
		if !listed[key] {
			gone = append(gone, key)
		}
	}
	m.mu.Unlock()
	for _, key := range gone {
		m.stopSession(key)
		m.release(ctx, key)
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		session, running := m.local(key)
		if !running {
			m.mu.Lock()
			full := len(m.sessions) >= m.maxSessions()
			m.mu.Unlock()
			if full {
				continue
			}
		}
		res, err := m.acquire(ctx, key, !running)
		if errors.Is(err, secrets.ErrDecrypt) {
			// 主密钥已更换等原因导致无法解密：标为需要处理，管理员重新保存凭证后会用当前主密钥重新加密。
			m.markUndecryptable(ctx, key)
			continue
		}
		if err != nil {
			m.logger().Warn("feishu_longconn_lease_failed", "organization_id", key.organizationID, "connection_id", key.connectionID, "error", err.Error())
			continue
		}
		if !res.owned {
			if running {
				m.stopSession(key)
			}
			continue
		}
		if running {
			// 凭证变了，或管理员重新验证 / 更新凭证把状态重置为 PENDING：重建长连接。
			if session.fingerprint != res.fingerprint || res.state == "PENDING" {
				m.stopSession(key)
			} else {
				continue
			}
			// 凭证变了：用新的 Secret 重新建立。
			if res, err = m.acquire(ctx, key, true); err != nil || !res.owned {
				continue
			}
		}
		if res.status != StatusActive || res.secret == "" {
			continue
		}
		m.startSession(ctx, key, res)
	}
	return nil
}

func (m *LongConnManager) markUndecryptable(ctx context.Context, key sessionKey) {
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := markFeishuNeedsAttention(ctx, tx, key.connectionID, ReasonCredentialsInvalid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_connection_states SET last_error = $2
			 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid`,
			key.connectionID, "无法解密已保存的 App Secret，请重新保存凭证")
		return err
	})
	if err != nil && ctx.Err() == nil {
		m.logger().Warn("feishu_longconn_mark_failed", "connection_id", key.connectionID, "error", err.Error())
		return
	}
	m.logger().Warn("feishu_longconn_secret_undecryptable", "connection_id", key.connectionID)
}

func (m *LongConnManager) startSession(parent context.Context, key sessionKey, res leaseResult) {
	// 接手后立即离开 PENDING：续租时 PENDING 表示管理员重置了状态（更新凭证 / 重新验证），需要重建长连接。
	m.updateState(parent, key, `
		UPDATE openfox.im_connection_states SET state = 'CONNECTING', updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid
		   AND lease_owner = $2 AND state = 'PENDING'`)
	ctx, cancel := context.WithCancel(parent)
	s := &localSession{cancel: cancel, done: make(chan struct{}), fingerprint: res.fingerprint}
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = map[sessionKey]*localSession{}
		m.touchedAt = map[sessionKey]time.Time{}
	}
	m.sessions[key] = s
	m.mu.Unlock()
	go func() {
		defer close(s.done)
		defer cancel()
		m.runSession(ctx, key, res.provider, res.appID, res.secret, res.accountKind, res.botOpenID)
	}()
}

// stopSession 停止本地会话并等待它退出（不释放租约）。
func (m *LongConnManager) stopSession(key sessionKey) {
	m.mu.Lock()
	s, ok := m.sessions[key]
	if ok {
		delete(m.sessions, key)
		delete(m.touchedAt, key)
		delete(m.wecomSessions, key)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(imStopWait):
		m.logger().Warn("feishu_longconn_stop_timeout", "connection_id", key.connectionID)
	}
}

// release 释放租约：连接已断开 → STOPPED，需要处理 → FAILED，其余 → PENDING（让其他实例接手）。
func (m *LongConnManager) release(ctx context.Context, key sessionKey) {
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_connection_states st
			   SET lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp(),
			       state = CASE (SELECT c.status FROM openfox.integration_connections c
			                      WHERE c.organization_id = st.organization_id AND c.id = st.connection_id)
			                    WHEN 'DISCONNECTED' THEN 'STOPPED' WHEN 'NEEDS_ATTENTION' THEN 'FAILED' ELSE 'PENDING' END
			 WHERE st.organization_id = openfox_private.current_organization_id() AND st.connection_id = $1::uuid AND st.lease_owner = $2`,
			key.connectionID, m.instance())
		return err
	})
	if err != nil {
		m.logger().Warn("feishu_longconn_release_failed", "connection_id", key.connectionID, "error", err.Error())
	}
}

// renewAll 为本地会话续租；租约被抢走、连接不再有效或凭证变化时停止会话。返回是否有会话被停止。
func (m *LongConnManager) renewAll(ctx context.Context) bool {
	m.mu.Lock()
	keys := make([]sessionKey, 0, len(m.sessions))
	for key := range m.sessions {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	stopped := false
	for _, key := range keys {
		session, ok := m.local(key)
		if !ok {
			continue
		}
		res, err := m.acquire(ctx, key, false)
		if err != nil {
			if ctx.Err() == nil {
				m.logger().Warn("feishu_longconn_renew_failed", "connection_id", key.connectionID, "error", err.Error())
			}
			continue
		}
		if !res.owned || res.status != StatusActive || res.fingerprint != session.fingerprint || res.state == "PENDING" {
			m.stopSession(key)
			stopped = true
		}
	}
	return stopped
}

// shutdown 关闭全部会话并释放租约（状态回到 PENDING，其他实例可立即接手）。
func (m *LongConnManager) shutdown() {
	m.mu.Lock()
	keys := make([]sessionKey, 0, len(m.sessions))
	for key := range m.sessions {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		m.stopSession(key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range keys {
		m.release(ctx, key)
	}
}

// ---------- 会话 ----------

func sanitizeLastError(err error) string {
	text := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	if r := []rune(text); len(r) > imLastErrorLimit {
		text = string(r[:imLastErrorLimit])
	}
	return text
}

func (m *LongConnManager) backoff(err error, failures int) time.Duration {
	var retry *feishu.RetryError
	if errors.As(err, &retry) && retry.Interval > 0 {
		wait := retry.Interval
		if retry.Nonce > 0 {
			wait += time.Duration(rand.Int63n(int64(retry.Nonce) + 1))
		}
		return wait
	}
	wait := m.retryBase()
	for i := 1; i < failures && wait < imMaxBackoff; i++ {
		wait *= 2
	}
	if wait > imMaxBackoff {
		wait = imMaxBackoff
	}
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(wait) * jitter)
}

// clientFor 返回服务商的长连接客户端：优先用 Service.IMClients，飞书兼容旧的 Client 字段。
func (m *LongConnManager) clientFor(provider string) *feishu.Client {
	if client := m.Service.IMClients[provider]; client != nil {
		return client
	}
	if provider == ProviderFeishu {
		return m.Client
	}
	return nil
}

func (m *LongConnManager) runSession(ctx context.Context, key sessionKey, provider, appID, secret, accountKind, botOpenID string) {
	if provider == ProviderDingTalk {
		m.runDingTalkSession(ctx, key, appID, secret, accountKind)
		return
	}
	if provider == ProviderWeCom {
		m.runWeComSession(ctx, key, appID, secret)
		return
	}
	failures := 0
	for {
		client := m.clientFor(provider)
		if client == nil {
			m.markPermanent(ctx, key, errIMClientMissing)
			return
		}
		err := client.Run(ctx, feishu.Config{AppID: appID, Secret: secret,
			OnConnected: func() {
				failures = 0
				m.markConnected(ctx, key)
			},
			Handler: m.handler(key, appID, accountKind, botOpenID),
		})
		if ctx.Err() != nil {
			return
		}
		if feishu.IsPermanent(err) {
			m.markPermanent(ctx, key, err)
			return
		}
		failures++
		m.markReconnecting(ctx, key, failures, err)
		timer := time.NewTimer(m.backoff(err, failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *LongConnManager) markConnected(ctx context.Context, key sessionKey) {
	m.updateState(ctx, key, `
		UPDATE openfox.im_connection_states
		   SET state = 'CONNECTED', last_connected_at = clock_timestamp(), failure_count = 0, last_error = '', updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND lease_owner = $2`)
}

func (m *LongConnManager) markReconnecting(ctx context.Context, key sessionKey, failures int, err error) {
	m.updateStateArgs(ctx, key, `
		UPDATE openfox.im_connection_states
		   SET state = 'RECONNECTING', failure_count = $3, last_error = $4, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND lease_owner = $2`,
		failures, sanitizeLastError(err))
	m.logger().Warn("feishu_longconn_reconnecting", "connection_id", key.connectionID, "failures", failures, "error", sanitizeLastError(err))
}

func (m *LongConnManager) updateState(ctx context.Context, key sessionKey, query string) {
	m.updateStateArgs(ctx, key, query)
}

func (m *LongConnManager) updateStateArgs(ctx context.Context, key sessionKey, query string, extra ...any) {
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		args := append([]any{key.connectionID, m.instance()}, extra...)
		_, err := tx.Exec(ctx, query, args...)
		return err
	})
	if err != nil && ctx.Err() == nil {
		m.logger().Warn("feishu_longconn_state_update_failed", "connection_id", key.connectionID, "error", err.Error())
	}
}

// markPermanent 处理不可自动恢复的错误：连接标为 NEEDS_ATTENTION，状态 FAILED，释放租约，会话不再重连。
func (m *LongConnManager) markPermanent(ctx context.Context, key sessionKey, err error) {
	reason := ReasonCredentialsInvalid
	if errors.Is(err, feishu.ErrConnectionLimit) {
		reason = ReasonConnectionLimit
	}
	text := sanitizeLastError(err)
	updateErr := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET status = 'NEEDS_ATTENTION', status_reason = $2, updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'`,
			key.connectionID, reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_connection_states
			   SET state = 'FAILED', last_error = $3, lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND lease_owner = $2`,
			key.connectionID, m.instance(), text)
		return err
	})
	if updateErr != nil && ctx.Err() == nil {
		m.logger().Warn("feishu_longconn_mark_failed", "connection_id", key.connectionID, "error", updateErr.Error())
		return
	}
	m.logger().Warn("feishu_longconn_needs_attention", "connection_id", key.connectionID, "reason", reason)
}

// ---------- 事件 ----------

type eventEnvelope struct {
	Header struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		AppID     string `json:"app_id"`
	} `json:"header"`
}

// handler 返回某个连接的事件处理函数：app_id 不符的丢弃；链接预览直接返回响应；其他事件按 event_id 去重后交给 Dispatch。
func (m *LongConnManager) handler(key sessionKey, appID, accountKind, botOpenID string) feishu.Handler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		var env eventEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return nil, nil
		}
		if env.Header.AppID != "" && !strings.EqualFold(env.Header.AppID, appID) {
			return nil, nil
		}
		m.touchEvent(ctx, key)
		if env.Header.EventType == "url.preview.get" {
			return m.preview(ctx, key, payload), nil
		}
		if env.Header.EventID == "" || env.Header.EventType == "" {
			return nil, nil
		}
		fresh, err := m.recordInbound(ctx, key, env.Header.EventID, env.Header.EventType)
		if err != nil {
			return nil, err
		}
		if !fresh {
			return nil, nil
		}
		// 机器人连接收到的消息只入队（3 秒内 ACK），由 IMWorker 解析发送人、读取群上下文并写入会话；组织应用连接仍只记录。
		if env.Header.EventType == "im.message.receive_v1" && (accountKind == AccountKindTaskAssistant || accountKind == AccountKindAgent) {
			if err := m.enqueueMessage(ctx, key, botOpenID, payload); err != nil {
				m.logger().Warn("feishu_message_enqueue_failed", "connection_id", key.connectionID, "error", sanitizeLastError(err))
			}
			return nil, nil
		}
		if m.Dispatch != nil {
			if err := m.Dispatch(ctx, key.organizationID, key.connectionID, env.Header.EventType, payload); err != nil {
				m.logger().Warn("feishu_event_dispatch_failed", "connection_id", key.connectionID, "event_type", env.Header.EventType, "error", err.Error())
			}
		}
		return nil, nil
	}
}

func (m *LongConnManager) recordInbound(ctx context.Context, key sessionKey, eventID, eventType string) (bool, error) {
	if len(eventID) > 128 {
		eventID = eventID[:128]
	}
	if len(eventType) > 128 {
		eventType = eventType[:128]
	}
	fresh := false
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO openfox.im_inbound_events (organization_id, connection_id, event_id, event_type)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3)
			ON CONFLICT DO NOTHING`, key.connectionID, eventID, eventType)
		fresh = tag.RowsAffected() == 1
		return err
	})
	return fresh, err
}

// touchEvent 更新 last_event_at，每个连接最多每 30 秒一次。
func (m *LongConnManager) touchEvent(ctx context.Context, key sessionKey) {
	now := time.Now()
	m.mu.Lock()
	if m.touchedAt == nil {
		m.touchedAt = map[sessionKey]time.Time{}
	}
	if last, ok := m.touchedAt[key]; ok && now.Sub(last) < imEventTouchInterval {
		m.mu.Unlock()
		return
	}
	m.touchedAt[key] = now
	m.mu.Unlock()
	m.updateState(ctx, key, `
		UPDATE openfox.im_connection_states SET last_event_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND lease_owner = $2`)
}

// preview 处理 url.preview.get。任何问题（开关关闭、链接不匹配、数据库错误）都返回 {}，不渲染预览。
func (m *LongConnManager) preview(ctx context.Context, key sessionKey, payload []byte) []byte {
	var event PreviewEvent
	if json.Unmarshal(payload, &event) != nil {
		return previewEmpty
	}
	orgInURL, displayID, ok := ParsePreviewURL(m.Service.PublicURL, event.Event.Context.URL)
	if !ok || orgInURL != strings.ToLower(key.organizationID) {
		return previewEmpty
	}
	var enabled bool
	var member *PreviewMember
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		enabled, member, err = FeishuPreviewLookup(ctx, tx, key.connectionID, event.Event.Operator.OpenID)
		return err
	})
	if err != nil {
		m.logger().Warn("feishu_preview_failed", "connection_id", key.connectionID, "error", err.Error())
		return previewEmpty
	}
	if !enabled {
		return previewEmpty
	}
	if member == nil {
		return previewResponse(previewGenericTitle)
	}
	title, readable := "", false
	err = m.inOrganizationAs(ctx, key.organizationID, member.UserID, member.AuthorizationVersion, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		title, readable, err = m.Service.FeishuPreviewTitle(ctx, tx, key.organizationID, *member, displayID)
		return err
	})
	if err != nil {
		m.logger().Warn("feishu_preview_failed", "connection_id", key.connectionID, "error", err.Error())
		return previewEmpty
	}
	if !readable {
		// 千人千面：无权限或不存在时不泄露任何标题。
		return previewResponse(previewGenericTitle)
	}
	return previewResponse(title)
}

// ---------- 保留期清理 ----------

func (m *LongConnManager) maybeCleanup(ctx context.Context) {
	if time.Since(m.cleanedAt) < imCleanupInterval {
		return
	}
	m.cleanedAt = time.Now()
	keys, err := m.listConnections(ctx)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key.organizationID] {
			continue
		}
		seen[key.organizationID] = true
		if err := m.Cleanup(ctx, key.organizationID); err != nil && ctx.Err() == nil {
			m.logger().Warn("feishu_cleanup_failed", "organization_id", key.organizationID, "error", err.Error())
		}
	}
}

// Cleanup 删除一个组织内过期的行：入站事件保留 7 天，已结束的出站项保留 30 天，每次最多各 1000 行。
func (m *LongConnManager) Cleanup(ctx context.Context, organizationID string) error {
	return m.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.im_inbound_events e
			 USING (SELECT connection_id, event_id FROM openfox.im_inbound_events
			         WHERE organization_id = openfox_private.current_organization_id()
			           AND received_at < clock_timestamp() - interval '`+imInboundRetention+`'
			         ORDER BY received_at LIMIT $1) old
			 WHERE e.organization_id = openfox_private.current_organization_id()
			   AND e.connection_id = old.connection_id AND e.event_id = old.event_id`, imCleanupBatch); err != nil {
			return err
		}
		// 已结束的飞书入站消息保留 7 天，回发项保留 30 天。
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.im_inbound_messages m
			 USING (SELECT id FROM openfox.im_inbound_messages
			         WHERE organization_id = openfox_private.current_organization_id()
			           AND finished_at IS NOT NULL AND finished_at < clock_timestamp() - interval '`+imInboundRetention+`'
			         ORDER BY finished_at LIMIT $1) old
			 WHERE m.organization_id = openfox_private.current_organization_id() AND m.id = old.id`, imCleanupBatch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.im_reply_outbox o
			 USING (SELECT id FROM openfox.im_reply_outbox
			         WHERE organization_id = openfox_private.current_organization_id()
			           AND finished_at IS NOT NULL AND finished_at < clock_timestamp() - interval '`+imOutboxRetention+`'
			         ORDER BY finished_at LIMIT $1) old
			 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = old.id`, imCleanupBatch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			DELETE FROM openfox.im_notification_outbox o
			 USING (SELECT id FROM openfox.im_notification_outbox
			         WHERE organization_id = openfox_private.current_organization_id()
			           AND finished_at IS NOT NULL AND finished_at < clock_timestamp() - interval '`+imOutboxRetention+`'
			         ORDER BY finished_at LIMIT $1) old
			 WHERE o.organization_id = openfox_private.current_organization_id() AND o.id = old.id`, imCleanupBatch)
		return err
	})
}

// ---------- 机器人收到的消息入队 ----------

type messageEvent struct {
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID  string `json:"open_id"`
				UnionID string `json:"union_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			ParentID    string `json:"parent_id"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
			Mentions    []struct {
				Key string `json:"key"`
				ID  struct {
					OpenID string `json:"open_id"`
				} `json:"id"`
				Name string `json:"name"`
			} `json:"mentions"`
		} `json:"message"`
	} `json:"event"`
}

func clip(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// enqueueMessage 把 im.message.receive_v1 写入 im_inbound_messages，什么也不多做：
// 非用户（机器人）发的消息丢弃；群聊只在 @ 了本机器人时处理；单聊都处理；同一消息 ON CONFLICT 去重。
func (m *LongConnManager) enqueueMessage(ctx context.Context, key sessionKey, botOpenID string, payload []byte) error {
	var event messageEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil
	}
	message, sender := event.Event.Message, event.Event.Sender
	if sender.SenderType != "user" || message.MessageID == "" || message.ChatID == "" {
		return nil
	}
	if message.ChatType != "p2p" && message.ChatType != "group" {
		return nil
	}
	mentions := make([]imMention, 0, len(message.Mentions))
	mentionedBot := false
	for _, mention := range message.Mentions {
		mentions = append(mentions, imMention{Key: mention.Key, OpenID: mention.ID.OpenID, Name: mention.Name})
		if botOpenID != "" && mention.ID.OpenID == botOpenID {
			mentionedBot = true
		}
	}
	if message.ChatType == "group" && !mentionedBot {
		return nil
	}
	text := parseMessageText(message.MessageType, message.Content, mentions, botOpenID)
	return m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var botID string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM openfox.im_bots
			 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND removed_at IS NULL`,
			key.connectionID).Scan(&botID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.im_inbound_messages (organization_id, bot_id, remote_message_id, chat_id, chat_type, sender_open_id,
			       sender_union_id, message_type, text, parent_remote_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT DO NOTHING`, botID, clip(message.MessageID, 128), clip(message.ChatID, 128), message.ChatType,
			clip(sender.SenderID.OpenID, 128), clip(sender.SenderID.UnionID, 128), clip(message.MessageType, 32), text, clip(message.ParentID, 128))
		return err
	})
}
