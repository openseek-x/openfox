-- DB-001/DB-002 foundation.
-- The install role must have CREATEROLE and be able to create schemas in the
-- target database. No application login or credential is created here.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'openfox_migrator') THEN
        CREATE ROLE openfox_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'openfox_app') THEN
        CREATE ROLE openfox_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'openfox_context') THEN
        CREATE ROLE openfox_context NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
    END IF;
END
$$;

ALTER ROLE openfox_migrator NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS;
ALTER ROLE openfox_app NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE openfox_context NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;

-- The trusted connection role may establish context and then drop to the data
-- role for tenant DML. NOINHERIT keeps the membership from widening the
-- context role before an explicit SET LOCAL ROLE inside a transaction.
GRANT openfox_app TO openfox_context;

CREATE SCHEMA IF NOT EXISTS openfox AUTHORIZATION openfox_migrator;
CREATE SCHEMA IF NOT EXISTS openfox_private AUTHORIZATION openfox_migrator;
REVOKE ALL ON SCHEMA openfox_private FROM PUBLIC;
GRANT USAGE ON SCHEMA openfox TO openfox_app, openfox_context;
GRANT USAGE ON SCHEMA openfox_private TO openfox_app, openfox_context;

-- The trusted context function creates a private ON COMMIT DELETE ROWS table.
-- The data role must not be able to create a competing temp table.
DO $$
BEGIN
    -- The context function creates its temp table as the security-definer
    -- migration owner. Runtime roles must not be able to pre-create a table
    -- that the trusted setter could mistake for its own context.
    EXECUTE format('REVOKE TEMP ON DATABASE %I FROM PUBLIC', current_database());
    EXECUTE format('GRANT TEMP ON DATABASE %I TO openfox_migrator', current_database());
    EXECUTE format('REVOKE TEMP ON DATABASE %I FROM openfox_app', current_database());
    EXECUTE format('REVOKE TEMP ON DATABASE %I FROM openfox_context', current_database());
END
$$;

SET ROLE openfox_migrator;

CREATE TABLE openfox.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT users_email_not_blank CHECK (length(btrim(email)) > 0),
    CONSTRAINT users_email_unique UNIQUE (email)
);

CREATE TABLE openfox.organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT organizations_slug_not_blank CHECK (length(btrim(slug)) > 0),
    CONSTRAINT organizations_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT organizations_slug_unique UNIQUE (slug)
);

CREATE TABLE openfox.organization_members (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    authorization_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (user_id) REFERENCES openfox.users (id),
    CONSTRAINT organization_members_role_valid CHECK (role IN ('OWNER', 'ADMIN', 'MEMBER', 'GUEST')),
    CONSTRAINT organization_members_status_valid CHECK (status IN ('ACTIVE', 'INVITED', 'REVOKED')),
    CONSTRAINT organization_members_authorization_version_positive CHECK (authorization_version > 0)
);

CREATE TABLE openfox.spaces (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    visibility text NOT NULL DEFAULT 'PRIVATE',
    status text NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    CONSTRAINT spaces_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT spaces_visibility_valid CHECK (visibility IN ('PRIVATE', 'SHARED')),
    CONSTRAINT spaces_status_valid CHECK (status IN ('ACTIVE', 'ARCHIVED'))
);

CREATE TABLE openfox.space_members (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL DEFAULT 'MEMBER',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, space_id, user_id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT space_members_role_valid CHECK (role IN ('OWNER', 'EDITOR', 'MEMBER', 'VIEWER'))
);

CREATE TABLE openfox.tasks (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    title text NOT NULL,
    status text NOT NULL DEFAULT 'TODO',
    assignee_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, assignee_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT tasks_title_not_blank CHECK (length(btrim(title)) > 0),
    CONSTRAINT tasks_status_valid CHECK (status IN ('TODO', 'IN_PROGRESS', 'DONE', 'CANCELED'))
);

CREATE INDEX organization_members_user_idx
    ON openfox.organization_members (user_id, organization_id);
CREATE INDEX space_members_user_idx
    ON openfox.space_members (organization_id, user_id, space_id);
CREATE INDEX tasks_space_idx
    ON openfox.tasks (organization_id, space_id, created_at, id);

RESET ROLE;

