-- T02: record known completion times without inventing historical/import dates.
-- Native task creation supplies clock_timestamp(); imports supply only a known
-- source time. Existing rows stay NULL. Retain this column when rolling back.
SET ROLE openfox_migrator;

ALTER TABLE openfox.tasks ADD COLUMN completed_at timestamptz;
ALTER TABLE openfox.tasks ADD CONSTRAINT tasks_completed_at_status CHECK (
    completed_at IS NULL OR status = 'DONE'
);

CREATE OR REPLACE FUNCTION openfox_private.sync_task_status_projection()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    category_value text;
    importing boolean := coalesce(current_setting('openfox.import_job', true), '') <> '';
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

    IF category_value <> 'DONE' THEN
        NEW.completed_at := NULL;
    ELSIF TG_OP = 'UPDATE' AND NOT importing THEN
        -- OLD.status is the category before a status definition was reclassified.
        IF OLD.status <> 'DONE' THEN
            NEW.completed_at := clock_timestamp();
        ELSE
            NEW.completed_at := OLD.completed_at;
        END IF;
    END IF;
    -- INSERT and existing historical-import transactions retain only the time
    -- explicitly supplied by their trusted application path, including NULL.
    NEW.status := category_value;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END
$$;
ALTER FUNCTION openfox_private.sync_task_status_projection() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.sync_task_status_projection() FROM PUBLIC;

RESET ROLE;
