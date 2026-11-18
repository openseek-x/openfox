-- 阶段 7d「对话补齐」：快捷回复（个人 + 组织两级）与小队对话。
--
-- 仅扩展（expand-only）：
--   - quick_replies：新表。scope = PERSONAL（仅 owner_user_id 本人可见和维护）或 ORG（全员可读，
--     只有组织所有者 / 管理员维护，由应用层判断）。删除为软删除（deleted_at），排序用 position（0 起）。
--     version 是编辑的乐观锁。每人 / 每组织的条数上限（各 50）由服务端在持有咨询锁后计数。
--     RLS 只做租户隔离，读写权限在应用层。
--   - conversations.crew_id：可空。非空表示这是小队对话；agent_id 仍然有值（创建时的队长），
--     旧会话全部为 NULL，行为不变。
--   - conversation_inputs.target_agent_id：可空。小队对话里本条输入应由哪个成员 Agent 执行；
--     NULL 表示由 conversations.agent_id 执行（旧行为）。
--   - validate_run_scope：Run 的 agent_id 原先必须等于会话的 agent_id；现在小队会话允许它是该小队的成员 Agent。
--     非小队会话的校验与原来完全相同。
--   - runs 的复合外键 (organization_id, conversation_id, space_id, agent_id) → conversations 同样要求 Run 的 Agent
--     等于会话 Agent，会挡住小队成员的 Run；改为只引用 (organization_id, id, space_id)
--     （需要 conversations 上新增对应的唯一约束），Agent 与会话的对应关系由 validate_run_scope 触发器保证。
-- 锁：新表与新可空列（无默认值，只改元数据）；外键添加时对 agent_crews / agents 只有短暂的 SHARE ROW EXCLUSIVE 锁。
--   注意：conversations_org_id_space_unique 会在 conversations 上建唯一索引，建索引期间持有 SHARE 锁（阻塞写入，读不受影响），
--   耗时与会话数成正比；runs 新外键先 NOT VALID 再 VALIDATE（校验时 SHARE UPDATE EXCLUSIVE，不阻塞读写）。
--   会话很多的环境应在低峰期执行，或先用 CREATE UNIQUE INDEX CONCURRENTLY 建好索引后改为 USING INDEX（需拆出单独的非事务步骤）。
-- 回滚：应用回退后新表和新列闲置即可（旧版本不读也不写）。确需移除时：先确认没有 crew_id 非空的会话
--   （否则这些会话的 Run 会违反旧触发器和旧外键），再按相反顺序恢复 000011 的 validate_run_scope 与
--   runs_organization_id_conversation_id_space_id_agent_id_fkey、DROP 两个新列、DROP TABLE openfox.quick_replies。

SET ROLE openfox_migrator;

CREATE TABLE openfox.quick_replies (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    scope text NOT NULL,
    owner_user_id uuid NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, owner_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT quick_replies_scope_valid CHECK (scope IN ('PERSONAL', 'ORG')),
    CONSTRAINT quick_replies_title_valid CHECK (char_length(btrim(title)) BETWEEN 1 AND 40),
    CONSTRAINT quick_replies_body_valid CHECK (char_length(btrim(body)) BETWEEN 1 AND 2000),
    CONSTRAINT quick_replies_position_valid CHECK (position BETWEEN 0 AND 999),
    CONSTRAINT quick_replies_version_positive CHECK (version > 0)
);
CREATE INDEX quick_replies_personal_idx
    ON openfox.quick_replies (organization_id, owner_user_id, position, created_at, id)
    WHERE scope = 'PERSONAL' AND deleted_at IS NULL;
CREATE INDEX quick_replies_org_idx
    ON openfox.quick_replies (organization_id, position, created_at, id)
    WHERE scope = 'ORG' AND deleted_at IS NULL;

ALTER TABLE openfox.quick_replies ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.quick_replies FORCE ROW LEVEL SECURITY;
CREATE POLICY quick_replies_tenant_isolation ON openfox.quick_replies
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.conversations ADD COLUMN crew_id uuid;
ALTER TABLE openfox.conversations
    ADD CONSTRAINT conversations_crew_fk
        FOREIGN KEY (organization_id, crew_id) REFERENCES openfox.agent_crews (organization_id, id);

ALTER TABLE openfox.conversation_inputs ADD COLUMN target_agent_id uuid;
ALTER TABLE openfox.conversation_inputs
    ADD CONSTRAINT conversation_inputs_target_agent_fk
        FOREIGN KEY (organization_id, target_agent_id) REFERENCES openfox.agents (organization_id, id);

ALTER TABLE openfox.conversations
    ADD CONSTRAINT conversations_org_id_space_unique UNIQUE (organization_id, id, space_id);
ALTER TABLE openfox.runs
    DROP CONSTRAINT runs_organization_id_conversation_id_space_id_agent_id_fkey;
ALTER TABLE openfox.runs
    ADD CONSTRAINT runs_conversation_space_fkey
        FOREIGN KEY (organization_id, conversation_id, space_id)
        REFERENCES openfox.conversations (organization_id, id, space_id) NOT VALID;
ALTER TABLE openfox.runs VALIDATE CONSTRAINT runs_conversation_space_fkey;

-- Run 与会话的范围校验：除 agent_id 外与 000011 完全一致；小队会话允许 Run 的 Agent 是该小队成员。
CREATE OR REPLACE FUNCTION openfox_private.validate_run_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    conversation_task_id uuid;
    agent_device_id uuid;
    grant_device_id uuid;
    grant_status text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.organization_id <> OLD.organization_id
           OR NEW.space_id <> OLD.space_id
           OR NEW.conversation_id <> OLD.conversation_id
           OR NEW.agent_id <> OLD.agent_id
           OR NEW.computer_id <> OLD.computer_id
           OR NEW.grant_id <> OLD.grant_id
           OR NEW.task_id IS DISTINCT FROM OLD.task_id THEN
            RAISE EXCEPTION 'Run identity is immutable' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    SELECT c.task_id INTO conversation_task_id
      FROM openfox.conversations c
     WHERE c.organization_id = NEW.organization_id
       AND c.id = NEW.conversation_id
       AND c.space_id = NEW.space_id
       AND (c.agent_id = NEW.agent_id
            OR (c.crew_id IS NOT NULL AND EXISTS (
                SELECT 1 FROM openfox.agent_crew_members m
                 WHERE m.organization_id = c.organization_id
                   AND m.crew_id = c.crew_id AND m.agent_id = NEW.agent_id)));
    IF NOT FOUND OR NEW.task_id IS DISTINCT FROM conversation_task_id THEN
        RAISE EXCEPTION 'Run task does not match its conversation'
            USING ERRCODE = '23514';
    END IF;

    SELECT device_id INTO agent_device_id
      FROM openfox.agents
     WHERE organization_id = NEW.organization_id AND id = NEW.agent_id;
    SELECT device_id, status INTO grant_device_id, grant_status
      FROM openfox.device_grants
     WHERE organization_id = NEW.organization_id AND id = NEW.grant_id;
    IF NOT FOUND OR grant_status <> 'ACTIVE' OR agent_device_id IS DISTINCT FROM NEW.computer_id
       OR grant_device_id IS DISTINCT FROM NEW.computer_id THEN
        RAISE EXCEPTION 'Run device grant does not match the Agent computer'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.quick_replies TO openfox_app;
