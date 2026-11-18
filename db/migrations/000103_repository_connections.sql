-- Existing repositories remain anonymous. A connection is an explicit shared grant.
SET ROLE openfox_migrator;
ALTER TABLE openfox.agent_repos ADD COLUMN connection_id uuid,
 ADD CONSTRAINT agent_repo_connection_fk FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id);
ALTER TABLE openfox.space_repos ADD COLUMN connection_id uuid,
 ADD CONSTRAINT space_repo_connection_fk FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id);
RESET ROLE;
