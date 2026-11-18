-- 阶段 4b「个人资料、偏好与快捷键」：用户全名 / 简介 / 头像、账号偏好、修改邮箱的两步验证。
--
-- 仅扩展（expand-only）：
--   - users：full_name（≤ 80）、bio（≤ 280）、头像（文件放在 OPENFOX_STORAGE_ROOT，表里存键、类型、哈希与更新时间）。
--   - user_preferences：每个用户一行；主题（system / light / dark）、账号语言（zh-CN / en-US，空 = 跟随设备）、
--     外边框色（0 = 不设置，1–12 = 内置色）、两个任务会话开关（默认开启）、发送方式（enter / shift_enter）、
--     应用命令的自定义按键（jsonb，有大小上限，结构由应用校验）、version（乐观锁）。
--   - email_change_challenges：修改邮箱的验证码。stage = current（发到当前邮箱，验证身份）/ new（发到新邮箱），
--     new 必须挂在一个已验证、未过期的 current 上；验证码只存加盐哈希，带尝试次数与过期时间。
--   - shares_active_organization：头像读取时判断两人是否同在一个活跃组织（SECURITY DEFINER，只返回布尔值）。
-- 这些都是账号级数据，不按组织隔离，沿用 user_sessions / email_login_challenges 的做法：没有 RLS，
-- 应用角色只拿到需要的列级权限，所有读写都按已认证的用户 ID 过滤。
-- 锁：users 加可空列 / 常量默认值，不重写表。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000038 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.users
    ADD COLUMN full_name text NOT NULL DEFAULT '',
    ADD COLUMN bio text NOT NULL DEFAULT '',
    ADD COLUMN avatar_key text,
    ADD COLUMN avatar_content_type text,
    ADD COLUMN avatar_sha256 text,
    ADD COLUMN avatar_updated_at timestamptz;
ALTER TABLE openfox.users
    ADD CONSTRAINT users_full_name_length CHECK (char_length(full_name) <= 80),
    ADD CONSTRAINT users_bio_length CHECK (char_length(bio) <= 280),
    ADD CONSTRAINT users_avatar_valid CHECK (
        (avatar_key IS NULL AND avatar_content_type IS NULL AND avatar_sha256 IS NULL AND avatar_updated_at IS NULL)
        OR (avatar_key IS NOT NULL AND length(avatar_key) BETWEEN 1 AND 256
            AND avatar_content_type IN ('image/png', 'image/jpeg', 'image/webp')
            AND avatar_sha256 ~ '^[0-9a-f]{64}$' AND avatar_updated_at IS NOT NULL)
    );

CREATE TABLE openfox.user_preferences (
    user_id uuid PRIMARY KEY REFERENCES openfox.users (id),
    theme text NOT NULL DEFAULT 'system',
    locale text,
    outline_color smallint NOT NULL DEFAULT 0,
    open_session_on_assign boolean NOT NULL DEFAULT true,
    open_session_on_quick_command boolean NOT NULL DEFAULT true,
    send_shortcut text NOT NULL DEFAULT 'enter',
    command_shortcuts jsonb NOT NULL DEFAULT '{}',
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT user_preferences_theme_valid CHECK (theme IN ('system', 'light', 'dark')),
    CONSTRAINT user_preferences_locale_valid CHECK (locale IS NULL OR locale IN ('zh-CN', 'en-US')),
    CONSTRAINT user_preferences_outline_color_valid CHECK (outline_color BETWEEN 0 AND 12),
    CONSTRAINT user_preferences_send_shortcut_valid CHECK (send_shortcut IN ('enter', 'shift_enter')),
    CONSTRAINT user_preferences_command_shortcuts_shape CHECK (
        jsonb_typeof(command_shortcuts) = 'object' AND pg_column_size(command_shortcuts) <= 2048
    ),
    CONSTRAINT user_preferences_version_positive CHECK (version > 0)
);

CREATE TABLE openfox.email_change_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES openfox.users (id),
    stage text NOT NULL,
    parent_id uuid REFERENCES openfox.email_change_challenges (id),
    email text NOT NULL,
    code_hash bytea NOT NULL,
    code_salt bytea NOT NULL,
    sent_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    verified_at timestamptz,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT email_change_challenges_stage_valid CHECK (stage IN ('current', 'new')),
    CONSTRAINT email_change_challenges_parent_valid CHECK ((stage = 'new') = (parent_id IS NOT NULL)),
    CONSTRAINT email_change_challenges_email_not_blank CHECK (length(btrim(email)) > 0 AND length(email) <= 320),
    CONSTRAINT email_change_challenges_code_hash_length CHECK (octet_length(code_hash) = 32),
    CONSTRAINT email_change_challenges_code_salt_length CHECK (octet_length(code_salt) = 16),
    CONSTRAINT email_change_challenges_attempts_valid CHECK (attempt_count >= 0 AND attempt_count <= max_attempts),
    CONSTRAINT email_change_challenges_max_attempts_positive CHECK (max_attempts > 0),
    CONSTRAINT email_change_challenges_expiry_after_sent CHECK (expires_at > sent_at)
);
CREATE INDEX email_change_challenges_user_idx
    ON openfox.email_change_challenges (user_id, stage, created_at DESC);

-- 头像读取的权限判断：两人是否同在一个活跃组织（双方成员关系都是 ACTIVE、组织未删除）。
-- user_organization_index / organization_directory 是控制面表，应用角色不能直接读，所以用定义者函数只返回布尔值。
CREATE OR REPLACE FUNCTION openfox_private.shares_active_organization(p_viewer uuid, p_target uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT p_viewer IS NOT NULL AND p_target IS NOT NULL AND (p_viewer = p_target OR EXISTS (
        SELECT 1
          FROM openfox.user_organization_index a
          JOIN openfox.user_organization_index b ON b.organization_id = a.organization_id
          JOIN openfox.organization_directory d ON d.organization_id = a.organization_id
         WHERE a.user_id = p_viewer AND a.membership_status = 'ACTIVE'
           AND b.user_id = p_target AND b.membership_status = 'ACTIVE'
           AND d.deleted_at IS NULL))
$$;
ALTER FUNCTION openfox_private.shares_active_organization(uuid, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.shares_active_organization(uuid, uuid) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.shares_active_organization(uuid, uuid) TO openfox_app;
GRANT UPDATE (email, full_name, bio, avatar_key, avatar_content_type, avatar_sha256, avatar_updated_at)
    ON openfox.users TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.user_preferences TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.email_change_challenges TO openfox_app;
