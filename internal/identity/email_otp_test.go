package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"openfox/internal/tenancy"
)

func TestEmailCodeUniversalTestOverrideIsSingleUse(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{base: fakeStore{credentials: Credentials{
		UserID: "user-1", PasswordHash: "unused", CredentialVersion: 2, Status: "ACTIVE",
	}}}
	service := Service{
		Store: store, EmailStore: store,
		EmailOTP:    EmailOTPConfig{ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Minute, MaxAttempts: 3, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	request, err := service.RequestEmailCode(context.Background(), " Alice@Example.TEST ")
	if err != nil {
		t.Fatal(err)
	}
	if !request.ExpiresAt.Equal(now.Add(10*time.Minute)) || store.challenge.Email != "alice@example.test" {
		t.Fatalf("request = %#v, challenge = %#v", request, store.challenge)
	}
	result, err := service.VerifyEmailCode(context.Background(), "alice@example.test", "666666", ClientWeb)
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "user-1" || result.Token == "" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := service.VerifyEmailCode(context.Background(), "alice@example.test", "666666", ClientWeb); !errors.Is(err, ErrEmailCodeInvalid) {
		t.Fatalf("second verification error = %v, want invalid code", err)
	}
}

func TestEmailCodeCapsAttemptsAndHonorsResendCooldown(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{base: fakeStore{credentials: Credentials{
		UserID: "user-1", PasswordHash: "unused", CredentialVersion: 1, Status: "ACTIVE",
	}}}
	service := Service{
		Store: store, EmailStore: store, EmailSender: fakeEmailSender{},
		EmailOTP:    EmailOTPConfig{ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Minute, MaxAttempts: 2},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	if _, err := service.RequestEmailCode(context.Background(), "user@example.test"); err != nil {
		t.Fatal(err)
	}
	var cooldown EmailCodeCooldownError
	if _, err := service.RequestEmailCode(context.Background(), "user@example.test"); !errors.As(err, &cooldown) {
		t.Fatalf("resend error = %v, want cooldown", err)
	}
	if _, err := service.VerifyEmailCode(context.Background(), "user@example.test", "000000", ClientWeb); !errors.Is(err, ErrEmailCodeInvalid) {
		t.Fatalf("first invalid code error = %v", err)
	}
	if _, err := service.VerifyEmailCode(context.Background(), "user@example.test", "000000", ClientWeb); !errors.Is(err, ErrEmailCodeInvalid) {
		t.Fatalf("second invalid code error = %v", err)
	}
	if store.consumedAt.IsZero() {
		t.Fatal("challenge remained usable after max attempts")
	}
}

func TestEmailCodeRequiresDeliveryOrTestOverride(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{base: fakeStore{credentials: Credentials{
		UserID: "user-1", PasswordHash: "unused", CredentialVersion: 1, Status: "ACTIVE",
	}}}
	service := Service{
		Store: store, EmailStore: store, EmailOTP: EmailOTPConfig{ChallengeTTL: time.Minute},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	if _, err := service.RequestEmailCode(context.Background(), "user@example.test"); !errors.Is(err, ErrEmailLoginNotConfigured) {
		t.Fatalf("request error = %v, want not configured", err)
	}
	if store.challenge.Email != "" {
		t.Fatalf("unconfigured request persisted a challenge: %#v", store.challenge)
	}
}

func TestEmailCodeIssuesForUnregisteredEmailAndMarksNewUser(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{
		missing: true,
		signup:  Credentials{UserID: "new-user", CredentialVersion: 1, Status: "ACTIVE", Created: true},
	}
	service := Service{
		Store: store, EmailStore: store,
		EmailOTP:    EmailOTPConfig{ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Minute, MaxAttempts: 3, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	if _, err := service.RequestEmailCode(context.Background(), "new@example.test"); err != nil {
		t.Fatal(err)
	}
	if store.challenge.Email != "new@example.test" {
		t.Fatalf("unregistered email did not receive a challenge: %#v", store.challenge)
	}
	result, err := service.VerifyEmailCode(context.Background(), "new@example.test", "666666", ClientWeb)
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "new-user" || !result.NewUser || result.Token == "" {
		t.Fatalf("result = %#v, want new user session", result)
	}
}

func TestEmailCodeSuppressesDeliveryForRevokedAccount(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{base: fakeStore{credentials: Credentials{
		UserID: "user-1", CredentialVersion: 1, Status: "REVOKED",
	}}}
	sender := &recordingEmailSender{}
	service := Service{
		Store: store, EmailStore: store, EmailSender: sender,
		EmailOTP:    EmailOTPConfig{ChallengeTTL: 10 * time.Minute},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	result, err := service.RequestEmailCode(context.Background(), "user@example.test")
	if err != nil || !result.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("result = %#v, err = %v; want indistinguishable success", result, err)
	}
	if store.challenge.Email != "" || sender.sent != 0 {
		t.Fatalf("revoked account received a challenge or email: %#v sent=%d", store.challenge, sender.sent)
	}
}

func TestEmailCodeSurfacesDailyLimitAndPassesLimitsToStore(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailOTPStoreFake{
		base:     fakeStore{credentials: Credentials{UserID: "user-1", CredentialVersion: 1, Status: "ACTIVE"}},
		limitErr: &EmailCodeLimitError{RetryAfter: time.Hour},
	}
	service := Service{
		Store: store, EmailStore: store,
		EmailOTP:    EmailOTPConfig{ChallengeTTL: 10 * time.Minute, UniversalCode: "666666", DailySends: 3},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	var limited EmailCodeLimitError
	if _, err := service.RequestEmailCode(context.Background(), "user@example.test"); !errors.As(err, &limited) {
		t.Fatalf("request error = %v, want daily limit", err)
	}
	if store.limits.DailySends != 3 || store.limits.DailyFailures != 20 || store.limits.Window != 24*time.Hour || store.limits.Cooldown != time.Minute {
		t.Fatalf("limits passed to store = %#v", store.limits)
	}
	if _, err := service.VerifyEmailCode(context.Background(), "user@example.test", "666666", ClientWeb); !errors.As(err, &limited) {
		t.Fatalf("verify error = %v, want daily limit", err)
	}
}

type recordingEmailSender struct{ sent int }

func (s *recordingEmailSender) Send(context.Context, string, string, string) error {
	s.sent++
	return nil
}

type emailOTPStoreFake struct {
	base       fakeStore
	limitErr   *EmailCodeLimitError
	limits     EmailLimits
	missing    bool
	signup     Credentials
	challenge  EmailLoginChallenge
	consumedAt time.Time
	attempts   int
}

func (s *emailOTPStoreFake) FindCredentials(ctx context.Context, email string) (Credentials, error) {
	if s.missing {
		return Credentials{}, ErrCredentialNotFound
	}
	return s.base.FindCredentials(ctx, email)
}

func (s *emailOTPStoreFake) CreateSession(ctx context.Context, session Session) error {
	return s.base.CreateSession(ctx, session)
}

func (s *emailOTPStoreFake) AuthenticateSession(ctx context.Context, token []byte, now time.Time, idle time.Duration) (Session, error) {
	return s.base.AuthenticateSession(ctx, token, now, idle)
}

func (s *emailOTPStoreFake) SelectOrganization(ctx context.Context, actorID, organizationID string) (tenancy.OrganizationSelection, string, error) {
	return tenancy.OrganizationSelection{}, "", nil
}

func (s *emailOTPStoreFake) IssueEmailLoginChallenge(_ context.Context, challenge EmailLoginChallenge, now time.Time, limits EmailLimits) (string, error) {
	s.limits = limits
	if s.limitErr != nil {
		return "", *s.limitErr
	}
	cooldown := limits.Cooldown
	if s.challenge.Email != "" && s.consumedAt.IsZero() && cooldown > 0 && s.challenge.SentAt.Add(cooldown).After(now) {
		return "", EmailCodeCooldownError{RetryAfter: s.challenge.SentAt.Add(cooldown).Sub(now)}
	}
	s.challenge = challenge
	return "challenge-1", nil
}

func (s *emailOTPStoreFake) ConsumeEmailLoginChallenge(_ context.Context, _ string, now time.Time, limits EmailLimits, verify func(EmailLoginChallenge) bool) (Credentials, error) {
	s.limits = limits
	if s.limitErr != nil {
		return Credentials{}, *s.limitErr
	}
	if s.challenge.Email == "" || !s.consumedAt.IsZero() {
		return Credentials{}, ErrEmailCodeInvalid
	}
	if !verify(s.challenge) {
		s.attempts++
		if s.attempts >= s.challenge.MaxAttempts {
			s.consumedAt = now
		}
		return Credentials{}, ErrEmailCodeInvalid
	}
	s.consumedAt = now
	if s.missing {
		return s.signup, nil
	}
	return s.base.credentials, nil
}

func (s *emailOTPStoreFake) CancelEmailLoginChallenge(_ context.Context, _ string, now time.Time) error {
	s.consumedAt = now
	return nil
}

type fakeEmailSender struct {
	to, subject, body string
}

func (s fakeEmailSender) Send(_ context.Context, to, subject, body string) error {
	s.to, s.subject, s.body = to, subject, body
	return nil
}
