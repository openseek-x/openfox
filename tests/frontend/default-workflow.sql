-- The desktop fixture creates its organization after the onboarding migration.
-- Complete its one example workflow without replacing any configured status.
DO $$
DECLARE
    org_id CONSTANT uuid := '00000000-0000-0000-0000-0000000000a1';
    space_id_qa CONSTANT uuid := '00000000-0000-0000-0000-0000000000aa';
    type_id CONSTANT uuid := '00000000-0000-0000-0000-000000000111';
    seed record;
    chosen uuid;
    next_position integer;
    changed boolean := false;
BEGIN
    PERFORM 1 FROM openfox.task_types
    WHERE organization_id = org_id AND space_id = space_id_qa AND id = type_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Expected desktop fixture task type is missing';
    END IF;
    SELECT COALESCE(max(position), -1) + 1 INTO next_position
    FROM openfox.workflow_statuses WHERE organization_id = org_id AND task_type_id = type_id;

    FOR seed IN SELECT * FROM (VALUES
        ('TRIAGE', '待分拣'), ('BACKLOG', '待规划'), ('TODO', '待开始'),
        ('IN_PROGRESS', '进行中'), ('DONE', '已完成'), ('CANCELLED', '已取消'),
        ('DUPLICATE', '存在重复')
    ) AS defaults(category, name) LOOP
        -- Prefer an already bound status of the category, then an existing organization status.
        SELECT s.id INTO chosen
        FROM openfox.task_statuses s
        LEFT JOIN openfox.workflow_statuses w ON w.organization_id = s.organization_id
            AND w.status_id = s.id AND w.task_type_id = type_id
        WHERE s.organization_id = org_id AND s.category = seed.category AND s.archived_at IS NULL
        ORDER BY (w.status_id IS NULL), s.position, s.id LIMIT 1;
        IF chosen IS NULL THEN
            INSERT INTO openfox.task_statuses (organization_id, name, category, position)
            VALUES (org_id, seed.name, seed.category, 0) RETURNING id INTO chosen;
        END IF;
        INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position)
        VALUES (org_id, space_id_qa, type_id, chosen, next_position) ON CONFLICT DO NOTHING;
        IF FOUND THEN
            next_position := next_position + 1;
            changed := true;
        END IF;
    END LOOP;
    IF changed THEN
        UPDATE openfox.task_types SET version = version + 1
        WHERE organization_id = org_id AND id = type_id;
    END IF;
END $$;
