package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	ErrEmailLoginNotConfigured = errors.New("identity: email login is not configured")
	ErrEmailCodeInvalid        = errors.New("identity: email code is invalid")
	ErrEmailDelivery           = errors.New("identity: email code delivery failed")
	ErrInvalidEmail            = errors.New("identity: email address is invalid")
)

type EmailCodeCooldownError struct {
	RetryAfter time.Duration
}

func (e EmailCodeCooldownError) Error() string { return "identity: email code resend is throttled" }

// EmailCodeLimitError 表示该邮箱在滚动窗口内的发信或失败次数已达上限。
// 与冷却不同，它防的是「换验证码继续猜」的长期暴力破解与对单一邮箱的发信轰炸。
type EmailCodeLimitError struct {
	RetryAfter time.Duration
}

func (e EmailCodeLimitError) Error() string { return "identity: email code daily limit reached" }

type EmailOTPConfig struct {
	ChallengeTTL   time.Duration
	ResendCooldown time.Duration
	MaxAttempts    int
	UniversalCode  string
	// DailySends / DailyFailures 是同一邮箱在 LimitWindow 内允许的签发次数与累计校验失败次数。
	DailySends    int
	DailyFailures int
	LimitWindow   time.Duration
}

// EmailLimits 是传给持久层、在按邮箱加锁的事务内执行的限额。
type EmailLimits struct {
	Cooldown      time.Duration
	DailySends    int
	DailyFailures int
	Window        time.Duration
}

type EmailLoginChallenge struct {
	ID           string
	Email        string
	CodeHash     []byte
	CodeSalt     []byte
	ExpiresAt    time.Time
	SentAt       time.Time
	AttemptCount int
	MaxAttempts  int
}

type EmailCodeRequestResult struct {
	ExpiresAt time.Time
}

func (s Service) RequestEmailCode(ctx context.Context, email string) (EmailCodeRequestResult, error) {
	normalized := normalizeEmail(email)
	if !validEmail(normalized) {
		return EmailCodeRequestResult{}, ErrInvalidEmail
	}
	ttl, cooldown, maxAttempts := s.emailOTPDefaults()
	if s.Store == nil || s.EmailStore == nil || s.Now == nil || ttl <= 0 || cooldown < 0 || maxAttempts < 1 {
		return EmailCodeRequestResult{}, ErrEmailLoginNotConfigured
	}
	now := s.Now().UTC()

	// 验证码登录即注册：未注册邮箱同样签发验证码，账号在验证通过后才创建。
	// 已停用账号返回与正常请求相同的响应且不发信，避免暴露账号状态。
	credentials, err := s.Store.FindCredentials(ctx, normalized)
	if err != nil && !errors.Is(err, ErrCredentialNotFound) {
		return EmailCodeRequestResult{}, err
	}
	if err == nil && (credentials.Status != "ACTIVE" || credentials.CredentialVersion <= 0) {
		return EmailCodeRequestResult{ExpiresAt: now.Add(ttl)}, nil
	}
	if s.EmailOTP.UniversalCode == "" && s.EmailSender == nil {
		return EmailCodeRequestResult{}, ErrEmailLoginNotConfigured
	}

	code, err := s.newEmailCode()
	if err != nil {
		return EmailCodeRequestResult{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return EmailCodeRequestResult{}, err
	}
	expiresAt := now.Add(ttl)
	challenge := EmailLoginChallenge{
		Email:       normalized,
		CodeHash:    hashEmailCode(salt, normalized, code),
		CodeSalt:    salt,
		ExpiresAt:   expiresAt,
		SentAt:      now,
		MaxAttempts: maxAttempts,
	}
	challengeID, err := s.EmailStore.IssueEmailLoginChallenge(ctx, challenge, now, s.emailLimits(cooldown))
	if err != nil {
		return EmailCodeRequestResult{}, err
	}
	if s.EmailOTP.UniversalCode == "" {
		body := fmt.Sprintf("你的 OpenFox 登录验证码是 %s。验证码将在 %s 后失效。", code, formatDuration(ttl))
		if err := s.EmailSender.Send(ctx, normalized, "OpenFox 登录验证码", body); err != nil {
			_ = s.EmailStore.CancelEmailLoginChallenge(ctx, challengeID, now)
			return EmailCodeRequestResult{}, fmt.Errorf("%w: %v", ErrEmailDelivery, err)
		}
	}
	return EmailCodeRequestResult{ExpiresAt: expiresAt}, nil
}

func (s Service) VerifyEmailCode(ctx context.Context, email, code string, clientKind ClientKind) (LoginResult, error) {
	normalized := normalizeEmail(email)
	if !validEmail(normalized) || !validEmailCode(code) || s.Store == nil || s.EmailStore == nil || s.Now == nil {
		return LoginResult{}, ErrEmailCodeInvalid
	}
	now := s.Now().UTC()
	_, cooldown, _ := s.emailOTPDefaults()
	credentials, err := s.EmailStore.ConsumeEmailLoginChallenge(ctx, normalized, now, s.emailLimits(cooldown), func(challenge EmailLoginChallenge) bool {
		if s.EmailOTP.UniversalCode != "" && subtle.ConstantTimeCompare([]byte(code), []byte(s.EmailOTP.UniversalCode)) == 1 {
			return true
		}
		candidate := hashEmailCode(challenge.CodeSalt, normalized, code)
		return subtle.ConstantTimeCompare(candidate, challenge.CodeHash) == 1
	})
	if err != nil {
		var limited EmailCodeLimitError
		if errors.Is(err, ErrEmailCodeInvalid) || errors.As(err, &limited) {
			return LoginResult{}, err
		}
		return LoginResult{}, err
	}
	if credentials.Status != "ACTIVE" || credentials.CredentialVersion <= 0 {
		return LoginResult{}, ErrEmailCodeInvalid
	}
	result, err := s.createSession(ctx, credentials.UserID, credentials.CredentialVersion, clientKind)
	if err != nil {
		return LoginResult{}, err
	}
	result.NewUser = credentials.Created
	return result, nil
}

func (s Service) emailLimits(cooldown time.Duration) EmailLimits {
	sends, failures, window := s.EmailOTP.DailySends, s.EmailOTP.DailyFailures, s.EmailOTP.LimitWindow
	if sends == 0 {
		sends = 10
	}
	if failures == 0 {
		failures = 20
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	return EmailLimits{Cooldown: cooldown, DailySends: sends, DailyFailures: failures, Window: window}
}

func (s Service) emailOTPDefaults() (time.Duration, time.Duration, int) {
	ttl := s.EmailOTP.ChallengeTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	cooldown := s.EmailOTP.ResendCooldown
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	maxAttempts := s.EmailOTP.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	return ttl, cooldown, maxAttempts
}

func (s Service) newEmailCode() (string, error) {
	if s.EmailOTP.UniversalCode != "" {
		return s.EmailOTP.UniversalCode, nil
	}
	value, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func validEmail(value string) bool {
	return len(value) <= 320 && strings.Count(value, "@") == 1 && !strings.ContainsAny(value, " \t\r\n")
}

func validEmailCode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func hashEmailCode(salt []byte, email, code string) []byte {
	hash := sha256.New()
	_, _ = hash.Write(salt)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(email))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(code))
	return hash.Sum(nil)
}

func formatDuration(value time.Duration) string {
	if value%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(value/time.Minute))
	}
	return value.Round(time.Second).String()
}
