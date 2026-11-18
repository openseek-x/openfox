-- 阶段 4a「组织与成员」：组织设置、软删除、任务优先级 4/5 级、邀请时加入空间、邀请邮件发送状态、邀请链接。
--
-- 仅扩展（expand-only）：
--   - organizations：description、图标（icon_kind icon / emoji / upload，与 000035 视图图标同一结构）、
--     default_locale（zh-CN / en-US）、priority_levels（4 / 5）、version、deleted_at / deleted_by（软删除）。
--   - organization_directory（控制面目录）：同步名称、slug、图标与 deleted_at；由 organizations 的更新触发器维护，
--     没有目录行的组织（例如测试夹具直接插入的）在第一次更新时补齐。
--   - resolve_organization / list_user_organizations / accept_invitation 排除已删除组织，删除对所有人立即生效。
--   - tasks / projects 的优先级增加 LOWEST（「最低」）；组织为 4 级时写入 LOWEST 由触发器拒绝（check_violation），
--     触发器对组织行加 FOR SHARE 锁，与 5 → 4 的切换（锁组织行后把 LOWEST 改为 LOW）互斥。
--   - invitations：space_ids（接受后加入的空间）、delivery_status（PENDING / SENT / FAILED）、last_sent_at。
--     accept_invitation 允许已移除（REVOKED）的成员凭新邀请重新加入，并加入仍然活跃的所选空间。
--   - invite_links：每个组织一条邀请链接（启用、角色 MEMBER / GUEST、加入空间、令牌哈希与密封后的令牌），FORCE RLS；
--     preview_invite_link / join_by_invite_link 两个 SECURITY DEFINER 函数凭令牌哈希预览与加入。
--   - 组织事件：organizations 更新发 organization.changed；成员与邀请变化发语句级 member.changed。
-- 锁：organizations / invitations 加可空列或常量默认值，不重写表；tasks / projects 换 CHECK 约束需扫描表
--   （与 000027 相同做法），大表上线时在低峰执行。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000037 条目。

SET ROLE openfox_migrator;

-- ===== 组织设置与软删除 =====

ALTER TABLE openfox.organizations
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN icon_kind text,
    ADD COLUMN icon text,
    ADD COLUMN icon_upload_id uuid,
    ADD COLUMN default_locale text NOT NULL DEFAULT 'zh-CN',
    ADD COLUMN priority_levels smallint NOT NULL DEFAULT 4,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid;
