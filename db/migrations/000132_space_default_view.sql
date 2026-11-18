-- T03: a same-space shared view is resolved dynamically; historical spaces stay NULL.
SET ROLE openfox_migrator;

ALTER TABLE openfox.views ADD CONSTRAINT views_space_identity_unique
    UNIQUE (organization_id, space_id, id);
ALTER TABLE openfox.spaces ADD COLUMN default_view_id uuid;
ALTER TABLE openfox.spaces ADD CONSTRAINT spaces_default_view_same_space
    FOREIGN KEY (organization_id, id, default_view_id)
    REFERENCES openfox.views (organization_id, space_id, id)
    ON DELETE SET NULL (default_view_id);

DROP TRIGGER spaces_emit_event_update ON openfox.spaces;
CREATE TRIGGER spaces_emit_event_update
AFTER UPDATE OF name, visibility, status, prefix, icon, purpose, views_enabled, projects_enabled,
    iterations_enabled, triage_enabled, multi_assignee, estimation_scale, estimation_extended,
    estimation_allow_zero, estimation_count_unestimated, retired_at, deleted_at,
    triage_require_priority, triage_action, default_view_id
ON openfox.spaces
FOR EACH ROW WHEN (OLD IS DISTINCT FROM NEW)
EXECUTE FUNCTION openfox_private.emit_space_event();

RESET ROLE;
