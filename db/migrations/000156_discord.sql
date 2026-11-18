-- I17 Discord: exact Snowflakes, scoped authorization, durable Gateway and reply
-- evidence. No legacy backfill. 147 remains unallocated; run only in a fresh
-- isolated database after the final cumulative 148 during implementation.
SET ROLE openfox_migrator;

CREATE DOMAIN openfox.discord_snowflake AS text
 CHECK(VALUE IS NULL OR CASE WHEN VALUE ~ '^[1-9][0-9]{0,19}$' THEN VALUE::numeric<=18446744073709551615 ELSE false END);

CREATE TABLE openfox.discord_bot_controls (
 organization_id uuid NOT NULL,bot_id uuid NOT NULL,connection_id uuid NOT NULL,
 remote_bot_id openfox.discord_snowflake NOT NULL,application_id openfox.discord_snowflake NOT NULL,
 username text NOT NULL CHECK(char_length(username) BETWEEN 1 AND 128),name text NOT NULL CHECK(char_length(name)<=200),
 kind text NOT NULL CHECK(kind IN ('TASK_ASSISTANT','AGENT')),target_space_id uuid,target_agent_id uuid,
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','ATTENTION','DISCONNECTED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),authorization_generation bigint NOT NULL DEFAULT 1 CHECK(authorization_generation>0),
 token_version bigint NOT NULL DEFAULT 1 CHECK(token_version>0),message_content_enabled boolean NOT NULL,
 bot_public boolean NOT NULL,managed_by uuid NOT NULL,managed_version bigint NOT NULL CHECK(managed_version>0),
 global_not_before timestamptz,send_claim text NOT NULL DEFAULT '' CHECK(char_length(send_claim)<=64),send_lease_until timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=64),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,bot_id),UNIQUE(organization_id,connection_id),UNIQUE(remote_bot_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,managed_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,target_space_id) REFERENCES openfox.spaces(organization_id,id),
 FOREIGN KEY(organization_id,target_agent_id) REFERENCES openfox.agents(organization_id,id),
 CHECK((kind='TASK_ASSISTANT' AND target_space_id IS NOT NULL AND target_agent_id IS NULL) OR
 (kind='AGENT' AND target_agent_id IS NOT NULL AND target_space_id IS NULL))
);
CREATE TABLE openfox.discord_connection_preparations (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),actor_id uuid NOT NULL,actor_version bigint NOT NULL CHECK(actor_version>0),
 operation_key uuid NOT NULL,request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 kind text NOT NULL CHECK(kind IN ('TASK_ASSISTANT','AGENT')),space_id uuid,agent_id uuid,target_version bigint NOT NULL CHECK(target_version>0),
 bot_id uuid,bot_version bigint NOT NULL DEFAULT 0 CHECK(bot_version>=0),
 remote_bot_id openfox.discord_snowflake NOT NULL,application_id openfox.discord_snowflake NOT NULL,
 username text NOT NULL CHECK(char_length(username) BETWEEN 1 AND 128),bot_name text NOT NULL CHECK(char_length(bot_name)<=200),
 message_content_enabled boolean NOT NULL,bot_public boolean NOT NULL,
 config jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(config)='object' AND pg_column_size(config)<=16384),
 extra_prompt text NOT NULL DEFAULT '' CHECK(char_length(extra_prompt)<=4000),token_sealed bytea CHECK(octet_length(token_sealed)<=8192),
 status text NOT NULL DEFAULT 'PENDING_CONFIRM' CHECK(status IN ('PENDING_CONFIRM','COMPLETED','FAILED','CANCELLED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),completed_at timestamptz,
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,actor_id,operation_key),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id),
 FOREIGN KEY(organization_id,agent_id) REFERENCES openfox.agents(organization_id,id),
 CHECK((status='PENDING_CONFIRM')=(token_sealed IS NOT NULL))
);
CREATE INDEX discord_preparations_expire ON openfox.discord_connection_preparations(organization_id,expires_at) WHERE status='PENDING_CONFIRM';

