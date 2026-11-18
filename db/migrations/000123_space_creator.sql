-- O02: retain the actual creator for new spaces only. Historical spaces remain
-- unknown; neither current owners nor incomplete audit history are a backfill.
-- Apply before the updated API/worker. Old applications may still insert NULL.
-- Application rollback retains this additive column and its recorded identities.
SET ROLE openfox_migrator;

ALTER TABLE openfox.spaces
    ADD COLUMN created_by uuid,
    ADD CONSTRAINT spaces_created_by_member_fkey
        FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id)
        ON DELETE SET NULL (created_by);

RESET ROLE;
