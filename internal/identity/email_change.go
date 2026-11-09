package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"
)

const (
	emailStageCurrent = "current"
	emailStageNew     = "new"
)

var (
	ErrEmailTaken     = errors.New("identity: email address is already in use")
	ErrEmailUnchanged = errors.New("identity: new email address matches the current one")
)

// EmailChangeChallenge 是修改邮箱的一条验证码记录，只保存加盐哈希。
// stage = current 是发到当前邮箱的第 1 步；stage = new 是发到新邮箱的第 3 步，ParentID 指向已校验的第 1 步。
type EmailChangeChallenge struct {
	ID           string
	UserID       string
	Stage        string
	ParentID     string
	Email        string
	CodeHash     []byte
	CodeSalt     []byte
	SentAt       time.Time
	ExpiresAt    time.Time
	AttemptCount int
	MaxAttempts  int
	VerifiedAt   *time.Time
	ConsumedAt   *time.Time
}

// EmailChallenge 是返回给客户端的挑战信息。
type EmailChallenge struct {
	ID          string
	ExpiresAt   time.Time
	ResendAfter time.Time
}

// EmailChangeStore 是可选的修改邮箱能力。所有方法都按已认证的用户 ID 过滤。
type EmailChangeStore interface {
	// IssueCurrentEmailChallenge 签发第 1 步验证码，作废该用户之前未消费的第 1 步验证码。
	IssueCurrentEmailChallenge(context.Context, string, EmailChangeChallenge, time.Time, EmailLimits) (string, error)
	// IssueNewEmailChallenge 签发第 3 步验证码；父验证码必须已校验且在 parentTTL 内，新邮箱不能被占用。
	IssueNewEmailChallenge(context.Context, string, EmailChangeChallenge, time.Time, time.Duration, EmailLimits) (string, error)
	// VerifyCurrentEmailChallenge 校验第 2 步验证码。错误时的尝试计数会提交。
	VerifyCurrentEmailChallenge(context.Context, string, string, time.Time, EmailLimits, func(EmailChangeChallenge) bool) error
	// CompleteEmailChange 校验第 4 步验证码并改邮箱，同一事务内吊销除当前会话外的其他会话。
	CompleteEmailChange(context.Context, string, string, string, time.Time, time.Duration, EmailLimits, func(EmailChangeChallenge) bool) (User, error)
	// CancelEmailChangeChallenge 作废发信失败的验证码。
	CancelEmailChangeChallenge(context.Context, string, time.Time) error
}

// StartEmailChange 第 1 步：向当前邮箱发送验证码。
func (s Service) StartEmailChange(ctx context.Context, userID string) (EmailChallenge, error) {
	store, ok := s.Store.(EmailChangeStore)
	if !ok {
		return EmailChallenge{}, ErrAccountUnsupported
	}
	ttl, cooldown, maxAttempts, err := s.emailChangeDefaults()
	if err != nil {
		return EmailChallenge{}, err
	}
	user, err := s.CurrentUser(ctx, userID)
	if err != nil {
		return EmailChallenge{}, err
	}
	email := normalizeEmail(user.Email)
	code, salt, hash, err := s.draftEmailCode(email)
	if err != nil {
		return EmailChallenge{}, err
	}
	now := s.Now().UTC()
	challenge := EmailChangeChallenge{
		Stage: emailStageCurrent, Email: email, CodeHash: hash, CodeSalt: salt,
		SentAt: now, ExpiresAt: now.Add(ttl), MaxAttempts: maxAttempts,
	}
	challengeID, err := store.IssueCurrentEmailChallenge(ctx, userID, challenge, now, s.emailLimits(cooldown))
	if err != nil {
		return EmailChallenge{}, err
	}
	body := fmt.Sprintf("你正在修改 OpenFox 账号的邮箱。验证码是 %s，将在 %s 后失效。若不是你本人操作，请忽略此邮件。", code, formatDuration(ttl))
	if err := s.deliverEmailChangeCode(ctx, store, challengeID, email, "OpenFox 修改邮箱验证码", body, now); err != nil {
		return EmailChallenge{}, err
	}
	return EmailChallenge{ID: challengeID, ExpiresAt: challenge.ExpiresAt, ResendAfter: now.Add(cooldown)}, nil
}

// VerifyCurrentEmail 第 2 步：校验发到当前邮箱的验证码。
func (s Service) VerifyCurrentEmail(ctx context.Context, userID, challengeID, code string) error {
	store, ok := s.Store.(EmailChangeStore)
	if !ok {
		return ErrAccountUnsupported
	}
	if s.Now == nil {
		return ErrEmailCodeInvalid
	}
	if !validUUID(challengeID) || !validEmailCode(code) {
		return ErrEmailCodeInvalid
	}
	_, cooldown, _ := s.emailOTPDefaults()
	return store.VerifyCurrentEmailChallenge(ctx, userID, challengeID, s.Now().UTC(), s.emailLimits(cooldown), s.matchEmailCode(code))
}

