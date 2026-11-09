-- WP-09 execution core. Runs, durable input/trigger deduplication, and the
-- outbox are tenant rows. Capacity rows remain owned by DB-002; this migration
-- only makes release safe after an offer has been claimed.

SET ROLE openfox_migrator;

CREATE TABLE openfox.conversations (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid,
    owner_user_id uuid NOT NULL,
    lifecycle text NOT NULL DEFAULT 'ACTIVE',
    handoff text NOT NULL DEFAULT 'NONE',
    binding_id uuid,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, id, space_id, agent_id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, owner_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT conversations_lifecycle_valid
        CHECK (lifecycle IN ('ACTIVE', 'COMPLETED', 'ARCHIVED')),
    CONSTRAINT conversations_handoff_valid
        CHECK (handoff IN ('NONE', 'REQUESTED', 'ACCEPTED')),
    CONSTRAINT conversations_version_positive CHECK (version > 0)
);

CREATE INDEX conversations_space_idx
    ON openfox.conversations (organization_id, space_id, updated_at DESC, id);
CREATE INDEX conversations_agent_idx
    ON openfox.conversations (organization_id, agent_id, updated_at DESC, id);

CREATE OR REPLACE FUNCTION openfox_private.validate_conversation_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    agent_space_id uuid;
    agent_status text;
    agent_archived_at timestamptz;
    task_space_id uuid;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.organization_id = OLD.organization_id
       AND NEW.space_id = OLD.space_id
       AND NEW.agent_id = OLD.agent_id
       AND NEW.task_id IS NOT DISTINCT FROM OLD.task_id THEN
        RETURN NEW;
    END IF;
    SELECT space_id, status, archived_at
      INTO agent_space_id, agent_status, agent_archived_at
      FROM openfox.agents
     WHERE organization_id = NEW.organization_id AND id = NEW.agent_id;
    IF NOT FOUND OR agent_space_id IS NULL OR agent_space_id <> NEW.space_id
       OR agent_status <> 'ACTIVE' OR agent_archived_at IS NOT NULL THEN
        RAISE EXCEPTION 'conversation Agent is not active in the requested space'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.task_id IS NOT NULL THEN
        SELECT space_id INTO task_space_id
          FROM openfox.tasks
         WHERE organization_id = NEW.organization_id AND id = NEW.task_id;
        IF NOT FOUND OR task_space_id <> NEW.space_id THEN
            RAISE EXCEPTION 'conversation task is outside the requested space'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_conversation_scope() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_conversation_scope() FROM PUBLIC;

CREATE TRIGGER conversations_scope_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.conversations
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_conversation_scope();

CREATE TABLE openfox.conversation_inputs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL,
    message_id uuid,
    body text NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    run_id uuid,
    source_event_id text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    assigned_at timestamptz,
    consumed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    CONSTRAINT conversation_inputs_body_not_blank CHECK (length(btrim(body)) > 0),
    CONSTRAINT conversation_inputs_body_length CHECK (length(body) <= 65536),
    CONSTRAINT conversation_inputs_source_length CHECK (
        source_event_id IS NULL OR length(source_event_id) BETWEEN 1 AND 256
    ),
    CONSTRAINT conversation_inputs_status_valid
        CHECK (status IN ('PENDING', 'ASSIGNED', 'CONSUMED', 'REJECTED')),
    CONSTRAINT conversation_inputs_assigned_time_valid CHECK (
        (status = 'PENDING' AND assigned_at IS NULL)
        OR (status <> 'PENDING' AND assigned_at IS NOT NULL)
    ),
    CONSTRAINT conversation_inputs_consumed_time_valid CHECK (
        (status = 'CONSUMED' AND consumed_at IS NOT NULL)
        OR (status <> 'CONSUMED' AND consumed_at IS NULL)
    )
);

CREATE UNIQUE INDEX conversation_inputs_source_unique
    ON openfox.conversation_inputs (organization_id, source_event_id)
    WHERE source_event_id IS NOT NULL;
CREATE INDEX conversation_inputs_queue_idx
    ON openfox.conversation_inputs (organization_id, conversation_id, status, created_at, id);

CREATE TABLE openfox.execution_triggers (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    source_event_id text NOT NULL,
    source_kind text NOT NULL,
    conversation_id uuid NOT NULL,
    run_id uuid,
    outcome text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, source_event_id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    CONSTRAINT execution_triggers_source_not_blank CHECK (length(btrim(source_event_id)) BETWEEN 1 AND 256),
    CONSTRAINT execution_triggers_kind_not_blank CHECK (length(btrim(source_kind)) BETWEEN 1 AND 64),
    CONSTRAINT execution_triggers_outcome_valid
        CHECK (outcome IN ('CREATED', 'MERGED', 'REJECTED', 'DUPLICATE'))
);

