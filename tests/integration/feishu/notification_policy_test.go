package feishuint

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/tenancy"
	"strings"
	"sync"
	"testing"
)

type policyChannel struct {
	Channel   string          `json:"channel"`
	Enabled   bool            `json:"enabled"`
	Direct    bool            `json:"direct_messages"`
	Overrides map[string]bool `json:"event_overrides"`
	Muted     []string        `json:"muted_groups"`
	Revision  int64           `json:"revision"`
}

// The three providers reuse the existing worker and real HTTP/PG/OAuth adapters,
// with separate local platform sinks. No request reaches a real platform.
func TestIMIndependentEventPolicies(t *testing.T) {
	for _, provider := range []string{"feishu", "lark", "dingtalk"} {
		t.Run(provider, func(t *testing.T) {
			w := setupFor(t, provider)
			w.bind()
			path := "/v1/me/notification-channels/" + provider
			var ch policyChannel
			patch := func(body map[string]any) {
				body["revision"] = ch.Revision
				w.member.json(200, "PATCH", path, body, &ch)
			}
			// Binding only re-enables its own channel; all other choices and CAS are preserved.
			w.member.json(200, "PATCH", path, map[string]any{"revision": 0, "enabled": false, "event_overrides": map[string]bool{"task_assigned_assignee": false}, "muted_groups": []string{"comments"}}, &ch)
			stale := ch.Revision
			other := "feishu"
			if provider == "feishu" {
				other = "lark"
			}
			w.member.json(200, "PUT", "/v1/me/notification-channels/"+other, map[string]any{"enabled": false, "disabled_events": []string{"assign"}}, nil)
			if provider == "dingtalk" {
				w.mustLinkDT(w.member, "st_member")
			} else {
				w.mustLink(w.member, "ou_member")
			}
			var list struct {
				Channels []policyChannel `json:"channels"`
			}
			w.member.json(200, "GET", "/v1/me/notification-channels", nil, &list)
			for _, c := range list.Channels {
				if c.Channel == provider {
					ch = c
				}
				if c.Channel == other && c.Enabled {
					t.Fatal("binding enabled another provider")
				}
			}
			if !ch.Enabled || ch.Direct || len(ch.Muted) != 1 || ch.Overrides["task_assigned_assignee"] || ch.Revision <= stale {
				t.Fatalf("binding lost policy: %+v", ch)
			}
			w.member.expect(409, "VERSION_CONFLICT", "PATCH", path, map[string]any{"revision": stale, "enabled": false})
			assign := func(want string) {
				t.Helper()
				w.task(w.foxSpace, "Policy "+w.newUUID(), w.memberID)
				w.runNotify()
				if row := w.outbox(w.memberID); row == nil || row.Status != want {
					t.Fatalf("assignment=%+v want %s", row, want)
				}
			}
			assign("SKIPPED")
			// Explicit leaf true overrides legacy aggregate, while the unrelated provider remains off.
			w.member.json(200, "PUT", path, map[string]any{"enabled": true, "disabled_events": []string{"assign"}}, &ch)
			patch(map[string]any{"event_overrides": map[string]bool{"task_assigned_assignee": true}})
			assign("DONE")
			patch(map[string]any{"muted_groups": []string{"assignments"}})
			assign("SKIPPED")
			patch(map[string]any{"muted_groups": []string{}})
			assign("DONE")
			// Queued jobs reread the current leaf choice at claim; no send after closing it.
			w.task(w.foxSpace, "Queued before mute", w.memberID)
			patch(map[string]any{"event_overrides": map[string]bool{"task_assigned_assignee": false}})
			w.runNotify()
			if row := w.outbox(w.memberID); row.Status != "SKIPPED" {
				t.Fatalf("queued mute=%+v", row)
			}
			// Private messages are produced by the actual direct-chat API. Leaf-on is not consent.
			var chat struct {
				ID string `json:"id"`
			}
			w.owner.json(200, "POST", w.path("/direct-chats"), map[string]any{"user_id": w.memberID}, &chat)
			send := func() {
				w.owner.json(200, "POST", w.path("/direct-chats/"+chat.ID+"/messages"), map[string]any{"client_message_id": w.newUUID(), "body": "PRIVATE_BODY_DO_NOT_SEND"}, nil)
			}
			patch(map[string]any{"event_overrides": map[string]bool{"chat_message_received_recipient": true}})
			before := w.outboxCount(w.memberID)
			send()
			if w.outboxCount(w.memberID) != before {
				t.Fatal("leaf bypassed private consent")
			}
			patch(map[string]any{"direct_messages": true})
			send()
			if w.outboxCount(w.memberID) != before+1 {
				t.Fatal("explicit private consent did not enqueue")
			}
			w.runNotify()
			if row := w.outbox(w.memberID); row.Status != "DONE" {
				t.Fatalf("private send=%+v", row)
			}
			if provider == "dingtalk" {
				for _, m := range w.dtFake.Messages(w.appID) {
					if strings.Contains(m.Text, "PRIVATE_BODY") {
						t.Fatal("private body leaked")
					}
				}
			} else {
				for _, m := range w.fake.Messages(w.appID) {
					if strings.Contains(m.Content, "PRIVATE_BODY") {
						t.Fatal("private body leaked")
					}
				}
			}
			// Two workers must not claim the same task concurrently. Existing unique outbox and leases are reused.
			patch(map[string]any{"event_overrides": map[string]bool{"task_assigned_assignee": true}})
			messages := func() int {
				if provider == "dingtalk" {
					return len(w.dtFake.Messages(w.appID))
				}
				return len(w.fake.Messages(w.appID))
			}
			count := messages()
			w.task(w.foxSpace, "Concurrent worker claim", w.memberID)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); w.runNotify() }()
			}
			wg.Wait()
			if messages() != count+1 {
				t.Fatal("duplicate concurrent delivery")
			}
			// Revocation after enqueue wins at send preparation.
			w.task(w.foxSpace, "Queued before unbind", w.memberID)
			personal := w.view(w.member).Personal
			w.member.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+personal.ConnectionID+"/disconnect"), map[string]any{"version": personal.Version}, nil)
			w.runNotify()
			if row := w.outbox(w.memberID); row.Status != "SKIPPED" {
				t.Fatalf("unbound=%s", fmt.Sprint(row))
			}
		})
	}
}

