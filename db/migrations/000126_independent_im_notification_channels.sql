-- N02: snapshot existing Feishu choices once; new private-chat consent is always false.
SET ROLE openfox_migrator;
ALTER TABLE openfox.user_notification_channels DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels ADD CONSTRAINT user_notification_channels_channel_valid
 CHECK (channel IN ('desktop','browser','feishu','email','wecom','lark','dingtalk'));
INSERT INTO openfox.user_notification_channels
 (user_id,channel,enabled,disabled_events,direct_messages,event_overrides,muted_groups,updated_at)
SELECT f.user_id,p.channel,f.enabled,f.disabled_events,false,f.event_overrides,f.muted_groups,clock_timestamp()
 FROM openfox.user_notification_channels f CROSS JOIN (VALUES ('lark'),('dingtalk')) p(channel)
 WHERE f.channel='feishu'
ON CONFLICT (user_id,channel) DO NOTHING;

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
       AND (n.kind <> 'DIRECT_MESSAGE' OR (p.provider IN ('feishu', 'lark', 'dingtalk', 'wecom') AND EXISTS (
         SELECT 1 FROM openfox.user_notification_channels ch
          WHERE ch.user_id = n.recipient_user_id AND ch.channel = p.provider
            AND ch.enabled AND ch.direct_messages IS TRUE)))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;


RESET ROLE;