CREATE TABLE openfox.runs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    task_id uuid,
    agent_id uuid NOT NULL,
    computer_id uuid NOT NULL,
    grant_id uuid NOT NULL,
    config_version bigint NOT NULL,
    grant_version bigint NOT NULL,
    actor_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'QUEUED',
    generation bigint NOT NULL DEFAULT 1,
    reconciliation_required boolean NOT NULL DEFAULT false,
    retry_of uuid,
    reason text NOT NULL DEFAULT '',
    wait_reason text NOT NULL DEFAULT '',
    reservation_id uuid,
    offer_id uuid,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    cancel_requested_at timestamptz,
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id, space_id, agent_id)
        REFERENCES openfox.conversations (organization_id, id, space_id, agent_id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (computer_id)
        REFERENCES openfox.physical_devices (id),
    FOREIGN KEY (organization_id, grant_id)
        REFERENCES openfox.device_grants (organization_id, id),
    FOREIGN KEY (organization_id, actor_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, retry_of)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT runs_state_valid CHECK (
        state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED',
                  'SUCCEEDED', 'FAILED', 'CANCELED')
    ),
    CONSTRAINT runs_generation_positive CHECK (generation > 0),
    CONSTRAINT runs_config_version_positive CHECK (config_version > 0),
    CONSTRAINT runs_grant_version_positive CHECK (grant_version > 0),
    CONSTRAINT runs_version_positive CHECK (version > 0),
    CONSTRAINT runs_capacity_pair_valid CHECK (
        (reservation_id IS NULL AND offer_id IS NULL)
        OR (reservation_id IS NOT NULL AND offer_id IS NOT NULL)
    ),
    CONSTRAINT runs_wait_reason_valid CHECK (
        wait_reason IN ('', 'ORGANIZATION_CAPACITY', 'AGENT_CONCURRENCY', 'COMPUTER_CONCURRENCY',
                        'CONVERSATION_BUSY', 'WORKDIR_BUSY', 'AWAITING_COMPUTER',
                        'COMPUTER_OFFLINE', 'TASK_GATE', 'OFFER_EXPIRED')
    ),
    CONSTRAINT runs_terminal_time_valid CHECK (
        (state IN ('SUCCEEDED', 'FAILED', 'CANCELED') AND finished_at IS NOT NULL)
        OR (state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED') AND finished_at IS NULL)
    ),
    CONSTRAINT runs_cancel_time_valid CHECK (
        (state IN ('CANCEL_REQUESTED', 'CANCELED')
            AND cancel_requested_at IS NOT NULL)
        OR (state NOT IN ('CANCEL_REQUESTED', 'CANCELED')
            AND cancel_requested_at IS NULL)
    )
);

CREATE UNIQUE INDEX runs_active_conversation_unique
    ON openfox.runs (organization_id, conversation_id)
    WHERE state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED');
CREATE UNIQUE INDEX runs_active_offer_unique
    ON openfox.runs (organization_id, offer_id)
    WHERE offer_id IS NOT NULL AND state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED');
CREATE INDEX runs_queue_idx
    ON openfox.runs (organization_id, state, wait_reason, created_at, id);
CREATE INDEX runs_conversation_idx
    ON openfox.runs (organization_id, conversation_id, created_at DESC, id);

CREATE OR REPLACE FUNCTION openfox_private.validate_run_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    conversation_task_id uuid;
    agent_device_id uuid;
    grant_device_id uuid;
    grant_status text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.organization_id <> OLD.organization_id
           OR NEW.space_id <> OLD.space_id
           OR NEW.conversation_id <> OLD.conversation_id
           OR NEW.agent_id <> OLD.agent_id
           OR NEW.computer_id <> OLD.computer_id
           OR NEW.grant_id <> OLD.grant_id
           OR NEW.task_id IS DISTINCT FROM OLD.task_id THEN
            RAISE EXCEPTION 'Run identity is immutable' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    SELECT task_id INTO conversation_task_id
      FROM openfox.conversations
     WHERE organization_id = NEW.organization_id
       AND id = NEW.conversation_id
       AND space_id = NEW.space_id
       AND agent_id = NEW.agent_id;
    IF NOT FOUND OR NEW.task_id IS DISTINCT FROM conversation_task_id THEN
        RAISE EXCEPTION 'Run task does not match its conversation'
            USING ERRCODE = '23514';
    END IF;

    SELECT device_id INTO agent_device_id
      FROM openfox.agents
     WHERE organization_id = NEW.organization_id AND id = NEW.agent_id;
    SELECT device_id, status INTO grant_device_id, grant_status
      FROM openfox.device_grants
     WHERE organization_id = NEW.organization_id AND id = NEW.grant_id;
    IF NOT FOUND OR grant_status <> 'ACTIVE' OR agent_device_id IS DISTINCT FROM NEW.computer_id
       OR grant_device_id IS DISTINCT FROM NEW.computer_id THEN
        RAISE EXCEPTION 'Run device grant does not match the Agent computer'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_run_scope() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_run_scope() FROM PUBLIC;

