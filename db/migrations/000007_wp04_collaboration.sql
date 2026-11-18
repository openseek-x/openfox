-- WP-04 collaboration foundation.
-- This migration extends the WP-01 task/space rows without rewriting the
-- previous migration. Existing rows receive deterministic defaults and keep
-- the legacy status column as a compatibility projection.

SET ROLE openfox_migrator;

ALTER TABLE openfox.spaces
    ADD COLUMN prefix text,
    ADD COLUMN next_task_number bigint NOT NULL DEFAULT 1,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();

WITH numbered AS (
    SELECT organization_id, id,
           row_number() OVER (PARTITION BY organization_id ORDER BY id) AS ordinal
    FROM openfox.spaces
)
UPDATE openfox.spaces AS s
   SET prefix = 'sp_' || lpad(numbered.ordinal::text, 4, '0')
  FROM numbered
 WHERE s.organization_id = numbered.organization_id
   AND s.id = numbered.id
   AND (s.prefix IS NULL OR btrim(s.prefix) = '');

ALTER TABLE openfox.spaces
    ALTER COLUMN prefix SET NOT NULL,
    ADD CONSTRAINT spaces_prefix_valid CHECK (prefix ~ '^[a-z][a-z0-9_-]{1,31}$'),
    ADD CONSTRAINT spaces_next_task_number_positive CHECK (next_task_number > 0),
    ADD CONSTRAINT spaces_version_positive CHECK (version > 0);

ALTER TABLE openfox.spaces
    ALTER COLUMN prefix SET DEFAULT ('sp_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 12));

CREATE UNIQUE INDEX spaces_organization_prefix_unique
    ON openfox.spaces (organization_id, lower(prefix));

ALTER TABLE openfox.space_members
    ADD COLUMN status text NOT NULL DEFAULT 'ACTIVE',
    ADD CONSTRAINT space_members_status_valid CHECK (status IN ('ACTIVE', 'REVOKED'));

CREATE TABLE openfox.task_statuses (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    category text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    color text,
    icon text,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    CONSTRAINT task_statuses_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT task_statuses_description_length CHECK (length(description) <= 1000),
    CONSTRAINT task_statuses_category_valid CHECK (
        category IN ('TRIAGE', 'BACKLOG', 'TODO', 'IN_PROGRESS', 'DONE', 'CANCELLED', 'DUPLICATE')
    ),
    CONSTRAINT task_statuses_position_nonnegative CHECK (position >= 0),
    CONSTRAINT task_statuses_color_valid CHECK (color IS NULL OR length(btrim(color)) BETWEEN 1 AND 64),
    CONSTRAINT task_statuses_icon_valid CHECK (icon IS NULL OR length(btrim(icon)) BETWEEN 1 AND 64),
    CONSTRAINT task_statuses_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX task_statuses_name_unique
    ON openfox.task_statuses (organization_id, lower(name));

CREATE TABLE openfox.task_types (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    icon text,
    enabled boolean NOT NULL DEFAULT true,
    is_default boolean NOT NULL DEFAULT false,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    CONSTRAINT task_types_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT task_types_description_length CHECK (length(description) <= 1000),
    CONSTRAINT task_types_icon_valid CHECK (icon IS NULL OR length(btrim(icon)) BETWEEN 1 AND 64),
    CONSTRAINT task_types_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX task_types_space_name_unique
    ON openfox.task_types (organization_id, space_id, lower(name));
CREATE UNIQUE INDEX task_types_space_default_unique
    ON openfox.task_types (organization_id, space_id)
    WHERE is_default AND archived_at IS NULL;
ALTER TABLE openfox.task_types
    ADD CONSTRAINT task_types_space_identity_unique UNIQUE (organization_id, space_id, id);

CREATE TABLE openfox.workflow_statuses (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_type_id uuid NOT NULL,
    status_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0,
    is_default boolean NOT NULL DEFAULT false,
    PRIMARY KEY (organization_id, task_type_id, status_id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, task_type_id)
        REFERENCES openfox.task_types (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_type_id)
        REFERENCES openfox.task_types (organization_id, space_id, id),
    FOREIGN KEY (organization_id, status_id)
        REFERENCES openfox.task_statuses (organization_id, id),
    CONSTRAINT workflow_statuses_position_nonnegative CHECK (position >= 0)
);

CREATE UNIQUE INDEX workflow_statuses_default_unique
    ON openfox.workflow_statuses (organization_id, task_type_id)
    WHERE is_default;

CREATE OR REPLACE FUNCTION openfox_private.validate_workflow_status()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    status_category text;
    status_archived timestamptz;
BEGIN
    SELECT category, archived_at INTO status_category, status_archived
    FROM openfox.task_statuses
    WHERE organization_id = NEW.organization_id AND id = NEW.status_id;
    IF status_category IS NULL OR status_archived IS NOT NULL THEN
        RAISE EXCEPTION 'workflow status is not active in organization' USING ERRCODE = '23514';
    END IF;
    IF NEW.is_default AND status_category IN ('DONE', 'CANCELLED', 'DUPLICATE') THEN
        RAISE EXCEPTION 'workflow default must be non-terminal' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_workflow_status() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_workflow_status() FROM PUBLIC;

CREATE TRIGGER workflow_status_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.workflow_statuses
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_workflow_status();

CREATE TABLE openfox.labels (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    CONSTRAINT labels_name_not_blank CHECK (length(btrim(name)) > 0)
);

CREATE UNIQUE INDEX labels_space_name_unique
    ON openfox.labels (organization_id, space_id, lower(name));

CREATE TABLE openfox.task_labels (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    label_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, task_id, label_id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, label_id)
        REFERENCES openfox.labels (organization_id, id)
);

ALTER TABLE openfox.tasks
    ADD COLUMN sequence bigint,
    ADD COLUMN type_id uuid,
    ADD COLUMN status_id uuid,
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN acceptance text NOT NULL DEFAULT '',
    ADD COLUMN priority text NOT NULL DEFAULT 'NORMAL',
    ADD COLUMN executor_agent_id uuid,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();

INSERT INTO openfox.task_statuses (organization_id, name, description, category, position)
SELECT o.id, statuses.name, '', statuses.category, statuses.position
FROM openfox.organizations o
CROSS JOIN (
    VALUES
        ('Triage', 'TRIAGE', 0),
        ('Backlog', 'BACKLOG', 1),
        ('Todo', 'TODO', 2),
        ('In Progress', 'IN_PROGRESS', 3),
        ('Done', 'DONE', 4),
        ('Cancelled', 'CANCELLED', 5),
        ('Duplicate', 'DUPLICATE', 6)
) AS statuses(name, category, position)
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.task_statuses existing
    WHERE existing.organization_id = o.id
      AND existing.category = statuses.category
);

INSERT INTO openfox.task_types (organization_id, space_id, name, is_default)
SELECT s.organization_id, s.id, 'Task', true
FROM openfox.spaces s
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.task_types existing
    WHERE existing.organization_id = s.organization_id
      AND existing.space_id = s.id
      AND lower(existing.name) = 'task'
);

