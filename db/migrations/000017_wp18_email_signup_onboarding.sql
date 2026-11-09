-- WP-18 email-code self sign-up and first-organization onboarding.
--
-- Runtime roles still cannot write identity tables or bypass tenant RLS. Two
-- narrow SECURITY DEFINER functions expose exactly the writes these flows
-- need; both validate their inputs and are idempotent under retries.

SET ROLE openfox_migrator;

-- Email-code-only accounts carry a sentinel credential so session revocation
-- (credential_version / status) keeps working. The sentinel is not an Argon2id
-- encoding, so password login always fails for these accounts.
CREATE OR REPLACE FUNCTION openfox_private.ensure_email_user(p_email text)
RETURNS TABLE (user_id uuid, credential_version bigint, status text, created boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
#variable_conflict use_column
DECLARE
    normalized text := lower(btrim(p_email));
    existing_id uuid;
    inserted boolean := false;
BEGIN
    IF normalized IS NULL OR length(normalized) < 3 OR length(normalized) > 320
       OR position('@' in normalized) < 2 THEN
        RAISE EXCEPTION 'invalid email' USING ERRCODE = '22023';
    END IF;

    SELECT u.id INTO existing_id FROM openfox.users u WHERE u.normalized_email = normalized;
    IF existing_id IS NULL THEN
        -- 唯一索引 users_normalized_email_unique 保证并发注册只会成功一次。
        INSERT INTO openfox.users (email) VALUES (normalized)
        ON CONFLICT DO NOTHING
        RETURNING id INTO existing_id;
        IF existing_id IS NULL THEN
            SELECT u.id INTO existing_id FROM openfox.users u WHERE u.normalized_email = normalized;
        ELSE
            inserted := true;
        END IF;
    END IF;

    INSERT INTO openfox.password_credentials (user_id, password_hash)
    VALUES (existing_id, '!email-code-only')
    ON CONFLICT ON CONSTRAINT password_credentials_pkey DO NOTHING;

    RETURN QUERY
    SELECT pc.user_id, pc.credential_version, pc.status, inserted
      FROM openfox.password_credentials pc
     WHERE pc.user_id = existing_id;
END
$$;

ALTER FUNCTION openfox_private.ensure_email_user(text) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.ensure_email_user(text) FROM PUBLIC;

-- Creates an organization with the caller as its sole OWNER, seeds the default
-- task statuses and capacity/runtime rows, and records an audit event. The
-- caller-supplied idempotency key makes retries return the same organization.
CREATE TABLE openfox.organization_creations (
    user_id uuid NOT NULL REFERENCES openfox.users (id),
    idempotency_key text NOT NULL,
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    request_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, idempotency_key),
    CONSTRAINT organization_creations_key_valid CHECK (idempotency_key ~ '^[A-Za-z0-9_-]{8,128}$'),
    CONSTRAINT organization_creations_hash_valid CHECK (length(request_hash) = 64)
);

CREATE INDEX organization_creations_user_created_idx
    ON openfox.organization_creations (user_id, created_at);

-- organizations has FORCE RLS, so even a definer function cannot read it
-- without a tenant context. The signed-in "which workspaces can I open" list
-- is served from this control-plane directory instead of relaxing that RLS.
-- Rows are written in the same transaction that creates the organization.
CREATE TABLE openfox.organization_directory (
    organization_id uuid PRIMARY KEY REFERENCES openfox.organizations (id),
    slug text NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT organization_directory_slug_unique UNIQUE (slug),
    CONSTRAINT organization_directory_name_not_blank CHECK (length(btrim(name)) > 0)
);

-- 回填已有组织（此时迁移以 migrator 运行，FORCE RLS 下需逐个安装上下文读取）。
DO $$
DECLARE
    org record;
