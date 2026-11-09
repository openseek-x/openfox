-- The tombstone keeps existing foreign keys and replay keys intact. All ordinary
-- readers (including older clients, exports, inbox and workers) lose visibility.
SET ROLE openfox_migrator;
CREATE TABLE openfox.task_comment_deletions (
    organization_id uuid NOT NULL,
    comment_id uuid NOT NULL,
    deleted_by uuid NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, comment_id),
    FOREIGN KEY (organization_id, comment_id) REFERENCES openfox.task_comments (organization_id, id),
    FOREIGN KEY (organization_id, deleted_by) REFERENCES openfox.organization_members (organization_id, user_id)
);
ALTER TABLE openfox.task_comment_deletions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_comment_deletions FORCE ROW LEVEL SECURITY;
CREATE POLICY comment_deletions_tenant ON openfox.task_comment_deletions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id() AND deleted_by = openfox_private.current_actor_id());
GRANT SELECT, INSERT ON openfox.task_comment_deletions TO openfox_app;
CREATE POLICY task_comments_not_deleted ON openfox.task_comments AS RESTRICTIVE FOR SELECT
    USING (NOT EXISTS (SELECT 1 FROM openfox.task_comment_deletions d
                       WHERE d.organization_id=task_comments.organization_id AND d.comment_id=task_comments.id));
CREATE POLICY notifications_comment_not_deleted ON openfox.notifications AS RESTRICTIVE FOR SELECT
    USING (comment_id IS NULL OR NOT EXISTS (SELECT 1 FROM openfox.task_comment_deletions d
                       WHERE d.organization_id=notifications.organization_id AND d.comment_id=notifications.comment_id));
RESET ROLE;
