package stage2

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/api"
	"openfox/internal/artifacts"
	"strings"
	"sync"
	"testing"

	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func forkBody(source, agent string) map[string]any {
	return map[string]any{"source_message_id": source, "agent_id": agent, "workspace_selection": nil}
}
func forkChat(t *testing.T, c *agClient, conversation, source, agent, key string) conversations.Conversation {
	t.Helper()
	var result conversations.Conversation
	response := c.do(http.MethodPost, conversationPath(conversation)+"/fork", forkBody(source, agent), "Idempotency-Key", key)
	if response.Status != http.StatusCreated && response.Status != http.StatusOK {
		t.Fatalf("fork=%d %s", response.Status, response.Body)
	}
	if response.Status == http.StatusOK && response.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay header missing")
	}
	response.decode(t, &result)
	return result
}
func TestConversationForkSnapshotAndIndependentPermissions(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, member, viewer := e.client(agOwnerID), e.client(agMemberID), e.client(agManagerID)
	source := f.newConversation(owner)
	first := postQuoted(t, owner, source.ID, "BEFORE 中🦊", "", "first")
	action, _ := f.agentMessage(source.ID, "ACTION_PROPOSAL")
	quoted := postQuoted(t, owner, source.ID, "SELECTED", first.ID, "selected")
	postQuoted(t, owner, source.ID, "FUTURE SECRET", "", "future")
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(source.ID), map[string]any{"title": "FUTURE TITLE"}, nil)
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(source.ID)+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(source.ID)+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	// Genuine legacy quote is materialized only from copied rows.
	e.exec(`BEGIN`)
	e.exec(`SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, agOwnerID)
	e.exec(`UPDATE openfox.messages SET reply_snapshot=NULL WHERE id=$1::uuid`, quoted.ID)
	e.exec(`COMMIT`)
	for _, c := range []*agClient{viewer, e.client(agOutsiderID)} {
		want := http.StatusForbidden
		if c != viewer {
			want = http.StatusNotFound
		}
		c.expectError(want, "", http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(quoted.ID, f.agentID), "Idempotency-Key", "denied-key")
	}
	fork := forkChat(t, member, source.ID, quoted.ID, f.agentID, "snapshot-key")
	if fork.OwnerUserID != agMemberID || fork.Visibility != "PRIVATE" || fork.ForkPrefixSeq != quoted.Seq || fork.CrewID != "" || fork.Origin != "USER" {
		t.Fatalf("fork %+v", fork)
	}
	page := referencePage(t, member, fork.ID)
	if len(page) != 3 || page[0].ID == first.ID || page[0].Body != first.Body || !page[0].CreatedAt.Equal(first.CreatedAt) || page[2].ReplyToID != page[0].ID || page[2].Reply == nil || !page[2].Reply.Snapshot || page[2].Reply.MessageID != page[0].ID {
		t.Fatalf("copy %+v", page)
	}
	for _, m := range page {
		if !m.IsForkHistory || m.RunID != nil || m.ClientMessageID != "" || m.AssistantAction != "" || len(m.Mentions) != 0 || len(m.Attachments) != 0 {
			t.Fatalf("inherited metadata %+v", m)
		}
	}
	owner.expectError(http.StatusNotFound, "", http.MethodGet, conversationPath(fork.ID), nil)
	for _, endpoint := range []string{"/confirm", "/proposal"} {
		method := http.MethodPost
		var input any = map[string]any{"space_id": f.space, "title": "must not create"}
		if endpoint == "/proposal" {
			method = http.MethodDelete
			input = nil
		}
		member.expectError(http.StatusConflict, "FORK_HISTORY_READ_ONLY", method, conversationPath(fork.ID)+"/messages/"+page[1].ID+endpoint, input)
	}
	var count int
	for _, table := range []string{"conversation_members", "conversation_inputs", "runs", "im_chat_bindings", "wechat_bindings"} {
		e.scalar(&count, `SELECT count(*) FROM openfox.`+table+` WHERE organization_id=$1::uuid AND conversation_id=$2::uuid`, e.org, fork.ID)
		if count != 0 {
			t.Fatalf("%s=%d", table, count)
		}
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.execution_outbox WHERE organization_id=$1::uuid`, e.org)
	if count != 0 {
		t.Fatalf("outbox=%d", count)
	}
	var summary conversations.Summary
	response := member.must(http.StatusOK, http.MethodGet, conversationPath(fork.ID), nil, &summary)
	if summary.Title == nil || *summary.Title != "分支会话" || summary.ForkPrefixSeq != quoted.Seq || strings.Contains(string(response.Body), source.ID) || strings.Contains(string(response.Body), "FUTURE") {
		t.Fatalf("summary %s", response.Body)
	}
	owner.must(http.StatusNoContent, http.MethodDelete, conversationPath(source.ID)+"/members/"+agMemberID, nil, nil)
	again := forkChat(t, member, source.ID, quoted.ID, f.agentID, "snapshot-key")
	if again.ID != fork.ID {
		t.Fatal("replay after revocation")
	}
	member.expectError(http.StatusNotFound, "", http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(quoted.ID, f.agentID), "Idempotency-Key", "new-denied-key")
	if len(referencePage(t, member, fork.ID)) != 3 {
		t.Fatal("source revocation removed snapshot")
	}
	member.must(http.StatusNoContent, http.MethodPut, conversationPath(fork.ID)+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	if len(referencePage(t, viewer, fork.ID)) != 3 {
		t.Fatal("explicit new sharing requires source")
	}
	// A new input starts after the prefix, and does not turn any copied action writable.
	later := postQuoted(t, member, fork.ID, "NEW INPUT", "", "new-input")
	if later.Seq != quoted.Seq+1 || later.IsForkHistory {
		t.Fatalf("seq %+v", later)
	}
	// Source remains untouched by the fork (including its unresolved proposal).
	var status string
	e.scalar(&status, `SELECT payload->>'status' FROM openfox.messages WHERE id=$1::uuid`, action)
	if status != "pending" {
		t.Fatal(status)
	}
}

