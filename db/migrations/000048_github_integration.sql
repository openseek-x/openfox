-- 阶段 6b-1「GitHub：OAuth 绑定、Webhook、PR / Commit 关联」。
--
-- 仅扩展（expand-only）：
--   - integration_connections 增加 webhook_token_hash（组织连接的 Webhook 地址令牌的 SHA-256，部分唯一）与
--     webhook_last_delivery_at（最近一次收到有效事件的时间，界面「Webhook 已接通」）。
--     访问令牌与 Webhook 签名密钥都在 credentials_sealed（信封加密的 JSON）里，接口不返回访问令牌。
--   - integration_oauth_states：OAuth 授权会话。state 只存 SHA-256，10 分钟有效、只能用一次（consumed_at）；
--     绑定组织时回调拿到的令牌加密暂存在 pending_sealed，等管理员勾选要绑定的组织 / 账号后清除。
--   - integration_provider_settings：服务商级设置（「关联 Pull Request / Git Commit」「Pull Request 回链」开关、分支格式）。
--   - task_git_links：任务与 Pull Request / Commit 的关联（按任务编号匹配，PR 状态随 Webhook 更新）。
--   - integration_webhook_deliveries：Webhook 投递记录，按 (连接, X-GitHub-Delivery) 唯一去重。
--   - resolve_git_webhook / resolve_integration_oauth_state：控制面函数（只返回组织与对象 ID），配套只读扫描策略。
-- RLS 与其他租户表相同；openfox_app 可读写（投递记录只 SELECT / INSERT / UPDATE）。
-- 锁：integration_connections 加两列（可空、无默认）与部分唯一索引（表在 6a 才上线，行数很少）；其余只建新表与函数。
-- 回滚：应用回退后新表与新列闲置即可。确需移除时 DROP 两个函数与扫描策略，DROP TABLE integration_webhook_deliveries,
--   task_git_links, integration_provider_settings, integration_oauth_states，再删除 integration_connections 的两列与索引。

SET ROLE openfox_migrator;

ALTER TABLE openfox.integration_connections ADD COLUMN webhook_token_hash bytea;
ALTER TABLE openfox.integration_connections ADD COLUMN webhook_last_delivery_at timestamptz;
ALTER TABLE openfox.integration_connections
    ADD CONSTRAINT integration_connections_webhook_token_valid CHECK (webhook_token_hash IS NULL OR octet_length(webhook_token_hash) = 32) NOT VALID;
ALTER TABLE openfox.integration_connections VALIDATE CONSTRAINT integration_connections_webhook_token_valid;
CREATE UNIQUE INDEX integration_connections_webhook_token_unique
    ON openfox.integration_connections (webhook_token_hash) WHERE webhook_token_hash IS NOT NULL;

CREATE TABLE openfox.integration_oauth_states (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    purpose text NOT NULL,
    user_id uuid NOT NULL,
    state_hash bytea NOT NULL,
    code_verifier_sealed bytea,
    pending_sealed bytea,
    pending_accounts jsonb NOT NULL DEFAULT '[]',
    connection_id uuid,
    status text NOT NULL DEFAULT 'PENDING',
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '10 minutes',
    consumed_at timestamptz,
    completed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    CONSTRAINT integration_oauth_states_provider_valid CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT integration_oauth_states_purpose_valid CHECK (purpose IN ('ORGANIZATION', 'PERSONAL', 'REAUTHORIZE')),
    -- PENDING = 等待回调；SELECTING = 回调成功、等待管理员勾选组织；COMPLETED / FAILED / EXPIRED 为终态。
    CONSTRAINT integration_oauth_states_status_valid CHECK (status IN ('PENDING', 'SELECTING', 'COMPLETED', 'FAILED', 'EXPIRED')),
    CONSTRAINT integration_oauth_states_state_hash_valid CHECK (octet_length(state_hash) = 32),
    CONSTRAINT integration_oauth_states_sealed_size CHECK (
        (code_verifier_sealed IS NULL OR octet_length(code_verifier_sealed) <= 4096)
        AND (pending_sealed IS NULL OR octet_length(pending_sealed) <= 16384)),
    CONSTRAINT integration_oauth_states_pending_shape CHECK (jsonb_typeof(pending_accounts) = 'array' AND pg_column_size(pending_accounts) <= 65536),
    CONSTRAINT integration_oauth_states_error_valid CHECK (char_length(error_code) <= 64)
);
CREATE UNIQUE INDEX integration_oauth_states_state_unique ON openfox.integration_oauth_states (state_hash);
CREATE INDEX integration_oauth_states_expiry_idx ON openfox.integration_oauth_states (organization_id, expires_at)
    WHERE status IN ('PENDING', 'SELECTING');

CREATE TABLE openfox.integration_provider_settings (
    organization_id uuid NOT NULL,
    provider text NOT NULL,
    settings jsonb NOT NULL DEFAULT '{}',
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, provider),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT integration_provider_settings_provider_valid CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT integration_provider_settings_shape CHECK (jsonb_typeof(settings) = 'object' AND pg_column_size(settings) <= 16384),
    CONSTRAINT integration_provider_settings_version_positive CHECK (version > 0)
);

