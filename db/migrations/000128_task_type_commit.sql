SET ROLE openfox_migrator;

ALTER TABLE openfox.task_types
    ADD COLUMN commit_type text,
    ADD CONSTRAINT task_types_commit_type_valid
        CHECK (commit_type IS NULL OR commit_type ~ '^[A-Za-z][A-Za-z0-9-]{0,31}$');

RESET ROLE;
