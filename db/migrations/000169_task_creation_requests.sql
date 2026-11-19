-- A user's submitted brief grants exactly one task creation to its own run.
SET ROLE openfox_migrator;
CREATE TABLE openfox.task_creation_requests (
    organization_id uuid NOT NULL,
    message_id uuid NOT NULL,
    options jsonb NOT NULL CHECK (jsonb_typeof(options) = 'object'),
    task_id uuid,
    display_id text,
    PRIMARY KEY (organization_id, message_id),
    FOREIGN KEY (organization_id, message_id) REFERENCES openfox.messages(organization_id, id) ON DELETE CASCADE,
    CHECK ((task_id IS NULL) = (display_id IS NULL))
);
ALTER TABLE openfox.task_creation_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_creation_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.task_creation_requests
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
GRANT SELECT, INSERT, UPDATE(task_id, display_id) ON openfox.task_creation_requests TO openfox_app;
-- Drafting results are independent of the normally muted background-run notices.
INSERT INTO openfox.inbox_policy_keys(key,group_key,default_level,position)
VALUES ('task_creation_result_requester','agent_activity','normal',67);

CREATE FUNCTION openfox_private.notify_task_creation(p_message uuid) RETURNS void
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,inbox_event,
        conversation_id,message_id,task_id,dedupe_key,safe_summary)
    SELECT q.organization_id,m.author_user_id,
        CASE WHEN q.task_id IS NULL THEN 'RUN_FAILED' ELSE 'RUN_COMPLETED' END,
        'task_creation_result_requester',
        CASE WHEN q.task_id IS NULL THEN m.conversation_id END,
        CASE WHEN q.task_id IS NULL THEN m.id END,
        q.task_id,'task-create:' || m.id::text || CASE WHEN q.task_id IS NULL THEN ':failed' ELSE ':created' END,
        CASE WHEN q.task_id IS NULL THEN '任务创建失败，打开通知可用原始描述手动创建。' ELSE '已创建任务 ' || q.display_id END
    FROM openfox.task_creation_requests q
    JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id
    JOIN openfox.organization_members u ON u.organization_id=m.organization_id AND u.user_id=m.author_user_id AND u.status='ACTIVE'
    WHERE q.organization_id=openfox_private.current_organization_id() AND q.message_id=p_message
      AND (q.task_id IS NOT NULL OR EXISTS (
        SELECT 1 FROM openfox.conversation_inputs i WHERE i.organization_id=q.organization_id AND i.message_id=q.message_id AND i.status IN ('REJECTED','CONSUMED')))
    ON CONFLICT (organization_id,recipient_user_id,dedupe_key) DO NOTHING
$$;
REVOKE ALL ON FUNCTION openfox_private.notify_task_creation(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notify_task_creation(uuid) TO openfox_app;
RESET ROLE;
