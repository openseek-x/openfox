-- Stable source IDs prevent edited comments from being imported a second time.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_sync_links ADD COLUMN comments_cursor text NOT NULL DEFAULT '';
CREATE TABLE openfox.import_sync_comments (
 organization_id uuid NOT NULL, job_id uuid NOT NULL, external_id text NOT NULL,
 remote_id text NOT NULL CHECK(length(remote_id) BETWEEN 1 AND 255), comment_id uuid NOT NULL,
 source_body text NOT NULL, local_body text NOT NULL,
 PRIMARY KEY(organization_id,job_id,external_id,remote_id),
 UNIQUE(organization_id,job_id,comment_id),
 FOREIGN KEY(organization_id,job_id,external_id) REFERENCES openfox.import_sync_items(organization_id,job_id,external_id),
 FOREIGN KEY(organization_id,comment_id) REFERENCES openfox.task_comments(organization_id,id)
);
ALTER TABLE openfox.import_sync_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_comments FORCE ROW LEVEL SECURITY;
CREATE POLICY import_sync_comments_org ON openfox.import_sync_comments
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.import_sync_comments TO openfox_app;
RESET ROLE;
