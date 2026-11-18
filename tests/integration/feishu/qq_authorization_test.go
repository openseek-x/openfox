package feishuint

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
	"testing"
	"time"
)

// Real HTTP/PG/dispatch tests for a normal shared QQ Agent, never a QQ task assistant.
type qqGateWorld struct {
	*botWorld
	connection, bot, epoch, route, conversation, agent, ownerIdentity, memberIdentity string
}

func newQQGateWorld(t *testing.T) *qqGateWorld {
	b := &botWorld{world: setup(t)}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'qq-gate-device',clock_timestamp())`, b.deviceID, "qq-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	f := &qqGateWorld{botWorld: b, connection: b.newUUID(), bot: b.newUUID(), epoch: b.newUUID(), route: b.newUUID(), conversation: b.newUUID()}
	f.agent = b.createAgent(b.owner, b.foxSpace, "QQ ordinary agent", "SHARED")
	b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'qq','ORGANIZATION',$2,'QQ fixture',$3::uuid)`, b.org, f.connection, b.ownerID)
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'AGENT',$4::uuid,$5::uuid)`, b.org, f.bot, f.connection, f.agent, b.ownerID)
	b.exec(`INSERT INTO openfox.qq_bot_controls(organization_id,bot_id,connection_id,app_id,state,created_by,operation_key,request_hash,webhook_hash,url_verified_at,activated_at) VALUES($1::uuid,$2::uuid,$3::uuid,$2,'ACTIVE',$4::uuid,$2,decode(repeat('ab',32),'hex'),decode(md5($2::text)||md5($2::text),'hex'),now(),now())`, b.org, f.bot, f.connection, b.ownerID)
	proof := b.newUUID()
	b.exec(`INSERT INTO openfox.qq_message_receipts(organization_id,id,bot_id,generation,event_id,event_type,scope,chat_id,subject_id,remote_message_id,body_hash,sent_at,disposition) VALUES($1::uuid,$2::uuid,$3::uuid,1,$2,'GROUP_AT_MESSAGE_CREATE','GROUP','opaque-group','owner-subject',$2,decode(repeat('ab',32),'hex'),now(),'IGNORED')`, b.org, proof, f.bot)
	b.exec(`INSERT INTO openfox.qq_group_epochs(organization_id,id,bot_id,generation,chat_id,authorized_by,proof_receipt_id,public_reply_consent) VALUES($1::uuid,$2::uuid,$3::uuid,1,'opaque-group',$4::uuid,$5::uuid,true)`, b.org, f.epoch, f.bot, b.ownerID, proof)
	f.ownerIdentity = f.bindIdentity(b.ownerID, "owner-subject", "GROUP", "opaque-group", f.epoch)
	f.memberIdentity = f.bindIdentity(b.memberID, "member-subject", "GROUP", "opaque-group", f.epoch)
	b.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'SHARED','IM')`, b.org, f.conversation, b.foxSpace, f.agent, b.ownerID)
	b.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1::uuid,$2::uuid,$3::uuid,'COMMENTER'),($1::uuid,$2::uuid,$4::uuid,'COMMENTER')`, b.org, f.conversation, b.memberID, b.adminID)
	b.exec(`INSERT INTO openfox.qq_routes(organization_id,id,bot_id,generation,scope,chat_id,group_epoch_id,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,1,'GROUP','opaque-group',$4::uuid,$5::uuid)`, b.org, f.route, f.bot, f.epoch, f.conversation)
	b.exec(`INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES($1::uuid,$2::uuid,$3,'group',$4::uuid,$5::uuid)`, b.org, f.bot, f.route, f.conversation, b.ownerID)
	return f
}
func (f *qqGateWorld) bindIdentity(actor, subject, scope, chat, epoch string) string {
	id := f.newUUID()
	proof := "ADMIN_ASSIGNED"
	creator := f.ownerID
	if scope == "C2C" {
		proof = "PRIVATE_CODE"
		creator = actor
	}
	f.exec(`INSERT INTO openfox.qq_bindings(organization_id,id,bot_id,generation,group_epoch_id,scope,chat_id,subject_id,owner_user_id,proof_kind,created_by,accepted_at) VALUES($1::uuid,$2::uuid,$3::uuid,1,NULLIF($4,'')::uuid,$5,$6,$7,$8::uuid,$9,$10::uuid,now())`, f.org, id, f.bot, epoch, scope, chat, subject, actor, proof, creator)
	return id
}
func (f *qqGateWorld) rebindMember() {
	f.exec(`UPDATE openfox.qq_bindings SET active=false,revoked_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	f.memberIdentity = f.bindIdentity(f.memberID, "member-subject", "GROUP", "opaque-group", f.epoch)
}
func (f *qqGateWorld) actorTx(actor string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: actor, AuthorizationVersion: 1}
	return f.pool.RunForOrganization(f.ctx, f.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func (f *qqGateWorld) post(c *client, text string) string {
	f.t.Helper()
	var message struct {
		ID string `json:"id"`
	}
	c.json(201, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{
		"body": text, "client_message_id": f.newUUID(), "mentions": []map[string]string{{"agent_id": f.agent}},
	}, &message)
	return message.ID
}

func (f *qqGateWorld) materialize() (int, int) {
	f.t.Helper()
	store, err := dispatch.NewStore(dispatch.DefaultConfig())
	if err != nil {
		f.t.Fatal(err)
	}
	var made, rejected int
	err = f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		made, rejected, err = store.MaterializeInputs(ctx, tx, f.org, 50)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return made, rejected
}

func (f *qqGateWorld) running(actor string) toolRun {
	f.t.Helper()
	if made, rejected := f.materialize(); made != 1 || rejected != 0 {
		f.t.Fatalf("materialize=%d/%d", made, rejected)
	}
	r := toolRun{conversationID: f.conversation, agentID: f.agent, actorID: actor, role: "MEMBER"}
	if actor == f.ownerID {
		r.role = "OWNER"
	}
	f.scan(&r.runID, `SELECT id::text FROM openfox.runs WHERE organization_id=$1::uuid AND conversation_id=$2::uuid AND state='QUEUED'`, f.org, f.conversation)
	f.exec(`UPDATE openfox.runs SET state='RUNNING',started_at=now(),lease_expires_at=now()+interval '1 hour' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.runID)
	return r
}

func (f *qqGateWorld) mention(r toolRun, call, body string) error {
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	raw, _ := json.Marshal(map[string]string{"mention": f.ownerID, "body": body})
	return f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		out, err := store.CallTool(ctx, tx, f.org, f.deviceID, r.runID, "conversation_mention", dispatch.ToolCall{Generation: 1, CallID: call, Arguments: raw})
		if err == nil && !out.OK {
			f.t.Fatalf("mention: %s", out.Text)
		}
		return err
	})
}

func TestQQAuthorizationInputsAndActualTool(t *testing.T) {
	f := newQQGateWorld(t)
	first := f.post(f.owner, "owner input")
	member := f.post(f.member, "member input")
	if n := f.count(`SELECT count(DISTINCT identity_id) FROM openfox.qq_inputs WHERE organization_id=$1::uuid AND route_id=$2::uuid AND source='OPENFOX'`, f.org, f.route); n != 2 {
		t.Fatal(n)
	}
	f.admin.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "unbound", "client_message_id": f.newUUID()}, nil)
	f.ownerB.json(404, "POST", "/v1/organizations/"+f.orgB+"/conversations/"+f.conversation+"/messages", map[string]any{"body": "cross tenant", "client_message_id": f.newUUID()}, nil)
	for _, sql := range []string{`UPDATE openfox.qq_inputs SET identity_version=2 WHERE message_id=$1::uuid`, `DELETE FROM openfox.qq_inputs WHERE message_id=$1::uuid`} {
		if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, first); return err }); err == nil {
			t.Fatal("snapshot changed")
		}
	}
	run := f.running(f.ownerID)
	if err := f.mention(run, "first", "ordinary Agent authorized write"); err != nil {
		t.Fatal(err)
	}
	f.rebindMember()
	if err := f.mention(run, "other-rebind", "owner still authorized"); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at=now() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, run.runID)
	f.post(f.member, "new identity input")
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("old input %d/%d", made, rejected)
	}
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid AND status='REJECTED'`, f.org, member); n != 1 {
		t.Fatal(n)
	}
	run = f.running(f.memberID)
	f.exec(`UPDATE openfox.qq_bindings SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	if err := f.mention(run, "revoked", "must not write"); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatal(err)
	}
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	if err := f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.Context(ctx, tx, f.org, f.deviceID, run.runID, 1)
		return err
	}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatal(err)
	}
}

func (f *qqGateWorld) remoteInput() conversations.MessageInput {
	id, receipt := f.newUUID(), f.newUUID()
	f.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,message_type,text,status,lease_owner,lease_expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,'group',$6,'text','remote input','PROCESSING','fixture',now()+interval '1 minute')`, f.org, id, f.bot, receipt, f.route, f.memberIdentity)
	f.exec(`INSERT INTO openfox.qq_message_receipts(organization_id,id,bot_id,generation,event_id,event_type,scope,chat_id,subject_id,remote_message_id,body_hash,text,sent_at,inbound_id,route_id,actor_id,actor_version,identity_id,identity_version,group_epoch_id,epoch_version,disposition) VALUES($1::uuid,$2::uuid,$3::uuid,1,$2,'GROUP_AT_MESSAGE_CREATE','GROUP','opaque-group','member-subject',$2,decode(repeat('ab',32),'hex'),'remote input',now(),$4::uuid,$5::uuid,$6::uuid,1,$7::uuid,1,$8::uuid,1,'QUEUED')`, f.org, receipt, f.bot, id, f.route, f.memberID, f.memberIdentity, f.epoch)
	return conversations.MessageInput{ID: id, Body: "remote input", ClientMessageID: "qq:" + f.bot + ":" + id, Mentions: []conversations.Mention{{AgentID: f.agent}}}
}
func (f *qqGateWorld) postRemote(input conversations.MessageInput, insert bool) error {
	return f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		if insert {
			_, err := tx.Exec(ctx, `INSERT INTO openfox.qq_inputs(organization_id,message_id,source,route_id,actor_id,actor_version,identity_id,identity_version,group_epoch_id,epoch_version,generation,receipt_id) SELECT organization_id,inbound_id,'REMOTE',route_id,actor_id,actor_version,identity_id,identity_version,group_epoch_id,epoch_version,generation,id FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND inbound_id=$1::uuid`, input.ID)
			if err != nil {
				return err
			}
		}
		_, err := (conversations.Service{}).PostMessage(ctx, tx, f.org, f.conversation, conversations.Actor{UserID: f.memberID, Role: membership.RoleMember, AuthorizationVersion: 1}, input)
		return err
	})
}
func TestQQAuthorizationRemoteProofReplayAndBatch(t *testing.T) {
	f := newQQGateWorld(t)
	input := f.remoteInput()
	// JSON cannot set trusted provenance. Reject reserved IDs before duplicate lookup.
	f.member.json(400, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": input.Body, "client_message_id": input.ClientMessageID}, nil)
	if err := f.postRemote(input, false); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("remote without snapshot = %v", err)
	}
	if err := f.postRemote(input, true); err != nil {
		t.Fatal(err)
	}
	if err := f.postRemote(input, false); err != nil {
		t.Fatalf("valid replay %v", err)
	}
	f.member.json(400, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": input.Body, "client_message_id": input.ClientMessageID}, nil)
	conflict := input
	conflict.ID = f.newUUID()
	if err := f.postRemote(conflict, false); !errors.Is(err, conversations.ErrDuplicateMessage) {
		t.Fatalf("wrong stable ID %v", err)
	}
	// A valid immutable row does not conceal an inconsistent real candidate.
	f.exec(`UPDATE openfox.qq_message_receipts SET subject_id='different-subject' WHERE organization_id=$1::uuid AND inbound_id=$2::uuid`, f.org, input.ID)
	if err := f.postRemote(input, false); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("mismatched receipt %v", err)
	}
	f.exec(`UPDATE openfox.qq_message_receipts SET subject_id='member-subject' WHERE organization_id=$1::uuid AND inbound_id=$2::uuid`, f.org, input.ID)
	native := f.post(f.member, "native input stays local")
	var nativeClientID string
	f.scan(&nativeClientID, `SELECT client_message_id FROM openfox.messages WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, native)
	f.member.json(201, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{
		"body": "native input stays local", "client_message_id": nativeClientID, "mentions": []map[string]string{{"agent_id": f.agent}},
	}, nil)
	r := f.running(f.memberID)
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND run_id=$2::uuid`, f.org, r.runID); n != 1 {
		t.Fatalf("mixed sources merged %d", n)
	}
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid AND status='PENDING'`, f.org, native); n != 1 {
		t.Fatal("native not preserved for next Run")
	}
	// Queue retention cannot erase the permanent authorization evidence.
	f.exec(`DELETE FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckQQRun(ctx, tx, f.conversation, f.memberID, r.runID)
	}); err != nil {
		t.Fatalf("retention invalidated Run %v", err)
	}
	f.rebindMember()
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckQQRun(ctx, tx, f.conversation, f.memberID, r.runID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("rebind revived Run %v", err)
	}
}

