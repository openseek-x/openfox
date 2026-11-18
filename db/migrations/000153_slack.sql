-- I15: Slack ordinary-workspace installations, identities, routing and durable
-- external-operation outcomes. Existing IM bots/inbound/reply/notification queues
-- remain authoritative; these rows hold Slack-specific identity and fencing.
-- Rollback: disconnect Slack locally and stop its dispatch before reverting code.
-- Keep these tables and UNKNOWN records; do not replay them through an older sender.
SET ROLE openfox_migrator;
-- Exact producing Run, assigned by dispatch's authoritative message insert.
-- NULL generic system messages are not guessed as another person's Run.
ALTER TABLE openfox.im_reply_outbox ADD COLUMN source_run_id uuid;
ALTER TABLE openfox.im_reply_outbox ADD CONSTRAINT im_reply_source_run_fk FOREIGN KEY(organization_id,source_run_id) REFERENCES openfox.runs(organization_id,id);

CREATE TABLE openfox.slack_apps (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 parent_app_id uuid,
 bot_id uuid,
 kind text NOT NULL CHECK(kind IN ('CENTRAL','AGENT','TASK_ASSISTANT')),
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 35),
 app_id text NOT NULL DEFAULT '' CHECK(char_length(app_id)<=64),
 team_id text NOT NULL DEFAULT '' CHECK(char_length(team_id)<=64),
 owner_slack_user_id text NOT NULL DEFAULT '' CHECK(char_length(owner_slack_user_id)<=64),
 bot_user_id text NOT NULL DEFAULT '' CHECK(char_length(bot_user_id)<=64),
 bot_external_id text NOT NULL DEFAULT '' CHECK(char_length(bot_external_id)<=64),
 created_by uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),
 parent_version bigint,
 deployment_fingerprint bytea CHECK(deployment_fingerprint IS NULL OR octet_length(deployment_fingerprint)=32),
 parent_generation bigint CHECK(parent_generation>0),
 identity_id uuid,
 identity_version bigint,
 state text NOT NULL DEFAULT 'PREPARED' CHECK(state IN ('PREPARED','PROVING','PROOF_UNKNOWN','CREATING','CREATE_UNKNOWN','APP_CREATED','MANIFEST_UNKNOWN','WAITING_URL_VERIFICATION','WAITING_INSTALL','ACTIVE','NEEDS_REAUTH','FAILED','CANCELLED','ORPHANED_APP','DISCONNECTED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 token_version bigint NOT NULL DEFAULT 1 CHECK(token_version>0),
 installed_at timestamptz,
 url_verified_at timestamptz,
 activated_at timestamptz,
 token_expires_at timestamptz,
 app_credentials_sealed bytea CHECK(octet_length(app_credentials_sealed)<=16384),
 app_credentials_key_id text NOT NULL DEFAULT '',
 configuration_sealed bytea CHECK(octet_length(configuration_sealed)<=16384),
 configuration_key_id text NOT NULL DEFAULT '',
 configuration_expires_at timestamptz,
 operation_key text NOT NULL CHECK(char_length(operation_key) BETWEEN 16 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 manifest_hash bytea CHECK(octet_length(manifest_hash)=32),
 operation_expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz,
 refresh_lease_owner text NOT NULL DEFAULT '',
 refresh_lease_expires_at timestamptz,
 next_attempt_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,connection_id),
 UNIQUE(organization_id,created_by,operation_key),
 FOREIGN KEY(organization_id) REFERENCES openfox.organizations(id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,parent_app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK((kind='CENTRAL' AND parent_app_id IS NULL AND bot_id IS NULL AND parent_generation IS NULL) OR (kind IN ('AGENT','TASK_ASSISTANT') AND parent_app_id IS NOT NULL AND bot_id IS NOT NULL AND parent_generation IS NOT NULL)),
 CHECK(state<>'ACTIVE' OR (installed_at IS NOT NULL AND url_verified_at IS NOT NULL AND app_id<>'' AND team_id<>'' AND bot_user_id<>'' AND activated_at IS NOT NULL))
);
-- External installation ownership is global; tenant-filtered prechecks cannot
-- enforce it. Do not include generation: reinstallation is the same installation.
CREATE UNIQUE INDEX slack_installation_owner_unique ON openfox.slack_apps(app_id,team_id)
 WHERE installed_at IS NOT NULL AND state NOT IN ('DISCONNECTED','CANCELLED','ORPHANED_APP');
CREATE INDEX slack_apps_due ON openfox.slack_apps(organization_id,next_attempt_at) WHERE next_attempt_at IS NOT NULL;
CREATE INDEX slack_apps_parent ON openfox.slack_apps(organization_id,parent_app_id);

CREATE TABLE openfox.slack_identities (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 parent_app_id uuid NOT NULL,
 owner_user_id uuid NOT NULL,
 team_id text NOT NULL CHECK(char_length(team_id) BETWEEN 1 AND 64),
 slack_user_id text NOT NULL CHECK(char_length(slack_user_id) BETWEEN 1 AND 64),
 subject text NOT NULL CHECK(char_length(subject) BETWEEN 1 AND 255),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,connection_id),
 FOREIGN KEY(organization_id,parent_app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE UNIQUE INDEX slack_identity_claim_unique ON openfox.slack_identities(organization_id,team_id,slack_user_id) WHERE active;
CREATE UNIQUE INDEX slack_identity_owner_unique ON openfox.slack_identities(organization_id,parent_app_id,owner_user_id) WHERE active;
ALTER TABLE openfox.slack_apps ADD FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.slack_identities(organization_id,id);

CREATE TABLE openfox.slack_oauth_sessions (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_id uuid NOT NULL,
 purpose text NOT NULL CHECK(purpose IN ('organization','personal','app')),
 actor_id uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),
 app_version bigint NOT NULL CHECK(app_version>0),
 generation bigint NOT NULL CHECK(generation>0),
 operation_key text NOT NULL CHECK(char_length(operation_key) BETWEEN 16 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 state_hash bytea NOT NULL UNIQUE CHECK(octet_length(state_hash)=32),
 nonce_sealed bytea NOT NULL CHECK(octet_length(nonce_sealed)<=2048),
 redirect_uri text NOT NULL CHECK(char_length(redirect_uri)<=2048),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','EXCHANGING','DONE','FAILED','UNKNOWN','CANCELLED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 expires_at timestamptz NOT NULL,
 lease_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 UNIQUE(organization_id,actor_id,operation_key)
);

-- Immutable authorization epochs: disabling never removes the record protecting
-- an existing Run. A later enable creates a distinct epoch and new routes.
CREATE TABLE openfox.slack_channels (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_id uuid NOT NULL,
 channel_id text NOT NULL CHECK(char_length(channel_id) BETWEEN 1 AND 64),
 name text NOT NULL DEFAULT '' CHECK(char_length(name)<=200),
 space_id uuid,
 enabled boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 generation bigint NOT NULL CHECK(generation>0),
 authorized_by uuid NOT NULL,
 identity_id uuid NOT NULL,
 identity_version bigint NOT NULL CHECK(identity_version>0),
 public_reply_consent boolean NOT NULL DEFAULT false,
 last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id),
 FOREIGN KEY(organization_id,authorized_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.slack_identities(organization_id,id),
 CHECK(NOT enabled OR (public_reply_consent AND disabled_at IS NULL))
);
CREATE INDEX slack_channel_history_idx ON openfox.slack_channels(organization_id,app_id,channel_id,version DESC);
CREATE UNIQUE INDEX slack_channel_epoch_unique ON openfox.slack_channels(organization_id,app_id,channel_id) WHERE disabled_at IS NULL;
CREATE TABLE openfox.slack_routes (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_id uuid NOT NULL,
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 channel_id text NOT NULL CHECK(char_length(channel_id) BETWEEN 1 AND 64),
 thread_ts text NOT NULL DEFAULT '' CHECK(char_length(thread_ts)<=32),
 channel_epoch_id uuid,
 identity_id uuid NOT NULL,
 identity_version bigint NOT NULL CHECK(identity_version>0),
 conversation_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,conversation_id),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,channel_epoch_id) REFERENCES openfox.slack_channels(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.slack_identities(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id)
);
CREATE UNIQUE INDEX slack_route_thread_unique ON openfox.slack_routes(organization_id,app_id,generation,channel_id,thread_ts,COALESCE(channel_epoch_id,'00000000-0000-0000-0000-000000000000'::uuid));

-- Each message snapshots its actual actor, not the thread creator. This fence
-- remains attached to a queued input and its Run after identity unlink/relink.
CREATE TABLE openfox.slack_input_authorizations (
 organization_id uuid NOT NULL,
 message_id uuid NOT NULL,
 route_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 identity_id uuid NOT NULL,
 identity_version bigint NOT NULL CHECK(identity_version>0),
 PRIMARY KEY(organization_id,message_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id),
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.slack_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.slack_identities(organization_id,id)
);
CREATE INDEX slack_input_identity_idx ON openfox.slack_input_authorizations(organization_id,identity_id);
ALTER TABLE openfox.slack_input_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_input_authorizations FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_input_authorizations_tenant ON openfox.slack_input_authorizations
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT ON openfox.slack_input_authorizations TO openfox_app;

CREATE TABLE openfox.slack_events (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_id uuid NOT NULL,
 team_id text NOT NULL CHECK(char_length(team_id) BETWEEN 1 AND 64),
 event_id text NOT NULL CHECK(char_length(event_id) BETWEEN 1 AND 128),
 event_type text NOT NULL CHECK(char_length(event_type) BETWEEN 1 AND 64),
 generation bigint NOT NULL CHECK(generation>0),
 payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND pg_column_size(payload)<=1048576),
 message_key text CHECK(char_length(message_key)<=160),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','PROCESSING','DONE','IGNORED','FAILED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 100),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,app_id,team_id,event_id),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id)
);
CREATE UNIQUE INDEX slack_message_event_unique ON openfox.slack_events(organization_id,app_id,team_id,message_key) WHERE message_key IS NOT NULL;
CREATE INDEX slack_events_due ON openfox.slack_events(organization_id,next_attempt_at) WHERE state IN ('PENDING','PROCESSING');

