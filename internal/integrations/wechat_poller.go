package integrations

// -W：微信长轮询管理器。每个有效绑定一个 getupdates 循环；租约（wechat_bindings.lease_owner）保证同一绑定同一时刻
// 只在一个实例上轮询。轮询线程只做三件事：收消息 → 过滤 → 在一个事务里入队（去重）并推进游标；入站处理与回发由 WeChatWorker 完成。
// 日志里不出现消息内容、令牌与二维码标识。

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations/wechat"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const (
	wechatScanLimit    = 100
	wechatMaxBackoff   = 2 * time.Minute
	wechatStopWait     = 10 * time.Second
	wechatMinPollSpan  = 500 * time.Millisecond
	wechatMaxMsgsBatch = 100
)

type wechatKey struct{ organizationID, bindingID string }

type wechatSession struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// WeChatPoller 管理本实例持有租约的微信长轮询。
type WeChatPoller struct {
	Pool     SyncRunner
	Logger   *slog.Logger
	Instance string
	Service  Service

	// ScanInterval 扫描新绑定的间隔（默认 10 秒）；LeaseTTL 租约有效期（默认 90 秒，需大于单次长轮询）；RenewInterval 续租间隔（默认 20 秒）。
	ScanInterval  time.Duration
	LeaseTTL      time.Duration
	RenewInterval time.Duration
	// MaxSessions 是本实例最多同时持有的轮询循环数（默认 200）；超出的绑定留在队列里，等有空位或其他实例接手。
	MaxSessions int
	// RetryBase 是轮询失败后的退避起点（默认 2 秒）。
	RetryBase time.Duration

	mu       sync.Mutex
	sessions map[wechatKey]*wechatSession
}

