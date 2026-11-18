-- C01 controlled conversation sources. Disposable browser databases only.
-- These are fixture messages, not output from a real external Runtime.
\set ON_ERROR_STOP on
BEGIN;
INSERT INTO openfox.agent_config_versions
    (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model, created_by)
SELECT a.organization_id, a.id, 1, a.device_id, g.id, 'codex', 'fixture-model', a.owner_user_id
FROM openfox.agents a
JOIN openfox.device_grants g ON g.organization_id=a.organization_id AND g.device_id=a.device_id
WHERE a.organization_id='00000000-0000-0000-0000-0000000000a1'
  AND a.id='00000000-0000-0000-0000-0000000001a8';

INSERT INTO openfox.conversations
    (organization_id, id, space_id, agent_id, owner_user_id, visibility, title, next_message_seq)
VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000003c01',
     '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-0000000001a8',
     '00000000-0000-0000-0000-000000000001', 'PRIVATE', 'C01 user quote fixture', 2),
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000003c02',
     '00000000-0000-0000-0000-0000000000aa', '00000000-0000-0000-0000-0000000001a8',
     '00000000-0000-0000-0000-000000000001', 'PRIVATE', 'C01 Agent quote fixture', 2);

INSERT INTO openfox.messages
    (organization_id, id, conversation_id, seq, author_kind, author_user_id, author_agent_id, body)
VALUES
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000003c11',
     '00000000-0000-0000-0000-000000003c01', 1, 'USER',
     '00000000-0000-0000-0000-000000000001', NULL, repeat('中🦊', 1001)),
    ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-000000003c12',
     '00000000-0000-0000-0000-000000003c02', 1, 'AGENT',
     NULL, '00000000-0000-0000-0000-0000000001a8', repeat('中🦊', 1001));
COMMIT;