-- The existing im_connection_states remains the single lease owner/expiry.
-- Each physical connection attempt increments this fence. Resume material is
-- sealed; received seq is never a durable checkpoint.
CREATE TABLE openfox.discord_gateway_sessions (
 organization_id uuid NOT NULL,connection_id uuid NOT NULL,bot_id uuid NOT NULL,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),token_version bigint NOT NULL CHECK(token_version>0),
 lease_fence bigint NOT NULL DEFAULT 1 CHECK(lease_fence>0),attempt_id uuid,
 session_generation bigint NOT NULL DEFAULT 1 CHECK(session_generation>0),resume_sealed bytea CHECK(octet_length(resume_sealed)<=8192),
 durable_seq bigint NOT NULL DEFAULT -1 CHECK(durable_seq>=-1),durable_at timestamptz,
 gateway_url text NOT NULL DEFAULT '' CHECK(char_length(gateway_url)<=512),
 identify_remaining bigint NOT NULL DEFAULT -1 CHECK(identify_remaining>=-1),identify_reset_at timestamptz,identify_not_before timestamptz,
 gap_count bigint NOT NULL DEFAULT 0 CHECK(gap_count>=0),gap_started_at timestamptz,gap_finished_at timestamptz,
 gap_reason text NOT NULL DEFAULT '' CHECK(char_length(gap_reason)<=64),
 PRIMARY KEY(organization_id,connection_id),UNIQUE(organization_id,bot_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id)
);
CREATE TABLE openfox.discord_identities (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,owner_user_id uuid NOT NULL,
 remote_user_id openfox.discord_snowflake NOT NULL,private_channel_id openfox.discord_snowflake NOT NULL,
 display_name text NOT NULL CHECK(char_length(display_name)<=200),active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 confirmed_at timestamptz NOT NULL DEFAULT clock_timestamp(),disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),CHECK(active=(disabled_at IS NULL))
);
CREATE UNIQUE INDEX discord_identity_remote_active ON openfox.discord_identities(organization_id,bot_id,remote_user_id) WHERE active;
CREATE UNIQUE INDEX discord_identity_owner_active ON openfox.discord_identities(organization_id,bot_id,owner_user_id) WHERE active;
CREATE TABLE openfox.discord_binding_sessions (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,owner_user_id uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 code_hash bytea NOT NULL UNIQUE CHECK(octet_length(code_hash)=32),
 status text NOT NULL DEFAULT 'WAITING' CHECK(status IN ('WAITING','CANDIDATE','COMPLETED','CANCELLED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),expires_at timestamptz NOT NULL,consumed_at timestamptz,
 candidate_user_id openfox.discord_snowflake,candidate_channel_id openfox.discord_snowflake,candidate_name text NOT NULL DEFAULT '' CHECK(char_length(candidate_name)<=200),
 identity_id uuid,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.discord_identities(organization_id,id),
 CHECK(status NOT IN ('CANDIDATE','COMPLETED') OR(candidate_user_id IS NOT NULL AND candidate_channel_id IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE UNIQUE INDEX discord_binding_pending ON openfox.discord_binding_sessions(organization_id,bot_id,owner_user_id) WHERE status IN ('WAITING','CANDIDATE');
CREATE INDEX discord_binding_expire ON openfox.discord_binding_sessions(organization_id,expires_at) WHERE status IN ('WAITING','CANDIDATE');
CREATE TABLE openfox.discord_channel_epochs (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,
 guild_id openfox.discord_snowflake NOT NULL,channel_id openfox.discord_snowflake NOT NULL,
 guild_name text NOT NULL CHECK(char_length(guild_name)<=200),channel_name text NOT NULL CHECK(char_length(channel_name)<=200),
 enabled boolean NOT NULL DEFAULT true,paused boolean NOT NULL DEFAULT false,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),public_reply_consent boolean NOT NULL CHECK(public_reply_consent),
 history_consent boolean NOT NULL CHECK(history_consent),authorized_by uuid NOT NULL,identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=64),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,authorized_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.discord_identities(organization_id,id),CHECK(enabled=(disabled_at IS NULL))
);
CREATE UNIQUE INDEX discord_channel_enabled ON openfox.discord_channel_epochs(organization_id,bot_id,channel_id) WHERE enabled;
CREATE TABLE openfox.discord_routes (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,
 channel_id openfox.discord_snowflake NOT NULL,guild_id openfox.discord_snowflake,
 kind text NOT NULL CHECK(kind IN ('p2p','group')),conversation_id uuid NOT NULL,
 identity_id uuid,identity_version bigint,epoch_id uuid,authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),closed_at timestamptz,
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,conversation_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.discord_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.discord_channel_epochs(organization_id,id),
 CHECK((kind='p2p' AND guild_id IS NULL AND identity_id IS NOT NULL AND identity_version IS NOT NULL AND identity_version>0 AND epoch_id IS NULL) OR
 (kind='group' AND guild_id IS NOT NULL AND identity_id IS NULL AND identity_version IS NULL AND epoch_id IS NOT NULL))
);
CREATE UNIQUE INDEX discord_route_current ON openfox.discord_routes(organization_id,bot_id,channel_id) WHERE closed_at IS NULL;

-- Permanent message dedup + mutable processing facts, not raw Gateway payload.
-- A queue UUID is allocated once as receipt id; queued inbound/message use it.
CREATE TABLE openfox.discord_message_receipts (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,connection_id uuid NOT NULL,
 remote_message_id openfox.discord_snowflake NOT NULL,channel_id openfox.discord_snowflake NOT NULL,guild_id openfox.discord_snowflake,
 sender_id openfox.discord_snowflake NOT NULL,body_hash bytea NOT NULL CHECK(octet_length(body_hash)=32),
 kind text NOT NULL CHECK(kind IN ('MESSAGE','BIND','NOTICE')),binding_code_hash bytea CHECK(octet_length(binding_code_hash)=32),
 inbound_id uuid,route_id uuid,actor_id uuid,actor_version bigint,identity_id uuid,identity_version bigint,epoch_id uuid,epoch_version bigint,authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 source_deleted boolean NOT NULL DEFAULT false,result text NOT NULL DEFAULT '' CHECK(char_length(result)<=64),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,bot_id,remote_message_id),UNIQUE(organization_id,inbound_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL(inbound_id),
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.discord_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.discord_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.discord_channel_epochs(organization_id,id),
 CHECK((epoch_id IS NULL)=(epoch_version IS NULL)),CHECK(inbound_id IS NULL OR inbound_id=id),
 CHECK(route_id IS NULL OR(actor_id IS NOT NULL AND actor_version IS NOT NULL AND actor_version>0 AND identity_id IS NOT NULL AND identity_version IS NOT NULL AND identity_version>0)),
 CHECK((kind='BIND')=(binding_code_hash IS NOT NULL))
);
CREATE TABLE openfox.discord_inputs (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,inbound_id uuid,message_id uuid NOT NULL,
 source_kind text NOT NULL CHECK(source_kind IN ('REMOTE','OPENFOX')),receipt_id uuid,
 route_id uuid NOT NULL,channel_id openfox.discord_snowflake NOT NULL,guild_id openfox.discord_snowflake,remote_message_id openfox.discord_snowflake,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),actor_id uuid NOT NULL,actor_version bigint NOT NULL CHECK(actor_version>0),
 identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),epoch_id uuid,epoch_version bigint,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,inbound_id),UNIQUE(organization_id,message_id),UNIQUE(organization_id,receipt_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL(inbound_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(organization_id,receipt_id) REFERENCES openfox.discord_message_receipts(organization_id,id),
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.discord_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.discord_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.discord_channel_epochs(organization_id,id),
 CHECK((epoch_id IS NULL)=(epoch_version IS NULL)),CHECK(epoch_version IS NULL OR epoch_version>0),
 CHECK((source_kind='REMOTE' AND receipt_id IS NOT NULL AND message_id=receipt_id AND remote_message_id IS NOT NULL)
 OR(source_kind='OPENFOX' AND receipt_id IS NULL AND remote_message_id IS NULL AND inbound_id IS NULL))
);
-- A no-Run rejection stores the exact batch (<=20), independently from expiring
-- IM queues. Each row references both the actual conversation input and its
-- permanent Discord authorization snapshot. There is no "latest input" lookup.
CREATE TABLE openfox.discord_reply_sources (
 organization_id uuid NOT NULL,message_id uuid NOT NULL,conversation_input_id uuid NOT NULL,snapshot_id uuid NOT NULL,
 ordinal smallint NOT NULL CHECK(ordinal BETWEEN 1 AND 20),batch_size smallint NOT NULL CHECK(batch_size BETWEEN 1 AND 20),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,message_id,ordinal),UNIQUE(organization_id,message_id,conversation_input_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id),
 FOREIGN KEY(organization_id,conversation_input_id) REFERENCES openfox.conversation_inputs(organization_id,id),
 FOREIGN KEY(organization_id,snapshot_id) REFERENCES openfox.discord_inputs(organization_id,id),CHECK(ordinal<=batch_size)
);
CREATE TABLE openfox.discord_replies (
 organization_id uuid NOT NULL,outbox_id uuid NOT NULL,bot_id uuid NOT NULL,input_id uuid,notice_receipt_id uuid,
 channel_id openfox.discord_snowflake NOT NULL,guild_id openfox.discord_snowflake,reply_to_message_id openfox.discord_snowflake NOT NULL,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),token_version bigint,
 body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 2000),body_hash bytea NOT NULL CHECK(octet_length(body_hash)=32),
 nonce text NOT NULL CHECK(nonce ~ '^[A-Za-z0-9_-]{1,25}$'),claim_token text NOT NULL DEFAULT '' CHECK(char_length(claim_token)<=64),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,outbox_id),UNIQUE(organization_id,bot_id,nonce),
 FOREIGN KEY(organization_id,outbox_id) REFERENCES openfox.im_reply_outbox(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,input_id) REFERENCES openfox.discord_inputs(organization_id,id),
 FOREIGN KEY(organization_id,notice_receipt_id) REFERENCES openfox.discord_message_receipts(organization_id,id),
 CHECK((input_id IS NULL)<>(notice_receipt_id IS NULL))
);
CREATE TABLE openfox.discord_rate_limits (
 organization_id uuid NOT NULL,bot_id uuid NOT NULL,major text NOT NULL CHECK(char_length(major)<=32),bucket text NOT NULL CHECK(char_length(bucket) BETWEEN 1 AND 128),
 not_before timestamptz NOT NULL,PRIMARY KEY(organization_id,bot_id,major,bucket),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id)
);
CREATE INDEX discord_rate_limits_expire ON openfox.discord_rate_limits(organization_id,not_before);

