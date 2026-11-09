package integrations

// -W：微信（腾讯 ClawBot / iLink）扫码绑定 Agent 的服务端逻辑：扫码会话、绑定列表与解绑。
// 对 iLink 的请求都在数据库事务之外：先短事务校验，再调用 iLink，最后短事务保存。
// 轮询收消息、入站处理与回发见 wechat_worker.go。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/wechat"
	"openfox/internal/membership"
)

const (
	ProviderWeChat = "wechat"

	// 每个用户最多的有效绑定数。
	maxWeChatBindingsPerUser = 20
	// 扫码会话有效期（上限 5 分钟）。
	wechatQRTTL = 5 * time.Minute

	WeChatActive  = "ACTIVE"
	WeChatExpired = "EXPIRED"
	WeChatRemoved = "REMOVED"

	QRPending    = "PENDING"
	QRScannedS   = "SCANNED"
	QRNeedVerify = "NEED_VERIFY"
	QRConfirmedS = "CONFIRMED"
	QRExpiredS   = "EXPIRED"
	QRBlocked    = "BLOCKED"
	QRFailed     = "FAILED"
	QRCancelled  = "CANCELLED"
)

var (
	ErrWeChatLimit       = errors.New("integrations: too many wechat bindings")
	ErrWeChatUnavailable = errors.New("integrations: wechat is unavailable")
)

// WeChatQR 对应契约 WeChatQRSession。qrcode 不对外返回。
type WeChatQR struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	Status    string     `json:"status"`
	QRContent string     `json:"qr_content"`
	ErrorCode string     `json:"error_code"`
	BindingID *string    `json:"binding_id"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	Finished  *time.Time `json:"finished_at"`
}

type WeChatRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WeChatBinding 对应契约 WeChatBinding。微信标识脱敏，不含任何凭据。
type WeChatBinding struct {
	ID           string    `json:"id"`
	Agent        WeChatRef `json:"agent"`
	WeChatUser   string    `json:"wechat_user"`
	Initiator    WeChatRef `json:"initiator"`
	Status       string    `json:"status"`
	StatusReason string    `json:"status_reason"`
	Connected    bool      `json:"connected"`
	CanManage    bool      `json:"can_manage"`
	CreatedAt    time.Time `json:"created_at"`
	Version      uint64    `json:"version"`
}

// WeChatView 对应契约 WeChatView。
type WeChatView struct {
	Configured  bool            `json:"configured"`
	CanBind     bool            `json:"can_bind"`
	MaxBindings int             `json:"max_bindings"`
	Bindings    []WeChatBinding `json:"bindings"`
}

func wechatAAD(organizationID, bindingID string) string {
	return "wechat:" + organizationID + ":" + bindingID
}

// maskWeChatID 脱敏微信标识：保留前 2 位与后 4 位。
func maskWeChatID(id string) string {
	r := []rune(id)
	if len(r) <= 8 {
		return string(r[:min(1, len(r))]) + "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-4:])
}

// WeChatConfigured 判断服务端是否具备微信绑定所需的客户端与加密密钥。
func (s Service) WeChatConfigured() bool { return s.WeChat != nil && s.Box != nil }

const wechatBindingSelect = `
	SELECT b.id::text, b.agent_id::text, a.name, b.ilink_user_id, b.created_by::text, COALESCE(u.email, ''),
	       b.status, b.status_reason, b.connected AND b.status = 'ACTIVE', b.created_at, b.version
	  FROM openfox.wechat_bindings b
	  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
	  LEFT JOIN openfox.users u ON u.id = b.created_by
	 WHERE b.organization_id = openfox_private.current_organization_id() AND b.status <> 'REMOVED'`

func scanWeChatBinding(row pgx.Row, actor collaboration.Actor) (WeChatBinding, error) {
	var b WeChatBinding
	var wechatUser string
	var version int64
	if err := row.Scan(&b.ID, &b.Agent.ID, &b.Agent.Name, &wechatUser, &b.Initiator.ID, &b.Initiator.Name,
		&b.Status, &b.StatusReason, &b.Connected, &b.CreatedAt, &version); err != nil {
		return b, err
	}
	b.WeChatUser = maskWeChatID(wechatUser)
	b.Version = uint64(version)
	b.CanManage = isAdminRole(actor.Role) || b.Initiator.ID == actor.UserID
	return b, nil
}

// WeChatView 返回绑定列表：管理员看到全部，其他成员只看到自己的。
func (s Service) WeChatView(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (WeChatView, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return WeChatView{}, err
	}
	view := WeChatView{Configured: s.WeChatConfigured(), CanBind: s.WeChatConfigured(), MaxBindings: maxWeChatBindingsPerUser, Bindings: []WeChatBinding{}}
	rows, err := tx.Query(ctx, wechatBindingSelect+` AND ($1 OR b.created_by = $2::uuid) ORDER BY b.created_at DESC LIMIT 500`,
		isAdminRole(actor.Role), actor.UserID)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanWeChatBinding(rows, actor)
		if err != nil {
			return view, err
		}
		view.Bindings = append(view.Bindings, b)
	}
	return view, rows.Err()
}

// ---------- 扫码会话 ----------

// WeChatQRPrepare 校验权限与上限，不访问 iLink。权限规则与飞书绑定 Agents 相同（Agent 所有者或组织管理员）。
func (s Service) WeChatQRPrepare(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, agentID string) error {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if s.Box == nil {
		return ErrSecretsUnavailable
	}
	if s.WeChat == nil {
		return ErrNotConfigured
	}
	if !membership.ValidateUUID(agentID) {
		return ErrInvalidRequest
	}
	if err := agentBindCheck(ctx, tx, actor, agentID); err != nil {
		return err
	}
	return checkWeChatLimit(ctx, tx, actor.UserID)
}

func checkWeChatLimit(ctx context.Context, tx pgx.Tx, userID string) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.wechat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND created_by = $1::uuid AND status <> 'REMOVED'`, userID).Scan(&count); err != nil {
		return err
	}
	if count >= maxWeChatBindingsPerUser {
		return ErrWeChatLimit
	}
	return nil
}

