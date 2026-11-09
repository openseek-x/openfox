-- Apply after 000076 in one migration transaction. Existing pending invitations need no backfill.
SET ROLE openfox_migrator;

-- Accept, revoke, and lazy expiry all update invitations. A statement trigger covers batch expiry
-- and locks the affected tasks in the same order as task writes. It can only consume the
-- reservations of this tenant and of the invitations that actually changed in the statement.
CREATE FUNCTION openfox_private.settle_task_invitation_assignees() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    tenant uuid := openfox_private.current_organization_id();
    target uuid := openfox_private.current_actor_id();
    finished uuid[];
BEGIN
    SELECT array_agg(n.id) INTO finished
      FROM new_invitations n JOIN old_invitations o USING (organization_id, id)
     WHERE n.organization_id = tenant AND o.status = 'PENDING'
       AND n.status IN ('ACCEPTED', 'REVOKED', 'EXPIRED');
    IF finished IS NULL THEN RETURN NULL; END IF;

    PERFORM t.id FROM openfox.tasks t
     WHERE t.organization_id = tenant AND EXISTS (
        SELECT 1 FROM openfox.task_invitation_assignees p WHERE p.organization_id = tenant
         AND p.task_id = t.id AND p.invitation_id = ANY(finished))
     ORDER BY t.id FOR UPDATE OF t;

    WITH removed AS (
        DELETE FROM openfox.task_invitation_assignees p
         WHERE p.organization_id = tenant AND p.invitation_id = ANY(finished)
        RETURNING p.task_id, p.invitation_id, p.created_by
    ), additions AS (
        SELECT t.id AS task_id, t.space_id, t.created_at, t.assignee_id, t.executor_agent_id, r.created_by,
               NOT EXISTS (SELECT 1 FROM openfox.task_assignees a WHERE a.organization_id=tenant AND a.task_id=t.id) AS legacy
          FROM removed r JOIN new_invitations i ON i.id = r.invitation_id AND i.organization_id=tenant
          JOIN openfox.users u ON u.id=target AND u.normalized_email=i.normalized_email
          JOIN openfox.organization_members m ON m.organization_id=tenant AND m.user_id=target AND m.status='ACTIVE'
          JOIN openfox.tasks t ON t.organization_id=tenant AND t.id=r.task_id AND t.deleted_at IS NULL
          JOIN openfox.spaces s ON s.organization_id=tenant AND s.id=t.space_id AND s.status='ACTIVE' AND s.deleted_at IS NULL
          LEFT JOIN openfox.space_members sm ON sm.organization_id=tenant AND sm.space_id=s.id AND sm.user_id=target
         WHERE i.status='ACCEPTED'
           AND (sm.status='ACTIVE' OR (sm.user_id IS NULL AND s.visibility='SHARED' AND m.role<>'GUEST'))
           AND t.assignee_id IS DISTINCT FROM target
           AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees a WHERE a.organization_id=tenant AND a.task_id=t.id AND a.user_id=target)
    ), inserted_assignees AS (
        -- Older tasks store their first human/agent only on tasks. Preserve both when switching
        -- to the normalized list, which takes precedence over those projections on reads.
        INSERT INTO openfox.task_assignees (organization_id,space_id,task_id,kind,user_id,agent_id,created_by,created_at)
        SELECT tenant, a.space_id, a.task_id, v.kind, v.user_id, v.agent_id, a.created_by, v.at
          FROM additions a CROSS JOIN LATERAL (VALUES
              ('HUMAN', target, NULL::uuid, clock_timestamp()),
              ('HUMAN', CASE WHEN a.legacy THEN a.assignee_id END, NULL::uuid, a.created_at),
              ('AGENT', NULL::uuid, CASE WHEN a.legacy THEN a.executor_agent_id END, a.created_at)
          ) v(kind,user_id,agent_id,at)
         WHERE v.user_id IS NOT NULL OR v.agent_id IS NOT NULL
        ON CONFLICT DO NOTHING
        RETURNING task_id, space_id, user_id
    ), followed AS (
        INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
        SELECT tenant, space_id, task_id, 'HUMAN', target, target
          FROM inserted_assignees WHERE user_id = target
        ON CONFLICT DO NOTHING
    ), activity AS (
        -- Ordinary task readers see only the accepted member, never invitation ids or emails.
        INSERT INTO openfox.task_activities (organization_id,space_id,task_id,actor_id,kind,safe_diff,source_id)
        SELECT tenant, space_id, task_id, target, 'task.assignee_added',
               jsonb_build_object('kind','HUMAN','id',target), gen_random_uuid()::text
          FROM inserted_assignees WHERE user_id = target
    )
    UPDATE openfox.tasks t
       SET assignee_id=COALESCE(t.assignee_id,
                   (SELECT a.user_id FROM inserted_assignees a WHERE a.task_id=t.id AND a.user_id=target)),
           version=t.version+1, updated_at=clock_timestamp()
     WHERE t.organization_id=tenant AND t.id IN (SELECT task_id FROM removed);
    RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION openfox_private.settle_task_invitation_assignees() FROM PUBLIC;
CREATE TRIGGER settle_task_invitation_assignees
AFTER UPDATE ON openfox.invitations
REFERENCING OLD TABLE AS old_invitations NEW TABLE AS new_invitations
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.settle_task_invitation_assignees();

RESET ROLE;
