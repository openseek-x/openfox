-- SP-03 capacity/control foundation. Physical devices are control-plane rows;
-- tenant grants and reservations remain RLS-protected by organization.

SET ROLE openfox_migrator;

CREATE TABLE openfox.physical_devices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_key text NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    generation bigint NOT NULL DEFAULT 1,
    max_slots integer NOT NULL,
    reserved_slots integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT physical_devices_key_not_blank CHECK (length(btrim(device_key)) > 0),
    CONSTRAINT physical_devices_status_valid CHECK (status IN ('ACTIVE', 'REVOKED', 'OFFLINE')),
    CONSTRAINT physical_devices_generation_positive CHECK (generation > 0),
    CONSTRAINT physical_devices_max_slots_nonnegative CHECK (max_slots >= 0),
    CONSTRAINT physical_devices_reserved_slots_nonnegative CHECK (reserved_slots >= 0),
    CONSTRAINT physical_devices_device_key_unique UNIQUE (device_key)
);

CREATE TABLE openfox.organization_capacity (
    organization_id uuid PRIMARY KEY,
    max_slots integer NOT NULL,
    reserved_slots integer NOT NULL DEFAULT 0,
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    CONSTRAINT organization_capacity_max_slots_nonnegative CHECK (max_slots >= 0),
    CONSTRAINT organization_capacity_reserved_slots_nonnegative CHECK (reserved_slots >= 0)
);

CREATE TABLE openfox.device_grants (
    organization_id uuid NOT NULL,
    device_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    permission_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, device_id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    CONSTRAINT device_grants_status_valid CHECK (status IN ('ACTIVE', 'REVOKED', 'EXPIRED')),
    CONSTRAINT device_grants_permission_version_positive CHECK (permission_version > 0)
);

CREATE TABLE openfox.agents (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    device_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    generation bigint NOT NULL DEFAULT 1,
    max_slots integer NOT NULL DEFAULT 1,
    reserved_slots integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, id, device_id),
    FOREIGN KEY (organization_id, device_id)
        REFERENCES openfox.device_grants (organization_id, device_id),
    CONSTRAINT agents_status_valid CHECK (status IN ('ACTIVE', 'REVOKED', 'OFFLINE')),
    CONSTRAINT agents_generation_positive CHECK (generation > 0),
    CONSTRAINT agents_max_slots_nonnegative CHECK (max_slots >= 0),
    CONSTRAINT agents_reserved_slots_nonnegative CHECK (reserved_slots >= 0)
);

CREATE TABLE openfox.capacity_reservations (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    device_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'RESERVED',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, id, run_id, generation, device_id),
    FOREIGN KEY (organization_id, agent_id, device_id)
        REFERENCES openfox.agents (organization_id, id, device_id),
    CONSTRAINT capacity_reservations_generation_positive CHECK (generation > 0),
    CONSTRAINT capacity_reservations_status_valid CHECK (status IN ('RESERVED', 'CLAIMED', 'RELEASED')),
    CONSTRAINT capacity_reservations_release_time_valid CHECK (
        (status = 'RELEASED' AND released_at IS NOT NULL)
        OR (status <> 'RELEASED' AND released_at IS NULL)
    )
);

CREATE UNIQUE INDEX capacity_reservations_effective_run_unique
    ON openfox.capacity_reservations (organization_id, run_id, generation)
    WHERE status IN ('RESERVED', 'CLAIMED');

CREATE TABLE openfox.dispatch_offers (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    reservation_id uuid NOT NULL,
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    target_device_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'LIVE',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    claimed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, reservation_id, run_id, generation, target_device_id)
        REFERENCES openfox.capacity_reservations (organization_id, id, run_id, generation, device_id),
    CONSTRAINT dispatch_offers_generation_positive CHECK (generation > 0),
    CONSTRAINT dispatch_offers_status_valid CHECK (status IN ('LIVE', 'CLAIMED', 'EXPIRED', 'CANCELED')),
    CONSTRAINT dispatch_offers_claim_time_valid CHECK (
        (status = 'CLAIMED' AND claimed_at IS NOT NULL)
        OR (status <> 'CLAIMED' AND claimed_at IS NULL)
    )
);

CREATE UNIQUE INDEX dispatch_offers_effective_run_unique
    ON openfox.dispatch_offers (organization_id, run_id, generation)
    WHERE status = 'LIVE';
CREATE INDEX dispatch_offers_expiry_idx
    ON openfox.dispatch_offers (status, expires_at, organization_id, id);

RESET ROLE;

-- Runtime code may read grant and Agent state, but capacity counters and
-- device lifecycle changes go through reviewed control-plane operations.
GRANT SELECT ON
    openfox.device_grants,
    openfox.agents
TO openfox_app;
GRANT SELECT ON
    openfox.capacity_reservations,
    openfox.dispatch_offers
TO openfox_app;

