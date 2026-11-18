package feishuint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
	"openfox/internal/spacetools"
	"openfox/internal/tenancy"
)

// Real authenticated HTTP, database tenancy, dispatch and task tools. Telegram
// transport is outside this gate test; no external service is called.
type telegramGateWorld struct {
	*botWorld
	connection, bot, epoch, route, conversation, agent string
	ownerIdentity, memberIdentity                      string
}

func newTelegramGateWorld(t *testing.T) *telegramGateWorld {
	b := &botWorld{world: setup(t)}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'telegram-gate-device',clock_timestamp())`, b.deviceID, "telegram-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	f := &telegramGateWorld{botWorld: b, connection: b.newUUID(), bot: b.newUUID(), epoch: b.newUUID(), route: b.newUUID(), conversation: b.newUUID()}
	f.agent = b.createAgent(b.owner, b.foxSpace, "Telegram task assistant", "SHARED")
	b.exec(`UPDATE openfox.agents SET builtin_kind='TASK_ASSISTANT',allow_assign=false WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, f.agent)
	b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'telegram','ORGANIZATION',$2,'Telegram fixture',$3::uuid)`, b.org, f.connection, b.ownerID)
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'TASK_ASSISTANT',$4::uuid,$5::uuid,$6::uuid)`, b.org, f.bot, f.connection, f.agent, b.foxSpace, b.ownerID)
	var remote int64
	b.scan(&remote, `SELECT COALESCE(max(remote_bot_id),9000)+1 FROM openfox.telegram_bot_controls`)
	b.exec(`INSERT INTO openfox.telegram_bot_controls(organization_id,bot_id,connection_id,remote_bot_id,username,kind,target_space_id,state,route_hash,webhook_sealed,managed_by,managed_version)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,'fixture_bot','TASK_ASSISTANT',$5::uuid,'ACTIVE',decode(md5($2::text)||md5($2::text),'hex'),decode('aa','hex'),$6::uuid,1)`, b.org, f.bot, f.connection, remote, b.foxSpace, b.ownerID)
	f.ownerIdentity = f.bindIdentity(b.ownerID, 1001)
	f.memberIdentity = f.bindIdentity(b.memberID, 1002)
	b.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'SHARED','IM')`, b.org, f.conversation, b.foxSpace, f.agent, b.ownerID)
	b.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1::uuid,$2::uuid,$3::uuid,'COMMENTER'),($1::uuid,$2::uuid,$4::uuid,'COMMENTER')`, b.org, f.conversation, b.memberID, b.adminID)
	b.exec(`INSERT INTO openfox.telegram_group_epochs(organization_id,id,bot_id,chat_id,title,authorization_generation,public_reply_consent,authorized_by,identity_id,identity_version)
 VALUES($1::uuid,$2::uuid,$3::uuid,-100,'Fixture',1,true,$4::uuid,$5::uuid,1)`, b.org, f.epoch, f.bot, b.ownerID, f.ownerIdentity)
	b.exec(`INSERT INTO openfox.telegram_routes(organization_id,id,bot_id,chat_id,kind,conversation_id,epoch_id,authorization_generation) VALUES($1::uuid,$2::uuid,$3::uuid,-100,'group',$4::uuid,$5::uuid,1)`, b.org, f.route, f.bot, f.conversation, f.epoch)
	return f
}
func (f *telegramGateWorld) bindIdentity(actor string, remote int64) string {
	id := f.newUUID()
	f.exec(`INSERT INTO openfox.telegram_identities(organization_id,id,bot_id,owner_user_id,remote_user_id,private_chat_id,display_name,authorization_generation) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$5,'Fixture',1)`, f.org, id, f.bot, actor, remote)
	return id
}
func (f *telegramGateWorld) rebindMember() {
	f.exec(`UPDATE openfox.telegram_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	f.memberIdentity = f.bindIdentity(f.memberID, 1002)
}
func (f *telegramGateWorld) actorTx(actor string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: actor, AuthorizationVersion: 1}
	return f.pool.RunForOrganization(f.ctx, f.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func (f *telegramGateWorld) post(c *client, text string) string {
	f.t.Helper()
	var message struct {
		ID string `json:"id"`
	}
	c.json(201, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{
		"body": text, "client_message_id": f.newUUID(), "mentions": []map[string]string{{"agent_id": f.agent}},
	}, &message)
	return message.ID
}

func (f *telegramGateWorld) materialize() (int, int) {
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

func (f *telegramGateWorld) running(actor string) toolRun {
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

func TestTelegramAuthorizationActualWriteRevocation(t *testing.T) {
	f := newTelegramGateWorld(t)
	f.post(f.member, "perform authorized write")
	r := f.running(f.memberID)
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	input := dispatch.ToolCall{Generation: 1, CallID: "held-write", Arguments: json.RawMessage(`{"title":"Admitted Telegram write"}`)}
	var plan dispatch.SpaceToolPlan
	if err := f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		plan, err = store.PrepareSpaceTool(ctx, tx, f.org, f.deviceID, r.runID, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	gate := wecomHookRunner{SyncRunner: f.pool, inside: func(ctx context.Context, tx pgx.Tx) error {
		if err := conversations.LockTelegramInvocation(ctx, tx, f.conversation, f.memberID); err != nil {
			return err
		}
		close(locked)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	written := make(chan error, 1)
	go func() {
		out, err := spacetools.Execute(f.ctx, gate, f.org, plan, "space_task_create", input)
		if err == nil && !out.OK {
			err = fmt.Errorf("tool: %s", out.Text)
		}
		written <- err
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("gate not acquired")
	}
	revoked := make(chan error, 1)
	go func() {
		revoked <- f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE openfox.telegram_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, f.memberIdentity)
			return err
		})
	}()
	waitFor(t, "identity revoke waits for actual tool transaction", func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE openfox.telegram_identities%'`) > 0
	})
	close(release)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	// Reuse a preflight captured before revocation: Execute must reject it itself.
	input.CallID = "late-preflight"
	input.Arguments = json.RawMessage(`{"title":"MUST NOT CREATE"}`)
	if _, err := spacetools.Execute(f.ctx, f.pool, f.org, plan, "space_task_create", input); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("late actual write = %v", err)
	}
	if n := f.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='Admitted Telegram write'`, f.org); n != 1 {
		t.Fatalf("admitted write count=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='MUST NOT CREATE'`, f.org); n != 0 {
		t.Fatal("revoked task created")
	}
}

