package stage1

import (
	"testing"
	"time"
)

func TestTasksCommentDeleteAndDerive(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	task := e.newTask(e.owner, sp, nil)
	path := taskPath(sp, task.ID)
	comments := path + "/comments"
	key := func() []string { return []string{"Idempotency-Key", cfgNewID(t)} }
	create := func(parent string) string {
		t.Helper()
		var c struct{ ID string }
		e.member.must(201, "POST", comments, map[string]any{"body": "note", "reply_to_id": parent, "client_comment_id": cfgSuffix(t)}, &c)
		return c.ID
	}
	root := create("")
	child := create(root)
	nested := create(child)
	sibling := create(root)
	e.owner.must(204, "PUT", comments+"/"+nested+"/resolution", map[string]bool{"resolved": true}, nil, key()...)
	source := map[string]string{"space_id": sp.ID, "task_id": task.ID, "id": child}
	for _, sub := range []bool{false, true} {
		in := map[string]any{"title": "derived", "type_id": sp.TypeID, "source_comment": source}
		if sub {
			in["parent_task_id"] = task.ID
		}
		var made tkTask
		k := key()
		e.owner.must(201, "POST", tasksPath(sp), in, &made, k...)
		if (made.ParentTaskID != nil) != sub {
			t.Fatal("wrong parent")
		}
		var n int
		e.scalar(&n, "SELECT count(*) FROM openfox.task_activities WHERE task_id=$1 AND safe_diff->>'source_comment_id'=$2", made.ID, child)
		if n != 1 {
			t.Fatal("missing source")
		}
		e.scalar(&n, "SELECT count(*) FROM openfox.task_relations WHERE source_task_id=$1 AND target_task_id=$2", task.ID, made.ID)
		if (n == 1) == sub {
			t.Fatal("wrong relation")
		}
	}
	other := e.addMember(tkUserA, "MEMBER")
	other.expectError(403, "ACTION_FORBIDDEN", "DELETE", comments+"/"+child, nil, key()...)
	k := key()
	e.owner.must(204, "DELETE", comments+"/"+child, nil, nil, k...)
	e.owner.must(204, "DELETE", comments+"/"+child, nil, nil, k...)
	var page struct{ Comments []struct{ ID string } }
	e.owner.must(200, "GET", comments, nil, &page)
	if len(page.Comments) != 2 {
		t.Fatalf("visible comments %+v", page)
	}
	e.member.expectError(404, "NOT_FOUND", "POST", comments, map[string]any{"body": "late", "reply_to_id": nested, "client_comment_id": cfgSuffix(t)})
	e.owner.expectError(404, "NOT_FOUND", "POST", tasksPath(sp), map[string]any{"title": "late", "type_id": sp.TypeID, "source_comment": source}, key()...)
	create(sibling)
	var reopened bool
	e.scalar(&reopened, "SELECT resolved_at IS NULL FROM openfox.task_comments WHERE id=$1", root)
	if !reopened {
		t.Fatal("resolution not cleared")
	}
	e.member.must(204, "DELETE", comments+"/"+root, nil, nil, key()...)
	e.owner.must(200, "GET", comments, nil, &page)
	if len(page.Comments) != 0 {
		t.Fatal("root not hidden")
	}
}