CREATE TABLE openfox.task_git_links (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    provider text NOT NULL,
    kind text NOT NULL,
    repository text NOT NULL,
    repository_private boolean NOT NULL DEFAULT false,
    number integer,
    sha text NOT NULL DEFAULT '',
    external_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    url text NOT NULL,
    state text NOT NULL,
    author_login text NOT NULL DEFAULT '',
    author_user_id uuid,
    head_branch text NOT NULL DEFAULT '',
    matched_in text NOT NULL DEFAULT '',
    backlink_comment_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    merged_at timestamptz,
    closed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id) REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    FOREIGN KEY (organization_id, author_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_git_links_provider_valid CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT task_git_links_kind_valid CHECK (kind IN ('PULL_REQUEST', 'COMMIT')),
    CONSTRAINT task_git_links_shape CHECK (
        (kind = 'PULL_REQUEST' AND number IS NOT NULL AND number > 0 AND state IN ('OPEN', 'DRAFT', 'CLOSED', 'MERGED'))
        OR (kind = 'COMMIT' AND number IS NULL AND sha ~ '^[0-9a-f]{7,64}$' AND state = 'COMMITTED')),
    CONSTRAINT task_git_links_text_valid CHECK (
        char_length(btrim(repository)) BETWEEN 1 AND 255 AND char_length(external_id) <= 128
        AND char_length(title) <= 500 AND char_length(url) BETWEEN 1 AND 2048
        AND char_length(author_login) <= 255 AND char_length(head_branch) <= 255
        AND matched_in IN ('', 'title', 'body', 'branch', 'message') AND char_length(backlink_comment_id) <= 64)
);
CREATE UNIQUE INDEX task_git_links_pr_unique ON openfox.task_git_links (organization_id, task_id, provider, lower(repository), number)
    WHERE kind = 'PULL_REQUEST';
CREATE UNIQUE INDEX task_git_links_commit_unique ON openfox.task_git_links (organization_id, task_id, provider, lower(repository), sha)
    WHERE kind = 'COMMIT';
CREATE INDEX task_git_links_task_idx ON openfox.task_git_links (organization_id, task_id, created_at);
CREATE INDEX task_git_links_pr_idx ON openfox.task_git_links (organization_id, provider, lower(repository), number)
    WHERE kind = 'PULL_REQUEST';

CREATE TABLE openfox.integration_webhook_deliveries (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    provider text NOT NULL,
    delivery_id text NOT NULL,
    event text NOT NULL,
    action text NOT NULL DEFAULT '',
    signature_status text NOT NULL,
    status text NOT NULL,
    detail text NOT NULL DEFAULT '',
    linked_count integer NOT NULL DEFAULT 0,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    CONSTRAINT integration_webhook_deliveries_status_valid CHECK (status IN ('PROCESSED', 'IGNORED', 'REJECTED', 'FAILED')),
    CONSTRAINT integration_webhook_deliveries_text_valid CHECK (
        char_length(btrim(delivery_id)) BETWEEN 1 AND 128 AND char_length(event) BETWEEN 1 AND 64
        AND char_length(action) <= 64 AND char_length(signature_status) <= 32 AND char_length(detail) <= 500
        AND linked_count >= 0)
);
CREATE UNIQUE INDEX integration_webhook_deliveries_unique ON openfox.integration_webhook_deliveries (organization_id, connection_id, delivery_id);
CREATE INDEX integration_webhook_deliveries_recent_idx ON openfox.integration_webhook_deliveries (organization_id, connection_id, received_at DESC);

ALTER TABLE openfox.integration_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_oauth_states FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_provider_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_provider_settings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_git_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_git_links FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_webhook_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY integration_oauth_states_tenant_isolation ON openfox.integration_oauth_states
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_provider_settings_tenant_isolation ON openfox.integration_provider_settings
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_git_links_tenant_isolation ON openfox.task_git_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_webhook_deliveries_tenant_isolation ON openfox.integration_webhook_deliveries
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 控制面函数（以 openfox_migrator 身份执行）只读。
CREATE POLICY integration_connections_scan ON openfox.integration_connections FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY integration_oauth_states_scan ON openfox.integration_oauth_states FOR SELECT TO openfox_migrator USING (true);

-- Webhook 地址令牌 → 组织与连接（只返回未断开的组织连接）。
CREATE OR REPLACE FUNCTION openfox_private.resolve_git_webhook(p_token_hash bytea)
RETURNS TABLE (organization_id uuid, connection_id uuid, provider text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT c.organization_id, c.id, c.provider
      FROM openfox.integration_connections c
      JOIN openfox.organization_directory d ON d.organization_id = c.organization_id AND d.deleted_at IS NULL
     WHERE p_token_hash IS NOT NULL AND octet_length(p_token_hash) = 32
       AND c.webhook_token_hash = p_token_hash AND c.scope = 'ORGANIZATION' AND c.status <> 'DISCONNECTED'
$$;
ALTER FUNCTION openfox_private.resolve_git_webhook(bytea) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_git_webhook(bytea) FROM PUBLIC;

-- OAuth 回调：state 哈希 → 组织与授权会话（只返回未过期、等待回调的会话）。
CREATE OR REPLACE FUNCTION openfox_private.resolve_integration_oauth_state(p_state_hash bytea)
RETURNS TABLE (organization_id uuid, state_id uuid, provider text, purpose text, user_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT s.organization_id, s.id, s.provider, s.purpose, s.user_id
      FROM openfox.integration_oauth_states s
      JOIN openfox.organization_directory d ON d.organization_id = s.organization_id AND d.deleted_at IS NULL
     WHERE p_state_hash IS NOT NULL AND octet_length(p_state_hash) = 32
       AND s.state_hash = p_state_hash AND s.status = 'PENDING' AND s.consumed_at IS NULL
       AND s.expires_at > clock_timestamp()
$$;
ALTER FUNCTION openfox_private.resolve_integration_oauth_state(bytea) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_integration_oauth_state(bytea) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.integration_oauth_states, openfox.integration_provider_settings,
    openfox.task_git_links, openfox.integration_webhook_deliveries TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_git_webhook(bytea) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_integration_oauth_state(bytea) TO openfox_context;