CREATE FUNCTION openfox_private.validate_discord_input_scope() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE valid boolean;
BEGIN
 SELECT r.closed_at IS NULL AND r.bot_id=NEW.bot_id AND r.channel_id=NEW.channel_id
 AND r.guild_id IS NOT DISTINCT FROM NEW.guild_id AND r.authorization_generation=NEW.authorization_generation
 AND i.bot_id=NEW.bot_id AND i.owner_user_id=NEW.actor_id AND i.active AND i.disabled_at IS NULL
 AND i.version=NEW.identity_version AND i.authorization_generation=NEW.authorization_generation
 AND ((r.kind='p2p' AND r.identity_id=NEW.identity_id AND r.identity_version=NEW.identity_version AND i.private_channel_id=NEW.channel_id AND NEW.epoch_id IS NULL)
 OR(r.kind='group' AND r.epoch_id=NEW.epoch_id AND e.bot_id=NEW.bot_id AND e.channel_id=NEW.channel_id
 AND e.guild_id=NEW.guild_id AND e.enabled AND NOT e.paused AND e.public_reply_consent AND e.history_consent
 AND e.version=NEW.epoch_version AND e.authorization_generation=NEW.authorization_generation)) INTO valid
 FROM openfox.discord_routes r JOIN openfox.discord_identities i ON i.organization_id=r.organization_id AND i.id=NEW.identity_id
 LEFT JOIN openfox.discord_channel_epochs e ON e.organization_id=r.organization_id AND e.id=r.epoch_id
 WHERE r.organization_id=NEW.organization_id AND r.id=NEW.route_id;
 IF valid IS DISTINCT FROM true THEN RAISE EXCEPTION 'invalid discord input scope' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='REMOTE' THEN
 SELECT p.kind='MESSAGE' AND NOT p.source_deleted AND p.id=NEW.message_id AND p.inbound_id=NEW.inbound_id
 AND (p.bot_id,p.route_id,p.channel_id,p.remote_message_id,p.actor_id,p.actor_version,p.identity_id,p.identity_version,p.authorization_generation)
 = (NEW.bot_id,NEW.route_id,NEW.channel_id,NEW.remote_message_id,NEW.actor_id,NEW.actor_version,NEW.identity_id,NEW.identity_version,NEW.authorization_generation)
 AND p.guild_id IS NOT DISTINCT FROM NEW.guild_id AND p.epoch_id IS NOT DISTINCT FROM NEW.epoch_id AND p.epoch_version IS NOT DISTINCT FROM NEW.epoch_version INTO valid
 FROM openfox.discord_message_receipts p WHERE p.organization_id=NEW.organization_id AND p.id=NEW.receipt_id;
 IF valid IS DISTINCT FROM true THEN RAISE EXCEPTION 'invalid discord receipt' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER discord_input_scope BEFORE INSERT ON openfox.discord_inputs FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_discord_input_scope();
