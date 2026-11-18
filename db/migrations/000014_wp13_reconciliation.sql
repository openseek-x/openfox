-- WP-13 durable supervisor facts, reconciliation cases, and typed evidence.
-- Unknown process state is never converted to a terminal Run state by this
-- schema. Resolution is an explicit, versioned operation.

SET ROLE openfox_migrator;

CREATE TABLE openfox.run_supervisor_facts (
    organization_id uuid NOT NULL,
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    device_generation bigint NOT NULL,
    center_epoch bigint NOT NULL,
    supervisor_nonce text NOT NULL,
    process_pid integer,
    process_started_at timestamptz,
    workdir_binding_id uuid,
    workdir_path_hash text NOT NULL DEFAULT '',
    manifest_hash text NOT NULL,
    process_state text NOT NULL DEFAULT 'INTENDED',
    exit_code integer,
    process_exited_at timestamptz,
    last_event_sequence bigint NOT NULL DEFAULT 0,
    last_result_id uuid,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, run_id, generation),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_supervisor_facts_generation_positive CHECK (generation > 0),
    CONSTRAINT run_supervisor_facts_device_generation_positive CHECK (device_generation > 0),
    CONSTRAINT run_supervisor_facts_center_epoch_positive CHECK (center_epoch > 0),
    CONSTRAINT run_supervisor_facts_nonce_valid CHECK (length(btrim(supervisor_nonce)) BETWEEN 16 AND 128),
    CONSTRAINT run_supervisor_facts_pid_valid CHECK (process_pid IS NULL OR process_pid > 0),
    CONSTRAINT run_supervisor_facts_path_hash_valid CHECK (
        workdir_path_hash = '' OR workdir_path_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT run_supervisor_facts_manifest_hash_valid CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT run_supervisor_facts_state_valid CHECK (
        process_state IN ('INTENDED', 'STARTED', 'RUNNING', 'EXITED', 'NOT_FOUND', 'UNKNOWN')
    ),
    CONSTRAINT run_supervisor_facts_exit_shape CHECK (
        (process_state = 'EXITED' AND process_exited_at IS NOT NULL)
        OR (process_state <> 'EXITED' AND process_exited_at IS NULL)
    ),
    CONSTRAINT run_supervisor_facts_sequence_nonnegative CHECK (last_event_sequence >= 0),
    CONSTRAINT run_supervisor_facts_version_positive CHECK (version > 0)
);

CREATE INDEX run_supervisor_facts_state_idx
    ON openfox.run_supervisor_facts (organization_id, process_state, observed_at DESC);

