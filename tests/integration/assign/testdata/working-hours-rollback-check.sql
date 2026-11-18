-- A04 expand-only binary rollback gate. Read-only, run after stopping writers
-- and dispatch; use an operator connection that can inspect ALL tenants.
-- Do not remove migration 125 or its guards. Before retrying a rejected gate,
-- use the new application's normal configuration and cancellation APIs, or
-- restore a verified pre-upgrade backup. Never clear schedule_pending by SQL.
BEGIN READ ONLY;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM openfox.agents a JOIN openfox.agent_config_versions c
   ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version
   WHERE c.working_hours->>'enabled'='true') THEN
   RAISE EXCEPTION 'A04 rollback blocked: enabled working-hours configuration';
 END IF;
 IF EXISTS(SELECT 1 FROM openfox.runs WHERE state='QUEUED' AND (schedule_pending OR not_before>clock_timestamp())) THEN
   RAISE EXCEPTION 'A04 rollback blocked: deferred Run must be canceled or admitted normally';
 END IF;
 IF EXISTS(SELECT 1 FROM openfox.task_agent_events WHERE processed_at IS NULL AND (kind='MENTIONED' OR requested_start_at IS NOT NULL)) THEN
   RAISE EXCEPTION 'A04 rollback blocked: new task trigger still pending';
 END IF;
 IF EXISTS(SELECT 1 FROM openfox.conversation_inputs WHERE status='PENDING' AND (trigger_kind<>'LEGACY' OR requested_start_at IS NOT NULL)) THEN
   RAISE EXCEPTION 'A04 rollback blocked: classified input still pending';
 END IF;
END $$;
ROLLBACK;
