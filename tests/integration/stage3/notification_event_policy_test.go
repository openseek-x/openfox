package stage3

import (
	"fmt"
	"net/url"
	"sync"
	"testing"

	"openfox/internal/conversations"
)

type notificationPolicyView struct {
	Channel   string          `json:"channel"`
	Enabled   bool            `json:"enabled"`
	Direct    bool            `json:"direct_messages"`
	Overrides map[string]bool `json:"event_overrides"`
	Muted     []string        `json:"muted_groups"`
	Revision  int64           `json:"revision"`
}

func resetNotificationPolicies(e *s3Env, uid string) {
	e.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid`, uid)
	e.t.Cleanup(func() { e.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid`, uid) })
}
func TestNotificationPatchCompatibilityAndRevision(t *testing.T) {
	e := newS3Env(t)
	uid, email := newTokenUser(t, e)
	c := e.loginClient(email)
	path := "/v1/me/notification-channels/browser"
	var item notificationPolicyView
	patch := func(body any) { c.must(200, "PATCH", path, body, &item) }
	patch(map[string]any{"revision": 0, "event_overrides": map[string]bool{"task_assigned_assignee": false}, "muted_groups": []string{"comments"}, "direct_messages": false})
	if item.Revision < 1 || item.Direct || item.Overrides["task_assigned_assignee"] || len(item.Muted) != 1 {
		t.Fatalf("patch=%+v", item)
	}
	oldRevision := item.Revision
	c.must(200, "PUT", path, map[string]any{"enabled": false, "disabled_events": []string{"status"}}, &item)
	if item.Enabled || item.Direct || item.Revision <= oldRevision || len(item.Overrides) != 1 || len(item.Muted) != 1 {
		t.Fatalf("legacy PUT lost policy: %+v", item)
	}
	c.must(409, "PATCH", path, map[string]any{"revision": oldRevision, "enabled": true}, nil)
	for _, invalid := range []any{
		map[string]any{"event_overrides": map[string]any{"task_assigned_assignee": nil}},
		map[string]any{"event_overrides": map[string]bool{"unknown_leaf": true}},
		map[string]any{"event_overrides": map[string]bool{"assignments": true}},
		map[string]any{"muted_groups": []string{"task_assigned_assignee"}},
		map[string]any{"muted_groups": []string{"comments", "comments"}},
	} {
		body := invalid.(map[string]any)
		body["revision"] = item.Revision
		c.must(400, "PATCH", path, body, nil)
	}
	var statuses [2]int
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = c.do("PATCH", path, map[string]any{"revision": item.Revision, "enabled": true}).Status
		}(i)
	}
	wg.Wait()
	if !((statuses[0] == 200 && statuses[1] == 409) || (statuses[0] == 409 && statuses[1] == 200)) {
		t.Fatalf("concurrent statuses=%v", statuses)
	}
	// Reload the revision, then prove DELETE/recreation cannot reuse it (ABA).
	var list struct {
		Channels []notificationPolicyView `json:"channels"`
	}
	c.must(200, "GET", "/v1/me/notification-channels", nil, &list)
	for _, v := range list.Channels {
		if v.Channel == "browser" {
			item = v
		}
	}
	// Legacy PUT and a fine-grained PATCH may race. Coarse replacement must
	// never erase fine values; the PATCH either commits or reports a conflict.
	var legacyResponse, patchResponse s3Response
	observed := item.Revision
	wg.Add(2)
	go func() {
		defer wg.Done()
		legacyResponse = c.do("PUT", path, map[string]any{"enabled": true, "disabled_events": []string{"status"}})
	}()
	go func() {
		defer wg.Done()
		patchResponse = c.do("PATCH", path, map[string]any{"revision": observed, "event_overrides": map[string]bool{"task_priority_updated_assignee": false}})
	}()
	wg.Wait()
	if legacyResponse.Status != 200 || (patchResponse.Status != 200 && patchResponse.Status != 409) {
		t.Fatalf("legacy/PATCH race=%d/%d", legacyResponse.Status, patchResponse.Status)
	}
	c.must(200, "GET", "/v1/me/notification-channels", nil, &list)
	for _, v := range list.Channels {
		if v.Channel == "browser" {
			item = v
		}
	}
	if item.Overrides["task_assigned_assignee"] || item.Direct || len(item.Muted) != 1 {
		t.Fatalf("legacy race erased policy=%+v", item)
	}
	_, present := item.Overrides["task_priority_updated_assignee"]
	if present != (patchResponse.Status == 200) {
		t.Fatal("conflicting PATCH partly wrote data")
	}
	stale := item.Revision
	c.must(409, "DELETE", fmt.Sprintf("%s?revision=%d", path, oldRevision), nil, nil)
	c.must(200, "DELETE", fmt.Sprintf("%s?revision=%d", path, stale), nil, &item)
	if item.Revision != 0 {
		t.Fatal("reset must expose virtual revision 0")
	}
	patch(map[string]any{"revision": 0, "event_overrides": map[string]bool{"task_assigned_assignee": false}})
	if item.Revision <= stale {
		t.Fatalf("revision reused: old=%d new=%d", stale, item.Revision)
	}
	c.must(409, "PATCH", path, map[string]any{"revision": stale, "enabled": false}, nil)
	c.must(403, "PATCH", path, map[string]any{"revision": item.Revision, "enabled": false}, nil, "X-CSRF-Token", "invalid")
	if n := e.count(`SELECT count(*) FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND event_overrides->'task_assigned_assignee'='false'::jsonb`, uid); n != 1 {
		t.Fatal("invalid/stale writes changed stored choice")
	}
}
func TestNotificationBannerPoliciesBeforeGrouping(t *testing.T) {
	e := newS3Env(t)
	resetNotificationPolicies(e, s3MemberID)
	c := e.client(s3MemberID)
	task := e.createTask(e.client(s3OwnerID), e.space.ID, nil)
	var policy notificationPolicyView
	path := "/v1/me/notification-channels/browser"
	c.must(200, "PATCH", path, map[string]any{"revision": 0, "event_overrides": map[string]bool{"task_status_updated_assignee": true, "task_priority_updated_assignee": false}}, &policy)
	insert := func(kind, leaf, key string) {
		e.exec(`INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,inbox_event,dedupe_key,safe_summary) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$6)`, e.org, s3MemberID, kind, task.ID, leaf, key)
	}
	insert("TASK_UPDATED", "task_status_updated_assignee", "earlier-on")
	insert("TASK_UPDATED", "task_priority_updated_assignee", "later-off")
	get := func(query string) conversations.InboxPage {
		var page conversations.InboxPage
		c.must(200, "GET", "/v1/organizations/"+e.org+"/inbox?"+query, nil, &page)
		return page
	}
	page := get("delivery=banner&channel=browser")
	if len(page.Groups) != 1 || page.Groups[0].InboxEvent != "task_status_updated_assignee" || page.Groups[0].Count != 1 {
		t.Fatalf("newest disabled leaf masked earlier allowed: %+v", page.Groups)
	}
	if p := get("delivery=banner"); len(p.Groups) != 1 || p.Groups[0].InboxEvent != "task_status_updated_assignee" {
		t.Fatalf("legacy intersection=%+v", p.Groups)
	}
	if p := get("delivery=banner&channel=desktop"); p.Groups[0].InboxEvent != "task_priority_updated_assignee" {
		t.Fatalf("desktop inherited browser choice: %+v", p.Groups)
	}
	c.must(400, "GET", "/v1/organizations/"+e.org+"/inbox?delivery=banner&channel=email", nil, nil)
	e.client(s3OutsiderID).must(404, "GET", "/v1/organizations/"+e.org+"/inbox?delivery=banner&channel=browser", nil, nil)
	// Four formerly discarded leaves stay discarded until an explicit channel opts in.
	for _, event := range []struct{ kind, leaf string }{{"REPLY", "chat_replied_participant"}, {"RUN_FAILED", "task_failed_participant"}, {"HANDOFF", "task_needs_human_input_participant"}, {"HANDOFF_REVIEW", "task_review_requested_participant"}} {
		insert(event.kind, event.leaf, event.leaf+"-silent")
		if n := e.count(`SELECT count(*) FROM openfox.notifications WHERE organization_id=$1::uuid AND dedupe_key=$2`, e.org, event.leaf+"-silent"); n != 0 {
			t.Fatal("legacy suppressed event inserted")
		}
		c.must(200, "PATCH", path, map[string]any{"revision": policy.Revision, "event_overrides": map[string]bool{event.leaf: true}}, &policy)
		insert(event.kind, event.leaf, event.leaf+"-explicit")
		if n := e.count(`SELECT count(*) FROM openfox.notifications WHERE organization_id=$1::uuid AND dedupe_key=$2 AND inbox_level='off'`, e.org, event.leaf+"-explicit"); n != 1 {
			t.Fatal("explicit event not retained with inbox still off")
		}
	}
	page = get("delivery=banner&channel=browser")
	if len(page.Groups) != 1 || page.Groups[0].InboxEvent != "task_review_requested_participant" {
		t.Fatalf("explicit banner=%+v", page.Groups)
	}
	if p := get("delivery=banner"); p.Groups[0].InboxEvent != "task_status_updated_assignee" {
		t.Fatal("old feed started alerting suppressed events")
	}
	if p := get("delivery=inbox"); p.Groups[0].InboxEvent == "task_review_requested_participant" {
		t.Fatal("external opt-in enabled inbox")
	}
	c.must(200, "PATCH", path, map[string]any{"revision": policy.Revision, "muted_groups": []string{"agent_activity"}}, &policy)
	if p := get("delivery=banner&channel=browser"); p.Groups[0].InboxEvent != "task_status_updated_assignee" {
		t.Fatal("group mute ignored")
	}
	if !policy.Overrides["task_review_requested_participant"] {
		t.Fatal("group mute erased child")
	}
}

