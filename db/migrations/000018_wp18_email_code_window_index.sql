-- WP-18 per-email rolling-window limits for email login codes.
--
-- The identity service counts, per email, how many codes were issued and how
-- many verification attempts failed within a rolling window (default 24h), to
-- stop long-running brute force and mail flooding. That count filters by
-- (email, created_at >= since); the existing active-lookup index leads with
-- consumed_at as its second column and cannot serve the range scan.
--
-- Expand-only. The table is small control-plane data; a plain CREATE INDEX is
-- acceptable here because the migration runner executes each file in a
-- transaction (CONCURRENTLY is not available).

SET ROLE openfox_migrator;

CREATE INDEX IF NOT EXISTS email_login_challenges_window_idx
    ON openfox.email_login_challenges (email, created_at);

RESET ROLE;
