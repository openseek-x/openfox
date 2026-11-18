-- A task discovered by polling after the import finished must survive undoing the initial import.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_mappings ADD COLUMN sync_created boolean NOT NULL DEFAULT false;
CREATE OR REPLACE FUNCTION openfox_private.list_import_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid import scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE (j.status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'CLEANING') OR (j.status = 'DRAFT' AND j.phase = 'fetching'))
           AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= clock_timestamp())
           AND (j.retry_at IS NULL OR j.retry_at <= clock_timestamp())
        UNION
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE j.status = 'DRAFT' AND j.created_at <= clock_timestamp() - interval '24 hours'
        UNION
        SELECT a.organization_id FROM openfox.external_accounts a
          JOIN openfox.invitations i ON i.id = a.invitation_id
         WHERE a.linked_user_id IS NULL AND i.status = 'ACCEPTED'
        UNION
        SELECT l.organization_id FROM openfox.import_sync_links l
          JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id AND j.status='SUCCEEDED'
         WHERE l.state='ACTIVE' AND l.next_check<=clock_timestamp()
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;

RESET ROLE;