-- This function is intentionally not exposed to openfox_app. It materializes
-- context in a temp table owned by the migration role, instead of trusting a
-- caller-controlled GUC. The caller must use a separately authenticated
-- context-capable database role and then SET LOCAL ROLE openfox_app.
CREATE OR REPLACE FUNCTION openfox_private.set_tenant_context(
    p_organization_id uuid,
    p_actor_id uuid,
    p_authorization_version bigint,
    p_source text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
BEGIN
    IF p_organization_id IS NULL
       OR p_actor_id IS NULL
       OR p_authorization_version IS NULL
       OR p_authorization_version <= 0
       OR p_source NOT IN ('user_request', 'device_request', 'organization_worker') THEN
        RAISE EXCEPTION 'invalid tenant context' USING ERRCODE = '22023';
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS openfox_tenant_context (
        organization_id uuid NOT NULL,
        actor_id uuid NOT NULL,
        authorization_version bigint NOT NULL,
        source text NOT NULL,
        CONSTRAINT openfox_tenant_context_source_valid
            CHECK (source IN ('user_request', 'device_request', 'organization_worker'))
    ) ON COMMIT DELETE ROWS;

    TRUNCATE TABLE pg_temp.openfox_tenant_context;
    INSERT INTO pg_temp.openfox_tenant_context (
        organization_id, actor_id, authorization_version, source
    ) VALUES (
        p_organization_id, p_actor_id, p_authorization_version, p_source
    );
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.current_organization_id()
RETURNS uuid
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    context_schema oid;
    organization_id uuid;
BEGIN
    context_schema := pg_my_temp_schema();
    IF context_schema = 0 THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relnamespace = context_schema
          AND relname = 'openfox_tenant_context'
    ) THEN
        RETURN NULL;
    END IF;

    EXECUTE 'SELECT organization_id FROM pg_temp.openfox_tenant_context LIMIT 1'
        INTO organization_id;
    RETURN organization_id;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.current_actor_id()
RETURNS uuid
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    context_schema oid;
    actor_id uuid;
BEGIN
    context_schema := pg_my_temp_schema();
    IF context_schema = 0 THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relnamespace = context_schema
          AND relname = 'openfox_tenant_context'
    ) THEN
        RETURN NULL;
    END IF;

    EXECUTE 'SELECT actor_id FROM pg_temp.openfox_tenant_context LIMIT 1'
        INTO actor_id;
    RETURN actor_id;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.current_authorization_version()
RETURNS bigint
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    context_schema oid;
    authorization_version bigint;
BEGIN
    context_schema := pg_my_temp_schema();
    IF context_schema = 0 THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relnamespace = context_schema
          AND relname = 'openfox_tenant_context'
    ) THEN
        RETURN NULL;
    END IF;

    EXECUTE 'SELECT authorization_version FROM pg_temp.openfox_tenant_context LIMIT 1'
        INTO authorization_version;
    RETURN authorization_version;
END
$$;

ALTER FUNCTION openfox_private.set_tenant_context(uuid, uuid, bigint, text) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.current_organization_id() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.current_actor_id() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.current_authorization_version() OWNER TO openfox_migrator;

REVOKE ALL ON FUNCTION openfox_private.set_tenant_context(uuid, uuid, bigint, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.current_organization_id() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.current_actor_id() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.current_authorization_version() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.set_tenant_context(uuid, uuid, bigint, text) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.current_organization_id() TO openfox_app, openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.current_actor_id() TO openfox_app, openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.current_authorization_version() TO openfox_app, openfox_context;

GRANT SELECT ON openfox.users TO openfox_app, openfox_context;
GRANT SELECT, INSERT, UPDATE, DELETE ON
    openfox.organizations,
    openfox.organization_members,
    openfox.spaces,
    openfox.space_members,
    openfox.tasks
TO openfox_app;

ALTER TABLE openfox.organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organizations FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_members FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.spaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.spaces FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_members FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.tasks FORCE ROW LEVEL SECURITY;

CREATE POLICY organizations_tenant_isolation ON openfox.organizations
    USING (id = openfox_private.current_organization_id())
    WITH CHECK (id = openfox_private.current_organization_id());

CREATE POLICY organization_members_tenant_isolation ON openfox.organization_members
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

CREATE POLICY spaces_tenant_isolation ON openfox.spaces
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

CREATE POLICY space_members_tenant_isolation ON openfox.space_members
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

CREATE POLICY tasks_tenant_isolation ON openfox.tasks
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