ALTER TABLE openfox.organizations
    ADD CONSTRAINT organizations_icon_upload_fk FOREIGN KEY (id, icon_upload_id)
        REFERENCES openfox.uploaded_icons (organization_id, id),
    ADD CONSTRAINT organizations_deleted_by_fk FOREIGN KEY (id, deleted_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    ADD CONSTRAINT organizations_description_length CHECK (length(description) <= 500),
    ADD CONSTRAINT organizations_icon_valid CHECK (
        (icon_kind IS NULL AND icon IS NULL AND icon_upload_id IS NULL)
        OR (icon_kind IN ('icon', 'emoji') AND length(icon) BETWEEN 1 AND 64 AND icon_upload_id IS NULL)
        OR (icon_kind = 'upload' AND icon IS NULL AND icon_upload_id IS NOT NULL)
    ),
    ADD CONSTRAINT organizations_default_locale_valid CHECK (default_locale IN ('zh-CN', 'en-US')),
    ADD CONSTRAINT organizations_priority_levels_valid CHECK (priority_levels IN (4, 5)),
    ADD CONSTRAINT organizations_version_positive CHECK (version > 0),
    ADD CONSTRAINT organizations_deleted_state_valid CHECK ((deleted_at IS NULL) = (deleted_by IS NULL));

ALTER TABLE openfox.organization_directory
    ADD COLUMN icon_kind text,
    ADD COLUMN icon text,
    ADD COLUMN icon_upload_id uuid,
    ADD COLUMN deleted_at timestamptz;

-- organizations 有 FORCE RLS，目录只能在组织自己的租户上下文里随更新同步。
CREATE OR REPLACE FUNCTION openfox_private.sync_organization_directory()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_directory (
        organization_id, slug, name, created_at, icon_kind, icon, icon_upload_id, deleted_at
    ) VALUES (
        NEW.id, NEW.slug, NEW.name, NEW.created_at, NEW.icon_kind, NEW.icon, NEW.icon_upload_id, NEW.deleted_at
    )
    ON CONFLICT (organization_id) DO UPDATE
       SET slug = EXCLUDED.slug, name = EXCLUDED.name, icon_kind = EXCLUDED.icon_kind,
           icon = EXCLUDED.icon, icon_upload_id = EXCLUDED.icon_upload_id, deleted_at = EXCLUDED.deleted_at;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.sync_organization_directory() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.sync_organization_directory() FROM PUBLIC;

CREATE TRIGGER organizations_sync_directory
AFTER UPDATE OF slug, name, icon_kind, icon, icon_upload_id, deleted_at ON openfox.organizations
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_organization_directory();

-- 已删除的组织：解析失败（所有请求按组织不存在处理）、不出现在组织列表里。
CREATE OR REPLACE FUNCTION openfox_private.resolve_organization(
    p_user_id uuid,
    p_organization_id uuid
) RETURNS TABLE (
    organization_id uuid,
    authorization_version bigint,
    role text,
    membership_status text
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT i.organization_id, i.authorization_version, i.role, i.membership_status
      FROM openfox.user_organization_index i
     WHERE i.user_id = p_user_id
       AND i.organization_id = p_organization_id
       AND NOT EXISTS (
           SELECT 1 FROM openfox.organization_directory d
            WHERE d.organization_id = i.organization_id AND d.deleted_at IS NOT NULL)
$$;

DROP FUNCTION openfox_private.list_user_organizations(uuid);
CREATE FUNCTION openfox_private.list_user_organizations(p_user_id uuid)
RETURNS TABLE (organization_id uuid, slug text, name text, role text, created_at timestamptz,
               icon_kind text, icon text, icon_upload_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT d.organization_id, d.slug, d.name, i.role, d.created_at, d.icon_kind, d.icon, d.icon_upload_id
      FROM openfox.user_organization_index i
      JOIN openfox.organization_directory d ON d.organization_id = i.organization_id
     WHERE i.user_id = p_user_id AND i.membership_status = 'ACTIVE' AND d.deleted_at IS NULL
     ORDER BY d.created_at, d.organization_id
     LIMIT 100
$$;
ALTER FUNCTION openfox_private.list_user_organizations(uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_user_organizations(uuid) FROM PUBLIC;

-- ===== 任务优先级 4 / 5 级 =====

ALTER TABLE openfox.tasks DROP CONSTRAINT tasks_priority_valid;
ALTER TABLE openfox.tasks ADD CONSTRAINT tasks_priority_valid
    CHECK (priority IN ('NONE', 'LOWEST', 'LOW', 'NORMAL', 'HIGH', 'URGENT'));
ALTER TABLE openfox.projects DROP CONSTRAINT projects_priority_valid;
ALTER TABLE openfox.projects ADD CONSTRAINT projects_priority_valid
    CHECK (priority IN ('NONE', 'LOWEST', 'LOW', 'NORMAL', 'HIGH', 'URGENT'));

-- 组织为 4 级时不允许写入 LOWEST。FOR SHARE 与切换档位时对组织行的更新互斥，避免切换后残留 LOWEST。
CREATE OR REPLACE FUNCTION openfox_private.enforce_priority_levels()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    levels smallint;
BEGIN
    SELECT o.priority_levels INTO levels
      FROM openfox.organizations o
     WHERE o.id = NEW.organization_id
     FOR SHARE;
    IF levels IS DISTINCT FROM 5 THEN
        RAISE EXCEPTION 'priority_level_unavailable'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'priority_levels';
    END IF;
    RETURN NEW;
END
$$;
ALTER FUNCTION openfox_private.enforce_priority_levels() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enforce_priority_levels() FROM PUBLIC;

CREATE TRIGGER tasks_enforce_priority_levels
BEFORE INSERT OR UPDATE OF priority ON openfox.tasks
FOR EACH ROW WHEN (NEW.priority = 'LOWEST')
EXECUTE FUNCTION openfox_private.enforce_priority_levels();
CREATE TRIGGER projects_enforce_priority_levels
BEFORE INSERT OR UPDATE OF priority ON openfox.projects
FOR EACH ROW WHEN (NEW.priority = 'LOWEST')
EXECUTE FUNCTION openfox_private.enforce_priority_levels();

-- ===== 邀请：加入空间与发送状态 =====

ALTER TABLE openfox.invitations
    ADD COLUMN space_ids uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN delivery_status text NOT NULL DEFAULT 'PENDING',
    ADD COLUMN last_sent_at timestamptz;
ALTER TABLE openfox.invitations
    ADD CONSTRAINT invitations_space_ids_size CHECK (cardinality(space_ids) <= 50),
    ADD CONSTRAINT invitations_delivery_status_valid CHECK (delivery_status IN ('PENDING', 'SENT', 'FAILED'));

-- 把成员加入仍然活跃的空间（已存在的空间成员关系改回 ACTIVE）。调用方已安装该组织的租户上下文。
CREATE OR REPLACE FUNCTION openfox_private.join_invited_spaces(
    p_organization_id uuid,
    p_user_id uuid,
    p_space_ids uuid[]
) RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
    SELECT s.organization_id, s.id, p_user_id, 'MEMBER', 'ACTIVE'
      FROM openfox.spaces s
     WHERE s.organization_id = p_organization_id
       AND s.id = ANY (p_space_ids)
       AND s.status = 'ACTIVE' AND s.deleted_at IS NULL
    ON CONFLICT (organization_id, space_id, user_id) DO UPDATE
       SET status = 'ACTIVE'
     WHERE openfox.space_members.status <> 'ACTIVE'
$$;
ALTER FUNCTION openfox_private.join_invited_spaces(uuid, uuid, uuid[]) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.join_invited_spaces(uuid, uuid, uuid[]) FROM PUBLIC;

-- 在 000005 的基础上：已删除组织的邀请视为不存在；已移除（REVOKED）的成员可以凭新邀请重新加入；
-- 接受后加入邀请所选的空间。其余规则不变。
CREATE OR REPLACE FUNCTION openfox_private.accept_invitation(
    p_invitation_id uuid,
    p_token_hash bytea,
    p_actor_id uuid DEFAULT NULL,
    p_password_hash text DEFAULT NULL
) RETURNS TABLE (
    user_id uuid,
    organization_id uuid,
    role text,
    status text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
-- 输出列 user_id / organization_id / role / status 与表列同名；000005 的版本在已登录接受时因此报
-- "column reference is ambiguous"。这里统一按表列解析（函数内不把输出列当变量使用）。
#variable_conflict use_column
DECLARE
    invitation_row openfox.invitations%ROWTYPE;
    target_user_id uuid;
    credential_status text;
    membership_status text;
BEGIN
    PERFORM openfox_private.set_invitation_lookup(p_invitation_id, p_token_hash);

    IF p_invitation_id IS NULL
       OR p_token_hash IS NULL
       OR octet_length(p_token_hash) <> 32 THEN
        RAISE EXCEPTION 'invitation_not_found' USING ERRCODE = 'P0002';
    END IF;

    SELECT *
      INTO invitation_row
      FROM openfox.invitations
     WHERE id = p_invitation_id
       AND token_hash = p_token_hash
     FOR UPDATE;

    IF NOT FOUND OR EXISTS (
        SELECT 1 FROM openfox.organization_directory d
         WHERE d.organization_id = invitation_row.organization_id AND d.deleted_at IS NOT NULL) THEN
        RAISE EXCEPTION 'invitation_not_found' USING ERRCODE = 'P0002';
    END IF;

    IF invitation_row.status <> 'PENDING' THEN
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    END IF;

    IF invitation_row.expires_at <= clock_timestamp() THEN
        UPDATE openfox.invitations
           SET status = 'EXPIRED'
         WHERE id = invitation_row.id AND status = 'PENDING';
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    END IF;

    IF p_actor_id IS NULL THEN
        SELECT id
          INTO target_user_id
          FROM openfox.users
         WHERE normalized_email = invitation_row.normalized_email
         FOR UPDATE;

        IF FOUND THEN
            RAISE EXCEPTION 'invitation_auth_required' USING ERRCODE = 'P0001';
        END IF;

        IF p_password_hash IS NULL OR btrim(p_password_hash) = '' THEN
            RAISE EXCEPTION 'invitation_password_required' USING ERRCODE = 'P0001';
        END IF;

        INSERT INTO openfox.users (email)
        VALUES (invitation_row.normalized_email)
        ON CONFLICT (normalized_email) DO NOTHING
        RETURNING id INTO target_user_id;

        IF target_user_id IS NULL THEN
            SELECT id
              INTO target_user_id
              FROM openfox.users
             WHERE normalized_email = invitation_row.normalized_email
             FOR UPDATE;
            RAISE EXCEPTION 'invitation_auth_required' USING ERRCODE = 'P0001';
        END IF;

        INSERT INTO openfox.password_credentials (user_id, password_hash)
        VALUES (target_user_id, p_password_hash)
        ON CONFLICT (user_id) DO NOTHING;

        SELECT status
          INTO credential_status
          FROM openfox.password_credentials
         WHERE user_id = target_user_id
         FOR UPDATE;
        IF credential_status IS DISTINCT FROM 'ACTIVE' THEN
            RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
        END IF;
    ELSE
        SELECT id
          INTO target_user_id
          FROM openfox.users
         WHERE normalized_email = invitation_row.normalized_email
         FOR UPDATE;

        IF NOT FOUND OR target_user_id <> p_actor_id THEN
            RAISE EXCEPTION 'invitation_email_mismatch' USING ERRCODE = 'P0001';
        END IF;

        IF p_password_hash IS NOT NULL AND btrim(p_password_hash) <> '' THEN
            RAISE EXCEPTION 'invitation_parameter_conflict' USING ERRCODE = 'P0001';
        END IF;

        SELECT status
          INTO credential_status
          FROM openfox.password_credentials
         WHERE user_id = target_user_id
         FOR UPDATE;
        IF credential_status IS DISTINCT FROM 'ACTIVE' THEN
            RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
        END IF;
    END IF;

    PERFORM openfox_private.set_tenant_context(
        invitation_row.organization_id,
        target_user_id,
        1,
        'user_request'
    );

    SELECT m.status
      INTO membership_status
      FROM openfox.organization_members m
     WHERE m.organization_id = invitation_row.organization_id
       AND m.user_id = target_user_id
     FOR UPDATE;

    IF membership_status = 'ACTIVE' THEN
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    ELSIF membership_status IN ('INVITED', 'REVOKED') THEN
        UPDATE openfox.organization_members m
           SET role = invitation_row.role,
               status = 'ACTIVE',
               authorization_version = m.authorization_version + 1
         WHERE m.organization_id = invitation_row.organization_id
           AND m.user_id = target_user_id;
    ELSE
        INSERT INTO openfox.organization_members (
            organization_id, user_id, role, status
        ) VALUES (
            invitation_row.organization_id, target_user_id, invitation_row.role, 'ACTIVE'
        );
    END IF;

    PERFORM openfox_private.join_invited_spaces(
        invitation_row.organization_id, target_user_id, invitation_row.space_ids);

    UPDATE openfox.invitations
       SET status = 'ACCEPTED', consumed_at = clock_timestamp()
     WHERE id = invitation_row.id AND status = 'PENDING';

    RETURN QUERY SELECT target_user_id, invitation_row.organization_id,
                        invitation_row.role, 'ACCEPTED'::text;
END
$$;

-- ===== 邀请链接 =====

CREATE TABLE openfox.invite_links (
    organization_id uuid PRIMARY KEY REFERENCES openfox.organizations (id),
    enabled boolean NOT NULL DEFAULT false,
    role text NOT NULL DEFAULT 'MEMBER',
    space_ids uuid[] NOT NULL DEFAULT '{}',
    token_hash bytea NOT NULL,
    token_ciphertext bytea NOT NULL,
    token_nonce bytea NOT NULL,
    token_key_id text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (organization_id, updated_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT invite_links_role_valid CHECK (role IN ('MEMBER', 'GUEST')),
    CONSTRAINT invite_links_space_ids_size CHECK (cardinality(space_ids) <= 50),
    CONSTRAINT invite_links_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT invite_links_ciphertext_size CHECK (octet_length(token_ciphertext) BETWEEN 16 AND 256),
    CONSTRAINT invite_links_nonce_size CHECK (octet_length(token_nonce) = 12),
    CONSTRAINT invite_links_key_id_valid CHECK (token_key_id ~ '^[A-Za-z0-9_.-]{1,64}$'),
    CONSTRAINT invite_links_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX invite_links_token_hash_unique ON openfox.invite_links (token_hash);

ALTER TABLE openfox.invite_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.invite_links FORCE ROW LEVEL SECURITY;
CREATE POLICY invite_links_tenant_isolation ON openfox.invite_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 预览与加入在建立租户上下文之前按令牌哈希查找链接；沿用 *_dispatch_scan 模式，只给 migrator 只读策略。
CREATE POLICY invite_links_token_lookup ON openfox.invite_links
    FOR SELECT TO openfox_migrator USING (true);

-- 预览邀请链接（加入页）。链接未启用、组织已删除时没有结果。会安装组织的租户上下文读组织信息、成员数与 Agent 数，结束前清除。
CREATE OR REPLACE FUNCTION openfox_private.preview_invite_link(p_token_hash bytea, p_user_id uuid)
RETURNS TABLE (organization_id uuid, name text, icon_kind text, icon text, icon_upload_id uuid,
               role text, inviter_email text, member_count integer, agent_count integer, already_member boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    link openfox.invite_links%ROWTYPE;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32 OR p_user_id IS NULL THEN
        RETURN;
    END IF;
    SELECT * INTO link FROM openfox.invite_links l WHERE l.token_hash = p_token_hash AND l.enabled;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    PERFORM openfox_private.set_tenant_context(link.organization_id,
        '00000000-0000-0000-0000-000000000000', 1, 'organization_worker');
    RETURN QUERY
    SELECT o.id, o.name, o.icon_kind, o.icon, o.icon_upload_id, link.role,
           (SELECT u.email FROM openfox.users u WHERE u.id = link.updated_by),
           (SELECT count(*)::integer FROM openfox.organization_members m
             WHERE m.organization_id = o.id AND m.status = 'ACTIVE'),
           (SELECT count(*)::integer FROM openfox.agents a
             WHERE a.organization_id = o.id AND a.archived_at IS NULL),
           EXISTS (SELECT 1 FROM openfox.organization_members m
                    WHERE m.organization_id = o.id AND m.user_id = p_user_id AND m.status = 'ACTIVE')
      FROM openfox.organizations o
     WHERE o.id = link.organization_id AND o.deleted_at IS NULL;
    TRUNCATE TABLE pg_temp.openfox_tenant_context;
END
$$;
ALTER FUNCTION openfox_private.preview_invite_link(bytea, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.preview_invite_link(bytea, uuid) FROM PUBLIC;

-- 凭邀请链接加入组织：已是成员时返回 ALREADY_MEMBER 不做改动；否则以链接角色加入（INVITED / REVOKED 改回 ACTIVE），
-- 并加入链接所选的活跃空间。链接行加 FOR SHARE，与重置 / 停用互斥。返回时保留租户上下文供调用方写审计。
CREATE OR REPLACE FUNCTION openfox_private.join_by_invite_link(p_token_hash bytea, p_user_id uuid)
RETURNS TABLE (organization_id uuid, role text, status text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    link openfox.invite_links%ROWTYPE;
    membership_status text;
    membership_role text;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32 OR p_user_id IS NULL THEN
        RAISE EXCEPTION 'invite_link_not_found' USING ERRCODE = 'P0002';
    END IF;
    -- 先按令牌哈希找到组织（migrator 只读策略），装好租户上下文后再加锁读取：FOR SHARE 同时要求通过租户策略。
    SELECT * INTO link FROM openfox.invite_links l WHERE l.token_hash = p_token_hash AND l.enabled;
    IF NOT FOUND OR EXISTS (
        SELECT 1 FROM openfox.organization_directory d
         WHERE d.organization_id = link.organization_id AND d.deleted_at IS NOT NULL)
       OR NOT EXISTS (SELECT 1 FROM openfox.users u WHERE u.id = p_user_id) THEN
        RAISE EXCEPTION 'invite_link_not_found' USING ERRCODE = 'P0002';
    END IF;

    PERFORM openfox_private.set_tenant_context(link.organization_id, p_user_id, 1, 'user_request');

    SELECT * INTO link FROM openfox.invite_links l
     WHERE l.organization_id = link.organization_id AND l.token_hash = p_token_hash AND l.enabled
     FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invite_link_not_found' USING ERRCODE = 'P0002';
    END IF;

    SELECT m.status, m.role INTO membership_status, membership_role
      FROM openfox.organization_members m
     WHERE m.organization_id = link.organization_id AND m.user_id = p_user_id
     FOR UPDATE;

    IF membership_status = 'ACTIVE' THEN
        RETURN QUERY SELECT link.organization_id, membership_role, 'ALREADY_MEMBER'::text;
        RETURN;
    ELSIF membership_status IN ('INVITED', 'REVOKED') THEN
        UPDATE openfox.organization_members m
           SET role = link.role, status = 'ACTIVE', authorization_version = m.authorization_version + 1
         WHERE m.organization_id = link.organization_id AND m.user_id = p_user_id;
    ELSE
        INSERT INTO openfox.organization_members (organization_id, user_id, role, status)
        VALUES (link.organization_id, p_user_id, link.role, 'ACTIVE');
    END IF;

    PERFORM openfox_private.join_invited_spaces(link.organization_id, p_user_id, link.space_ids);

    RETURN QUERY SELECT link.organization_id, link.role, 'JOINED'::text;
END
$$;
ALTER FUNCTION openfox_private.join_by_invite_link(bytea, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.join_by_invite_link(bytea, uuid) FROM PUBLIC;

-- ===== 组织事件 =====

CREATE OR REPLACE FUNCTION openfox_private.emit_organization_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, version)
    VALUES (NEW.id, 'organization.changed', 'organization', NEW.id, NEW.version);
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_organization_event() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_organization_event() FROM PUBLIC;

CREATE TRIGGER organizations_emit_event
AFTER UPDATE OF name, slug, description, icon_kind, icon, icon_upload_id, default_locale, priority_levels, deleted_at
ON openfox.organizations
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_organization_event();

-- 成员与邀请变化：语句级，每个组织一条 member.changed（不带载荷，前端据此刷新成员页）。
CREATE OR REPLACE FUNCTION openfox_private.emit_member_event_new()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity)
    SELECT DISTINCT c.organization_id, 'member.changed', 'organization' FROM changed_new c;
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.emit_member_event_old()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity)
    SELECT DISTINCT c.organization_id, 'member.changed', 'organization' FROM changed_old c;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_member_event_new() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_member_event_old() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_member_event_new() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_member_event_old() FROM PUBLIC;

CREATE TRIGGER organization_members_emit_insert AFTER INSERT ON openfox.organization_members
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_member_event_new();
CREATE TRIGGER organization_members_emit_update AFTER UPDATE ON openfox.organization_members
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_member_event_new();
CREATE TRIGGER organization_members_emit_delete AFTER DELETE ON openfox.organization_members
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_member_event_old();
CREATE TRIGGER invitations_emit_insert AFTER INSERT ON openfox.invitations
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_member_event_new();
CREATE TRIGGER invitations_emit_update AFTER UPDATE ON openfox.invitations
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_member_event_new();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.invite_links TO openfox_app;
-- 修正 000005 的遗漏：invitations 的 RLS 策略调用 invitation_lookup_matches，应用角色没有执行权限时
-- INSERT … RETURNING / UPDATE … RETURNING 会报 permission denied。函数只判断本会话查找表里有没有这一行，
-- 查找表只能由 accept_invitation（SECURITY DEFINER）写入，授权不扩大可见范围。
GRANT EXECUTE ON FUNCTION openfox_private.invitation_lookup_matches(uuid, bytea) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_user_organizations(uuid) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.preview_invite_link(bytea, uuid) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.join_by_invite_link(bytea, uuid) TO openfox_app;
