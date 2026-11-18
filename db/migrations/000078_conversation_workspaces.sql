-- Optional creation-time selection. Both columns contain only logical root names
-- and tenant-local identifiers. Existing conversations and runs require no backfill.
SET ROLE openfox_migrator;
ALTER TABLE openfox.conversations ADD COLUMN workspace_selection jsonb
    CHECK (workspace_selection IS NULL OR (
        jsonb_typeof(workspace_selection) = 'object'
        AND workspace_selection ?& ARRAY['mode','computer_id']
        AND workspace_selection ->> 'mode' IN ('DIRECT','WORKTREE','REUSE','BASELINE')
    ));
ALTER TABLE openfox.runs ADD COLUMN workspace_selection jsonb
    CHECK (workspace_selection IS NULL OR (
        jsonb_typeof(workspace_selection) = 'object'
        AND workspace_selection ?& ARRAY['mode','computer_id']
        AND workspace_selection ->> 'computer_id' = computer_id::text
    ));
RESET ROLE;
