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
	"openfox/internal/spacetools"
	"openfox/internal/tenancy"
)

// The shared harness supplies real HTTP authentication, PG tenancy, dispatch and
// task tools. Slack itself is not contacted by these authorization tests.
type slackGateWorld struct {
	*botWorld
	parent, app, bot, epoch, route, conversation, agent string
	ownerIdentity, memberIdentity                       string
}

func newSlackGateWorld(t *testing.T) *slackGateWorld {
	b := &botWorld{world: setup(t)}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'slack-gate-device',clock_timestamp())`, b.deviceID, "slack-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	f := &slackGateWorld{botWorld: b, parent: b.newUUID(), app: b.newUUID(), bot: b.newUUID(), epoch: b.newUUID(), route: b.newUUID(), conversation: b.newUUID()}
	f.agent = b.createAgent(b.owner, b.foxSpace, "Slack task assistant", "SHARED")
	b.exec(`UPDATE openfox.agents SET builtin_kind='TASK_ASSISTANT',allow_assign=false WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, f.agent)
	central, dynamic := b.newUUID(), b.newUUID()
	for _, id := range []string{central, dynamic} {
		b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'slack','ORGANIZATION',$2,'Slack fixture',$3::uuid)`, b.org, id, b.ownerID)
	}
	b.exec(`INSERT INTO openfox.slack_apps(organization_id,id,connection_id,kind,name,app_id,team_id,bot_user_id,created_by,actor_version,state,operation_key,request_hash,installed_at,url_verified_at,activated_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,'CENTRAL','Central',$2,'T-fixture','U-central',$4::uuid,1,'ACTIVE',$2,decode(repeat('ab',32),'hex'),now(),now(),now())`, b.org, f.parent, central, b.ownerID)
	f.ownerIdentity = f.bindIdentity(central, b.ownerID, "U-owner")
	f.memberIdentity = f.bindIdentity(central, b.memberID, "U-member")
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'TASK_ASSISTANT',$4::uuid,$5::uuid,$6::uuid)`, b.org, f.bot, dynamic, f.agent, b.foxSpace, b.ownerID)
	b.exec(`INSERT INTO openfox.slack_apps(organization_id,id,connection_id,parent_app_id,parent_generation,bot_id,kind,name,app_id,team_id,bot_user_id,created_by,actor_version,state,operation_key,request_hash,installed_at,url_verified_at,activated_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,1,$5::uuid,'TASK_ASSISTANT','Assistant',$2,'T-fixture','U-assistant',$6::uuid,1,'ACTIVE',$2,decode(repeat('cd',32),'hex'),now(),now(),now())`, b.org, f.app, dynamic, f.parent, f.bot, b.ownerID)
	b.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility,origin) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'SHARED','IM')`, b.org, f.conversation, b.foxSpace, f.agent, b.ownerID)
	b.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1::uuid,$2::uuid,$3::uuid,'COMMENTER'),($1::uuid,$2::uuid,$4::uuid,'COMMENTER')`, b.org, f.conversation, b.memberID, b.adminID)
	b.exec(`INSERT INTO openfox.slack_channels(organization_id,id,app_id,channel_id,space_id,enabled,generation,authorized_by,identity_id,identity_version,public_reply_consent) VALUES($1::uuid,$2::uuid,$3::uuid,'C-fixture',$4::uuid,true,1,$5::uuid,$6::uuid,1,true)`, b.org, f.epoch, f.app, b.foxSpace, b.ownerID, f.ownerIdentity)
	b.exec(`INSERT INTO openfox.slack_routes(organization_id,id,app_id,bot_id,generation,channel_id,thread_ts,channel_epoch_id,identity_id,identity_version,conversation_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,1,'C-fixture','123.001',$5::uuid,$6::uuid,1,$7::uuid)`, b.org, f.route, f.app, f.bot, f.epoch, f.ownerIdentity, f.conversation)
	return f
}