// WeChatQRStart 先短事务校验，再向 iLink 申请二维码，最后保存扫码会话。
func (s Service) WeChatQRStart(ctx context.Context, run TxRunner, organizationID string, actor collaboration.Actor, agentID string) (WeChatQR, error) {
	if err := run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return s.WeChatQRPrepare(ctx, tx, organizationID, actor, agentID)
	}); err != nil {
		return WeChatQR{}, err
	}
	qr, err := s.WeChat.GetBotQRCode(ctx)
	if err != nil {
		return WeChatQR{}, ErrWeChatUnavailable
	}
	var out WeChatQR
	err = run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// 同一发起人对同一 Agent 同时只保留一个进行中的会话。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.wechat_qr_sessions SET status = 'CANCELLED', finished_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND requested_by = $1::uuid AND agent_id = $2::uuid
			   AND status IN ('PENDING', 'SCANNED', 'NEED_VERIFY')`, actor.UserID, agentID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO openfox.wechat_qr_sessions (organization_id, agent_id, requested_by, qrcode, qr_content, poll_base_url, expires_at)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, clock_timestamp() + make_interval(secs => $6::float8))
			RETURNING `+wechatQRColumns, agentID, actor.UserID, qr.Code, qr.Content, s.WeChat.BaseURL, wechatQRTTL.Seconds())
		var err error
		out, err = scanWeChatQR(row)
		return err
	})
	return out, err
}

const wechatQRColumns = `id::text, agent_id::text, status, qr_content, error_code, binding_id::text, expires_at, created_at, finished_at`

func scanWeChatQR(row pgx.Row) (WeChatQR, error) {
	var q WeChatQR
	err := row.Scan(&q.ID, &q.AgentID, &q.Status, &q.QRContent, &q.ErrorCode, &q.BindingID, &q.ExpiresAt, &q.CreatedAt, &q.Finished)
	return q, err
}