func TestConversationForkConcurrentIdempotencyAndValidation(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	source := f.newConversation(owner)
	m := postQuoted(t, owner, source.ID, "boundary", "", "source")
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(e.ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, agOwnerID)
	tx.Exec(e.ctx, `SET LOCAL ROLE openfox_app`)
	tenant, _ := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: agOwnerID, AuthorizationVersion: 1}, tenancy.SourceUserRequest)
	ctx := tenancy.WithTenantContext(e.ctx, tenant)
	_, debugErr := (conversations.Service{}).Fork(ctx, tx, e.org, source.ID, conversations.Actor{UserID: agOwnerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}, conversations.ForkInput{SourceMessageID: m.ID, AgentID: f.agentID})
	tx.Rollback(e.ctx)
	if debugErr != nil {
		t.Fatalf("direct fork: %v", debugErr)
	}
	path := conversationPath(source.ID) + "/fork"
	body := forkBody(m.ID, f.agentID)
	owner.expectError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", http.MethodPost, path, body)
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, path, map[string]any{"source_message_id": m.ID, "agent_id": f.agentID}, "Idempotency-Key", "missing-workspace")
	bad := forkBody(m.ID, f.agentID)
	bad["owner_user_id"] = agMemberID
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, path, bad, "Idempotency-Key", "forged-owner")
	bad = forkBody(m.ID, f.agentID)
	bad["workspace_selection"] = map[string]any{"mode": "REUSE", "source_conversation_id": source.ID}
	owner.expectError(http.StatusConflict, "WORKSPACE_UNAVAILABLE", http.MethodPost, path, bad, "Idempotency-Key", "illegal-reuse")
	responses := make([]agResponse, 6)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = owner.do(http.MethodPost, path, body, "Idempotency-Key", "concurrent-fork")
		}(i)
	}
	wg.Wait()
	var id string
	for _, r := range responses {
		if r.Status != http.StatusCreated && r.Status != http.StatusOK {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		var c conversations.Conversation
		r.decode(t, &c)
		if id == "" {
			id = c.ID
		}
		if c.ID != id {
			t.Fatal("duplicates")
		}
	}
	bad = forkBody(agNewID(t), f.agentID)
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, bad, "Idempotency-Key", "concurrent-fork")
	upper := forkChat(t, owner, strings.ToUpper(source.ID), strings.ToUpper(m.ID), strings.ToUpper(f.agentID), "concurrent-fork")
	if upper.ID != id {
		t.Fatal("UUID normalization")
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.conversations WHERE parent_conversation_id=$1::uuid`, source.ID)
	if count != 1 {
		t.Fatal(count)
	}
	e.exec(`UPDATE openfox.api_idempotency_keys SET created_at=clock_timestamp()-interval '25 hours',expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND operation='conversation.fork'`, e.org)
	if forkChat(t, owner, source.ID, m.ID, f.agentID, "concurrent-fork").ID == id {
		t.Fatal("expired key replay")
	}
}

func TestConversationForkMessageBudgetAtomicity(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	source := f.newConversation(owner)
	e.exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body)
 SELECT $1::uuid,gen_random_uuid(),$2::uuid,n,'USER',$3::uuid,'small 中🦊' FROM generate_series(1,501) n`, e.org, source.ID, agOwnerID)
	e.exec(`UPDATE openfox.conversations SET next_message_seq=502 WHERE id=$1::uuid`, source.ID)
	var last, allowed string
	e.scalar(&last, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1::uuid AND seq=501`, source.ID)
	e.scalar(&allowed, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1::uuid AND seq=500`, source.ID)
	owner.expectError(http.StatusRequestEntityTooLarge, "FORK_CONTEXT_TOO_LARGE", http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(last, f.agentID), "Idempotency-Key", "over-budget-key")
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.conversations WHERE parent_conversation_id=$1::uuid`, source.ID)
	if count != 0 {
		t.Fatal("partial fork")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.api_idempotency_keys WHERE organization_id=$1::uuid AND operation='conversation.fork'`, e.org)
	if count != 0 {
		t.Fatal("claim not rolled back")
	}
	fork := forkChat(t, owner, source.ID, allowed, f.agentID, "at-budget-key")
	e.scalar(&count, `SELECT count(*) FROM openfox.messages WHERE conversation_id=$1::uuid`, fork.ID)
	if count != 500 {
		t.Fatal(count)
	}
	e.exec(`UPDATE openfox.messages SET body=repeat('中🦊',10000) WHERE conversation_id=$1::uuid AND seq<=4`, source.ID)
	var fourth string
	e.scalar(&fourth, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1::uuid AND seq=4`, source.ID)
	owner.expectError(http.StatusRequestEntityTooLarge, "FORK_CONTEXT_TOO_LARGE", http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(fourth, f.agentID), "Idempotency-Key", "over-bytes-key")
}

// Keep JSON round-trip assertions explicit: source pointer columns must never be public.
func TestConversationForkPublicMetadata(t *testing.T) {
	encoded, _ := json.Marshal(conversations.Conversation{ForkPrefixSeq: 4})
	if strings.Contains(string(encoded), "parent_conversation") || strings.Contains(string(encoded), "fork_message_id") {
		t.Fatal(string(encoded))
	}
}

func forkTenantTx(t *testing.T, e *agEnv, actor string, run func(context.Context, pgx.Tx) error) error {
	t.Helper()
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err = tx.Exec(e.ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(e.ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		t.Fatal(err)
	}
	tenant, err := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: actor, AuthorizationVersion: 1}, tenancy.SourceUserRequest)
	if err != nil {
		t.Fatal(err)
	}
	return run(tenancy.WithTenantContext(e.ctx, tenant), tx)
}
func TestConversationForkActionsDoubleGateAndRollback(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	source := f.newConversation(owner)
	proposal, _ := f.agentMessage(source.ID, "ACTION_PROPOSAL")
	boundary := postQuoted(t, owner, source.ID, "FORK_INSERT_FAULT", "", "end")
	fork := forkChat(t, owner, source.ID, boundary.ID, f.agentID, "actions-fork")
	copied := referencePage(t, owner, fork.ID)[0]
	err := forkTenantTx(t, e, agOwnerID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).ResolveProposal(ctx, tx, conversations.Proposal{ConversationID: fork.ID, MessageID: copied.ID, AgentID: f.agentID, Payload: conversations.ProposalPayload{Action: "create_task", Status: "pending"}}, "cancelled", nil)
		return err
	})
	if !errors.Is(err, conversations.ErrProposalResolved) {
		t.Fatalf("direct service bypass: %v", err)
	}
	// Both original proposals and later genuine proposals remain writable.
	owner.must(http.StatusOK, http.MethodDelete, conversationPath(source.ID)+"/messages/"+proposal+"/proposal", nil, nil)
	fresh, _ := f.agentMessage(fork.ID, "ACTION_PROPOSAL")
	owner.must(http.StatusOK, http.MethodDelete, conversationPath(fork.ID)+"/messages/"+fresh+"/proposal", nil, nil)
	// Isolated fault injection: fail after target Create and before bulk INSERT completes.
	e.exec(`ALTER TABLE openfox.messages ADD CONSTRAINT c04_fixture_failure CHECK(body<>'FORK_INSERT_FAULT' OR conversation_id='` + source.ID + `'::uuid) NOT VALID`)
	response := owner.do(http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(boundary.ID, f.agentID), "Idempotency-Key", "rollback-fork")
	e.exec(`ALTER TABLE openfox.messages DROP CONSTRAINT c04_fixture_failure`)
	if response.Status < 400 {
		t.Fatal("fault did not reject")
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.api_idempotency_keys WHERE organization_id=$1::uuid AND idempotency_key='rollback-fork'`, e.org)
	if count != 0 {
		t.Fatal("claim survived failure")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.conversations WHERE parent_conversation_id=$1::uuid`, source.ID)
	if count != 1 {
		t.Fatal("partial target survived failure")
	}
	forkChat(t, owner, source.ID, boundary.ID, f.agentID, "rollback-fork")
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(source.ID), map[string]any{"archived": true}, nil)
	owner.expectError(http.StatusConflict, "CONVERSATION_CLOSED", http.MethodPost, conversationPath(source.ID)+"/fork", forkBody(boundary.ID, f.agentID), "Idempotency-Key", "archived-fork")
}

func TestConversationForkReferencesAttachmentsSharingAndDeletion(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, member := e.client(agOwnerID), e.client(agMemberID)
	source := f.newConversation(owner)
	first := postQuoted(t, owner, source.ID, "C04 copied text", "", "first")
	attachment := agNewID(t)
	e.exec(`INSERT INTO openfox.attachments(organization_id,id,owner_kind,space_id,conversation_id,message_id,name,file_key,content_type,size_bytes,sha256,uploaded_by)
 VALUES($1::uuid,$2::uuid,'CONVERSATION',$3::uuid,$4::uuid,$5::uuid,'PRIVATE-FILE.txt','fixture-private-file','text/plain',1,repeat('0',64),$6::uuid)`, e.org, attachment, f.space, source.ID, first.ID, agOwnerID)
	secretSpace := e.createSpace("private")
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id<>$3::uuid`, e.org, secretSpace, agOwnerID)
	task := f.createTask(secretSpace, "DYNAMIC PRIVATE TASK")
	doc := d4Node(owner, secretSpace, "", "", "doc", "DYNAMIC PRIVATE DOC").ID
	sendReference(t, owner, source.ID, task, "task-ref")
	sendDocReference(t, owner, source.ID, doc, "doc-ref")
	boundary := postQuoted(t, owner, source.ID, "C04 end", first.ID, "end")
	fork := forkChat(t, owner, source.ID, boundary.ID, f.agentID, "consumers-fork")
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(fork.ID)+"/members/"+agMemberID, map[string]any{"access_level": "VIEWER"}, nil)
	items := referencePage(t, member, fork.ID)
	encoded, _ := json.Marshal(items)
	for _, secret := range []string{"DYNAMIC PRIVATE", task, doc, "PRIVATE-FILE", attachment, "fixture-private-file"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leak %s", secret)
		}
	}
	for _, i := range []int{1, 2} {
		if items[i].Reference == nil || items[i].Reference.Available || len(items[i].Payload) != 0 {
			t.Fatalf("ref %+v", items[i])
		}
	}
	if len(items[0].Attachments) != 0 || items[3].Reply == nil || items[3].Reply.Body != first.Body {
		t.Fatal("copy/reply")
	}
	// A copied quote remains a proper same-conversation quote, not a pointer into the source.
	quoted := postQuoted(t, owner, fork.ID, "reply in fork", items[0].ID, "new-quote")
	if quoted.Reply == nil || quoted.Reply.MessageID != items[0].ID {
		t.Fatal("C01")
	}
	response := owner.must(http.StatusCreated, http.MethodPost, "share-snapshots", map[string]any{"kind": "CONVERSATION", "conversation_id": fork.ID, "scope": "ORGANIZATION", "expiry": "30d"}, nil)
	var shared struct {
		Token string `json:"token"`
	}
	response.decode(t, &shared)
	view := member.must(http.StatusOK, http.MethodPost, "/v1/share-snapshots/view", map[string]string{"token": shared.Token}, nil)
	if !strings.Contains(string(view.Body), "C04 copied text") || strings.Contains(string(view.Body), "DYNAMIC PRIVATE") || strings.Contains(string(view.Body), "PRIVATE-FILE") {
		t.Fatalf("share %s", view.Body)
	}
	owner.expectError(http.StatusNotFound, "", http.MethodPost, "share-snapshots", map[string]any{"kind": "CONVERSATION", "conversation_id": fork.ID, "message_id": items[1].ID, "scope": "ORGANIZATION", "expiry": "30d"})
	// Delete a minimal original source in the isolated fixture: no provenance FK
	// blocks deletion and copied history remains readable.
	original := f.newConversation(owner)
	single := postQuoted(t, owner, original.ID, "source deletion survival", "", "source")
	independent := forkChat(t, owner, original.ID, single.ID, f.agentID, "deletion-fork")
	e.exec(`DELETE FROM openfox.messages WHERE conversation_id=$1::uuid`, original.ID)
	e.exec(`DELETE FROM openfox.conversations WHERE id=$1::uuid`, original.ID)
	if got := referencePage(t, owner, independent.ID); len(got) != 1 || got[0].Body != single.Body {
		t.Fatal("source deletion lost snapshot")
	}
}

