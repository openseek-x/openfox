-- 首页「全部 Agent 和小队」：expand-only。
-- 1. agent_crews / agent_crew_members：小队是同一空间内若干 Agent 的命名分组，
--    可见性与归档语义和 agents 一致（PRIVATE 仅创建人可见，archived_at 非空即已归档）。
--    本迁移只建目录对象，小队如何承接任务（调度）不在本期范围。
-- 2. agent_directory_pins：成员个人的置顶，只能指向一个 Agent 或一个小队；
--    RLS 限定只能读写自己的行（与 notifications 收件人策略同一做法）。
-- 只新建表与索引，不改写已有数据，不放宽已有 GRANT 或 RLS 策略。
-- 回滚：应用回退后新表闲置即可；确需移除时按 pins → crew_members → crews 的顺序 DROP TABLE。

SET ROLE openfox_migrator;

CREATE TABLE openfox.agent_crews (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    visibility text NOT NULL DEFAULT 'SHARED',
    archived_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    CONSTRAINT agent_crews_space_fk
        FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    CONSTRAINT agent_crews_owner_fk
        FOREIGN KEY (organization_id, owner_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_crews_name_valid
        CHECK (length(btrim(name)) > 0 AND length(name) <= 255),
    CONSTRAINT agent_crews_description_length
        CHECK (length(description) <= 10000),
    CONSTRAINT agent_crews_visibility_valid
        CHECK (visibility IN ('PRIVATE', 'SHARED')),
    CONSTRAINT agent_crews_version_positive
        CHECK (version > 0)
);

CREATE UNIQUE INDEX agent_crews_space_name_unique
    ON openfox.agent_crews (organization_id, space_id, lower(name))
    WHERE archived_at IS NULL;
CREATE INDEX agent_crews_listing_idx
    ON openfox.agent_crews (organization_id, space_id, archived_at, created_at, id);

CREATE TABLE openfox.agent_crew_members (
    organization_id uuid NOT NULL,
    crew_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    position integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, crew_id, agent_id),
    CONSTRAINT agent_crew_members_crew_fk
        FOREIGN KEY (organization_id, crew_id)
        REFERENCES openfox.agent_crews (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_crew_members_agent_fk
        FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT agent_crew_members_position_valid
        CHECK (position >= 0 AND position < 64)
);

CREATE INDEX agent_crew_members_agent_idx
    ON openfox.agent_crew_members (organization_id, agent_id);

CREATE TABLE openfox.agent_directory_pins (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid,
    crew_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT agent_directory_pins_user_fk
        FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_directory_pins_agent_fk
        FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT agent_directory_pins_crew_fk
        FOREIGN KEY (organization_id, crew_id)
        REFERENCES openfox.agent_crews (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_directory_pins_one_target
        CHECK ((agent_id IS NULL) <> (crew_id IS NULL))
);

CREATE UNIQUE INDEX agent_directory_pins_agent_unique
    ON openfox.agent_directory_pins (organization_id, user_id, agent_id)
    WHERE agent_id IS NOT NULL;
CREATE UNIQUE INDEX agent_directory_pins_crew_unique
    ON openfox.agent_directory_pins (organization_id, user_id, crew_id)
    WHERE crew_id IS NOT NULL;

ALTER TABLE openfox.agent_crews ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_crews FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_crew_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_crew_members FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_directory_pins ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_directory_pins FORCE ROW LEVEL SECURITY;

CREATE POLICY agent_crews_tenant_isolation ON openfox.agent_crews
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY agent_crew_members_tenant_isolation ON openfox.agent_crew_members
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 置顶是个人偏好：只有本人能读写自己的行。
CREATE POLICY agent_directory_pins_owner ON openfox.agent_directory_pins
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

RESET ROLE;

GRANT SELECT, INSERT, UPDATE (name, description, visibility, archived_at, version, updated_at)
    ON openfox.agent_crews TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.agent_crew_members TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.agent_directory_pins TO openfox_app;
