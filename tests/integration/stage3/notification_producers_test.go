package stage3

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"testing"
	"time"
)

// Business APIs and existing triggers produce these leaves; this does not insert
// notifications or supply inbox_event by hand.
func TestNotificationMemberProducers(t *testing.T) {
	e := newS3Env(t)
	sp := e.createSpace("shared")
	owner := e.owner()
	owner.must(200, "PATCH", spacePath(sp.ID), map[string]any{"version": sp.Version, "multi_assignee": true}, &sp)
	task := e.createTask(owner, sp.ID, map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": s3MemberID}}})
	path := spacePath(sp.ID) + "/tasks/" + task.ID
	owner.must(204, "PUT", path+"/followers/human/"+s3OtherID, nil, nil)
	change := func(body map[string]any) { body["version"] = task.Version; owner.must(200, "PATCH", path, body, &task) }
	change(map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": s3MemberID}, {"kind": "HUMAN", "id": s3ManagerID}}})
	change(map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": s3MemberID}}})
	task = e.setStatus(owner, sp.ID, task, "IN_PROGRESS")
	change(map[string]any{"priority": "HIGH", "start_date": "2026-11-15", "due_date": "2026-11-30"})
	task = e.setStatus(owner, sp.ID, task, "DONE")
	comment := func(c *s3Client, body map[string]any) string {
		body["client_comment_id"] = s3NewID(t)
		var out struct {
			ID string `json:"id"`
		}
		c.must(201, "POST", path+"/comments", body, &out)
		return out.ID
	}
	root := comment(e.manager(), map[string]any{"body": "Root by a different author"})
	comment(owner, map[string]any{"body": "Member reply", "reply_to_id": root})
	owner.must(204, "PUT", path+"/reactions", map[string]any{"comment_id": root, "emoji": "👍"}, nil)
	comment(owner, map[string]any{"body": "Member mention", "mentions": []map[string]string{{"user_id": s3OtherID}}})
	// A shared conversation plus an actual member message creates chat mention.
	agent := e.insertAgent(sp.ID)
	var conv struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	owner.must(201, "POST", "conversations", map[string]any{"space_id": sp.ID, "agent_id": agent, "visibility": "SHARED"}, &conv)
	owner.must(204, "PUT", "conversations/"+conv.ID+"/members/"+s3MemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	owner.must(201, "POST", "conversations/"+conv.ID+"/messages", map[string]any{"body": "Member chat mention", "client_message_id": s3NewID(t), "mentions": []map[string]string{{"user_id": s3MemberID}}}, nil)
	chat := dmChat(t, owner, s3MemberID)
	dmSend(t, owner, chat.ID, 900, "Private test body")
	wanted := []string{"task_assigned_assignee", "task_unassigned_assignee", "task_assignee_updated_assignee", "task_assignee_updated_follower", "task_subscribed_follower", "comment_created_member_assignee", "comment_replied_member_assignee", "comment_created_member_follower", "comment_replied_member_follower", "comment_replied_member_author", "reaction_created_member_author", "comment_mentioned_member_recipient", "chat_mentioned_member_recipient", "chat_message_received_recipient"}
	for _, base := range []string{"task_status_updated", "task_priority_updated", "task_start_date_updated", "task_due_date_updated", "task_completed"} {
		for _, role := range []string{"assignee", "follower"} {
			wanted = append(wanted, base+"_"+role)
		}
	}
	for _, leaf := range wanted {
		t.Run(leaf, func(t *testing.T) {
			if n := e.count(`SELECT count(*) FROM openfox.notifications WHERE organization_id=$1::uuid AND inbox_event=$2`, e.org, leaf); n == 0 {
				t.Fatalf("business producer missing %s", leaf)
			}
			assertProducedLeafChannelPolicy(e, leaf)
		})
	}
	t.Logf("verified %d member-produced leaves", len(wanted))
}

func TestNotificationAutomationProducers(t *testing.T) {
	e := newS3Env(t)
	ensureDirectory(e)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	body := automationBody(agent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	body["notify_user_ids"] = []string{s3MemberID}
	var saved automationSave
	e.manager().must(201, "POST", automationsPath(e.space.ID), body, &saved)
	path := automationsPath(e.space.ID) + "/" + saved.Automation.ID + "/run"
	for _, state := range []string{"SUCCEEDED", "FAILED", "CANCELED"} {
		var run runView
		e.manager().must(201, "POST", path, nil, &run)
		if state == "SUCCEEDED" {
			var skipped runView
			e.manager().must(201, "POST", path, nil, &skipped)
			if skipped.Status != "skipped" {
				t.Fatal("expected overlapping attempt to skip")
			}
		}
		e.materialize()
		if err := e.inService(time.Now(), func(ctx context.Context, _ collaboration.Service, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE openfox.runs SET state=$2,finished_at=clock_timestamp(),cancel_requested_at=CASE WHEN $2='CANCELED' THEN clock_timestamp() ELSE NULL END WHERE id=$1::uuid`, e.runOf(run.ID), state)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range []string{"completed", "failed", "cancelled", "skipped"} {
		leaf := "automation_run_" + result + "_recipient"
		if n := e.count(`SELECT count(*) FROM openfox.notifications WHERE organization_id=$1::uuid AND recipient_user_id=$2::uuid AND inbox_event=$3`, e.org, s3MemberID, leaf); n != 1 {
			t.Fatalf("%s notifications=%d", leaf, n)
		}
	}

	for _, result := range []string{"completed", "failed", "cancelled", "skipped"} {
		assertProducedLeafChannelPolicy(e, "automation_run_"+result+"_recipient")
	}
}

// On each actual producer result, set opposite browser/desktop choices through
// authenticated HTTP, then evaluate the shared delivery predicate on that row.
// Transport sinks and banner aggregation have separate end-to-end tests.
func assertProducedLeafChannelPolicy(e *s3Env, leaf string) {
	e.t.Helper()
	var recipient, notice string
	if err := e.superuser.QueryRow(e.ctx, `SELECT recipient_user_id::text,id::text FROM openfox.notifications WHERE organization_id=$1::uuid AND inbox_event=$2 ORDER BY created_at LIMIT 1`, e.org, leaf).Scan(&recipient, &notice); err != nil {
		e.t.Fatal(err)
	}
	c := e.client(recipient)
	var list struct {
		Channels []notificationPolicyView `json:"channels"`
	}
	c.must(200, "GET", "/v1/me/notification-channels", nil, &list)
	policies := map[string]notificationPolicyView{}
	for _, p := range list.Channels {
		policies[p.Channel] = p
	}
	for _, ch := range []string{"browser", "desktop"} {
		name := ch
		e.t.Cleanup(func() {
			e.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel=$2`, recipient, name)
		})
	}
	for _, enabled := range []bool{false, true} {
		for _, ch := range []string{"browser", "desktop"} {
			value := enabled
			if ch == "desktop" {
				value = !value
			}
			p := policies[ch]
			c.must(200, "PATCH", "/v1/me/notification-channels/"+ch, map[string]any{"revision": p.Revision, "enabled": true, "direct_messages": true, "muted_groups": []string{}, "event_overrides": map[string]bool{leaf: value}}, &p)
			policies[ch] = p
		}
		var browser, desktop bool
		if err := e.superuser.QueryRow(e.ctx, `SELECT openfox_private.notification_channel_allows(recipient_user_id,'browser',kind,inbox_event,inbox_level),openfox_private.notification_channel_allows(recipient_user_id,'desktop',kind,inbox_event,inbox_level) FROM openfox.notifications WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, notice).Scan(&browser, &desktop); err != nil {
			e.t.Fatal(err)
		}
		if browser != enabled || desktop == enabled {
			e.t.Fatalf("%s browser=%v desktop=%v wanted browser=%v", leaf, browser, desktop, enabled)
		}
	}
}
