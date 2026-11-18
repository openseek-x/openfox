package stage2

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/conversations"
	"openfox/internal/dispatch"
)

func referenceBody(task, key string) map[string]any {
	return map[string]any{"type": "TASK", "task_id": task, "client_message_id": key}
}

func sendReference(t *testing.T, client *agClient, conv, task, key string) conversations.Message {
	t.Helper()
	var item conversations.Message
	client.must(http.StatusCreated, http.MethodPost, conversationPath(conv)+"/references", referenceBody(task, key), &item)
	return item
}

func referencePage(t *testing.T, client *agClient, conv string) []conversations.Message {
	t.Helper()
	var page struct {
		Messages []conversations.Message `json:"messages"`
	}
	client.must(http.StatusOK, http.MethodGet, conversationPath(conv)+"/messages?limit=100", nil, &page)
	return page.Messages
}

func TestTaskReferencesDynamicAccessAndConsumers(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, reader := e.client(agOwnerID), e.client(agMemberID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	sourceSpace := e.createSpace("private")
	// Controlled isolated ACL fixture: the recipient can read the target but not source.
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id<>$3::uuid`, e.org, sourceSpace, agOwnerID)
	title := "PRIVATE-T04-中🦊 " + strings.Repeat("long", 90)
	task := f.createTask(sourceSpace, title)
	message := sendReference(t, owner, conv.ID, strings.ToUpper(task), "ref")
	if message.Kind != "REFERENCE" || message.Body != conversations.TaskReferenceBody || message.Reference == nil || !message.Reference.Available || message.Reference.Title != title || message.Reference.TaskID != task || message.RunID != nil || len(message.Attachments) != 0 {
		t.Fatalf("reference=%+v", message)
	}
	var body string
	e.scalar(&body, `SELECT body||payload::text FROM openfox.messages WHERE id=$1::uuid`, message.ID)
	if strings.Contains(body, title) || strings.Contains(body, "display_id") || strings.Contains(body, "space_id") {
		t.Fatal("source copied into storage")
	}
	assertHidden := func() {
		t.Helper()
		items := referencePage(t, reader, conv.ID)
		encoded, _ := json.Marshal(items)
		for _, secret := range []string{task, sourceSpace, title} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("unavailable response leaked source field %q", secret)
			}
		}
		if len(items) != 1 || items[0].Reference == nil || items[0].Reference.Available || items[0].Reference.Title != "" || items[0].Reference.SpaceID != "" || items[0].Reference.DisplayID != "" {
			t.Fatalf("source leaked %+v", items)
		}
	}
	assertHidden()
	reader.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(task, "hidden"))
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(agNewID(t), "missing"))
	// Organization administrator has no implicit access to a private source.
	admin := e.client(agAdminID)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agAdminID, map[string]any{"access_level": "COMMENTER"}, nil)
	admin.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(task, "admin"))
	e.addSpaceMember(sourceSpace, agMemberID, "MEMBER")
	if items := referencePage(t, reader, conv.ID); !items[0].Reference.Available {
		t.Fatal("newly readable reference missing")
	}
	e.exec(`UPDATE openfox.tasks SET title='current-title' WHERE id=$1::uuid`, task)
	if items := referencePage(t, reader, conv.ID); items[0].Reference.Title != "current-title" {
		t.Fatal("stale snapshot title")
	}
	e.exec(`UPDATE openfox.space_members SET status='REVOKED' WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, e.org, sourceSpace, agMemberID)
	assertHidden()
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/messages", map[string]any{"body": "quote", "client_message_id": "quote", "reply_to_id": message.ID})
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "share-snapshots", map[string]any{"kind": "CONVERSATION", "conversation_id": conv.ID, "message_id": message.ID, "scope": "ORGANIZATION", "expiry": "30d"})
	var summary convSummary
	owner.must(http.StatusOK, http.MethodGet, path, nil, &summary)
	// Search indexes only the safe placeholder, not the derived source title.
	response := reader.must(http.StatusOK, http.MethodGet, "search?types=message&q=PRIVATE-T04", nil, nil)
	if strings.Contains(string(response.Body), message.ID) {
		t.Fatal("reference indexed private title")
	}
	var version int
	e.scalar(&version, `SELECT version FROM openfox.tasks WHERE id=$1::uuid`, task)
	owner.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("spaces/%s/tasks/%s?version=%d", sourceSpace, task, version), nil, nil)
	items := referencePage(t, owner, conv.ID)
	if items[0].Reference.Available {
		t.Fatal("deleted task reference remained available")
	}
	retry := sendReference(t, owner, conv.ID, task, "ref")
	if retry.ID != message.ID || retry.Reference.Available {
		t.Fatal("retry copied deleted source or created duplicate")
	}
}

