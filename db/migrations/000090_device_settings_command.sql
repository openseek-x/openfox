SET ROLE openfox_migrator;
ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_kind_valid,
 ADD CONSTRAINT device_commands_kind_valid CHECK (kind IN ('runtime_probe','runtime_test','storage_cleanup','skill_scan','skill_import','device_settings'));
RESET ROLE;
