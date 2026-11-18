-- Only future local comments are eligible. Historical comments are not replayed.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_sync_links ADD COLUMN comments_outbound_after timestamptz NOT NULL DEFAULT clock_timestamp();
CREATE TABLE openfox.import_comment_deliveries (
 organization_id uuid NOT NULL, job_id uuid NOT NULL, comment_id uuid NOT NULL,
 external_id text NOT NULL, body text NOT NULL, author_user_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN('SENDING','SENT')),
 remote_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,job_id,comment_id),
 FOREIGN KEY(organization_id,job_id,external_id) REFERENCES openfox.import_sync_items(organization_id,job_id,external_id),
 FOREIGN KEY(organization_id,comment_id) REFERENCES openfox.task_comments(organization_id,id),
 FOREIGN KEY(organization_id,author_user_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
ALTER TABLE openfox.import_comment_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_comment_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY import_comment_deliveries_org ON openfox.import_comment_deliveries
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.import_comment_deliveries TO openfox_app;
RESET ROLE;
