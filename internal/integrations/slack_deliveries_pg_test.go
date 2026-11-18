package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/integrations/slack"
)

type slackOutboundPG struct {
	*slackPG
	app                  SlackApp
	worker               *IMWorker
	conversation, source string
	mu                   sync.Mutex
	mode                 string
	posts                int
	saved                slack.Message
	beforePost           func()
	beforeUser           func()
}

func newSlackOutboundPG(t *testing.T) *slackOutboundPG {
	f := newSlackPG(t)
	a := f.installedCentral(true)
	space, agent := f.fixtureAgent()
	x := &slackOutboundPG{slackPG: f, app: a, conversation: refsTestID(t)}
	err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := f.service.saveSlackIdentity(ctx, tx, f.org, f.actor, a, slack.Identity{Subject: "outbound-fixture", TeamID: a.TeamID, UserID: "UHUMAN"}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,'PRIVATE')`, x.conversation, space, agent, f.user); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.user_notification_channels(user_id,channel,enabled) VALUES($1::uuid,'slack',true) ON CONFLICT(user_id,channel) DO UPDATE SET enabled=true`, f.user); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		x.mu.Lock()
		mode, hook, beforeUser := x.mode, x.beforePost, x.beforeUser
		x.mu.Unlock()
		switch r.URL.Path {
		case "/api/users.info":
			if beforeUser != nil {
				beforeUser()
			}
			slackTestReply(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": a.TeamID, "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
		case "/api/conversations.open", "/api/conversations.info":
			slackTestReply(w, map[string]any{"ok": true, "channel": map[string]any{"id": "DLOCAL", "user": "UHUMAN", "is_im": true, "is_mpim": false, "is_archived": false}})
		case "/api/chat.postMessage":
			if hook != nil {
				hook()
			}
			var p struct {
				Channel, Text, ThreadTS string
				Metadata                *slack.MessageMetadata
			}
			var raw struct {
				Channel  string                 `json:"channel"`
				Text     string                 `json:"text"`
				ThreadTS string                 `json:"thread_ts"`
				Metadata *slack.MessageMetadata `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Error(err)
			}
			p.Channel, p.Text, p.ThreadTS, p.Metadata = raw.Channel, raw.Text, raw.ThreadTS, raw.Metadata
			x.mu.Lock()
			x.posts++
			x.saved = slack.Message{Type: "message", Subtype: "bot_message", AppID: a.ExternalAppID, BotID: "BBOT", User: a.BotUserID, Team: a.TeamID, TS: "1700000000.000123", ThreadTS: p.ThreadTS, Text: p.Text, Metadata: p.Metadata}
			x.mu.Unlock()
			if mode == "unknown" {
				http.Error(w, "synthetic upstream uncertainty", 500)
				return
			}
			if mode == "rate" {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				return
			}
			slackTestReply(w, map[string]any{"ok": true, "channel": p.Channel, "ts": "1700000000.000123"})
		case "/api/conversations.history":
			x.mu.Lock()
			m := x.saved
			x.mu.Unlock()
			slackTestReply(w, map[string]any{"ok": true, "messages": []slack.Message{m}, "has_more": false})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			http.Error(w, "unexpected", 400)
		}
	})
	x.worker = f.worker()
	if err := x.worker.inOrganization(context.Background(), x.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT openfox_private.enqueue_notification($1::uuid,'MENTION',$2::uuid,NULL,NULL,NULL,'outbound-fixture','You were mentioned in a conversation.')`, f.user, x.conversation); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_notification_outbox WHERE organization_id=openfox_private.current_organization_id() AND app_connection_id=$1::uuid`, a.ConnectionID).Scan(&x.source)
	}); err != nil {
		t.Fatal(err)
	}
	return x
}
func (x *slackOutboundPG) step() {
	x.t.Helper()
	if err := x.worker.processSlackNotifications(context.Background(), x.org); err != nil {
		x.t.Fatal(err)
	}
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		x.t.Fatal(err)
	}
}
func (x *slackOutboundPG) row() slackDeliveryRow {
	x.t.Helper()
	var d slackDeliveryRow
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_id=$1::uuid ORDER BY part LIMIT 1`, x.source).Scan(&id); err != nil {
			return err
		}
		var err error
		d, err = loadSlackDelivery(ctx, tx, id, false)
		return err
	}); err != nil {
		x.t.Fatal(err)
	}
	return d
}
func (x *slackOutboundPG) update(q string, args ...any) {
	x.t.Helper()
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q, args...); return err }); err != nil {
		x.t.Fatal(err)
	}
}
func (x *slackOutboundPG) count() int { x.mu.Lock(); defer x.mu.Unlock(); return x.posts }
func TestSlackDeliveryNotificationPipelinePG(t *testing.T) {
	x := newSlackOutboundPG(t)
	x.step()
	if d := x.row(); d.State != "PENDING" || d.ChannelID != "DLOCAL" || x.count() != 0 {
		t.Fatal("open DM phase", d.State, d.ChannelID, x.count())
	}
	x.step()
	if d := x.row(); d.State != "DONE" || d.RemoteTS == "" || x.count() != 1 {
		t.Fatal("send phase", d.State, x.count())
	}
	x.step()
	if x.count() != 1 {
		t.Fatal("duplicate send")
	}
	x.mu.Lock()
	text := x.saved.Text
	x.mu.Unlock()
	if !strings.Contains(text, "/chat/"+x.conversation) {
		t.Fatal("protected link missing")
	}
	var status string
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM openfox.im_notification_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, x.source).Scan(&status)
	}); err != nil || status != "DONE" {
		t.Fatal(status, err)
	}
}
func TestSlackDeliveryRevocationAndPolicyPG(t *testing.T) {
	for _, kind := range []string{"identity", "policy", "permission"} {
		t.Run(kind, func(t *testing.T) {
			x := newSlackOutboundPG(t)
			x.step()
			switch kind {
			case "identity":
				x.update(`UPDATE openfox.slack_identities SET active=false,version=version+1 WHERE organization_id=openfox_private.current_organization_id()`)
			case "policy":
				x.update(`UPDATE openfox.user_notification_channels SET enabled=false WHERE user_id=$1::uuid AND channel='slack'`, x.user)
			case "permission":
				other := refsTestID(t)
				c, err := pgx.Connect(context.Background(), x.dsn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.Exec(context.Background(), `INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, other, other+"@example.test"); err != nil {
					t.Fatal(err)
				}
				if _, err = c.Exec(context.Background(), `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, x.org, other); err != nil {
					t.Fatal(err)
				}
				c.Close(context.Background())
				x.update(`UPDATE openfox.conversations SET owner_user_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, x.conversation, other)
			}
			x.step()
			if d := x.row(); d.State != "SKIPPED" || x.count() != 0 {
				t.Fatal("revoked delivery", d.State, x.count())
			}
		})
	}
}
func TestSlackDeliveryUnknownReceiptAndManualPG(t *testing.T) {
	for _, action := range []string{"event", "verify", "skip"} {
		t.Run(action, func(t *testing.T) {
			x := newSlackOutboundPG(t)
			x.mode = "unknown"
			x.step()
			x.step()
			d := x.row()
			if d.State != "UNKNOWN" {
				t.Fatal(d.State)
			}
			x.step()
			if x.count() != 1 {
				t.Fatal("unknown retried")
			}
			if action == "event" {
				x.mu.Lock()
				m := x.saved
				x.mu.Unlock()
				event := SlackEvent{Type: m.Type, Subtype: m.Subtype, User: m.User, Text: m.Text, TS: m.TS, ThreadTS: m.ThreadTS, BotID: m.BotID, AppID: m.AppID, Team: m.Team, Metadata: m.Metadata, Channel: d.ChannelID}
				event.BotID = "OTHER"
				if err := x.worker.processSlackReceipt(context.Background(), x.org, x.app.ID, x.app.Generation, event); err != nil {
					t.Fatal(err)
				}
				if x.row().State != "UNKNOWN" {
					t.Fatal("forged receipt accepted")
				}
				event.BotID = m.BotID
				if err := x.worker.processSlackReceipt(context.Background(), x.org, x.app.ID, x.app.Generation, event); err != nil {
					t.Fatal(err)
				}
			} else {
				var work SlackDeliveryResolution
				remote := ""
				if action == "verify" {
					remote = "1700000000.000123"
				}
				if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
					var err error
					work, err = x.service.PrepareSlackDeliveryResolution(ctx, tx, x.org, x.actor, x.app.ID, d.ID, SlackResolveDeliveryInput{Version: d.Version, Action: action, RemoteTS: remote})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				result := x.service.ExecuteSlackDeliveryResolution(context.Background(), work)
				if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
					_, err := x.service.FinishSlackDeliveryResolution(ctx, tx, x.org, x.actor, work, result)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
					_, err := x.service.FinishSlackDeliveryResolution(ctx, tx, x.org, x.actor, work, result)
					return err
				}); !errors.Is(err, ErrVersionConflict) {
					t.Fatal("stale resolution replay", err)
				}
			}
			want := "DONE"
			if action == "skip" {
				want = "SKIPPED"
			}
			if x.row().State != want || x.count() != 1 {
				t.Fatal(x.row().State, x.count())
			}
		})
	}
}
func TestSlackDeliveryConcurrentLeaseAndLateSuccessPG(t *testing.T) {
	x := newSlackOutboundPG(t)
	x.step()
	entered, release := make(chan struct{}), make(chan struct{})
	x.beforePost = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- x.worker.processSlackDeliveries(context.Background(), x.org) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("send did not start")
	}
	// A second process cannot claim an installation with a live part lease.
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	x.update(`UPDATE openfox.slack_deliveries SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id()`)
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	if x.row().State != "UNKNOWN" {
		t.Fatal("expired POST was reset")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if x.row().State != "DONE" || x.count() != 1 {
		t.Fatal("positive late receipt lost", x.row().State, x.count())
	}
}
func TestSlackDeliveryRateLimitDefersWithoutReplayPG(t *testing.T) {
	x := newSlackOutboundPG(t)
	x.mode = "rate"
	x.step()
	x.step()
	d := x.row()
	if d.State != "PENDING" || x.count() != 1 {
		t.Fatal(d.State, x.count())
	}
	var wait float64
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT extract(epoch FROM next_attempt_at-clock_timestamp())::float8 FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, d.ID).Scan(&wait)
	}); err != nil {
		t.Fatal(err)
	}
	if wait < 110 || wait > 121 {
		t.Fatal("retry delay changed", wait)
	}
	x.step()
	if x.count() != 1 {
		t.Fatal("ignored retry after")
	}
}

func TestSlackDeliveryReplyUsesExactRunAndInputFencePG(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "actual_run", true: "rebound_identity"}[revoke], func(t *testing.T) {
			x, identity := newSlackReplyPG(t)
			if err := x.worker.processSlackReplies(context.Background(), x.org); err != nil {
				t.Fatal(err)
			}
			if revoke {
				x.update(`UPDATE openfox.slack_identities SET version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, identity)
			}
			if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
				t.Fatal(err)
			}
			if revoke {
				if x.row().State != "SKIPPED" || x.count() != 0 {
					t.Fatal("old Run revived", x.row().State, x.count())
				}
			} else {
				if x.row().State != "DONE" || x.count() != 1 {
					t.Fatal("reply failed", x.row().State, x.count())
				}
				x.update(`UPDATE openfox.slack_rate_limits SET not_before=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id()`)
				if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
					t.Fatal(err)
				}
				if x.count() != 2 {
					t.Fatal("later part not sent", x.count())
				}
			}
			// A later SYSTEM message without an explicit source must not inherit the
			// previous Run merely because its timestamp is nearby.
			if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `WITH n AS(UPDATE openfox.conversations SET next_message_seq=next_message_seq+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING next_message_seq-1 seq)
 INSERT INTO openfox.messages(organization_id,conversation_id,seq,author_kind,body) SELECT openfox_private.current_organization_id(),$1::uuid,seq,'SYSTEM','unattributed rejection' FROM n`, x.conversation)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := x.worker.processSlackReplies(context.Background(), x.org); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND source_run_id IS NULL AND bot_id=(SELECT bot_id FROM openfox.slack_routes WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid)`, x.conversation).Scan(&status)
			}); err != nil || status != "SKIPPED" {
				t.Fatal("unattributed message borrowed actor", status, err)
			}
		})
	}
}

func TestSlackDeliverySharedConversationUsesRecipientMembershipPG(t *testing.T) {
	x := newSlackOutboundPG(t)
	other := refsTestID(t)
	c, err := pgx.Connect(context.Background(), x.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err = c.Exec(context.Background(), `INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, other, other+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(context.Background(), `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, x.org, other); err != nil {
		t.Fatal(err)
	}
	x.update(`UPDATE openfox.conversations SET owner_user_id=$2::uuid,visibility='SHARED' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, x.conversation, other)
	x.update(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'VIEWER')`, x.conversation, x.user)
	x.step()
	x.step()
	if d := x.row(); d.State != "DONE" || x.count() != 1 {
		t.Fatal("worker substituted its actor for recipient", d.State, x.count())
	}
}

func newSlackReplyPG(t *testing.T) (*slackOutboundPG, string) {
	x := newSlackOutboundPG(t)
	appID, botID, connID, routeID, runID := refsTestID(t), refsTestID(t), refsTestID(t), refsTestID(t), refsTestID(t)
	var agent, space, identity string
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT agent_id::text,space_id::text FROM openfox.conversations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, x.conversation).Scan(&agent, &space); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND parent_app_id=$1::uuid AND active`, x.app.ID).Scan(&identity); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,credentials_sealed,credentials_key_id,created_by)
 SELECT organization_id,$2::uuid,'slack','ORGANIZATION',$2,'Reply bot','agent','ACTIVE',NULL,'',$3::uuid FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, x.app.ConnectionID, connID, x.user); err != nil {
			return err
		}
		grant := slack.TokenGrant{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour), TokenType: "bot"}
		raw, _ := json.Marshal(grant)
		blob, key, err := x.service.seal(raw, connectionAAD(x.org, connID))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connID, blob, key); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'AGENT',$3::uuid,$4::uuid)`, botID, connID, agent, x.user); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.slack_apps(organization_id,id,connection_id,parent_app_id,parent_generation,bot_id,kind,name,app_id,team_id,bot_user_id,bot_external_id,created_by,actor_version,state,operation_key,request_hash,installed_at,url_verified_at,activated_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,'AGENT','Reply bot',$1,$6,'UBOT','BBOT',$7::uuid,1,'ACTIVE',$1,decode(repeat('ab',32),'hex'),now(),now(),now())`, appID, connID, x.app.ID, x.app.Generation, botID, x.app.TeamID, x.user); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'DLOCAL','p2p',$2::uuid,$3::uuid)`, botID, x.conversation, x.user); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.slack_routes(organization_id,id,app_id,bot_id,generation,channel_id,identity_id,identity_version,conversation_id)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,1,'DLOCAL',$4::uuid,1,$5::uuid)`, routeID, appID, botID, identity, x.conversation); err != nil {
			return err
		}
		if _, err = (conversations.Service{}).PostMessage(ctx, tx, x.org, x.conversation, conversations.Actor{UserID: x.user, Role: x.actor.Role, AuthorizationVersion: 1}, conversations.MessageInput{ID: refsTestID(t), Body: "controlled run input", ClientMessageID: "controlled-input", Mentions: []conversations.Mention{{AgentID: agent}}}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,finished_at)
 SELECT v.organization_id,$1::uuid,$2::uuid,$3::uuid,v.agent_id,v.computer_id,v.grant_id,v.config_version,g.permission_version,$4::uuid,'SUCCEEDED',clock_timestamp()
 FROM openfox.agent_config_versions v JOIN openfox.device_grants g ON (g.organization_id,g.id)=(v.organization_id,v.grant_id)
 WHERE v.organization_id=openfox_private.current_organization_id() AND v.agent_id=$5::uuid AND v.config_version=1`, runID, space, x.conversation, x.user, agent); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE openfox.conversation_inputs SET run_id=$2::uuid,status='ASSIGNED',assigned_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid`, x.conversation, runID); err != nil {
			return err
		}
		message, err := dispatch.FinalizeConversation(ctx, tx, execution.Run{ID: runID, OrganizationID: x.org, ConversationID: x.conversation, AgentID: agent, SpaceID: space, ActorID: x.user, State: execution.StateSucceeded}, dispatch.AuthorAgent, strings.Repeat("reply ", 900), false)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid AND source_run_id=$2::uuid`, message, runID).Scan(&x.source)
	}); err != nil {
		t.Fatal(err)
	}
	return x, identity
}

func TestSlackDeliveryUnknownStopsRemainingPartsPG(t *testing.T) {
	x, _ := newSlackReplyPG(t)
	x.mode = "unknown"
	if err := x.worker.processSlackReplies(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	d := x.row()
	if d.State != "UNKNOWN" || x.count() != 1 {
		t.Fatal(d.State, x.count())
	}
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	if x.count() != 1 {
		t.Fatal("later part overtook unknown")
	}
	var work SlackDeliveryResolution
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		work, err = x.service.PrepareSlackDeliveryResolution(ctx, tx, x.org, x.actor, d.AppID, d.ID, SlackResolveDeliveryInput{Version: d.Version, Action: "skip"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result := x.service.ExecuteSlackDeliveryResolution(context.Background(), work)
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := x.service.FinishSlackDeliveryResolution(ctx, tx, x.org, x.actor, work, result)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var skipped int
	if err := x.run(x.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_id=$1::uuid AND state='SKIPPED'`, x.source).Scan(&skipped)
	}); err != nil || skipped != 2 {
		t.Fatal("remaining parts not stopped", skipped, err)
	}
	if err := x.worker.processSlackDeliveries(context.Background(), x.org); err != nil {
		t.Fatal(err)
	}
	if x.count() != 1 {
		t.Fatal("manual skip replayed")
	}
}
func TestSlackDeliveryRechecksAfterNetworkReadPG(t *testing.T) {
	x := newSlackOutboundPG(t)
	x.step()
	var once sync.Once
	x.beforeUser = func() {
		once.Do(func() {
			x.update(`UPDATE openfox.slack_identities SET active=false,version=version+1 WHERE organization_id=openfox_private.current_organization_id()`)
		})
	}
	x.step()
	if d := x.row(); d.State != "SKIPPED" || x.count() != 0 {
		t.Fatal("read preflight bypassed later revoke", d.State, x.count())
	}
}
