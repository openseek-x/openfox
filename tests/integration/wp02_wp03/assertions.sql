\set ON_ERROR_STOP on

-- Fail with a precise contract boundary when the parent migration is not
-- installed. This avoids reporting a partial assertion run as a pass.
DO $$
DECLARE
    required_table text;
    required_tables text[] := ARRAY[
        'openfox.invitations',
        'openfox.device_keys',
        'openfox.device_challenges',
        'openfox.device_sessions'
    ];
BEGIN
    FOREACH required_table IN ARRAY required_tables LOOP
        IF to_regclass(required_table) IS NULL THEN
            RAISE EXCEPTION 'WP-02/WP-03 schema prerequisite missing: table %', required_table;
        END IF;
    END LOOP;
END
$$;

DO $$
DECLARE
    required_column record;
BEGIN
    FOR required_column IN
        SELECT * FROM (VALUES
            ('openfox', 'invitations', 'id'),
            ('openfox', 'invitations', 'organization_id'),
            ('openfox', 'invitations', 'normalized_email'),
            ('openfox', 'invitations', 'role'),
            ('openfox', 'invitations', 'token_hash'),
            ('openfox', 'invitations', 'status'),
            ('openfox', 'invitations', 'expires_at'),
            ('openfox', 'invitations', 'consumed_at'),
            ('openfox', 'device_keys', 'device_id'),
            ('openfox', 'device_keys', 'key_id'),
            ('openfox', 'device_keys', 'public_key'),
            ('openfox', 'device_keys', 'status'),
            ('openfox', 'device_keys', 'generation'),
            ('openfox', 'device_challenges', 'device_id'),
            ('openfox', 'device_challenges', 'key_id'),
            ('openfox', 'device_challenges', 'nonce'),
            ('openfox', 'device_challenges', 'expires_at'),
            ('openfox', 'device_challenges', 'consumed_at'),
            ('openfox', 'device_sessions', 'token_hash'),
            ('openfox', 'device_sessions', 'device_id'),
            ('openfox', 'device_sessions', 'key_generation'),
            ('openfox', 'device_sessions', 'expires_at'),
            ('openfox', 'device_sessions', 'revoked_at')
        ) AS columns(schema_name, table_name, column_name)
    LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = required_column.schema_name
              AND table_name = required_column.table_name
              AND column_name = required_column.column_name
        ) THEN
            RAISE EXCEPTION 'WP-02/WP-03 schema prerequisite missing: column %.%.%',
                required_column.schema_name, required_column.table_name, required_column.column_name;
        END IF;
    END LOOP;
END
$$;

DO $$
DECLARE
    bypasses boolean;
    app_owner boolean;
    context_can_create_temp boolean;
    app_can_update_agent boolean;
    relation_name text;
    rls_enabled boolean;
    rls_forced boolean;
BEGIN
    SELECT rolsuper OR rolbypassrls INTO bypasses
      FROM pg_roles WHERE rolname = 'openfox_app';
    SELECT (pg_get_userbyid(c.relowner) = 'openfox_app') INTO app_owner
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'openfox' AND c.relname = 'organizations';
    SELECT has_database_privilege('openfox_context', current_database(), 'TEMPORARY')
      INTO context_can_create_temp;
    SELECT has_table_privilege('openfox_app', 'openfox.agents', 'UPDATE')
      INTO app_can_update_agent;
    IF bypasses OR app_owner OR context_can_create_temp OR app_can_update_agent THEN
        RAISE EXCEPTION 'runtime roles have excessive privilege';
    END IF;

    FOREACH relation_name IN ARRAY ARRAY[
        'organizations', 'organization_members', 'spaces', 'space_members', 'tasks',
        'organization_capacity', 'device_grants', 'agents',
        'capacity_reservations', 'dispatch_offers'
    ] LOOP
        SELECT c.relrowsecurity, c.relforcerowsecurity
          INTO rls_enabled, rls_forced
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'openfox' AND c.relname = relation_name;
        IF NOT COALESCE(rls_enabled, false) OR NOT COALESCE(rls_forced, false) THEN
            RAISE EXCEPTION 'tenant relation %.% is not FORCE RLS protected', 'openfox', relation_name;
        END IF;
    END LOOP;
END
$$;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    visible_rows bigint;
BEGIN
    SELECT count(*) INTO visible_rows FROM openfox.organizations;
    IF visible_rows <> 0 THEN
        RAISE EXCEPTION 'missing context exposed % organization rows', visible_rows;
    END IF;
END
$$;
DO $$
BEGIN
    BEGIN
        PERFORM openfox_private.set_tenant_context(
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-000000000001', 1, 'user_request'
        );
        RAISE EXCEPTION 'openfox_app executed the trusted context setter';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'user_request'
);
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    organization_rows bigint;
    member_rows bigint;
    foreign_rows bigint;
    owner_rows bigint;
