# Collaboration contract

The collaboration API is tenant-scoped and is exposed under `/api/v1` by the
current server. A request must authenticate as a user and select an active
organization membership. Cookie mutations require the existing CSRF header;
Bearer mutations do not use the browser CSRF path.

## Stable rules

- A space has an immutable internal UUID and an organization-unique normalized
  prefix. Prefix changes do not change task UUIDs, historical activities, or
  result references.
- Task display IDs are derived from the current prefix and an atomically
  allocated per-space sequence. Sequence gaps are allowed.
- Task statuses have one of the fixed semantic categories `TRIAGE`, `BACKLOG`,
  `TODO`, `IN_PROGRESS`, `DONE`, `CANCELLED`, or `DUPLICATE`. The category is
  never user-renamed or reinterpreted. Status name, description, position,
  color, and icon are organization records; multiple records may share a
  category.
- A task type is a space-level configurable object with name, description,
  icon, enabled state, archive state, and optimistic version. It references
  only organization statuses through `workflow_statuses`; the association
  controls the available set and order, and has exactly one non-terminal
  default status.
- People assignees and Agent executors are separate fields. Collaboration
  validates the organization boundary; Agent capacity and Run execution are
  governed by `agent-management.md` and `execution.md`.
- Task updates require the current `version`. A stale version returns
  `VERSION_CONFLICT` and does not partially apply fields or labels.
- A status referenced by a task cannot be deleted; it may be archived while
  existing tasks retain the stable `status_id`. Archiving removes the status
  from non-terminal workflow selections and rejects archiving when it is a
  workflow default until another default is selected. An unreferenced extra
  status may be deleted, but each fixed category keeps at least one active
  record. Task types with tasks cannot be archived.
- Task activities contain safe structured diffs and a unique request source ID;
  they never contain credentials, tokens, prompts, or private conversation
  bodies.

## Endpoint families

- `PATCH /v1/organizations/{organizationID}/spaces/{spaceID}`
- `GET|PUT /v1/organizations/{organizationID}/spaces/{spaceID}/members[/{userID}]`
- `GET /v1/organizations/{organizationID}/task-statuses`
- `POST /v1/organizations/{organizationID}/task-statuses`
- `PATCH /v1/organizations/{organizationID}/task-statuses/{statusID}`
- `DELETE /v1/organizations/{organizationID}/task-statuses/{statusID}`
- `GET|POST /v1/organizations/{organizationID}/spaces/{spaceID}/task-types`
- `PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/task-types/{typeID}`
- `GET|POST /v1/organizations/{organizationID}/spaces/{spaceID}/tasks`
- `GET|PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/tasks/{taskID}`
- `GET /v1/organizations/{organizationID}/spaces/{spaceID}/tasks/{taskID}/activities`

The server maps hidden cross-tenant or private-space resources to `404`,
authorization failures to `403`, malformed request data to `400`, and stale
versions or state conflicts to `409`, using the shared error envelope.
