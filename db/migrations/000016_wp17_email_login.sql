-- WP-17 email verification login challenges.
-- The application stores only a salted digest of the short-lived code. The
-- table is control-plane data and is intentionally not tenant-scoped.

SET ROLE openfox_migrator;

CREATE TABLE openfox.email_login_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    code_hash bytea NOT NULL,
    code_salt bytea NOT NULL,
    sent_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    consumed_at timestamptz,
    CONSTRAINT email_login_challenges_email_not_blank CHECK (length(btrim(email)) > 0),
    CONSTRAINT email_login_challenges_code_hash_length CHECK (octet_length(code_hash) = 32),
    CONSTRAINT email_login_challenges_code_salt_length CHECK (octet_length(code_salt) = 16),
    CONSTRAINT email_login_challenges_attempt_count_valid CHECK (attempt_count >= 0 AND attempt_count <= max_attempts),
    CONSTRAINT email_login_challenges_max_attempts_positive CHECK (max_attempts > 0),
    CONSTRAINT email_login_challenges_expiry_after_sent CHECK (expires_at > sent_at),
    CONSTRAINT email_login_challenges_consumed_after_sent CHECK (consumed_at IS NULL OR consumed_at >= sent_at)
);

CREATE INDEX email_login_challenges_active_lookup_idx
    ON openfox.email_login_challenges (email, consumed_at, created_at DESC);

RESET ROLE;

-- Authentication code issuance and consumption are controlled by the Go
-- service; runtime callers do not receive direct schema ownership.
GRANT SELECT, INSERT, UPDATE ON openfox.email_login_challenges TO openfox_app;
