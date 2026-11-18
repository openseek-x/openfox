-- S04: preserve the historical created_at/id order within each tenant/space.
-- NULL remains accepted for older writers and sorts after explicit positions.
SET ROLE openfox_migrator;
ALTER TABLE openfox.task_types ADD COLUMN position bigint,
    ADD CONSTRAINT task_types_position_nonnegative CHECK (position IS NULL OR position >= 0);
CREATE POLICY task_types_order_migration_read ON openfox.task_types
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY task_types_order_migration_update ON openfox.task_types
    FOR UPDATE TO openfox_migrator USING (true) WITH CHECK (true);
WITH ordered AS (
    SELECT organization_id, id,
           row_number() OVER (PARTITION BY organization_id, space_id ORDER BY created_at, id) - 1 AS position
      FROM openfox.task_types
)
UPDATE openfox.task_types t SET position = ordered.position FROM ordered
 WHERE t.organization_id = ordered.organization_id AND t.id = ordered.id;
DROP POLICY task_types_order_migration_read ON openfox.task_types;
DROP POLICY task_types_order_migration_update ON openfox.task_types;
CREATE INDEX task_types_space_order ON openfox.task_types (organization_id, space_id, position, created_at, id);
RESET ROLE;