-- Parent source IDs point to the existing outbox or event. The unique part key
-- permits partial success without replaying earlier messages; unknown sends are
-- never picked by the retry scanner.
CREATE TABLE openfox.slack_deliveries (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_id uuid NOT NULL,
 source_kind text NOT NULL CHECK(source_kind IN ('REPLY','NOTIFICATION','UNFURL','NOTICE')),
 source_id uuid NOT NULL,
 part integer NOT NULL CHECK(part BETWEEN 1 AND 10),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 generation bigint NOT NULL CHECK(generation>0),
 token_version bigint NOT NULL CHECK(token_version>0),
 channel_epoch_id uuid,
 identity_id uuid,
 identity_version bigint,
 channel_id text NOT NULL DEFAULT '' CHECK(char_length(channel_id)<=64),
 thread_ts text NOT NULL DEFAULT '' CHECK(char_length(thread_ts)<=32),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND pg_column_size(body)<=32768),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 marker text NOT NULL UNIQUE CHECK(char_length(marker) BETWEEN 16 AND 128),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','DONE','UNKNOWN','FAILED','SKIPPED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 remote_ts text NOT NULL DEFAULT '' CHECK(char_length(remote_ts)<=32),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 100),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,source_kind,source_id,part),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id),
 FOREIGN KEY(organization_id,channel_epoch_id) REFERENCES openfox.slack_channels(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.slack_identities(organization_id,id)
);
CREATE INDEX slack_deliveries_due ON openfox.slack_deliveries(organization_id,next_attempt_at) WHERE state IN ('PENDING','SENDING');
CREATE TABLE openfox.slack_rate_limits (
 organization_id uuid NOT NULL,
 app_id uuid NOT NULL,
 method text NOT NULL CHECK(method IN ('history','replies','post','unfurl','users','conversation','members','open_dm','manifest')),
 resource text NOT NULL DEFAULT '' CHECK(char_length(resource)<=64),
 not_before timestamptz NOT NULL,
 PRIMARY KEY(organization_id,app_id,method,resource),
 FOREIGN KEY(organization_id,app_id) REFERENCES openfox.slack_apps(organization_id,id)
);

