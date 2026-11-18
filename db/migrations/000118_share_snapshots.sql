-- 阶段 12g「分享快照」（M-11）：对话或评论线程在创建时刻的不可变文本副本，可公开或限组织，可撤销。
--
-- 仅扩展（expand-only），内容不依赖迁移编号：
--   - organizations.allow_public_sharing：组织级「允许公开分享」开关，默认 false。关闭时不能创建公开快照，
--     已有公开快照的访问返回 404（重新打开后恢复）；限组织分享不受影响。
--   - share_snapshots：快照本体。content 是创建时刻的纯文本副本（jsonb），不引用来源对象，来源之后修改或删除都不影响；
--     不含附件与产物。token_hash 是 32 字节随机访问令牌的 SHA-256，明文令牌不入库、只在创建响应里出现一次。
--     组织复合外键 + 强制 RLS；应用角色只有 SELECT / INSERT 与 revoked_at、revoked_by 两列的 UPDATE，
--     没有 DELETE，内容列创建后不可改；触发器保证撤销后不可恢复。
--   - openfox_private.resolve_share_snapshot：匿名或登录用户凭令牌哈希读取快照的唯一入口（SECURITY DEFINER）。
--     令牌不存在、已撤销、已过期、组织已删除、公开分享被关闭、限组织但访问者不是该组织 ACTIVE 成员，一律返回零行，
--     调用方统一回 404，不泄露存在性。
-- 锁：organizations 加常量默认值的列，不重写表；其余只建新对象。
-- 回滚：应用回退后新列、新表闲置即可；不提供 down 迁移。

SET ROLE openfox_migrator;

ALTER TABLE openfox.organizations
    ADD COLUMN allow_public_sharing boolean NOT NULL DEFAULT false;

CREATE TABLE openfox.share_snapshots (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    kind text NOT NULL,
    scope text NOT NULL,
    title text NOT NULL,
    content jsonb NOT NULL,
    item_count integer NOT NULL,
    creator_name text NOT NULL DEFAULT '',
    token_hash bytea NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    revoked_at timestamptz,
    revoked_by uuid,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, revoked_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT share_snapshots_kind_valid CHECK (kind IN ('CONVERSATION', 'COMMENT')),
    CONSTRAINT share_snapshots_scope_valid CHECK (scope IN ('PUBLIC', 'ORGANIZATION')),
    CONSTRAINT share_snapshots_title_valid CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT share_snapshots_content_shape CHECK (
 jsonb_typeof(content) = 'object' AND octet_length(content::text) BETWEEN 1 AND 262144),
    CONSTRAINT share_snapshots_item_count_valid CHECK (item_count BETWEEN 1 AND 1000),
    CONSTRAINT share_snapshots_creator_name_valid CHECK (char_length(creator_name) <= 80),
    CONSTRAINT share_snapshots_token_hash_size CHECK (octet_length(token_hash) = 32),
    CONSTRAINT share_snapshots_revoke_shape CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    CONSTRAINT share_snapshots_expiry_after_create CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE UNIQUE INDEX share_snapshots_token_hash_unique ON openfox.share_snapshots (token_hash);
CREATE INDEX share_snapshots_creator_idx ON openfox.share_snapshots (organization_id, created_by, created_at DESC);
CREATE INDEX share_snapshots_org_idx ON openfox.share_snapshots (organization_id, created_at DESC);

ALTER TABLE openfox.share_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.share_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY share_snapshots_tenant_isolation ON openfox.share_snapshots
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 访问者在建立租户上下文之前凭令牌哈希查找；沿用 invite_links 的做法，只给 migrator 只读策略。
CREATE POLICY share_snapshots_token_lookup ON openfox.share_snapshots
    FOR SELECT TO openfox_migrator USING (true);

-- 撤销不可恢复：已撤销的行不允许再改。
CREATE OR REPLACE FUNCTION openfox_private.share_snapshots_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, openfox_private
AS $$
BEGIN
    IF OLD.revoked_at IS NOT NULL THEN
        RAISE EXCEPTION 'share snapshot is revoked' USING ERRCODE = 'check_violation',
     CONSTRAINT = 'share_snapshots_revoked_immutable';
    END IF;
    RETURN NEW;
END
$$;
ALTER FUNCTION openfox_private.share_snapshots_guard() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.share_snapshots_guard() FROM PUBLIC;
CREATE TRIGGER share_snapshots_guard BEFORE UPDATE ON openfox.share_snapshots
    FOR EACH ROW EXECUTE FUNCTION openfox_private.share_snapshots_guard();

-- 凭令牌哈希解析快照。会装好租户上下文读组织与成员关系，结束前清除；失败条件一律零行。
CREATE OR REPLACE FUNCTION openfox_private.resolve_share_snapshot(p_token_hash bytea, p_user_id uuid)
RETURNS TABLE (id uuid, kind text, scope text, title text, content jsonb, item_count integer,
 creator_name text, organization_name text, created_at timestamptz, expires_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    snap openfox.share_snapshots%ROWTYPE;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32 THEN
        RETURN;
    END IF;
    SELECT * INTO snap FROM openfox.share_snapshots s WHERE s.token_hash = p_token_hash;
    IF NOT FOUND OR snap.revoked_at IS NOT NULL
       OR (snap.expires_at IS NOT NULL AND snap.expires_at <= clock_timestamp()) THEN
        RETURN;
    END IF;
    PERFORM openfox_private.set_tenant_context(snap.organization_id,
        '00000000-0000-0000-0000-000000000000', 1, 'organization_worker');
    RETURN QUERY
    SELECT snap.id, snap.kind, snap.scope, snap.title, snap.content, snap.item_count,
           snap.creator_name, o.name, snap.created_at, snap.expires_at
      FROM openfox.organizations o
     WHERE o.id = snap.organization_id AND o.deleted_at IS NULL
AND ((snap.scope = 'PUBLIC' AND o.allow_public_sharing)
            OR (snap.scope = 'ORGANIZATION' AND p_user_id IS NOT NULL AND EXISTS (
         SELECT 1 FROM openfox.organization_members m
   WHERE m.organization_id = o.id AND m.user_id = p_user_id AND m.status = 'ACTIVE')));
    TRUNCATE TABLE pg_temp.openfox_tenant_context;
END
$$;
ALTER FUNCTION openfox_private.resolve_share_snapshot(bytea, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_share_snapshot(bytea, uuid) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT ON openfox.share_snapshots TO openfox_app;
GRANT UPDATE (revoked_at, revoked_by) ON openfox.share_snapshots TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_share_snapshot(bytea, uuid) TO openfox_app;
