-- Immutable derived locations; the source comment snapshot is never rewritten.
SET ROLE openfox_migrator;
CREATE TABLE openfox.workspace_anchor_resolutions (
 organization_id uuid NOT NULL,
 id uuid NOT NULL,
 thread_id uuid NOT NULL,
 thread_version bigint NOT NULL,
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=131072),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,thread_id,thread_version)
   REFERENCES openfox.workspace_comment_revisions(organization_id,thread_id,version)
);
CREATE INDEX workspace_anchor_thread ON openfox.workspace_anchor_resolutions(organization_id,thread_id,created_at DESC,id);
ALTER TABLE openfox.workspace_anchor_resolutions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.workspace_anchor_resolutions FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.workspace_anchor_resolutions
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT ON openfox.workspace_anchor_resolutions TO openfox_app;
RESET ROLE;