func TestNotificationBannerPaginationKeepsFilteredCursor(t *testing.T) {
	e := newS3Env(t)
	resetNotificationPolicies(e, s3MemberID)
	c := e.member()
	c.must(200, "PATCH", "/v1/me/notification-channels/browser", map[string]any{"revision": 0, "event_overrides": map[string]bool{"task_status_updated_assignee": true, "task_priority_updated_assignee": false}}, nil)
	for i := 0; i < 3; i++ {
		task := e.createTask(e.owner(), e.space.ID, nil)
		for j, leaf := range []string{"task_status_updated_assignee", "task_priority_updated_assignee"} {
			e.exec(`INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,inbox_event,dedupe_key,safe_summary) VALUES($1::uuid,$2::uuid,'TASK_UPDATED',$3::uuid,$4,$5,'paging')`, e.org, s3MemberID, task.ID, leaf, fmt.Sprintf("page-%d-%d", i, j))
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 3; i++ {
		var page conversations.InboxPage
		c.must(200, "GET", "inbox?delivery=banner&channel=browser&limit=1&cursor="+url.QueryEscape(cursor), nil, &page)
		if len(page.Groups) != 1 || page.Groups[0].InboxEvent != "task_status_updated_assignee" || seen[page.Groups[0].ID] {
			t.Fatalf("page%d=%+v", i, page)
		}
		seen[page.Groups[0].ID] = true
		cursor = page.NextCursor
	}
	if cursor != "" || len(seen) != 3 {
		t.Fatal("filtered pagination did not finish")
	}
}
