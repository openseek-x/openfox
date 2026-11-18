-- Individually report CSV Epic rows, including projects without any tasks.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_items
 ADD COLUMN project_id uuid,
 ADD FOREIGN KEY (organization_id,project_id) REFERENCES openfox.projects(organization_id,id),
 DROP CONSTRAINT import_items_task_valid,
 ADD CONSTRAINT import_items_task_valid CHECK (
   (project_id IS NULL OR task_id IS NULL) AND
   (status NOT IN ('CREATED','UPDATED') OR project_id IS NOT NULL OR (task_id IS NOT NULL AND task_version IS NOT NULL)));
CREATE INDEX import_items_project_source ON openfox.import_items(organization_id,external_id,job_id) WHERE project_id IS NOT NULL;
RESET ROLE;
