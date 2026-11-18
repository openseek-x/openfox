-- Runtime has execute-only access to bounded expiry cleanup.
SET ROLE openfox_migrator;
CREATE FUNCTION openfox_private.purge_device_challenges()
RETURNS bigint
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    WITH expired AS (
        SELECT id FROM openfox.device_challenges
         WHERE expires_at < CURRENT_TIMESTAMP - interval '7 days'
         ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
    ), removed AS (
        DELETE FROM openfox.device_challenges c USING expired
         WHERE c.id = expired.id RETURNING 1
    )
    SELECT count(*) FROM removed;
$$;
REVOKE ALL ON FUNCTION openfox_private.purge_device_challenges() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.purge_device_challenges() TO openfox_context;
RESET ROLE;
