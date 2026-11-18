-- A04 integration: reuse deployed 000114 comment_id, crew_id, mention shape and unique index. No historical comments are replayed.
-- Downgrade requires stopping dispatch writers and safely draining/canceling
-- scheduled runs and MENTIONED events; retaining columns alone is not sufficient.
SET ROLE openfox_migrator;
ALTER TABLE openfox.agent_config_versions ADD COLUMN working_hours jsonb,
 ADD CONSTRAINT agent_working_hours_shape CHECK (working_hours IS NULL OR (jsonb_typeof(working_hours)='object' AND octet_length(working_hours::text)<=4096));
ALTER TABLE openfox.task_agent_events
 ADD COLUMN requested_start_at timestamptz,
 DROP CONSTRAINT task_agent_events_kind_valid,
 ADD CONSTRAINT task_agent_events_kind_valid CHECK (kind IN ('ASSIGNED','UNASSIGNED','MENTIONED')),
 ADD CONSTRAINT task_agent_events_comment_shape CHECK ((kind='MENTIONED')=(comment_id IS NOT NULL)),
 ADD CONSTRAINT task_agent_events_requested_time CHECK (requested_start_at IS NULL OR isfinite(requested_start_at));
ALTER TABLE openfox.conversation_inputs
 ADD COLUMN trigger_kind text NOT NULL DEFAULT 'LEGACY',
 ADD COLUMN requested_start_at timestamptz,
 ADD CONSTRAINT conversation_inputs_trigger_kind CHECK (trigger_kind IN ('LEGACY','ASSIGNMENT','COMMENT_MENTION','DIRECT_CHAT','OTHER')),
 ADD CONSTRAINT conversation_inputs_requested_time CHECK (requested_start_at IS NULL OR isfinite(requested_start_at));
ALTER TABLE openfox.runs
 ADD COLUMN trigger_kind text NOT NULL DEFAULT 'LEGACY',
 ADD COLUMN requested_start_at timestamptz,
 ADD COLUMN not_before timestamptz,
 ADD COLUMN schedule_pending boolean NOT NULL DEFAULT false,
 ADD COLUMN schedule_config_version bigint,
 ADD COLUMN working_hours_bypassed boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT runs_schedule_trigger_kind CHECK (trigger_kind IN ('LEGACY','ASSIGNMENT','COMMENT_MENTION','DIRECT_CHAT','OTHER')),
 ADD CONSTRAINT runs_schedule_times CHECK ((requested_start_at IS NULL OR isfinite(requested_start_at)) AND (not_before IS NULL OR isfinite(not_before))),
 ADD CONSTRAINT runs_schedule_version CHECK (schedule_config_version IS NULL OR schedule_config_version>0),
 ADD CONSTRAINT runs_pending_no_capacity CHECK (NOT schedule_pending OR (state NOT IN ('PREPARING','RUNNING','CANCEL_REQUESTED') AND reservation_id IS NULL AND offer_id IS NULL AND queue_expires_at IS NULL));
ALTER TABLE openfox.runs DROP CONSTRAINT runs_wait_reason_valid;
ALTER TABLE openfox.runs ADD CONSTRAINT runs_wait_reason_valid CHECK (
    wait_reason IN ('', 'ORGANIZATION_CAPACITY', 'AGENT_CONCURRENCY', 'AGENT_CHAT_CONCURRENCY', 'COMPUTER_CONCURRENCY',
                    'CONVERSATION_BUSY', 'WORKDIR_BUSY', 'AWAITING_COMPUTER',
                    'COMPUTER_OFFLINE', 'TASK_GATE', 'OFFER_EXPIRED', 'RUNTIME_UNAVAILABLE', 'SCHEDULED')
) NOT VALID;
ALTER TABLE openfox.runs VALIDATE CONSTRAINT runs_wait_reason_valid;
CREATE INDEX runs_schedule_due ON openfox.runs(organization_id,not_before,id) WHERE state='QUEUED' AND schedule_pending AND not_before IS NOT NULL;

