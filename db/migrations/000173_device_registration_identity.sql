-- A device authentication key belongs to one physical device, including after revocation.
SET ROLE openfox_migrator;
CREATE UNIQUE INDEX device_keys_public_key_unique ON openfox.device_keys(public_key);

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
    member_role text;
    registered_id uuid;
    new_epoch bigint;
    registered_generation bigint;
    registered_scope text;
    grant_row openfox.device_grants%ROWTYPE;
    action_name text;
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

    SELECT oi.authorization_version, oi.role
      INTO member_authorization_version, member_role
      FROM openfox.user_organization_index AS oi
     WHERE oi.organization_id = registration.organization_id
       AND oi.user_id = registration.issued_by_user_id
       AND oi.membership_status = 'ACTIVE';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'device registration issuer is not active' USING ERRCODE = '42501';
    END IF;
    -- 签发后角色可能变化：按兑换时的角色重新判断签发资格。
    IF (registration.scope = 'ORGANIZATION' AND member_role NOT IN ('OWNER', 'ADMIN'))
       OR (registration.scope = 'PERSONAL' AND member_role = 'GUEST') THEN
        RAISE EXCEPTION 'device registration issuer may not grant this scope' USING ERRCODE = '42501';
    END IF;

    PERFORM openfox_private.set_tenant_context(
        registration.organization_id,
        registration.issued_by_user_id,
        member_authorization_version,
        'user_request'
    );

    -- Separate one-time tokens can arrive simultaneously for the same installation.
    PERFORM pg_advisory_xact_lock(hashtextextended(encode(p_public_key, 'hex'), 0));
    SELECT k.device_id INTO registered_id FROM openfox.device_keys k
     WHERE k.public_key = p_public_key;
    IF NOT FOUND THEN
        registered_id := gen_random_uuid();
        INSERT INTO openfox.physical_devices (id, owner_user_id, device_key, name, install_id, max_slots)
        VALUES (registered_id, registration.issued_by_user_id, 'device-' || registered_id::text,
                registration.name, registration.install_id, registration.max_slots);
        INSERT INTO openfox.device_keys (device_id, key_id, public_key)
        VALUES (registered_id, p_key_id, p_public_key) RETURNING generation INTO registered_generation;
        INSERT INTO openfox.device_grants (organization_id, device_id, scope, scope_user_id)
        VALUES (registration.organization_id, registered_id, registration.scope,
                CASE WHEN registration.scope = 'PERSONAL' THEN registration.issued_by_user_id END);
        registered_scope := registration.scope;
        action_name := 'device.registered';
    ELSE
        PERFORM 1 FROM openfox.physical_devices d WHERE d.id = registered_id
           AND d.owner_user_id = registration.issued_by_user_id AND d.status = 'ACTIVE' FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'device registration identity conflict' USING ERRCODE = '42501';
        END IF;
        SELECT k.generation INTO registered_generation FROM openfox.device_keys k
         WHERE k.device_id = registered_id AND k.public_key = p_public_key
           AND k.key_id = p_key_id AND k.status = 'ACTIVE' FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'device registration identity conflict' USING ERRCODE = '42501';
        END IF;
        SELECT g.* INTO grant_row FROM openfox.device_grants g
         WHERE g.organization_id = registration.organization_id AND g.device_id = registered_id FOR UPDATE;
        -- Additional organizations use the explicit join workflow.
        IF NOT FOUND OR (grant_row.status <> 'ACTIVE' AND grant_row.removed_by_manager_at IS NOT NULL
                         AND member_role NOT IN ('OWNER', 'ADMIN')) THEN
            RAISE EXCEPTION 'device registration identity conflict' USING ERRCODE = '42501';
        END IF;
        registered_scope := grant_row.scope;
        IF grant_row.status <> 'ACTIVE' THEN
            UPDATE openfox.device_grants g SET status = 'ACTIVE', scope = registration.scope,
                   scope_user_id = CASE WHEN registration.scope = 'PERSONAL' THEN registration.issued_by_user_id END,
                   scope_space_id = NULL, removed_by_manager_at = NULL, permission_version = g.permission_version + 1
             WHERE g.organization_id = registration.organization_id AND g.device_id = registered_id;
            registered_scope := registration.scope;
        END IF;
        action_name := 'device.reconnected';
    END IF;

    SELECT cs.center_epoch INTO new_epoch
      FROM openfox.center_state AS cs
     WHERE cs.id = 1;

    UPDATE openfox.device_registration_tokens
       SET consumed_at = clock_timestamp(), device_id = registered_id
     WHERE id = registration.id;

    INSERT INTO openfox.audit_events (
        id, organization_id, actor_id, action, decision, reason,
        resource_kind, resource_id, request_id, authorization_version,
        safe_metadata, created_at
    ) VALUES (
        gen_random_uuid(), registration.organization_id, registration.issued_by_user_id,
        action_name, 'allow', 'allowed', 'device', registered_id::text,
        registration.id::text, member_authorization_version,
        jsonb_build_object('status', 'ACTIVE', 'grant_scope', registered_scope),
        clock_timestamp()
    );

    RETURN QUERY SELECT registered_id, registration.organization_id,
                        registration.issued_by_user_id, registered_generation, new_epoch;
END
$$;

RESET ROLE;
