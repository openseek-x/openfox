package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

const emailChangeChallengeColumns = `id::text, user_id::text, stage, COALESCE(parent_id::text, ''), email,
	code_hash, code_salt, sent_at, expires_at, attempt_count, max_attempts, verified_at, consumed_at`

// lockEmailChangeUser 串行化同一用户的修改邮箱操作，让限额统计与签发、校验互斥。
// 键带前缀，不会与登录验证码按邮箱加的锁冲突。
func lockEmailChangeUser(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "email-change:"+userID)
	return err
}

// loadEmailChangeChallenge 读取并锁定本人的某阶段验证码；不存在或不属于本人时返回 pgx.ErrNoRows。
func loadEmailChangeChallenge(ctx context.Context, tx pgx.Tx, userID, challengeID, stage string) (EmailChangeChallenge, error) {
	var challenge EmailChangeChallenge
	err := tx.QueryRow(ctx, `
		SELECT `+emailChangeChallengeColumns+`
		  FROM openfox.email_change_challenges
		 WHERE id = $1::uuid AND user_id = $2::uuid AND stage = $3
		 FOR UPDATE
	`, challengeID, userID, stage).Scan(&challenge.ID, &challenge.UserID, &challenge.Stage, &challenge.ParentID,
		&challenge.Email, &challenge.CodeHash, &challenge.CodeSalt, &challenge.SentAt, &challenge.ExpiresAt,
		&challenge.AttemptCount, &challenge.MaxAttempts, &challenge.VerifiedAt, &challenge.ConsumedAt)
	return challenge, err
}

// emailChangeWindowUsage 统计用户在滚动窗口内签发的验证码数与累计失败次数（两个阶段合计）。
func emailChangeWindowUsage(ctx context.Context, tx pgx.Tx, userID string, since time.Time) (sends int, failures int, oldest time.Time, err error) {
	var oldestSend *time.Time
	err = tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(attempt_count), 0), min(created_at)
		  FROM openfox.email_change_challenges
		 WHERE user_id = $1::uuid AND created_at >= $2
	`, userID, since).Scan(&sends, &failures, &oldestSend)
	if oldestSend != nil {
		oldest = *oldestSend
	}
	return sends, failures, oldest, err
}

// checkEmailChangeSendLimits 检查重发冷却（同阶段最近一次签发，含已消费的记录）与每日发送上限。
func checkEmailChangeSendLimits(ctx context.Context, tx pgx.Tx, userID, stage string, now time.Time, limits EmailLimits) error {
	var lastSent time.Time
	err := tx.QueryRow(ctx, `
		SELECT sent_at
		  FROM openfox.email_change_challenges
		 WHERE user_id = $1::uuid AND stage = $2
		 ORDER BY created_at DESC
		 LIMIT 1
	`, userID, stage).Scan(&lastSent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && limits.Cooldown > 0 && lastSent.Add(limits.Cooldown).After(now) {
		return EmailCodeCooldownError{RetryAfter: lastSent.Add(limits.Cooldown).Sub(now)}
	}
	if limits.Window <= 0 {
		return nil
	}
	sends, _, oldest, err := emailChangeWindowUsage(ctx, tx, userID, now.Add(-limits.Window))
	if err != nil {
		return err
	}
	if limits.DailySends > 0 && sends >= limits.DailySends {
		return EmailCodeLimitError{RetryAfter: oldest.Add(limits.Window).Sub(now)}
	}
	return nil
}

// checkEmailChangeFailures 在校验前检查滚动窗口内的累计失败次数，达到上限后即使有新验证码也不再接受校验。
func checkEmailChangeFailures(ctx context.Context, tx pgx.Tx, userID string, now time.Time, limits EmailLimits) error {
	if limits.Window <= 0 || limits.DailyFailures <= 0 {
		return nil
	}
	_, failures, oldest, err := emailChangeWindowUsage(ctx, tx, userID, now.Add(-limits.Window))
	if err != nil {
		return err
	}
	if failures >= limits.DailyFailures {
		return EmailCodeLimitError{RetryAfter: oldest.Add(limits.Window).Sub(now)}
	}
	return nil
}

// insertEmailChangeChallenge 写入一条验证码记录；stage = current 时 ParentID 为空，NULL 写入 parent_id。
func insertEmailChangeChallenge(ctx context.Context, tx pgx.Tx, userID string, challenge EmailChangeChallenge) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO openfox.email_change_challenges (
			user_id, stage, parent_id, email, code_hash, code_salt, sent_at, expires_at, max_attempts
		) VALUES ($1::uuid, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9)
		RETURNING id::text
	`, userID, challenge.Stage, challenge.ParentID, challenge.Email, challenge.CodeHash, challenge.CodeSalt,
		challenge.SentAt, challenge.ExpiresAt, challenge.MaxAttempts).Scan(&id)
	return id, err
}