REVOKE ALL ON FUNCTION openfox_private.validate_discord_input_scope() FROM PUBLIC;

CREATE FUNCTION openfox_private.validate_discord_reply_source() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE valid boolean;source_snapshot openfox.discord_inputs%ROWTYPE;
BEGIN
 SELECT s.bot_id,s.route_id,s.actor_id,s.actor_version,s.identity_id,s.identity_version,s.authorization_generation,s.epoch_id,s.epoch_version
 INTO source_snapshot.bot_id,source_snapshot.route_id,source_snapshot.actor_id,source_snapshot.actor_version,source_snapshot.identity_id,source_snapshot.identity_version,source_snapshot.authorization_generation,source_snapshot.epoch_id,source_snapshot.epoch_version
 FROM openfox.discord_inputs s WHERE s.organization_id=NEW.organization_id AND s.id=NEW.snapshot_id;
 SELECT s.source_kind='REMOTE' AND ci.message_id=s.message_id AND ci.status='REJECTED' AND ci.run_id IS NULL
 AND m.author_kind='SYSTEM' AND m.conversation_id=ci.conversation_id AND r.conversation_id=m.conversation_id INTO valid
 FROM openfox.discord_inputs s JOIN openfox.discord_routes r ON (r.organization_id,r.id)=(s.organization_id,s.route_id)
 JOIN openfox.conversation_inputs ci ON ci.organization_id=s.organization_id AND ci.id=NEW.conversation_input_id
 JOIN openfox.messages m ON m.organization_id=s.organization_id AND m.id=NEW.message_id
 WHERE s.organization_id=NEW.organization_id AND s.id=NEW.snapshot_id;
 IF valid IS DISTINCT FROM true THEN RAISE EXCEPTION 'invalid discord reply source' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM openfox.discord_reply_sources rs JOIN openfox.discord_inputs s ON (s.organization_id,s.id)=(rs.organization_id,rs.snapshot_id)
 WHERE rs.organization_id=NEW.organization_id AND rs.message_id=NEW.message_id AND
 (rs.batch_size<>NEW.batch_size OR (s.bot_id,s.route_id,s.actor_id,s.actor_version,s.identity_id,s.identity_version,s.authorization_generation)
 IS DISTINCT FROM (source_snapshot.bot_id,source_snapshot.route_id,source_snapshot.actor_id,source_snapshot.actor_version,source_snapshot.identity_id,source_snapshot.identity_version,source_snapshot.authorization_generation)
 OR s.epoch_id IS DISTINCT FROM source_snapshot.epoch_id OR s.epoch_version IS DISTINCT FROM source_snapshot.epoch_version)) THEN
 RAISE EXCEPTION 'mixed discord reply batch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER discord_reply_source BEFORE INSERT ON openfox.discord_reply_sources FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_discord_reply_source();
