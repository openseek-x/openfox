-- Short-lived rendezvous between API replicas. Payload slots contain TLS records.
SET ROLE openfox_migrator;
CREATE UNLOGGED TABLE openfox.gitlab_proxy_streams (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id uuid NOT NULL,
  device_id uuid NOT NULL,
  owner_id uuid NOT NULL,
  address text NOT NULL CHECK (length(address) <= 300),
  expires_at timestamptz NOT NULL DEFAULT (clock_timestamp() + interval '30 seconds'),
  claimed boolean NOT NULL DEFAULT false,
  to_device bytea CHECK (octet_length(to_device) <= 32768),
  to_center bytea CHECK (octet_length(to_center) <= 32768),
  FOREIGN KEY (organization_id,device_id) REFERENCES openfox.device_grants(organization_id,device_id) ON DELETE CASCADE
);
CREATE INDEX gitlab_proxy_expiry ON openfox.gitlab_proxy_streams(organization_id,device_id,expires_at);
ALTER TABLE openfox.gitlab_proxy_streams ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.gitlab_proxy_streams FORCE ROW LEVEL SECURITY;
CREATE POLICY proxy_stream_scope ON openfox.gitlab_proxy_streams
USING (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (device_id,'00000000-0000-0000-0000-000000000000'::uuid))
WITH CHECK (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (device_id,'00000000-0000-0000-0000-000000000000'::uuid));
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.gitlab_proxy_streams TO openfox_app;
RESET ROLE;
