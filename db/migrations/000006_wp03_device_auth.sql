-- WP-03 device authentication. Device credentials and bearer sessions are
-- separate from user credentials and sessions and are fail-closed by current
-- device/key generations plus active organization grants.

SET ROLE openfox_migrator;

CREATE TABLE openfox.device_keys (
    device_id uuid NOT NULL,
    key_id text NOT NULL,
    public_key bytea NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    generation bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (device_id, key_id),
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    CONSTRAINT device_keys_key_id_not_blank CHECK (length(btrim(key_id)) > 0),
    CONSTRAINT device_keys_key_id_length CHECK (length(key_id) <= 128),
    CONSTRAINT device_keys_public_key_length CHECK (octet_length(public_key) = 32),
    CONSTRAINT device_keys_status_valid CHECK (status IN ('ACTIVE', 'REVOKED')),
    CONSTRAINT device_keys_generation_positive CHECK (generation > 0),
    CONSTRAINT device_keys_revoke_time_valid CHECK (
        (status = 'REVOKED' AND revoked_at IS NOT NULL)
        OR (status = 'ACTIVE' AND revoked_at IS NULL)
    )
);

CREATE INDEX device_keys_active_idx
    ON openfox.device_keys (device_id, status, key_id);

CREATE TABLE openfox.device_challenges (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL,
    key_id text NOT NULL,
    nonce bytea NOT NULL,
    challenge_bytes bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    FOREIGN KEY (device_id, key_id)
        REFERENCES openfox.device_keys (device_id, key_id),
    CONSTRAINT device_challenges_nonce_length CHECK (octet_length(nonce) = 32),
    CONSTRAINT device_challenges_nonce_unique UNIQUE (nonce),
    CONSTRAINT device_challenges_bytes_length CHECK (octet_length(challenge_bytes) = 32),
    CONSTRAINT device_challenges_expiry_after_creation CHECK (expires_at > created_at),
    CONSTRAINT device_challenges_consume_time_valid CHECK (
        consumed_at IS NULL OR consumed_at >= created_at
    )
);

CREATE INDEX device_challenges_expiry_idx
    ON openfox.device_challenges (expires_at, consumed_at);

CREATE TABLE openfox.device_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id uuid NOT NULL,
    key_id text NOT NULL,
    device_generation bigint NOT NULL,
    key_generation bigint NOT NULL,
    token_hash bytea NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    FOREIGN KEY (device_id, key_id)
        REFERENCES openfox.device_keys (device_id, key_id),
    CONSTRAINT device_sessions_generation_positive CHECK (
        device_generation > 0 AND key_generation > 0
    ),
    CONSTRAINT device_sessions_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT device_sessions_expiry_after_issue CHECK (expires_at > issued_at),
    CONSTRAINT device_sessions_revoke_time_valid CHECK (
        revoked_at IS NULL OR revoked_at >= issued_at
    )
);

CREATE UNIQUE INDEX device_sessions_token_hash_unique
    ON openfox.device_sessions (token_hash);
CREATE INDEX device_sessions_device_active_idx
    ON openfox.device_sessions (device_id, revoked_at, expires_at);

-- The selector is the device equivalent of the user membership resolver. It
-- returns only an active grant and its permission version, never a user role.
CREATE OR REPLACE FUNCTION openfox_private.resolve_device_organization(
    p_device_id uuid,
    p_organization_id uuid
) RETURNS TABLE (
    organization_id uuid,
    permission_version bigint
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT g.organization_id, g.permission_version
      FROM openfox.device_grants g
      JOIN openfox.physical_devices d ON d.id = g.device_id
     WHERE g.device_id = p_device_id
       AND g.organization_id = p_organization_id
       AND g.status = 'ACTIVE'
       AND d.status = 'ACTIVE'
$$;

CREATE OR REPLACE FUNCTION openfox_private.device_has_active_grant(
    p_device_id uuid
) RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT EXISTS (
        SELECT 1
          FROM openfox.device_grants g
          JOIN openfox.physical_devices d ON d.id = g.device_id
         WHERE g.device_id = p_device_id
           AND g.status = 'ACTIVE'
           AND d.status = 'ACTIVE'
    )
$$;

-- Global lifecycle changes are narrow security-definer operations so one
-- revoke invalidates generations, sessions, and grants atomically.
CREATE OR REPLACE FUNCTION openfox_private.revoke_device(
    p_device_id uuid,
    p_revoked_at timestamptz
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_device_id IS NULL OR p_revoked_at IS NULL THEN
        RAISE EXCEPTION 'invalid device revoke' USING ERRCODE = '22023';
    END IF;
    UPDATE openfox.physical_devices
       SET status = 'REVOKED', generation = generation + 1
     WHERE id = p_device_id AND status <> 'REVOKED';
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    UPDATE openfox.device_sessions
       SET revoked_at = COALESCE(revoked_at, p_revoked_at)
     WHERE device_id = p_device_id AND revoked_at IS NULL;
    UPDATE openfox.device_grants
       SET status = 'REVOKED', permission_version = permission_version + 1
     WHERE device_id = p_device_id AND status = 'ACTIVE';
    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.revoke_device_key(
    p_device_id uuid,
    p_key_id text,
    p_revoked_at timestamptz
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_device_id IS NULL OR p_key_id IS NULL OR p_revoked_at IS NULL THEN
        RAISE EXCEPTION 'invalid key revoke' USING ERRCODE = '22023';
    END IF;
    UPDATE openfox.device_keys
       SET status = 'REVOKED', generation = generation + 1, revoked_at = p_revoked_at
     WHERE device_id = p_device_id AND key_id = p_key_id AND status <> 'REVOKED';
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    UPDATE openfox.device_sessions
       SET revoked_at = COALESCE(revoked_at, p_revoked_at)
     WHERE device_id = p_device_id AND key_id = p_key_id AND revoked_at IS NULL;
    RETURN true;
END
$$;

ALTER FUNCTION openfox_private.resolve_device_organization(uuid, uuid) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.device_has_active_grant(uuid) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.revoke_device(uuid, timestamptz) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.revoke_device_key(uuid, text, timestamptz) OWNER TO openfox_migrator;

REVOKE ALL ON FUNCTION openfox_private.resolve_device_organization(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.device_has_active_grant(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.revoke_device(uuid, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.revoke_device_key(uuid, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_device_organization(uuid, uuid) TO openfox_context, openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.device_has_active_grant(uuid) TO openfox_context, openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.revoke_device(uuid, timestamptz) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.revoke_device_key(uuid, text, timestamptz) TO openfox_app;

RESET ROLE;

-- Device authentication is control-plane state. Runtime code can only write
-- through the bounded columns used by the service and cannot alter keys.
GRANT SELECT, INSERT (device_key, max_slots), UPDATE (status, generation)
    ON openfox.physical_devices TO openfox_app;
GRANT SELECT, INSERT, UPDATE (status, generation, revoked_at)
    ON openfox.device_keys TO openfox_app;
GRANT SELECT, INSERT, UPDATE (consumed_at)
    ON openfox.device_challenges TO openfox_app;
GRANT SELECT, INSERT, UPDATE (revoked_at)
    ON openfox.device_sessions TO openfox_app;
GRANT INSERT (organization_id, device_id), UPDATE (status, permission_version)
    ON openfox.device_grants TO openfox_app;