REVOKE ALL ON FUNCTION openfox_private.validate_discord_reply_source() FROM PUBLIC;
CREATE FUNCTION openfox_private.validate_discord_reply_batch() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 IF (SELECT count(*) FROM openfox.discord_reply_sources WHERE organization_id=NEW.organization_id AND message_id=NEW.message_id)<>NEW.batch_size THEN
 RAISE EXCEPTION 'incomplete discord reply batch' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER discord_reply_batch AFTER INSERT ON openfox.discord_reply_sources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_discord_reply_batch();
REVOKE ALL ON FUNCTION openfox_private.validate_discord_reply_batch() FROM PUBLIC;

-- Serialize only queue admission. Never take a Bot row lock from a message
-- trigger (the conversation is already locked by PostMessage/Finalize).
CREATE FUNCTION openfox_private.limit_discord_reply_queue() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE pending integer;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM openfox.im_bots b JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 WHERE b.organization_id=NEW.organization_id AND b.id=NEW.bot_id AND c.provider='discord') THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.organization_id::text||':discord:reply-budget:'||NEW.bot_id::text,0));
 IF NEW.message_id IS NOT NULL AND EXISTS(SELECT 1 FROM openfox.im_reply_outbox WHERE organization_id=NEW.organization_id AND message_id=NEW.message_id) THEN RETURN NEW; END IF;
 SELECT count(*) INTO pending FROM(SELECT 1 FROM openfox.im_reply_outbox WHERE organization_id=NEW.organization_id AND bot_id=NEW.bot_id AND status IN ('PENDING','SENDING') LIMIT 1000) q;
 IF pending>=1000 THEN
  -- Preserve the local terminal message and expose a durable delivery failure.
  -- Queue saturation must not roll back task/Run completion.
  NEW.status:='FAILED';NEW.last_error:='queue_full';NEW.finished_at:=clock_timestamp();
  NEW.lease_owner:='';NEW.lease_expires_at:=NULL;
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.limit_discord_reply_queue() FROM PUBLIC;
CREATE TRIGGER discord_reply_queue_budget BEFORE INSERT ON openfox.im_reply_outbox FOR EACH ROW EXECUTE FUNCTION openfox_private.limit_discord_reply_queue();

