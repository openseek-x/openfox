package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
)

func (f *slackPG) fixtureAgent() (string, string) {
	f.t.Helper()
	space, agent, device := refsTestID(f.t), refsTestID(f.t), refsTestID(f.t)
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		f.t.Fatal(e)
	}
	defer c.Close(context.Background())
	for _, q := range []string{
		`INSERT INTO openfox.spaces(organization_id,id,name,prefix,visibility) VALUES('` + f.org + `','` + space + `','Slack fixture','SLACK','SHARED')`,
		`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES('` + device + `','` + device + `',4,'` + f.user + `','Slack fixture',clock_timestamp())`,
		`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES('` + f.org + `','` + device + `')`,
		`INSERT INTO openfox.agents(organization_id,id,device_id,space_id,owner_user_id,name,kind,visibility,current_config_version,version,allow_direct_chat) VALUES('` + f.org + `','` + agent + `','` + device + `','` + space + `','` + f.user + `','Slack Agent','CODE','SHARED',1,1,true)`,
		`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by) SELECT '` + f.org + `','` + agent + `',1,'` + device + `',id,'codex','default','` + f.user + `' FROM openfox.device_grants WHERE organization_id='` + f.org + `' AND device_id='` + device + `'`,
	} {
		if _, e = c.Exec(context.Background(), q); e != nil {
			f.t.Fatal(e)
		}
	}
	return space, agent
}
func newSlackDynamicPG(t *testing.T) (*slackPG, SlackApp) { return newSlackDynamicPGFor(t, "agent") }
func newSlackDynamicPGFor(t *testing.T, purpose string) (*slackPG, SlackApp) {
	f := newSlackPG(t)
	parent := f.installedCentral(true)
	space, agent := f.fixtureAgent()
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return f.service.saveSlackIdentity(ctx, tx, f.org, f.actor, parent, slack.Identity{Subject: "test-sub", TeamID: parent.TeamID, UserID: "UHUMAN"})
	}); e != nil {
		t.Fatal(e)
	}
	var creates, updates atomic.Int32
	appExternal := "AD" + f.org
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tooling.tokens.rotate":
			slackTestReply(w, map[string]any{"ok": true, "token": "synthetic-config-access", "refresh_token": "synthetic-next-refresh", "team_id": parent.TeamID, "user_id": "UHUMAN", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
		case "/api/apps.manifest.create":
			creates.Add(1)
			slackTestReply(w, map[string]any{"ok": true, "app_id": appExternal, "credentials": map[string]string{"client_id": "456.789", "client_secret": "dynamic-client-secret", "signing_secret": "dynamic-signing-secret"}})
		case "/api/apps.manifest.update":
			updates.Add(1)
			slackTestReply(w, map[string]any{"ok": true, "app_id": appExternal})
		case "/api/oauth.v2.access":
			slackTestReply(w, map[string]any{"ok": true, "access_token": "dynamic-access", "refresh_token": "dynamic-refresh", "expires_in": 43200, "token_type": "bot", "scope": strings.Join(slackAgentScopes, ","), "app_id": appExternal, "bot_user_id": "UDBOT", "team": map[string]string{"id": parent.TeamID, "name": "Fixture"}, "authed_user": map[string]string{"id": "UHUMAN"}})
		case "/api/auth.test":
			slackTestReply(w, map[string]any{"ok": true, "team_id": parent.TeamID, "user_id": "UDBOT", "bot_id": "BDBOT"})
		case "/api/users.info":
			slackTestReply(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": parent.TeamID, "name": "Fixture", "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
		case "/api/conversations.info":
			id := r.URL.Query().Get("channel")
			c := map[string]any{"id": id, "is_im": id == "D01", "is_mpim": false, "is_shared": false, "is_ext_shared": false, "is_archived": false}
			if id == "D01" {
				c["user"] = "UHUMAN"
			} else {
				c["is_channel"] = true
				c["is_group"] = false
				c["is_member"] = true
				c["is_private"] = true
				c["name"] = "controlled"
			}
			slackTestReply(w, map[string]any{"ok": true, "channel": c})
		case "/api/conversations.members":
			slackTestReply(w, map[string]any{"ok": true, "members": []string{"UHUMAN", "UDBOT"}, "response_metadata": map[string]string{"next_cursor": ""}})
		case "/api/conversations.replies":
			slackTestReply(w, map[string]any{"ok": true, "messages": []any{}, "has_more": false, "response_metadata": map[string]string{"next_cursor": ""}})
		default:
			t.Error("unexpected Slack path", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	var app SlackApp
	in := SlackCreateAppInput{ParentAppID: parent.ID, ParentVersion: parent.Version, Purpose: "agent", Name: "Controlled Agent", AgentID: agent, IdempotencyKey: "create-test-operation", ConfigurationRefreshToken: "synthetic-config-refresh"}
	if purpose == "task_assistant" {
		var runtime FeishuAssistantRuntime
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT computer_id::text,grant_id::text,runtime,model FROM openfox.agent_config_versions WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid AND config_version=1`, agent).Scan(&runtime.ComputerID, &runtime.GrantID, &runtime.Runtime, &runtime.Model)
		}); e != nil {
			t.Fatal(e)
		}
		in.Purpose = purpose
		in.AgentID = ""
		in.SpaceID = space
		in.Runtime = &runtime
	}

	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		var fresh bool
		app, fresh, e = f.service.PrepareSlackApp(ctx, tx, f.org, f.actor, in)
		if e == nil && !fresh {
			t.Error("not fresh")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	proof := f.service.RotateSlackConfiguration(context.Background(), in.ConfigurationRefreshToken)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		app, e = f.service.FinishSlackProof(ctx, tx, f.org, f.actor, app.ID, app.Version, proof)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	worker := f.worker()
	for range 2 {
		if e := worker.processSlackOperations(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	read := func() {
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			app, e = loadSlackApp(ctx, tx, app.ID)
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if creates.Load() != 1 || updates.Load() != 1 || app.State != "WAITING_URL_VERIFICATION" {
		t.Fatal("manifest gates", creates.Load(), updates.Load(), app.State)
	}
	raw := []byte(`{"type":"url_verification","challenge":"synthetic-challenge"}`)
	ts, sig := slackSigned("dynamic-signing-secret", raw)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		challenge, e := f.service.ReceiveSlackEvent(ctx, tx, f.org, app.ID, ts, sig, raw)
		if e == nil && challenge != "synthetic-challenge" {
			t.Error(challenge)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	read()
	if app.State != "WAITING_INSTALL" {
		t.Fatal(app.State)
	}
	var session SlackOAuthSession
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.StartSlackAuthorization(ctx, tx, f.org, f.actor, SlackOAuthInput{Purpose: "app", AppID: app.ID, Version: app.Version, IdempotencyKey: "dynamic-oauth-operation"})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	target := f.claim(session, false)
	outcome := f.service.ExchangeSlackCallback(context.Background(), target, "synthetic-code")
	if outcome.Failure != "" {
		t.Fatal(outcome.Failure)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackCallback(ctx, tx, f.org, target, outcome, false)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	read()
	if app.State != "ACTIVE" {
		t.Fatal(app.State)
	}
	return f, app
}
func TestSlackDynamicManifestChannelAndInboundPG(t *testing.T) {
	f, app := newSlackDynamicPG(t)
	worker := f.worker()
	send := func(channel, channelType, thread string, n int) {
		t.Helper()
		event := SlackEnvelope{Type: "event_callback", AppID: app.ExternalAppID, TeamID: app.TeamID, EventID: fmt.Sprintf("E%d", n), EventTime: time.Now().Unix(), Event: SlackEvent{Type: "message", Channel: channel, ChannelType: channelType, User: "UHUMAN", Text: "<@UDBOT> synthetic request", TS: fmt.Sprintf("%d.%06d", time.Now().Unix(), n), ThreadTS: thread}}
		raw, _ := json.Marshal(event)
		ts, sig := slackSigned("dynamic-signing-secret", raw)
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			_, e := f.service.ReceiveSlackEvent(ctx, tx, f.org, app.ID, ts, sig, raw)
			return e
		}); e != nil {
			t.Fatal(e)
		}
		if e := worker.processSlackMessages(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	send("D01", "im", "", 1)
	send("D01", "im", "", 2)
	var chTarget SlackChannelTarget
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		chTarget, e = f.service.PrepareSlackChannel(ctx, tx, f.org, f.actor, app.BotID, "C01", SlackChannelInput{Enabled: true, PublicReplyConsent: true})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	remote, e := f.service.VerifySlackChannel(context.Background(), chTarget)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackChannel(ctx, tx, f.org, chTarget, remote)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	send("C01", "channel", "", 3)
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var routes, inputs, done int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_routes WHERE organization_id=openfox_private.current_organization_id()`).Scan(&routes); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_input_authorizations WHERE organization_id=openfox_private.current_organization_id()`).Scan(&inputs); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND state='DONE'`).Scan(&done); e != nil {
			return e
		}
		if routes != 2 || inputs != 3 || done != 3 {
			var reasons string
			_ = tx.QueryRow(ctx, `SELECT string_agg(state||':'||status_reason,',') FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id()`).Scan(&reasons)
			t.Fatalf("routes=%d inputs=%d done=%d events=%s", routes, inputs, done, reasons)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestSlackTaskAssistantCreationAndUnboundDMPG(t *testing.T) {
	f, a := newSlackDynamicPGFor(t, "task_assistant")
	if a.Kind != "TASK_ASSISTANT" || a.AgentID == "" || a.SpaceID == "" {
		t.Fatal("assistant shape", a.Kind)
	}
	var id string
	var version uint64
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var builtin string
		if e := tx.QueryRow(ctx, `SELECT builtin_kind FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.AgentID).Scan(&builtin); e != nil {
			return e
		}
		if builtin != "TASK_ASSISTANT" {
			t.Fatal(builtin)
		}
		if e := tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND parent_app_id=$1::uuid AND active`, a.ParentAppID).Scan(&id, &version); e != nil {
			return e
		}
		return f.service.DisconnectSlackIdentity(ctx, tx, f.org, f.actor, id, version)
	}); e != nil {
		t.Fatal(e)
	}
	f.receive(a, "dynamic-signing-secret", SlackEvent{Type: "message", User: "UHUMAN", Channel: "D01", ChannelType: "im", TS: fmt.Sprintf("%d.000001", time.Now().Unix()), Text: "UNBOUND_PRIVATE_CANARY"}, "Eunbound")
	worker := f.worker()
	if e := worker.processSlackMessages(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var routes int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_routes WHERE organization_id=openfox_private.current_organization_id()`).Scan(&routes); e != nil {
			return e
		}
		if routes != 0 {
			t.Fatal("unbound launched conversation")
		}
		var body string
		e := tx.QueryRow(ctx, `SELECT body::text FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_kind='NOTICE'`).Scan(&body)
		if strings.Contains(body, "UNBOUND_PRIVATE_CANARY") {
			t.Fatal("notice leaked inbound")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSlackParentGenerationViewDoesNotMutateChildPG(t *testing.T) {
	f, a := newSlackDynamicPG(t)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET generation=generation+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ParentAppID); e != nil {
			return e
		}
		view, e := f.service.GetSlackApp(ctx, tx, f.org, f.actor, a.ID)
		if e != nil {
			return e
		}
		if view.State != "NEEDS_REAUTH" || view.StatusReason != "parent_authorization_changed" || view.Version != a.Version {
			t.Fatal("derived view", view.State, view.Version)
		}
		stored, e := loadSlackApp(ctx, tx, a.ID)
		if e == nil && (stored.State != "ACTIVE" || stored.Version != a.Version) {
			t.Fatal("read mutated stored app")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