func TestQQAuthorizationConcurrentToolAndTerminalProof(t *testing.T) {
	f := newQQGateWorld(t)
	f.post(f.member, "perform an authorized mention")
	run := f.running(f.memberID)
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
			store, _ := dispatch.NewStore(dispatch.DefaultConfig())
			raw, _ := json.Marshal(map[string]string{"mention": f.ownerID, "body": "admitted write"})
			out, err := store.CallTool(ctx, tx, f.org, f.deviceID, run.runID, "conversation_mention", dispatch.ToolCall{Generation: 1, CallID: "held", Arguments: raw})
			if err != nil {
				return err
			}
			if !out.OK {
				return errors.New(out.Text)
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("tool not admitted")
	}
	revoked := make(chan error, 1)
	go func() {
		revoked <- f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE openfox.qq_bindings SET active=false,revoked_at=now(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, f.memberIdentity)
			return err
		})
	}()
	waitFor(t, "QQ actual tool holds identity gate", func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE openfox.qq_bindings%'`) > 0
	})
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := f.mention(run, "late", "MUST NOT WRITE"); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND source_run_id=$2::uuid AND NOT source_terminal`, f.org, run.runID); n != 1 {
		t.Fatalf("intermediate proof %d", n)
	}
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=now() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND state='RUNNING'`, run.runID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("terminal Run CAS failed")
		}
		_, err = dispatch.FinalizeConversation(ctx, tx, execution.Run{ID: run.runID, ConversationID: f.conversation, AgentID: f.agent, ActorID: f.memberID, State: execution.StateSucceeded}, dispatch.AuthorAgent, "terminal result", false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND source_run_id=$2::uuid AND source_terminal`, f.org, run.runID); n != 1 {
		t.Fatalf("terminal proof %d", n)
	}
}

func TestQQAuthorizationEpochPrivateAndControl(t *testing.T) {
	f := newQQGateWorld(t)
	f.post(f.member, "before epoch version change")
	f.exec(`UPDATE openfox.qq_group_epochs SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("epoch version ignored %d/%d", made, rejected)
	}
	f.post(f.member, "before membership version change")
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='ADMIN' WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid`, f.memberID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("member version ignored %d/%d", made, rejected)
	}
	oldConversation, oldEpoch := f.conversation, f.epoch
	f.exec(`UPDATE openfox.qq_group_epochs SET disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, oldEpoch)
	f.exec(`UPDATE openfox.qq_routes SET closed_at=now() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.route)
	f.epoch, f.route, f.conversation = f.newUUID(), f.newUUID(), f.newUUID()
	f.exec(`INSERT INTO openfox.qq_group_epochs(organization_id,id,bot_id,generation,chat_id,authorized_by,proof_receipt_id,public_reply_consent) SELECT organization_id,$2::uuid,bot_id,generation,chat_id,authorized_by,proof_receipt_id,true FROM openfox.qq_group_epochs WHERE organization_id=$1::uuid AND id=$3::uuid`, f.org, f.epoch, oldEpoch)
	f.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'SHARED','IM')`, f.org, f.conversation, f.foxSpace, f.agent, f.ownerID)
	f.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1::uuid,$2::uuid,$3::uuid,'COMMENTER')`, f.org, f.conversation, f.memberID)
	f.exec(`INSERT INTO openfox.qq_routes(organization_id,id,bot_id,generation,scope,chat_id,group_epoch_id,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,1,'GROUP','opaque-group',$4::uuid,$5::uuid)`, f.org, f.route, f.bot, f.epoch, f.conversation)
	f.member.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "old group assignment", "client_message_id": f.newUUID()}, nil)
	f.rebindMember()
	f.post(f.member, "new group assignment accepted")
	f.member.json(403, "POST", f.path("/conversations/"+oldConversation+"/messages"), map[string]any{"body": "closed group remains closed", "client_message_id": f.newUUID()}, nil)
	// C2C and GROUP subjects are separate identities. The same person must not
	// inherit an old private route after re-binding.
	privateIdentity := f.bindIdentity(f.ownerID, "owner-subject", "C2C", "owner-subject", "")
	f.route, f.conversation = f.newUUID(), f.newUUID()
	f.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'PRIVATE','IM')`, f.org, f.conversation, f.foxSpace, f.agent, f.ownerID)
	f.exec(`INSERT INTO openfox.qq_routes(organization_id,id,bot_id,generation,scope,chat_id,identity_id,identity_version,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,1,'C2C','owner-subject',$4::uuid,1,$5::uuid)`, f.org, f.route, f.bot, privateIdentity, f.conversation)
	f.post(f.owner, "private owner input")
	f.member.json(404, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "another user's private chat", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.qq_bindings SET active=false,revoked_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, privateIdentity)
	f.bindIdentity(f.ownerID, "owner-subject", "C2C", "owner-subject", "")
	f.owner.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "rebind cannot inherit private history", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.qq_bot_controls SET generation=2 WHERE organization_id=$1::uuid AND bot_id=$2::uuid`, f.org, f.bot)
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.LockQQInvocation(ctx, tx, f.conversation, f.ownerID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("generation ignored %v", err)
	}
}