func TestTelegramAuthorizationInputRunPipeline(t *testing.T) {
	f := newTelegramGateWorld(t)
	ownerMessage := f.post(f.owner, "owner input")
	memberMessage := f.post(f.member, "member input")
	if n := f.count(`SELECT count(DISTINCT identity_id) FROM openfox.telegram_inputs WHERE organization_id=$1::uuid AND route_id=$2::uuid AND source_kind='OPENFOX'`, f.org, f.route); n != 2 {
		t.Fatalf("actual authors=%d", n)
	}
	f.admin.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "unbound", "client_message_id": f.newUUID()}, nil)
	f.ownerB.json(404, "POST", "/v1/organizations/"+f.orgB+"/conversations/"+f.conversation+"/messages", map[string]any{"body": "cross tenant", "client_message_id": f.newUUID()}, nil)
	for _, sql := range []string{`UPDATE openfox.telegram_inputs SET identity_version=2 WHERE message_id=$1::uuid`, `DELETE FROM openfox.telegram_inputs WHERE message_id=$1::uuid`} {
		if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, ownerMessage); return err }); err == nil {
			t.Fatal("snapshot mutation allowed")
		}
	}
	r := f.running(f.ownerID)
	if out := f.mustCall(r, "space_task_create", "first", map[string]any{"title": "Telegram actual tool"}); !out.OK {
		t.Fatal(out.Text)
	}
	f.rebindMember()
	if out := f.mustCall(r, "space_task_create", "unrelated-rebind", map[string]any{"title": "Owner remains authorized"}); !out.OK {
		t.Fatal(out.Text)
	}
	f.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at=now() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.runID)
	f.post(f.member, "new member input")
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("stale input revived %d/%d", made, rejected)
	}
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid AND status='REJECTED'`, f.org, memberMessage); n != 1 {
		t.Fatal("old input not rejected")
	}
	r = f.running(f.memberID)
	f.exec(`UPDATE openfox.telegram_identities SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	if _, err := f.call(r, "space_task_create", "stale-run", map[string]any{"title": "MUST NOT CREATE"}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("old Run %v", err)
	}
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	if err := f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.Context(ctx, tx, f.org, f.deviceID, r.runID, 1)
		return err
	}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("old context %v", err)
	}
}

