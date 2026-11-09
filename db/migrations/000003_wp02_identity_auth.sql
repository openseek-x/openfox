-- WP-02 identity/auth foundation and the control-plane organization selector.
-- Password verification remains in Go; the database stores only Argon2id
-- encoded hashes and opaque session token hashes.

SET ROLE openfox_migrator;

ALTER TABLE openfox.users
    ADD COLUMN IF NOT EXISTS normalized_email text;

UPDATE openfox.users
   SET normalized_email = lower(btrim(email))
 WHERE normalized_email IS NULL;

CREATE OR REPLACE FUNCTION openfox_private.sync_normalized_email()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, openfox_private
AS $$
BEGIN
    NEW.normalized_email := lower(btrim(NEW.email));
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.sync_normalized_email() OWNER TO openfox_migrator;
DROP TRIGGER IF EXISTS users_normalized_email_sync ON openfox.users;
CREATE TRIGGER users_normalized_email_sync
BEFORE INSERT OR UPDATE OF email ON openfox.users
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_normalized_email();

ALTER TABLE openfox.users
    ALTER COLUMN normalized_email SET NOT NULL;

ALTER TABLE openfox.users
    ADD CONSTRAINT users_normalized_email_not_blank
    CHECK (length(normalized_email) > 0);

CREATE UNIQUE INDEX IF NOT EXISTS users_normalized_email_unique
    ON openfox.users (normalized_email);

-- This is a narrow control-plane read model used to validate an organization
-- choice before tenant RLS context is established. Membership write paths must
-- update this row in the same transaction as organization_members.
CREATE TABLE openfox.user_organization_index (
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    role text NOT NULL,
    membership_status text NOT NULL,
    authorization_version bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, organization_id),
    FOREIGN KEY (user_id) REFERENCES openfox.users (id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    CONSTRAINT user_organization_index_role_valid CHECK (role IN ('OWNER', 'ADMIN', 'MEMBER', 'GUEST')),
    CONSTRAINT user_organization_index_status_valid CHECK (membership_status IN ('ACTIVE', 'INVITED', 'REVOKED')),
    CONSTRAINT user_organization_index_version_positive CHECK (authorization_version > 0)
);

CREATE OR REPLACE FUNCTION openfox_private.sync_user_organization_index()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF TG_OP = 'DELETE' OR TG_OP = 'UPDATE' THEN
        DELETE FROM openfox.user_organization_index
         WHERE user_id = OLD.user_id AND organization_id = OLD.organization_id;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        INSERT INTO openfox.user_organization_index (
            user_id, organization_id, role, membership_status, authorization_version, updated_at
        ) VALUES (
            NEW.user_id, NEW.organization_id, NEW.role, NEW.status,
            NEW.authorization_version, clock_timestamp()
        );
    END IF;
    RETURN COALESCE(NEW, OLD);
END
$$;

ALTER FUNCTION openfox_private.sync_user_organization_index() OWNER TO openfox_migrator;
DROP TRIGGER IF EXISTS organization_members_index_sync ON openfox.organization_members;
CREATE TRIGGER organization_members_index_sync
AFTER INSERT OR UPDATE OR DELETE ON openfox.organization_members
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_user_organization_index();

INSERT INTO openfox.user_organization_index (
    user_id, organization_id, role, membership_status, authorization_version
)
SELECT user_id, organization_id, role, status, authorization_version
FROM openfox.organization_members
ON CONFLICT (user_id, organization_id) DO NOTHING;

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
$$;

ALTER FUNCTION openfox_private.resolve_organization(uuid, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_organization(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_organization(uuid, uuid) TO openfox_context;

CREATE TABLE openfox.password_credentials (
    user_id uuid PRIMARY KEY,
    password_hash text NOT NULL,
    credential_version bigint NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (user_id) REFERENCES openfox.users (id),
    CONSTRAINT password_credentials_hash_not_blank CHECK (length(btrim(password_hash)) > 0),
    CONSTRAINT password_credentials_version_positive CHECK (credential_version > 0),
    CONSTRAINT password_credentials_status_valid CHECK (status IN ('ACTIVE', 'REVOKED'))
);

CREATE TABLE openfox.user_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    client_kind text NOT NULL,
    credential_version bigint NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    FOREIGN KEY (user_id) REFERENCES openfox.users (id),
    CONSTRAINT user_sessions_token_hash_not_empty CHECK (octet_length(token_hash) = 32),
    CONSTRAINT user_sessions_client_kind_valid CHECK (client_kind IN ('WEB', 'DESKTOP', 'DEVICE')),
    CONSTRAINT user_sessions_version_positive CHECK (credential_version > 0),
    CONSTRAINT user_sessions_expiry_after_issue CHECK (expires_at > issued_at),
    CONSTRAINT user_sessions_revoke_time_valid CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);

CREATE UNIQUE INDEX user_sessions_token_hash_unique
    ON openfox.user_sessions (token_hash);
CREATE INDEX user_sessions_user_active_idx
    ON openfox.user_sessions (user_id, revoked_at, expires_at);

RESET ROLE;

-- Authentication is still an application boundary: the runtime role receives
-- only the rows needed by parameterized repository queries. No session token
-- or password is returned by a database function.
GRANT SELECT ON openfox.password_credentials TO openfox_app;
GRANT SELECT, INSERT, UPDATE (last_seen_at, revoked_at) ON openfox.user_sessions TO openfox_app;