// TxRunner 在（发起人身份的）租户事务里执行 fn。
type TxRunner func(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error

func isTerminalQR(status string) bool {
	switch status {
	case QRPending, QRScannedS, QRNeedVerify:
		return false
	}
	return true
}

type qrWork struct {
	qrcode, base string
}

// loadQR 按发起人读取会话；已过期的进行中会话在这里置为 EXPIRED。返回 work 非空表示需要向 iLink 查询状态。
func loadQR(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, id string) (WeChatQR, *qrWork, error) {
	if !membership.ValidateUUID(id) {
		return WeChatQR{}, nil, ErrNotFound
	}
	var work qrWork
	var expired bool
	row := tx.QueryRow(ctx, `
		SELECT `+wechatQRColumns+`, qrcode, poll_base_url, expires_at <= clock_timestamp()
		  FROM openfox.wechat_qr_sessions
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND requested_by = $2::uuid`, id, actor.UserID)
	var q WeChatQR
	err := row.Scan(&q.ID, &q.AgentID, &q.Status, &q.QRContent, &q.ErrorCode, &q.BindingID, &q.ExpiresAt, &q.CreatedAt, &q.Finished,
		&work.qrcode, &work.base, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return WeChatQR{}, nil, ErrNotFound
	}
	if err != nil {
		return WeChatQR{}, nil, err
	}
	if isTerminalQR(q.Status) {
		return q, nil, nil
	}
	if expired {
		q, err = scanWeChatQR(tx.QueryRow(ctx, `
			UPDATE openfox.wechat_qr_sessions SET status = 'EXPIRED', finished_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING `+wechatQRColumns, id))
		return q, nil, err
	}
	return q, &work, nil
}

// WeChatQRGet 返回扫码会话（只允许发起人）。不访问 iLink，用于只读。
func (s Service) WeChatQRGet(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, id string) (WeChatQR, error) {
	q, _, err := loadQR(ctx, tx, actor, id)
	return q, err
}

// WeChatQRCancel 取消进行中的会话。
func (s Service) WeChatQRCancel(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, id string) (WeChatQR, error) {
	q, _, err := loadQR(ctx, tx, actor, id)
	if err != nil || isTerminalQR(q.Status) {
		return q, err
	}
	return scanWeChatQR(tx.QueryRow(ctx, `
		UPDATE openfox.wechat_qr_sessions SET status = 'CANCELLED', finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING `+wechatQRColumns, id))
}

// WeChatQRAdvance 向 iLink 查询一次扫码状态（可带验证码）并推进会话。confirmed 时在同一事务里幂等建立绑定。
// iLink 的状态查询是服务端长轮询：单次最多 wechat.StatusTimeout，超时按「仍在等待」处理。
//
// record 在保存状态的同一事务里写审计（绑定建立时）；可以为 nil。
func (s Service) WeChatQRAdvance(ctx context.Context, run TxRunner, organizationID string, actor collaboration.Actor, id, verifyCode string,
	record func(ctx context.Context, tx pgx.Tx, entries []AuditEntry) error) (WeChatQR, error) {
	if s.WeChat == nil {
		return WeChatQR{}, ErrNotConfigured
	}
	var q WeChatQR
	var work *qrWork
	if err := run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		q, work, err = loadQR(ctx, tx, actor, id)
		return err
	}); err != nil {
		return WeChatQR{}, err
	}
	if work == nil {
		return q, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, wechat.StatusTimeout)
	status, err := s.WeChat.GetQRCodeStatus(callCtx, work.base, work.qrcode, verifyCode)
	cancel()
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return q, nil
	case err != nil && ctx.Err() != nil:
		return WeChatQR{}, ctx.Err()
	case err != nil:
		return WeChatQR{}, ErrWeChatUnavailable
	}
	err = run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var audits []AuditEntry
		var err error
		q, audits, err = s.applyQRStatus(ctx, tx, organizationID, actor, id, status)
		if err != nil || record == nil || len(audits) == 0 {
			return err
		}
		return record(ctx, tx, audits)
	})
	return q, err
}