CREATE TRIGGER runs_scope_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.runs
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_run_scope();

ALTER TABLE openfox.conversation_inputs
    ADD CONSTRAINT conversation_inputs_run_fk
        FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id);

ALTER TABLE openfox.execution_triggers
    ADD CONSTRAINT execution_triggers_run_fk
        FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id);

CREATE TABLE openfox.run_idempotency (
    organization_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    actor_id uuid NOT NULL,
    request_hash text NOT NULL,
    run_id uuid,
    response_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    PRIMARY KEY (organization_id, operation, idempotency_key),
    FOREIGN KEY (organization_id, actor_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_idempotency_operation_valid
        CHECK (operation ~ '^[a-z][a-z0-9_.-]{1,63}$'),
    CONSTRAINT run_idempotency_key_not_blank CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 256),
    CONSTRAINT run_idempotency_hash_not_blank CHECK (length(btrim(request_hash)) BETWEEN 1 AND 128),
    CONSTRAINT run_idempotency_response_object CHECK (jsonb_typeof(response_state) = 'object')
);

CREATE TABLE openfox.execution_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    aggregate_id uuid NOT NULL,
    payload jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id)
        REFERENCES openfox.organizations (id),
    CONSTRAINT execution_outbox_kind_valid CHECK (kind ~ '^[a-z][a-z0-9_.-]{1,63}$'),
    CONSTRAINT execution_outbox_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT execution_outbox_attempts_nonnegative CHECK (attempts >= 0)
);

CREATE INDEX execution_outbox_pending_idx
    ON openfox.execution_outbox (organization_id, next_attempt_at, created_at, id)
    WHERE delivered_at IS NULL;

-- The capacity-era function originally released only RESERVED rows. A Run can
-- finish after an Agent claims its offer, so release must accept CLAIMED while
-- preserving the same lock order and exactly-once counter decrement.
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
    IF p_organization_id IS NULL OR p_reservation_id IS NULL
       OR p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;
    SELECT device_id, agent_id, status
      INTO reservation_device_id, reservation_agent_id, reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = p_reservation_id;
    IF NOT FOUND OR reservation_status NOT IN ('RESERVED', 'CLAIMED') THEN
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
    IF reservation_status IS DISTINCT FROM 'RESERVED'
       AND reservation_status IS DISTINCT FROM 'CLAIMED' THEN
        RETURN false;
    END IF;

    UPDATE openfox.dispatch_offers
       SET status = 'CANCELED'
     WHERE organization_id = p_organization_id
       AND reservation_id = p_reservation_id
       AND status = 'LIVE';
    UPDATE openfox.capacity_reservations
       SET status = 'RELEASED', released_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = p_reservation_id
       AND status IN ('RESERVED', 'CLAIMED');
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
     WHERE organization_id = p_organization_id AND id = reservation_agent_id
       AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Agent capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    RETURN true;
END
$$;

ALTER FUNCTION openfox_private.release_reservation(uuid, uuid) OWNER TO openfox_migrator;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON
    openfox.conversations,
    openfox.conversation_inputs,
    openfox.execution_triggers,
    openfox.runs,
    openfox.run_idempotency,
    openfox.execution_outbox
TO openfox_app;

ALTER TABLE openfox.conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversations FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_inputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_inputs FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.execution_triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.execution_triggers FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.runs FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_idempotency FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.execution_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.execution_outbox FORCE ROW LEVEL SECURITY;

CREATE POLICY conversations_tenant_isolation ON openfox.conversations
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY conversation_inputs_tenant_isolation ON openfox.conversation_inputs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY execution_triggers_tenant_isolation ON openfox.execution_triggers
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY runs_tenant_isolation ON openfox.runs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_idempotency_tenant_isolation ON openfox.run_idempotency
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY execution_outbox_tenant_isolation ON openfox.execution_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- FORCE RLS also applies to the non-login migration owner used by the
-- security-definer scope checks. These read-only policies do not widen the
-- application role and mirror the WP-04 trigger visibility pattern.
CREATE POLICY agents_execution_trigger_access ON openfox.agents
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY tasks_execution_trigger_access ON openfox.tasks
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY device_grants_execution_trigger_access ON openfox.device_grants
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY conversations_execution_trigger_access ON openfox.conversations
    FOR SELECT TO openfox_migrator USING (true);

REVOKE ALL ON FUNCTION openfox_private.release_reservation(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.release_reservation(uuid, uuid) TO openfox_context;
