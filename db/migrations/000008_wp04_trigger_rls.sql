-- WP-04 trigger visibility fix.
-- SECURITY DEFINER triggers run as openfox_migrator. Keep FORCE RLS enabled,
-- but allow that non-login owner role to inspect status metadata needed to
-- validate workflow rows and maintain the legacy task status projection.

SET ROLE openfox_migrator;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_policies
        WHERE schemaname = 'openfox'
          AND tablename = 'task_statuses'
          AND policyname = 'task_statuses_migrator_trigger_access'
    ) THEN
        CREATE POLICY task_statuses_migrator_trigger_access
            ON openfox.task_statuses
            AS PERMISSIVE
            FOR ALL
            TO openfox_migrator
            USING (true)
            WITH CHECK (true);
    END IF;
END
$$;

RESET ROLE;
