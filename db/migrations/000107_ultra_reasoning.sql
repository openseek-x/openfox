SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations
    DROP CONSTRAINT conversations_reasoning_effort_valid,
    ADD CONSTRAINT conversations_reasoning_effort_valid CHECK (
        reasoning_effort_override IS NULL OR reasoning_effort_override IN
        ('DEFAULT', 'LOW', 'MEDIUM', 'HIGH', 'EXTRA_HIGH', 'MAX', 'ULTRA')
    );

ALTER TABLE openfox.runs
    DROP CONSTRAINT runs_reasoning_effort_valid,
    ADD CONSTRAINT runs_reasoning_effort_valid CHECK (
        reasoning_effort IN ('DEFAULT', 'LOW', 'MEDIUM', 'HIGH', 'EXTRA_HIGH', 'MAX', 'ULTRA')
    );