func (f *slackGateWorld) bindIdentity(parentConnection, actor, slackUser string) string {
	id, conn := f.newUUID(), f.newUUID()
	f.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'slack','PERSONAL',$3::uuid,$4::uuid,$5,'Identity',$4::uuid)`, f.org, conn, parentConnection, actor, slackUser)
	f.exec(`INSERT INTO openfox.slack_identities(organization_id,id,connection_id,parent_app_id,owner_user_id,team_id,slack_user_id,subject) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'T-fixture',$6,$6)`, f.org, id, conn, f.parent, actor, slackUser)
	return id
}

func (f *slackGateWorld) rebindIdentity(identity, actor, slackUser string) string {
	f.exec(`UPDATE openfox.slack_identities SET active=false,version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, identity)
	f.exec(`UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=now() WHERE organization_id=$1::uuid AND id=(SELECT connection_id FROM openfox.slack_identities WHERE organization_id=$1::uuid AND id=$2::uuid)`, f.org, identity)
	var parentConnection string
	f.scan(&parentConnection, `SELECT connection_id::text FROM openfox.slack_apps WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.parent)
	return f.bindIdentity(parentConnection, actor, slackUser)
}

func (f *slackGateWorld) actorTx(actor string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: actor, AuthorizationVersion: 1}
	return f.pool.RunForOrganization(f.ctx, f.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func (f *slackGateWorld) post(c *client, text string) string {
	f.t.Helper()
	var message struct {
		ID string `json:"id"`
	}
	c.json(201, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{
		"body": text, "client_message_id": f.newUUID(), "mentions": []map[string]string{{"agent_id": f.agent}},
	}, &message)
	return message.ID
}

func (f *slackGateWorld) materialize() (int, int) {
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

func (f *slackGateWorld) running(actor string) toolRun {
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

func TestSlackAuthorizationInputRunPipeline(t *testing.T) {
	f := newSlackGateWorld(t)
	ownerMessage := f.post(f.owner, "owner input")
	memberMessage := f.post(f.member, "member input in same thread")
	if n := f.count(`SELECT count(DISTINCT identity_id) FROM openfox.slack_input_authorizations WHERE organization_id=$1::uuid AND route_id=$2::uuid`, f.org, f.route); n != 2 {
		t.Fatalf("actual authors not snapshotted: %d", n)
	}
	f.admin.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "not bound", "client_message_id": f.newUUID()}, nil)
	f.ownerB.json(404, "POST", "/v1/organizations/"+f.orgB+"/conversations/"+f.conversation+"/messages", map[string]any{"body": "cross tenant", "client_message_id": f.newUUID()}, nil)
	// The application role cannot rewrite the immutable authorization history.
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.slack_input_authorizations SET identity_version=2 WHERE message_id=$1::uuid`, ownerMessage)
		return err
	}); err == nil {
		t.Fatal("snapshot UPDATE unexpectedly allowed")
	}
	r := f.running(f.ownerID)
	if out := f.mustCall(r, "space_task_create", "first", map[string]any{"title": "Slack actual tool"}); !out.OK {
		t.Fatal(out.Text)
	}
	// Rebind another person: the creator's Run is unaffected. The stale queued
	// member input is rejected, even though the member is currently bound again.
	f.memberIdentity = f.rebindIdentity(f.memberIdentity, f.memberID, "U-member")
	if out := f.mustCall(r, "space_task_create", "second", map[string]any{"title": "Other actor remains authorized"}); !out.OK {
		t.Fatal(out.Text)
	}
	f.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at=now() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.runID)
	// New input arrives while the stale one is still queued. They must not be
	// combined into a single rejected batch solely because the author matches.
	f.post(f.member, "new member input")
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("old member input revived: %d/%d", made, rejected)
	}
	if n := f.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid AND status='REJECTED'`, f.org, memberMessage); n != 1 {
		t.Fatal("old input not rejected")
	}
	memberRun := f.running(f.memberID)
	if out := f.mustCall(memberRun, "space_task_create", "member-create", map[string]any{"title": "Member after rebind"}); !out.OK {
		t.Fatal(out.Text)
	}
	// Even a fully valid identity after another rebind cannot revive this Run.
	f.exec(`UPDATE openfox.slack_identities SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.memberIdentity)
	if _, err := f.call(memberRun, "space_task_create", "stale-run", map[string]any{"title": "MUST NOT CREATE"}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("old Run = %v", err)
	}
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	if err := f.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.Context(ctx, tx, f.org, f.deviceID, memberRun.runID, 1)
		return err
	}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("stale Runtime context = %v", err)
	}
}

