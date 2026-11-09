package wp18

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

// TestEmailCodePerEmailLimitsAgainstPostgreSQL verifies that rotating codes
// cannot bypass the rolling per-email send and failure caps.
func TestEmailCodePerEmailLimitsAgainstPostgreSQL(t *testing.T) {
	connectionString := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	database, err := postgres.New(ctx, postgres.Config{
		URL: connectionString, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := identity.PostgresStore{Pool: database}
	clock := time.Now().UTC()
	service := identity.Service{
		Store: store, EmailStore: store,
		EmailOTP: identity.EmailOTPConfig{
			ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Second, MaxAttempts: 2,
			UniversalCode: "666666", DailySends: 3, DailyFailures: 4,
		},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return clock },
	}
	var limited identity.EmailCodeLimitError

	// 发信上限：窗口内第 4 次签发被拒绝，冷却已过也一样。
	sendsEmail := "limit-sends-" + clock.Format("150405.000000") + "@example.test"
	for index := 0; index < 3; index++ {
		clock = clock.Add(2 * time.Second)
		if _, err := service.RequestEmailCode(ctx, sendsEmail); err != nil {
			t.Fatalf("send %d: %v", index, err)
		}
	}
	clock = clock.Add(2 * time.Second)
	if _, err := service.RequestEmailCode(ctx, sendsEmail); !errors.As(err, &limited) {
		t.Fatalf("4th send error = %v, want daily limit", err)
	}
	if limited.RetryAfter <= 0 || limited.RetryAfter > 24*time.Hour {
		t.Fatalf("retry after = %s", limited.RetryAfter)
	}

	// 累计失败上限：每个验证码只能试 2 次，换新码后累计 4 次失败即锁定，正确码也不再接受。
	failEmail := "limit-fails-" + clock.Format("150405.000000") + "@example.test"
	for round := 0; round < 2; round++ {
		clock = clock.Add(2 * time.Second)
		if _, err := service.RequestEmailCode(ctx, failEmail); err != nil {
			t.Fatalf("round %d request: %v", round, err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if _, err := service.VerifyEmailCode(ctx, failEmail, "000000", identity.ClientWeb); !errors.Is(err, identity.ErrEmailCodeInvalid) {
				t.Fatalf("round %d attempt %d error = %v", round, attempt, err)
			}
		}
	}
	clock = clock.Add(2 * time.Second)
	if _, err := service.RequestEmailCode(ctx, failEmail); !errors.As(err, &limited) {
		t.Fatalf("request after failures error = %v, want daily limit", err)
	}
	if _, err := service.VerifyEmailCode(ctx, failEmail, "666666", identity.ClientWeb); !errors.As(err, &limited) {
		t.Fatalf("verify after failures error = %v, want daily limit even with the right code", err)
	}

	// 窗口过去后恢复。
	clock = clock.Add(25 * time.Hour)
	if _, err := service.RequestEmailCode(ctx, failEmail); err != nil {
		t.Fatalf("request after window: %v", err)
	}
	if result, err := service.VerifyEmailCode(ctx, failEmail, "666666", identity.ClientWeb); err != nil || result.Token == "" {
		t.Fatalf("verify after window = %#v, %v", result, err)
	}
}