func TestTasksConversationOfferConsent(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("private")
	writer := e.addMember(tkUserA, "MEMBER")
	reader := e.addMember(tkUserB, "MEMBER")
	e.addSpaceMember(sp.ID, tkUserA, "MEMBER", "ACTIVE")
	e.addSpaceMember(sp.ID, tkUserB, "VIEWER", "ACTIVE")
	e.addSpaceMember(sp.ID, cfgMemberID, "MEMBER", "ACTIVE")
	task := e.newTask(e.owner, sp, map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": cfgOwnerID}, {"kind": "HUMAN", "id": tkUserA}}})
	agent := e.insertAgent(sp.ID, cfgOwnerID, "SHARED")
	var c struct{ ID string }
	e.owner.must(201, "POST", "conversations", map[string]string{"space_id": sp.ID, "task_id": task.ID, "agent_id": agent, "visibility": "PRIVATE"}, &c)
	base := "conversations/" + c.ID
	key := func() []string { return []string{"Idempotency-Key", cfgNewID(t)} }
	offer := func(sender *cfgClient, kind, target string, also bool, want int) {
		t.Helper()
		sender.must(want, "POST", base+"/offers", map[string]any{"kind": kind, "recipient_user_id": target, "transfer_assignee": also}, nil, key()...)
	}
	pending := func(user *cfgClient) string {
		t.Helper()
		var rows []struct{ ID string }
		user.must(200, "GET", "conversation-offers", nil, &rows)
		if len(rows) != 1 {
			t.Fatalf("pending=%+v", rows)
		}
		return "conversation-offers/" + rows[0].ID
	}
	decide := func(user *cfgClient, path string, accept bool, status int) {
		t.Helper()
		user.must(status, "PUT", path, map[string]bool{"accept": accept}, nil)
	}
	legacyGrant := func(target, level string) {
		t.Helper()
		e.owner.expectError(409, "CONVERSATION_INVITE_REQUIRED", "PUT", base+"/members/"+target, map[string]string{"access_level": level})
	}
	var version, before int
	e.scalar(&before, "SELECT version FROM openfox.conversations WHERE id=$1", c.ID)
	legacyGrant(cfgMemberID, "VIEWER")
	legacyGrant(cfgMemberID, "COMMENTER")
	e.scalar(&version, "SELECT version FROM openfox.conversations WHERE id=$1 AND visibility='PRIVATE'", c.ID)
	var n int
	e.scalar(&n, "SELECT count(*) FROM openfox.conversation_offers WHERE conversation_id=$1", c.ID)
	if before != version || n != 0 {
		t.Fatal("rejected legacy grant changed the conversation or sent an invitation")
	}
	offer(writer, "INVITE", cfgMemberID, false, 404)
	offer(e.owner, "TRANSFER", tkUserB, false, 403)
	offer(e.owner, "INVITE", cfgMemberID, false, 204)
	offer(e.owner, "INVITE", cfgMemberID, false, 204)
	var cards int
	e.scalar(&cards, `SELECT count(*) FROM openfox.direct_messages m JOIN openfox.conversation_offers o ON o.id=m.id AND o.organization_id=m.organization_id WHERE o.conversation_id=$1`, c.ID)
	if cards != 1 {
		t.Fatalf("duplicate cards: %d", cards)
	}
	p := pending(e.member)
	legacyGrant(cfgMemberID, "COMMENTER")
	decide(writer, p, true, 404)
	e.member.expectError(404, "", "GET", base, nil)
	decide(e.member, p, false, 204)
	legacyGrant(cfgMemberID, "COMMENTER")
	e.scalar(&n, "SELECT count(*) FROM openfox.conversation_members WHERE conversation_id=$1 AND user_id=$2", c.ID, cfgMemberID)
	if n != 0 {
		t.Fatal("legacy grant created a member before acceptance")
	}
	e.member.expectError(404, "", "GET", base, nil)
	offer(e.owner, "INVITE", cfgMemberID, false, 204)
	p = pending(e.member)
	decide(e.member, p, true, 204)
	decide(e.member, p, true, 204)
	decide(e.member, p, false, 409)
	e.member.must(200, "GET", base, nil, nil)
	for _, level := range []string{"VIEWER", "COMMENTER"} {
		e.owner.must(204, "PUT", base+"/members/"+cfgMemberID, map[string]string{"access_level": level}, nil)
		var got string
		e.scalar(&got, "SELECT access_level FROM openfox.conversation_members WHERE conversation_id=$1 AND user_id=$2", c.ID, cfgMemberID)
		if got != level {
			t.Fatalf("accepted member level = %s, want %s", got, level)
		}
	}
	offer(e.member, "TRANSFER", tkUserA, false, 403)
	offer(e.member, "INVITE", tkUserB, false, 204)
	p = pending(reader)
	decide(reader, p, true, 204)
	offer(reader, "INVITE", tkUserA, false, 403)
	e.owner.must(204, "DELETE", base+"/members/"+tkUserB, nil, nil)
	legacyGrant(tkUserB, "VIEWER")
	reader.expectError(404, "NOT_FOUND", "GET", base, nil)
	offer(e.owner, "TRANSFER", tkUserA, true, 204)
	p = pending(writer)
	var owner string
	e.scalar(&owner, "SELECT owner_user_id::text FROM openfox.conversations WHERE id=$1", c.ID)
	if owner != cfgOwnerID {
		t.Fatal("transfer before consent")
	}
	e.addSpaceMember(sp.ID, tkUserA, "MEMBER", "REVOKED")
	decide(writer, p, true, 404)
	e.addSpaceMember(sp.ID, tkUserA, "MEMBER", "ACTIVE")
	decide(writer, p, true, 204)
	e.scalar(&owner, "SELECT owner_user_id::text FROM openfox.conversations WHERE id=$1", c.ID)
	if owner != tkUserA {
		t.Fatal("owner not transferred")
	}
	e.owner.must(200, "GET", base, nil, nil)
	e.owner.expectError(403, "ACTION_FORBIDDEN", "PUT", base+"/members/"+cfgMemberID, map[string]string{"access_level": "VIEWER"})
	after := e.getTask(writer, sp, task.ID)
	if len(after.Assignees) != 1 || after.Assignees[0].ID != tkUserA {
		t.Fatalf("assignees %+v", after.Assignees)
	}
	// Two recipients accept competing transfers from the current owner at once.
	offer(writer, "TRANSFER", cfgOwnerID, false, 204)
	offer(writer, "TRANSFER", cfgMemberID, false, 204)
	first, second := pending(e.owner), pending(e.member)
	results := make(chan int, 2)
	go func() { results <- e.owner.do("PUT", first, map[string]bool{"accept": true}).Status }()
	go func() { results <- e.member.do("PUT", second, map[string]bool{"accept": true}).Status }()
	a, b := <-results, <-results
	if !(a == 204 && b == 403 || a == 403 && b == 204) {
		t.Fatalf("competing decisions: %d %d", a, b)
	}

}

func TestTasksLegacyMemberGrantWaitsForBinding(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	task := e.newTask(e.owner, sp, nil)
	var c struct{ ID string }
	e.owner.must(201, "POST", "conversations", map[string]string{"space_id": sp.ID, "agent_id": e.insertAgent(sp.ID, cfgOwnerID, "SHARED")}, &c)
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, "UPDATE openfox.conversations SET task_id=$1 WHERE id=$2", task.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan cfgResponse, 1)
	go func() {
		done <- e.owner.do("PUT", "conversations/"+c.ID+"/members/"+cfgMemberID, map[string]string{"access_level": "COMMENTER"})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := tx.QueryRow(e.ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pg_backend_pid()=ANY(pg_blocking_pids(pid)))").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("grant completed before binding committed: %d", r.Status)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("legacy grant did not wait for binding")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Status != 409 || r.code() != "CONVERSATION_INVITE_REQUIRED" {
		t.Fatalf("legacy grant after binding: %d %s", r.Status, r.Body)
	}
	var n int
	e.scalar(&n, "SELECT count(*) FROM openfox.conversation_members WHERE conversation_id=$1 AND user_id=$2", c.ID, cfgMemberID)
	if n != 0 {
		t.Fatal("legacy grant raced binding")
	}
}
