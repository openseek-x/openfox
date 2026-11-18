-- WP-05 device management. This migration expands the WP-03 device identity
-- tables without changing the existing challenge/session contract.

SET ROLE openfox_migrator;

-- Control-plane device authentication functions run as the non-login
-- migrator role before a tenant is selected. Keep FORCE RLS for application
-- sessions, but let that narrowly-owned security-definer path inspect grants.
DROP POLICY IF EXISTS device_grants_tenant_isolation ON openfox.device_grants;
CREATE POLICY device_grants_tenant_isolation ON openfox.device_grants
    USING (
        current_user = 'openfox_migrator'
        OR organization_id = openfox_private.current_organization_id()
    )
    WITH CHECK (
        current_user = 'openfox_migrator'
        OR organization_id = openfox_private.current_organization_id()
    );

ALTER TABLE openfox.physical_devices
    ADD COLUMN owner_user_id uuid,
    ADD COLUMN name text NOT NULL DEFAULT 'Unnamed device',
    ADD COLUMN install_id text,
    ADD COLUMN last_seen_at timestamptz,
    ADD COLUMN protocol_major integer NOT NULL DEFAULT 1,
    ADD COLUMN protocol_minor integer NOT NULL DEFAULT 0,
    ADD COLUMN daemon_version text NOT NULL DEFAULT '',
    ADD COLUMN operating_system text NOT NULL DEFAULT '',
    ADD COLUMN architecture text NOT NULL DEFAULT '',
    ADD COLUMN capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN center_epoch bigint NOT NULL DEFAULT 1,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();

ALTER TABLE openfox.physical_devices
    ADD CONSTRAINT physical_devices_owner_fk
        FOREIGN KEY (owner_user_id) REFERENCES openfox.users (id),
    ADD CONSTRAINT physical_devices_name_not_blank
        CHECK (length(btrim(name)) > 0),
    ADD CONSTRAINT physical_devices_install_id_valid
        CHECK (install_id IS NULL OR length(btrim(install_id)) BETWEEN 1 AND 255),
    ADD CONSTRAINT physical_devices_protocol_major_valid
        CHECK (protocol_major >= 1),
    ADD CONSTRAINT physical_devices_protocol_minor_valid
        CHECK (protocol_minor >= 0),
    ADD CONSTRAINT physical_devices_capabilities_object
        CHECK (jsonb_typeof(capabilities) = 'object'),
    ADD CONSTRAINT physical_devices_center_epoch_positive
        CHECK (center_epoch > 0),
    ADD CONSTRAINT physical_devices_version_positive
        CHECK (version > 0);

CREATE UNIQUE INDEX physical_devices_install_id_unique
    ON openfox.physical_devices (install_id)
    WHERE install_id IS NOT NULL;
CREATE INDEX physical_devices_owner_idx
    ON openfox.physical_devices (owner_user_id, status, id);
CREATE INDEX physical_devices_last_seen_idx
    ON openfox.physical_devices (status, last_seen_at, id);

ALTER TABLE openfox.device_grants
    ADD COLUMN scope text NOT NULL DEFAULT 'ORGANIZATION',
    ADD COLUMN scope_user_id uuid,
    ADD COLUMN scope_space_id uuid;