BEGIN
    SELECT count(*) INTO organization_rows FROM openfox.organizations;
    SELECT count(*) INTO member_rows FROM openfox.organization_members;
    SELECT count(*) INTO foreign_rows
      FROM openfox.organization_members
     WHERE organization_id = '00000000-0000-0000-0000-0000000000b1';
    SELECT count(*) INTO owner_rows
      FROM openfox.organization_members
     WHERE organization_id = '00000000-0000-0000-0000-0000000000a1'
       AND status = 'ACTIVE' AND role = 'OWNER';
    IF organization_rows <> 1 OR member_rows <> 4 OR foreign_rows <> 0 THEN
        RAISE EXCEPTION 'tenant A visibility mismatch: organizations %, members %, foreign members %',
            organization_rows, member_rows, foreign_rows;
    END IF;
    IF owner_rows < 1 THEN
        RAISE EXCEPTION 'fixture organization A has no active owner';
    END IF;
END
$$;
DO $$
DECLARE
    affected bigint;
BEGIN
    UPDATE openfox.spaces
       SET name = 'cross-tenant-write-must-not-match'
     WHERE organization_id = '00000000-0000-0000-0000-0000000000b1';
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 0 THEN
        RAISE EXCEPTION 'cross-organization update affected % rows', affected;
    END IF;
END
$$;
DO $$
BEGIN
    BEGIN
        INSERT INTO openfox.space_members (organization_id, space_id, user_id)
        VALUES (
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-0000000000bb',
            '00000000-0000-0000-0000-000000000001'
        );
        RAISE EXCEPTION 'cross-organization composite association was accepted';
    EXCEPTION WHEN foreign_key_violation OR check_violation OR insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
BEGIN
    BEGIN
        INSERT INTO openfox.organizations (id, slug, name)
        VALUES ('00000000-0000-0000-0000-0000000000c1', 'wp02-no-context', 'must fail');
        RAISE EXCEPTION 'missing context write was accepted';
    EXCEPTION WHEN insufficient_privilege OR check_violation OR foreign_key_violation THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'organization_worker'
);
SET LOCAL ROLE openfox_app;
SELECT count(*) AS organization_a_rows
  FROM openfox.organizations;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    visible_rows bigint;
BEGIN
    SELECT count(*) INTO visible_rows FROM openfox.organizations;
    IF visible_rows <> 0 THEN
        RAISE EXCEPTION 'tenant context leaked across transaction boundary: % rows', visible_rows;
    END IF;
END
$$;
COMMIT;

-- Invitation storage must expose only a digest-like field. A raw token column
-- would make plaintext persistence possible even if the HTTP layer is safe.
DO $$
DECLARE
    forbidden_column text;
    invalid_hashes bigint;
BEGIN
    FOREACH forbidden_column IN ARRAY ARRAY['token', 'raw_token', 'plaintext_token', 'token_value'] LOOP
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'openfox'
               AND table_name = 'invitations'
               AND column_name = forbidden_column
        ) THEN
            RAISE EXCEPTION 'invitation table contains forbidden plaintext column %', forbidden_column;
        END IF;
    END LOOP;
    EXECUTE 'SELECT count(*) FROM openfox.invitations WHERE token_hash IS NULL' INTO invalid_hashes;
    IF invalid_hashes <> 0 THEN
        RAISE EXCEPTION 'invitation rows with NULL token_hash: %', invalid_hashes;
    END IF;
END
$$;

DO $$
DECLARE
    forbidden_column text;
    invalid_hashes bigint;
BEGIN
    FOREACH forbidden_column IN ARRAY ARRAY['token', 'raw_token', 'plaintext_token', 'private_key'] LOOP
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'openfox'
               AND table_name = 'device_sessions'
               AND column_name = forbidden_column
        ) THEN
            RAISE EXCEPTION 'device session table contains forbidden secret column %', forbidden_column;
        END IF;
    END LOOP;
    EXECUTE 'SELECT count(*) FROM openfox.device_sessions WHERE token_hash IS NULL' INTO invalid_hashes;
    IF invalid_hashes <> 0 THEN
        RAISE EXCEPTION 'device session rows with NULL token_hash: %', invalid_hashes;
    END IF;
END
$$;

-- Last-owner protection is an application transaction invariant. The SQL
-- baseline still verifies the fixture invariant; the HTTP role matrix tests
-- the state transition and the final-owner conflict code.
DO $$
DECLARE
    current_organization_id uuid;
    active_owners bigint;
BEGIN
    FOR current_organization_id IN
        SELECT id FROM openfox.organizations
        WHERE id IN (
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-0000000000b1'
        )
    LOOP
        SELECT count(*) INTO active_owners
          FROM openfox.organization_members
         WHERE organization_members.organization_id = current_organization_id
           AND status = 'ACTIVE' AND role = 'OWNER';
        IF active_owners < 1 THEN
            RAISE EXCEPTION 'organization % has no active owner', current_organization_id;
        END IF;
    END LOOP;
END
$$;
