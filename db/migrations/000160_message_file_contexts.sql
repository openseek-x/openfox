-- File feedback is input to ordinary chat turns. There are no document versions
-- or candidate/acceptance states; result fields only make tool retries idempotent.
SET ROLE openfox_migrator;
ALTER TABLE openfox.messages ADD COLUMN file_context_hash text;
CREATE TABLE openfox.message_file_contexts (
 organization_id uuid NOT NULL,
 id uuid NOT NULL,
 message_id uuid NOT NULL,
 source_artifact_id uuid,
 input_snapshot jsonb NOT NULL CHECK(octet_length(input_snapshot::text)<=262144),
 payload_hash text NOT NULL CHECK(length(payload_hash)=64),
 result_hash text CHECK(length(result_hash)=64),
 result_artifact_id uuid,
 result_sha256 text CHECK(length(result_sha256)=64),
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id),
 FOREIGN KEY(organization_id,source_artifact_id) REFERENCES openfox.artifacts(organization_id,id),
 FOREIGN KEY(organization_id,result_artifact_id) REFERENCES openfox.artifacts(organization_id,id)
);
CREATE INDEX message_file_context_message ON openfox.message_file_contexts(organization_id,message_id);
CREATE UNIQUE INDEX message_file_context_source_result ON openfox.message_file_contexts(organization_id,source_artifact_id) WHERE result_artifact_id IS NOT NULL;
CREATE UNIQUE INDEX message_file_context_result ON openfox.message_file_contexts(organization_id,result_artifact_id) WHERE result_artifact_id IS NOT NULL;
ALTER TABLE openfox.message_file_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.message_file_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.message_file_contexts
 USING (organization_id=openfox_private.current_organization_id())
 WITH CHECK (organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.message_file_contexts TO openfox_app;
RESET ROLE;
