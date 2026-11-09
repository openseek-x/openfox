-- 阶段 8d-1「对话级 Runtime / 模型 / 权限模式覆盖」。
--
-- 仅扩展（expand-only）：只加列、加约束、替换一个事件触发器函数，不删除也不改写任何已有数据。
--   - conversations 增加四列：runtime_override / model_override / approval_mode_override（可空，NULL = 沿用 Agent 配置）
--     与 overrides_version（NOT NULL DEFAULT 0，覆盖设置的乐观锁，与 conversations.version 相互独立）。
--     约束：runtime_override 只能是 codex / claude-code / opencode / pi；model_override 1..128 字符且不含控制字符；
--     approval_mode_override 只能是 FOLLOW_RUNTIME / READ_ONLY / WORKSPACE_WRITE / FULL_ACCESS；
--     小队对话（crew_id 非空）不允许有任何覆盖（小队消息按 @ 分派给不同成员，会话级覆盖没有明确含义）。
--   - runs 增加三列快照：runtime / model / approval_mode（可空）。派发在创建 Run 时写入「有效配置」，
--     之后不再改写；这就是「修改后下一次运行生效、进行中的运行不受影响」的实现。
--     NULL 表示本迁移之前创建的旧 Run，读取时回退到 agent_config_versions；三列要么同时为空要么同时非空。
--   - agent_config_versions.approval_mode 增加 CHECK，取值限定在四档之内（原来只有 ^[A-Z][A-Z_]{1,31}$ 的正则，
--     后端也从不读取它）。先 NOT VALID 再 VALIDATE；若存在四档之外的历史值，VALIDATE 失败、整个迁移回滚，
--     不会留下半成品。执行前请先跑下面的预检查询。
--   - runs_wait_reason_valid 增加等待原因 RUNTIME_UNAVAILABLE：电脑在线，但对话选择的 Runtime 在最近一次心跳里
--     不存在或 runnable=false。Run 保持 QUEUED 等待（与 COMPUTER_OFFLINE 同语义，到 queue_expires_at 仍会 QUEUE_TIMEOUT）。
--   - openfox_private.emit_conversation_event 增加对 overrides_version 变化的判断，使覆盖设置的修改也会写
--     conversation.updated 事件（payload 不变），其他客户端据此刷新。其余逻辑与 000029 完全一致。
-- RLS：不新增表；已有表的策略不变，新列随表继承。
-- 锁：ADD COLUMN（可空 / 常量默认值）只改目录，瞬间完成，但需要短暂的 ACCESS EXCLUSIVE 锁（conversations、runs）；
--   所有新增 CHECK 都是 NOT VALID 再 VALIDATE：ADD CONSTRAINT 只短暂持 ACCESS EXCLUSIVE，
--   VALIDATE CONSTRAINT 只持 SHARE UPDATE EXCLUSIVE，不阻塞读写，但要顺序扫描全表一次（runs 行数大时低峰期执行）。
--   runs_wait_reason_valid 是 DROP + ADD NOT VALID + VALIDATE，在同一事务内完成，其他会话看不到中间状态。
--   CREATE OR REPLACE FUNCTION 不锁表。
-- 新旧版本共存：旧二进制不认识新列，INSERT 不写它们（runs 快照为 NULL → 读取时回退，conversations 覆盖为 NULL），
--   行为与升级前完全一致；旧二进制写入的 Run 没有快照，新二进制按配置版本解释。
-- 预检查询（执行前；两条都必须返回 0 行）：
--   SELECT DISTINCT approval_mode FROM openfox.agent_config_versions
--    WHERE approval_mode NOT IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS');
--   SELECT DISTINCT wait_reason FROM openfox.runs
--    WHERE wait_reason NOT IN ('', 'ORGANIZATION_CAPACITY', 'AGENT_CONCURRENCY', 'AGENT_CHAT_CONCURRENCY',
--          'COMPUTER_CONCURRENCY', 'CONVERSATION_BUSY', 'WORKDIR_BUSY', 'AWAITING_COMPUTER',
--          'COMPUTER_OFFLINE', 'TASK_GATE', 'OFFER_EXPIRED');
-- 验证查询（执行后）：
--   SELECT count(*) FROM information_schema.columns WHERE table_schema = 'openfox'
--    AND ((table_name = 'conversations' AND column_name IN ('runtime_override', 'model_override', 'approval_mode_override', 'overrides_version'))
--      OR (table_name = 'runs' AND column_name IN ('runtime', 'model', 'approval_mode')));            -- 期望 7
--   SELECT conname, convalidated FROM pg_constraint WHERE conrelid IN ('openfox.conversations'::regclass, 'openfox.runs'::regclass,
--          'openfox.agent_config_versions'::regclass) AND conname IN ('conversations_overrides_valid', 'conversations_overrides_not_crew',
--          'runs_snapshot_valid', 'runs_wait_reason_valid', 'agent_config_versions_approval_mode_modes');  -- 期望 5 行且全部 convalidated = true
-- 回滚：应用回退到旧二进制即可（旧版忽略新列）；新列闲置不影响任何功能，因此不建议执行下面的删除语句。
--   确需移除时，先回退应用，并确认没有 Run 处于 RUNTIME_UNAVAILABLE 等待；然后按下面顺序在一个事务里执行
--   （第 1 步必须最先做：新版触发器函数引用了 overrides_version，列被删除后触发器会报错）：
--   1) CREATE OR REPLACE FUNCTION openfox_private.emit_conversation_event() ... 恢复 000029 中的定义
--      （比较列表去掉 overrides_version，其余完全相同）；
--   2) ALTER TABLE openfox.runs DROP CONSTRAINT runs_snapshot_valid, DROP COLUMN runtime, DROP COLUMN model, DROP COLUMN approval_mode;
--   3) ALTER TABLE openfox.conversations DROP CONSTRAINT conversations_overrides_valid, DROP CONSTRAINT conversations_overrides_not_crew,
--          DROP COLUMN runtime_override, DROP COLUMN model_override, DROP COLUMN approval_mode_override, DROP COLUMN overrides_version;
--   4) ALTER TABLE openfox.agent_config_versions DROP CONSTRAINT agent_config_versions_approval_mode_modes;
--   5) UPDATE openfox.runs SET wait_reason = 'COMPUTER_OFFLINE' WHERE wait_reason = 'RUNTIME_UNAVAILABLE';
--   6) ALTER TABLE openfox.runs DROP CONSTRAINT runs_wait_reason_valid;，再按 000032 的取值重新 ADD（不含 RUNTIME_UNAVAILABLE）。