ALTER TABLE openfox.device_grants
    ADD CONSTRAINT device_grants_scope_valid
        CHECK (scope IN ('PERSONAL', 'SPACE', 'ORGANIZATION')),
    ADD CONSTRAINT device_grants_scope_target_valid
        CHECK (
            (scope = 'PERSONAL' AND scope_user_id IS NOT NULL AND scope_space_id IS NULL)
            OR (scope = 'SPACE' AND scope_user_id IS NULL AND scope_space_id IS NOT NULL)
            OR (scope = 'ORGANIZATION' AND scope_user_id IS NULL AND scope_space_id IS NULL)
        ),
    ADD CONSTRAINT device_grants_scope_user_fk
        FOREIGN KEY (organization_id, scope_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    ADD CONSTRAINT device_grants_scope_space_fk
        FOREIGN KEY (organization_id, scope_space_id)
        REFERENCES openfox.spaces (organization_id, id);

CREATE INDEX device_grants_scope_idx
    ON openfox.device_grants (organization_id, scope, scope_space_id, scope_user_id, status);

CREATE TABLE openfox.device_registration_tokens (
    organization_id uuid NOT NULL,
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    issued_by_user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    name text NOT NULL,
    install_id text,
    max_slots integer NOT NULL DEFAULT 1,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    device_id uuid,
    FOREIGN KEY (organization_id, issued_by_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    CONSTRAINT device_registration_tokens_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT device_registration_tokens_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT device_registration_tokens_install_id_valid CHECK (
        install_id IS NULL OR length(btrim(install_id)) BETWEEN 1 AND 255
    ),
    CONSTRAINT device_registration_tokens_max_slots_valid CHECK (max_slots BETWEEN 1 AND 1024),
    CONSTRAINT device_registration_tokens_expiry_after_issue CHECK (expires_at > issued_at),
    CONSTRAINT device_registration_tokens_consumption_valid CHECK (
        (consumed_at IS NULL AND device_id IS NULL) OR
        (consumed_at IS NOT NULL AND device_id IS NOT NULL)
    ),
    CONSTRAINT device_registration_tokens_revoke_time_valid CHECK (
        revoked_at IS NULL OR revoked_at >= issued_at
    )
);

CREATE UNIQUE INDEX device_registration_tokens_hash_unique
    ON openfox.device_registration_tokens (token_hash);
CREATE INDEX device_registration_tokens_active_idx
    ON openfox.device_registration_tokens (organization_id, expires_at, consumed_at, revoked_at);

ALTER TABLE openfox.device_registration_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY device_registration_tokens_tenant_isolation
    ON openfox.device_registration_tokens
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

CREATE TABLE openfox.center_state (
    id smallint PRIMARY KEY DEFAULT 1,
    center_epoch bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT center_state_singleton CHECK (id = 1),
    CONSTRAINT center_state_epoch_positive CHECK (center_epoch > 0)
);

INSERT INTO openfox.center_state (id, center_epoch)
VALUES (1, 1)
ON CONFLICT (id) DO NOTHING;

CREATE OR REPLACE FUNCTION openfox_private.current_center_epoch()
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT center_epoch FROM openfox.center_state WHERE id = 1
$$;

-- The registration exchange is the only unauthenticated device-creation path.
-- It consumes a hashed, short-lived token and establishes tenant context from
-- the issuing member before inserting the tenant-scoped grant.
CREATE OR REPLACE FUNCTION openfox_private.register_device_with_token(
    p_token_hash bytea,
    p_key_id text,
    p_public_key bytea
) RETURNS TABLE (
    device_id uuid,
    organization_id uuid,
    owner_user_id uuid,
    key_generation bigint,
    center_epoch bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
DECLARE
    registration openfox.device_registration_tokens%ROWTYPE;
    member_authorization_version bigint;
    new_device_id uuid;
    new_epoch bigint;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32
       OR p_key_id IS NULL OR length(btrim(p_key_id)) = 0 OR length(p_key_id) > 128
       OR p_public_key IS NULL OR octet_length(p_public_key) <> 32 THEN
        RAISE EXCEPTION 'invalid device registration' USING ERRCODE = '22023';
    END IF;

    SELECT *
      INTO registration
      FROM openfox.device_registration_tokens
     WHERE token_hash = p_token_hash
     FOR UPDATE;
    IF NOT FOUND OR registration.consumed_at IS NOT NULL
       OR registration.revoked_at IS NOT NULL
       OR registration.expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'device registration token is not active' USING ERRCODE = '42501';
    END IF;

    SELECT oi.authorization_version
      INTO member_authorization_version
      FROM openfox.user_organization_index AS oi
     WHERE oi.organization_id = registration.organization_id
       AND oi.user_id = registration.issued_by_user_id
       AND oi.membership_status = 'ACTIVE';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'device registration issuer is not active' USING ERRCODE = '42501';
    END IF;

    PERFORM openfox_private.set_tenant_context(
        registration.organization_id,
        registration.issued_by_user_id,
        member_authorization_version,
        'user_request'
    );

    new_device_id := gen_random_uuid();
    INSERT INTO openfox.physical_devices (
        id, owner_user_id, device_key, name, install_id, max_slots
    ) VALUES (
        new_device_id,
        registration.issued_by_user_id,
        'device-' || new_device_id::text,
        registration.name,
        registration.install_id,
        registration.max_slots
    );

    INSERT INTO openfox.device_keys (device_id, key_id, public_key)
    VALUES (new_device_id, btrim(p_key_id), p_public_key);

    INSERT INTO openfox.device_grants (organization_id, device_id, scope)
    VALUES (registration.organization_id, new_device_id, 'ORGANIZATION');

    SELECT cs.center_epoch INTO new_epoch
      FROM openfox.center_state AS cs
     WHERE cs.id = 1;

    UPDATE openfox.device_registration_tokens
       SET consumed_at = clock_timestamp(), device_id = new_device_id
     WHERE id = registration.id;

    INSERT INTO openfox.audit_events (
        id, organization_id, actor_id, action, decision, reason,
        resource_kind, resource_id, request_id, authorization_version,
        safe_metadata, created_at
    ) VALUES (
        gen_random_uuid(), registration.organization_id, registration.issued_by_user_id,
        'device.registered', 'allow', 'allowed', 'device', new_device_id::text,
        registration.id::text, member_authorization_version,
        '{"status":"ACTIVE","grant_scope":"ORGANIZATION"}'::jsonb,
        clock_timestamp()
    );

    RETURN QUERY SELECT new_device_id, registration.organization_id,
                        registration.issued_by_user_id, 1::bigint, new_epoch;
END
$$;

ALTER FUNCTION openfox_private.current_center_epoch() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.register_device_with_token(bytea, text, bytea)
    OWNER TO openfox_migrator;

REVOKE ALL ON FUNCTION openfox_private.current_center_epoch() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.register_device_with_token(bytea, text, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.current_center_epoch() TO openfox_app, openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.register_device_with_token(bytea, text, bytea) TO openfox_app;

RESET ROLE;

GRANT INSERT (organization_id, issued_by_user_id, token_hash, name, install_id, max_slots, expires_at)
    ON openfox.device_registration_tokens TO openfox_app;
GRANT SELECT (id, expires_at)
    ON openfox.device_registration_tokens TO openfox_app;
GRANT SELECT, UPDATE (
    last_seen_at, protocol_major, protocol_minor, daemon_version,
    operating_system, architecture, capabilities, center_epoch, version, updated_at
) ON openfox.physical_devices TO openfox_app;
GRANT SELECT ON openfox.center_state TO openfox_app;
GRANT SELECT, UPDATE (scope, scope_user_id, scope_space_id, status, permission_version)
    ON openfox.device_grants TO openfox_app;