DO $policies$
DECLARE table_name text;
BEGIN
 FOREACH table_name IN ARRAY ARRAY['discord_bot_controls','discord_connection_preparations','discord_gateway_sessions',
 'discord_identities','discord_binding_sessions','discord_channel_epochs','discord_routes','discord_message_receipts',
 'discord_inputs','discord_reply_sources','discord_replies','discord_rate_limits'] LOOP
 EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',table_name);
 EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',table_name);
 EXECUTE format('CREATE POLICY %I ON openfox.%I USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id())',table_name||'_tenant',table_name);
 END LOOP;
END $policies$;
RESET ROLE;
GRANT USAGE ON TYPE openfox.discord_snowflake TO openfox_app;
GRANT SELECT,INSERT,UPDATE ON openfox.discord_bot_controls,openfox.discord_connection_preparations,openfox.discord_gateway_sessions,
 openfox.discord_identities,openfox.discord_binding_sessions,openfox.discord_channel_epochs,openfox.discord_routes,openfox.discord_message_receipts,
 openfox.discord_replies,openfox.discord_rate_limits TO openfox_app;
GRANT DELETE ON openfox.discord_connection_preparations,openfox.discord_binding_sessions,openfox.discord_replies,openfox.discord_rate_limits TO openfox_app;
GRANT SELECT,INSERT ON openfox.discord_inputs,openfox.discord_reply_sources TO openfox_app;
-- Discord uses explicit original Run / rejected-batch sources. Preserve the
-- complete existing WeCom predicate and exclude Discord from latest-input routing.
SET ROLE openfox_migrator;
CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_reply()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,message_id,reply_to_remote_id,group_authorization_version)
 SELECT n.organization_id,b.bot_id,b.chat_id,n.id,
  coalesce((SELECT i.remote_message_id FROM openfox.im_inbound_messages i
   JOIN openfox.messages m ON m.organization_id=i.organization_id AND m.id=i.message_id
   WHERE i.organization_id=n.organization_id AND i.bot_id=b.bot_id AND i.message_id IS NOT NULL AND m.conversation_id=n.conversation_id
   ORDER BY m.seq DESC LIMIT 1),''), ga.version
 FROM changed_new n
 JOIN openfox.im_chat_bindings b ON b.organization_id=n.organization_id AND b.conversation_id=n.conversation_id
 JOIN openfox.im_bots bot ON bot.organization_id=b.organization_id AND bot.id=b.bot_id AND bot.removed_at IS NULL
 JOIN openfox.integration_connections c ON c.organization_id=bot.organization_id AND c.id=bot.connection_id AND c.status='ACTIVE'
 LEFT JOIN openfox.wecom_group_authorizations ga ON ga.organization_id=b.organization_id AND ga.bot_id=b.bot_id AND ga.chat_id=b.chat_id AND ga.conversation_id=b.conversation_id AND ga.enabled
 WHERE c.provider<>'discord' AND n.author_kind IN ('AGENT','SYSTEM') AND (c.provider<>'wecom' OR b.chat_type<>'group' OR ga.version IS NOT NULL)
 ON CONFLICT DO NOTHING;
 RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_reply() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_reply() FROM PUBLIC;
