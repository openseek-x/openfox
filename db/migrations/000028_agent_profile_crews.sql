-- 阶段 2a「Agent 与小队」：Agent 资料与触发开关、运行设置扩展、环境变量（加密）、Git 仓库、收藏；
-- 小队队长、指令、任务运行设置、真人成员与专属 Agent。
--
-- 仅扩展（expand-only），不改写已有数据：
--   - agents 新增列全部带默认值；kind 约束放宽为超集（新增 DESIGN，界面禁用）。
--     profile_version 是资料类修改的乐观锁，与配置版本链使用的 version 分开，互不冲突。
--   - agent_config_versions 不可变：新增列全部带默认值，已有版本行按默认值解释（与旧行为一致）。
--   - agent_env_vars 只存密文（AES-256-GCM，主密钥由 OPENFOX_SECRETS_KEY_FILE 提供，
--     key_id 记录所用主密钥）；派发 Run 时读取当前值，不固定到配置版本。
--   - 小队真人成员放在独立表 agent_crew_human_members，agent_crew_members 主键不变；
--     「一个 Agent 至多是一个小队的专属成员」用部分唯一索引兜底。
--   - leader_agent_id 为空的旧小队仍合法，应用读取时取 position 最小的 Agent 作为队长。
-- 迁移运行器在事务内执行每个文件，无法使用 CONCURRENTLY；表为控制面数据、规模小，
-- CREATE INDEX 与约束校验期间会短暂阻塞相关表的写入。
-- 回滚：应用回退后新列和新表闲置即可；确需移除时见 db/migrations/README.md 的 000028 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.agents
    ADD COLUMN title text NOT NULL DEFAULT '',
    ADD COLUMN avatar jsonb,
    ADD COLUMN allow_assign boolean NOT NULL DEFAULT true,
    ADD COLUMN allow_direct_chat boolean NOT NULL DEFAULT true,
    ADD COLUMN trigger_crew boolean NOT NULL DEFAULT true,
    ADD COLUMN trigger_mention boolean NOT NULL DEFAULT true,
    ADD COLUMN trigger_workflow boolean NOT NULL DEFAULT false,
    ADD COLUMN profile_version bigint NOT NULL DEFAULT 1;

ALTER TABLE openfox.agents
    ADD CONSTRAINT agents_title_length CHECK (length(title) <= 64),
    ADD CONSTRAINT agents_avatar_shape CHECK (
        avatar IS NULL OR (jsonb_typeof(avatar) = 'object' AND pg_column_size(avatar) <= 1024)
    ),
    ADD CONSTRAINT agents_profile_version_positive CHECK (profile_version > 0),
    DROP CONSTRAINT agents_kind_valid,
    ADD CONSTRAINT agents_kind_valid CHECK (kind IN ('CODE', 'OFFICE', 'DESIGN'));

ALTER TABLE openfox.agent_config_versions
    ADD COLUMN chat_concurrency text NOT NULL DEFAULT 'DEFAULT',
    ADD COLUMN chat_concurrency_value integer,
    ADD COLUMN workdir_isolation text NOT NULL DEFAULT 'WORKTREE',
    ADD COLUMN approval_mode text NOT NULL DEFAULT 'FOLLOW_RUNTIME',
    ADD COLUMN collaboration_mode text NOT NULL DEFAULT 'FOLLOW_AGENT',
    ADD COLUMN custom_args jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN git_author_name_mode text NOT NULL DEFAULT 'USER_AND_AGENT',
    ADD COLUMN git_author_name text NOT NULL DEFAULT '',
    ADD COLUMN git_author_email_mode text NOT NULL DEFAULT 'USER',
    ADD COLUMN git_author_email text NOT NULL DEFAULT '',
    ADD COLUMN env_digest text NOT NULL DEFAULT '';

ALTER TABLE openfox.agent_config_versions
    ADD CONSTRAINT agent_config_versions_chat_concurrency_valid CHECK (
        (chat_concurrency IN ('DEFAULT', 'SHARE_TASK') AND chat_concurrency_value IS NULL)
        OR (chat_concurrency = 'MANUAL' AND chat_concurrency_value BETWEEN 1 AND 64)
    ),
    ADD CONSTRAINT agent_config_versions_workdir_isolation_valid
        CHECK (workdir_isolation IN ('WORKTREE', 'DIRECT')),
    ADD CONSTRAINT agent_config_versions_approval_mode_valid
        CHECK (approval_mode ~ '^[A-Z][A-Z_]{1,31}$'),
    ADD CONSTRAINT agent_config_versions_collaboration_mode_valid
        CHECK (collaboration_mode ~ '^[A-Z][A-Z_]{1,31}$'),
    ADD CONSTRAINT agent_config_versions_custom_args_shape CHECK (
        jsonb_typeof(custom_args) = 'array' AND jsonb_array_length(custom_args) <= 64
        AND pg_column_size(custom_args) <= 16384
    ),
    ADD CONSTRAINT agent_config_versions_git_author_name_valid CHECK (
        git_author_name_mode IN ('USER_AND_AGENT', 'USER', 'OWNER_AND_AGENT', 'OWNER', 'MACHINE', 'MANUAL')
        AND length(git_author_name) <= 128
        AND (git_author_name_mode <> 'MANUAL' OR length(btrim(git_author_name)) > 0)
    ),
    ADD CONSTRAINT agent_config_versions_git_author_email_valid CHECK (
        git_author_email_mode IN ('USER', 'OWNER', 'MACHINE', 'MANUAL')
        AND length(git_author_email) <= 254
        AND (git_author_email_mode <> 'MANUAL' OR git_author_email ~ '^[^\s@]+@[^\s@]+\.[^\s@]+$')
    ),
    ADD CONSTRAINT agent_config_versions_env_digest_valid
        CHECK (env_digest = '' OR env_digest ~ '^[0-9a-f]{64}$');

