-- Browser metadata only. Page content, credentials and run grants stay local.
SET ROLE openfox_migrator;
CREATE TABLE openfox.browser_bindings (
 organization_id uuid NOT NULL,
 id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 owner_user_id uuid NOT NULL,
 device_id uuid NOT NULL,
 host_instance_id uuid NOT NULL,
 host_epoch bigint NOT NULL CHECK(host_epoch>0),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 status text NOT NULL CHECK(status IN ('PENDING','ACTIVE','REVOKED')),
 claim_hash text CHECK(length(claim_hash)=64),
 lease_expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,conversation_id,owner_user_id,device_id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,device_id) REFERENCES openfox.device_grants(organization_id,device_id),
 CHECK((status='PENDING')=(claim_hash IS NOT NULL))
);
ALTER TABLE openfox.browser_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.browser_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_isolation ON openfox.browser_bindings
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.browser_bindings TO openfox_app;
RESET ROLE;
