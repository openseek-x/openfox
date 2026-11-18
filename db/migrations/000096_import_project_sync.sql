-- Opt-in link for an API project import. An existing import is never enrolled by migration.
SET ROLE openfox_migrator;
CREATE TABLE openfox.import_sync_links (
 organization_id uuid NOT NULL, job_id uuid NOT NULL, connection_id uuid NOT NULL, space_id uuid NOT NULL,
 project_id text NOT NULL CHECK(length(project_id) BETWEEN 1 AND 128),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN('ACTIVE','PAUSED','DISCONNECTED')),
 version bigint NOT NULL DEFAULT 1, next_check timestamptz NOT NULL DEFAULT now(), lease uuid,
 last_success timestamptz, error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(organization_id,job_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_jobs(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id)
);
CREATE UNIQUE INDEX import_sync_live_project ON openfox.import_sync_links(organization_id,connection_id,project_id,space_id) WHERE state<>'DISCONNECTED';
CREATE INDEX import_sync_due ON openfox.import_sync_links(next_check,organization_id) WHERE state='ACTIVE';
CREATE TABLE openfox.import_sync_items(
 organization_id uuid NOT NULL,job_id uuid NOT NULL,external_id text NOT NULL,task_id uuid NOT NULL,
 source_title text NOT NULL,source_description text NOT NULL,
 PRIMARY KEY(organization_id,job_id,external_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_sync_links(organization_id,job_id),
 FOREIGN KEY(organization_id,task_id) REFERENCES openfox.tasks(organization_id,id)
);
ALTER TABLE openfox.import_sync_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_links FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_sync_items FORCE ROW LEVEL SECURITY;
CREATE POLICY import_sync_org ON openfox.import_sync_links USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY import_sync_scan ON openfox.import_sync_links FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY import_sync_item_org ON openfox.import_sync_items USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.import_sync_links,openfox.import_sync_items TO openfox_app;

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
        SELECT l.organization_id FROM openfox.import_sync_links l WHERE l.state='ACTIVE' AND l.next_check<=clock_timestamp()
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;

RESET ROLE;
