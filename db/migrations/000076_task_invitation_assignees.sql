-- Reserve task assignments for pending organization invitations without exposing
-- them to the legacy assignee table, its exports or its replace-all writes.
SET ROLE openfox_migrator;

CREATE UNIQUE INDEX invitations_organization_id_unique ON openfox.invitations (organization_id, id);
CREATE TABLE openfox.task_invitation_assignees (
    organization_id uuid NOT NULL,
    task_id uuid NOT NULL,
    invitation_id uuid NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, task_id, invitation_id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, invitation_id)
        REFERENCES openfox.invitations (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id)
);
CREATE INDEX task_invitation_assignees_invitation_idx
    ON openfox.task_invitation_assignees (organization_id, invitation_id, task_id);
ALTER TABLE openfox.task_invitation_assignees ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_invitation_assignees FORCE ROW LEVEL SECURITY;
CREATE POLICY task_invitation_assignees_admin ON openfox.task_invitation_assignees TO openfox_app
    USING (organization_id = openfox_private.current_organization_id() AND EXISTS (
        SELECT 1 FROM openfox.organization_members m
          JOIN openfox.tasks t ON t.organization_id=m.organization_id AND t.id=task_invitation_assignees.task_id
          JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id AND s.deleted_at IS NULL
          LEFT JOIN openfox.space_members sm ON sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id
         WHERE m.organization_id = openfox_private.current_organization_id()
           AND m.user_id = openfox_private.current_actor_id()
           AND m.status = 'ACTIVE' AND m.role IN ('OWNER','ADMIN')
           AND (sm.status='ACTIVE' OR (sm.user_id IS NULL AND s.visibility='SHARED'))
    ));
CREATE POLICY task_invitation_assignees_lifecycle ON openfox.task_invitation_assignees TO openfox_migrator
    USING (organization_id = openfox_private.current_organization_id());
GRANT SELECT, INSERT, DELETE ON openfox.task_invitation_assignees TO openfox_app;

-- The result reveals only whether this visible replacement fits. The API checks
-- task write permission and holds the task lock before calling it.
CREATE FUNCTION openfox_private.task_assignees_fit(p_task uuid, p_visible integer)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT p_visible BETWEEN 0 AND 10 AND p_visible + count(*) <= 10
      FROM openfox.task_invitation_assignees a
      JOIN openfox.invitations i ON i.organization_id = a.organization_id AND i.id = a.invitation_id
      JOIN openfox.tasks t ON t.organization_id=a.organization_id AND t.id=a.task_id
      JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id AND s.deleted_at IS NULL
      JOIN openfox.organization_members m ON m.organization_id=a.organization_id AND m.user_id=openfox_private.current_actor_id() AND m.status='ACTIVE'
      LEFT JOIN openfox.space_members sm ON sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id
     WHERE (sm.status='ACTIVE' OR (sm.user_id IS NULL AND s.visibility='SHARED' AND m.role<>'GUEST'))
       AND a.organization_id = openfox_private.current_organization_id() AND a.task_id = p_task
       AND i.status = 'PENDING' AND i.expires_at > statement_timestamp()
$$;
REVOKE ALL ON FUNCTION openfox_private.task_assignees_fit(uuid, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.task_assignees_fit(uuid, integer) TO openfox_app;

-- Import, account linking, and integration sync can still insert the original
-- assignee rows directly. Serialize with task edits, then check the reserved
-- capacity against rows that were actually inserted (including ON CONFLICT).
CREATE FUNCTION openfox_private.check_reserved_task_capacity() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
BEGIN
    PERFORM t.id FROM openfox.tasks t
     WHERE t.organization_id=openfox_private.current_organization_id()
       AND t.id IN (SELECT n.task_id FROM new_assignees n)
     ORDER BY t.id FOR NO KEY UPDATE OF t;
    IF EXISTS (
        SELECT 1 FROM (SELECT DISTINCT task_id FROM new_assignees) n
        JOIN openfox.task_invitation_assignees p ON p.organization_id=openfox_private.current_organization_id() AND p.task_id=n.task_id
        JOIN openfox.invitations i ON i.organization_id=p.organization_id AND i.id=p.invitation_id
         AND i.status='PENDING' AND i.expires_at>statement_timestamp()
        GROUP BY p.organization_id,p.task_id
        HAVING count(*) + (SELECT count(*) FROM openfox.task_assignees a
                            WHERE a.organization_id=p.organization_id AND a.task_id=p.task_id) > 10
    ) THEN
        RAISE EXCEPTION 'too_many_assignees' USING ERRCODE='23514',CONSTRAINT='task_assignees_reserved_capacity';
    END IF;
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION openfox_private.check_reserved_task_capacity() FROM PUBLIC;
CREATE TRIGGER task_assignees_reserved_capacity AFTER INSERT ON openfox.task_assignees
REFERENCING NEW TABLE AS new_assignees FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.check_reserved_task_capacity();

RESET ROLE;
