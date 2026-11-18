package stage2

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/conversations"
	"openfox/internal/dispatch"
)

func postQuoted(t *testing.T, client *agClient, conversation, body, source, key string, mentions ...conversations.Mention) conversations.Message {
	t.Helper()
	input := map[string]any{"body": body, "client_message_id": key}
	if source != "" {
		input["reply_to_id"] = source
	}
	if len(mentions) > 0 {
		input["mentions"] = mentions
	}
	var message conversations.Message
	client.must(http.StatusCreated, http.MethodPost, conversationPath(conversation)+"/messages", input, &message)
	return message
}

// Real HTTP/session/PG path. Direct SQL changes below are isolated fixture fault
// injection, not a message editing/deletion feature.
func TestMessageRepliesSnapshotsLegacyAndPermissions(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, commenter, viewer := e.client(agOwnerID), e.client(agMemberID), e.client(agManagerID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	body := strings.Repeat("中🦊", 1001)
	source := postQuoted(t, owner, conv.ID, body, "", "source")
	quote := postQuoted(t, owner, conv.ID, "a reply", source.ID, "quoted")
	if quote.Body != "a reply" || quote.ReplyToID != source.ID || quote.Reply == nil || !quote.Reply.Snapshot || !quote.Reply.Truncated || utf8.RuneCountInString(quote.Reply.Body) != 2000 || quote.Reply.Body != strings.Repeat("中🦊", 1000) || quote.Reply.AuthorKind != "USER" {
		t.Fatalf("quote = %+v / %+v", quote, quote.Reply)
	}
	agentID, _ := f.agentMessage(conv.ID, "TEXT")
	agentReply := postQuoted(t, owner, conv.ID, "agent quote", agentID, "agent-quoted")
	if agentReply.Reply == nil || agentReply.Reply.AuthorKind != "AGENT" || agentReply.Reply.AuthorAgentID != f.agentID {
		t.Fatalf("agent quote = %+v", agentReply.Reply)
	}
	// Frozen text survives source modification; retry must return the SAME snapshot.
	e.exec(`UPDATE openfox.messages SET body='modified fixture source' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, source.ID)
	retried := postQuoted(t, owner, conv.ID, "a reply", source.ID, "quoted")
	if retried.ID != quote.ID || !reflect.DeepEqual(retried.Reply, quote.Reply) {
		t.Fatal("retry regenerated snapshot")
	}
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path+"/messages", map[string]any{"body": "a reply", "reply_to_id": agentID, "client_message_id": "quoted"})
	legacy := postQuoted(t, owner, conv.ID, "legacy", source.ID, "legacy")
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err = tx.Exec(e.ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, e.org, agOwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(e.ctx, `UPDATE openfox.messages SET reply_snapshot=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Messages []conversations.Message `json:"messages"`
	}
	owner.must(http.StatusOK, http.MethodGet, path+"/messages?limit=100", nil, &page)
	seen := map[string]conversations.Message{}
	for _, message := range page.Messages {
		seen[message.ID] = message
	}
	if !reflect.DeepEqual(seen[quote.ID].Reply, quote.Reply) || seen[source.ID].Reply != nil || seen[legacy.ID].Reply == nil || seen[legacy.ID].Reply.Snapshot || seen[legacy.ID].Reply.Body != "modified fixture source" {
		t.Fatal("list lost snapshot/legacy/no-reference distinction")
	}
	// Existing client support for non-TEXT IDs remains compatible (UI only offers TEXT).
	proposal, _ := f.agentMessage(conv.ID, "ACTION_PROPOSAL")
	postQuoted(t, owner, conv.ID, "legacy client proposal reference", proposal, "proposal")
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	postQuoted(t, commenter, conv.ID, "allowed", source.ID, "commenter")
	denied := map[string]any{"body": "denied", "reply_to_id": source.ID, "client_message_id": "denied"}
	viewer.expectError(http.StatusForbidden, "", http.MethodPost, path+"/messages", denied)
	viewer.must(http.StatusOK, http.MethodGet, path+"/messages", nil, &page)
	owner.must(http.StatusNoContent, http.MethodDelete, path+"/members/"+agMemberID, nil, nil)
	commenter.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/messages", denied)
	commenter.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/messages", nil)
	other := f.newConversation(owner)
	for _, id := range []string{source.ID, agNewID(t)} {
		owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, conversationPath(other.ID)+"/messages", map[string]any{"body": "wrong source", "reply_to_id": id, "client_message_id": id})
	}
	e.client(agOutsiderID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/messages", denied)
	// A foreign-tenant source is indistinguishable from a missing source.
	foreign := newAgEnv(t)
	foreignFixture := newConvFixture(foreign)
	foreignOwner := foreign.client(agOwnerID)
	foreignConversation := foreignFixture.newConversation(foreignOwner)
	foreignSource := postQuoted(t, foreignOwner, foreignConversation.ID, "foreign confidential text", "", "source")
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/messages", map[string]any{"body": "cross tenant", "reply_to_id": foreignSource.ID, "client_message_id": "foreign"})
	// Existing FK deliberately prevents physical deletion of a referenced source.
	if _, err := e.superuser.Exec(e.ctx, `DELETE FROM openfox.messages WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, source.ID); err != nil {
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "23503" {
			t.Fatalf("expected existing reference FK, got %v", err)
		}
	} else {
		t.Fatal("referenced source was deleted")
	}
}

func TestMessageRepliesConcurrentIdempotency(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	conv := f.newConversation(owner)
	source := postQuoted(t, owner, conv.ID, "source", "", "source")
	input := map[string]any{"body": "reply", "reply_to_id": source.ID, "client_message_id": "same", "mentions": []map[string]string{{"agent_id": f.agentID}}}
	responses := make([]agResponse, 5)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = owner.do(http.MethodPost, conversationPath(conv.ID)+"/messages", input)
		}(i)
	}
	wg.Wait()
	var id string
	for _, response := range responses {
		if response.Status != http.StatusCreated {
			t.Fatalf("concurrent = %d %s", response.Status, response.Body)
		}
		var message conversations.Message
		response.decode(t, &message)
		if id == "" {
			id = message.ID
		}
		if message.ID != id || message.Reply == nil || !message.Reply.Snapshot {
			t.Fatal("non-idempotent response")
		}
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid`, e.org, id)
	if count != 1 {
		t.Fatalf("execution inputs=%d", count)
	}
}

func TestMessageRepliesDeviceContextAndHistoryBudget(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	_, agentID, runID := e.devRunFixture(executor, nil)
	var conv string
	e.scalar(&conv, `SELECT conversation_id::text FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
	source := postQuoted(t, owner, conv, strings.Repeat("中🦊", 1001), "", "source")
	// Source falls outside the 20-row history window. Each quote consumes >7KB.
	for i := 0; i < 22; i++ {
		postQuoted(t, owner, conv, "historic reply", source.ID, agSuffix(t))
	}
	current := postQuoted(t, owner, conv, "current request", source.ID, "current", conversations.Mention{AgentID: agentID})
	e.exec(`UPDATE openfox.conversation_inputs SET run_id=$3::uuid,status='ASSIGNED',assigned_at=clock_timestamp() WHERE organization_id=$1::uuid AND message_id=$2::uuid`, e.org, current.ID, runID)
	response := e.deviceCall(executor, http.MethodGet, "/v1/device/organizations/"+e.org+"/runs/"+runID+"/context?generation=1", nil)
	if response.Status != http.StatusOK {
		t.Fatalf("context=%d %s", response.Status, response.Body)
	}
	var context dispatch.RunContext
	response.decode(t, &context)
	if !strings.Contains(context.Prompt, strings.Repeat("中🦊", 1000)) || !strings.HasSuffix(context.Prompt, "Reply:\ncurrent request") {
		t.Fatal("context omitted frozen reference outside history window")
	}
	total := 0
	for _, message := range context.History {
		total += len(message.Body)
		if !strings.Contains(message.Body, "Quoted USER message #1") {
			t.Fatal("history omitted quote")
		}
	}
	if total > dispatch.MaxHistoryBytes || len(context.History) != 4 {
		t.Fatalf("history count=%d bytes=%d", len(context.History), total)
	}
	var body string
	e.scalar(&body, `SELECT body FROM openfox.conversation_inputs WHERE organization_id=$1::uuid AND message_id=$2::uuid`, e.org, current.ID)
	if body != "current request" {
		t.Fatal("execution input body was rewritten")
	}
	// A device without the run grant cannot obtain source or reply text.
	bystander := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	expectCode(t, e.deviceCall(bystander, http.MethodGet, "/v1/device/organizations/"+e.org+"/runs/"+runID+"/context?generation=1", nil), http.StatusNotFound, "")
}
