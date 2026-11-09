\set ON_ERROR_STOP on

BEGIN;
-- 组织目录（000017 起「我的组织」列表读它；集成测试夹具早于 000017，没有写这张表）。
INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
SELECT id, slug, name, created_at FROM openfox.organizations
ON CONFLICT (organization_id) DO NOTHING;
-- 组织 A 的第二位成员 carol，用于验证指派与 @ 提及进入对方收件箱。
INSERT INTO openfox.users (id, email) VALUES
('00000000-0000-0000-0000-0000000000c3', 'carol@example.test');
INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000c3', 'MEMBER');
INSERT INTO openfox.space_members (organization_id, space_id, user_id, role) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-0000000000c3', 'MEMBER');
INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000112', 'Ready for work', 'TODO', 20),
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000113', 'Building', 'IN_PROGRESS', 21),
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000000114', 'Reviewing', 'IN_PROGRESS', 22);
INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000111', 'WP11 task type', true);
INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000111', '00000000-0000-0000-0000-000000000112', 0, true),
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000111', '00000000-0000-0000-0000-000000000113', 1, false),
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000111', '00000000-0000-0000-0000-000000000114', 2, false);
\ir default-workflow.sql
INSERT INTO openfox.agents (organization_id, id, device_id, status, generation, max_slots, space_id, owner_user_id, name, kind, visibility, current_config_version) VALUES
('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000001a8', '00000000-0000-0000-0000-000000000011', 'ACTIVE', 1, 1,
 '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-000000000001', 'WP11 browser agent', 'CODE', 'PRIVATE', 1);
COMMIT;
