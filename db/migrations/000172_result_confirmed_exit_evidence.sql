SET ROLE openfox_migrator;

-- Match the evidence kinds accepted by VerifyEvidence and manual result acceptance.
ALTER TABLE openfox.run_reconciliation_evidence
    DROP CONSTRAINT run_reconciliation_evidence_exit_shape,
    ADD CONSTRAINT run_reconciliation_evidence_exit_shape CHECK (
        NOT process_exited OR kind IN ('PROCESS_EXITED', 'PROCESS_NOT_FOUND', 'RESULT_CONFIRMED')
    );

RESET ROLE;
