-- WeCom bots use the existing IM connection, bot, message, and outbox tables.
-- A private-chat code binds one external userid to one active organization member.
-- Apply after 000112; deploy the application after this migration.
SET ROLE openfox_migrator;

CREATE UNIQUE INDEX integration_connections_wecom_bot_unique
    ON openfox.integration_connections (provider, lower(external_id))
    WHERE provider = 'wecom' AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED';
CREATE UNIQUE INDEX integration_connections_wecom_user_unique
    ON openfox.integration_connections (organization_id, parent_connection_id, external_id)
    WHERE provider = 'wecom' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';
CREATE UNIQUE INDEX integration_connections_wecom_member_unique
    ON openfox.integration_connections (organization_id, parent_connection_id, owner_user_id)
    WHERE provider = 'wecom' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';

CREATE TABLE openfox.wecom_binding_codes (
    organization_id uuid NOT NULL,
    bot_id uuid NOT NULL,
    user_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, bot_id, user_id),
    FOREIGN KEY (organization_id, bot_id) REFERENCES openfox.im_bots (organization_id, id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT wecom_binding_codes_hash_valid CHECK (octet_length(code_hash) = 32),
    CONSTRAINT wecom_binding_codes_expiry_valid CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX wecom_binding_codes_hash_unique
    ON openfox.wecom_binding_codes (code_hash);
CREATE INDEX wecom_binding_codes_expired_idx
    ON openfox.wecom_binding_codes (organization_id, expires_at);

CREATE TABLE openfox.wecom_notification_targets (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    personal_connection_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, personal_connection_id)
        REFERENCES openfox.integration_connections (organization_id, id)
);
CREATE INDEX wecom_notification_targets_connection_idx
    ON openfox.wecom_notification_targets (organization_id, personal_connection_id);

ALTER TABLE openfox.wecom_binding_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wecom_binding_codes FORCE ROW LEVEL SECURITY;
CREATE POLICY wecom_binding_codes_tenant_isolation ON openfox.wecom_binding_codes
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
ALTER TABLE openfox.wecom_notification_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wecom_notification_targets FORCE ROW LEVEL SECURITY;
CREATE POLICY wecom_notification_targets_tenant_isolation ON openfox.wecom_notification_targets
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.user_notification_channels
    DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels
    ADD CONSTRAINT user_notification_channels_channel_valid
    CHECK (channel IN ('desktop', 'browser', 'feishu', 'email', 'wecom'));

CREATE OR REPLACE FUNCTION openfox_private.list_im_connections(p_after_organization uuid, p_after_connection uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, connection_id uuid)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im connection scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT c.organization_id, c.id
      FROM openfox.integration_connections c
      JOIN openfox.organization_directory d ON d.organization_id = c.organization_id AND d.deleted_at IS NULL
     WHERE c.provider IN ('feishu', 'lark', 'dingtalk', 'wecom') AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
       AND (p_after_organization IS NULL
            OR (c.organization_id, c.id) > (p_after_organization, coalesce(p_after_connection, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY c.organization_id, c.id
     LIMIT p_limit;
END $$;
ALTER FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) FROM PUBLIC;

CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_notification()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private AS $$
BEGIN
    INSERT INTO openfox.im_notification_outbox (
        organization_id, notification_id, recipient_user_id, personal_connection_id, app_connection_id,
        kind, safe_summary, task_id, conversation_id, direct_chat_id, direct_message_id)
    SELECT n.organization_id, n.id, n.recipient_user_id, p.id, a.id, n.kind, n.safe_summary,
           n.task_id, n.conversation_id, n.direct_chat_id, n.direct_message_id
      FROM changed_new n
      JOIN openfox.integration_connections p
        ON p.organization_id = n.organization_id AND p.provider IN ('feishu', 'lark', 'dingtalk', 'wecom')
       AND p.scope = 'PERSONAL' AND p.owner_user_id = n.recipient_user_id AND p.status = 'ACTIVE'
      JOIN openfox.integration_connections a
        ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
       AND a.scope = 'ORGANIZATION' AND a.status = 'ACTIVE'
       AND (a.account_kind = 'app' OR (p.provider = 'wecom' AND a.account_kind IN ('task_assistant', 'agent')))
     WHERE (p.provider <> 'wecom' OR (
         EXISTS (SELECT 1 FROM openfox.wecom_notification_targets target
                  WHERE target.organization_id = n.organization_id AND target.user_id = n.recipient_user_id
                    AND target.personal_connection_id = p.id)
         AND EXISTS (SELECT 1 FROM openfox.user_notification_channels ch
                      WHERE ch.user_id = n.recipient_user_id AND ch.channel = 'wecom' AND ch.enabled)))
       AND (n.kind <> 'DIRECT_MESSAGE' OR (p.provider IN ('feishu', 'wecom') AND EXISTS (
         SELECT 1 FROM openfox.user_notification_channels ch
          WHERE ch.user_id = n.recipient_user_id AND ch.channel = p.provider
            AND ch.enabled AND ch.direct_messages IS TRUE)))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;

RESET ROLE;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.wecom_binding_codes TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.wecom_notification_targets TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) TO openfox_context;