SET ROLE openfox_migrator;

-- 1. conversations：覆盖设置
ALTER TABLE openfox.conversations
    ADD COLUMN runtime_override text,
    ADD COLUMN model_override text,
    ADD COLUMN approval_mode_override text,
    ADD COLUMN overrides_version integer NOT NULL DEFAULT 0;
ALTER TABLE openfox.conversations
    ADD CONSTRAINT conversations_overrides_valid CHECK (
        (runtime_override IS NULL OR runtime_override IN ('codex', 'claude-code', 'opencode', 'pi'))
        AND (model_override IS NULL OR (char_length(model_override) BETWEEN 1 AND 128
                                        AND model_override !~ '[\u0001-\u001f\u007f-\u009f]'))
        AND (approval_mode_override IS NULL
             OR approval_mode_override IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS'))
        AND overrides_version >= 0
    ) NOT VALID,
    ADD CONSTRAINT conversations_overrides_not_crew CHECK (
        crew_id IS NULL OR (runtime_override IS NULL AND model_override IS NULL AND approval_mode_override IS NULL)
    ) NOT VALID;
ALTER TABLE openfox.conversations VALIDATE CONSTRAINT conversations_overrides_valid;
ALTER TABLE openfox.conversations VALIDATE CONSTRAINT conversations_overrides_not_crew;

-- 2. runs：创建时写入的有效配置快照
ALTER TABLE openfox.runs
    ADD COLUMN runtime text,
    ADD COLUMN model text,
    ADD COLUMN approval_mode text;
ALTER TABLE openfox.runs
    ADD CONSTRAINT runs_snapshot_valid CHECK (
        ((runtime IS NULL) = (model IS NULL)) AND ((runtime IS NULL) = (approval_mode IS NULL))
        AND (runtime IS NULL OR (length(btrim(runtime)) BETWEEN 1 AND 64 AND length(btrim(model)) BETWEEN 1 AND 128
                                 AND approval_mode IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS')))
    ) NOT VALID;
ALTER TABLE openfox.runs VALIDATE CONSTRAINT runs_snapshot_valid;

-- 3. Agent 配置的默认权限模式限定为四档（旧的正则约束保留，新约束只会更严）
ALTER TABLE openfox.agent_config_versions
    ADD CONSTRAINT agent_config_versions_approval_mode_modes
        CHECK (approval_mode IN ('FOLLOW_RUNTIME', 'READ_ONLY', 'WORKSPACE_WRITE', 'FULL_ACCESS')) NOT VALID;
ALTER TABLE openfox.agent_config_versions VALIDATE CONSTRAINT agent_config_versions_approval_mode_modes;

-- 4. 等待原因：选定的 Runtime 在电脑上不可运行
ALTER TABLE openfox.runs DROP CONSTRAINT runs_wait_reason_valid;
ALTER TABLE openfox.runs ADD CONSTRAINT runs_wait_reason_valid CHECK (
    wait_reason IN ('', 'ORGANIZATION_CAPACITY', 'AGENT_CONCURRENCY', 'AGENT_CHAT_CONCURRENCY', 'COMPUTER_CONCURRENCY',
                    'CONVERSATION_BUSY', 'WORKDIR_BUSY', 'AWAITING_COMPUTER',
                    'COMPUTER_OFFLINE', 'TASK_GATE', 'OFFER_EXPIRED', 'RUNTIME_UNAVAILABLE')
) NOT VALID;
ALTER TABLE openfox.runs VALIDATE CONSTRAINT runs_wait_reason_valid;

-- 5. 覆盖设置变化也写 conversation.updated（其余与 000029 一致）
CREATE OR REPLACE FUNCTION openfox_private.emit_conversation_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF (NEW.lifecycle, NEW.handoff, NEW.title, NEW.archived_at, NEW.task_id, NEW.visibility, NEW.overrides_version)
       IS NOT DISTINCT FROM
       (OLD.lifecycle, OLD.handoff, OLD.title, OLD.archived_at, OLD.task_id, OLD.visibility, OLD.overrides_version) THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.organization_events (
        organization_id, type, entity, entity_id, version, space_id, conversation_id, payload
    ) VALUES (
        NEW.organization_id, 'conversation.updated', 'conversation', NEW.id, NEW.version,
        NEW.space_id, NEW.id,
        jsonb_build_object('lifecycle', NEW.lifecycle, 'handoff', NEW.handoff)
    );
    RETURN NULL;
END
$$;

RESET ROLE;