INSERT INTO openfox.workflow_statuses (
    organization_id, space_id, task_type_id, status_id, position, is_default
)
SELECT tt.organization_id, tt.space_id, tt.id, ts.id, ts.position,
       ts.category = 'TODO'
FROM openfox.task_types tt
JOIN openfox.task_statuses ts ON ts.organization_id = tt.organization_id
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.workflow_statuses existing
    WHERE existing.organization_id = tt.organization_id
      AND existing.task_type_id = tt.id
      AND existing.status_id = ts.id
);

WITH numbered AS (
    SELECT organization_id, space_id, id,
           row_number() OVER (PARTITION BY organization_id, space_id ORDER BY created_at, id) AS ordinal
    FROM openfox.tasks
)
UPDATE openfox.tasks AS t
   SET sequence = numbered.ordinal
  FROM numbered
 WHERE t.organization_id = numbered.organization_id
   AND t.space_id = numbered.space_id
   AND t.id = numbered.id
   AND t.sequence IS NULL;

UPDATE openfox.spaces AS s
   SET next_task_number = GREATEST(
       1,
       COALESCE((
           SELECT max(t.sequence) + 1
           FROM openfox.tasks t
           WHERE t.organization_id = s.organization_id
             AND t.space_id = s.id
       ), 1)
   )
 WHERE s.next_task_number = 1;

UPDATE openfox.tasks AS t
   SET type_id = defaults.id
  FROM openfox.task_types defaults
 WHERE defaults.organization_id = t.organization_id
   AND defaults.space_id = t.space_id
   AND lower(defaults.name) = 'task'
   AND t.type_id IS NULL;

UPDATE openfox.tasks AS t
   SET status_id = statuses.id
  FROM openfox.task_statuses statuses
 WHERE statuses.organization_id = t.organization_id
   AND statuses.category = CASE upper(t.status)
       WHEN 'CANCELED' THEN 'CANCELLED'
       WHEN 'CANCELLED' THEN 'CANCELLED'
       WHEN 'IN_PROGRESS' THEN 'IN_PROGRESS'
       WHEN 'DONE' THEN 'DONE'
       WHEN 'BACKLOG' THEN 'BACKLOG'
       WHEN 'TRIAGE' THEN 'TRIAGE'
       WHEN 'DUPLICATE' THEN 'DUPLICATE'
       ELSE 'TODO'
   END
   AND t.status_id IS NULL;