func TestTelegramAuthorizationEpochPrivateAndControl(t *testing.T) {
	f := newTelegramGateWorld(t)
	f.post(f.member, "before epoch version change")
	f.exec(`UPDATE openfox.telegram_group_epochs SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("epoch version ignored %d/%d", made, rejected)
	}
	f.post(f.member, "before membership authorization change")
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='ADMIN' WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid`, f.memberID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("membership version ignored %d/%d", made, rejected)
	}
	f.exec(`UPDATE openfox.telegram_routes SET kind='p2p',chat_id=1001,identity_id=$3::uuid,identity_version=1,epoch_id=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.route, f.ownerIdentity)
	f.member.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "another person's DM", "client_message_id": f.newUUID()}, nil)
	f.post(f.owner, "owner private input")
	f.exec(`UPDATE openfox.telegram_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.ownerIdentity)
	f.ownerIdentity = f.bindIdentity(f.ownerID, 1001)
	f.owner.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "same person rebind must use new DM", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.telegram_routes SET kind='group',chat_id=-100,identity_id=NULL,identity_version=NULL,epoch_id=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.route, f.epoch)
	f.exec(`UPDATE openfox.telegram_bot_controls SET authorization_generation=2 WHERE organization_id=$1::uuid AND bot_id=$2::uuid`, f.org, f.bot)
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.LockTelegramInvocation(ctx, tx, f.conversation, f.memberID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("generation ignored %v", err)
	}
	f.exec(`UPDATE openfox.telegram_bot_controls SET authorization_generation=1 WHERE organization_id=$1::uuid AND bot_id=$2::uuid`, f.org, f.bot)
	f.exec(`UPDATE openfox.telegram_routes SET closed_at=now() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.route)
	f.member.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "closed route cannot become native", "client_message_id": f.newUUID()}, nil)
}

// A real durable queue + receipt precedes the same-TX immutable snapshot and
// PostMessage. The provider materializer owns this sequence in production.
func (f *telegramGateWorld) remoteInput() conversations.MessageInput {
	id := f.newUUID()
	f.exec(`INSERT INTO openfox.im_inbound_events(organization_id,connection_id,event_id,event_type) VALUES($1::uuid,$2::uuid,'0','message')`, f.org, f.connection)
	f.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,message_type,text,parent_remote_id) VALUES($1::uuid,$2::uuid,$3::uuid,'-100:10','-100','group','1002','text','remote input','10')`, f.org, id, f.bot)
	f.exec(`INSERT INTO openfox.telegram_update_receipts(organization_id,connection_id,event_id,body_hash,kind,inbound_id,route_id,chat_id,remote_message_id,update_id,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version,authorization_generation)
 VALUES($1::uuid,$2::uuid,'0',decode(repeat('ab',32),'hex'),'message',$3::uuid,$4::uuid,-100,10,0,$5::uuid,1,$6::uuid,1,$7::uuid,1,1)`, f.org, f.connection, id, f.route, f.memberID, f.memberIdentity, f.epoch)
	return conversations.MessageInput{ID: id, Body: "remote input", ClientMessageID: "telegram:" + f.bot + ":" + id, Mentions: []conversations.Mention{{AgentID: f.agent}}}
}
func (f *telegramGateWorld) insertRemote(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.telegram_inputs(organization_id,bot_id,inbound_id,message_id,source_kind,route_id,chat_id,remote_message_id,update_id,authorization_generation,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version)
 SELECT organization_id,$2::uuid,inbound_id,inbound_id,'REMOTE',route_id,chat_id,remote_message_id,update_id,authorization_generation,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND inbound_id=$1::uuid`, id, f.bot)
	return err
}
func (f *telegramGateWorld) postRemote(input conversations.MessageInput, insert bool) error {
	return f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		if insert {
			if err := f.insertRemote(ctx, tx, input.ID); err != nil {
				return err
			}
		}
		_, err := (conversations.Service{}).PostMessage(ctx, tx, f.org, f.conversation, conversations.Actor{UserID: f.memberID, Role: membership.RoleMember, AuthorizationVersion: 1}, input)
		return err
	})
}

func TestTelegramAuthorizationRemoteProofReplayAndBatch(t *testing.T) {
	f := newTelegramGateWorld(t)
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
	f.exec(`UPDATE openfox.telegram_update_receipts SET update_id=1 WHERE organization_id=$1::uuid AND inbound_id=$2::uuid`, f.org, input.ID)
	if err := f.postRemote(input, false); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("mismatched receipt %v", err)
	}
	f.exec(`UPDATE openfox.telegram_update_receipts SET update_id=0 WHERE organization_id=$1::uuid AND inbound_id=$2::uuid`, f.org, input.ID)
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
		return conversations.CheckTelegramRun(ctx, tx, f.conversation, f.memberID, r.runID)
	}); err != nil {
		t.Fatalf("retention invalidated Run %v", err)
	}
	f.rebindMember()
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckTelegramRun(ctx, tx, f.conversation, f.memberID, r.runID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("rebind revived Run %v", err)
	}
}