func (p *WeChatPoller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func (p *WeChatPoller) instance() string {
	if p.Instance == "" {
		p.Instance = "wechat-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return p.Instance
}

func (p *WeChatPoller) scanInterval() time.Duration {
	return orDuration(p.ScanInterval, 10*time.Second)
}
func (p *WeChatPoller) leaseTTL() time.Duration { return orDuration(p.LeaseTTL, 90*time.Second) }
func (p *WeChatPoller) renewInterval() time.Duration {
	return orDuration(p.RenewInterval, 20*time.Second)
}
func (p *WeChatPoller) retryBase() time.Duration { return orDuration(p.RetryBase, 2*time.Second) }
func (p *WeChatPoller) maxSessions() int {
	if p.MaxSessions <= 0 {
		return 200
	}
	return p.MaxSessions
}

func (p *WeChatPoller) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return p.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// Run 循环扫描与续租，直到 ctx 取消；返回前停止全部循环并释放租约。
func (p *WeChatPoller) Run(ctx context.Context) error {
	if p.Pool == nil || p.Service.WeChat == nil || p.Service.Box == nil {
		return errors.New("integrations: wechat poller is not configured")
	}
	scan := time.NewTicker(p.scanInterval())
	renew := time.NewTicker(p.renewInterval())
	defer scan.Stop()
	defer renew.Stop()
	_ = p.Scan(ctx)
	for {
		select {
		case <-ctx.Done():
			p.shutdown()
			return nil
		case <-scan.C:
			if err := p.Scan(ctx); err != nil && ctx.Err() == nil {
				p.logger().Warn("wechat_scan_failed", "error", sanitizeLastError(err))
			}
		case <-renew.C:
			p.renewAll(ctx)
		}
	}
}

func (p *WeChatPoller) listBindings(ctx context.Context) ([]wechatKey, error) {
	var keys []wechatKey
	var afterOrg, afterBinding any
	for {
		var page []wechatKey
		err := p.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT organization_id::text, binding_id::text FROM openfox_private.list_wechat_bindings($1::uuid, $2::uuid, $3)`,
				afterOrg, afterBinding, wechatScanLimit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var key wechatKey
				if err := rows.Scan(&key.organizationID, &key.bindingID); err != nil {
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
		if len(page) < wechatScanLimit {
			return keys, nil
		}
		last := page[len(page)-1]
		afterOrg, afterBinding = last.organizationID, last.bindingID
	}
}

// wechatLease 是拿到租约时读到的绑定现状。
type wechatLease struct {
	owned       bool
	agentID     string
	userID      string
	baseURL     string
	token       string
	syncBuf     string
	timeoutMs   int
	ilinkUserID string
}

// acquire 拿到（或续上）绑定的租约，并读取轮询所需的信息。
func (p *WeChatPoller) acquire(ctx context.Context, key wechatKey, needSecret bool) (wechatLease, error) {
	var res wechatLease
	err := p.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var sealed []byte
		err := tx.QueryRow(ctx, `
			UPDATE openfox.wechat_bindings
			   SET lease_owner = $2, lease_expires_at = clock_timestamp() + make_interval(secs => $3::float8), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'
			   AND (lease_owner = $2 OR lease_owner = '' OR lease_expires_at IS NULL OR lease_expires_at < clock_timestamp())
			RETURNING agent_id::text, created_by::text, base_url, bot_token_sealed, sync_buf, poll_timeout_ms, ilink_user_id`,
			key.bindingID, p.instance(), p.leaseTTL().Seconds()).
			Scan(&res.agentID, &res.userID, &res.baseURL, &sealed, &res.syncBuf, &res.timeoutMs, &res.ilinkUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		res.owned = true
		if needSecret {
			plain, err := p.Service.open(sealed, wechatAAD(key.organizationID, key.bindingID))
			if err != nil {
				return err
			}
			res.token = string(plain)
		}
		return nil
	})
	return res, err
}

func (p *WeChatPoller) local(key wechatKey) (*wechatSession, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[key]
	return s, ok
}

func (p *WeChatPoller) reap() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, s := range p.sessions {
		select {
		case <-s.done:
			delete(p.sessions, key)
		default:
		}
	}
}

// Sessions 返回本实例当前的轮询循环数（测试与运维观察用）。
func (p *WeChatPoller) Sessions() int {
	p.reap()
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// Scan 执行一轮：列出有效绑定，对缺失的拿租约并启动循环（超过并发上限的留到下一轮），停止已消失的循环。
func (p *WeChatPoller) Scan(ctx context.Context) error {
	p.reap()
	keys, err := p.listBindings(ctx)
	if err != nil {
		return err
	}
	listed := make(map[wechatKey]bool, len(keys))
	for _, key := range keys {
		listed[key] = true
	}
	p.mu.Lock()
	var gone []wechatKey
	for key := range p.sessions {
		if !listed[key] {
			gone = append(gone, key)
		}
	}
	p.mu.Unlock()
	for _, key := range gone {
		p.stopSession(key)
		p.release(ctx, key)
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, running := p.local(key); running {
			continue
		}
		p.mu.Lock()
		full := len(p.sessions) >= p.maxSessions()
		p.mu.Unlock()
		if full {
			continue
		}
		res, err := p.acquire(ctx, key, true)
		if errors.Is(err, secrets.ErrDecrypt) {
			p.markDead(ctx, key, "credentials_undecryptable")
			continue
		}
		if err != nil {
			p.logger().Warn("wechat_lease_failed", "organization_id", key.organizationID, "binding_id", key.bindingID, "error", sanitizeLastError(err))
			continue
		}
		if !res.owned || res.token == "" {
			continue
		}
		p.startSession(ctx, key, res)
	}
	return nil
}

func (p *WeChatPoller) startSession(parent context.Context, key wechatKey, res wechatLease) {
	ctx, cancel := context.WithCancel(parent)
	s := &wechatSession{cancel: cancel, done: make(chan struct{})}
	p.mu.Lock()
	if p.sessions == nil {
		p.sessions = map[wechatKey]*wechatSession{}
	}
	p.sessions[key] = s
	p.mu.Unlock()
	go func() {
		defer close(s.done)
		defer cancel()
		p.runSession(ctx, key, res)
	}()
}

func (p *WeChatPoller) stopSession(key wechatKey) {
	p.mu.Lock()
	s, ok := p.sessions[key]
	if ok {
		delete(p.sessions, key)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(wechatStopWait):
		p.logger().Warn("wechat_stop_timeout", "binding_id", key.bindingID)
	}
}

// release 释放租约并标记未连接。
func (p *WeChatPoller) release(ctx context.Context, key wechatKey) {
	err := p.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_bindings SET lease_owner = '', lease_expires_at = NULL, connected = false, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2`, key.bindingID, p.instance())
		return err
	})
	if err != nil && ctx.Err() == nil {
		p.logger().Warn("wechat_release_failed", "binding_id", key.bindingID, "error", sanitizeLastError(err))
	}
}