ALTER TABLE openfox.slack_apps ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_apps FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_apps_tenant ON openfox.slack_apps
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_apps TO openfox_app;

ALTER TABLE openfox.slack_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_identities FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_identities_tenant ON openfox.slack_identities
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_identities TO openfox_app;

ALTER TABLE openfox.slack_oauth_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_oauth_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_oauth_sessions_tenant ON openfox.slack_oauth_sessions
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_oauth_sessions TO openfox_app;

ALTER TABLE openfox.slack_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_channels_tenant ON openfox.slack_channels
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_channels TO openfox_app;

ALTER TABLE openfox.slack_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_routes_tenant ON openfox.slack_routes
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_routes TO openfox_app;

ALTER TABLE openfox.slack_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_events FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_events_tenant ON openfox.slack_events
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_events TO openfox_app;

ALTER TABLE openfox.slack_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_deliveries_tenant ON openfox.slack_deliveries
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_deliveries TO openfox_app;

ALTER TABLE openfox.slack_rate_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.slack_rate_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY slack_rate_limits_tenant ON openfox.slack_rate_limits
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.slack_rate_limits TO openfox_app;

CREATE POLICY slack_apps_control_read ON openfox.slack_apps FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY slack_oauth_control_read ON openfox.slack_oauth_sessions FOR SELECT TO openfox_migrator USING(true);
-- Only routing information crosses tenant boundaries. App secrets are loaded in
-- the selected organization transaction and never returned by these helpers.
CREATE FUNCTION openfox_private.slack_app_route(p_id uuid)
RETURNS TABLE(organization_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT a.organization_id FROM openfox.slack_apps a JOIN openfox.organization_directory d ON d.organization_id=a.organization_id AND d.deleted_at IS NULL WHERE a.id=p_id LIMIT 1
$$;
REVOKE ALL ON FUNCTION openfox_private.slack_app_route(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.slack_app_route(uuid) TO openfox_context;
CREATE FUNCTION openfox_private.slack_oauth_route(p_hash bytea)
RETURNS TABLE(organization_id uuid,session_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT s.organization_id,s.id FROM openfox.slack_oauth_sessions s JOIN openfox.organization_directory d ON d.organization_id=s.organization_id AND d.deleted_at IS NULL WHERE s.state_hash=p_hash LIMIT 1
$$;
REVOKE ALL ON FUNCTION openfox_private.slack_oauth_route(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.slack_oauth_route(bytea) TO openfox_context;
-- Central event URL is shared by all workspace installations. This record is
-- not a tenant directory and carries no credential or installation identifier.
CREATE TABLE openfox_private.slack_deployment_verifications (
 fingerprint bytea PRIMARY KEY CHECK(octet_length(fingerprint)=32),
 verified_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
REVOKE ALL ON openfox_private.slack_deployment_verifications FROM PUBLIC;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox_private.slack_deployment_verifications TO openfox_context;

-- Slack joins N01's existing 42 keys and revision/quiet-group logic. It has no
-- implicit consent row and does not copy another provider's preferences.
ALTER TABLE openfox.user_notification_channels DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels ADD CONSTRAINT user_notification_channels_channel_valid
 CHECK(channel IN ('desktop','browser','feishu','email','wecom','lark','dingtalk','slack'));
CREATE OR REPLACE FUNCTION openfox_private.notification_channel_allows(
 p_user uuid, p_channel text, p_kind text, p_event text, p_level text)
RETURNS boolean LANGUAGE sql STABLE SET search_path=pg_catalog,openfox AS $$
 SELECT p_channel IN ('desktop','browser','feishu','email','wecom','lark','dingtalk','slack')
  AND COALESCE(ch.enabled,p_channel NOT IN ('email','wecom','slack'))
  AND (p_kind<>'DIRECT_MESSAGE' OR COALESCE(ch.direct_messages,p_channel IN ('desktop','browser')))
  AND NOT COALESCE(k.group_key=ANY(ch.muted_groups),false)
  AND COALESCE((ch.event_overrides->>p_event)::boolean,
    NOT (COALESCE(p_level='off',false) AND COALESCE(p_event IN (
      'chat_replied_participant','task_failed_participant','task_needs_human_input_participant','task_review_requested_participant'),false))
    AND NOT COALESCE((CASE
      WHEN p_kind IN ('TASK_ASSIGNED','TRIAGE_NEW') THEN 'assign'
      WHEN p_kind='TASK_UPDATED' THEN 'status'
      WHEN p_kind IN ('TASK_COMMENT','TASK_REACTION','REPLY') THEN 'comment'
      WHEN p_kind='MENTION' THEN 'mention'
      WHEN p_kind='AUTOMATION_RESULT' THEN 'automation'
      WHEN p_kind='DIRECT_MESSAGE' THEN 'human_chat'
      ELSE 'agent' END)=ANY(ch.disabled_events),false))
 FROM (SELECT 1) seed
 LEFT JOIN openfox.user_notification_channels ch ON ch.user_id=p_user AND ch.channel=p_channel
 LEFT JOIN openfox.inbox_policy_keys k ON k.key=p_event
$$;
REVOKE ALL ON FUNCTION openfox_private.notification_channel_allows(uuid,text,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notification_channel_allows(uuid,text,text,text,text) TO openfox_app;

-- Notifications are recipient-only. Workers receive only a policy decision for an
-- existing outbox item in their current tenant, never notification content.
-- The migrator's existing notification read policy and comment tombstones remain in force.
CREATE OR REPLACE FUNCTION openfox_private.notification_outbox_policy(p_outbox uuid,p_channel text)
RETURNS TABLE(enabled boolean,allowed boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
 SELECT COALESCE(ch.enabled,p_channel NOT IN ('email','wecom','slack')),
        openfox_private.notification_channel_allows(n.recipient_user_id,p_channel,n.kind,n.inbox_event,n.inbox_level)
 FROM (
   SELECT q.organization_id,q.notification_id,q.recipient_user_id
     FROM openfox.email_notification_outbox q
    WHERE p_channel='email' AND q.organization_id=openfox_private.current_organization_id() AND q.id=p_outbox
   UNION ALL
   SELECT q.organization_id,q.notification_id,q.recipient_user_id
     FROM openfox.im_notification_outbox q
     JOIN openfox.integration_connections a ON a.organization_id=q.organization_id AND a.id=q.app_connection_id
    WHERE p_channel IN ('feishu','lark','dingtalk','wecom','slack') AND a.provider=p_channel
      AND q.organization_id=openfox_private.current_organization_id() AND q.id=p_outbox
 ) queued
 JOIN openfox.notifications n ON n.organization_id=queued.organization_id AND n.id=queued.notification_id
  AND n.recipient_user_id=queued.recipient_user_id
 LEFT JOIN openfox.user_notification_channels ch ON ch.user_id=n.recipient_user_id AND ch.channel=p_channel
$$;
REVOKE ALL ON FUNCTION openfox_private.notification_outbox_policy(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notification_outbox_policy(uuid,text) TO openfox_app;

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
        ON p.organization_id = n.organization_id AND p.provider IN ('feishu', 'lark', 'dingtalk', 'wecom', 'slack')
       AND p.scope = 'PERSONAL' AND p.owner_user_id = n.recipient_user_id AND p.status = 'ACTIVE'
      JOIN openfox.integration_connections a
        ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
       AND a.scope = 'ORGANIZATION' AND a.status = 'ACTIVE'
       AND (a.account_kind = 'app' OR (p.provider = 'wecom' AND a.account_kind IN ('task_assistant', 'agent')))
     WHERE (p.provider <> 'slack' OR (EXISTS (SELECT 1 FROM openfox.slack_identities si JOIN openfox.slack_apps sa ON sa.organization_id=si.organization_id AND sa.id=si.parent_app_id WHERE si.organization_id=n.organization_id AND si.connection_id=p.id AND si.active AND sa.connection_id=a.id AND sa.state='ACTIVE') AND EXISTS(SELECT 1 FROM openfox.user_notification_channels sc WHERE sc.user_id=n.recipient_user_id AND sc.channel='slack' AND sc.enabled)))
       AND (p.provider <> 'wecom' OR (
         EXISTS (SELECT 1 FROM openfox.wecom_notification_targets target
                  WHERE target.organization_id = n.organization_id AND target.user_id = n.recipient_user_id
                    AND target.personal_connection_id = p.id)
         AND EXISTS (SELECT 1 FROM openfox.user_notification_channels ch
                      WHERE ch.user_id = n.recipient_user_id AND ch.channel = 'wecom' AND ch.enabled)))
       AND (n.kind <> 'DIRECT_MESSAGE' OR (p.provider IN ('feishu', 'lark', 'dingtalk', 'wecom', 'slack') AND EXISTS (
         SELECT 1 FROM openfox.user_notification_channels ch
          WHERE ch.user_id = n.recipient_user_id AND ch.channel = p.provider
            AND ch.enabled AND ch.direct_messages IS TRUE)))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;
CREATE POLICY slack_identities_control_read ON openfox.slack_identities FOR SELECT TO openfox_migrator USING(true);
CREATE FUNCTION openfox_private.slack_notifications_available(p_user uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT EXISTS(SELECT 1 FROM openfox.slack_identities i
 JOIN openfox.slack_apps a ON a.organization_id=i.organization_id AND a.id=i.parent_app_id
 JOIN openfox.integration_connections p ON p.organization_id=i.organization_id AND p.id=i.connection_id
 JOIN openfox.integration_connections c ON c.organization_id=a.organization_id AND c.id=a.connection_id
 JOIN openfox.organization_directory d ON d.organization_id=i.organization_id AND d.deleted_at IS NULL
 JOIN openfox.user_organization_index m ON m.organization_id=i.organization_id AND m.user_id=i.owner_user_id AND m.membership_status='ACTIVE' AND m.role<>'GUEST'
 WHERE i.owner_user_id=p_user AND i.active AND a.kind='CENTRAL' AND a.state='ACTIVE' AND p.status='ACTIVE' AND c.status='ACTIVE')
$$;
REVOKE ALL ON FUNCTION openfox_private.slack_notifications_available(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.slack_notifications_available(uuid) TO openfox_context;
CREATE FUNCTION openfox_private.slack_central_route(p_app text,p_team text)
RETURNS TABLE(organization_id uuid,app_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT a.organization_id,a.id FROM openfox.slack_apps a
 JOIN openfox.organization_directory d ON d.organization_id=a.organization_id AND d.deleted_at IS NULL
 WHERE a.kind='CENTRAL' AND a.app_id=p_app AND a.team_id=p_team AND a.installed_at IS NOT NULL
 AND a.state NOT IN ('DISCONNECTED','CANCELLED','ORPHANED_APP') LIMIT 1
$$;
REVOKE ALL ON FUNCTION openfox_private.slack_central_route(text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.slack_central_route(text,text) TO openfox_context;

CREATE FUNCTION openfox_private.slack_deployment_organizations(p_after uuid,p_fingerprint bytea)
RETURNS TABLE(organization_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT DISTINCT a.organization_id FROM openfox.slack_apps a
 JOIN openfox.organization_directory d ON d.organization_id=a.organization_id AND d.deleted_at IS NULL
 WHERE a.kind='CENTRAL' AND a.state NOT IN ('DISCONNECTED','CANCELLED','ORPHANED_APP')
 AND a.deployment_fingerprint IS DISTINCT FROM p_fingerprint
 AND (p_after IS NULL OR a.organization_id>p_after)
 ORDER BY a.organization_id LIMIT 100
$$;
REVOKE ALL ON FUNCTION openfox_private.slack_deployment_organizations(uuid,bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.slack_deployment_organizations(uuid,bytea) TO openfox_context;
CREATE POLICY slack_events_scan ON openfox.slack_events FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY slack_deliveries_scan ON openfox.slack_deliveries FOR SELECT TO openfox_migrator USING(true);
CREATE OR REPLACE FUNCTION openfox_private.list_im_worker_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im worker scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT r.organization_id FROM openfox.im_app_registrations r
         WHERE r.status = 'PENDING' AND r.next_poll_at <= clock_timestamp()
           AND (r.lease_expires_at IS NULL OR r.lease_expires_at <= clock_timestamp())
        UNION
        SELECT i.organization_id FROM openfox.im_inbound_messages i
         WHERE (i.status = 'PENDING' AND i.next_attempt_at <= clock_timestamp())
            OR (i.status = 'PROCESSING' AND (i.lease_expires_at IS NULL OR i.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT a.organization_id FROM openfox.slack_apps a
         WHERE (a.next_attempt_at<=clock_timestamp() AND (a.lease_expires_at IS NULL OR a.lease_expires_at<=clock_timestamp()))
            OR (a.state IN ('PROVING','CREATING') AND a.lease_expires_at<=clock_timestamp())
            OR (a.state='ACTIVE' AND a.token_expires_at<=clock_timestamp()+interval '5 minutes' AND (a.next_attempt_at IS NULL OR a.next_attempt_at<=clock_timestamp()))
            OR (a.state='WAITING_URL_VERIFICATION' AND a.kind='CENTRAL')
            OR (a.refresh_lease_owner<>'' AND a.refresh_lease_expires_at<=clock_timestamp())
        UNION
        SELECT e.organization_id FROM openfox.slack_events e
         WHERE (e.state='PENDING' AND e.next_attempt_at<=clock_timestamp()) OR (e.state='PROCESSING' AND e.lease_expires_at<=clock_timestamp())
        UNION
        SELECT d.organization_id FROM openfox.slack_deliveries d
         WHERE (d.state='PENDING' AND d.next_attempt_at<=clock_timestamp()) OR (d.state='SENDING' AND d.lease_expires_at<=clock_timestamp())
        UNION
        SELECT s.organization_id FROM openfox.slack_oauth_sessions s
         WHERE (s.status='EXCHANGING' AND s.lease_expires_at<=clock_timestamp()) OR (s.status='PENDING' AND s.expires_at<=clock_timestamp())
        UNION
        SELECT n.organization_id FROM openfox.im_notification_outbox n
         JOIN openfox.integration_connections c ON c.organization_id=n.organization_id AND c.id=n.app_connection_id
         WHERE c.provider='slack' AND ((n.status='PENDING' AND n.next_attempt_at<=clock_timestamp()) OR (n.status='SENDING' AND n.lease_expires_at<=clock_timestamp()))
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) FROM PUBLIC;


RESET ROLE;