func TestSlackAuthorizationEpochAndParentFences(t *testing.T) {
	f := newSlackGateWorld(t)
	f.post(f.member, "queued")
	f.exec(`UPDATE openfox.slack_channels SET enabled=false,disabled_at=now(),version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.epoch)
	if made, rejected := f.materialize(); made != 0 || rejected != 1 {
		t.Fatalf("closed epoch accepted: %d/%d", made, rejected)
	}
	// Reopening means a distinct epoch and new route. The old route stays closed.
	f.exec(`INSERT INTO openfox.slack_channels(organization_id,app_id,channel_id,space_id,enabled,generation,authorized_by,identity_id,identity_version,public_reply_consent) VALUES($1::uuid,$2::uuid,'C-fixture',$3::uuid,true,1,$4::uuid,$5::uuid,1,true)`, f.org, f.app, f.foxSpace, f.ownerID, f.ownerIdentity)
	f.member.json(403, "POST", f.path("/conversations/"+f.conversation+"/messages"), map[string]any{"body": "old thread", "client_message_id": f.newUUID()}, nil)
	// Parent replacement must invalidate a child even while it still says ACTIVE.
	f.exec(`UPDATE openfox.slack_apps SET generation=generation+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.parent)
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.LockSlackInvocation(ctx, tx, f.conversation, f.ownerID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("parent generation = %v", err)
	}
	// A DM cannot be used by a second bound person even if conversation membership
	// was granted separately. No Slack call or user-provided actor can override it.
	f.exec(`UPDATE openfox.slack_apps SET parent_generation=2 WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.app)
	f.exec(`UPDATE openfox.slack_routes SET channel_epoch_id=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.route)
	if err := f.actorTx(f.memberID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckSlackInvocation(ctx, tx, f.conversation, f.memberID)
	}); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("other DM actor = %v", err)
	}
	if err := f.actorTx(f.ownerID, func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckSlackInvocation(ctx, tx, f.conversation, f.ownerID)
	}); err != nil {
		t.Fatal(err)
	}
	f.ownerIdentity = f.rebindIdentity(f.ownerIdentity, f.ownerID, "U-owner")
	f.post(f.owner, "new private input after same-person rebind")
}

func TestSlackAuthorizationActualWriteRevocation(t *testing.T) {
	f := newSlackGateWorld(t)
	f.post(f.member, "perform authorized write")
	r := f.running(f.memberID)
	store, _ := dispatch.NewStore(dispatch.DefaultConfig())
	input := dispatch.ToolCall{Generation: 1, CallID: "held-write", Arguments: json.RawMessage(`{"title":"Admitted Slack write"}`)}
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
		if err := conversations.LockSlackInvocation(ctx, tx, f.conversation, f.memberID); err != nil {
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
			_, err := tx.Exec(ctx, `UPDATE openfox.slack_identities SET active=false,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, f.memberIdentity)
			return err
		})
	}()
	waitFor(t, "identity revoke waits for actual tool transaction", func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE openfox.slack_identities%'`) > 0
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
	if n := f.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='Admitted Slack write'`, f.org); n != 1 {
		t.Fatalf("admitted write count=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='MUST NOT CREATE'`, f.org); n != 0 {
		t.Fatal("revoked task created")
	}
}
