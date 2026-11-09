# Agent management contract

## Scope

An Agent is a configurable tenant object owned by one user and bound to one
Space. Agent management stores and authorizes the definition; it does not
execute Runs.
Code and office Agents use the fixed `CODE` and `OFFICE` kinds.

## Visibility and authorization

- `organization_id` and `space_id` are trusted server-side fields. An Agent,
  its device grant, and its configuration version must belong to the same
  organization; the Agent and space must match as well.
- `PRIVATE` Agents are visible and manageable only by `owner_user_id`.
- `SHARED` Agents are readable by active members who can read the Space.
  Create/update/archive and configuration-version writes require Space
  management permission, with the private-owner check still enforced.
- An archived Agent remains readable with `include_archived=true`, but new
  configuration versions are rejected. Restore changes only configuration
  state and does not replay an old Run.

## Immutable configuration versions

`agent_config_versions` is append-only. A version records the instructions,
computer, device grant, runtime, model, typed execution limits, and typed
directory policy. It is never updated or deleted. The Agent's
`current_config_version` is changed with an Agent `version` compare-and-swap;
stale writes return `VERSION_CONFLICT`.

The limits are bounded as follows:

- `max_concurrency`: 1-16;
- `queue_timeout_seconds`: 1-604800;
- `run_timeout_seconds`: 60-28800;
- `directory_mode`: `NONE`, `WORKSPACE`, or `EXPLICIT`.

`EXPLICIT` directory roots are logical relative references only. Absolute
paths, home-directory expansion, parent traversal, empty segments, control
characters, and more than 32 roots are rejected. Repository binding and actual
directory resolution are governed by `workdir.md`.

Daemon directory resolution: each executing computer maps logical root names
to local Git repositories with the repeatable `openfox daemon run
--directory-root name=/absolute/path` flag. An `EXPLICIT` Run uses
exactly one root; the Daemon creates one Git worktree per conversation under
its `--workspace-root` (branch `openfox/work/<id>`) and never modifies the
original repository's working tree. A Run fails during preparation with
`REPO_UNAVAILABLE` when the configuration lists more than one root, the root
name is not mapped on that computer, or the mapped path is not a usable Git
repository. Failure text names the logical root only, never the local path.

## API shape

- `GET/POST /v1/organizations/{organizationID}/spaces/{spaceID}/agents`
- `GET/PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}`
- `GET/POST /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}/config-versions`

Mutations require the authenticated user and, for cookie sessions, CSRF
protection. Request bodies are bounded and reject unknown fields.
