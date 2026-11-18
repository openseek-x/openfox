package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTokenRenewBefore 是会话到期前主动续期的提前量。
	DefaultTokenRenewBefore = time.Hour
	// 续期失败的退避：1s 起，×2，上限 5 分钟，±20% 抖动。
	tokenRetryBase   = time.Second
	tokenRetryMax    = 5 * time.Minute
	tokenRetryJitter = 0.2
)

var ErrTokenUnavailable = errors.New("daemon: device session token is unavailable")

// SessionIssuer 用私钥签挑战换取新的会话令牌（AuthClient.Session）。
type SessionIssuer interface {
	Session(ctx context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (DeviceSession, error)
}

// RenewingTokenProviderConfig 配置会话令牌续期。
type RenewingTokenProviderConfig struct {
	TokenFile  string
	DeviceID   string
	KeyID      string
	PrivateKey ed25519.PrivateKey
	Issuer     SessionIssuer
	// RenewBefore 为 0 时取 DefaultTokenRenewBefore；测试钩子可调大以提前续期。
	RenewBefore time.Duration
	Now         func() time.Time
	Rand        func() float64
	Logger      *slog.Logger
}

// RenewingTokenProvider 读取 token 文件中的会话令牌，并在到期前 RenewBefore（或调用方报告 401）时
// 用私钥签挑战换新令牌，原子替换 token 文件。并发请求合并为一次续期；失败按指数退避。
// 启动时不知道文件中令牌的到期时间，首次使用时先续期一次；续期失败则继续使用文件中的令牌。
type RenewingTokenProvider struct {
	config RenewingTokenProviderConfig

	mu          sync.Mutex
	token       string
	expiresAt   time.Time
	invalid     bool
	nextAttempt time.Time
	failures    int
	inflight    *renewCall
}

type renewCall struct {
	done  chan struct{}
	token string
	err   error
	// keyID / privateKey 是发起续期时的密钥快照：续期期间发生密钥切换时丢弃旧密钥换来的会话。
	keyID      string
	privateKey ed25519.PrivateKey
}

func NewRenewingTokenProvider(config RenewingTokenProviderConfig) (*RenewingTokenProvider, error) {
	if strings.TrimSpace(config.TokenFile) == "" || strings.TrimSpace(config.DeviceID) == "" || strings.TrimSpace(config.KeyID) == "" ||
		len(config.PrivateKey) != ed25519.PrivateKeySize || config.Issuer == nil {
		return nil, ErrRemoteConfiguration
	}
	if config.RenewBefore <= 0 {
		config.RenewBefore = DefaultTokenRenewBefore
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Rand == nil {
		config.Rand = rand.Float64
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &RenewingTokenProvider{config: config}, nil
}

// Token 返回当前可用的会话令牌，必要时先续期。可作为 TokenProvider 使用。
func (p *RenewingTokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.token == "" {
		if token, err := ReadTokenFile(p.config.TokenFile); err == nil {
			p.token = token
		}
	}
	now := p.config.Now()
	if !p.needsRenewal(now) {
		token := p.token
		p.mu.Unlock()
		return token, nil
	}
	if p.inflight == nil && now.Before(p.nextAttempt) {
		// 退避期内不发请求：仍可用的旧令牌继续使用。
		token, ok := p.usable(now)
		p.mu.Unlock()
		if !ok {
			return "", ErrTokenUnavailable
		}
		return token, nil
	}
	call := p.inflight
	if call == nil {
		call = &renewCall{done: make(chan struct{}), keyID: p.config.KeyID, privateKey: p.config.PrivateKey}
		p.inflight = call
		go p.renew(call)
	}
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-call.done:
	}
	if call.err == nil {
		return call.token, nil
	}
	p.mu.Lock()
	token, ok := p.usable(p.config.Now())
	p.mu.Unlock()
	if ok {
		return token, nil
	}
	return "", ErrTokenUnavailable
}

// Invalidate 报告 token 被中心以 401 拒绝：下次 Token 立即续期（不受退避限制）。
// 只有与当前令牌相同时才生效，避免旧请求的 401 让刚续期的令牌失效。
func (p *RenewingTokenProvider) Invalidate(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if token != "" && token == p.token {
		p.invalid = true
		p.nextAttempt = time.Time{}
	}
}

// SwitchKey 在密钥轮换把新密钥写入本地配置之后调用：此后续期改用新密钥，并立即使用新会话令牌。
// 令牌文件由调用方先行写入。
func (p *RenewingTokenProvider) SwitchKey(keyID string, privateKey ed25519.PrivateKey, session DeviceSession) {
	if p == nil || strings.TrimSpace(keyID) == "" || len(privateKey) != ed25519.PrivateKeySize {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.KeyID, p.config.PrivateKey = keyID, privateKey
	p.token, p.expiresAt, p.invalid = session.Token, session.ExpiresAt, false
	p.failures, p.nextAttempt = 0, time.Time{}
	// 在途的旧密钥续期结束时会发现密钥已变并丢弃结果（见 renew）。
	p.inflight = nil
}

// needsRenewal 在持有 mu 时调用。
func (p *RenewingTokenProvider) needsRenewal(now time.Time) bool {
	return p.token == "" || p.invalid || p.expiresAt.IsZero() || !now.Before(p.expiresAt.Add(-p.config.RenewBefore))
}

// usable 在持有 mu 时调用：令牌未被拒绝且未过期（到期时间未知时视为可用）。
func (p *RenewingTokenProvider) usable(now time.Time) (string, bool) {
	if p.token == "" || p.invalid || (!p.expiresAt.IsZero() && !now.Before(p.expiresAt)) {
		return "", false
	}
	return p.token, true
}

// renew 在独立协程执行，不随单个调用方的 ctx 取消，结果对所有等待者生效。
func (p *RenewingTokenProvider) renew(call *renewCall) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*deviceAuthTimeout)
	defer cancel()
	session, err := p.config.Issuer.Session(ctx, p.config.DeviceID, call.keyID, call.privateKey)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config.KeyID != call.keyID {
		// 续期期间已切换到新密钥（阶段 12e 轮换）：旧密钥的会话作废，等待者拿切换后的令牌。
		if p.inflight == call {
			p.inflight = nil
		}
		call.token, call.err = p.token, nil
		if p.token == "" {
			call.err = ErrTokenUnavailable
		}
		close(call.done)
		return
	}
	if err == nil {
		err = WritePrivateFile(p.config.TokenFile, []byte(session.Token+"\n"))
	}
	p.inflight = nil
	if err != nil {
		p.failures++
		delay := retryDelay(p.failures, p.config.Rand())
		p.nextAttempt = p.config.Now().Add(delay)
		// 错误只含状态码 / 错误码或网络错误，不含令牌与签名。
		p.config.Logger.Warn("daemon_token_renew_failed", "error", err.Error(), "retry_in", delay.String())
		call.err = err
		close(call.done)
		return
	}
	p.token, p.expiresAt, p.invalid = session.Token, session.ExpiresAt, false
	p.failures, p.nextAttempt = 0, time.Time{}
	p.config.Logger.Info("daemon_token_renewed", "expires_at", session.ExpiresAt.UTC().Format(time.RFC3339))
	call.token = session.Token
	close(call.done)
}

// retryDelay 返回第 failures 次失败后的等待：1s×2^(n-1)，上限 5 分钟，再乘以 1±20%。
func retryDelay(failures int, random float64) time.Duration {
	delay := tokenRetryBase
	for i := 1; i < failures && delay < tokenRetryMax; i++ {
		delay *= 2
	}
	if delay > tokenRetryMax {
		delay = tokenRetryMax
	}
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	factor := 1 + tokenRetryJitter*(2*random-1)
	return time.Duration(float64(delay) * factor)
}
