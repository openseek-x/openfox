-- Validated candidates are not published artifact revisions until human acceptance.
SET ROLE openfox_migrator;
CREATE TABLE openfox.workspace_proposals (
 organization_id uuid NOT NULL,
 id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 run_id uuid NOT NULL,
 context_id uuid,
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 source_snapshot jsonb NOT NULL CHECK(octet_length(source_snapshot::text)<=262144),
 base_file jsonb NOT NULL,
 base_key text NOT NULL DEFAULT '',
 base_body text NOT NULL DEFAULT '' CHECK(octet_length(base_body)<=5242880),
 storage_key text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes BETWEEN 0 AND 10485760),
 sha256 text NOT NULL CHECK(length(sha256)=64),
 summary text NOT NULL CHECK(octet_length(summary)<=8192),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','ACCEPTED','REJECTED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_by uuid NOT NULL REFERENCES openfox.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 decision_id uuid,
 decided_by uuid REFERENCES openfox.users(id),
 decided_at timestamptz,
 result_artifact_id uuid,
 result_node_version bigint,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,run_id,context_id,request_hash),
 UNIQUE(organization_id,decision_id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY(organization_id,run_id) REFERENCES openfox.runs(organization_id,id),
 FOREIGN KEY(organization_id,context_id) REFERENCES openfox.message_file_contexts(organization_id,id),
 FOREIGN KEY(organization_id,result_artifact_id) REFERENCES openfox.artifacts(organization_id,id),
 CHECK((status='PENDING')=(decision_id IS NULL)),
 CHECK((decision_id IS NULL)=(decided_by IS NULL)),
 CHECK((decision_id IS NULL)=(decided_at IS NULL))
);
CREATE INDEX workspace_proposals_conversation ON openfox.workspace_proposals(organization_id,conversation_id,created_at DESC,id DESC);
CREATE INDEX workspace_proposals_storage ON openfox.workspace_proposals(organization_id,storage_key);
ALTER TABLE openfox.workspace_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.workspace_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.workspace_proposals
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT ON openfox.workspace_proposals TO openfox_app;
GRANT UPDATE(status,version,decision_id,decided_by,decided_at,result_artifact_id,result_node_version) ON openfox.workspace_proposals TO openfox_app;
RESET ROLE;
