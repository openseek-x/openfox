-- 阶段 4c「个人访问令牌、通知渠道、内测开关」。
--
-- 仅扩展（expand-only）：
--   - personal_access_tokens：个人访问令牌。只存 SHA-256 哈希与展示用前缀，明文只在创建时返回一次；
--     可选过期时间、最近使用时间（应用按分钟节流更新）、吊销时间。令牌与本人权限相同，由应用限制
--     令牌不能管理令牌、不能修改邮箱。
--   - user_notification_channels：每人每个推送渠道（desktop / browser）一行：总开关与关闭的事件类型
--     （assign / status / comment / mention / agent / automation）。没有行 = 默认（开启、全部事件）。
--   - users.access_tier：standard / beta / internal；只有 beta / internal 能看到内测功能开关页（由运维用 SQL 设置）。
--   - user_beta_features：内测功能的个人开关。可用功能清单由应用维护，库里只存开关状态。
-- 账号级表沿用 user_sessions / user_preferences 的做法：不开 RLS，只给应用角色需要的列级权限，
-- 所有读写按已认证的用户 ID 过滤。
-- 锁：users 加常量默认值列，不重写表。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000039 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.personal_access_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES openfox.users (id),
    name text NOT NULL,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamptz,
    last_used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT personal_access_tokens_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT personal_access_tokens_prefix_valid CHECK (token_prefix ~ '^ofx_pat_[A-Za-z0-9]{4}$'),
    CONSTRAINT personal_access_tokens_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT personal_access_tokens_expiry_valid CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE UNIQUE INDEX personal_access_tokens_hash_unique ON openfox.personal_access_tokens (token_hash);
CREATE INDEX personal_access_tokens_user_idx ON openfox.personal_access_tokens (user_id, created_at DESC);

CREATE TABLE openfox.user_notification_channels (
    user_id uuid NOT NULL REFERENCES openfox.users (id),
    channel text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    disabled_events text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, channel),
    CONSTRAINT user_notification_channels_channel_valid CHECK (channel IN ('desktop', 'browser')),
    CONSTRAINT user_notification_channels_events_valid CHECK (
        disabled_events <@ ARRAY['assign', 'status', 'comment', 'mention', 'agent', 'automation']::text[]
    )
);

ALTER TABLE openfox.users
    ADD COLUMN access_tier text NOT NULL DEFAULT 'standard';
ALTER TABLE openfox.users
    ADD CONSTRAINT users_access_tier_valid CHECK (access_tier IN ('standard', 'beta', 'internal'));

CREATE TABLE openfox.user_beta_features (
    user_id uuid NOT NULL REFERENCES openfox.users (id),
    feature text NOT NULL,
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, feature),
    CONSTRAINT user_beta_features_feature_valid CHECK (feature ~ '^[a-z][a-z0-9_]{1,63}$')
);

RESET ROLE;

GRANT SELECT, INSERT ON openfox.personal_access_tokens TO openfox_app;
GRANT UPDATE (last_used_at, revoked_at) ON openfox.personal_access_tokens TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.user_notification_channels TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.user_beta_features TO openfox_app;
