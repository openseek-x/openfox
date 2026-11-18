-- WP-02 invitation lifecycle.
-- Invitation tokens are opaque values at the API boundary and are stored only
-- as SHA-256 digests. Acceptance is kept in one security-definer transaction
-- because the accept route does not carry an organization selector.

SET ROLE openfox_migrator;

CREATE TABLE openfox.invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    normalized_email text NOT NULL,
    role text NOT NULL,
    created_by uuid NOT NULL,
    token_hash bytea NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT (clock_timestamp() + interval '7 days'),
    consumed_at timestamptz,
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT invitations_email_not_blank CHECK (length(btrim(normalized_email)) > 0),
    CONSTRAINT invitations_role_valid CHECK (role IN ('OWNER', 'ADMIN', 'MEMBER', 'GUEST')),
    CONSTRAINT invitations_status_valid CHECK (status IN ('PENDING', 'ACCEPTED', 'REVOKED', 'EXPIRED')),
    CONSTRAINT invitations_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT invitations_expiry_after_issue CHECK (expires_at > issued_at),
    CONSTRAINT invitations_consumed_state_valid CHECK (
        (status = 'ACCEPTED') = (consumed_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX invitations_token_hash_unique
    ON openfox.invitations (token_hash);

CREATE UNIQUE INDEX invitations_pending_email_unique
    ON openfox.invitations (organization_id, normalized_email)
    WHERE status = 'PENDING';

CREATE INDEX invitations_organization_status_idx
    ON openfox.invitations (organization_id, status, issued_at, id);

ALTER TABLE openfox.invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.invitations FORCE ROW LEVEL SECURITY;

-- The accept endpoint discovers the organization from the token, so it needs
-- a one-row, transaction-local lookup context before normal tenant context is
-- available. Runtime roles cannot create this temp table because migration
-- role is the only role with TEMP on the database.
CREATE OR REPLACE FUNCTION openfox_private.set_invitation_lookup(
    p_invitation_id uuid,
    p_token_hash bytea
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
BEGIN
    IF p_invitation_id IS NULL
       OR p_token_hash IS NULL
       OR octet_length(p_token_hash) <> 32 THEN
        RAISE EXCEPTION 'invalid invitation lookup' USING ERRCODE = '22023';
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS openfox_invitation_lookup (
        invitation_id uuid NOT NULL PRIMARY KEY,
        token_hash bytea NOT NULL
    ) ON COMMIT DELETE ROWS;

    TRUNCATE TABLE pg_temp.openfox_invitation_lookup;
    INSERT INTO pg_temp.openfox_invitation_lookup (invitation_id, token_hash)
    VALUES (p_invitation_id, p_token_hash);
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.invitation_lookup_matches(
    p_invitation_id uuid,
    p_token_hash bytea
) RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    temp_schema oid;
BEGIN
    temp_schema := pg_my_temp_schema();
    IF temp_schema = 0 THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (
        SELECT 1
          FROM pg_class
         WHERE relnamespace = temp_schema
           AND relname = 'openfox_invitation_lookup'
    ) THEN
        RETURN false;
    END IF;
    RETURN EXISTS (
        SELECT 1
          FROM pg_temp.openfox_invitation_lookup
         WHERE invitation_id = p_invitation_id
           AND token_hash = p_token_hash
    );
END
$$;

ALTER FUNCTION openfox_private.set_invitation_lookup(uuid, bytea)
    OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.invitation_lookup_matches(uuid, bytea)
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.set_invitation_lookup(uuid, bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.invitation_lookup_matches(uuid, bytea) FROM PUBLIC;

CREATE POLICY invitations_tenant_isolation ON openfox.invitations
    USING (
        organization_id = openfox_private.current_organization_id()
        OR openfox_private.invitation_lookup_matches(id, token_hash)
    )
    WITH CHECK (organization_id = openfox_private.current_organization_id());

GRANT SELECT, INSERT, UPDATE ON openfox.invitations TO openfox_app;

-- The function is the only unauthenticated-to-tenant transition. It performs
-- all acceptance writes as the migration owner, then establishes tenant
-- context before touching organization_members, so RLS remains fail-closed.
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

    IF NOT FOUND THEN
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

    SELECT status
      INTO membership_status
      FROM openfox.organization_members
     WHERE organization_id = invitation_row.organization_id
       AND user_id = target_user_id
     FOR UPDATE;

    IF membership_status = 'ACTIVE' OR membership_status = 'REVOKED' THEN
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    ELSIF membership_status = 'INVITED' THEN
        UPDATE openfox.organization_members
           SET role = invitation_row.role,
               status = 'ACTIVE',
               authorization_version = authorization_version + 1
         WHERE organization_id = invitation_row.organization_id
           AND user_id = target_user_id;
    ELSE
        INSERT INTO openfox.organization_members (
            organization_id, user_id, role, status
        ) VALUES (
            invitation_row.organization_id, target_user_id, invitation_row.role, 'ACTIVE'
        );
    END IF;

    UPDATE openfox.invitations
       SET status = 'ACCEPTED', consumed_at = clock_timestamp()
     WHERE id = invitation_row.id AND status = 'PENDING';

    RETURN QUERY SELECT target_user_id, invitation_row.organization_id,
                        invitation_row.role, 'ACCEPTED'::text;
END
$$;

ALTER FUNCTION openfox_private.accept_invitation(uuid, bytea, uuid, text)
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.accept_invitation(uuid, bytea, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.accept_invitation(uuid, bytea, uuid, text)
    TO openfox_app, openfox_context;

RESET ROLE;
