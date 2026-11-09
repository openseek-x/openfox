-- Expired invitation acceptance must return INVITATION_CONFLICT, not an RLS error.
-- Replaces only the existing function body; signature, owner, grants, security
-- mode and successful acceptance rules remain unchanged. No table/data changes.
-- Apply after 000066 using the normal transactional migration runner.
-- Rollback, if necessary: restore only accept_invitation from 000037 in a new
-- forward migration. That restores the expired-link error; no data undo is needed.

SET ROLE openfox_migrator;

CREATE OR REPLACE FUNCTION openfox_private.accept_invitation(
    p_invitation_id uuid,
    p_token_hash bytea,
    p_actor_id uuid DEFAULT NULL,
    p_password_hash text DEFAULT NULL
) RETURNS TABLE (
    user_id uuid,
    organization_id uuid,
    role text,
    status text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
-- 输出列 user_id / organization_id / role / status 与表列同名；000005 的版本在已登录接受时因此报
-- "column reference is ambiguous"。这里统一按表列解析（函数内不把输出列当变量使用）。
#variable_conflict use_column
DECLARE
    invitation_row openfox.invitations%ROWTYPE;
    target_user_id uuid;
    credential_status text;
    membership_status text;
BEGIN
    PERFORM openfox_private.set_invitation_lookup(p_invitation_id, p_token_hash);

    IF p_invitation_id IS NULL
       OR p_token_hash IS NULL
       OR octet_length(p_token_hash) <> 32 THEN
        RAISE EXCEPTION 'invitation_not_found' USING ERRCODE = 'P0002';
    END IF;

    SELECT *
      INTO invitation_row
      FROM openfox.invitations
     WHERE id = p_invitation_id
       AND token_hash = p_token_hash
     FOR UPDATE;

    IF NOT FOUND OR EXISTS (
        SELECT 1 FROM openfox.organization_directory d
         WHERE d.organization_id = invitation_row.organization_id AND d.deleted_at IS NOT NULL) THEN
        RAISE EXCEPTION 'invitation_not_found' USING ERRCODE = 'P0002';
    END IF;

    IF invitation_row.status <> 'PENDING' THEN
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    END IF;

    IF invitation_row.expires_at <= clock_timestamp() THEN
        -- No tenant context exists yet. Reject without writing: a write here
        -- violates FORCE RLS, and the following exception would roll it back anyway.
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    END IF;

    IF p_actor_id IS NULL THEN
        SELECT id
          INTO target_user_id
          FROM openfox.users
         WHERE normalized_email = invitation_row.normalized_email
         FOR UPDATE;

        IF FOUND THEN
            RAISE EXCEPTION 'invitation_auth_required' USING ERRCODE = 'P0001';
        END IF;

        IF p_password_hash IS NULL OR btrim(p_password_hash) = '' THEN
            RAISE EXCEPTION 'invitation_password_required' USING ERRCODE = 'P0001';
        END IF;

        INSERT INTO openfox.users (email)
        VALUES (invitation_row.normalized_email)
        ON CONFLICT (normalized_email) DO NOTHING
        RETURNING id INTO target_user_id;

        IF target_user_id IS NULL THEN
            SELECT id
              INTO target_user_id
              FROM openfox.users
             WHERE normalized_email = invitation_row.normalized_email
             FOR UPDATE;
            RAISE EXCEPTION 'invitation_auth_required' USING ERRCODE = 'P0001';
        END IF;

        INSERT INTO openfox.password_credentials (user_id, password_hash)
        VALUES (target_user_id, p_password_hash)
        ON CONFLICT (user_id) DO NOTHING;

        SELECT status
          INTO credential_status
          FROM openfox.password_credentials
         WHERE user_id = target_user_id
         FOR UPDATE;
        IF credential_status IS DISTINCT FROM 'ACTIVE' THEN
            RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
        END IF;
    ELSE
        SELECT id
          INTO target_user_id
          FROM openfox.users
         WHERE normalized_email = invitation_row.normalized_email
         FOR UPDATE;

        IF NOT FOUND OR target_user_id <> p_actor_id THEN
            RAISE EXCEPTION 'invitation_email_mismatch' USING ERRCODE = 'P0001';
        END IF;

        IF p_password_hash IS NOT NULL AND btrim(p_password_hash) <> '' THEN
            RAISE EXCEPTION 'invitation_parameter_conflict' USING ERRCODE = 'P0001';
        END IF;

        SELECT status
          INTO credential_status
          FROM openfox.password_credentials
         WHERE user_id = target_user_id
         FOR UPDATE;
        IF credential_status IS DISTINCT FROM 'ACTIVE' THEN
            RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
        END IF;
    END IF;

    PERFORM openfox_private.set_tenant_context(
        invitation_row.organization_id,
        target_user_id,
        1,
        'user_request'
    );

    SELECT m.status
      INTO membership_status
      FROM openfox.organization_members m
     WHERE m.organization_id = invitation_row.organization_id
       AND m.user_id = target_user_id
     FOR UPDATE;

    IF membership_status = 'ACTIVE' THEN
        RAISE EXCEPTION 'invitation_conflict' USING ERRCODE = 'P0001';
    ELSIF membership_status IN ('INVITED', 'REVOKED') THEN
        UPDATE openfox.organization_members m
           SET role = invitation_row.role,
               status = 'ACTIVE',
               authorization_version = m.authorization_version + 1
         WHERE m.organization_id = invitation_row.organization_id
           AND m.user_id = target_user_id;
    ELSE
        INSERT INTO openfox.organization_members (
            organization_id, user_id, role, status
        ) VALUES (
            invitation_row.organization_id, target_user_id, invitation_row.role, 'ACTIVE'
        );
    END IF;

    PERFORM openfox_private.join_invited_spaces(
        invitation_row.organization_id, target_user_id, invitation_row.space_ids);

    UPDATE openfox.invitations
       SET status = 'ACCEPTED', consumed_at = clock_timestamp()
     WHERE id = invitation_row.id AND status = 'PENDING';

    RETURN QUERY SELECT target_user_id, invitation_row.organization_id,
                        invitation_row.role, 'ACCEPTED'::text;
END
$$;

RESET ROLE;
