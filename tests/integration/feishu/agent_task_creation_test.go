package feishuint

import (
	"bytes"

	"encoding/json"
	"errors"
	"io"
	"net/http"

	"testing"

	"openfox/internal/conversations"
	"openfox/internal/devices"
	"openfox/internal/dispatch"

	"openfox/internal/tenancy"
)

// Exercise the HTTP intake and background worker through to the requester's inbox.
func TestAgentTaskCreation(t *testing.T) {
	b := setupBotsFor(t, "feishu", false)
	agent := b.createAgent(b.owner, b.foxSpace, "Writer", "PRIVATE")
	post := func(c *client, body map[string]string, key string, want int) string {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest("POST", c.base+b.path("/spaces/"+b.foxSpace+"/tasks/agent-requests"), bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", c.csrf)
		req.Header.Set("Idempotency-Key", key)
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err = io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("intake %d: %s, want %d", resp.StatusCode, raw, want)
		}
		if want >= 400 {
			return ""
		}
		var result struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.RequestID == "" {
			t.Fatalf("receipt %s", raw)
		}
		return result.RequestID
	}
	store, err := dispatch.NewStore(dispatch.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"agent_id": agent, "brief": "Fix the login spinner on slow networks", "priority": "HIGH", "due_date": "2027-02-05"}
	post(b.member, body, b.newUUID(), 404)
	key := b.newUUID()
	conv := post(b.owner, body, key, 201)
	var listed struct {
		Conversations []conversations.Summary `json:"conversations"`
	}
	listWriterChats := func() []conversations.Summary {
		t.Helper()
		b.owner.json(200, "GET", b.path("/conversations?agent_id="+agent+"&archived=include"), nil, &listed)
		return listed.Conversations
	}
	if chats := listWriterChats(); len(chats) != 0 {
		t.Fatalf("queued drafting request appeared in chats: %v", chats[0].ID)
	}
	if post(b.owner, body, key, 200) != conv {
		t.Fatal("submission replay changed request")
	}
	body["brief"] = "Keep the original failure steps"
	post(b.owner, body, key, 409)
	failedConv := post(b.owner, body, b.newUUID(), 201)
	lead := b.createAgent(b.owner, b.foxSpace, "Team lead", "SHARED")
	var crew struct{ ID string }
	b.owner.json(201, "POST", b.path("/spaces/"+b.foxSpace+"/crews"), map[string]any{"name": "Drafting team", "leader_agent_id": lead, "agent_ids": []string{lead}}, &crew)
	crewConv := post(b.owner, map[string]string{"brief": "Document retry behavior", "crew_id": crew.ID}, b.newUUID(), 201)
	b.exec(`UPDATE openfox.users SET full_name='Test Reviewer' WHERE id=$1`, b.adminID)
	humanConv := post(b.owner, map[string]string{"brief": "Ask Test Reviewer to check the fix", "agent_id": agent}, b.newUUID(), 201)
	deniedAgent := b.createAgent(b.owner, b.foxSpace, "Unavailable writer", "SHARED")
	deniedConv := post(b.owner, map[string]string{"brief": "Queued before agent archive", "agent_id": deniedAgent}, b.newUUID(), 201)
	b.exec(`UPDATE openfox.agents SET archived_at=now() WHERE organization_id=$1 AND id=$2`, b.org, deniedAgent)
	countChats := func() int {
		var n int
		b.scan(&n, `SELECT count(*) FROM openfox.conversations WHERE organization_id=$1 AND space_id=$2 AND task_id IS NULL`, b.org, b.foxSpace)
		return n
	}
	if countChats() != 0 {
		t.Fatal("submission created an ordinary conversation")
	}
	cmds := devices.PostgresStore{Pool: b.pool}
	sel := tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.deviceID, AuthorizationVersion: 1}
	if _, err := cmds.ClaimCommand(b.ctx, sel, deniedConv); !errors.Is(err, devices.ErrCommandConflict) {
		t.Fatalf("archived agent claim %v", err)
	}
	for _, id := range []string{conv, failedConv, crewConv, humanConv} {
		c, err := cmds.ClaimCommand(b.ctx, sel, id)
		if err != nil {
			t.Fatal(err)
		}
		var rc dispatch.RunContext
		if err := json.Unmarshal(c.Params, &rc); err != nil || rc.Runtime == "" || rc.Prompt == "" || rc.ConversationID != "" {
			t.Fatal("invalid draft context", err)
		}
		if countChats() != 0 {
			t.Fatal("claim created a conversation")
		}
		in := devices.CommandResultInput{Status: "SUCCEEDED", Result: json.RawMessage(`{"title":"Fix slow login","description":"Stop the spinner after login completes."}`)}
		switch id {
		case failedConv:
			in = devices.CommandResultInput{Status: "FAILED", ErrorCode: "COMMAND_FAILED"}
		case crewConv:
			in.Result = json.RawMessage(`{"title":"Team task","description":"Review the draft."}`)
		case humanConv:
			in.Result = json.RawMessage(`{"title":"Named reviewer","description":"Review the draft.","assignee_name":"Test Reviewer"}`)
		}
		for range 2 {
			if _, err := cmds.CompleteCommand(b.ctx, sel, id, in); err != nil {
				t.Fatal(err)
			}
		}
	}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1,10) ON CONFLICT DO NOTHING`, b.org)
	dw := &dispatch.Worker{Pool: b.pool, Store: store}
	for range 2 {
		if _, err := dw.RunOnce(b.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if countChats() != 0 {
		t.Fatal("completion or failure created an ordinary conversation")
	}
	var n int
	b.scan(&n, `SELECT count(*) FROM openfox.tasks WHERE organization_id=$1 AND title='Fix slow login' AND executor_agent_id=$2 AND priority='HIGH' AND due_date='2027-02-05'`, b.org, agent)
	if n != 1 {
		t.Fatalf("created %d matching tasks", n)
	}
	b.scan(&n, `SELECT count(*) FROM openfox.tasks t JOIN openfox.task_assignees a ON a.organization_id=t.organization_id AND a.task_id=t.id WHERE t.organization_id=$1 AND t.title='Team task' AND a.crew_id=$2`, b.org, crew.ID)
	if n != 1 {
		t.Fatal("missing crew assignment")
	}
	b.scan(&n, `SELECT count(*) FROM openfox.tasks WHERE organization_id=$1 AND title='Named reviewer' AND assignee_id=$2`, b.org, b.adminID)
	if n != 1 {
		t.Fatal("missing named assignee")
	}
	var inbox conversations.InboxPage
	b.owner.json(200, "GET", b.path("/inbox"), nil, &inbox)
	success, recovered, rejected := false, false, false
	for _, group := range inbox.Groups {
		if group.ConversationID == "" && group.SpaceID == b.foxSpace && group.TaskCreationDraft != nil && group.TaskCreationDraft.AgentID == deniedAgent {
			rejected = true
		}
		if group.Kind == "RUN_COMPLETED" && group.TaskTitle == "Fix slow login" && group.ConversationID == "" {
			success = true
		}
		if g := group.TaskCreationDraft; group.Kind == "RUN_FAILED" && g != nil && g.Brief == body["brief"] && g.Priority == "HIGH" && g.AgentID == agent {
			recovered = true
		}
	}
	if !success || !recovered || !rejected {
		t.Fatalf("inbox %+v", inbox.Groups)
	}

	// Keep the assigned task's execution chat and a real direct chat with the
	// same agent. A failed draft and a draft assigned to a human stay internal.
	var direct conversations.Conversation
	b.owner.json(201, "POST", b.path("/conversations"), map[string]string{"space_id": b.foxSpace, "agent_id": agent, "visibility": "PRIVATE"}, &direct)
	chats := listWriterChats()
	if len(chats) != 2 {
		t.Fatalf("expected one direct and one task conversation, got %d", len(chats))
	}
	var taskChat string
	for _, item := range chats {
		if item.Task != nil && item.Task.Title == "Fix slow login" {
			taskChat = item.ID
		} else if item.ID != direct.ID {
			t.Fatalf("unexpected ordinary chat %s", item.ID)
		}
	}
	if taskChat == "" || taskChat == conv {
		t.Fatal("missing assigned task conversation")
	}
	// The same search term occurs in the request and in a normal message.
	b.owner.json(201, "POST", b.path("/conversations/"+direct.ID+"/messages"), map[string]string{"body": "networks", "client_message_id": b.newUUID()}, nil)
	for _, example := range []struct {
		kind, term string
		want       int
	}{
		{"message", "networks", 1}, {"conversation", "Writer", 2},
	} {
		var results struct {
			Groups []struct {
				Total int
				Items []struct {
					ConversationID string `json:"conversation_id"`
				}
			}
		}
		b.owner.json(200, "GET", b.path("/search?types="+example.kind+"&q="+example.term+"&offset=0"), nil, &results)
		if len(results.Groups) != 1 || results.Groups[0].Total != example.want || len(results.Groups[0].Items) != example.want {
			t.Fatalf("unexpected %s search results: %+v", example.kind, results)
		}
		for _, item := range results.Groups[0].Items {
			if item.ConversationID != direct.ID && item.ConversationID != taskChat {
				t.Fatalf("%s search exposed draft %s", example.kind, item.ConversationID)
			}
		}
	}
}