RESET ROLE;


-- Cross-tenant scanners expose identifiers only through existing bounded APIs.
SET ROLE openfox_migrator;
CREATE POLICY discord_controls_scan ON openfox.discord_bot_controls FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY discord_preparations_scan ON openfox.discord_connection_preparations FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY discord_binding_scan ON openfox.discord_binding_sessions FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY discord_rate_limits_scan ON openfox.discord_rate_limits FOR SELECT TO openfox_migrator USING(true);
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
        UNION
        SELECT t.organization_id FROM openfox.telegram_bot_controls t
         WHERE t.state<>'DISCONNECTED' AND ((t.next_attempt_at IS NOT NULL AND t.next_attempt_at<=clock_timestamp())
         OR (t.control_status='SENDING' AND t.control_lease_until<=clock_timestamp()))
        UNION
        SELECT p.organization_id FROM openfox.telegram_connection_preparations p
         WHERE (p.status='PENDING_CONFIRM' AND p.expires_at<=clock_timestamp()) OR p.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT s.organization_id FROM openfox.telegram_binding_sessions s
         WHERE (s.status IN ('WAITING','CANDIDATE') AND s.expires_at<=clock_timestamp()) OR s.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT g.organization_id FROM openfox.telegram_group_discoveries g WHERE g.expires_at<=clock_timestamp()
        UNION
        SELECT b.organization_id FROM openfox.telegram_background_messages b WHERE b.message_date<clock_timestamp()-interval '10 minutes'
        UNION
        SELECT m.organization_id FROM openfox.im_inbound_messages m JOIN openfox.telegram_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id
         WHERE m.finished_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q JOIN openfox.telegram_bot_controls t ON t.organization_id=q.organization_id AND t.bot_id=q.bot_id
         WHERE q.finished_at<clock_timestamp()-interval '30 days'
        UNION
        SELECT e.organization_id FROM openfox.im_inbound_events e JOIN openfox.telegram_bot_controls t ON t.organization_id=e.organization_id AND t.connection_id=e.connection_id
         WHERE e.received_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT p.organization_id FROM openfox.discord_connection_preparations p
         WHERE (p.status='PENDING_CONFIRM' AND p.expires_at<=clock_timestamp()) OR p.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT s.organization_id FROM openfox.discord_binding_sessions s
         WHERE (s.status IN ('WAITING','CANDIDATE') AND s.expires_at<=clock_timestamp()) OR s.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT r.organization_id FROM openfox.discord_rate_limits r WHERE r.not_before<clock_timestamp()-interval '1 hour'
        UNION
        SELECT m.organization_id FROM openfox.im_inbound_messages m JOIN openfox.discord_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id
         WHERE m.finished_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q JOIN openfox.discord_bot_controls t ON t.organization_id=q.organization_id AND t.bot_id=q.bot_id
         WHERE q.finished_at<clock_timestamp()-interval '30 days'
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) FROM PUBLIC;

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
     WHERE (c.provider IN ('feishu', 'lark', 'dingtalk', 'wecom') OR (c.provider='discord' AND EXISTS(SELECT 1 FROM openfox.discord_bot_controls t WHERE t.organization_id=c.organization_id AND t.connection_id=c.id AND t.state='ACTIVE'))) AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
       AND (p_after_organization IS NULL
            OR (c.organization_id, c.id) > (p_after_organization, coalesce(p_after_connection, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY c.organization_id, c.id
     LIMIT p_limit;
END $$;
ALTER FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) FROM PUBLIC;

RESET ROLE;
