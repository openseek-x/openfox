SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations
    ADD COLUMN reasoning_effort_override text;
ALTER TABLE openfox.conversations
    ADD CONSTRAINT conversations_reasoning_effort_valid CHECK (
        reasoning_effort_override IS NULL OR reasoning_effort_override IN
        ('DEFAULT', 'LOW', 'MEDIUM', 'HIGH', 'EXTRA_HIGH', 'MAX')
    );

ALTER TABLE openfox.runs
    ADD COLUMN reasoning_effort text NOT NULL DEFAULT 'DEFAULT';
ALTER TABLE openfox.runs ADD COLUMN git_settings_snapshot jsonb;
ALTER TABLE openfox.agent_config_versions ADD COLUMN git_co_author_enabled boolean DEFAULT false;
ALTER TABLE openfox.runs
    ADD CONSTRAINT runs_reasoning_effort_valid CHECK (
        reasoning_effort IN ('DEFAULT', 'LOW', 'MEDIUM', 'HIGH', 'EXTRA_HIGH', 'MAX')
    );

CREATE TABLE openfox.organization_git_settings (
    organization_id uuid PRIMARY KEY,
    author_name text NOT NULL DEFAULT '',
    author_email text NOT NULL DEFAULT '',
    co_author_enabled boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, updated_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT organization_git_settings_name_length CHECK (length(author_name) <= 256),
    CONSTRAINT organization_git_settings_email_length CHECK (length(author_email) <= 254),
    CONSTRAINT organization_git_settings_email_valid CHECK (
        author_email = '' OR author_email ~ '^[^\s@]+@[^\s@]+\.[^\s@]+$'
    ),
    CONSTRAINT organization_git_settings_version_positive CHECK (version > 0)
);

CREATE TABLE openfox.space_git_settings (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    author_name text NOT NULL DEFAULT '',
    author_email text NOT NULL DEFAULT '',
    co_author_enabled boolean,
    version bigint NOT NULL DEFAULT 1,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, space_id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, updated_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT space_git_settings_name_length CHECK (length(author_name) <= 256),
    CONSTRAINT space_git_settings_email_length CHECK (length(author_email) <= 254),
    CONSTRAINT space_git_settings_email_valid CHECK (
        author_email = '' OR author_email ~ '^[^\s@]+@[^\s@]+\.[^\s@]+$'
    ),
    CONSTRAINT space_git_settings_version_positive CHECK (version > 0)
);

INSERT INTO openfox.organization_git_settings (organization_id)
SELECT id FROM openfox.organizations
ON CONFLICT (organization_id) DO NOTHING;

INSERT INTO openfox.space_git_settings (organization_id, space_id)
SELECT organization_id, id FROM openfox.spaces
ON CONFLICT (organization_id, space_id) DO NOTHING;

CREATE OR REPLACE FUNCTION openfox_private.ensure_organization_git_settings()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_git_settings (organization_id)
    VALUES (NEW.id);
    RETURN NEW;
END
$$;
ALTER FUNCTION openfox_private.ensure_organization_git_settings() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.ensure_organization_git_settings() FROM PUBLIC;
CREATE TRIGGER organizations_ensure_git_settings
AFTER INSERT ON openfox.organizations
FOR EACH ROW EXECUTE FUNCTION openfox_private.ensure_organization_git_settings();

CREATE OR REPLACE FUNCTION openfox_private.ensure_space_git_settings()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.space_git_settings (organization_id, space_id)
    VALUES (NEW.organization_id, NEW.id);
    RETURN NEW;
END
$$;
ALTER FUNCTION openfox_private.ensure_space_git_settings() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.ensure_space_git_settings() FROM PUBLIC;
CREATE TRIGGER spaces_ensure_git_settings
AFTER INSERT ON openfox.spaces
FOR EACH ROW EXECUTE FUNCTION openfox_private.ensure_space_git_settings();

ALTER TABLE openfox.organization_git_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_git_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_git_settings_tenant_isolation
    ON openfox.organization_git_settings TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY organization_git_settings_trigger_insert
    ON openfox.organization_git_settings FOR INSERT TO openfox_migrator
    WITH CHECK (true);

ALTER TABLE openfox.space_git_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_git_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY space_git_settings_tenant_isolation
    ON openfox.space_git_settings TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY space_git_settings_trigger_insert
    ON openfox.space_git_settings FOR INSERT TO openfox_migrator
    WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE ON openfox.organization_git_settings TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.space_git_settings TO openfox_app;

ALTER TABLE openfox.agent_config_versions
    DROP CONSTRAINT agent_config_versions_git_author_name_valid,
    DROP CONSTRAINT agent_config_versions_git_author_email_valid;
ALTER TABLE openfox.agent_config_versions
    ADD CONSTRAINT agent_config_versions_git_author_name_valid CHECK (
        git_author_name_mode IN ('INHERIT', 'USER_AND_AGENT', 'USER', 'OWNER_AND_AGENT', 'OWNER', 'MACHINE', 'MANUAL')
        AND length(git_author_name) <= 128
        AND (git_author_name_mode <> 'MANUAL' OR length(btrim(git_author_name)) > 0)
    ),
    ADD CONSTRAINT agent_config_versions_git_author_email_valid CHECK (
        git_author_email_mode IN ('INHERIT', 'USER', 'OWNER', 'MACHINE', 'MANUAL')
        AND length(git_author_email) <= 254
        AND (git_author_email_mode <> 'MANUAL' OR git_author_email ~ '^[^\s@]+@[^\s@]+\.[^\s@]+$')
    );