ALTER TABLE openfox.tasks
    DROP CONSTRAINT tasks_status_valid,
    ADD CONSTRAINT tasks_status_valid CHECK (
        status IN ('TRIAGE', 'BACKLOG', 'TODO', 'IN_PROGRESS', 'DONE', 'CANCELLED', 'CANCELED', 'DUPLICATE')
    ),
    ALTER COLUMN sequence SET NOT NULL,
    ALTER COLUMN type_id SET NOT NULL,
    ALTER COLUMN status_id SET NOT NULL,
    ADD CONSTRAINT tasks_sequence_positive CHECK (sequence > 0),
    ADD CONSTRAINT tasks_priority_valid CHECK (priority IN ('LOW', 'NORMAL', 'HIGH', 'URGENT')),
    ADD CONSTRAINT tasks_version_positive CHECK (version > 0),
    ADD CONSTRAINT tasks_type_same_organization FOREIGN KEY (organization_id, type_id)
        REFERENCES openfox.task_types (organization_id, id),
    ADD CONSTRAINT tasks_type_same_space FOREIGN KEY (organization_id, space_id, type_id)
        REFERENCES openfox.task_types (organization_id, space_id, id),
    ADD CONSTRAINT tasks_status_same_organization FOREIGN KEY (organization_id, status_id)
        REFERENCES openfox.task_statuses (organization_id, id),
    ADD CONSTRAINT tasks_executor_same_organization FOREIGN KEY (organization_id, executor_agent_id)
        REFERENCES openfox.agents (organization_id, id);

CREATE UNIQUE INDEX tasks_space_sequence_unique
    ON openfox.tasks (organization_id, space_id, sequence);
CREATE INDEX tasks_space_status_idx
    ON openfox.tasks (organization_id, space_id, status_id, created_at, id);
CREATE INDEX tasks_space_priority_idx
    ON openfox.tasks (organization_id, space_id, priority, created_at, id);

CREATE OR REPLACE FUNCTION openfox_private.sync_task_status_projection()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    category_value text;
BEGIN
    IF NEW.status_id IS NULL THEN
        RAISE EXCEPTION 'task status_id is required' USING ERRCODE = '23514';
    END IF;

    SELECT category INTO category_value
    FROM openfox.task_statuses
    WHERE organization_id = NEW.organization_id AND id = NEW.status_id;
    IF category_value IS NULL THEN
        RAISE EXCEPTION 'task status does not belong to organization' USING ERRCODE = '23503';
    END IF;
    NEW.status := category_value;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.sync_task_status_projection() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.sync_task_status_projection() FROM PUBLIC;

CREATE TRIGGER tasks_status_projection_trigger
BEFORE INSERT OR UPDATE OF status, status_id ON openfox.tasks
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_task_status_projection();

CREATE TABLE openfox.task_activities (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    actor_id uuid NOT NULL,
    kind text NOT NULL,
    safe_diff jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    CONSTRAINT task_activities_kind_valid CHECK (kind ~ '^[a-z][a-z0-9_.-]{1,63}$'),
    CONSTRAINT task_activities_source_not_blank CHECK (length(btrim(source_id)) > 0),
    CONSTRAINT task_activities_safe_diff_object CHECK (jsonb_typeof(safe_diff) = 'object')
);

CREATE UNIQUE INDEX task_activities_source_unique
    ON openfox.task_activities (organization_id, source_id);
CREATE INDEX task_activities_task_idx
    ON openfox.task_activities (organization_id, task_id, created_at, id);

ALTER TABLE openfox.task_statuses ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_statuses FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_types ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_types FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.workflow_statuses ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.workflow_statuses FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.labels FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_labels FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_activities ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_activities FORCE ROW LEVEL SECURITY;

CREATE POLICY task_statuses_tenant_isolation ON openfox.task_statuses
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_types_tenant_isolation ON openfox.task_types
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY workflow_statuses_tenant_isolation ON openfox.workflow_statuses
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY labels_tenant_isolation ON openfox.labels
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_labels_tenant_isolation ON openfox.task_labels
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_activities_tenant_isolation ON openfox.task_activities
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON
    openfox.task_statuses,
    openfox.task_types,
    openfox.workflow_statuses,
    openfox.labels,
    openfox.task_labels,
    openfox.task_activities
TO openfox_app;

RESET ROLE;
