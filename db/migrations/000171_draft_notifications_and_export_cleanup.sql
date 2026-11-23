SET ROLE openfox_migrator;

CREATE OR REPLACE FUNCTION openfox_private.notify_task_draft(p_id uuid) RETURNS void
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,inbox_event,task_id,task_draft_id,dedupe_key,safe_summary)
    SELECT q.organization_id,q.requester_id,CASE WHEN q.task_id IS NULL THEN 'RUN_FAILED' ELSE 'RUN_COMPLETED' END,
        'task_creation_result_requester',q.task_id,q.id,'task-draft:' || q.id::text,
        CASE WHEN q.task_id IS NULL THEN '任务创建失败，打开通知可用原始描述手动创建。' ELSE '已创建任务 ' || s.prefix || '-' || t.sequence::text END
    FROM openfox.task_drafts q
    JOIN openfox.organization_members m ON m.organization_id=q.organization_id AND m.user_id=q.requester_id AND m.status='ACTIVE'
    LEFT JOIN openfox.tasks t ON t.organization_id=q.organization_id AND t.id=q.task_id
    JOIN openfox.spaces s ON s.organization_id=q.organization_id AND s.id=COALESCE(t.space_id,q.space_id)
    WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=p_id AND q.finished_at IS NOT NULL
    ON CONFLICT(organization_id,recipient_user_id,dedupe_key) DO NOTHING
$$;

CREATE INDEX export_jobs_cleanup_pending ON openfox.export_jobs (organization_id, updated_at, id)
    WHERE status = 'EXPIRED' AND file_key <> '';

CREATE OR REPLACE FUNCTION openfox_private.list_export_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid export scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT j.organization_id FROM openfox.export_jobs j
         WHERE j.status IN ('QUEUED', 'RUNNING')
           AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= clock_timestamp())
           AND (j.retry_at IS NULL OR j.retry_at <= clock_timestamp())
        UNION
        SELECT j.organization_id FROM openfox.export_jobs j
         WHERE (j.status = 'SUCCEEDED' AND j.expires_at <= clock_timestamp())
            OR (j.status = 'EXPIRED' AND j.file_key <> '')
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;

RESET ROLE;