func (s Service) applyQRStatus(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string,
	status wechat.QRStatus) (WeChatQR, []AuditEntry, error) {
	var current, agentID string
	if err := tx.QueryRow(ctx, `
		SELECT status, agent_id::text FROM openfox.wechat_qr_sessions
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND requested_by = $2::uuid FOR UPDATE`,
		id, actor.UserID).Scan(&current, &agentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WeChatQR{}, nil, ErrNotFound
		}
		return WeChatQR{}, nil, err
	}
	reread := func() (WeChatQR, error) {
		return scanWeChatQR(tx.QueryRow(ctx, `SELECT `+wechatQRColumns+` FROM openfox.wechat_qr_sessions
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id))
	}
	// 并发轮询：别的请求已经推进到终态（例如已确认并建好绑定）时，丢弃本次拿到的结果（含令牌）。
	if isTerminalQR(current) {
		q, err := reread()
		return q, nil, err
	}
	set := func(next, code string, finished bool, extra string) (WeChatQR, []AuditEntry, error) {
		query := `UPDATE openfox.wechat_qr_sessions SET status = $2, error_code = $3, updated_at = clock_timestamp()` + extra
		if finished {
			query += `, finished_at = clock_timestamp()`
		}
		query += ` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`
		if _, err := tx.Exec(ctx, query, id, next, code); err != nil {
			return WeChatQR{}, nil, err
		}
		q, err := reread()
		return q, nil, err
	}
	switch status.Status {
	case wechat.QRWait:
		return set(QRPending, "", false, "")
	case wechat.QRScanned:
		return set(QRScannedS, "", false, "")
	case wechat.QRNeedVerifyCode:
		return set(QRNeedVerify, "", false, "")
	case wechat.QRVerifyCodeBlocked:
		return set(QRBlocked, "verify_code_blocked", true, "")
	case wechat.QRExpired:
		return set(QRExpiredS, "", true, "")
	case wechat.QRBindedRedirect:
		return set(QRFailed, "already_bound", true, "")
	case wechat.QRScanedButRedirect:
		base, err := s.WeChat.NormalizeBase(status.RedirectHost)
		if err != nil {
			return set(QRFailed, "untrusted_host", true, "")
		}
		if _, err := tx.Exec(ctx, `UPDATE openfox.wechat_qr_sessions SET poll_base_url = $2
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, base); err != nil {
			return WeChatQR{}, nil, err
		}
		return set(QRScannedS, "", false, "")
	case wechat.QRConfirmed:
		return s.confirmQR(ctx, tx, organizationID, actor, id, agentID, status, set)
	}
	// 未知状态：保持原状，等待下一次轮询。
	q, err := reread()
	return q, nil, err
}

type qrSetter func(next, code string, finished bool, extra string) (WeChatQR, []AuditEntry, error)

