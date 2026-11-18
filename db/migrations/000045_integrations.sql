-- 阶段 6a「外部应用集成」基础：连接与同步关联（服务商目录是代码里的静态清单，不入库）。
--
-- 仅扩展（expand-only）：
--   - integration_connections：组织级连接（ORGANIZATION，例如 Jira 实例、GitHub 组织、飞书企业应用）与个人连接
--     （PERSONAL，例如个人 Jira / GitHub 账号，parent_connection_id 指向所属组织连接，可空）。
--     凭据只以信封加密形式保存（credentials_sealed，secrets.Box，AAD = integration:<org>:<connection>），接口从不返回；
--     credentials_hint 只存末四位等提示。状态 ACTIVE / NEEDS_ATTENTION / DISCONNECTED；断开不删任何任务与专项。
--     同一组织同一服务商同一外部账号（external_id）只能有一个未断开的组织连接；同一成员在同一组织连接下只能有一个未断开的个人连接。
--   - integration_sync_links：外部项目 / 仓库 ↔ 空间的同步关联（方向 INBOUND = 外部 → OpenFox，BIDIRECTIONAL = 双向），
--     具体同步逻辑由各服务商子阶段实现；同一连接下同一外部项目与空间只有一条未删除的关联。
-- RLS 与其他租户表相同；openfox_app 可读写。
-- 锁：只建新表。回滚：应用回退后新表闲置即可；确需移除时 DROP TABLE integration_sync_links, integration_connections。

SET ROLE openfox_migrator;

CREATE TABLE openfox.integration_connections (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    scope text NOT NULL,
    parent_connection_id uuid,
    owner_user_id uuid,
    external_id text NOT NULL,
    display_name text NOT NULL,
    account_kind text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'ACTIVE',
    status_reason text NOT NULL DEFAULT '',
    credentials_sealed bytea,
    credentials_key_id text NOT NULL DEFAULT '',
    credentials_hint text NOT NULL DEFAULT '',
    settings jsonb NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    disconnected_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, parent_connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    FOREIGN KEY (organization_id, owner_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT integration_connections_provider_valid CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT integration_connections_scope_valid CHECK (
        (scope = 'ORGANIZATION' AND owner_user_id IS NULL AND parent_connection_id IS NULL)
        OR (scope = 'PERSONAL' AND owner_user_id IS NOT NULL)
    ),
    CONSTRAINT integration_connections_status_valid CHECK (status IN ('ACTIVE', 'NEEDS_ATTENTION', 'DISCONNECTED')),
    CONSTRAINT integration_connections_disconnected_valid CHECK ((status = 'DISCONNECTED') = (disconnected_at IS NOT NULL)),
    CONSTRAINT integration_connections_text_valid CHECK (
        char_length(btrim(external_id)) BETWEEN 1 AND 255
        AND char_length(btrim(display_name)) BETWEEN 1 AND 200
        AND char_length(account_kind) <= 32
        AND char_length(status_reason) <= 500
        AND char_length(credentials_key_id) <= 64
        AND char_length(credentials_hint) <= 32
    ),
    CONSTRAINT integration_connections_credentials_size CHECK (credentials_sealed IS NULL OR octet_length(credentials_sealed) <= 16384),
    CONSTRAINT integration_connections_settings_shape CHECK (jsonb_typeof(settings) = 'object' AND pg_column_size(settings) <= 16384),
    CONSTRAINT integration_connections_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX integration_connections_org_unique
    ON openfox.integration_connections (organization_id, provider, lower(external_id))
    WHERE scope = 'ORGANIZATION' AND status <> 'DISCONNECTED';
CREATE UNIQUE INDEX integration_connections_personal_unique
    ON openfox.integration_connections (organization_id, provider, owner_user_id, COALESCE(parent_connection_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE scope = 'PERSONAL' AND status <> 'DISCONNECTED';
CREATE INDEX integration_connections_provider_idx ON openfox.integration_connections (organization_id, provider, created_at);

CREATE TABLE openfox.integration_sync_links (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    space_id uuid NOT NULL,
    external_project_id text NOT NULL,
    external_project_name text NOT NULL,
    direction text NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    status_reason text NOT NULL DEFAULT '',
    settings jsonb NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT integration_sync_links_direction_valid CHECK (direction IN ('INBOUND', 'BIDIRECTIONAL')),
    CONSTRAINT integration_sync_links_status_valid CHECK (status IN ('ACTIVE', 'PAUSED', 'NEEDS_ATTENTION')),
    CONSTRAINT integration_sync_links_text_valid CHECK (
        char_length(btrim(external_project_id)) BETWEEN 1 AND 255
        AND char_length(btrim(external_project_name)) BETWEEN 1 AND 255
        AND char_length(status_reason) <= 500
    ),
    CONSTRAINT integration_sync_links_settings_shape CHECK (jsonb_typeof(settings) = 'object' AND pg_column_size(settings) <= 16384),
    CONSTRAINT integration_sync_links_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX integration_sync_links_unique
    ON openfox.integration_sync_links (organization_id, connection_id, lower(external_project_id), space_id)
    WHERE deleted_at IS NULL;
CREATE INDEX integration_sync_links_space_idx ON openfox.integration_sync_links (organization_id, space_id) WHERE deleted_at IS NULL;

ALTER TABLE openfox.integration_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_sync_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_sync_links FORCE ROW LEVEL SECURITY;
CREATE POLICY integration_connections_tenant_isolation ON openfox.integration_connections
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_sync_links_tenant_isolation ON openfox.integration_sync_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.integration_connections, openfox.integration_sync_links TO openfox_app;
