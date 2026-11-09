-- 阶段 1「任务核心」：任务日期、父子任务、手动排序键、软删除，以及多处理人、关注者、链接、表情。
--
-- 仅扩展（expand-only），不改写已有数据：
--   - tasks 新增列全部可空；priority 约束放宽为超集（新增 NONE），旧值仍然合法。
--   - tasks_space_identity_unique 只是给已唯一的 (organization_id, id) 加上 space_id 前缀，
--     不会因已有数据失败；它让子表可以用复合外键保证「行与任务同属一个空间」。
--   - rank 为空的旧任务按 'h' || lpad(sequence, 12, '0') 参与排序（应用与索引使用同一表达式），
--     因此无需回填；拖动时只写被拖动的那一行。
--   - 多处理人存在 task_assignees；tasks.assignee_id / executor_agent_id 继续作为「第一位人员 /
--     第一个 Agent」投影，由应用在同一事务里维护，旧接口不受影响。已有任务不回填：
--     没有 task_assignees 行时，应用从投影列推导处理人（回填需绕过 FORCE RLS，风险高于收益）。
--   - 删除任务只写 deleted_at / deleted_by（墓碑），评论、动态、会话和执行记录的外键保持有效；
--     应用的所有任务查询都过滤 deleted_at IS NULL。
-- 迁移运行器在事务内执行每个文件，无法使用 CONCURRENTLY；表为控制面数据、规模小，
-- CREATE INDEX 与约束校验期间会短暂阻塞 tasks 写入。
-- 回滚：应用回退后新列和新表闲置即可；确需移除时见 db/migrations/README.md 的 000025 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.tasks
    ADD COLUMN start_date date,
    ADD COLUMN due_date date,
    ADD COLUMN parent_task_id uuid,
    ADD COLUMN rank text COLLATE "C",
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid;

ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_space_identity_unique UNIQUE (organization_id, space_id, id);

ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_parent_same_space FOREIGN KEY (organization_id, space_id, parent_task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    ADD CONSTRAINT tasks_deleted_by_member FOREIGN KEY (organization_id, deleted_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    ADD CONSTRAINT tasks_parent_not_self CHECK (parent_task_id IS NULL OR parent_task_id <> id),
    ADD CONSTRAINT tasks_dates_ordered CHECK (start_date IS NULL OR due_date IS NULL OR start_date <= due_date),
    ADD CONSTRAINT tasks_rank_valid CHECK (rank IS NULL OR rank ~ '^[0-9a-z]{1,64}$'),
    ADD CONSTRAINT tasks_deleted_consistent CHECK ((deleted_at IS NULL) = (deleted_by IS NULL)),
    DROP CONSTRAINT tasks_priority_valid,
    ADD CONSTRAINT tasks_priority_valid CHECK (
        priority IN ('NONE', 'LOW', 'NORMAL', 'HIGH', 'URGENT')
    );

CREATE INDEX tasks_parent_idx
    ON openfox.tasks (organization_id, parent_task_id)
    WHERE parent_task_id IS NOT NULL AND deleted_at IS NULL;

-- 看板按状态分列、列内按手动排序键排列。
CREATE INDEX tasks_board_idx
    ON openfox.tasks (
        organization_id, space_id, status_id,
        (COALESCE(rank, 'h' || lpad(sequence::text, 12, '0')) COLLATE "C"), id
    )
    WHERE deleted_at IS NULL;

-- 多处理人：每行指向恰好一个成员、Agent 或小队。
CREATE TABLE openfox.task_assignees (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    user_id uuid,
    agent_id uuid,
    crew_id uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, crew_id)
        REFERENCES openfox.agent_crews (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_assignees_target_valid CHECK (
        (kind = 'HUMAN' AND user_id IS NOT NULL AND agent_id IS NULL AND crew_id IS NULL)
        OR (kind = 'AGENT' AND agent_id IS NOT NULL AND user_id IS NULL AND crew_id IS NULL)
        OR (kind = 'CREW' AND crew_id IS NOT NULL AND user_id IS NULL AND agent_id IS NULL)
    )
);

CREATE UNIQUE INDEX task_assignees_target_unique
    ON openfox.task_assignees (organization_id, task_id, kind, COALESCE(user_id, agent_id, crew_id));
CREATE INDEX task_assignees_user_idx
    ON openfox.task_assignees (organization_id, user_id, task_id)
    WHERE user_id IS NOT NULL;
CREATE INDEX task_assignees_agent_idx
    ON openfox.task_assignees (organization_id, agent_id, task_id)
    WHERE agent_id IS NOT NULL;
CREATE INDEX task_assignees_crew_idx
    ON openfox.task_assignees (organization_id, crew_id, task_id)
    WHERE crew_id IS NOT NULL;

-- 关注者：成员或 Agent。评论通知按成员关注者扇出。
CREATE TABLE openfox.task_followers (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    user_id uuid,
    agent_id uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_followers_target_valid CHECK (
        (kind = 'HUMAN' AND user_id IS NOT NULL AND agent_id IS NULL)
        OR (kind = 'AGENT' AND agent_id IS NOT NULL AND user_id IS NULL)
    )
);

CREATE UNIQUE INDEX task_followers_target_unique
    ON openfox.task_followers (organization_id, task_id, kind, COALESCE(user_id, agent_id));
CREATE INDEX task_followers_user_idx
    ON openfox.task_followers (organization_id, user_id, task_id)
    WHERE user_id IS NOT NULL;

-- 外部链接（「添加链接」）。
CREATE TABLE openfox.task_links (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    url text NOT NULL,
    title text NOT NULL DEFAULT '',
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_links_url_valid CHECK (url ~ '^https?://' AND length(url) <= 2048),
    CONSTRAINT task_links_title_length CHECK (length(title) <= 200),
    CONSTRAINT task_links_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX task_links_url_unique ON openfox.task_links (organization_id, task_id, url);

-- 表情回应：comment_id 为空表示回应任务描述。
CREATE TABLE openfox.task_reactions (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    comment_id uuid,
    user_id uuid NOT NULL,
    emoji text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, comment_id)
        REFERENCES openfox.task_comments (organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_reactions_emoji_valid CHECK (length(btrim(emoji)) BETWEEN 1 AND 32)
);

CREATE UNIQUE INDEX task_reactions_unique
    ON openfox.task_reactions (
        organization_id, task_id,
        COALESCE(comment_id, '00000000-0000-0000-0000-000000000000'::uuid),
        user_id, emoji
    );

ALTER TABLE openfox.task_assignees ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_assignees FORCE ROW LEVEL SECURITY;
CREATE POLICY task_assignees_tenant_isolation ON openfox.task_assignees
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_followers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_followers FORCE ROW LEVEL SECURITY;
CREATE POLICY task_followers_tenant_isolation ON openfox.task_followers
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_links FORCE ROW LEVEL SECURITY;
CREATE POLICY task_links_tenant_isolation ON openfox.task_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_reactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_reactions FORCE ROW LEVEL SECURITY;
CREATE POLICY task_reactions_tenant_isolation ON openfox.task_reactions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT SELECT, INSERT, DELETE ON openfox.task_assignees, openfox.task_followers, openfox.task_reactions TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.task_links TO openfox_app;
