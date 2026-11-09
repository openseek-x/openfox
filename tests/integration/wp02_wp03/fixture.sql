\set ON_ERROR_STOP on

-- This fixture is for an isolated integration database only. It contains no
-- passwords, session tokens, private keys, or invitation plaintext tokens.
BEGIN;

INSERT INTO openfox.users (id, email) VALUES
    ('00000000-0000-0000-0000-000000000001', 'owner@example.test'),
    ('00000000-0000-0000-0000-000000000002', 'other@example.test'),
    ('00000000-0000-0000-0000-000000000003', 'admin@example.test'),
    ('00000000-0000-0000-0000-000000000004', 'member@example.test'),
    ('00000000-0000-0000-0000-000000000005', 'guest@example.test'),
    ('00000000-0000-0000-0000-000000000006', 'invite-target@example.test'),
    ('00000000-0000-0000-0000-000000000007', 'concurrent-invite-target@example.test')
ON CONFLICT (id) DO NOTHING;

INSERT INTO openfox.organizations (id, slug, name) VALUES
    ('00000000-0000-0000-0000-0000000000a1', 'wp02-org-a', 'WP02 Organization A'),
    ('00000000-0000-0000-0000-0000000000b1', 'wp02-org-b', 'WP02 Organization B')
ON CONFLICT (id) DO NOTHING;

INSERT INTO openfox.organization_members (organization_id, user_id, role, status, authorization_version) VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000001', 'OWNER', 'ACTIVE', 1),
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000003', 'ADMIN', 'ACTIVE', 1),
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000004', 'MEMBER', 'ACTIVE', 1),
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000005', 'GUEST', 'ACTIVE', 1),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-000000000002', 'OWNER', 'ACTIVE', 1)
ON CONFLICT (organization_id, user_id) DO UPDATE
SET role = EXCLUDED.role,
    status = EXCLUDED.status,
    authorization_version = EXCLUDED.authorization_version;

INSERT INTO openfox.spaces (organization_id, id, name, visibility) VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', 'WP02 A private space', 'PRIVATE'),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000bb', 'WP02 B private space', 'PRIVATE')
ON CONFLICT (organization_id, id) DO NOTHING;

-- Task status defaults are provisioned here because this fixture creates the
-- organizations and spaces after the migration has already run.
INSERT INTO openfox.task_statuses (organization_id, name, description, category, position)
SELECT organizations.id, seed.name, seed.description, seed.category, seed.position
FROM openfox.organizations
CROSS JOIN (
    VALUES
        ('Triage', '', 'TRIAGE', 0),
        ('Backlog', '', 'BACKLOG', 1),
        ('Todo', '', 'TODO', 2),
        ('In Progress', '', 'IN_PROGRESS', 3),
        ('Done', '', 'DONE', 4),
        ('Cancelled', '', 'CANCELLED', 5),
        ('Duplicate', '', 'DUPLICATE', 6)
) AS seed(name, description, category, position)
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.task_statuses existing
    WHERE existing.organization_id = organizations.id
      AND lower(existing.name) = lower(seed.name)
);

INSERT INTO openfox.task_types (organization_id, space_id, name, description, enabled, is_default)
SELECT spaces.organization_id, spaces.id, 'Task', '', true, true
FROM openfox.spaces
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.task_types existing
    WHERE existing.organization_id = spaces.organization_id
      AND existing.space_id = spaces.id
      AND lower(existing.name) = 'task'
);

INSERT INTO openfox.workflow_statuses (
    organization_id, space_id, task_type_id, status_id, position, is_default
)
SELECT task_types.organization_id, task_types.space_id, task_types.id,
       task_statuses.id, task_statuses.position, task_statuses.category = 'TODO'
FROM openfox.task_types
JOIN openfox.task_statuses
  ON task_statuses.organization_id = task_types.organization_id
WHERE NOT EXISTS (
    SELECT 1
    FROM openfox.workflow_statuses existing
    WHERE existing.organization_id = task_types.organization_id
      AND existing.task_type_id = task_types.id
      AND existing.status_id = task_statuses.id
);

INSERT INTO openfox.space_members (organization_id, space_id, user_id, role) VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000001', 'OWNER'),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000bb', '00000000-0000-0000-0000-000000000002', 'OWNER')
ON CONFLICT (organization_id, space_id, user_id) DO NOTHING;

INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES
    ('00000000-0000-0000-0000-000000000011', 'wp02-device-a', 1),
    ('00000000-0000-0000-0000-000000000022', 'wp02-device-b', 1)
ON CONFLICT (id) DO NOTHING;

INSERT INTO openfox.device_grants (organization_id, device_id, status, permission_version) VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000011', 'ACTIVE', 1),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-000000000022', 'ACTIVE', 1)
ON CONFLICT (organization_id, device_id) DO UPDATE
SET status = EXCLUDED.status,
    permission_version = EXCLUDED.permission_version;

INSERT INTO openfox.agents (organization_id, id, device_id) VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000a2', '00000000-0000-0000-0000-000000000011'),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000b2', '00000000-0000-0000-0000-000000000022')
ON CONFLICT (organization_id, id) DO NOTHING;

-- The invitation/device tables are created by the identity and device
-- implementation. assertions.sql performs the explicit table/column
-- preflight before it tries to inspect their invariants.
COMMIT;