// checkEmailChangeCode 校验验证码并记录结果。过期或次数用尽时作废；校验错误时递增尝试次数，用尽即作废。
// 返回 false 时调用方必须提交事务，否则尝试计数会丢失。
func checkEmailChangeCode(ctx context.Context, tx pgx.Tx, challenge EmailChangeChallenge, now time.Time, verify func(EmailChangeChallenge) bool) (bool, error) {
	if emailChallengeExhausted(challenge.ExpiresAt, challenge.AttemptCount, challenge.MaxAttempts, now) {
		_, err := tx.Exec(ctx, `UPDATE openfox.email_change_challenges SET consumed_at = $2 WHERE id = $1::uuid`, challenge.ID, now)
		return false, err
	}
	if verify(challenge) {
		return true, nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.email_change_challenges
		   SET attempt_count = $2,
		       consumed_at = CASE WHEN $2 >= max_attempts THEN $3 ELSE consumed_at END
		 WHERE id = $1::uuid
	`, challenge.ID, challenge.AttemptCount+1, now)
	return false, err
}

// IssueCurrentEmailChallenge 签发第 1 步验证码，并作废该用户之前未消费的第 1 步验证码。
func (s PostgresStore) IssueCurrentEmailChallenge(ctx context.Context, userID string, challenge EmailChangeChallenge, now time.Time, limits EmailLimits) (string, error) {
	if s.Pool == nil {
		return "", postgres.ErrNilPool
	}
	var challengeID string
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockEmailChangeUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := checkEmailChangeSendLimits(ctx, tx, userID, emailStageCurrent, now, limits); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.email_change_challenges
			   SET consumed_at = $3
			 WHERE user_id = $1::uuid AND stage = $2 AND consumed_at IS NULL
		`, userID, emailStageCurrent, now); err != nil {
			return err
		}
		var err error
		challengeID, err = insertEmailChangeChallenge(ctx, tx, userID, challenge)
		return err
	})
	return challengeID, err
}

// IssueNewEmailChallenge 签发第 3 步验证码。父验证码必须属于本人、已校验、未消费且在 parentTTL 内；
// 新邮箱不能被其他用户占用。占用检查放在限额之后，限额同样约束探测占用的请求。
func (s PostgresStore) IssueNewEmailChallenge(ctx context.Context, userID string, challenge EmailChangeChallenge, now time.Time, parentTTL time.Duration, limits EmailLimits) (string, error) {
	if s.Pool == nil {
		return "", postgres.ErrNilPool
	}
	var challengeID string
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockEmailChangeUser(ctx, tx, userID); err != nil {
			return err
		}
		parent, err := loadEmailChangeChallenge(ctx, tx, userID, challenge.ParentID, emailStageCurrent)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEmailCodeInvalid
		}
		if err != nil {
			return err
		}
		if !emailChangeParentUsable(parent, now, parentTTL) {
			return ErrEmailCodeInvalid
		}
		if err := checkEmailChangeSendLimits(ctx, tx, userID, emailStageNew, now, limits); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.users WHERE normalized_email = $1 AND id <> $2::uuid)
		`, challenge.Email, userID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrEmailTaken
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.email_change_challenges
			   SET consumed_at = $3
			 WHERE user_id = $1::uuid AND stage = $2 AND consumed_at IS NULL
		`, userID, emailStageNew, now); err != nil {
			return err
		}
		challengeID, err = insertEmailChangeChallenge(ctx, tx, userID, challenge)
		return err
	})
	return challengeID, err
}

