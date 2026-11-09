package wp17

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

const wp17AliceEmail = "alice@example.test"

func TestEmailLoginAgainstPostgreSQL(t *testing.T) {
	connectionString := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
		MemoryKiB: 16 * 1024, Iterations: 1, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("wp17-test-only")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		t.Fatal(err)
	}
	seedResult, err := seed.Exec(ctx, `
		INSERT INTO openfox.password_credentials (user_id, password_hash)
		SELECT id, $2 FROM openfox.users WHERE normalized_email = $1
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()
	`, wp17AliceEmail, hash)
	if err != nil {
		_ = seed.Close(ctx)
		t.Fatal(err)
	}
	if seedResult.RowsAffected() != 1 {
		_ = seed.Close(ctx)
		t.Fatalf("seeded credentials rows = %d, want 1", seedResult.RowsAffected())
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	database, err := postgres.New(ctx, postgres.Config{
		URL: connectionString, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	store := identity.PostgresStore{Pool: database}
	if err := database.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.email_login_challenges
			SET consumed_at = COALESCE(consumed_at, clock_timestamp())
			WHERE email = $1 AND consumed_at IS NULL
		`, wp17AliceEmail)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	service := identity.Service{
		Store: store, EmailStore: store,
		EmailOTP:    identity.EmailOTPConfig{ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Second, MaxAttempts: 5, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}
	if _, err := service.RequestEmailCode(ctx, wp17AliceEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyEmailCode(ctx, wp17AliceEmail, "000000", identity.ClientWeb); !errors.Is(err, identity.ErrEmailCodeInvalid) {
		t.Fatalf("wrong code error = %v, want invalid code", err)
	}
	var cooldown identity.EmailCodeCooldownError
	if _, err := service.RequestEmailCode(ctx, wp17AliceEmail); !errors.As(err, &cooldown) {
		t.Fatalf("immediate resend error = %v, want cooldown", err)
	}
	loginResult, err := service.VerifyEmailCode(ctx, wp17AliceEmail, "666666", identity.ClientWeb)
	if err != nil {
		t.Fatal(err)
	}
	if loginResult.UserID == "" || loginResult.Token == "" {
		t.Fatalf("login result = %#v", loginResult)
	}
	if _, err := service.VerifyEmailCode(ctx, wp17AliceEmail, "666666", identity.ClientWeb); !errors.Is(err, identity.ErrEmailCodeInvalid) {
		t.Fatalf("second verification error = %v, want invalid code", err)
	}
}
