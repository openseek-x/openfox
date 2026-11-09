-- Additive: old comments start at version 1 and their threads are open.
SET ROLE openfox_migrator;
ALTER TABLE openfox.task_comments
    ADD COLUMN edit_version bigint NOT NULL DEFAULT 1,
    ADD COLUMN edited_at timestamptz,
    ADD COLUMN original_body_hash bytea,
    ADD COLUMN resolved_at timestamptz,
    ADD COLUMN resolved_by uuid,
    ADD COLUMN resolved_comment_id uuid,
    ADD CONSTRAINT task_comments_resolution_shape CHECK (
        (resolved_at IS NULL AND resolved_by IS NULL AND resolved_comment_id IS NULL) OR
        (reply_to_id IS NULL AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL AND resolved_comment_id IS NOT NULL)
    ) NOT VALID,
    ADD CONSTRAINT task_comments_resolver_fk FOREIGN KEY (organization_id, resolved_by) REFERENCES openfox.organization_members (organization_id, user_id) NOT VALID,
    ADD CONSTRAINT task_comments_resolution_fk FOREIGN KEY (organization_id, resolved_comment_id) REFERENCES openfox.task_comments (organization_id, id) NOT VALID;
ALTER TABLE openfox.task_comments VALIDATE CONSTRAINT task_comments_resolution_shape;
ALTER TABLE openfox.task_comments VALIDATE CONSTRAINT task_comments_resolution_fk;
ALTER TABLE openfox.task_comments VALIDATE CONSTRAINT task_comments_resolver_fk;
RESET ROLE;
