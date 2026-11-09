package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

type PostgresStore struct {
	Pool *postgres.Pool
}

func (s PostgresStore) FindCredentials(ctx context.Context, email string) (Credentials, error) {
	if s.Pool == nil {
		return Credentials{}, postgres.ErrNilPool
	}
	var credentials Credentials
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.id::text, pc.password_hash, pc.credential_version, pc.status
			FROM openfox.users u
			JOIN openfox.password_credentials pc ON pc.user_id = u.id
			WHERE u.normalized_email = $1
		`, email).Scan(&credentials.UserID, &credentials.PasswordHash, &credentials.CredentialVersion, &credentials.Status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrCredentialNotFound
	}
	return credentials, err
}

func (s PostgresStore) CreateSession(ctx context.Context, session Session) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO openfox.user_sessions (
				user_id, token_hash, client_kind, credential_version, issued_at, expires_at, last_seen_at
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		`, session.UserID, session.TokenHash, string(session.ClientKind), session.CredentialVersion,
			session.IssuedAt, session.ExpiresAt, session.LastSeenAt)
		return err
	})
}

func (s PostgresStore) AuthenticateSession(ctx context.Context, tokenHash []byte, now time.Time, idle time.Duration) (Session, error) {
	if s.Pool == nil {
		return Session{}, postgres.ErrNilPool
	}
	var session Session
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var clientKind string
		err := tx.QueryRow(ctx, `
			SELECT s.id::text, s.user_id::text, s.client_kind, s.credential_version,
			       s.issued_at, s.expires_at, s.last_seen_at, s.revoked_at
			FROM openfox.user_sessions s
			JOIN openfox.password_credentials pc ON pc.user_id = s.user_id
			WHERE s.token_hash = $1
			  AND s.revoked_at IS NULL
			  AND s.expires_at > $2
			  AND s.last_seen_at > $2 - $3::interval
			  AND pc.status = 'ACTIVE'
			  AND pc.credential_version = s.credential_version
			FOR UPDATE OF s
		`, tokenHash, now, idle.String()).Scan(&session.ID, &session.UserID, &clientKind, &session.CredentialVersion,
			&session.IssuedAt, &session.ExpiresAt, &session.LastSeenAt, &session.RevokedAt)
		if err == nil {
			session.ClientKind = ClientKind(clientKind)
			_, err = tx.Exec(ctx, `
				UPDATE openfox.user_sessions
				   SET last_seen_at = $1
				 WHERE id = $2::uuid AND revoked_at IS NULL AND expires_at > $1
			`, now, session.ID)
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	return session, err
}

func (s PostgresStore) SelectOrganization(ctx context.Context, actorID, organizationID string) (tenancy.OrganizationSelection, string, error) {
	if s.Pool == nil {
		return tenancy.OrganizationSelection{}, "", postgres.ErrNilPool
	}
	return s.Pool.ResolveOrganization(ctx, actorID, organizationID)
}

// emailWindowUsage 统计该邮箱在滚动窗口内已签发的验证码数与累计失败次数。
// 必须在持有该邮箱 advisory lock 的事务内调用，保证与签发/消费互斥。
func emailWindowUsage(ctx context.Context, tx pgx.Tx, email string, since time.Time) (sends int, failures int, oldest time.Time, err error) {
	var oldestSend *time.Time
	err = tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(attempt_count), 0), min(created_at)
		FROM openfox.email_login_challenges
		WHERE email = $1 AND created_at >= $2
	`, email, since).Scan(&sends, &failures, &oldestSend)
	if oldestSend != nil {
		oldest = *oldestSend
	}
	return sends, failures, oldest, err
}

func (s PostgresStore) IssueEmailLoginChallenge(ctx context.Context, challenge EmailLoginChallenge, now time.Time, limits EmailLimits) (string, error) {
	cooldown := limits.Cooldown
	if s.Pool == nil {
		return "", postgres.ErrNilPool
	}
	var challengeID string
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, challenge.Email); err != nil {
			return err
		}
		var lastSent time.Time
		err := tx.QueryRow(ctx, `
			SELECT sent_at
			FROM openfox.email_login_challenges
			WHERE email = $1 AND consumed_at IS NULL
			ORDER BY created_at DESC
			LIMIT 1
			FOR UPDATE
		`, challenge.Email).Scan(&lastSent)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && cooldown > 0 && lastSent.Add(cooldown).After(now) {
			return EmailCodeCooldownError{RetryAfter: lastSent.Add(cooldown).Sub(now)}
		}
		if limits.Window > 0 {
			sends, failures, oldest, err := emailWindowUsage(ctx, tx, challenge.Email, now.Add(-limits.Window))
			if err != nil {
				return err
			}
			if (limits.DailySends > 0 && sends >= limits.DailySends) ||
				(limits.DailyFailures > 0 && failures >= limits.DailyFailures) {
				return EmailCodeLimitError{RetryAfter: oldest.Add(limits.Window).Sub(now)}
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.email_login_challenges
			SET consumed_at = COALESCE(consumed_at, $2)
			WHERE email = $1 AND consumed_at IS NULL
		`, challenge.Email, now); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO openfox.email_login_challenges (
				email, code_hash, code_salt, sent_at, expires_at, max_attempts
			) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text
		`, challenge.Email, challenge.CodeHash, challenge.CodeSalt, challenge.SentAt, challenge.ExpiresAt, challenge.MaxAttempts).Scan(&challengeID)
	})
	return challengeID, err
}

func (s PostgresStore) ConsumeEmailLoginChallenge(ctx context.Context, email string, now time.Time, limits EmailLimits, verify func(EmailLoginChallenge) bool) (Credentials, error) {
	if s.Pool == nil {
		return Credentials{}, postgres.ErrNilPool
	}
	if verify == nil {
		return Credentials{}, ErrEmailCodeInvalid
	}
	var credentials Credentials
	invalid := false
	var limited *EmailCodeLimitError
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, email); err != nil {
			return err
		}
		// 累计失败达上限后，窗口内即使拿到新的验证码也不再接受校验，阻断「换码继续猜」。
		if limits.Window > 0 && limits.DailyFailures > 0 {
			_, failures, oldest, err := emailWindowUsage(ctx, tx, email, now.Add(-limits.Window))
			if err != nil {
				return err
			}
			if failures >= limits.DailyFailures {
				limited = &EmailCodeLimitError{RetryAfter: oldest.Add(limits.Window).Sub(now)}
				return nil
			}
		}
		var challenge EmailLoginChallenge
		err := tx.QueryRow(ctx, `
			SELECT id::text, email, code_hash, code_salt, sent_at, expires_at, attempt_count, max_attempts
			FROM openfox.email_login_challenges
			WHERE email = $1 AND consumed_at IS NULL
			ORDER BY created_at DESC
			LIMIT 1
			FOR UPDATE
		`, email).Scan(&challenge.ID, &challenge.Email, &challenge.CodeHash, &challenge.CodeSalt,
			&challenge.SentAt, &challenge.ExpiresAt, &challenge.AttemptCount, &challenge.MaxAttempts)
		if errors.Is(err, pgx.ErrNoRows) {
			invalid = true
			return nil
		}
		if err != nil {
			return err
		}
		if !now.Before(challenge.ExpiresAt) || challenge.AttemptCount >= challenge.MaxAttempts {
			_, updateErr := tx.Exec(ctx, `UPDATE openfox.email_login_challenges SET consumed_at = $2 WHERE id = $1::uuid`, challenge.ID, now)
			if updateErr != nil {
				return updateErr
			}
			invalid = true
			return nil
		}
		if !verify(challenge) {
			nextAttempt := challenge.AttemptCount + 1
			_, updateErr := tx.Exec(ctx, `
				UPDATE openfox.email_login_challenges
				SET attempt_count = $2,
				    consumed_at = CASE WHEN $2 >= max_attempts THEN $3 ELSE consumed_at END
				WHERE id = $1::uuid
			`, challenge.ID, nextAttempt, now)
			if updateErr != nil {
				return updateErr
			}
			invalid = true
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE openfox.email_login_challenges SET consumed_at = $2 WHERE id = $1::uuid`, challenge.ID, now); err != nil {
			return err
		}
		// 与消费验证码在同一事务中查找或创建账号：验证码失效与账号创建同时提交或同时回滚。
		var created bool
		err = tx.QueryRow(ctx, `
			SELECT user_id::text, credential_version, status, created
			FROM openfox_private.ensure_email_user($1)
		`, email).Scan(&credentials.UserID, &credentials.CredentialVersion, &credentials.Status, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			invalid = true
			return nil
		}
		credentials.Created = created
		return err
	})
	if limited != nil {
		return Credentials{}, *limited
	}
	if invalid {
		return Credentials{}, ErrEmailCodeInvalid
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrEmailCodeInvalid
	}
	return credentials, err
}

func (s PostgresStore) CancelEmailLoginChallenge(ctx context.Context, challengeID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.email_login_challenges
			SET consumed_at = COALESCE(consumed_at, $2)
			WHERE id = $1::uuid
		`, challengeID, now)
		return err
	})
}
