-- 首页「小助手」：expand-only，支持内置助手与对话内草稿卡。
-- 1. agents.builtin_kind：标记内置助手（目前只有 ASSISTANT）。每个成员在一个组织内最多一个，
--    必须是本人私有的 Agent；内置助手不参与“同空间 Agent 名称唯一”，避免多人的“小助手”互相冲突。
-- 2. conversations.title / archived_at：会话重命名与归档，已有会话均为空（无标题、未归档）。
-- 3. messages.kind / payload：助手回复里的结构化草稿（ACTION_PROPOSAL）与执行结果（ACTION_EXECUTED）。
--    已有消息全部是 TEXT，payload 为空。
-- 不改写已有数据，不改 GRANT 和 RLS：openfox_app 对 agents 已有表级 SELECT/INSERT（UPDATE 限定列，
-- 不含 builtin_kind，所以内置标记写入后应用改不掉），对 messages 已有表级 SELECT/INSERT/UPDATE。
-- 回滚：应用回退后新列闲置即可；确需移除时见 db/migrations/README.md。

SET ROLE openfox_migrator;

ALTER TABLE openfox.agents ADD COLUMN builtin_kind text;
ALTER TABLE openfox.agents ADD CONSTRAINT agents_builtin_kind_valid
    CHECK (builtin_kind IS NULL
           OR (builtin_kind = 'ASSISTANT' AND visibility = 'PRIVATE' AND owner_user_id IS NOT NULL));

CREATE UNIQUE INDEX agents_builtin_assistant_unique
    ON openfox.agents (organization_id, owner_user_id)
    WHERE builtin_kind = 'ASSISTANT';

-- 名称唯一索引改为排除内置助手：先建新索引再删旧索引，最后沿用原名。
CREATE UNIQUE INDEX agents_space_name_unique_next
    ON openfox.agents (organization_id, space_id, lower(name))
    WHERE space_id IS NOT NULL AND archived_at IS NULL AND builtin_kind IS NULL;
DROP INDEX openfox.agents_space_name_unique;
ALTER INDEX openfox.agents_space_name_unique_next RENAME TO agents_space_name_unique;

-- 会话选择器支持重命名、归档与恢复。归档只是从列表收起，不改 lifecycle。
ALTER TABLE openfox.conversations
    ADD COLUMN title text,
    ADD COLUMN archived_at timestamptz;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_title_valid
    CHECK (title IS NULL OR (length(btrim(title)) > 0 AND length(title) <= 200));

ALTER TABLE openfox.messages
    ADD COLUMN kind text NOT NULL DEFAULT 'TEXT',
    ADD COLUMN payload jsonb;
ALTER TABLE openfox.messages ADD CONSTRAINT messages_kind_valid
    CHECK (kind IN ('TEXT', 'ACTION_PROPOSAL', 'ACTION_EXECUTED'));
ALTER TABLE openfox.messages ADD CONSTRAINT messages_payload_shape_valid
    CHECK ((kind = 'TEXT' AND payload IS NULL)
           OR (kind <> 'TEXT' AND author_kind = 'AGENT' AND jsonb_typeof(payload) = 'object'
               AND octet_length(payload::text) <= 65536));

RESET ROLE;
