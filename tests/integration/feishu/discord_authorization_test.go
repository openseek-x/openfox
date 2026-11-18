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
	"strconv"
	"strings"
	"testing"
	"time"
)

type discordGateWorld struct {
	*botWorld
	connection, bot, epoch, route, conversation, agent, ownerIdentity, memberIdentity string
}

func newDiscordGateWorld(t *testing.T) *discordGateWorld {
	b := &botWorld{world: setup(t)}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'discord-gate-device',clock_timestamp())`, b.deviceID, "discord-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	f := &discordGateWorld{botWorld: b, connection: b.newUUID(), bot: b.newUUID(), epoch: b.newUUID(), route: b.newUUID(), conversation: b.newUUID()}
	f.agent = b.createAgent(b.owner, b.foxSpace, "Discord ordinary Agent", "SHARED")
	b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'discord','ORGANIZATION',$2,'Discord fixture',$3::uuid)`, b.org, f.connection, b.ownerID)
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'AGENT',$4::uuid,$5::uuid)`, b.org, f.bot, f.connection, f.agent, b.ownerID)
	snow, _ := strconv.ParseUint(strings.ReplaceAll(f.bot, "-", "")[:15], 16, 64)
	b.exec(`INSERT INTO openfox.discord_bot_controls(organization_id,bot_id,connection_id,remote_bot_id,application_id,username,name,kind,target_agent_id,message_content_enabled,bot_public,managed_by,managed_version) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$4,'fixture','Fixture','AGENT',$5::uuid,true,false,$6::uuid,1)`, b.org, f.bot, f.connection, strconv.FormatUint(snow, 10), f.agent, b.ownerID)
	f.ownerIdentity = f.bindIdentity(b.ownerID, "101", "901")
	f.memberIdentity = f.bindIdentity(b.memberID, "102", "902")
	b.exec(`INSERT INTO openfox.discord_channel_epochs(organization_id,id,bot_id,guild_id,channel_id,guild_name,channel_name,authorization_generation,public_reply_consent,history_consent,authorized_by,identity_id,identity_version) VALUES($1::uuid,$2::uuid,$3::uuid,'18446744073709551615','18446744073709551614','Guild','Channel',1,true,true,$4::uuid,$5::uuid,1)`, b.org, f.epoch, f.bot, b.ownerID, f.ownerIdentity)
	b.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'SHARED','IM')`, b.org, f.conversation, b.foxSpace, f.agent, b.ownerID)
	b.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1::uuid,$2::uuid,$3::uuid,'COMMENTER'),($1::uuid,$2::uuid,$4::uuid,'COMMENTER')`, b.org, f.conversation, b.memberID, b.adminID)
	b.exec(`INSERT INTO openfox.discord_routes(organization_id,id,bot_id,authorization_generation,kind,channel_id,guild_id,epoch_id,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,1,'group','18446744073709551614','18446744073709551615',$4::uuid,$5::uuid)`, b.org, f.route, f.bot, f.epoch, f.conversation)
	b.exec(`INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES($1::uuid,$2::uuid,'18446744073709551614','group',$3::uuid,$4::uuid)`, b.org, f.bot, f.conversation, b.ownerID)
	return f
}
func (f *discordGateWorld) bindIdentity(actor, subject, privateChannel string) string {
	id := f.newUUID()
	f.exec(`INSERT INTO openfox.discord_identities(organization_id,id,bot_id,owner_user_id,remote_user_id,private_channel_id,display_name,authorization_generation) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,'Fixture member',1)`, f.org, id, f.bot, actor, subject, privateChannel)
	return id
}
func (f *discordGateWorld) rebindMember() {
	f.exec(`UPDATE openfox.discord_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	f.memberIdentity = f.bindIdentity(f.memberID, "102", "903")
}
func (f *discordGateWorld) actorTx(actor string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: actor, AuthorizationVersion: 1}
	return f.pool.RunForOrganization(f.ctx, f.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func (f *discordGateWorld) post(c *client, text string) string {
	f.t.Helper()
	var message struct {
		ID string `json:"id"`
	}
	c.json(201, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{
		"body": text, "client_message_id": f.newUUID(), "mentions": []map[string]string{{"agent_id": f.agent}},
	}, &message)
	return message.ID
}

func (f *discordGateWorld) materialize() (int, int) {
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

func (f *discordGateWorld) running(actor string) toolRun {
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

func (f *discordGateWorld) mention(r toolRun, call, body string) error {
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

func TestDiscordAuthorizationInputsAndActualTool(t *testing.T) {
	f := newDiscordGateWorld(t)
	first := f.post(f.owner, "owner input")
	member := f.post(f.member, "member input")
	if n := f.count(`SELECT count(DISTINCT identity_id) FROM openfox.discord_inputs WHERE organization_id=$1::uuid AND route_id=$2::uuid AND source_kind='OPENFOX'`, f.org, f.route); n != 2 {
		t.Fatal(n)
	}
	f.admin.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "unbound", "client_message_id": f.newUUID()}, nil)
	f.ownerB.json(404, "POST", "/v1/organizations/"+f.orgB+"/conversations/"+f.conversation+"/messages", map[string]any{"body": "cross tenant", "client_message_id": f.newUUID()}, nil)
	for _, sql := range []string{`UPDATE openfox.discord_inputs SET identity_version=2 WHERE message_id=$1::uuid`, `DELETE FROM openfox.discord_inputs WHERE message_id=$1::uuid`} {
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
	f.exec(`UPDATE openfox.discord_identities SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
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

func (f *discordGateWorld) remoteInput(remote string) conversations.MessageInput {
	id := f.newUUID()
	f.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,parent_remote_id,chat_id,chat_type,sender_open_id,message_type,text,status,lease_owner,lease_expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$4,'18446744073709551614','group','102','text','remote input','PROCESSING','fixture',now()+interval '1 minute')`, f.org, id, f.bot, remote)
	f.exec(`INSERT INTO openfox.discord_message_receipts(organization_id,id,bot_id,connection_id,remote_message_id,channel_id,guild_id,sender_id,body_hash,kind,inbound_id,route_id,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version,authorization_generation,result) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'18446744073709551614','18446744073709551615','102',decode(repeat('ab',32),'hex'),'MESSAGE',$2::uuid,$6::uuid,$7::uuid,1,$8::uuid,1,$9::uuid,1,1,'MATERIALIZING')`, f.org, id, f.bot, f.connection, remote, f.route, f.memberID, f.memberIdentity, f.epoch)
	return conversations.MessageInput{ID: id, Body: "remote input", ClientMessageID: "discord:" + f.bot + ":" + id, Mentions: []conversations.Mention{{AgentID: f.agent}}}
}
func (f *discordGateWorld) postRemote(input conversations.MessageInput, insert bool) error {
	return f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		if insert {
			_, err := tx.Exec(ctx, `INSERT INTO openfox.discord_inputs(organization_id,message_id,source_kind,bot_id,route_id,channel_id,guild_id,remote_message_id,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version,authorization_generation,receipt_id,inbound_id) SELECT organization_id,id,'REMOTE',bot_id,route_id,channel_id,guild_id,remote_message_id,actor_id,actor_version,identity_id,identity_version,epoch_id,epoch_version,authorization_generation,id,inbound_id FROM openfox.discord_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, input.ID)
			if err != nil {
				return err
			}
		}
		_, err := (conversations.Service{}).PostMessage(ctx, tx, f.org, f.conversation, conversations.Actor{UserID: f.memberID, Role: membership.RoleMember, AuthorizationVersion: 1}, input)
		return err
	})
}
func TestDiscordAuthorizationRemoteProofAndTerminalSource(t *testing.T) {
	f := newDiscordGateWorld(t)
	input := f.remoteInput("10001")
	f.member.json(400, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": input.Body, "client_message_id": input.ClientMessageID}, nil)
	if err := f.postRemote(input, false); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("missing proof %v", err)
	}
	if err := f.postRemote(input, true); err != nil {
		t.Fatal(err)
	}
	if err := f.postRemote(input, false); err != nil {
		t.Fatalf("valid replay %v", err)
	}
	f.exec(`UPDATE openfox.discord_message_receipts SET sender_id='103' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
	if err := f.postRemote(input, false); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("wrong sender %v", err)
	}
	f.exec(`UPDATE openfox.discord_message_receipts SET sender_id='102' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
	f.post(f.member, "native input stays local")
	run := f.running(f.memberID)
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND run_id=$2::uuid`, f.org, run.runID); n != 1 {
		t.Fatalf("mixed input batch %d", n)
	}
	newer := f.remoteInput("10002")
	if err := f.postRemote(newer, true); err != nil {
		t.Fatal(err)
	}
	f.exec(`DELETE FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckDiscordRun(ctx, tx, f.conversation, f.memberID, run.runID)
	}); err != nil {
		t.Fatalf("retention invalidates Run %v", err)
	}
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=now() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, run.runID)
		if err != nil {
			return err
		}
		_, err = dispatch.FinalizeConversation(ctx, tx, execution.Run{ID: run.runID, ConversationID: f.conversation, AgentID: f.agent, ActorID: f.memberID, State: execution.StateSucceeded}, dispatch.AuthorAgent, "terminal result", false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND source_run_id=$2::uuid AND source_terminal AND reply_to_remote_id='10001' AND chat_id='18446744073709551614'`, f.org, run.runID); n != 1 {
		t.Fatalf("exact source %d", n)
	}
	f.rebindMember()
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckDiscordRun(ctx, tx, f.conversation, f.memberID, run.runID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("old Run revived %v", err)
	}
}
func TestDiscordAuthorizationRejectedExactBatch(t *testing.T) {
	f := newDiscordGateWorld(t)
	for _, remote := range []string{"20001", "20002"} {
		in := f.remoteInput(remote)
		if err := f.postRemote(in, true); err != nil {
			t.Fatal(err)
		}
	}
	// A current provider identity can still receive a factual no-Run rejection
	// when its execution device grant is unavailable.
	f.exec(`UPDATE openfox.device_grants SET status='REVOKED' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.grantID)
	if made, rejected := f.materialize(); made != 0 || rejected != 2 {
		t.Fatalf("reject=%d/%d", made, rejected)
	}
	if n := f.count(`SELECT count(*) FROM openfox.discord_reply_sources WHERE organization_id=$1::uuid AND batch_size=2`, f.org); n != 2 {
		t.Fatalf("source membership %d", n)
	}
	if n := f.count(`SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND source_run_id IS NULL AND NOT source_terminal AND reply_to_remote_id='20002'`, f.org); n != 1 {
		t.Fatalf("no-Run reply %d", n)
	}
	// Retention does not delete the exact batch or its remote source evidence.
	f.exec(`DELETE FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid`, f.org)
	if n := f.count(`SELECT count(*) FROM openfox.discord_reply_sources WHERE organization_id=$1::uuid`, f.org); n != 2 {
		t.Fatal(n)
	}
}

func TestDiscordAuthorizationConcurrentTool(t *testing.T) {
	f := newDiscordGateWorld(t)
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
			_, err := tx.Exec(ctx, `UPDATE openfox.discord_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, f.memberIdentity)
			return err
		})
	}()
	waitFor(t, "Discord actual tool holds identity gate", func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE openfox.discord_identities%'`) > 0
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
}

func TestDiscordAuthorizationEpochPrivateAndControl(t *testing.T) {
	f := newDiscordGateWorld(t)
	f.post(f.member, "before epoch changes")
	f.exec(`UPDATE openfox.discord_channel_epochs SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("epoch version ignored %d/%d", made, rejected)
	}
	f.exec(`UPDATE openfox.discord_channel_epochs SET paused=true WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	f.member.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "paused channel", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.discord_channel_epochs SET paused=false WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	f.post(f.member, "before member version changes")
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='ADMIN' WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid`, f.memberID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("member version ignored %d/%d", made, rejected)
	}
	// Private channel and Discord user IDs deliberately differ.
	f.route, f.conversation = f.newUUID(), f.newUUID()
	f.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'PRIVATE','IM')`, f.org, f.conversation, f.foxSpace, f.agent, f.ownerID)
	f.exec(`INSERT INTO openfox.discord_routes(organization_id,id,bot_id,authorization_generation,kind,channel_id,identity_id,identity_version,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,1,'p2p','901',$4::uuid,1,$5::uuid)`, f.org, f.route, f.bot, f.ownerIdentity, f.conversation)
	f.post(f.owner, "private owner message")
	f.member.json(404, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "not my private conversation", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.discord_identities SET active=false,disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.ownerIdentity)
	f.bindIdentity(f.ownerID, "101", "904")
	f.owner.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "rebind cannot inherit history", "client_message_id": f.newUUID()}, nil)
	f.exec(`UPDATE openfox.discord_bot_controls SET authorization_generation=2 WHERE organization_id=$1::uuid AND bot_id=$2::uuid`, f.org, f.bot)
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.LockDiscordInvocation(ctx, tx, f.conversation, f.ownerID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("generation ignored %v", err)
	}
}

func TestDiscordAuthorizationDeletedRemoteSource(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		f := newDiscordGateWorld(t)
		input := f.remoteInput("20001")
		if err := f.postRemote(input, true); err != nil {
			t.Fatal(err)
		}
		f.exec(`UPDATE openfox.discord_message_receipts SET source_deleted=true WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
		if made, rejected := f.materialize(); made != 0 || rejected != 1 {
			t.Fatalf("deleted source admitted: %d/%d", made, rejected)
		}
	})
	t.Run("running", func(t *testing.T) {
		f := newDiscordGateWorld(t)
		input := f.remoteInput("20002")
		if err := f.postRemote(input, true); err != nil {
			t.Fatal(err)
		}
		run := f.running(f.memberID)
		f.exec(`UPDATE openfox.discord_message_receipts SET source_deleted=true WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, input.ID)
		if err := f.mention(run, "source-deleted", "must not write"); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
			t.Fatalf("deleted source tool: %v", err)
		}
		store, _ := dispatch.NewStore(dispatch.DefaultConfig())
		if err := f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
			_, err := store.Context(ctx, tx, f.org, f.deviceID, run.runID, 1)
			return err
		}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
			t.Fatalf("deleted source run: %v", err)
		}
	})
}
