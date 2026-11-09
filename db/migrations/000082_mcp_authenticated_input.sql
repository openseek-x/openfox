-- Only messages submitted through a verified user session may activate a
-- physical computer owner's personal MCP configuration. No historical inference.
SET ROLE openfox_migrator;
ALTER TABLE openfox.messages ADD COLUMN authenticated_user_input boolean NOT NULL DEFAULT false;
RESET ROLE;