// renewAll 续租；租约被别人拿走（或绑定已不是 ACTIVE）的循环随即停止。
func (p *WeChatPoller) renewAll(ctx context.Context) {
	p.mu.Lock()
	keys := make([]wechatKey, 0, len(p.sessions))
	for key := range p.sessions {
		keys = append(keys, key)
	}
	p.mu.Unlock()
	for _, key := range keys {
		res, err := p.acquire(ctx, key, false)
		if err != nil {
			continue // 网络 / 数据库抖动：租约还没过期，下一次再试
		}
		if !res.owned {
			p.stopSession(key)
		}
	}
}

// Stop 停止全部本地循环并释放租约（Run 退出时也会调用；测试与优雅停机用）。
func (p *WeChatPoller) Stop() { p.shutdown() }

func (p *WeChatPoller) shutdown() {
	p.mu.Lock()
	keys := make([]wechatKey, 0, len(p.sessions))
	for key := range p.sessions {
		keys = append(keys, key)
	}
	p.mu.Unlock()
	for _, key := range keys {
		p.stopSession(key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range keys {
		p.release(ctx, key)
	}
}

// markDead 把绑定标为 EXPIRED（微信登录已失效或凭据无法解密），停止轮询。页面提示「微信登录已失效，请重新扫码」。
func (p *WeChatPoller) markDead(ctx context.Context, key wechatKey, reason string) {
	err := p.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_bindings
			   SET status = 'EXPIRED', status_reason = $2, connected = false, lease_owner = '', lease_expires_at = NULL,
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'`, key.bindingID, reason)
		return err
	})
	if err != nil && ctx.Err() == nil {
		p.logger().Warn("wechat_mark_expired_failed", "binding_id", key.bindingID, "error", sanitizeLastError(err))
		return
	}
	p.logger().Info("wechat_binding_expired", "organization_id", key.organizationID, "binding_id", key.bindingID, "reason", reason)
}

func (p *WeChatPoller) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (p *WeChatPoller) backoff(failures int) time.Duration {
	wait := p.retryBase()
	for i := 1; i < failures && wait < wechatMaxBackoff; i++ {
		wait *= 2
	}
	if wait > wechatMaxBackoff {
		wait = wechatMaxBackoff
	}
	return time.Duration(float64(wait) * (0.8 + rand.Float64()*0.4))
}

// runSession 是单个绑定的长轮询循环。
func (p *WeChatPoller) runSession(ctx context.Context, key wechatKey, res wechatLease) {
	buf := res.syncBuf
	timeout := time.Duration(res.timeoutMs) * time.Millisecond
	failures := 0
	connected := false
	for ctx.Err() == nil {
		started := time.Now()
		updates, err := p.Service.WeChat.GetUpdates(ctx, res.baseURL, res.token, buf, timeout)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, wechat.ErrSessionExpired) {
			p.markDead(ctx, key, "session_expired")
			return
		}
		if err != nil {
			failures++
			if connected || failures == 1 {
				p.setConnected(ctx, key, false, err)
				connected = false
			}
			p.logger().Warn("wechat_poll_failed", "organization_id", key.organizationID, "binding_id", key.bindingID, "failures", failures, "error", wechatErrText(err))
			if !p.sleep(ctx, p.backoff(failures)) {
				return
			}
			continue
		}
		failures = 0
		if updates.TimeoutMs > 0 {
			timeout = time.Duration(updates.TimeoutMs) * time.Millisecond
		}
		if len(updates.Messages) > 0 || updates.Buf != buf || !connected {
			alive, err := p.commitUpdates(ctx, key, res, buf, updates)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// 没有落库就不能推进游标：下次用旧游标重新拉取，入站去重保证不重复。
				failures++
				p.logger().Warn("wechat_commit_failed", "organization_id", key.organizationID, "binding_id", key.bindingID, "error", sanitizeLastError(err))
				if !p.sleep(ctx, p.backoff(failures)) {
					return
				}
				continue
			}
			if !alive {
				return // 租约已丢失或绑定已被移除
			}
			buf = updates.Buf
			connected = true
		}
		// 服务端立即返回空结果时避免空转。
		if len(updates.Messages) == 0 && time.Since(started) < wechatMinPollSpan {
			if !p.sleep(ctx, time.Second) {
				return
			}
		}
	}
}