// confirmQR 在确认时建立绑定：校验服务端返回的基址（SSRF 白名单）、再次校验发起人对 Agent 的权限与上限，
// 同一发起人同一 Agent 的旧绑定标为 REMOVED。唯一约束冲突（ilink_bot_id 已被绑定）时会话置为 FAILED。
func (s Service) confirmQR(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id, agentID string,
	status wechat.QRStatus, set qrSetter) (WeChatQR, []AuditEntry, error) {
	if status.BotToken == "" || status.BotID == "" || status.UserID == "" {
		return set(QRFailed, "invalid_response", true, "")
	}
	var sessionBase string
	if err := tx.QueryRow(ctx, `SELECT poll_base_url FROM openfox.wechat_qr_sessions
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id).Scan(&sessionBase); err != nil {
		return WeChatQR{}, nil, err
	}
	base := sessionBase
	if strings.TrimSpace(status.BaseURL) != "" {
		normalized, err := s.WeChat.NormalizeBase(status.BaseURL)
		if err != nil {
			return set(QRFailed, "untrusted_host", true, "")
		}
		base = normalized
	}
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return set(QRFailed, "forbidden", true, "")
	}
	if err := agentBindCheck(ctx, tx, actor, agentID); err != nil {
		return set(QRFailed, "forbidden", true, "")
	}
	bindingID, err := membership.NewID()
	if err != nil {
		return WeChatQR{}, nil, err
	}
	blob, keyID, err := s.seal([]byte(status.BotToken), wechatAAD(organizationID, bindingID))
	if err != nil {
		return WeChatQR{}, nil, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return WeChatQR{}, nil, err
	}
	// 旧绑定（同一发起人、同一 Agent）被取代：先标记 REMOVED 并撤销令牌，再插入新绑定（部分唯一索引要求先后顺序）。
	if _, err := sp.Exec(ctx, `
		UPDATE openfox.wechat_bindings
		   SET status = 'REMOVED', removed_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1,
		       bot_token_sealed = '\x'::bytea, lease_owner = '', lease_expires_at = NULL, connected = false, status_reason = 'replaced'
		 WHERE organization_id = openfox_private.current_organization_id() AND created_by = $1::uuid AND agent_id = $2::uuid AND status <> 'REMOVED'`,
		actor.UserID, agentID); err != nil {
		_ = sp.Rollback(ctx)
		return WeChatQR{}, nil, err
	}
	if err := checkWeChatLimit(ctx, sp, actor.UserID); err != nil {
		_ = sp.Rollback(ctx)
		if errors.Is(err, ErrWeChatLimit) {
			return set(QRFailed, "limit_reached", true, "")
		}
		return WeChatQR{}, nil, err
	}
	if _, err := sp.Exec(ctx, `
		INSERT INTO openfox.wechat_bindings (organization_id, id, agent_id, created_by, ilink_bot_id, ilink_user_id,
		       bot_token_sealed, bot_token_key_id, base_url)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)`,
		bindingID, agentID, actor.UserID, status.BotID, status.UserID, blob, keyID, base); err != nil {
		_ = sp.Rollback(ctx)
		if isUniqueViolation(err) {
			return set(QRFailed, "bot_in_use", true, "")
		}
		return WeChatQR{}, nil, err
	}
	if err := sp.Commit(ctx); err != nil {
		return WeChatQR{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.wechat_qr_sessions SET binding_id = $2::uuid
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, bindingID); err != nil {
		return WeChatQR{}, nil, err
	}
	q, _, err := set(QRConfirmedS, "", true, "")
	if err != nil {
		return WeChatQR{}, nil, err
	}
	return q, []AuditEntry{{Action: "integration.connection_created", ResourceID: bindingID,
		Metadata: map[string]string{"source": ProviderWeChat, "result": "agent"}}}, nil
}

// WeChatRemove 解绑：发起人本人或组织管理员。令牌随即作废，轮询在下一次租约续期时停止。
func (s Service) WeChatRemove(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, version uint64) ([]AuditEntry, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(id) {
		return nil, ErrNotFound
	}
	var owner string
	var current int64
	err := tx.QueryRow(ctx, `
		SELECT created_by::text, version FROM openfox.wechat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status <> 'REMOVED' FOR UPDATE`, id).Scan(&owner, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if owner != actor.UserID && !isAdminRole(actor.Role) {
		// 其他成员的绑定对非管理员不可见。
		return nil, ErrNotFound
	}
	if uint64(current) != version {
		return nil, ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.wechat_bindings
		   SET status = 'REMOVED', removed_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1,
		       bot_token_sealed = '\x'::bytea, lease_owner = '', lease_expires_at = NULL, connected = false, status_reason = 'removed'
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
		return nil, err
	}
	return []AuditEntry{{Action: "integration.connection_disconnected", ResourceID: id,
		Metadata: map[string]string{"source": ProviderWeChat, "result": "agent"}}}, nil
}