func TestTaskReferencesIdempotencyAndDestinationPermissions(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	task := f.createTask(f.space, "source")
	var wg sync.WaitGroup
	responses := make([]agResponse, 8)
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = owner.do(http.MethodPost, path+"/references", referenceBody(task, "parallel"))
		}(i)
	}
	wg.Wait()
	var id string
	for _, response := range responses {
		if response.Status != http.StatusCreated {
			t.Fatalf("concurrent %d %s", response.Status, response.Body)
		}
		var m conversations.Message
		response.decode(t, &m)
		if id == "" {
			id = m.ID
		}
		if m.ID != id || m.Seq != 1 {
			t.Fatal("duplicate message/sequence")
		}
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1::uuid AND action='conversation.message_created' AND resource_id=$2`, e.org, id)
	if count != 1 {
		t.Fatalf("duplicate audit=%d", count)
	}
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path+"/references", referenceBody(agNewID(t), "parallel"))
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path+"/messages", map[string]any{"body": conversations.TaskReferenceBody, "client_message_id": "parallel"})
	postQuoted(t, owner, conv.ID, "text", "", "old-text")
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path+"/references", referenceBody(task, "old-text"))
	for _, bad := range []map[string]any{referenceBody(task, ""), referenceBody(task, strings.Repeat("k", 257)), {"type": "DOC", "task_id": task, "client_message_id": "doc"}, {"type": "TASK", "task_id": task, "client_message_id": "body", "body": "private"}} {
		owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path+"/references", bad)
	}
	member := e.client(agMemberID)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(task, "member"))
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	m := sendReference(t, member, conv.ID, task, "member")
	if !m.Reference.Available {
		t.Fatal("commenter unable to reference")
	}
	member.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path+"/references", referenceBody(task, "parallel"))
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agMemberID, map[string]any{"access_level": "VIEWER"}, nil)
	member.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, path+"/references", referenceBody(task, "member"))
	owner.must(http.StatusNoContent, http.MethodDelete, path+"/members/"+agMemberID, nil, nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(task, "member"))
	e.exec(`UPDATE openfox.conversations SET lifecycle='COMPLETED' WHERE id=$1::uuid`, conv.ID)
	owner.expectError(http.StatusConflict, "CONVERSATION_CLOSED", http.MethodPost, path+"/references", referenceBody(task, "parallel"))
}

func TestTaskReferencesPassiveRuntimeAndHistory(t *testing.T) { testPassiveReferences(t, false) }
func testPassiveReferences(t *testing.T, document bool) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	space, agent, run := e.devRunFixture(executor, nil)
	var conv string
	e.scalar(&conv, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1::uuid`, run)
	f := convFixture{env: e, space: space, agentID: agent}
	task, body := "", conversations.TaskReferenceBody
	if document {
		task = d4Node(owner, space, "", "", "doc", "Never send this private title to runtime").ID
		body = conversations.DocReferenceBody
	} else {
		task = f.createTask(space, "Never send this private title to runtime")
	}

	// Real outbound triggers have active bindings. Fixture credentials are inert;
	// no worker or network client runs in this test.
	connection, bot := agNewID(t), agNewID(t)
	e.exec(`INSERT INTO openfox.integration_connections (organization_id,id,provider,scope,external_id,display_name,account_kind,created_by)
		VALUES($1::uuid,$2::uuid,'feishu','ORGANIZATION',$2,'Fixture bot','agent',$3::uuid)`, e.org, connection, agOwnerID)
	e.exec(`INSERT INTO openfox.im_bots (organization_id,id,connection_id,kind,agent_id,created_by)
		VALUES($1::uuid,$2::uuid,$3::uuid,'AGENT',$4::uuid,$5::uuid)`, e.org, bot, connection, agent, agOwnerID)
	e.exec(`INSERT INTO openfox.im_chat_bindings (organization_id,bot_id,chat_id,chat_type,conversation_id,created_by)
		VALUES($1::uuid,$2::uuid,'fixture-chat','p2p',$3::uuid,$4::uuid)`, e.org, bot, conv, agOwnerID)
	e.exec(`INSERT INTO openfox.wechat_bindings (organization_id,agent_id,created_by,ilink_bot_id,ilink_user_id,bot_token_sealed,base_url,conversation_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4,'fixture-user','\x00','http://127.0.0.1:1',$5::uuid)`, e.org, agent, agOwnerID, agNewID(t), conv)
	// Positive control: both bindings enqueue an ordinary Agent message.
	e.exec(`BEGIN`)
	e.exec(`SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, agOwnerID)
	control, _ := f.agentMessage(conv, "TEXT")
	e.exec(`COMMIT`)
	for _, table := range []string{"im_reply_outbox", "wechat_reply_outbox"} {
		var count int
		e.scalar(&count, `SELECT count(*) FROM openfox.`+table+` WHERE message_id=$1::uuid`, control)
		if count != 1 {
			t.Fatalf("inactive outbound fixture %s", table)
		}
	}
	postQuoted(t, owner, conv, "real earlier text", "", "text-before")
	for i := 0; i < 25; i++ {
		if document {
			sendDocReference(t, owner, conv, task, fmt.Sprintf("ref-%d", i))
		} else {
			sendReference(t, owner, conv, task, fmt.Sprintf("ref-%d", i))
		}
	}
	for _, table := range []string{"conversation_inputs", "execution_outbox", "im_reply_outbox", "wechat_reply_outbox"} {
		var count int
		if table == "execution_outbox" {
			e.scalar(&count, `SELECT count(*) FROM openfox.execution_outbox q JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.aggregate_id WHERE m.conversation_id=$1::uuid AND m.kind='REFERENCE'`, conv)
		} else {
			e.scalar(&count, `SELECT count(*) FROM openfox.`+table+` q JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id WHERE m.conversation_id=$1::uuid AND m.kind='REFERENCE'`, conv)
		}
		if count != 0 {
			t.Fatalf("reference created %s", table)
		}
	}
	current := postQuoted(t, owner, conv, "current actual input", "", "current", conversations.Mention{AgentID: agent})
	e.exec(`UPDATE openfox.conversation_inputs SET run_id=$3::uuid,status='ASSIGNED',assigned_at=clock_timestamp() WHERE organization_id=$1::uuid AND message_id=$2::uuid`, e.org, current.ID, run)
	response := e.deviceCall(executor, http.MethodGet, "/v1/device/organizations/"+e.org+"/runs/"+run+"/context?generation=1", nil)
	if response.Status != http.StatusOK {
		t.Fatalf("context %d %s", response.Status, response.Body)
	}
	var context dispatch.RunContext
	response.decode(t, &context)
	encoded, _ := json.Marshal(context)
	if strings.Contains(string(encoded), "Never send") || strings.Contains(string(encoded), body) || strings.Contains(string(encoded), task) {
		t.Fatal("reference leaked to Runtime")
	}
	found := false
	for _, m := range context.History {
		if m.Body == "real earlier text" {
			found = true
		}
	}
	if !found {
		t.Fatal("references consumed history LIMIT before exclusion")
	}
}

func TestTaskReferencesAssistantCrewAndCrossTenant(t *testing.T) {
	testAssistantCrewReferences(t, false)
}
func testAssistantCrewReferences(t *testing.T, document bool) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	f := newConvFixture(e)
	create := func(f convFixture, c *agClient, space, title string) string {
		if document {
			return d4Node(c, space, "", "", "doc", title).ID
		}
		return f.createTask(space, title)
	}
	send := sendReference
	body, request := conversations.TaskReferenceBody, referenceBody
	if document {
		send, body, request = sendDocReference, conversations.DocReferenceBody, docReferenceBody
	}
	task := create(f, owner, f.space, "passive")
	var assistant struct {
		Assistant agAgent `json:"assistant"`
	}
	owner.must(http.StatusOK, http.MethodPut, "assistant", map[string]any{
		"computer_id": e.deviceID, "grant_id": e.grantID, "runtime": "codex", "model": "default",
	}, &assistant)
	af := convFixture{env: e, space: assistant.Assistant.SpaceID, agentID: assistant.Assistant.ID}
	conv := af.newConversation(owner)
	send(t, owner, conv.ID, task, "assistant")
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid`, conv.ID)
	if count != 0 {
		t.Fatal("assistant reference auto-started")
	}
	// The first passive message does not become the assistant's default title.
	response := owner.must(http.StatusOK, http.MethodGet, "assistant/sessions", nil, nil)
	if strings.Contains(string(response.Body), body) {
		t.Fatal("reference became assistant session title")
	}
	e.exec(`UPDATE openfox.spaces SET status='ARCHIVED' WHERE id=$1::uuid`, assistant.Assistant.SpaceID)
	owner.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, conversationPath(conv.ID)+"/references", request(task, "archived"))
	crew := newCrewFixture(t)
	crewOwner := crew.env.client(agOwnerID)
	crewConv := crew.newConversation(crewOwner)
	cf := convFixture{env: crew.env, space: crew.space, agentID: crew.leader.ID}
	crewTask := create(cf, crewOwner, crew.space, "crew source")
	send(t, crewOwner, crewConv.ID, crewTask, "crew")
	crew.env.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid`, crewConv.ID)
	if count != 0 {
		t.Fatal("crew reference dispatched")
	}
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, conversationPath(crewConv.ID)+"/references", request(task, "foreign-target"))
	crewOwner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, conversationPath(crewConv.ID)+"/references", request(task, "foreign-source"))
}

func TestTaskReferencesGuestMovementAndSchema(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, guest := e.client(agOwnerID), e.client(agGuestID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	task := f.createTask(f.space, "Move source")
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agGuestID, map[string]any{"access_level": "COMMENTER"}, nil)
	// Shared space does not itself grant organization guests source/target access.
	guest.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/references", referenceBody(task, "guest"))
	e.addSpaceMember(f.space, agGuestID, "VIEWER")
	ref := sendReference(t, guest, conv.ID, task, "guest")
	if !ref.Reference.Available {
		t.Fatal("readable source rejected")
	}
	private := e.createSpace("private")
	// Exercise the existing move API so dependent rows and current-space ACL move together.
	var version int
	e.scalar(&version, `SELECT version FROM openfox.tasks WHERE id=$1::uuid`, task)
	owner.must(http.StatusOK, http.MethodPost, fmt.Sprintf("spaces/%s/tasks/%s/move", f.space, task), map[string]any{"target_space_id": private, "version": version}, nil, "Idempotency-Key", agNewID(t))
	page := referencePage(t, guest, conv.ID)
	if page[0].Reference.Available {
		t.Fatal("moved private task exposed")
	}
	ownerPage := referencePage(t, owner, conv.ID)
	if !ownerPage[0].Reference.Available || ownerPage[0].Reference.SpaceID != private {
		t.Fatal("owner link did not follow moved task")
	}
	for _, payload := range []any{nil, `{}`, `{"type":"DOC","id":"00000000-0000-0000-0000-000000000001"}`, `{"type":"TASK","id":"bad"}`, fmt.Sprintf(`{"type":"TASK","id":%q,"title":"copied"}`, task)} {
		if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.messages SET payload=$2::jsonb WHERE id=$1::uuid`, ref.ID, payload); err == nil {
			t.Fatalf("invalid payload accepted: %v", payload)
		}
	}
	if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.messages SET body='source body' WHERE id=$1::uuid`, ref.ID); err == nil {
		t.Fatal("non-placeholder reference body accepted")
	}
	if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.messages SET author_kind='AGENT',author_user_id=NULL,author_agent_id=$2::uuid WHERE id=$1::uuid`, ref.ID, f.agentID); err == nil {
		t.Fatal("agent reference accepted")
	}
}

