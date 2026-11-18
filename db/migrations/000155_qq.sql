-- I14 QQ official Agent bots: typed identities, explicit group consent and
-- durable one-shot passive replies. No historic data backfill.
-- Rollback is local disable/drain only: retain app claims, receipts and UNKNOWN
-- outcomes. An older generic IM sender must never claim these QQ rows.
SET ROLE openfox_migrator;
-- Only dispatch's authoritative terminal insertion sets this flag together with
-- source_run_id; intermediate social/proposal messages remain false.
ALTER TABLE openfox.im_reply_outbox ADD COLUMN source_terminal boolean NOT NULL DEFAULT false;
ALTER TABLE openfox.im_reply_outbox ADD CONSTRAINT im_reply_source_terminal_valid CHECK(NOT source_terminal OR source_run_id IS NOT NULL);

CREATE TABLE openfox.qq_bot_controls (
 organization_id uuid NOT NULL,
 bot_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 app_id text CHECK(octet_length(app_id) BETWEEN 1 AND 1024),
 state text NOT NULL DEFAULT 'PREPARED' CHECK(state IN ('PREPARED','VALIDATING','WAITING_WEBHOOK','ACTIVE','NEEDS_REAUTH','DISCONNECTED','CANCELLED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 credential_version bigint NOT NULL DEFAULT 1 CHECK(credential_version>0),
 created_by uuid NOT NULL,
 operation_key text NOT NULL CHECK(char_length(operation_key) BETWEEN 16 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 webhook_hash bytea UNIQUE CHECK(octet_length(webhook_hash)=32),
 webhook_sealed bytea CHECK(octet_length(webhook_sealed)<=2048),
 webhook_key_id text NOT NULL DEFAULT '',
 url_verified_at timestamptz,
 activated_at timestamptz,
 token_sealed bytea CHECK(octet_length(token_sealed)<=16384),
 token_key_id text NOT NULL DEFAULT '',
 token_expires_at timestamptz,
 lease_owner text NOT NULL DEFAULT '' CHECK(char_length(lease_owner)<=128),
 lease_expires_at timestamptz,
 refresh_lease_owner text NOT NULL DEFAULT '' CHECK(char_length(refresh_lease_owner)<=128),
 refresh_lease_expires_at timestamptz,
 refresh_after timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,bot_id),
 UNIQUE(organization_id,connection_id),
 UNIQUE(organization_id,created_by,operation_key),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK(state<>'ACTIVE' OR (app_id IS NOT NULL AND url_verified_at IS NOT NULL AND activated_at IS NOT NULL AND webhook_hash IS NOT NULL))
);
-- The verified AppID claim survives disconnect/removal. An unverified candidate
-- never claims this key; token validation and its CAS completion precede claim.
CREATE UNIQUE INDEX qq_app_claim_unique ON openfox.qq_bot_controls(app_id) WHERE app_id IS NOT NULL;
CREATE INDEX qq_controls_refresh_due ON openfox.qq_bot_controls(organization_id,refresh_after) WHERE refresh_after IS NOT NULL;

CREATE TABLE openfox.qq_qr_sessions (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),
 generation bigint NOT NULL CHECK(generation>0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 operation_key text NOT NULL CHECK(char_length(operation_key) BETWEEN 16 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','POLLING','WAITING_SCAN','DONE','UNKNOWN','CANCELLED','EXPIRED','FAILED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 qr_url text NOT NULL DEFAULT '' CHECK(octet_length(qr_url)<=8192),
 candidate_sealed bytea CHECK(octet_length(candidate_sealed)<=16384),
 candidate_key_id text NOT NULL DEFAULT '',
 lease_owner text NOT NULL DEFAULT '' CHECK(char_length(lease_owner)<=128),
 lease_expires_at timestamptz,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,actor_id,operation_key),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE UNIQUE INDEX qq_qr_bot_active_unique ON openfox.qq_qr_sessions(organization_id,bot_id) WHERE status IN ('PENDING','POLLING','WAITING_SCAN');
CREATE INDEX qq_qr_due ON openfox.qq_qr_sessions(organization_id,created_at) WHERE status IN ('PENDING','POLLING','WAITING_SCAN');

CREATE TABLE openfox.qq_message_receipts (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 event_id text NOT NULL CHECK(octet_length(event_id) BETWEEN 1 AND 1024),
 event_type text NOT NULL CHECK(event_type IN ('C2C_MESSAGE_CREATE','GROUP_AT_MESSAGE_CREATE','FRIEND_DEL','GROUP_DEL_ROBOT')),
 scope text NOT NULL CHECK(scope IN ('C2C','GROUP')),
 chat_id text NOT NULL CHECK(octet_length(chat_id) BETWEEN 1 AND 1024),
 subject_id text NOT NULL CHECK(octet_length(subject_id) BETWEEN 1 AND 1024),
 remote_message_id text NOT NULL CHECK(octet_length(remote_message_id) BETWEEN 1 AND 1024),
 message_index text NOT NULL DEFAULT '' CHECK(octet_length(message_index)<=1024),
 member_role text NOT NULL DEFAULT '' CHECK(char_length(member_role)<=32),
 body_hash bytea NOT NULL CHECK(octet_length(body_hash)=32),
 text text NOT NULL DEFAULT '' CHECK(char_length(text)<=8000),
 sent_at timestamptz NOT NULL,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 inbound_id uuid,
 route_id uuid,
 actor_id uuid,
 actor_version bigint CHECK(actor_version>0),
 identity_id uuid,
 identity_version bigint CHECK(identity_version>0),
 group_epoch_id uuid,
 epoch_version bigint CHECK(epoch_version>0),
 disposition text NOT NULL DEFAULT 'PENDING' CHECK(disposition IN ('PENDING','BINDING','QUEUED','DONE','IGNORED','FAILED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,bot_id,event_id),
 UNIQUE(organization_id,bot_id,scope,chat_id,remote_message_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL (inbound_id),
 CHECK((scope='C2C' AND event_type IN ('C2C_MESSAGE_CREATE','FRIEND_DEL') AND chat_id=subject_id) OR (scope='GROUP' AND event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_DEL_ROBOT')))
);
CREATE INDEX qq_receipts_pending ON openfox.qq_message_receipts(organization_id,received_at) WHERE disposition='PENDING';
CREATE INDEX qq_receipts_inbound ON openfox.qq_message_receipts(organization_id,inbound_id) WHERE inbound_id IS NOT NULL;

CREATE TABLE openfox.qq_group_epochs (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 chat_id text NOT NULL CHECK(octet_length(chat_id) BETWEEN 1 AND 1024),
 authorized_by uuid NOT NULL,
 proof_receipt_id uuid NOT NULL,
 public_reply_consent boolean NOT NULL CHECK(public_reply_consent),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,authorized_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,proof_receipt_id) REFERENCES openfox.qq_message_receipts(organization_id,id)
);
CREATE UNIQUE INDEX qq_group_current_unique ON openfox.qq_group_epochs(organization_id,bot_id,chat_id) WHERE disabled_at IS NULL;

CREATE TABLE openfox.qq_bindings (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 group_epoch_id uuid,
 scope text NOT NULL CHECK(scope IN ('C2C','GROUP')),
 chat_id text NOT NULL CHECK(octet_length(chat_id) BETWEEN 1 AND 1024),
 subject_id text NOT NULL CHECK(octet_length(subject_id) BETWEEN 1 AND 1024),
 owner_user_id uuid NOT NULL,
 proof_kind text NOT NULL CHECK(proof_kind IN ('PRIVATE_CODE','ADMIN_ASSIGNED')),
 created_by uuid NOT NULL,
 accepted_at timestamptz NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id),
 CHECK((scope='C2C' AND proof_kind='PRIVATE_CODE' AND chat_id=subject_id AND created_by=owner_user_id AND group_epoch_id IS NULL) OR (scope='GROUP' AND proof_kind='ADMIN_ASSIGNED' AND group_epoch_id IS NOT NULL)),
 CHECK(active=(revoked_at IS NULL))
);
CREATE UNIQUE INDEX qq_binding_subject_unique ON openfox.qq_bindings(organization_id,bot_id,scope,chat_id,subject_id) WHERE active;
CREATE UNIQUE INDEX qq_binding_owner_unique ON openfox.qq_bindings(organization_id,bot_id,scope,(CASE WHEN scope='GROUP' THEN chat_id ELSE '' END),owner_user_id) WHERE active;

CREATE FUNCTION openfox_private.qq_identity_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF (NEW.organization_id,NEW.id,NEW.bot_id,NEW.generation,NEW.scope,NEW.chat_id,NEW.subject_id,NEW.owner_user_id,NEW.group_epoch_id,NEW.proof_kind,NEW.created_by,NEW.accepted_at)
 IS DISTINCT FROM (OLD.organization_id,OLD.id,OLD.bot_id,OLD.generation,OLD.scope,OLD.chat_id,OLD.subject_id,OLD.owner_user_id,OLD.group_epoch_id,OLD.proof_kind,OLD.created_by,OLD.accepted_at) THEN
 RAISE EXCEPTION 'QQ identity subject is immutable' USING ERRCODE='23514';
 END IF;
 IF NOT OLD.active AND NEW.active THEN RAISE EXCEPTION 'QQ identity cannot be revived' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER qq_identity_immutable BEFORE UPDATE ON openfox.qq_bindings FOR EACH ROW EXECUTE FUNCTION openfox_private.qq_identity_immutable();
REVOKE ALL ON FUNCTION openfox_private.qq_identity_immutable() FROM PUBLIC;

CREATE TABLE openfox.qq_binding_sessions (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 purpose text NOT NULL CHECK(purpose IN ('PRIVATE_CODE','GROUP_DISCOVERY','GROUP_ASSIGN')),
 created_by uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),
 owner_user_id uuid NOT NULL,
 operation_key text NOT NULL CHECK(char_length(operation_key) BETWEEN 16 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 code_hash bytea UNIQUE CHECK(octet_length(code_hash)=32),
 code_sealed bytea CHECK(octet_length(code_sealed)<=2048),
 code_key_id text NOT NULL DEFAULT '',
 receipt_id uuid,
 group_epoch_id uuid,
 binding_id uuid,
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','OBSERVED','CONFIRMED','CANCELLED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,created_by,operation_key),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,receipt_id) REFERENCES openfox.qq_message_receipts(organization_id,id),
 FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id),
 FOREIGN KEY(organization_id,binding_id) REFERENCES openfox.qq_bindings(organization_id,id),
 CHECK((purpose IN ('PRIVATE_CODE','GROUP_DISCOVERY') AND code_hash IS NOT NULL AND created_by=owner_user_id) OR (purpose='GROUP_ASSIGN' AND code_hash IS NULL AND code_sealed IS NULL AND receipt_id IS NOT NULL AND group_epoch_id IS NOT NULL))
);
CREATE INDEX qq_binding_sessions_expiry ON openfox.qq_binding_sessions(organization_id,expires_at) WHERE status IN ('PENDING','OBSERVED');

CREATE TABLE openfox.qq_routes (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 scope text NOT NULL CHECK(scope IN ('C2C','GROUP')),
 chat_id text NOT NULL CHECK(octet_length(chat_id) BETWEEN 1 AND 1024),
 identity_id uuid,
 identity_version bigint,
 group_epoch_id uuid,
 conversation_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 closed_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,conversation_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.qq_bindings(organization_id,id),
 FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 CHECK((scope='C2C' AND identity_id IS NOT NULL AND identity_version IS NOT NULL AND identity_version>0 AND group_epoch_id IS NULL) OR (scope='GROUP' AND identity_id IS NULL AND identity_version IS NULL AND group_epoch_id IS NOT NULL))
);
CREATE UNIQUE INDEX qq_route_live_unique ON openfox.qq_routes(organization_id,bot_id,generation,scope,chat_id) WHERE closed_at IS NULL;

ALTER TABLE openfox.qq_message_receipts
 ADD FOREIGN KEY(organization_id,route_id) REFERENCES openfox.qq_routes(organization_id,id),
 ADD FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 ADD FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.qq_bindings(organization_id,id),
 ADD FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id),
 ADD CHECK(disposition NOT IN ('QUEUED','DONE') OR (route_id IS NOT NULL AND actor_id IS NOT NULL AND actor_version IS NOT NULL AND identity_id IS NOT NULL AND identity_version IS NOT NULL)),
 ADD CHECK((group_epoch_id IS NULL)=(epoch_version IS NULL));

CREATE TABLE openfox.qq_inputs (
 organization_id uuid NOT NULL,
 message_id uuid NOT NULL,
 route_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 source text NOT NULL CHECK(source IN ('REMOTE','OPENFOX')),
 actor_version bigint NOT NULL CHECK(actor_version>0),
 identity_id uuid NOT NULL,
 identity_version bigint NOT NULL CHECK(identity_version>0),
 group_epoch_id uuid,
 epoch_version bigint,
 generation bigint NOT NULL CHECK(generation>0),
 receipt_id uuid,
 PRIMARY KEY(organization_id,message_id),
 UNIQUE(organization_id,receipt_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.qq_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.qq_bindings(organization_id,id),
 FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id),
 FOREIGN KEY(organization_id,receipt_id) REFERENCES openfox.qq_message_receipts(organization_id,id),
 CHECK((source='REMOTE')=(receipt_id IS NOT NULL)),
 CHECK((group_epoch_id IS NULL)=(epoch_version IS NULL)),
 CHECK(epoch_version>0)
);
CREATE INDEX qq_inputs_identity_idx ON openfox.qq_inputs(organization_id,identity_id);

-- Snapshots are immutable application evidence, inserted before the message
-- under the caller's ordered authorization locks. This trigger independently
-- checks references; it does not acquire parent/Run locks in a reverse order.
CREATE FUNCTION openfox_private.validate_qq_input_scope() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 IF NOT EXISTS (
 SELECT 1 FROM openfox.qq_routes r
 JOIN openfox.qq_bot_controls c ON c.organization_id=r.organization_id AND c.bot_id=r.bot_id
 JOIN openfox.im_bots b ON b.organization_id=c.organization_id AND b.id=c.bot_id AND b.connection_id=c.connection_id AND b.removed_at IS NULL
 JOIN openfox.integration_connections a ON a.organization_id=c.organization_id AND a.id=c.connection_id AND a.provider='qq' AND a.status='ACTIVE'
 JOIN openfox.qq_bindings i ON i.organization_id=r.organization_id AND i.id=NEW.identity_id
 JOIN openfox.organization_members m ON m.organization_id=r.organization_id AND m.user_id=NEW.actor_id AND m.status='ACTIVE' AND m.role<>'GUEST' AND m.authorization_version=NEW.actor_version
 LEFT JOIN openfox.qq_group_epochs e ON e.organization_id=r.organization_id AND e.id=r.group_epoch_id
 WHERE r.organization_id=NEW.organization_id AND r.id=NEW.route_id AND r.closed_at IS NULL
 AND c.state='ACTIVE' AND c.generation=NEW.generation AND r.generation=NEW.generation
 AND i.active AND i.owner_user_id=NEW.actor_id AND i.version=NEW.identity_version AND i.generation=NEW.generation
 AND i.bot_id=r.bot_id AND i.scope=r.scope AND i.chat_id=r.chat_id
 AND ((r.scope='C2C' AND r.identity_id=i.id AND r.identity_version=i.version AND NEW.group_epoch_id IS NULL)
 OR (r.scope='GROUP' AND e.id=NEW.group_epoch_id AND e.id=i.group_epoch_id AND e.bot_id=r.bot_id AND e.chat_id=r.chat_id
 AND e.generation=NEW.generation AND e.version=NEW.epoch_version AND e.disabled_at IS NULL AND e.public_reply_consent))) THEN
 RAISE EXCEPTION 'invalid QQ input authorization' USING ERRCODE='23514';
 END IF;
 IF NEW.source='REMOTE' AND NOT EXISTS (
 SELECT 1 FROM openfox.qq_message_receipts p
 JOIN openfox.im_inbound_messages q ON q.organization_id=p.organization_id AND q.id=p.inbound_id
 JOIN openfox.qq_routes r ON r.organization_id=p.organization_id AND r.id=p.route_id
 WHERE p.organization_id=NEW.organization_id AND p.id=NEW.receipt_id AND p.disposition='QUEUED'
 AND p.route_id=NEW.route_id AND p.generation=NEW.generation AND p.actor_id=NEW.actor_id AND p.actor_version=NEW.actor_version
 AND p.identity_id=NEW.identity_id AND p.identity_version=NEW.identity_version
 AND p.group_epoch_id IS NOT DISTINCT FROM NEW.group_epoch_id AND p.epoch_version IS NOT DISTINCT FROM NEW.epoch_version
 AND q.id=NEW.message_id AND q.bot_id=p.bot_id AND q.chat_id=r.id::text AND q.remote_message_id=p.id::text
 AND q.status='PROCESSING') THEN
 RAISE EXCEPTION 'invalid QQ remote input source' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER qq_input_scope BEFORE INSERT ON openfox.qq_inputs FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_qq_input_scope();
REVOKE ALL ON FUNCTION openfox_private.validate_qq_input_scope() FROM PUBLIC;

CREATE TABLE openfox.qq_replies (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL,
 route_id uuid NOT NULL,
 outbox_id uuid NOT NULL,
 run_id uuid NOT NULL,
 receipt_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 credential_version bigint NOT NULL CHECK(credential_version>0),
 identity_id uuid NOT NULL,
 identity_version bigint NOT NULL CHECK(identity_version>0),
 group_epoch_id uuid,
 sequence integer NOT NULL DEFAULT 1 CHECK(sequence=1),
 body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 8000),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 deadline timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','DONE','UNKNOWN','FAILED','SKIPPED','EXPIRED')),
 status_reason text NOT NULL DEFAULT '' CHECK(char_length(status_reason)<=100),
 remote_message_id text NOT NULL DEFAULT '' CHECK(octet_length(remote_message_id)<=1024),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 10),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease_owner text NOT NULL DEFAULT '' CHECK(char_length(lease_owner)<=128),
 lease_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,outbox_id),
 UNIQUE(organization_id,receipt_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.qq_bot_controls(organization_id,bot_id),
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.qq_routes(organization_id,id),
 FOREIGN KEY(organization_id,outbox_id) REFERENCES openfox.im_reply_outbox(organization_id,id),
 FOREIGN KEY(organization_id,run_id) REFERENCES openfox.runs(organization_id,id),
 FOREIGN KEY(organization_id,receipt_id) REFERENCES openfox.qq_message_receipts(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.qq_bindings(organization_id,id),
 FOREIGN KEY(organization_id,group_epoch_id) REFERENCES openfox.qq_group_epochs(organization_id,id)
);
CREATE INDEX qq_replies_due ON openfox.qq_replies(organization_id,next_attempt_at) WHERE state IN ('PENDING','SENDING');

ALTER TABLE openfox.qq_bot_controls ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_bot_controls FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_bot_controls_tenant ON openfox.qq_bot_controls
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_bot_controls TO openfox_app;

ALTER TABLE openfox.qq_qr_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_qr_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_qr_sessions_tenant ON openfox.qq_qr_sessions
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_qr_sessions TO openfox_app;

ALTER TABLE openfox.qq_message_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_message_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_message_receipts_tenant ON openfox.qq_message_receipts
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_message_receipts TO openfox_app;

ALTER TABLE openfox.qq_group_epochs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_group_epochs FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_group_epochs_tenant ON openfox.qq_group_epochs
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_group_epochs TO openfox_app;

ALTER TABLE openfox.qq_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_bindings_tenant ON openfox.qq_bindings
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_bindings TO openfox_app;

ALTER TABLE openfox.qq_binding_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_binding_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_binding_sessions_tenant ON openfox.qq_binding_sessions
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_binding_sessions TO openfox_app;

ALTER TABLE openfox.qq_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_routes_tenant ON openfox.qq_routes
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_routes TO openfox_app;

ALTER TABLE openfox.qq_inputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_inputs FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_inputs_tenant ON openfox.qq_inputs
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT ON openfox.qq_inputs TO openfox_app;

ALTER TABLE openfox.qq_replies ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.qq_replies FORCE ROW LEVEL SECURITY;
CREATE POLICY qq_replies_tenant ON openfox.qq_replies
 USING(organization_id=openfox_private.current_organization_id())
 WITH CHECK(organization_id=openfox_private.current_organization_id());
GRANT SELECT,INSERT,UPDATE ON openfox.qq_replies TO openfox_app;

CREATE POLICY qq_controls_router_read ON openfox.qq_bot_controls FOR SELECT TO openfox_migrator USING(true);
CREATE FUNCTION openfox_private.qq_webhook_route(p_hash bytea)
RETURNS TABLE(organization_id uuid,bot_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT q.organization_id,q.bot_id FROM openfox.qq_bot_controls q
 JOIN openfox.organization_directory d ON d.organization_id=q.organization_id AND d.deleted_at IS NULL
 WHERE q.webhook_hash=p_hash AND q.state IN ('WAITING_WEBHOOK','ACTIVE') LIMIT 1
$$;
REVOKE ALL ON FUNCTION openfox_private.qq_webhook_route(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.qq_webhook_route(bytea) TO openfox_context;
RESET ROLE;
