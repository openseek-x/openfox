-- C03 release integration: retain deployed 000118 table, COMMENT support,
-- immutable snapshot content and all permissions. Deployment preflight verified
-- share_snapshots is empty; refuse to invent encrypted tokens for historical hashes.
SET ROLE openfox_migrator;
LOCK TABLE openfox.share_snapshots IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM openfox.share_snapshots) THEN
  RAISE EXCEPTION 'recoverable share-token migration requires an empty snapshot table';
 END IF;
END $$;
ALTER TABLE openfox.share_snapshots
 ADD COLUMN token_ciphertext bytea NOT NULL CHECK (octet_length(token_ciphertext)=59),
 ADD COLUMN token_nonce bytea NOT NULL CHECK (octet_length(token_nonce)=12),
 ADD COLUMN token_key_id text NOT NULL CHECK (token_key_id ~ '^[0-9a-f]{16}$');

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
    snap record;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32 THEN
        RETURN;
    END IF;
    SELECT s.organization_id, s.id, s.kind, s.scope, s.title, s.content, s.item_count,
           s.creator_name, s.created_at, s.expires_at, s.revoked_at
      INTO snap FROM openfox.share_snapshots s WHERE s.token_hash = p_token_hash;
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
