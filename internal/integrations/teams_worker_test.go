package integrations

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

type teamsWorkerRunner struct{ f *teamsFixture }

func (r teamsWorkerRunner) RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return r.f.run(fn)
}
func (r teamsWorkerRunner) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	if org != r.f.org {
		return ErrForbidden
	}
	selection, err := selector.SelectOrganization(ctx, org)
	if err != nil {
		return err
	}
	tenant, err := tenancy.NewTenantContext(selection, source)
	if err != nil {
		return err
	}
	ctx = tenancy.WithTenantContext(ctx, tenant)
	c, err := pgx.Connect(ctx, r.f.dsn)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,$3,$4)`, org, selection.ActorID, selection.AuthorizationVersion, string(source)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (f *teamsFixture) receive(bot TeamsBot, id, text string) (TeamsReceiveResult, error) {
	route := f.route("personal")
	route.ActivityID = id
	return f.receiveRoute(bot, route, text)
}
func (f *teamsFixture) receiveRoute(bot TeamsBot, route teams.Route, text string) (TeamsReceiveResult, error) {
	id := route.ActivityID
	event := teams.VerifiedActivity{Route: route, Text: text, Supported: true, BodyHash: hex.EncodeToString(TokenHash(id + ":" + text))}
	event.Activity.Type = "message"
	var p TeamsReceiveTarget
	var out TeamsReceiveResult
	err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.PrepareTeamsActivity(ctx, tx, f.org, bot.ID, event)
		return e
	})
	if err != nil {
		return out, err
	}
	proof, err := f.service.VerifyTeamsActivity(context.Background(), p)
	if err != nil {
		return out, err
	}
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ReceiveTeamsActivity(ctx, tx, f.org, p, proof)
		return e
	})
	return out, err
}
func (f *teamsFixture) bind(bot TeamsBot) {
	f.t.Helper()
	var c TeamsBindingChallenge
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = f.service.IssueTeamsChallenge(ctx, tx, f.org, f.actor, bot.ID)
		return e
	})
	if r, e := f.receive(bot, "bind-"+c.ID, c.Command); e != nil || r.Result != "identity_bound" {
		f.t.Fatalf("bind result %+v %v", r, e)
	}
}
func teamsFinalize(t *testing.T, f *teamsFixture, inbound, body string) (execution.Run, string) {
	t.Helper()
	var run execution.Run
	var outbox string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		created, rejected, e := (dispatch.Store{}).MaterializeInputs(ctx, tx, f.org, 10)
		if e != nil {
			return e
		}
		if created != 1 || rejected != 0 {
			t.Fatalf("materialize %d/%d", created, rejected)
		}
		e = tx.QueryRow(ctx, `SELECT r.id::text,r.space_id::text,r.conversation_id::text,r.agent_id::text,r.actor_id::text FROM openfox.runs r JOIN openfox.conversation_inputs i ON(i.organization_id,i.run_id)=(r.organization_id,r.id) WHERE r.organization_id=openfox_private.current_organization_id() AND i.message_id=$1::uuid`, inbound).Scan(&run.ID, &run.SpaceID, &run.ConversationID, &run.AgentID, &run.ActorID)
		if e != nil {
			return e
		}
		run.OrganizationID = f.org
		run.State = execution.StateSucceeded
		if _, e = tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, run.ID); e != nil {
			return e
		}
		message, e := dispatch.FinalizeConversation(ctx, tx, run, dispatch.AuthorAgent, body, false)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid AND source_run_id=$2::uuid AND source_terminal`, message, run.ID).Scan(&outbox)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	return run, outbox
}
func teamsInboundID(f *teamsFixture, bot TeamsBot, id string) string {
	f.t.Helper()
	r := f.route("personal")
	r.ActivityID = id
	var inbound string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND remote_message_id=$2`, bot.ID, r.MessageKey()).Scan(&inbound)
	})
	return inbound
}
func TestTeamsOriginalRunRetentionUnknownAndNativePG(t *testing.T) {
	f := newTeamsFixture(t)
	bot, e := f.connect()
	if e != nil {
		t.Fatal(e)
	}
	f.bind(bot)
	w := &IMWorker{Pool: teamsWorkerRunner{f}, Service: f.service, Instance: "teams-native-test"}
	// The one fixed binding notice is real local TLS output, separately counted.
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if r, e := f.receive(bot, "request/one;opaque", "controlled input one"); e != nil || r.Result != "queued" {
		t.Fatalf("queue %+v %v", r, e)
	}
	if r, e := f.receive(bot, "request/one;opaque", "controlled input one"); e != nil || !r.Duplicate {
		t.Fatalf("duplicate %+v %v", r, e)
	}
	if _, e = f.receive(bot, "request/one;opaque", "conflicting input"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("expected payload conflict", e)
	}
	if e = w.ProcessTeamsInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	inbound := teamsInboundID(f, bot, "request/one;opaque")
	run, outbox := teamsFinalize(t, f, inbound, strings.Repeat("界", 9000))
	if outbox == "" {
		t.Fatal("terminal helper did not enqueue")
	}
	// Queue cleanup cannot remove original Run authority or reply routing.
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `DELETE FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, inbound); e != nil {
			return e
		}
		var retained bool
		e := tx.QueryRow(ctx, `SELECT inbound_id IS NULL AND source_kind='REMOTE' AND route->>'activity_id'='request/one;opaque' FROM openfox.teams_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, inbound).Scan(&retained)
		if e != nil {
			return e
		}
		if !retained {
			return errors.New("source lost with queue")
		}
		return conversations.CheckTeamsRun(ctx, tx, run.ConversationID, f.user, run.ID)
	})
	if _, e = f.receive(bot, "request/two;opaque", "controlled input two"); e != nil {
		t.Fatal(e)
	}
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls := len(f.replies)
	body := f.replies[calls-1]["text"].(string)
	path := f.replyPaths[calls-1]
	f.mu.Unlock()
	if calls != 2 || !strings.HasSuffix(path, "/activities/request/one;opaque") || len([]rune(body)) != 8000 || !strings.Contains(body, "https://openfox.example.test/o/") {
		t.Fatalf("wrong original route/body calls=%d path=%s length=%d", calls, path, len([]rune(body)))
	}
	if e = w.ProcessTeamsInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	_, unknown := teamsFinalize(t, f, teamsInboundID(f, bot, "request/two;opaque"), "controlled second result")
	f.mu.Lock()
	f.loseReply = true
	f.mu.Unlock()
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var state, reason string
		e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, unknown).Scan(&state, &reason)
		if e == nil && (state != "FAILED" || reason != "delivery_unknown") {
			return errors.New("unknown send not terminal")
		}
		return e
	})
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls = len(f.replies)
	f.mu.Unlock()
	if calls != 3 {
		t.Fatalf("unknown retried: %d", calls)
	}
	native, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, run.ConversationID, conversations.Actor(f.actor), conversations.MessageInput{ID: native, Body: "OpenFox-only input", ClientMessageID: refsTestID(t), AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: bot.Agent.ID}}})
		return e
	})
	_, nativeOutbox := teamsFinalize(t, f, native, "native-only result")
	if nativeOutbox != "" {
		t.Fatal("native-only result was exported")
	}
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, run.ConversationID, conversations.Actor(f.actor), conversations.MessageInput{ID: refsTestID(t), Body: "spoof", ClientMessageID: "teams:" + bot.ID + ":" + inbound, AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: bot.Agent.ID}}})
		return e
	})
	if !errors.Is(err, conversations.ErrInvalidRequest) {
		t.Fatal("remote namespace spoof accepted", err)
	}
	var bindings []TeamsPersonalBinding
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		bindings, e = f.service.TeamsPersonalBindings(ctx, tx, f.org, f.actor)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RemoveTeamsPersonalBinding(ctx, tx, f.org, f.actor, bot.ID, bindings[0].Version)
	})
	f.bind(bot)
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckTeamsRun(ctx, tx, run.ConversationID, f.user, run.ID)
	})
	if !errors.Is(err, conversations.ErrForbidden) {
		t.Fatal("rebind revived old Run", err)
	}
}

func (f *teamsFixture) scope(bot TeamsBot, row TeamsScope, enabled bool) (TeamsScope, error) {
	var plan TeamsScopeTarget
	err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareTeamsScope(ctx, tx, f.org, f.actor, bot.ID, row.ID, TeamsScopeInput{Version: row.Version, Enabled: enabled, PublicReplyConsent: enabled})
		return e
	})
	if err != nil {
		return TeamsScope{}, err
	}
	proof, err := f.service.VerifyTeamsScope(context.Background(), plan)
	if err != nil {
		return TeamsScope{}, err
	}
	var result TeamsScope
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		result, e = f.service.SaveTeamsScope(ctx, tx, f.org, f.actor, plan, proof)
		return e
	})
	return result, err
}
func TestTeamsScopeEpochAndChannelPG(t *testing.T) {
	f := newTeamsFixture(t)
	bot, err := f.connect()
	if err != nil {
		t.Fatal(err)
	}
	f.bind(bot)
	route := f.route("personal")
	route.ScopeKind = "group"
	route.ConversationID = "19:group/opaque;thread"
	route.ActivityID = "discover"
	if r, e := f.receiveRoute(bot, route, "group text"); e != nil || r.Result != "scope_not_enabled" {
		t.Fatalf("discover %+v %v", r, e)
	}
	var page TeamsScopesPage
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		page, e = f.service.TeamsScopes(ctx, tx, f.org, f.actor, bot.ID, "")
		return e
	})
	if len(page.Scopes) != 1 || page.Scopes[0].Enabled {
		t.Fatalf("not disabled discovery %+v", page)
	}
	active, err := f.scope(bot, page.Scopes[0], true)
	if err != nil {
		t.Fatal(err)
	}
	route.ActivityID = "group-original"
	if _, err = f.receiveRoute(bot, route, "controlled group request"); err != nil {
		t.Fatal(err)
	}
	w := &IMWorker{Pool: teamsWorkerRunner{f}, Service: f.service}
	if err = w.ProcessTeamsInbound(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	var inbound string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND remote_message_id=$1`, route.MessageKey()).Scan(&inbound)
	})
	oldRun, outbox := teamsFinalize(t, f, inbound, "group result")
	if outbox == "" {
		t.Fatal("missing old outbox")
	}
	disabled, err := f.scope(bot, active, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := f.scope(bot, disabled, true)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID == active.ID {
		t.Fatal("same authorization epoch")
	}
	if e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		return conversations.CheckTeamsRun(ctx, tx, oldRun.ConversationID, f.user, oldRun.ID)
	}); e == nil {
		t.Fatal("old run revived")
	}
	if err = w.ProcessTeamsReplies(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	var status string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status)
	})
	if status != "SKIPPED" {
		t.Fatal("old reply", status)
	}
	route.ActivityID = "group-new"
	if _, err = f.receiveRoute(bot, route, "new group request"); err != nil {
		t.Fatal(err)
	}
	if err = w.ProcessTeamsInbound(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	var newer string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT m.conversation_id::text FROM openfox.im_inbound_messages i JOIN openfox.messages m ON(m.organization_id,m.id)=(i.organization_id,i.id) WHERE i.organization_id=openfox_private.current_organization_id() AND i.remote_message_id=$1`, route.MessageKey()).Scan(&newer)
	})
	if newer == oldRun.ConversationID || newer == "" {
		t.Fatal("conversation epoch not renewed")
	}
	// Channel enable performs the real, minimum Graph metadata GET; no message read.
	channel := route
	channel.ScopeKind = "channel"
	channel.TeamID = "19:team;opaque"
	channel.AADGroupID = refsTestID(t)
	channel.ChannelID = "19:channel;opaque"
	channel.ConversationID = "19:channel;opaque;messageid=thread"
	channel.ActivityID = "channel-discover"
	if _, err = f.receiveRoute(bot, channel, "channel text"); err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		page, e = f.service.TeamsScopes(ctx, tx, f.org, f.actor, bot.ID, "")
		return e
	})
	var cs TeamsScope
	for _, v := range page.Scopes {
		if v.Kind == "channel" {
			cs = v
		}
	}
	f.mu.Lock()
	f.channelType = "private"
	f.mu.Unlock()
	if _, e := f.scope(bot, cs, true); !errors.Is(e, teams.ErrChannelScope) {
		t.Fatal("private channel accepted", e)
	}
	f.mu.Lock()
	f.channelType = "standard"
	f.mu.Unlock()
	if _, e := f.scope(bot, cs, true); e != nil {
		t.Fatal("standard posts channel", e)
	}
	f.mu.Lock()
	calls := f.channelCalls
	f.mu.Unlock()
	if calls != 2 {
		t.Fatal("channel verification calls", calls)
	}
}

func TestTeamsSplitNativeAndRejectedPG(t *testing.T) {
	f := newTeamsFixture(t)
	bot, e := f.connect()
	if e != nil {
		t.Fatal(e)
	}
	f.bind(bot)
	w := &IMWorker{Pool: teamsWorkerRunner{f}, Service: f.service}
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(bot, "mixed-remote", "remote input"); e != nil {
		t.Fatal(e)
	}
	if e = w.ProcessTeamsInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	remote := teamsInboundID(f, bot, "mixed-remote")
	var conv string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT conversation_id::text FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, remote).Scan(&conv)
	})
	native := refsTestID(t)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, conv, conversations.Actor(f.actor), conversations.MessageInput{ID: native, Body: "private native context", ClientMessageID: refsTestID(t), AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: bot.Agent.ID}}})
		return e
	})
	run, out := teamsFinalize(t, f, remote, "only remote result")
	if out == "" {
		t.Fatal("remote no reply")
	}
	var count int
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=openfox_private.current_organization_id() AND run_id=$1::uuid`, run.ID).Scan(&count)
	})
	if count != 1 {
		t.Fatal("native mixed into remote Run", count)
	}
	_, nativeOut := teamsFinalize(t, f, native, "private native result")
	if nativeOut != "" {
		t.Fatal("native output exported")
	}
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(bot, "rejected-device", "request after grant removed"); e != nil {
		t.Fatal(e)
	}
	if e = w.ProcessTeamsInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.device_grants SET status='REVOKED',permission_version=permission_version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, f.grant)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		created, rejected, e := (dispatch.Store{}).MaterializeInputs(ctx, tx, f.org, 10)
		if e == nil && (created != 0 || rejected != 1) {
			t.Fatalf("reject counts %d/%d", created, rejected)
		}
		return e
	})
	if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) != 3 {
		t.Fatal("expected notice,remote,rejection", len(f.replies))
	}
	last := f.replies[2]["text"].(string)
	if last != "The Agent could not start this request: DEVICE_GRANT_UNAVAILABLE" || !strings.HasSuffix(f.replyPaths[2], "/activities/rejected-device") {
		t.Fatal("missing exact rejection original route", last, f.replyPaths[2])
	}
}

