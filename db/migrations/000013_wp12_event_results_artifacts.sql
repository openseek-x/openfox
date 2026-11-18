-- WP-12 durable execution events, final results, and bounded artifact metadata.
-- Event payloads are typed by the event contract and remain bounded JSON; this
-- is not a generic key/value extension table.

SET ROLE openfox_migrator;

CREATE SEQUENCE openfox.run_event_stream_seq
    AS bigint
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 100;

ALTER SEQUENCE openfox.run_event_stream_seq OWNER TO openfox_migrator;

CREATE TABLE openfox.run_events (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    sequence bigint NOT NULL,
    stream_seq bigint NOT NULL DEFAULT nextval('openfox.run_event_stream_seq'),
    type text NOT NULL,
    safe_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    payload_hash text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, run_id, generation, sequence),
    UNIQUE (organization_id, stream_seq),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_events_generation_positive CHECK (generation > 0),
    CONSTRAINT run_events_sequence_positive CHECK (sequence > 0),
    CONSTRAINT run_events_type_valid CHECK (length(btrim(type)) BETWEEN 1 AND 64),
    CONSTRAINT run_events_payload_hash_valid CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT run_events_payload_size CHECK (pg_column_size(safe_payload) <= 65536)
);

CREATE INDEX run_events_run_idx
    ON openfox.run_events (organization_id, run_id, generation, sequence);
CREATE INDEX run_events_stream_idx
    ON openfox.run_events (organization_id, stream_seq);

CREATE TABLE openfox.run_results (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    status text NOT NULL,
    summary text NOT NULL DEFAULT '',
    output text NOT NULL DEFAULT '',
    checks jsonb NOT NULL DEFAULT '[]'::jsonb,
    unverified boolean NOT NULL DEFAULT false,
    artifact_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    branch_ref text NOT NULL DEFAULT '',
    payload_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, run_id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_results_generation_positive CHECK (generation > 0),
    CONSTRAINT run_results_status_valid CHECK (status IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT')),
    CONSTRAINT run_results_summary_length CHECK (length(summary) <= 16384),
    CONSTRAINT run_results_output_length CHECK (length(output) <= 262144),
    CONSTRAINT run_results_payload_hash_valid CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT run_results_checks_size CHECK (pg_column_size(checks) <= 65536),
    CONSTRAINT run_results_artifacts_size CHECK (pg_column_size(artifact_ids) <= 16384)
);

CREATE TABLE openfox.artifacts (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL,
    result_id uuid,
    name text NOT NULL,
    storage_key text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    mime_type text NOT NULL DEFAULT 'application/octet-stream',
    state text NOT NULL DEFAULT 'AVAILABLE',
    retention_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, storage_key),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    FOREIGN KEY (organization_id, result_id)
        REFERENCES openfox.run_results (organization_id, id),
    CONSTRAINT artifacts_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 255),
    CONSTRAINT artifacts_storage_key_valid CHECK (length(storage_key) BETWEEN 1 AND 512),
    CONSTRAINT artifacts_size_valid CHECK (size_bytes >= 0 AND size_bytes <= 20971520),
    CONSTRAINT artifacts_sha256_valid CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT artifacts_mime_type_valid CHECK (length(mime_type) BETWEEN 1 AND 255),
    CONSTRAINT artifacts_state_valid CHECK (state IN ('AVAILABLE', 'DELETED', 'MISSING'))
);

CREATE INDEX artifacts_run_idx
    ON openfox.artifacts (organization_id, run_id, created_at, id);
CREATE INDEX artifacts_retention_idx
    ON openfox.artifacts (retention_until)
    WHERE state = 'AVAILABLE' AND retention_until IS NOT NULL;

RESET ROLE;

GRANT USAGE, SELECT ON SEQUENCE openfox.run_event_stream_seq TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON
    openfox.run_events,
    openfox.run_results,
    openfox.artifacts
TO openfox_app;

-- A terminal device result releases the reservation in the same transaction.
-- The function still validates the tenant context and reservation ownership;
-- this grant does not expose a general capacity mutation primitive.
GRANT EXECUTE ON FUNCTION openfox_private.release_reservation(uuid, uuid) TO openfox_app;

ALTER TABLE openfox.run_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_events FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_results FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifacts FORCE ROW LEVEL SECURITY;

CREATE POLICY run_events_tenant_isolation ON openfox.run_events
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_results_tenant_isolation ON openfox.run_results
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY artifacts_tenant_isolation ON openfox.artifacts
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
