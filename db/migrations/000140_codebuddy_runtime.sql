-- R03: only widen the two provider checks. No rows, permissions, secrets or session
-- exports are changed. 000131 is reserved for P03; no placeholder is created.
-- The migration runner applies this transactionally. Replacing constraints takes
-- table locks; validation scans conversations and device_runtime_defaults.
-- Application rollback keeps these expanded checks and retained data. Stop/clear
-- CodeBuddy runs and explicitly reconfigure/archive their Agents before rollback;
-- never silently select another runtime or delete credentials/history.
SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations DROP CONSTRAINT conversations_overrides_valid;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_overrides_valid CHECK (
    (runtime_override IS NULL OR runtime_override IN ('codex', 'claude-code', 'opencode', 'pi', 'codebuddy'))
    AND (model_override IS NULL OR (char_length(model_override) BETWEEN 1 AND 128
                                    AND model_override !~ '[\u0001-\u001f\u007f-\u009f]'))
    AND (approval_mode_override IS NULL
         OR approval_mode_override IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS'))
    AND overrides_version >= 0
) NOT VALID;
ALTER TABLE openfox.conversations VALIDATE CONSTRAINT conversations_overrides_valid;

ALTER TABLE openfox.device_runtime_defaults DROP CONSTRAINT device_runtime_defaults_runtime_check;
ALTER TABLE openfox.device_runtime_defaults ADD CONSTRAINT device_runtime_defaults_runtime_check
    CHECK (runtime IN ('codex', 'claude-code', 'opencode', 'pi', 'codebuddy')) NOT VALID;
ALTER TABLE openfox.device_runtime_defaults VALIDATE CONSTRAINT device_runtime_defaults_runtime_check;

RESET ROLE;