func TestTaskReferencesRevokedMemberReceivesSafeInvalidation(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, reader := e.client(agOwnerID), e.client(agMemberID)
	conv := f.newConversation(owner)
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(conv.ID)+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	private := e.createSpace("private")
	task := f.createTask(private, "source secret")
	sendReference(t, owner, conv.ID, task, "event")
	if !referencePage(t, reader, conv.ID)[0].Reference.Available {
		t.Fatal("precondition")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/v1/organizations/"+e.org+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := reader.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE status %d", response.StatusCode)
	}
	owner.must(http.StatusNoContent, http.MethodDelete, "spaces/"+private+"/members/"+agMemberID, nil, nil)
	scanner := bufio.NewScanner(response.Body)
	event := ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if event == "member.changed" && strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			for _, secret := range []string{private, task, agMemberID, "source secret"} {
				if strings.Contains(data, secret) {
					t.Fatalf("invalidation leaked %q: %s", secret, data)
				}
			}
			var payload struct {
				SpaceID  string         `json:"space_id"`
				EntityID string         `json:"entity_id"`
				Payload  map[string]any `json:"payload"`
			}
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SpaceID != "" || payload.EntityID != "" || len(payload.Payload) != 0 {
				t.Fatalf("not an opaque signal: %s", data)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("revoked reader received no safe invalidation: %v", scanner.Err())
	}
	if referencePage(t, reader, conv.ID)[0].Reference.Available {
		t.Fatal("source remained readable")
	}
}

func TestTaskReferencesPreserveLegacyActionQuotes(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	conv := f.newConversation(owner)
	for _, kind := range []string{"ACTION_PROPOSAL", "ACTION_EXECUTED"} {
		source, _ := f.agentMessage(conv.ID, kind)
		reply := postQuoted(t, owner, conv.ID, "legacy action reply", source, kind)
		if reply.Reply == nil || !reply.Reply.Snapshot {
			t.Fatal("existing action quoting removed")
		}
		// Simulate the pre-snapshot data shape; only REFERENCE is excluded.
		e.exec(`BEGIN`)
		e.exec(`SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, agOwnerID)
		e.exec(`UPDATE openfox.messages SET reply_snapshot=NULL WHERE id=$1::uuid`, reply.ID)
		e.exec(`COMMIT`)
		var found bool
		for _, m := range referencePage(t, owner, conv.ID) {
			if m.ID == reply.ID {
				found = m.Reply != nil && !m.Reply.Snapshot && m.Reply.Body == "agent reply"
			}
		}
		if !found {
			t.Fatal("legacy action quote fallback removed")
		}
	}
}