CREATE TABLE openfox.run_reconciliation_cases (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    reason_code text NOT NULL,
    failure_code text NOT NULL,
    status text NOT NULL DEFAULT 'OPEN',
    safe_detail text NOT NULL DEFAULT '',
    resolution text,
    resolution_evidence_id uuid,
    opened_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at timestamptz,
    resolved_by text,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_reconciliation_cases_generation_positive CHECK (generation > 0),
    CONSTRAINT run_reconciliation_cases_reason_valid CHECK (
        reason_code IN ('LEASE_EXPIRED', 'DEVICE_OFFLINE', 'EPOCH_CONFLICT',
                        'AUTHORIZATION_REVOKED', 'UNKNOWN_PROCESS', 'PID_REUSE',
                        'STARTUP_CRASH', 'CANCEL_UNCONFIRMED', 'RESULT_UNCONFIRMED',
                        'CAPACITY_RELEASE_FAILED')
    ),
    CONSTRAINT run_reconciliation_cases_failure_valid CHECK (
        failure_code IN ('CREDENTIAL_REJECTED', 'QUOTA_EXHAUSTED', 'RATE_LIMITED',
                         'PROVIDER_OVERLOADED', 'MODEL_NOT_FOUND', 'CONTEXT_TOO_LARGE',
                         'IMAGE_TOO_LARGE', 'NETWORK_UNREACHABLE', 'WORKTREE_PREP_FAILED',
                         'REPO_UNAVAILABLE', 'RUNTIME_UNAVAILABLE', 'QUEUE_TIMEOUT',
                         'RUN_TIMEOUT', 'AUTHORIZATION_REVOKED', 'UNKNOWN_PROCESS',
                         'INTERNAL')
    ),
    CONSTRAINT run_reconciliation_cases_status_valid CHECK (status IN ('OPEN', 'RESOLVED', 'REJECTED')),
    CONSTRAINT run_reconciliation_cases_resolution_valid CHECK (
        resolution IS NULL OR resolution IN ('PROCESS_EXITED_FAILED',
                                             'PROCESS_NOT_STARTED_CANCELED',
                                             'RESULT_ACCEPTED', 'MANUAL_REVIEW', 'REJECTED')
    ),
    CONSTRAINT run_reconciliation_cases_resolution_shape CHECK (
        (status = 'OPEN' AND resolved_at IS NULL AND resolution IS NULL AND resolved_by IS NULL)
        OR (status <> 'OPEN' AND resolved_at IS NOT NULL AND resolution IS NOT NULL AND resolved_by IS NOT NULL)
    ),
    CONSTRAINT run_reconciliation_cases_detail_length CHECK (length(safe_detail) <= 4096),
    CONSTRAINT run_reconciliation_cases_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX run_reconciliation_cases_active_unique
    ON openfox.run_reconciliation_cases (organization_id, run_id, generation)
    WHERE status = 'OPEN';
CREATE INDEX run_reconciliation_cases_run_idx
    ON openfox.run_reconciliation_cases (organization_id, run_id, opened_at DESC, id);

CREATE TABLE openfox.run_reconciliation_evidence (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    case_id uuid NOT NULL,
    run_id uuid NOT NULL,
    generation bigint NOT NULL,
    device_generation bigint NOT NULL,
    center_epoch bigint NOT NULL,
    kind text NOT NULL,
    source text NOT NULL,
    device_id uuid,
    supervisor_nonce text NOT NULL,
    process_pid integer,
    process_started_at timestamptz,
    process_state text NOT NULL,
    process_exited boolean NOT NULL DEFAULT false,
    exit_code integer,
    event_sequence bigint NOT NULL DEFAULT 0,
    result_id uuid,
    payload_hash text NOT NULL,
    safe_detail text NOT NULL DEFAULT '',
    accepted_state text NOT NULL DEFAULT 'PENDING',
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, case_id)
        REFERENCES openfox.run_reconciliation_cases (organization_id, id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_reconciliation_evidence_generation_positive CHECK (generation > 0),
    CONSTRAINT run_reconciliation_evidence_device_generation_positive CHECK (device_generation > 0),
    CONSTRAINT run_reconciliation_evidence_center_epoch_positive CHECK (center_epoch > 0),
    CONSTRAINT run_reconciliation_evidence_kind_valid CHECK (
        kind IN ('PROCESS_STARTED', 'PROCESS_RUNNING', 'PROCESS_EXITED',
                 'PROCESS_NOT_FOUND', 'RESULT_CONFIRMED', 'HEARTBEAT', 'REVOCATION')
    ),
    CONSTRAINT run_reconciliation_evidence_source_valid CHECK (
        source IN ('DAEMON', 'CENTER', 'OPERATOR')
    ),
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    CONSTRAINT run_reconciliation_evidence_device_shape CHECK (
        (source = 'DAEMON' AND device_id IS NOT NULL)
        OR (source IN ('CENTER', 'OPERATOR') AND device_id IS NULL)
    ),
    CONSTRAINT run_reconciliation_evidence_nonce_valid CHECK (length(btrim(supervisor_nonce)) BETWEEN 16 AND 128),
    CONSTRAINT run_reconciliation_evidence_pid_valid CHECK (process_pid IS NULL OR process_pid > 0),
    CONSTRAINT run_reconciliation_evidence_state_valid CHECK (
        process_state IN ('INTENDED', 'STARTED', 'RUNNING', 'EXITED', 'NOT_FOUND', 'UNKNOWN')
    ),
    CONSTRAINT run_reconciliation_evidence_exit_shape CHECK (
        (process_exited AND kind IN ('PROCESS_EXITED', 'PROCESS_NOT_FOUND'))
        OR (NOT process_exited)
    ),
    CONSTRAINT run_reconciliation_evidence_sequence_nonnegative CHECK (event_sequence >= 0),
    CONSTRAINT run_reconciliation_evidence_hash_valid CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT run_reconciliation_evidence_accepted_state_valid CHECK (
        accepted_state IN ('PENDING', 'ACCEPTED', 'STALE', 'REJECTED')
    ),
    CONSTRAINT run_reconciliation_evidence_detail_length CHECK (length(safe_detail) <= 4096)
);

CREATE INDEX run_reconciliation_evidence_case_idx
    ON openfox.run_reconciliation_evidence (organization_id, case_id, created_at, id);
CREATE INDEX run_reconciliation_evidence_run_idx
    ON openfox.run_reconciliation_evidence (organization_id, run_id, generation, created_at, id);

ALTER TABLE openfox.run_reconciliation_cases
    ADD CONSTRAINT run_reconciliation_cases_resolution_evidence_fk
    FOREIGN KEY (organization_id, resolution_evidence_id)
    REFERENCES openfox.run_reconciliation_evidence (organization_id, id);

CREATE UNIQUE INDEX run_reconciliation_cases_resolution_evidence_unique
    ON openfox.run_reconciliation_cases (organization_id, resolution_evidence_id)
    WHERE resolution_evidence_id IS NOT NULL;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON
    openfox.run_supervisor_facts,
    openfox.run_reconciliation_cases,
    openfox.run_reconciliation_evidence
TO openfox_app;

ALTER TABLE openfox.run_supervisor_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_supervisor_facts FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_reconciliation_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_reconciliation_cases FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_reconciliation_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_reconciliation_evidence FORCE ROW LEVEL SECURITY;

CREATE POLICY run_supervisor_facts_tenant_isolation ON openfox.run_supervisor_facts
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_reconciliation_cases_tenant_isolation ON openfox.run_reconciliation_cases
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_reconciliation_evidence_tenant_isolation ON openfox.run_reconciliation_evidence
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
