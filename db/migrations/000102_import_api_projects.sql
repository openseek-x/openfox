SET ROLE openfox_migrator;
CREATE TABLE openfox.import_project_mappings (
 organization_id uuid NOT NULL, space_id uuid NOT NULL, source_system text NOT NULL,
 source_instance text NOT NULL, external_id text NOT NULL, project_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),created_job_id uuid NOT NULL,released_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,project_id) REFERENCES openfox.projects(organization_id,id),
 FOREIGN KEY(organization_id,created_job_id) REFERENCES openfox.import_jobs(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id)
);
CREATE UNIQUE INDEX import_project_live_source ON openfox.import_project_mappings(organization_id,space_id,source_system,source_instance,external_id) WHERE released_at IS NULL;
CREATE INDEX import_project_job ON openfox.import_project_mappings(organization_id,created_job_id);
CREATE TABLE openfox.import_sync_projects (
 organization_id uuid NOT NULL,job_id uuid NOT NULL,external_id text NOT NULL,project_id uuid NOT NULL,
 source_name text NOT NULL,source_description text NOT NULL,
 PRIMARY KEY(organization_id,job_id,external_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_sync_links(organization_id,job_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES openfox.projects(organization_id,id)
);
ALTER TABLE openfox.import_project_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_project_mappings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY import_project_map_org ON openfox.import_project_mappings USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY import_sync_project_org ON openfox.import_sync_projects USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.import_project_mappings,openfox.import_sync_projects TO openfox_app;
RESET ROLE;