-- reserve_capacity already holds organization -> physical device -> Agent locks
-- before this insert. No new inverse lock ordering or broader table grant.
CREATE FUNCTION openfox_private.guard_scheduled_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM openfox.runs r WHERE r.organization_id=NEW.organization_id AND r.id=NEW.run_id AND (r.schedule_pending OR r.not_before>clock_timestamp())) THEN
  RAISE EXCEPTION 'run is awaiting its scheduled time' USING ERRCODE='40001';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.guard_scheduled_reservation() FROM PUBLIC;
CREATE TRIGGER capacity_reservations_schedule_gate BEFORE INSERT ON openfox.capacity_reservations FOR EACH ROW EXECUTE FUNCTION openfox_private.guard_scheduled_reservation();

-- Defense for stale/fabricated offers and direct state transitions, including
-- device claim/start. This does not grant permission to change any Run.
CREATE FUNCTION openfox_private.guard_scheduled_run() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 IF (NEW.state IN ('PREPARING','RUNNING') OR NEW.reservation_id IS NOT NULL OR NEW.offer_id IS NOT NULL)
 AND (NEW.schedule_pending OR NEW.not_before>clock_timestamp()) THEN
  RAISE EXCEPTION 'run is awaiting its scheduled time' USING ERRCODE='40001';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.guard_scheduled_run() FROM PUBLIC;
CREATE TRIGGER runs_schedule_gate BEFORE INSERT OR UPDATE OF state,reservation_id,offer_id,schedule_pending,not_before ON openfox.runs FOR EACH ROW EXECUTE FUNCTION openfox_private.guard_scheduled_run();

CREATE OR REPLACE FUNCTION openfox_private.list_dispatch_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid dispatch organization limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    WITH work AS (
        SELECT i.organization_id FROM openfox.conversation_inputs i WHERE i.status = 'PENDING'
 AND NOT EXISTS (SELECT 1 FROM openfox.runs r WHERE r.organization_id=i.organization_id AND r.conversation_id=i.conversation_id AND r.state IN ('QUEUED','PREPARING','RUNNING','CANCEL_REQUESTED'))
        UNION
        SELECT r.organization_id FROM openfox.runs r
 JOIN openfox.agents a ON a.organization_id=r.organization_id AND a.id=r.agent_id
 WHERE r.state = 'QUEUED' AND (NOT r.schedule_pending OR r.not_before <= clock_timestamp() OR r.schedule_config_version IS DISTINCT FROM a.current_config_version)
        UNION
        SELECT o.organization_id FROM openfox.dispatch_offers o
         WHERE o.status = 'LIVE' AND o.expires_at <= clock_timestamp()
        UNION
        SELECT r.organization_id FROM openfox.runs r
         WHERE r.lease_expires_at IS NOT NULL
           AND r.lease_expires_at <= clock_timestamp()
           AND r.reconciliation_required = false
           AND r.state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
        UNION
        SELECT c.organization_id FROM openfox.capacity_reservations c
          LEFT JOIN openfox.runs r ON r.organization_id = c.organization_id AND r.id = c.run_id
         WHERE c.status IN ('RESERVED', 'CLAIMED')
           AND c.created_at <= clock_timestamp() - interval '1 minute'
           AND (r.id IS NULL
                OR r.state IN ('SUCCEEDED', 'FAILED', 'CANCELED')
                OR r.reservation_id IS DISTINCT FROM c.id)
        UNION
        SELECT e.organization_id FROM openfox.task_agent_events e WHERE e.processed_at IS NULL
    )
    SELECT w.organization_id
      FROM work w
     ORDER BY (p_after IS NOT NULL AND w.organization_id <= p_after), w.organization_id
     LIMIT p_limit;
END
$$;


RESET ROLE;
