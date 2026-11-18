package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"openfox/internal/tenancy"
)

var (
	ErrCredentialNotFound = errors.New("identity: credential not found")
	ErrSessionNotFound    = errors.New("identity: session not found")
)

type ClientKind string

const (
	ClientWeb     ClientKind = "WEB"
	ClientDesktop ClientKind = "DESKTOP"
	ClientDevice  ClientKind = "DEVICE"
)

func (k ClientKind) valid() bool {
	return k == ClientWeb || k == ClientDesktop || k == ClientDevice
}

type Credentials struct {
	UserID            string
	PasswordHash      string
	CredentialVersion int64
	Status            string
	// Created 仅在邮箱验证码登录时有意义：true 表示本次登录自动注册了新账号。
	Created bool
}

type Session struct {
	ID                string
	UserID            string
	TokenHash         []byte
	ClientKind        ClientKind
	CredentialVersion int64
	IssuedAt          time.Time
	ExpiresAt         time.Time
	LastSeenAt        time.Time
	RevokedAt         *time.Time
}

type Store interface {
	FindCredentials(context.Context, string) (Credentials, error)
	CreateSession(context.Context, Session) error
	AuthenticateSession(context.Context, []byte, time.Time, time.Duration) (Session, error)
	SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, string, error)
}

// EmailLoginStore is kept separate from Store so existing device and API
// test doubles do not need to implement the email-login persistence path.
type EmailLoginStore interface {
	IssueEmailLoginChallenge(context.Context, EmailLoginChallenge, time.Time, EmailLimits) (string, error)
	ConsumeEmailLoginChallenge(context.Context, string, time.Time, EmailLimits, func(EmailLoginChallenge) bool) (Credentials, error)
	CancelEmailLoginChallenge(context.Context, string, time.Time) error
}

type EmailSender interface {
	Send(context.Context, string, string, string) error
}

type Service struct {
	Store           Store
	EmailStore      EmailLoginStore
	EmailSender     EmailSender
	EmailOTP        EmailOTPConfig
	PasswordHasher  Argon2idHasher
	SessionIdle     time.Duration
	SessionAbsolute time.Duration
	Now             func() time.Time
}

type LoginResult struct {
	UserID    string
	Token     string
	ExpiresAt time.Time
	// NewUser 表示本次邮箱验证码登录自动注册了账号，前端据此进入首登初始化。
	NewUser bool
}

type Principal struct {
	SessionID         string
	UserID            string
	CredentialVersion int64
	// TokenID 只在个人访问令牌认证时有值；会话认证时为空。
	TokenID string
}

func (s Service) Login(ctx context.Context, email, password string, clientKind ClientKind) (LoginResult, error) {
	if s.Store == nil || s.Now == nil || !clientKind.valid() || s.SessionAbsolute <= 0 || s.SessionIdle <= 0 || s.SessionIdle > s.SessionAbsolute {
		return LoginResult{}, ErrInvalidCredentials
	}
	normalized := normalizeEmail(email)
	if normalized == "" || password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	credentials, err := s.Store.FindCredentials(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	if credentials.Status != "ACTIVE" || credentials.CredentialVersion <= 0 {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := s.PasswordHasher.Verify(password, credentials.PasswordHash); err != nil {
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrInvalidPasswordHash) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	return s.createSession(ctx, credentials.UserID, credentials.CredentialVersion, clientKind)
}

func (s Service) createSession(ctx context.Context, userID string, credentialVersion int64, clientKind ClientKind) (LoginResult, error) {
	if s.Store == nil || s.Now == nil || !clientKind.valid() || userID == "" || credentialVersion <= 0 || s.SessionAbsolute <= 0 || s.SessionIdle <= 0 || s.SessionIdle > s.SessionAbsolute {
		return LoginResult{}, ErrInvalidCredentials
	}
	token, tokenHash, err := NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := s.Now().UTC()
	expiresAt := now.Add(s.SessionAbsolute)
	if err := s.Store.CreateSession(ctx, Session{
		UserID:            userID,
		TokenHash:         tokenHash,
		ClientKind:        clientKind,
		CredentialVersion: credentialVersion,
		IssuedAt:          now,
		ExpiresAt:         expiresAt,
		LastSeenAt:        now,
	}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{UserID: userID, Token: token, ExpiresAt: expiresAt}, nil
}

func (s Service) AuthenticateSession(ctx context.Context, token string) (Principal, error) {
	if s.Store == nil || strings.TrimSpace(token) == "" || s.Now == nil {
		return Principal{}, ErrSessionNotFound
	}
	now := s.Now().UTC()
	session, err := s.Store.AuthenticateSession(ctx, HashSessionToken(token), now, s.SessionIdle)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return Principal{}, ErrSessionNotFound
		}
		return Principal{}, err
	}
	if s.SessionIdle <= 0 || session.RevokedAt != nil || !now.Before(session.ExpiresAt) || !session.LastSeenAt.Add(s.SessionIdle).After(now) {
		return Principal{}, ErrSessionNotFound
	}
	return Principal{SessionID: session.ID, UserID: session.UserID, CredentialVersion: session.CredentialVersion}, nil
}

func (s Service) SelectOrganization(ctx context.Context, actorID, organizationID string) (tenancy.OrganizationSelection, string, error) {
	if s.Store == nil {
		return tenancy.OrganizationSelection{}, "", tenancy.ErrOrganizationSelection
	}
	return s.Store.SelectOrganization(ctx, actorID, organizationID)
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
