package integrations

import (
	"bytes"
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

func (f *slackPG) receive(app SlackApp, secret string, ev SlackEvent, id string) {
	f.t.Helper()
	raw, _ := json.Marshal(SlackEnvelope{Type: "event_callback", AppID: app.ExternalAppID, TeamID: app.TeamID, EventID: id, EventTime: time.Now().Unix(), Event: ev})
	ts, sig := slackSigned(secret, raw)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReceiveSlackEvent(ctx, tx, f.org, app.ID, ts, sig, raw)
		return e
	}); e != nil {
		f.t.Fatal(e)
	}
}
func TestSlackSpecialNoticeUnknownAndRevocationPG(t *testing.T) {
	f := newSlackPG(t)
	app := f.installedCentral(true)
	var posts atomic.Int32
	var input slack.PostMessageInput
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users.info":
			slackTestReply(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": app.TeamID, "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
		case "/api/conversations.info":
			slackTestReply(w, map[string]any{"ok": true, "channel": map[string]any{"id": "D01", "is_im": true, "is_mpim": false, "is_shared": false, "is_ext_shared": false, "is_archived": false, "user": "UHUMAN"}})
		case "/api/chat.postMessage":
			posts.Add(1)
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				t.Error(e)
			}
			// A 2xx with no complete acknowledgement is an uncertain mutation.
			slackTestReply(w, map[string]any{"ok": true})
		default:
			t.Error("unexpected remote request", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	worker := f.worker()
	ts := fmt.Sprintf("%d.000001", time.Now().Unix())
	f.receive(app, f.service.SlackSigningSecret, SlackEvent{Type: "message", User: "UHUMAN", Channel: "D01", ChannelType: "im", TS: ts, Text: "PRIVATE_INBOUND_CANARY"}, "Ecentral")
	if e := worker.processSlackSpecialEvents(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := worker.processSlackSpecialDeliveries(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	state := func() string {
		var state string
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT state FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id()`).Scan(&state)
		}); e != nil {
			t.Fatal(e)
		}
		return state
	}
	if state() != "UNKNOWN" || posts.Load() != 1 {
		t.Fatal(state(), posts.Load())
	}
	if e := worker.processSlackSpecialDeliveries(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if posts.Load() != 1 || strings.Contains(input.Text, "PRIVATE_INBOUND_CANARY") || input.Metadata == nil || len(input.Metadata.EventPayload) != 3 {
		t.Fatal("notice repeated or leaked source")
	}
	receipt := SlackEvent{Type: "message", Subtype: "bot_message", User: app.BotUserID, Channel: "D01", ThreadTS: ts, TS: fmt.Sprintf("%d.000002", time.Now().Unix()), BotID: "BBOT", AppID: app.ExternalAppID, Team: app.TeamID, Metadata: input.Metadata, Text: "platform changed render"}
	forged := receipt
	forged.BotID = "BOTHER"
	if e := worker.processSlackSpecialReceipt(context.Background(), f.org, app.ID, app.Generation, forged); e != nil {
		t.Fatal(e)
	}
	if state() != "UNKNOWN" {
		t.Fatal("forged sender settled notice")
	}
	if e := worker.processSlackSpecialReceipt(context.Background(), f.org, app.ID, app.Generation, receipt); e != nil {
		t.Fatal(e)
	}
	if state() != "DONE" {
		t.Fatal(state())
	}
	f.receive(app, f.service.SlackSigningSecret, SlackEvent{Type: "app_uninstalled"}, "Euninstall")
	if e := worker.processSlackSpecialEvents(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := loadSlackApp(ctx, tx, app.ID)
		if e == nil && (a.State != "NEEDS_REAUTH" || a.Generation != app.Generation+1) {
			t.Fatal(a.State, a.Generation)
		}
		var clear bool
		if e == nil {
			e = tx.QueryRow(ctx, `SELECT credentials_sealed IS NULL FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID).Scan(&clear)
			if !clear {
				t.Fatal("revoked token retained")
			}
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSlackPreviewURLBoundary(t *testing.T) {
	org := "11111111-1111-4111-8111-111111111111"
	space := "22222222-2222-4222-8222-222222222222"
	valid := "https://openfox.example.test/base/o/" + org + "/spaces/" + space + "/tasks?task=FOX-1"
	if s, d, ok := slackTaskURL("https://openfox.example.test/base", valid, org); !ok || s != space || d != "FOX-1" {
		t.Fatal(s, d, ok)
	}
	for _, bad := range []string{strings.Replace(valid, "https://", "http://", 1), strings.Replace(valid, "openfox.example.test", "openfox.example.test.evil", 1), valid + "&task=FOX-2", valid + "&extra=1", valid + "#fragment", strings.Replace(valid, "/spaces/", "/spaces/%2e%2e/", 1), strings.Replace(valid, "https://", "https://user@", 1)} {
		if _, _, ok := slackTaskURL("https://openfox.example.test/base", bad, org); ok {
			t.Fatal("accepted", bad)
		}
	}
}

func TestSlackUnfurlActualTaskAndChannelRevocationPG(t *testing.T) {
	f, child := newSlackDynamicPG(t)
	var parent SlackApp
	task := refsTestID(t)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		parent, e = loadSlackApp(ctx, tx, child.ParentAppID)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES(openfox_private.current_organization_id(),'Todo','TODO',0)`); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.task_types(organization_id,space_id,name,is_default) VALUES(openfox_private.current_organization_id(),$1::uuid,'Task',true)`, child.SpaceID); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.tasks(organization_id,id,space_id,sequence,title,type_id,status_id) SELECT openfox_private.current_organization_id(),$1::uuid,$2::uuid,1,'Controlled preview <text>',tt.id,st.id FROM openfox.task_types tt JOIN openfox.task_statuses st ON st.organization_id=tt.organization_id WHERE tt.organization_id=openfox_private.current_organization_id() AND tt.space_id=$2::uuid AND st.category='TODO'`, task, child.SpaceID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var target SlackChannelTarget
	var channel SlackChannel
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = f.service.PrepareSlackChannel(ctx, tx, f.org, f.actor, child.BotID, "C01", SlackChannelInput{Enabled: true, PublicReplyConsent: true})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	remote, e := f.service.VerifySlackChannel(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		channel, e = f.service.FinishSlackChannel(ctx, tx, f.org, target, remote)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var sends atomic.Int32
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users.info":
			slackTestReply(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": parent.TeamID, "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
		case "/api/conversations.info":
			slackTestReply(w, map[string]any{"ok": true, "channel": map[string]any{"id": "C01", "is_im": false, "is_mpim": false, "is_channel": true, "is_group": false, "is_shared": false, "is_ext_shared": false, "is_member": true, "is_private": true, "is_archived": false}})
		case "/api/conversations.members":
			slackTestReply(w, map[string]any{"ok": true, "members": []string{"UHUMAN"}, "response_metadata": map[string]string{"next_cursor": ""}})
		case "/api/chat.unfurl":
			sends.Add(1)
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("unfurl JSON")
			}
			raw, _ := json.Marshal(body)
			raw = bytes.ReplaceAll(raw, []byte(`\u0026`), []byte("&"))
			if !strings.Contains(string(raw), "Controlled preview &lt;text&gt;") {
				t.Error("preview lacks escaped title")
			}
			slackTestReply(w, map[string]any{"ok": true})
		default:
			t.Error(r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	worker := f.worker()
	send := func(id string, n int) {
		var event SlackEvent
		raw, _ := json.Marshal(map[string]any{"type": "link_shared", "user": "UHUMAN", "channel": "C01", "ts": fmt.Sprintf("%d.%06d", time.Now().Unix(), n), "links": []map[string]string{{"url": f.service.PublicURL + "/o/" + f.org + "/spaces/" + child.SpaceID + "/tasks?task=SLACK-1", "domain": "openfox.example.test"}}})
		_ = json.Unmarshal(raw, &event)
		f.receive(parent, f.service.SlackSigningSecret, event, id)
		if e := worker.processSlackSpecialEvents(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	send("Epreview1", 1)
	if e = worker.processSlackSpecialDeliveries(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if sends.Load() != 1 {
		var reason string
		_ = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT string_agg(state||':'||status_reason,',') FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id()`).Scan(&reason)
		})
		t.Fatal("preview not sent", reason)
	}
	send("Epreview2", 2)
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.DisableSlackChannel(ctx, tx, f.org, f.actor, child.BotID, "C01", channel.Version)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e = worker.processSlackSpecialDeliveries(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if sends.Load() != 1 {
		t.Fatal("closed epoch allowed preview")
	}
	var skipped int
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_kind='UNFURL' AND state='SKIPPED'`).Scan(&skipped)
	}); e != nil || skipped != 1 {
		t.Fatal(skipped, e)
	}
}