func TestTeamsRevocationDuringNetworkPG(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		name := "before_send"
		if afterWrite {
			name = "after_write"
		}
		t.Run(name, func(t *testing.T) {
			f := newTeamsFixture(t)
			bot, e := f.connect()
			if e != nil {
				t.Fatal(e)
			}
			f.bind(bot)
			w := &IMWorker{Pool: teamsWorkerRunner{f}, Service: f.service}
			if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			if _, e = f.receive(bot, "revoke-network", "controlled request"); e != nil {
				t.Fatal(e)
			}
			if e = w.ProcessTeamsInbound(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			_, out := teamsFinalize(t, f, teamsInboundID(f, bot, "revoke-network"), "controlled result")
			started, gate := make(chan struct{}), make(chan struct{})
			f.mu.Lock()
			if afterWrite {
				f.replyStarted = started
				f.replyGate = gate
			} else {
				f.memberStarted = started
				f.memberGate = gate
			}
			f.mu.Unlock()
			result := make(chan error, 1)
			go func() { result <- w.ProcessTeamsReplies(context.Background(), f.org) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("network did not begin")
			}
			revoke := make(chan error, 1)
			go func() {
				revoke <- f.run(func(ctx context.Context, tx pgx.Tx) error {
					rows, e := f.service.TeamsPersonalBindings(ctx, tx, f.org, f.actor)
					if e != nil {
						return e
					}
					return f.service.RemoveTeamsPersonalBinding(ctx, tx, f.org, f.actor, bot.ID, rows[0].Version)
				})
			}()
			select {
			case e = <-revoke:
				if e != nil {
					close(gate)
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				close(gate)
				t.Fatal("network held authorization transaction")
			}
			close(gate)
			if e = <-result; e != nil {
				t.Fatal(e)
			}
			var state string
			f.must(func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, out).Scan(&state)
			})
			want, calls := "SKIPPED", 1
			if afterWrite {
				want, calls = "DONE", 2
			}
			if state != want {
				t.Fatal("wrong recorded outcome", state, want)
			}
			f.mu.Lock()
			count := len(f.replies)
			f.mu.Unlock()
			if count != calls {
				t.Fatal("wrong send count", count, calls)
			}
			if e = w.ProcessTeamsReplies(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
		})
	}
}
