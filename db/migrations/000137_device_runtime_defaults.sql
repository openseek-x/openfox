-- R06: organization-scoped computer Runtime defaults. Expand only; no secret backfill.
SET ROLE openfox_migrator;

ALTER TABLE openfox.agent_config_versions
    ADD COLUMN model_source text NOT NULL DEFAULT 'EXPLICIT',
    ADD CONSTRAINT agent_config_model_source_valid
        CHECK (model_source = 'EXPLICIT' OR (model_source = 'MACHINE' AND model = 'default'));

CREATE TABLE openfox.device_runtime_defaults (
    organization_id uuid NOT NULL REFERENCES openfox.organizations(id),
    device_id uuid NOT NULL REFERENCES openfox.physical_devices(id),
    runtime text NOT NULL CHECK (runtime IN ('codex', 'claude-code', 'opencode', 'pi')),
    model text CHECK (model IS NULL OR (octet_length(model) BETWEEN 1 AND 128 AND model <> 'default')),
    version bigint NOT NULL CHECK (version > 0),
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, device_id, runtime),
    FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members(organization_id, user_id)
);

CREATE TABLE openfox.device_runtime_env_vars (
    organization_id uuid NOT NULL,
    device_id uuid NOT NULL,
    runtime text NOT NULL,
    name text NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    value_ciphertext bytea NOT NULL CHECK (octet_length(value_ciphertext) BETWEEN 16 AND 8208),
    value_nonce bytea NOT NULL CHECK (octet_length(value_nonce) = 12),
    key_id text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9_.-]{1,64}$'),
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, device_id, runtime, name),
    FOREIGN KEY (organization_id, device_id, runtime)
        REFERENCES openfox.device_runtime_defaults(organization_id, device_id, runtime)
);

ALTER TABLE openfox.device_runtime_defaults ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_runtime_defaults FORCE ROW LEVEL SECURITY;
CREATE POLICY device_runtime_defaults_tenant ON openfox.device_runtime_defaults
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
ALTER TABLE openfox.device_runtime_env_vars ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_runtime_env_vars FORCE ROW LEVEL SECURITY;
CREATE POLICY device_runtime_env_vars_tenant ON openfox.device_runtime_env_vars
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;
GRANT SELECT, INSERT, UPDATE ON openfox.device_runtime_defaults TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.device_runtime_env_vars TO openfox_app;
