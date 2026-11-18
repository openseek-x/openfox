-- I07 inbound only. No outbound queue, credentials for Tencent, or status
-- trigger is created. Disable reception before rolling back the application;
-- retain mappings and tasks so a retry cannot resurrect or duplicate a task.
SET ROLE openfox_migrator;

-- One durable local configuration identity per organization, including a
-- disconnected configuration. Re-enabling never silently changes its identity.
CREATE UNIQUE INDEX integration_bugly_organization_unique
    ON openfox.integration_connections (organization_id)
    WHERE provider='bugly' AND scope='ORGANIZATION';

CREATE TABLE openfox.integration_bugly_bindings (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    product_id text NOT NULL CHECK (char_length(product_id) BETWEEN 1 AND 255),
    project_name text NOT NULL DEFAULT '' CHECK (char_length(project_name)<=200),
    space_id uuid NOT NULL,
    task_type_id uuid NOT NULL,
    priority_mapping jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(priority_mapping)='object' AND pg_column_size(priority_mapping)<=4096),
    authorized_by uuid NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','PAUSED','NEEDS_ATTENTION','REMOVED')),
    status_reason text NOT NULL DEFAULT '' CHECK (char_length(status_reason)<=100),
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    removed_at timestamptz,
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,connection_id,id,product_id),
    FOREIGN KEY (organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
    FOREIGN KEY (organization_id,space_id) REFERENCES openfox.spaces(organization_id,id),
    FOREIGN KEY (organization_id,space_id,task_type_id) REFERENCES openfox.task_types(organization_id,space_id,id),
    FOREIGN KEY (organization_id,authorized_by) REFERENCES openfox.organization_members(organization_id,user_id),
    CHECK ((status='REMOVED')=(removed_at IS NOT NULL))
);
CREATE UNIQUE INDEX integration_bugly_product_route_unique
    ON openfox.integration_bugly_bindings(organization_id,connection_id,product_id)
    WHERE removed_at IS NULL;

CREATE TABLE openfox.integration_bugly_sources (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    binding_id uuid NOT NULL,
    product_id text NOT NULL CHECK (char_length(product_id) BETWEEN 1 AND 255),
    issue_key text NOT NULL CHECK (char_length(issue_key) BETWEEN 1 AND 255),
    issue_type text NOT NULL CHECK (char_length(issue_type) BETWEEN 1 AND 64),
    task_id uuid,
    task_space_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,connection_id,binding_id,product_id,issue_key),
    FOREIGN KEY (organization_id,connection_id,binding_id,product_id)
        REFERENCES openfox.integration_bugly_bindings(organization_id,connection_id,id,product_id),
    FOREIGN KEY (organization_id,task_id) REFERENCES openfox.tasks(organization_id,id) ON DELETE SET NULL (task_id),
    FOREIGN KEY (organization_id,task_space_id) REFERENCES openfox.spaces(organization_id,id)
);
CREATE INDEX integration_bugly_source_task_idx ON openfox.integration_bugly_sources(organization_id,task_id);

ALTER TABLE openfox.integration_bugly_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_bugly_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_bugly_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_bugly_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY bugly_bindings_tenant ON openfox.integration_bugly_bindings
    USING (organization_id=openfox_private.current_organization_id())
    WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY bugly_sources_tenant ON openfox.integration_bugly_sources
    USING (organization_id=openfox_private.current_organization_id())
    WITH CHECK (organization_id=openfox_private.current_organization_id());

CREATE FUNCTION openfox_private.resolve_bugly_webhook(p_token_hash bytea)
RETURNS TABLE(organization_id uuid,connection_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
    SELECT c.organization_id,c.id FROM openfox.integration_connections c
    JOIN openfox.organization_directory d ON d.organization_id=c.organization_id AND d.deleted_at IS NULL
    WHERE octet_length(p_token_hash)=32 AND c.webhook_token_hash=p_token_hash
      AND c.provider='bugly' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
$$;
ALTER FUNCTION openfox_private.resolve_bugly_webhook(bytea) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_bugly_webhook(bytea) FROM PUBLIC;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_bugly_bindings,openfox.integration_bugly_sources TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_bugly_webhook(bytea) TO openfox_context;
