package stage3

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations"
	"openfox/internal/tenancy"
)

func TestWeComBindingAndNotificationConsent(t *testing.T) {
	env := newS3Env(t)
	client := env.client(s3MemberID)
	agentID := env.insertAgent(env.space.ID)
	connectionID, botID := s3NewID(t), s3NewID(t)
	env.exec(`INSERT INTO openfox.integration_connections
		(organization_id,id,provider,scope,external_id,display_name,account_kind,created_by)
		VALUES ($1::uuid,$2::uuid,'wecom','ORGANIZATION',$3,'Test WeCom bot','agent',$4::uuid)`,
		env.org, connectionID, "test-wecom-"+s3Suffix(t), s3OwnerID)
	env.exec(`INSERT INTO openfox.im_bots
		(organization_id,id,connection_id,kind,agent_id,created_by)
		VALUES ($1::uuid,$2::uuid,$3::uuid,'AGENT',$4::uuid,$5::uuid)`,
		env.org, botID, connectionID, agentID, s3OwnerID)

	path := "/v1/organizations/" + env.org + "/integrations/wecom/personal"
	var issued integrations.WeComBindingCode
	client.must(http.StatusCreated, http.MethodPost, path+"/codes", map[string]string{"bot_id": botID}, &issued)
	if issued.BotID != botID || len(issued.Code) != 32 {
		t.Fatalf("binding code = %+v", issued)
	}
	selection := tenancy.OrganizationSelection{OrganizationID: env.org,
		ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
	consume := func(code string) bool {
		t.Helper()
		var bound bool
		err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceOrganizationWorker,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
				return selection, nil
			}), func(ctx context.Context, tx pgx.Tx) error {
				var err error
				bound, err = (integrations.Service{}).ConsumeWeComBindingCode(ctx, tx, botID, "wecom-member-"+s3MemberID, code)
				return err
			})
		if err != nil {
			t.Fatal(err)
		}
		return bound
	}
	if !consume(issued.Code) || consume(issued.Code) {
		t.Fatal("binding code must be accepted exactly once")
	}
	var personal integrations.WeComPersonalView
	client.must(http.StatusOK, http.MethodGet, path, nil, &personal)
	if len(personal.Bindings) != 1 || personal.Bindings[0].BotID != botID || personal.Bindings[0].NotificationTarget {
		t.Fatalf("personal bindings = %+v", personal.Bindings)
	}
	var channels struct {
		Channels []notificationChannelView `json:"channels"`
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me/notification-channels", nil, &channels)
	if len(channels.Channels) != 5 || channels.Channels[4].Channel != "wecom" || channels.Channels[4].Enabled ||
		channels.Channels[4].DirectMessages == nil || *channels.Channels[4].DirectMessages {
		t.Fatalf("default wecom consent = %+v", channels.Channels)
	}
	task := env.createTask(env.client(s3OwnerID), env.space.ID, nil)
	queueCount := func(key string) int {
		t.Helper()
		return env.count(`SELECT count(*) FROM openfox.im_notification_outbox q
			JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
			JOIN openfox.integration_connections parent ON parent.organization_id=q.organization_id AND parent.id=q.app_connection_id
			WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2 AND parent.provider='wecom'`, env.org, key)
	}
	insert := func(kind string) string {
		t.Helper()
		key := "wecom-test:" + s3NewID(t)
		insertEmailTestNotice(env, s3MemberID, task.ID, kind, key, "Safe summary")
		return key
	}
	client.must(http.StatusOK, http.MethodPut, path+"/notification-target", map[string]any{"bot_id": botID}, &personal)
	if !personal.Bindings[0].NotificationTarget || queueCount(insert("MENTION")) != 0 {
		t.Fatal("default-off wecom channel must not enqueue notifications")
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/wecom",
		map[string]any{"enabled": true, "disabled_events": []string{}}, nil)
	client.must(http.StatusOK, http.MethodPut, path+"/notification-target", map[string]any{"bot_id": nil}, &personal)
	if queueCount(insert("MENTION")) != 0 {
		t.Fatal("wecom notification without selected personal target was queued")
	}
	client.must(http.StatusOK, http.MethodPut, path+"/notification-target", map[string]any{"bot_id": botID}, &personal)
	queuedKey := insert("MENTION")
	if queueCount(queuedKey) != 1 {
		t.Fatal("opted-in wecom notification was not queued")
	}
	env.exec(`UPDATE openfox.im_notification_outbox q
		SET status='SENDING', lease_owner='stale', lease_expires_at=clock_timestamp()-interval '1 second'
		FROM openfox.notifications n
		WHERE q.organization_id=n.organization_id AND q.notification_id=n.id
		  AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, queuedKey)
	notifyWorker := &integrations.FeishuNotifyWorker{Pool: env.database, Service: integrations.Service{}, Instance: s3NewID(t)}
	if err := notifyWorker.RunOrganization(env.ctx, env.org); err != nil {
		t.Fatal(err)
	}
	var status string
	env.scalar(&status, `SELECT q.status FROM openfox.im_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, queuedKey)
	if status != "FAILED" {
		t.Fatalf("expired WeCom send = %s, want FAILED without replay", status)
	}
	replyID := s3NewID(t)
	env.exec(`INSERT INTO openfox.im_reply_outbox
		(organization_id,id,bot_id,chat_id,body,status,lease_owner,lease_expires_at)
		VALUES ($1::uuid,$2::uuid,$3::uuid,'wecom-member','Reply','SENDING','stale',clock_timestamp()-interval '1 second')`,
		env.org, replyID, botID)
	replyWorker := &integrations.IMWorker{Pool: env.database, Service: integrations.Service{}, Instance: s3NewID(t)}
	if err := replyWorker.RunOrganization(env.ctx, env.org); err != nil {
		t.Fatal(err)
	}
	env.scalar(&status, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND id=$2::uuid`, env.org, replyID)
	if status != "FAILED" {
		t.Fatalf("expired WeCom reply = %s, want FAILED without replay", status)
	}
	if queueCount(insert("DIRECT_MESSAGE")) != 0 {
		t.Fatal("direct messages must remain off until separately enabled")
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/wecom",
		map[string]any{"enabled": true, "disabled_events": []string{}, "direct_messages": true}, nil)
	if queueCount(insert("DIRECT_MESSAGE")) != 1 {
		t.Fatal("opted-in direct message was not queued")
	}
	client.must(http.StatusNoContent, http.MethodDelete, path+"/"+botID, nil, nil)
	if queueCount(insert("MENTION")) != 0 {
		t.Fatal("unbound personal account must not receive notifications")
	}
}
