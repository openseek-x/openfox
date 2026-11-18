-- R05: widen only the two Runtime provider CHECKs from migration 132.
-- No data, RLS, columns or tables change. The runner applies this transactionally.
-- Locks/validation scan the two tables. Application rollback retains these checks;
-- stop Hermes Runs and explicitly reconfigure/archive affected Agents first.
SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations DROP CONSTRAINT conversations_overrides_valid;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_overrides_valid CHECK (
    (runtime_override IS NULL OR runtime_override IN ('codex', 'claude-code', 'opencode', 'pi', 'codebuddy', 'hermes'))
    AND (model_override IS NULL OR (char_length(model_override) BETWEEN 1 AND 128
                                    AND model_override !~ '[\u0001-\u001f\u007f-\u009f]'))
    AND (approval_mode_override IS NULL
         OR approval_mode_override IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS'))
    AND overrides_version >= 0
) NOT VALID;
ALTER TABLE openfox.conversations VALIDATE CONSTRAINT conversations_overrides_valid;

ALTER TABLE openfox.device_runtime_defaults DROP CONSTRAINT device_runtime_defaults_runtime_check;
ALTER TABLE openfox.device_runtime_defaults ADD CONSTRAINT device_runtime_defaults_runtime_check
    CHECK (runtime IN ('codex', 'claude-code', 'opencode', 'pi', 'codebuddy', 'hermes')) NOT VALID;
ALTER TABLE openfox.device_runtime_defaults VALIDATE CONSTRAINT device_runtime_defaults_runtime_check;

RESET ROLE;