ALTER TABLE openfox.organization_capacity ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_capacity FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agents FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.capacity_reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.dispatch_offers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.dispatch_offers FORCE ROW LEVEL SECURITY;

CREATE POLICY organization_capacity_tenant_isolation ON openfox.organization_capacity
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY device_grants_tenant_isolation ON openfox.device_grants
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY agents_tenant_isolation ON openfox.agents
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY capacity_reservations_tenant_isolation ON openfox.capacity_reservations
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY dispatch_offers_tenant_isolation ON openfox.dispatch_offers
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

CREATE OR REPLACE FUNCTION openfox_private.reserve_capacity(
    p_organization_id uuid,
    p_reservation_id uuid,
    p_run_id uuid,
    p_generation bigint,
    p_device_id uuid,
    p_agent_id uuid,
    p_offer_id uuid,
    p_expires_at timestamptz
) RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    ignored integer;
    reservation_status text;
BEGIN
    IF p_organization_id IS NULL
       OR p_reservation_id IS NULL
       OR p_run_id IS NULL
       OR p_generation IS NULL
       OR p_generation <= 0
       OR p_device_id IS NULL
       OR p_agent_id IS NULL
       OR p_offer_id IS NULL
       OR p_expires_at IS NULL
       OR p_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'invalid capacity reservation' USING ERRCODE = '22023';
    END IF;
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity not configured' USING ERRCODE = '23503';
    END IF;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = p_device_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device not found' USING ERRCODE = '23503';
    END IF;
    PERFORM 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = p_agent_id AND device_id = p_device_id
        FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'agent not found' USING ERRCODE = '23503';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM openfox.device_grants
        WHERE organization_id = p_organization_id
          AND device_id = p_device_id
          AND status = 'ACTIVE'
    ) THEN
        RAISE EXCEPTION 'device grant is not active' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM openfox.physical_devices
        WHERE id = p_device_id AND status = 'ACTIVE' AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'physical device capacity exhausted' USING ERRCODE = '55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'organization capacity exhausted' USING ERRCODE = '55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = p_agent_id
          AND status = 'ACTIVE' AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'agent capacity exhausted' USING ERRCODE = '55000';
    END IF;

    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots + 1
     WHERE organization_id = p_organization_id;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots + 1
     WHERE id = p_device_id;
    UPDATE openfox.agents
       SET reserved_slots = reserved_slots + 1
     WHERE organization_id = p_organization_id AND id = p_agent_id;

    INSERT INTO openfox.capacity_reservations (
        organization_id, id, run_id, generation, device_id, agent_id
    ) VALUES (
        p_organization_id, p_reservation_id, p_run_id, p_generation, p_device_id, p_agent_id
    );
    INSERT INTO openfox.dispatch_offers (
        organization_id, id, reservation_id, run_id, generation, target_device_id, expires_at
    ) VALUES (
        p_organization_id, p_offer_id, p_reservation_id, p_run_id, p_generation, p_device_id, p_expires_at
    );
    RETURN p_offer_id;