// RequestNewEmail 第 3 步：向新邮箱发送验证码。新邮箱规范化后不能与当前邮箱相同。
func (s Service) RequestNewEmail(ctx context.Context, userID, challengeID, email string) (EmailChallenge, error) {
	store, ok := s.Store.(EmailChangeStore)
	if !ok {
		return EmailChallenge{}, ErrAccountUnsupported
	}
	normalized := normalizeEmail(email)
	if !validEmail(normalized) {
		return EmailChallenge{}, ErrInvalidEmail
	}
	ttl, cooldown, maxAttempts, err := s.emailChangeDefaults()
	if err != nil {
		return EmailChallenge{}, err
	}
	if !validUUID(challengeID) {
		return EmailChallenge{}, ErrEmailCodeInvalid
	}
	user, err := s.CurrentUser(ctx, userID)
	if err != nil {
		return EmailChallenge{}, err
	}
	if normalizeEmail(user.Email) == normalized {
		return EmailChallenge{}, ErrEmailUnchanged
	}
	code, salt, hash, err := s.draftEmailCode(normalized)
	if err != nil {
		return EmailChallenge{}, err
	}
	now := s.Now().UTC()
	challenge := EmailChangeChallenge{
		Stage: emailStageNew, ParentID: challengeID, Email: normalized, CodeHash: hash, CodeSalt: salt,
		SentAt: now, ExpiresAt: now.Add(ttl), MaxAttempts: maxAttempts,
	}
	newID, err := store.IssueNewEmailChallenge(ctx, userID, challenge, now, ttl, s.emailLimits(cooldown))
	if err != nil {
		return EmailChallenge{}, err
	}
	body := fmt.Sprintf("你正在把 OpenFox 账号的邮箱改为本地址。验证码是 %s，将在 %s 后失效。若不是你本人操作，请忽略此邮件。", code, formatDuration(ttl))
	if err := s.deliverEmailChangeCode(ctx, store, newID, normalized, "OpenFox 新邮箱验证码", body, now); err != nil {
		return EmailChallenge{}, err
	}
	return EmailChallenge{ID: newID, ExpiresAt: challenge.ExpiresAt, ResendAfter: now.Add(cooldown)}, nil
}

// ConfirmEmailChange 第 4 步：校验发到新邮箱的验证码并修改邮箱。sessionID 是当前会话，它保持有效。
func (s Service) ConfirmEmailChange(ctx context.Context, userID, sessionID, challengeID, code string) (User, error) {
	store, ok := s.Store.(EmailChangeStore)
	if !ok {
		return User{}, ErrAccountUnsupported
	}
	if s.Now == nil {
		return User{}, ErrEmailCodeInvalid
	}
	if !validUUID(challengeID) || !validEmailCode(code) {
		return User{}, ErrEmailCodeInvalid
	}
	ttl, cooldown, _ := s.emailOTPDefaults()
	return store.CompleteEmailChange(ctx, userID, sessionID, challengeID, s.Now().UTC(), ttl, s.emailLimits(cooldown), s.matchEmailCode(code))
}

// emailChangeDefaults 检查发信所需的配置，与登录验证码的检查保持一致。
func (s Service) emailChangeDefaults() (time.Duration, time.Duration, int, error) {
	ttl, cooldown, maxAttempts := s.emailOTPDefaults()
	if s.Now == nil || ttl <= 0 || cooldown < 0 || maxAttempts < 1 {
		return 0, 0, 0, ErrEmailLoginNotConfigured
	}
	if s.EmailOTP.UniversalCode == "" && s.EmailSender == nil {
		return 0, 0, 0, ErrEmailLoginNotConfigured
	}
	return ttl, cooldown, maxAttempts, nil
}

// draftEmailCode 生成明文验证码（只用于发信）与加盐哈希（入库）。万能验证码模式下明文即万能码。
func (s Service) draftEmailCode(email string) (string, []byte, []byte, error) {
	code, err := s.newEmailCode()
	if err != nil {
		return "", nil, nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", nil, nil, err
	}
	return code, salt, hashEmailCode(salt, email, code), nil
}

// deliverEmailChangeCode 发送验证码。万能验证码模式不发信；发信失败时作废刚签发的验证码。
func (s Service) deliverEmailChangeCode(ctx context.Context, store EmailChangeStore, challengeID, email, subject, body string, now time.Time) error {
	if s.EmailOTP.UniversalCode != "" {
		return nil
	}
	if err := s.EmailSender.Send(ctx, email, subject, body); err != nil {
		_ = store.CancelEmailChangeChallenge(ctx, challengeID, now)
		return fmt.Errorf("%w: %v", ErrEmailDelivery, err)
	}
	return nil
}

// matchEmailCode 与登录验证码一致：万能验证码或加盐哈希相等即通过。
func (s Service) matchEmailCode(code string) func(EmailChangeChallenge) bool {
	return func(challenge EmailChangeChallenge) bool {
		if s.EmailOTP.UniversalCode != "" && subtle.ConstantTimeCompare([]byte(code), []byte(s.EmailOTP.UniversalCode)) == 1 {
			return true
		}
		candidate := hashEmailCode(challenge.CodeSalt, challenge.Email, code)
		return subtle.ConstantTimeCompare(candidate, challenge.CodeHash) == 1
	}
}

// emailChallengeExhausted 判断验证码已过期或尝试次数用尽，此时不能再校验。
func emailChallengeExhausted(expiresAt time.Time, attempts, maxAttempts int, now time.Time) bool {
	return !now.Before(expiresAt) || attempts >= maxAttempts
}

// emailChangeParentUsable 判断第 1 步验证码能否作为第 3、4 步的依据：已校验、未消费，且校验时间在 TTL 之内。
func emailChangeParentUsable(parent EmailChangeChallenge, now time.Time, ttl time.Duration) bool {
	if parent.Stage != emailStageCurrent || parent.VerifiedAt == nil || parent.ConsumedAt != nil {
		return false
	}
	return now.Before(parent.VerifiedAt.Add(ttl))
}