-- 环境变量：只存密文；名称在同一 Agent 内唯一（大小写敏感，与进程环境一致）。
CREATE TABLE openfox.agent_env_vars (
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    name text NOT NULL,
    value_ciphertext bytea NOT NULL,
    value_nonce bytea NOT NULL,
    key_id text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    updated_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, agent_id, name),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, updated_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_env_vars_name_valid CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    CONSTRAINT agent_env_vars_ciphertext_size CHECK (octet_length(value_ciphertext) BETWEEN 16 AND 8208),
    CONSTRAINT agent_env_vars_nonce_size CHECK (octet_length(value_nonce) = 12),
    CONSTRAINT agent_env_vars_key_id_valid CHECK (key_id ~ '^[A-Za-z0-9_.-]{1,64}$'),
    CONSTRAINT agent_env_vars_version_positive CHECK (version > 0)
);

-- Agent 的 Git 仓库；为空时继承空间设置（空间仓库在阶段 3）。
CREATE TABLE openfox.agent_repos (
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    url text NOT NULL,
    base_branch text NOT NULL DEFAULT '',
    position integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_repos_url_valid CHECK (
        length(url) <= 2048 AND (url ~ '^https://[^\s]+$' OR url ~ '^(ssh://)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/][^\s]+$')
    ),
    CONSTRAINT agent_repos_branch_valid CHECK (length(base_branch) <= 255 AND base_branch !~ '\s'),
    CONSTRAINT agent_repos_position_valid CHECK (position >= 0 AND position < 64),
    CONSTRAINT agent_repos_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX agent_repos_url_unique ON openfox.agent_repos (organization_id, agent_id, url);

-- 收藏到侧栏（☆）：个人偏好，与目录置顶 agent_directory_pins 是两个概念。
CREATE TABLE openfox.agent_favorites (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid,
    crew_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, crew_id)
        REFERENCES openfox.agent_crews (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_favorites_one_target CHECK ((agent_id IS NULL) <> (crew_id IS NULL))
);

CREATE UNIQUE INDEX agent_favorites_agent_unique
    ON openfox.agent_favorites (organization_id, user_id, agent_id)
    WHERE agent_id IS NOT NULL;
CREATE UNIQUE INDEX agent_favorites_crew_unique
    ON openfox.agent_favorites (organization_id, user_id, crew_id)
    WHERE crew_id IS NOT NULL;

-- 小队：队长、指令、任务运行设置。
ALTER TABLE openfox.agent_crews
    ADD COLUMN leader_agent_id uuid,
    ADD COLUMN instructions text NOT NULL DEFAULT '',
    ADD COLUMN shared_task_dir boolean NOT NULL DEFAULT false;
ALTER TABLE openfox.agent_crews
    ADD CONSTRAINT agent_crews_leader_fk FOREIGN KEY (organization_id, leader_agent_id)
        REFERENCES openfox.agents (organization_id, id),
    ADD CONSTRAINT agent_crews_instructions_length CHECK (length(instructions) <= 100000);

ALTER TABLE openfox.agent_crew_members
    ADD COLUMN exclusive boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX agent_crew_members_exclusive_unique
    ON openfox.agent_crew_members (organization_id, agent_id)
    WHERE exclusive;

CREATE TABLE openfox.agent_crew_human_members (
    organization_id uuid NOT NULL,
    crew_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, crew_id, user_id),
    FOREIGN KEY (organization_id, crew_id)
        REFERENCES openfox.agent_crews (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id)
);

CREATE INDEX agent_crew_human_members_user_idx
    ON openfox.agent_crew_human_members (organization_id, user_id);

ALTER TABLE openfox.agent_env_vars ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_env_vars FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_env_vars_tenant_isolation ON openfox.agent_env_vars
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.agent_repos ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_repos FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_repos_tenant_isolation ON openfox.agent_repos
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.agent_favorites ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_favorites FORCE ROW LEVEL SECURITY;
-- 收藏是个人偏好：只有本人能读写自己的行。
CREATE POLICY agent_favorites_owner ON openfox.agent_favorites
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

ALTER TABLE openfox.agent_crew_human_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_crew_human_members FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_crew_human_members_tenant_isolation ON openfox.agent_crew_human_members
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT UPDATE (title, avatar, allow_assign, allow_direct_chat, trigger_crew, trigger_mention,
              trigger_workflow, profile_version)
    ON openfox.agents TO openfox_app;
GRANT UPDATE (space_id, owner_user_id, leader_agent_id, instructions, shared_task_dir)
    ON openfox.agent_crews TO openfox_app;
GRANT UPDATE (position, exclusive) ON openfox.agent_crew_members TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.agent_env_vars, openfox.agent_repos TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.agent_favorites, openfox.agent_crew_human_members TO openfox_app;