// wechatErrText 只给出错误类别，避免把请求地址（含二维码标识 / 令牌）写进日志。
func wechatErrText(err error) string {
	var api *wechat.APIError
	switch {
	case errors.As(err, &api):
		return api.Error()
	case errors.Is(err, wechat.ErrUnavailable):
		return "upstream unavailable"
	default:
		return "poll error"
	}
}

func (p *WeChatPoller) setConnected(ctx context.Context, key wechatKey, connected bool, cause error) {
	note := ""
	if cause != nil {
		note = wechatErrText(cause)
	}
	err := p.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_bindings SET connected = $3, last_error = $4, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND lease_owner = $2 AND status = 'ACTIVE'`,
			key.bindingID, p.instance(), connected, note)
		return err
	})
	if err != nil && ctx.Err() == nil {
		p.logger().Warn("wechat_state_update_failed", "binding_id", key.bindingID, "error", sanitizeLastError(err))
	}
}

// commitUpdates 在一个事务里：确认租约仍属于本实例 → 过滤并入队消息（去重）→ 推进游标。返回 alive=false 表示应退出循环。
func (p *WeChatPoller) commitUpdates(ctx context.Context, key wechatKey, res wechatLease, oldBuf string, updates wechat.Updates) (bool, error) {
	alive := true
	err := p.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var owner string
		err := tx.QueryRow(ctx, `
			SELECT lease_owner FROM openfox.wechat_bindings
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE' FOR UPDATE`, key.bindingID).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != p.instance()) {
			alive = false
			return nil
		}
		if err != nil {
			return err
		}
		contextToken := ""
		for i, msg := range updates.Messages {
			if i >= wechatMaxMsgsBatch {
				break
			}
			// 只接受用户发给 bot 的单聊消息，且发送人必须是绑定时记录的微信用户；其余一律丢弃（不记录内容）。
			if msg.MessageType != 1 || msg.GroupID != "" || msg.FromUserID != res.ilinkUserID {
				continue
			}
			remoteID := msg.MessageID.String()
			if remoteID == "" || len(remoteID) > 256 {
				continue
			}
			text, _ := msg.Text()
			text = limitRunes(text, 8000)
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.wechat_inbound (organization_id, binding_id, remote_message_id, text, context_token)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4)
				ON CONFLICT (organization_id, binding_id, remote_message_id) DO NOTHING`,
				key.bindingID, remoteID, text, clip(msg.ContextToken, 2048)); err != nil {
				return err
			}
			if msg.ContextToken != "" {
				contextToken = clip(msg.ContextToken, 2048)
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE openfox.wechat_bindings
			   SET sync_buf = $2, connected = true, last_error = '', updated_at = clock_timestamp(),
			       last_context_token = CASE WHEN $3 <> '' THEN $3 ELSE last_context_token END,
			       last_inbound_at = CASE WHEN $3 <> '' THEN clock_timestamp() ELSE last_inbound_at END,
			       poll_timeout_ms = CASE WHEN $4 BETWEEN 1000 AND 120000 THEN $4 ELSE poll_timeout_ms END
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			key.bindingID, limitSyncBuf(updates.Buf, oldBuf), contextToken, updates.TimeoutMs)
		return err
	})
	return alive, err
}

// limitSyncBuf 保证游标不超过列约束（超长时保留旧游标，宁可重复拉取也不写坏）。
func limitSyncBuf(next, old string) string {
	if len(next) > 16384 {
		return old
	}
	return next
}
