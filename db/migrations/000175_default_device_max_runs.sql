SET ROLE openfox_migrator;
ALTER TABLE openfox.device_registration_tokens ALTER COLUMN max_slots SET DEFAULT 5;
RESET ROLE;
