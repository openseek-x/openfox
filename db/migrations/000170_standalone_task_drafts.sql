-- A draft runs as a device command. Only assignment of the resulting task can create a conversation.
SET ROLE openfox_migrator;
CREATE TABLE openfox.task_drafts (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    space_id uuid NOT NULL,
    requester_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    brief text NOT NULL CHECK (length(brief) BETWEEN 1 AND 10000),
    options jsonb NOT NULL CHECK (jsonb_typeof(options)='object'),
    finished_at timestamptz,
    task_id uuid,
    PRIMARY KEY (organization_id,id),
    FOREIGN KEY (organization_id,space_id) REFERENCES openfox.spaces(organization_id,id),
    FOREIGN KEY (organization_id,requester_id) REFERENCES openfox.organization_members(organization_id,user_id),
    FOREIGN KEY (organization_id,agent_id) REFERENCES openfox.agents(organization_id,id)
);
CREATE INDEX task_drafts_pending ON openfox.task_drafts(organization_id,id) WHERE finished_at IS NULL;
ALTER TABLE openfox.task_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_drafts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON openfox.task_drafts USING (organization_id=openfox_private.current_organization_id());
GRANT SELECT, INSERT, UPDATE(finished_at,task_id) ON openfox.task_drafts TO openfox_app;

ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_kind_valid,
    ADD CONSTRAINT device_commands_kind_valid CHECK (kind IN ('runtime_probe','runtime_test','storage_cleanup','skill_scan','skill_import','device_settings','workspace_path','runtime_session_export','key_rotate','task_draft'));
ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_result_shape,
    ADD CONSTRAINT device_commands_result_shape CHECK (result IS NULL OR (jsonb_typeof(result)='object' AND pg_column_size(result)<=CASE WHEN kind IN ('skill_scan','task_draft') THEN 65536 ELSE 16384 END));
DROP INDEX openfox.device_commands_open_unique;
CREATE UNIQUE INDEX device_commands_open_unique ON openfox.device_commands(organization_id,device_id,kind)
    WHERE status IN ('PENDING','DELIVERED') AND kind<>'task_draft';

ALTER TABLE openfox.notifications ADD COLUMN task_draft_id uuid,
    ADD FOREIGN KEY (organization_id,task_draft_id) REFERENCES openfox.task_drafts(organization_id,id),
    DROP CONSTRAINT notifications_source_present,
    ADD CONSTRAINT notifications_source_present CHECK (conversation_id IS NOT NULL OR task_id IS NOT NULL OR automation_id IS NOT NULL OR (direct_chat_id IS NOT NULL AND direct_message_id IS NOT NULL) OR task_draft_id IS NOT NULL);
CREATE FUNCTION openfox_private.notify_task_draft(p_id uuid) RETURNS void
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,inbox_event,task_id,task_draft_id,dedupe_key,safe_summary)
    SELECT q.organization_id,q.requester_id,CASE WHEN q.task_id IS NULL THEN 'RUN_FAILED' ELSE 'RUN_COMPLETED' END,
        'task_creation_result_requester',q.task_id,q.id,'task-draft:' || q.id::text,
        CASE WHEN q.task_id IS NULL THEN '任务创建失败，打开通知可用原始描述手动创建。' ELSE '已创建任务 ' || s.prefix || '-' || t.sequence::text END
    FROM openfox.task_drafts q JOIN openfox.spaces s ON s.organization_id=q.organization_id AND s.id=q.space_id
    JOIN openfox.organization_members m ON m.organization_id=q.organization_id AND m.user_id=q.requester_id AND m.status='ACTIVE'
    LEFT JOIN openfox.tasks t ON t.organization_id=q.organization_id AND t.id=q.task_id
    WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=p_id AND q.finished_at IS NOT NULL
    ON CONFLICT(organization_id,recipient_user_id,dedupe_key) DO NOTHING
$$;
REVOKE ALL ON FUNCTION openfox_private.notify_task_draft(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notify_task_draft(uuid) TO openfox_app;
-- The control-plane dispatch scan must also find an otherwise idle organization.
CREATE POLICY dispatch_pending ON openfox.task_drafts FOR SELECT TO openfox_migrator USING (finished_at IS NULL);
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
        UNION
        SELECT d.organization_id FROM openfox.task_drafts d WHERE d.finished_at IS NULL
    )
    SELECT w.organization_id
      FROM work w
     ORDER BY (p_after IS NOT NULL AND w.organization_id <= p_after), w.organization_id
     LIMIT p_limit;
END
$$;


RESET ROLE;
