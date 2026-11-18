-- User-submitted snapshots only; live refs never grant browser control.
-- API bounds compact input to 256 KiB; JSONB text adds structural whitespace.
SET ROLE openfox_migrator;
CREATE TABLE openfox.message_browser_contexts (
 organization_id uuid NOT NULL,
 message_id uuid NOT NULL,
 input_snapshot jsonb NOT NULL CHECK(jsonb_typeof(input_snapshot)='array' AND jsonb_array_length(input_snapshot)<=10 AND octet_length(input_snapshot::text)<=524288),
 payload_hash text NOT NULL CHECK(length(payload_hash)=64),
 PRIMARY KEY(organization_id,message_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id)
);
ALTER TABLE openfox.message_browser_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.message_browser_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.message_browser_contexts
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT ON openfox.message_browser_contexts TO openfox_app;
RESET ROLE;