// VerifyCurrentEmailChallenge 校验第 2 步验证码。校验通过时记录 verified_at。
func (s PostgresStore) VerifyCurrentEmailChallenge(ctx context.Context, userID, challengeID string, now time.Time, limits EmailLimits, verify func(EmailChangeChallenge) bool) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	if verify == nil {
		return ErrEmailCodeInvalid
	}
	invalid := false
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockEmailChangeUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := checkEmailChangeFailures(ctx, tx, userID, now, limits); err != nil {
			return err
		}
		challenge, err := loadEmailChangeChallenge(ctx, tx, userID, challengeID, emailStageCurrent)
		if errors.Is(err, pgx.ErrNoRows) {
			invalid = true
			return nil
		}
		if err != nil {
			return err
		}
		if challenge.ConsumedAt != nil || challenge.VerifiedAt != nil {
			invalid = true
			return nil
		}
		ok, err := checkEmailChangeCode(ctx, tx, challenge, now, verify)
		if err != nil {
			return err
		}
		if !ok {
			invalid = true
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.email_change_challenges SET verified_at = $2 WHERE id = $1::uuid`, challenge.ID, now)
		return err
	})
	if err != nil {
		return err
	}
	if invalid {
		return ErrEmailCodeInvalid
	}
	return nil
}

// CompleteEmailChange 锁定第 3 步与其父验证码，校验通过后在同一事务内改邮箱、作废两条验证码并吊销其他会话。
// 唯一约束冲突（新邮箱刚被占用）返回 ErrEmailTaken，事务回滚。
func (s PostgresStore) CompleteEmailChange(ctx context.Context, userID, sessionID, challengeID string, now time.Time, parentTTL time.Duration, limits EmailLimits, verify func(EmailChangeChallenge) bool) (User, error) {
	if s.Pool == nil {
		return User{}, postgres.ErrNilPool
	}
	if verify == nil {
		return User{}, ErrEmailCodeInvalid
	}
	var user User
	invalid := false
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockEmailChangeUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := checkEmailChangeFailures(ctx, tx, userID, now, limits); err != nil {
			return err
		}
		child, err := loadEmailChangeChallenge(ctx, tx, userID, challengeID, emailStageNew)
		if errors.Is(err, pgx.ErrNoRows) {
			invalid = true
			return nil
		}
		if err != nil {
			return err
		}
		parent, err := loadEmailChangeChallenge(ctx, tx, userID, child.ParentID, emailStageCurrent)
		if errors.Is(err, pgx.ErrNoRows) {
			invalid = true
			return nil
		}
		if err != nil {
			return err
		}
		if child.ConsumedAt != nil || !emailChangeParentUsable(parent, now, parentTTL) {
			invalid = true
			return nil
		}
		ok, err := checkEmailChangeCode(ctx, tx, child, now, verify)
		if err != nil {
			return err
		}
		if !ok {
			invalid = true
			return nil
		}
		user, err = scanUser(tx.QueryRow(ctx, `
			UPDATE openfox.users SET email = $2 WHERE id = $1::uuid
			RETURNING `+userProfileColumns, userID, child.Email))
		if isUniqueViolation(err) {
			return ErrEmailTaken
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.email_change_challenges
			   SET consumed_at = $2
			 WHERE id = ANY($1::uuid[])
		`, []string{child.ID, parent.ID}, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE openfox.user_sessions
			   SET revoked_at = GREATEST($3, issued_at)
			 WHERE user_id = $1::uuid AND id <> $2::uuid AND revoked_at IS NULL
		`, userID, sessionID, now)
		return err
	})
	if err != nil {
		return User{}, err
	}
	if invalid {
		return User{}, ErrEmailCodeInvalid
	}
	return user, nil
}

// CancelEmailChangeChallenge 作废发信失败的验证码，幂等。
func (s PostgresStore) CancelEmailChangeChallenge(ctx context.Context, challengeID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.email_change_challenges
			   SET consumed_at = COALESCE(consumed_at, $2)
			 WHERE id = $1::uuid
		`, challengeID, now)
		return err
	})
}