func TestConversationForkRuntimeBudgetAndRunIsolation(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	device := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	space, agent, sourceRun := e.devRunFixture(device, nil)
	var source string
	e.scalar(&source, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1::uuid`, sourceRun)
	f := convFixture{env: e, space: space, agentID: agent}
	task := f.createTask(space, "NEVER IN RUNTIME")
	for i := 0; i < 22; i++ {
		postQuoted(t, owner, source, strings.Repeat("中🦊", 600), "", agSuffix(t))
	}
	sendReference(t, owner, source, task, "passive-task")
	boundary := postQuoted(t, owner, source, "selected runtime prefix", "", "boundary")
	postQuoted(t, owner, source, "AFTER BOUNDARY NEVER IN RUNTIME", "", "future")
	fork := forkChat(t, owner, source, boundary.ID, agent, "runtime-fork")
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.runs WHERE conversation_id=$1::uuid`, fork.ID)
	if count != 0 {
		t.Fatal("automatic run")
	}
	current := postQuoted(t, owner, fork.ID, "real new input", "", "new-input", conversations.Mention{AgentID: agent})
	run := agNewID(t)
	// Controlled dispatcher lease fixture; no external Runtime is started.
	e.exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,lease_expires_at)
 SELECT organization_id,$2::uuid,space_id,$3::uuid,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,'PREPARING',clock_timestamp()+interval '5 minutes'
 FROM openfox.runs WHERE id=$1::uuid`, sourceRun, run, fork.ID)
	e.exec(`UPDATE openfox.conversation_inputs SET run_id=$2::uuid,status='ASSIGNED',assigned_at=clock_timestamp() WHERE message_id=$1::uuid`, current.ID, run)
	response := e.deviceCall(device, http.MethodGet, "/v1/device/organizations/"+e.org+"/runs/"+run+"/context?generation=1", nil)
	if response.Status != http.StatusOK {
		t.Fatalf("context %d %s", response.Status, response.Body)
	}
	var rc dispatch.RunContext
	response.decode(t, &rc)
	total := 0
	for _, m := range rc.History {
		total += len(m.Body)
	}
	if len(rc.History) != 8 || total > dispatch.MaxHistoryBytes || strings.Contains(string(response.Body), "NEVER IN RUNTIME") || strings.Contains(string(response.Body), conversations.TaskReferenceBody) {
		t.Fatalf("history=%d bytes=%d %s", len(rc.History), total, response.Body)
	}
	if rc.ConversationID != fork.ID {
		t.Fatal("runtime context crossed conversation")
	}
	if rc.Prompt != "real new input" {
		t.Fatalf("prompt=%q", rc.Prompt)
	}
	// Even a later run with an artificially early finish cannot acquire copied messages.
	e.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at='2000-01-01' WHERE id=$1::uuid`, run)
	for _, m := range referencePage(t, owner, fork.ID) {
		if m.IsForkHistory && m.RunID != nil {
			t.Fatal("inherited run association")
		}
	}
}

