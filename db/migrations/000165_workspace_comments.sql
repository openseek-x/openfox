-- Ordinary discussion is independent of chat inputs and Agent runs.
SET ROLE openfox_migrator;
CREATE TABLE openfox.workspace_comment_threads (
 organization_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 id uuid NOT NULL,
 version bigint NOT NULL CHECK(version > 0),
 snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=131072),
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id)
);
CREATE INDEX workspace_comments_conversation ON openfox.workspace_comment_threads(organization_id,conversation_id,id);
CREATE TABLE openfox.workspace_comment_revisions (
 organization_id uuid NOT NULL,
 thread_id uuid NOT NULL,
 version bigint NOT NULL,
 operation_id uuid NOT NULL,
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=131072),
 PRIMARY KEY(organization_id,thread_id,version),
 UNIQUE(organization_id,operation_id),
 FOREIGN KEY(organization_id,thread_id) REFERENCES openfox.workspace_comment_threads(organization_id,id)
);
ALTER TABLE openfox.workspace_comment_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.workspace_comment_threads FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.workspace_comment_threads
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
ALTER TABLE openfox.workspace_comment_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.workspace_comment_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.workspace_comment_revisions
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.workspace_comment_threads TO openfox_app;
GRANT SELECT,INSERT ON openfox.workspace_comment_revisions TO openfox_app;
-- A saved screenshot belongs to its discussion, not a pending chat message.
ALTER TABLE openfox.attachments ADD COLUMN comment_thread_id uuid,
 ADD CONSTRAINT attachment_comment_thread_fk FOREIGN KEY(organization_id,comment_thread_id) REFERENCES openfox.workspace_comment_threads(organization_id,id),
 ADD CONSTRAINT attachment_comment_owner CHECK(comment_thread_id IS NULL OR (owner_kind='CONVERSATION' AND message_id IS NULL));
CREATE INDEX attachment_comment_thread ON openfox.attachments(organization_id,comment_thread_id) WHERE comment_thread_id IS NOT NULL;
GRANT UPDATE(comment_thread_id) ON openfox.attachments TO openfox_app;
RESET ROLE;