EXCEPTION
    WHEN unique_violation THEN
        RAISE EXCEPTION 'capacity reservation or offer conflicts with an existing effective operation'
            USING ERRCODE = '23505';
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.claim_offer(
    p_organization_id uuid,
    p_offer_id uuid,
    p_device_id uuid,
    p_generation bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    reservation_id uuid;
    reservation_run_id uuid;
    offer_generation bigint;
    offer_device_id uuid;
    offer_status text;
    reservation_status text;
BEGIN
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match offer' USING ERRCODE = '42501';
    END IF;
    SELECT o.reservation_id, o.run_id, o.generation, o.target_device_id, o.status
      INTO reservation_id, reservation_run_id, offer_generation, offer_device_id, offer_status
      FROM openfox.dispatch_offers o
     WHERE o.organization_id = p_organization_id AND o.id = p_offer_id;
    IF NOT FOUND OR offer_status <> 'LIVE' THEN
        RETURN false;
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = offer_device_id FOR UPDATE;
    PERFORM 1
      FROM openfox.agents a
      JOIN openfox.capacity_reservations r
        ON r.organization_id = a.organization_id AND r.agent_id = a.id AND r.device_id = a.device_id
     WHERE r.organization_id = p_organization_id AND r.id = reservation_id
     FOR UPDATE OF a;
    SELECT r.status INTO reservation_status
      FROM openfox.capacity_reservations r
     WHERE r.organization_id = p_organization_id AND r.id = reservation_id
     FOR UPDATE;
    IF reservation_status IS DISTINCT FROM 'RESERVED' THEN
        RETURN false;
    END IF;
    SELECT status INTO offer_status
      FROM openfox.dispatch_offers
     WHERE organization_id = p_organization_id AND id = p_offer_id
     FOR UPDATE;

    IF offer_status <> 'LIVE'
       OR offer_generation <> p_generation
       OR offer_device_id <> p_device_id
       OR EXISTS (
           SELECT 1 FROM openfox.dispatch_offers
            WHERE organization_id = p_organization_id AND id = p_offer_id
              AND expires_at <= clock_timestamp()
       )
       OR NOT EXISTS (
           SELECT 1 FROM openfox.device_grants
            WHERE organization_id = p_organization_id AND device_id = p_device_id AND status = 'ACTIVE'
       ) THEN
        RETURN false;
    END IF;

    UPDATE openfox.dispatch_offers
       SET status = 'CLAIMED', claimed_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = p_offer_id;
    UPDATE openfox.capacity_reservations
       SET status = 'CLAIMED'
     WHERE organization_id = p_organization_id AND id = reservation_id AND status = 'RESERVED';
    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.release_reservation(
    p_organization_id uuid,
    p_reservation_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    reservation_device_id uuid;
    reservation_agent_id uuid;
    reservation_status text;
BEGIN
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;
    SELECT device_id, agent_id, status
      INTO reservation_device_id, reservation_agent_id, reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = p_reservation_id;
    IF NOT FOUND OR reservation_status <> 'RESERVED' THEN
        RETURN false;
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = reservation_device_id FOR UPDATE;
    PERFORM 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = reservation_agent_id
        FOR UPDATE;
    PERFORM 1 FROM openfox.capacity_reservations
        WHERE organization_id = p_organization_id AND id = p_reservation_id
        FOR UPDATE;

    SELECT status INTO reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = p_reservation_id;
    IF reservation_status IS DISTINCT FROM 'RESERVED' THEN
        RETURN false;
    END IF;
    UPDATE openfox.dispatch_offers
       SET status = 'CANCELED'
     WHERE organization_id = p_organization_id
       AND reservation_id = p_reservation_id
       AND status = 'LIVE';
    UPDATE openfox.capacity_reservations
       SET status = 'RELEASED', released_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = p_reservation_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'reservation state changed while releasing' USING ERRCODE = '40001';
    END IF;
    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots - 1
     WHERE id = reservation_device_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.agents
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND id = reservation_agent_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Agent capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.reap_offer(
    p_organization_id uuid,
    p_offer_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    reservation_id uuid;
    reservation_device_id uuid;
    reservation_agent_id uuid;
    offer_status text;
    offer_expiry timestamptz;
    reservation_status text;
BEGIN
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match offer' USING ERRCODE = '42501';
    END IF;
    SELECT o.reservation_id, o.status, o.expires_at
      INTO reservation_id, offer_status, offer_expiry
      FROM openfox.dispatch_offers o
     WHERE o.organization_id = p_organization_id AND o.id = p_offer_id;
    IF NOT FOUND OR offer_status <> 'LIVE' OR offer_expiry > clock_timestamp() THEN
        RETURN false;
    END IF;
    SELECT device_id, agent_id, status
      INTO reservation_device_id, reservation_agent_id, reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = reservation_id;
    IF NOT FOUND OR reservation_status <> 'RESERVED' THEN
        RETURN false;
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = reservation_device_id FOR UPDATE;
    PERFORM 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = reservation_agent_id
        FOR UPDATE;
    SELECT status INTO reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = reservation_id
     FOR UPDATE;
    IF reservation_status IS DISTINCT FROM 'RESERVED' THEN
        RETURN false;
    END IF;
    SELECT status INTO offer_status
      FROM openfox.dispatch_offers
     WHERE organization_id = p_organization_id AND id = p_offer_id
     FOR UPDATE;
    IF offer_status <> 'LIVE'
       OR NOT EXISTS (
           SELECT 1 FROM openfox.dispatch_offers
            WHERE organization_id = p_organization_id AND id = p_offer_id
              AND expires_at <= clock_timestamp()
       ) THEN
        RETURN false;
    END IF;

    UPDATE openfox.dispatch_offers
       SET status = 'EXPIRED'
     WHERE organization_id = p_organization_id AND id = p_offer_id;
    UPDATE openfox.capacity_reservations
       SET status = 'RELEASED', released_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = reservation_id AND status = 'RESERVED';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'reservation state changed while reaping' USING ERRCODE = '40001';
    END IF;
    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots - 1
     WHERE id = reservation_device_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.agents
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND id = reservation_agent_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Agent capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    RETURN true;
END
$$;

ALTER FUNCTION openfox_private.reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.claim_offer(uuid, uuid, uuid, bigint) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.release_reservation(uuid, uuid) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.reap_offer(uuid, uuid) OWNER TO openfox_migrator;

REVOKE ALL ON FUNCTION openfox_private.reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.claim_offer(uuid, uuid, uuid, bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.release_reservation(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.reap_offer(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.claim_offer(uuid, uuid, uuid, bigint) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.release_reservation(uuid, uuid) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.reap_offer(uuid, uuid) TO openfox_context;