func TestConversationForkSessionExportDoesNotRecoverParent(t *testing.T) {
	e := newAgEnvWithOptionsAndStorage(t, api.DispatchOptions{}, artifacts.Local{Root: t.TempDir()})
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	source := f.newConversation(owner)
	boundary := postQuoted(t, owner, source.ID, "source session history", "", "boundary")
	sourceRun := f.insertRun(source.ID, "SUCCEEDED")
	e.exec(`UPDATE openfox.runs SET runtime='codex',model='fixture',approval_mode='FOLLOW_RUNTIME' WHERE id=$1::uuid`, sourceRun)
	e.exec(`INSERT INTO openfox.run_events(organization_id,run_id,generation,sequence,type,safe_payload,payload_hash)
 VALUES($1::uuid,$2::uuid,1,1,'started','{"data":{"thread_id":"source-native-session"}}',repeat('0',64))`, e.org, sourceRun)
	e.exec(`UPDATE openfox.physical_devices SET capabilities='{"runtime_session_export":true,"runtimes":{"codex":{"runnable":true}}}' WHERE id=$1::uuid`, e.deviceID)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"public_key": base64.RawURLEncoding.EncodeToString(der)}
	owner.must(http.StatusAccepted, http.MethodPost, conversationPath(source.ID)+"/runtime-session-exports", input, nil)
	var fullHour bool
	e.scalar(&fullHour, `SELECT expires_at = created_at + interval '1 hour' FROM openfox.runtime_session_exports
		WHERE organization_id = $1::uuid AND conversation_id = $2::uuid`, e.org, source.ID)
	if !fullHour {
		t.Fatal("导出记录应从同一数据库时刻起算一小时")
	}

	fork := forkChat(t, owner, source.ID, boundary.ID, f.agentID, "session-export-fork")
	owner.expectError(http.StatusNotFound, "SESSION_NOT_EXPORTABLE", http.MethodPost, conversationPath(fork.ID)+"/runtime-session-exports", input)
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.device_commands WHERE organization_id=$1::uuid AND kind='runtime_session_export'`, e.org)
	if count != 1 {
		t.Fatal("parent export reused")
	}
}