func TestNotificationOutboxPolicyIsolationAndDeletedSource(t *testing.T) {
	w := notifyWorld(t)
	task := w.task(w.foxSpace, "Comments policy", w.memberID)
	w.runNotify()
	path := w.path("/spaces/" + w.foxSpace + "/tasks/" + task.ID + "/comments")
	var comment struct {
		ID string `json:"id"`
	}
	w.owner.json(201, "POST", path, map[string]any{"body": "Before deletion", "client_comment_id": w.newUUID()}, &comment)
	var outbox, notice string
	if err := w.super.QueryRow(w.ctx, `SELECT q.id::text,n.id::text FROM openfox.im_notification_outbox q JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id WHERE n.organization_id=$1::uuid AND n.comment_id=$2::uuid AND n.recipient_user_id=$3::uuid`, w.org, comment.ID, w.memberID).Scan(&outbox, &notice); err != nil {
		t.Fatal(err)
	}
	verify := func(org, id, provider string, want int) {
		t.Helper()
		selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
		err := w.pool.RunForOrganization(w.ctx, org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox_private.notification_outbox_policy($1::uuid,$2)`, id, provider).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Errorf("policy org=%s provider=%s count=%d want%d", org, provider, n, want)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.notifications WHERE id=$1::uuid`, notice).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Error("worker gained direct notification access")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	verify(w.org, outbox, "feishu", 1)
	verify(w.orgB, outbox, "feishu", 0)
	verify(w.org, outbox, "lark", 0)
	verify(w.org, outbox, "email", 0)
	verify(w.org, w.newUUID(), "feishu", 0)
	request, err := http.NewRequest("DELETE", w.owner.base+path+"/"+comment.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-CSRF-Token", w.owner.csrf)
	request.Header.Set("Idempotency-Key", w.newUUID())
	response, err := w.owner.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete comment=%d", response.StatusCode)
	}
	verify(w.org, outbox, "feishu", 0)
	count := len(w.fake.Messages(w.appID))
	w.task(w.foxSpace, "Good item behind deleted source", w.memberID)
	if err := w.notifyWorker().RunOrganization(w.ctx, w.org); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := w.super.QueryRow(w.ctx, `SELECT status,last_error FROM openfox.im_notification_outbox WHERE id=$1::uuid`, outbox).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "SKIPPED" || reason != "source_unavailable" || len(w.fake.Messages(w.appID)) != count+1 {
		t.Fatalf("deleted source blocked batch: %s %s", status, reason)
	}
}
