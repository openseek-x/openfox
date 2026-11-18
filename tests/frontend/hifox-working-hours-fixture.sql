\set ON_ERROR_STOP on
-- Isolated browser fixture only: the physical device remains offline.
INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,queue_timeout_seconds,run_timeout_seconds,created_by)
SELECT a.organization_id,a.id,1,a.device_id,g.id,'codex','default',60,600,a.owner_user_id
FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id=a.organization_id AND g.device_id=a.device_id
WHERE a.id='00000000-0000-0000-0000-0000000001a8';
