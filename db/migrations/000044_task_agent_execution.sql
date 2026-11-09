-- 任务指派给 Agent 自动执行与 Agent 任务工具。
--
-- 仅扩展（expand-only）：
--   - conversations.origin：USER（默认，原有会话）/ TASK_ASSIGNMENT（指派触发自动创建）。
--     同一任务 + Agent 只保留一个未关闭、未归档的指派会话（部分唯一索引，只约束 TASK_ASSIGNMENT，原有数据不受影响）。
--   - task_agent_events：任务写事务里新增 / 移除 Agent 处理人时写入的待处理事件，由 dispatch worker 处理：
--     ASSIGNED → 以指派人身份找到或创建指派会话并发一条消息给 Agent；UNASSIGNED → 取消该 Agent 在该任务上的活跃运行。
--   - task_comments：支持 Agent 作者（Agent 任务工具写评论）。author_user_id 改为可空，新增 author_agent_id / run_id，
--     CHECK 恰有一个作者；原有行都有用户作者，约束直接校验通过。
--   - list_dispatch_organizations 增加「有待处理指派事件」的组织，worker 每轮能扫到。
--   - enqueue_automation_comment 跳过 Agent 作者的评论（避免 Agent 评论触发自动化再唤醒 Agent 的循环）。
-- 锁：conversations / task_comments 加可空列与 CHECK（扫描表校验，行数有限）；去掉 NOT NULL 只改目录。
-- 回滚：应用回退后新表与新列闲置即可。注意旧版本按非空读取 task_comments.author_user_id，
--   回退前须先删除 Agent 作者的评论（DELETE FROM openfox.task_comments WHERE author_agent_id IS NOT NULL），
--   再视需要恢复 NOT NULL；详见 db/migrations/README.md 的 000044 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations
    ADD COLUMN origin text NOT NULL DEFAULT 'USER',
    ADD CONSTRAINT conversations_origin_valid CHECK (origin IN ('USER', 'TASK_ASSIGNMENT')),
    ADD CONSTRAINT conversations_task_origin_requires_task CHECK (origin <> 'TASK_ASSIGNMENT' OR task_id IS NOT NULL);

CREATE UNIQUE INDEX conversations_task_assignment_open_unique
    ON openfox.conversations (organization_id, task_id, agent_id)
    WHERE origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN' AND archived_at IS NULL;

CREATE TABLE openfox.task_agent_events (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processed_at timestamptz,
    outcome text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    CONSTRAINT task_agent_events_kind_valid CHECK (kind IN ('ASSIGNED', 'UNASSIGNED')),
    CONSTRAINT task_agent_events_outcome_length CHECK (char_length(outcome) <= 200),
    CONSTRAINT task_agent_events_attempts_nonnegative CHECK (attempts >= 0)
);
CREATE INDEX task_agent_events_pending_idx
    ON openfox.task_agent_events (organization_id, created_at, id) WHERE processed_at IS NULL;

ALTER TABLE openfox.task_agent_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_agent_events FORCE ROW LEVEL SECURITY;
CREATE POLICY task_agent_events_tenant_isolation ON openfox.task_agent_events
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_agent_events_scan ON openfox.task_agent_events FOR SELECT TO openfox_migrator USING (true);
GRANT SELECT, INSERT, UPDATE ON openfox.task_agent_events TO openfox_app;

ALTER TABLE openfox.task_comments
    ALTER COLUMN author_user_id DROP NOT NULL,
    ADD COLUMN author_agent_id uuid,
    ADD COLUMN run_id uuid,
    ADD CONSTRAINT task_comments_author_agent_fk
        FOREIGN KEY (organization_id, author_agent_id) REFERENCES openfox.agents (organization_id, id),
    ADD CONSTRAINT task_comments_author_shape CHECK (
        (author_user_id IS NOT NULL AND author_agent_id IS NULL AND run_id IS NULL)
        OR (author_user_id IS NULL AND author_agent_id IS NOT NULL AND run_id IS NOT NULL)
    );

-- Agent 任务工具写的评论不触发评论自动化，避免 Agent 评论 → 自动化 → Agent 运行的循环（其余与 000040 相同）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_automation_comment()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.automation_events (organization_id, kind, space_id, task_id, comment_id, actor_user_id)
    SELECT n.organization_id, 'comment', n.space_id, n.task_id, n.id, n.author_user_id
      FROM changed_new n
     WHERE n.author_agent_id IS NULL
       AND EXISTS (SELECT 1 FROM openfox.automation_triggers t
                    WHERE t.organization_id = n.organization_id AND t.space_id = n.space_id
                      AND t.kind = 'comment' AND t.enabled);
    RETURN NULL;
END
$$;

-- 与 000020 起的版本相同，只在 work 里多一项待处理的指派事件。
CREATE OR REPLACE FUNCTION openfox_private.list_dispatch_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid dispatch organization limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    WITH work AS (
        SELECT i.organization_id FROM openfox.conversation_inputs i WHERE i.status = 'PENDING'
        UNION
        SELECT r.organization_id FROM openfox.runs r WHERE r.state = 'QUEUED'
        UNION
        SELECT o.organization_id FROM openfox.dispatch_offers o
         WHERE o.status = 'LIVE' AND o.expires_at <= clock_timestamp()
        UNION
        SELECT r.organization_id FROM openfox.runs r
         WHERE r.lease_expires_at IS NOT NULL
           AND r.lease_expires_at <= clock_timestamp()
           AND r.reconciliation_required = false
           AND r.state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
        UNION
        SELECT c.organization_id FROM openfox.capacity_reservations c
          LEFT JOIN openfox.runs r ON r.organization_id = c.organization_id AND r.id = c.run_id
         WHERE c.status IN ('RESERVED', 'CLAIMED')
           AND c.created_at <= clock_timestamp() - interval '1 minute'
           AND (r.id IS NULL
                OR r.state IN ('SUCCEEDED', 'FAILED', 'CANCELED')
                OR r.reservation_id IS DISTINCT FROM c.id)
        UNION
        SELECT e.organization_id FROM openfox.task_agent_events e WHERE e.processed_at IS NULL
    )
    SELECT w.organization_id
      FROM work w
     ORDER BY (p_after IS NOT NULL AND w.organization_id <= p_after), w.organization_id
     LIMIT p_limit;
END
$$;

RESET ROLE;