BEGIN
    FOR org IN SELECT DISTINCT organization_id FROM openfox.user_organization_index LOOP
        PERFORM openfox_private.set_tenant_context(org.organization_id,
            '00000000-0000-0000-0000-000000000000', 1, 'organization_worker');
        INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
        SELECT o.id, o.slug, o.name, o.created_at FROM openfox.organizations o
         WHERE o.id = org.organization_id
        ON CONFLICT (organization_id) DO NOTHING;
    END LOOP;
    IF to_regclass('pg_temp.openfox_tenant_context') IS NOT NULL THEN
        TRUNCATE TABLE pg_temp.openfox_tenant_context;
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.create_first_organization(
    p_user_id uuid,
    p_name text,
    p_slug text,
    p_idempotency_key text,
    p_request_hash text,
    p_request_id text,
    p_max_owned integer
) RETURNS TABLE (organization_id uuid, slug text, name text, created_at timestamptz, replayed boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
#variable_conflict use_column
DECLARE
    trimmed_name text := btrim(p_name);
    normalized_slug text := lower(btrim(p_slug));
    previous record;
    owned bigint;
    new_id uuid;
BEGIN
    IF p_user_id IS NULL OR NOT EXISTS (SELECT 1 FROM openfox.users u WHERE u.id = p_user_id) THEN
        RAISE EXCEPTION 'unknown user' USING ERRCODE = '22023';
    END IF;
    IF trimmed_name IS NULL OR length(trimmed_name) = 0 OR length(trimmed_name) > 100 THEN
        RAISE EXCEPTION 'invalid organization name' USING ERRCODE = '22023',
            CONSTRAINT = 'organization_name_invalid';
    END IF;
    IF normalized_slug IS NULL OR normalized_slug !~ '^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$' THEN
        RAISE EXCEPTION 'invalid organization slug' USING ERRCODE = '22023',
            CONSTRAINT = 'organization_slug_invalid';
    END IF;
    IF p_max_owned IS NULL OR p_max_owned < 1 THEN
        RAISE EXCEPTION 'invalid owned organization limit' USING ERRCODE = '22023';
    END IF;

    -- 同一用户的创建请求串行化，保证幂等检查与数量上限检查不会并发穿透。
    PERFORM pg_advisory_xact_lock(hashtextextended('org-create:' || p_user_id::text, 0));

    SELECT c.organization_id, c.request_hash INTO previous
      FROM openfox.organization_creations c
     WHERE c.user_id = p_user_id AND c.idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF previous.request_hash <> p_request_hash THEN
            RAISE EXCEPTION 'idempotency key reused with different request' USING ERRCODE = '23505',
                CONSTRAINT = 'organization_creations_key_conflict';
        END IF;
        PERFORM openfox_private.set_tenant_context(previous.organization_id, p_user_id, 1, 'user_request');
        RETURN QUERY
        SELECT o.id, o.slug, o.name, o.created_at, true
          FROM openfox.organizations o WHERE o.id = previous.organization_id;
        TRUNCATE TABLE pg_temp.openfox_tenant_context;
        RETURN;
    END IF;

    SELECT count(*) INTO owned
      FROM openfox.user_organization_index i
     WHERE i.user_id = p_user_id AND i.role = 'OWNER' AND i.membership_status = 'ACTIVE';
    IF owned >= p_max_owned THEN
        RAISE EXCEPTION 'owned organization limit reached' USING ERRCODE = '54000',
            CONSTRAINT = 'organization_owned_limit';
    END IF;

    -- 先生成 ID 并安装租户上下文，组织行及后续租户表写入都按现有
    -- tenant_isolation 策略校验，不新增任何放宽的 RLS 策略。
    new_id := gen_random_uuid();
    PERFORM openfox_private.set_tenant_context(new_id, p_user_id, 1, 'user_request');

    -- slug 冲突由 organizations_slug_unique 拒绝（23505），整个事务回滚。
    INSERT INTO openfox.organizations (id, slug, name)
    VALUES (new_id, normalized_slug, trimmed_name);

    INSERT INTO openfox.organization_members (organization_id, user_id, role, status, authorization_version)
    VALUES (new_id, p_user_id, 'OWNER', 'ACTIVE', 1);

    INSERT INTO openfox.task_statuses (organization_id, name, description, category, position)
    VALUES
        (new_id, 'Triage', '', 'TRIAGE', 0),
        (new_id, 'Backlog', '', 'BACKLOG', 1),
        (new_id, 'Todo', '', 'TODO', 2),
        (new_id, 'In Progress', '', 'IN_PROGRESS', 3),
        (new_id, 'Done', '', 'DONE', 4),
        (new_id, 'Cancelled', '', 'CANCELLED', 5),
        (new_id, 'Duplicate', '', 'DUPLICATE', 6);

    INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES (new_id, 0);
    INSERT INTO openfox.organization_runtime_configs (organization_id) VALUES (new_id);

    INSERT INTO openfox.audit_events (
        id, organization_id, actor_id, action, decision, reason,
        resource_kind, resource_id, request_id, authorization_version, safe_metadata
    ) VALUES (
        gen_random_uuid(), new_id, p_user_id, 'organization.created', 'allow', 'self_service',
        'organization', new_id::text, COALESCE(NULLIF(btrim(p_request_id), ''), 'unknown'), 1,
        jsonb_build_object('resource_kind', 'organization')
    );

    INSERT INTO openfox.organization_creations (user_id, idempotency_key, organization_id, request_hash)
    VALUES (p_user_id, p_idempotency_key, new_id, p_request_hash);

    INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
    SELECT o.id, o.slug, o.name, o.created_at FROM openfox.organizations o WHERE o.id = new_id;

    RETURN QUERY
    SELECT o.id, o.slug, o.name, o.created_at, false
      FROM openfox.organizations o WHERE o.id = new_id;

    -- 清除事务内租户上下文，避免同事务后续语句沿用（ON COMMIT 也会清空）。
    TRUNCATE TABLE pg_temp.openfox_tenant_context;
END
$$;

ALTER FUNCTION openfox_private.create_first_organization(uuid, text, text, text, text, text, integer)
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.create_first_organization(uuid, text, text, text, text, text, integer)
    FROM PUBLIC;

-- Lists the caller's active organizations before any tenant context exists,
-- so the client can route a signed-in user to a workspace or to onboarding.
CREATE OR REPLACE FUNCTION openfox_private.list_user_organizations(p_user_id uuid)
RETURNS TABLE (organization_id uuid, slug text, name text, role text, created_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT d.organization_id, d.slug, d.name, i.role, d.created_at
      FROM openfox.user_organization_index i
      JOIN openfox.organization_directory d ON d.organization_id = i.organization_id
     WHERE i.user_id = p_user_id AND i.membership_status = 'ACTIVE'
     ORDER BY d.created_at, d.organization_id
     LIMIT 100
$$;

ALTER FUNCTION openfox_private.list_user_organizations(uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_user_organizations(uuid) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.ensure_email_user(text) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.create_first_organization(uuid, text, text, text, text, text, integer)
    TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_user_organizations(uuid) TO openfox_app;
