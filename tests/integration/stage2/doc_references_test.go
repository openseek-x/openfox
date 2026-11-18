package stage2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/docs"
)

func docReferenceBody(id, key string) map[string]any {
	return map[string]any{"type": "DOC", "doc_id": id, "client_message_id": key}
}
func sendDocReference(t *testing.T, c *agClient, conv, id, key string) conversations.Message {
	t.Helper()
	var item conversations.Message
	c.must(http.StatusCreated, http.MethodPost, conversationPath(conv)+"/references", docReferenceBody(id, key), &item)
	return item
}
func d4Set(c *agClient, space string) string {
	var item docs.Set
	c.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/doc-sets", map[string]any{"name": "D04 team"}, &item)
	return item.ID
}
func d4Node(c *agClient, space, set, parent, kind, title string) docs.NodeDetail {
	input := map[string]any{"kind": kind, "title": title, "container": map[string]any{"kind": "personal"}}
	if set != "" {
		input["container"] = map[string]any{"kind": "set", "set_id": set}
	}
	if parent != "" {
		input["parent_id"] = parent
	}
	if kind == "doc" || kind == "sheet" {
		input["body"] = "D04-PRIVATE-BODY-🦊"
	}
	var item docs.NodeDetail
	c.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/docs", input, &item)
	return item
}
func d4Get(c *agClient, space, id string) docs.NodeDetail {
	var item docs.NodeDetail
	c.must(http.StatusOK, http.MethodGet, "spaces/"+space+"/docs/"+id, nil, &item)
	return item
}
func d4Upload(c *agClient, path string, fields map[string]string, name string, data []byte) agResponse {
	t := c.env.t
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(c.env.ctx, http.MethodPost, c.server.URL+"/v1/organizations/"+c.env.org+"/"+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	r, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("upload %d %s", r.StatusCode, b)
	}
	return agResponse{Status: r.StatusCode, Body: b, Header: r.Header}
}
func assertDocUnavailable(t *testing.T, c *agClient, conv string, secrets ...string) {
	t.Helper()
	page := referencePage(t, c, conv)
	b, _ := json.Marshal(page)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(b), secret) {
			t.Fatalf("denied reference leaked %q", secret)
		}
	}
	for _, m := range page {
		if m.Kind == "REFERENCE" && (m.Reference == nil || m.Reference.Type != "DOC" || m.Reference.Available || m.Payload != nil) {
			t.Fatalf("unsafe reference %+v", m)
		}
	}
}

func TestDocReferencesPersonalTeamKindsAndConsumers(t *testing.T) {
	e := newAgEnvWithOptionsAndStorage(t, api.DispatchOptions{}, artifacts.Local{Root: t.TempDir()})
	f := newConvFixture(e)
	owner, member, admin := e.client(agOwnerID), e.client(agMemberID), e.client(agAdminID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	for _, id := range []string{agMemberID, agAdminID, agGuestID} {
		owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+id, map[string]any{"access_level": "COMMENTER"}, nil)
	}
	personal := d4Node(owner, f.space, "", "", "doc", strings.Repeat("中🦊", 100))
	m := sendDocReference(t, owner, conv.ID, strings.ToUpper(personal.ID), "personal")
	if m.Reference == nil || !m.Reference.Available || m.Reference.DocID != personal.ID || m.Reference.DocKind != "doc" || m.Reference.Title != personal.Title || m.Body != conversations.DocReferenceBody || m.Payload != nil || m.RunID != nil {
		t.Fatalf("card %+v", m)
	}
	assertDocUnavailable(t, member, conv.ID, personal.ID, personal.Title)
	assertDocUnavailable(t, admin, conv.ID, personal.ID, personal.Title)
	for _, c := range []*agClient{member, admin} {
		c.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(personal.ID, "forbidden"))
	}
	// Organization owner also cannot read another member's personal node.
	other := d4Node(member, f.space, "", "", "doc", "member-only")
	owner.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(other.ID, "not-mine"))
	set := d4Set(owner, f.space)
	sheet := d4Node(owner, f.space, set, "", "sheet", "")
	fileData := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...)
	var file docs.Node
	d4Upload(owner, "spaces/"+f.space+"/docs/upload", map[string]string{"container": "set", "set_id": set}, "secret-file.png", fileData).decode(t, &file)
	var attachment docs.Attachment
	d4Upload(owner, "spaces/"+f.space+"/docs/"+personal.ID+"/attachments", nil, "private.png", fileData).decode(t, &attachment)
	for _, node := range []docs.Node{sheet.Node, file} {
		got := sendDocReference(t, member, conv.ID, node.ID, node.Kind)
		if !got.Reference.Available || got.Reference.DocKind != node.Kind {
			t.Fatalf("kind %+v", got)
		}
		b, _ := json.Marshal(got)
		for _, secret := range []string{"D04-PRIVATE-BODY", attachment.ID, "file_key", "download", "attachments/"} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("copied source %q", secret)
			}
		}
	}
	// Reference never grants download/attachment access.
	member.expectError(404, "NOT_FOUND", http.MethodGet, "spaces/"+f.space+"/docs/"+personal.ID+"/download", nil)
	member.expectError(404, "NOT_FOUND", http.MethodGet, "spaces/"+f.space+"/doc-attachments/"+attachment.ID, nil)
	downloaded := member.must(200, http.MethodGet, "spaces/"+f.space+"/docs/"+file.ID+"/download", nil, nil)
	if !bytes.Equal(downloaded.Body, fileData) {
		t.Fatal("file download changed")
	}
	owner.expectError(404, "NOT_FOUND", http.MethodPost, path+"/messages", map[string]any{"body": "quote", "client_message_id": "quote", "reply_to_id": m.ID})
	owner.expectError(404, "NOT_FOUND", http.MethodPost, "share-snapshots", map[string]any{"kind": "CONVERSATION", "conversation_id": conv.ID, "message_id": m.ID, "scope": "ORGANIZATION", "expiry": "30d"})
	search := member.must(200, http.MethodGet, "search?types=message&q=D04-PRIVATE-BODY", nil, nil)
	if strings.Contains(string(search.Body), m.ID) {
		t.Fatal("source indexed")
	}
	var stored string
	e.scalar(&stored, `SELECT body||payload::text FROM openfox.messages WHERE id=$1::uuid`, m.ID)
	if strings.Contains(stored, personal.Title) || strings.Contains(stored, "BODY") {
		t.Fatal("copied into row")
	}
	folder := d4Node(owner, f.space, set, "", "folder", "folder")
	for _, id := range []string{folder.ID, set, agNewID(t)} {
		owner.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(id, "excluded-"+id))
	}
	private := e.createSpace("private")
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id<>$3::uuid`, e.org, private, agOwnerID)
	restricted := d4Node(owner, private, d4Set(owner, private), "", "doc", "private-team")
	member.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(restricted.ID, "private"))
	admin.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(restricted.ID, "admin-private"))
	guest := e.client(agGuestID)
	guest.expectError(404, "NOT_FOUND", http.MethodPost, path+"/references", docReferenceBody(sheet.ID, "guest"))
	e.addSpaceMember(f.space, agGuestID, "VIEWER")
	if !sendDocReference(t, guest, conv.ID, sheet.ID, "guest").Reference.Available {
		t.Fatal("read-only guest source")
	}
	e.addSpaceMember(private, agMemberID, "VIEWER")
	if !sendDocReference(t, member, conv.ID, restricted.ID, "private-now").Reference.Available {
		t.Fatal("read-only source rejected")
	}
}

func TestDocReferencesMoveDeleteAndSafeOldScopeEvent(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, reader := e.client(agOwnerID), e.client(agMemberID)
	conv := f.newConversation(owner)
	owner.must(204, http.MethodPut, conversationPath(conv.ID)+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	set := d4Set(owner, f.space)
	folder := d4Node(owner, f.space, set, "", "folder", "parent")
	node := d4Node(owner, f.space, set, folder.ID, "doc", "current-title")
	original := sendDocReference(t, owner, conv.ID, node.ID, "move")
	if !referencePage(t, reader, conv.ID)[0].Reference.Available {
		t.Fatal("precondition")
	}
	owner.must(200, http.MethodPatch, "spaces/"+f.space+"/docs/"+node.ID, map[string]any{"version": node.Version, "title": "renamed"}, nil)
	if referencePage(t, reader, conv.ID)[0].Reference.Title != "renamed" {
		t.Fatal("snapshot instead of live metadata")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/v1/organizations/"+e.org+"/events", nil)
	stream, err := reader.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatal(stream.StatusCode)
	}
	owner.must(200, http.MethodPost, "spaces/"+f.space+"/docs/"+folder.ID+"/move", map[string]any{"version": folder.Version, "target_space_id": f.space, "container": map[string]any{"kind": "personal"}}, nil)
	scanner := bufio.NewScanner(stream.Body)
	event := ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if event == "doc.changed" && strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var item struct {
				SpaceID  string         `json:"space_id"`
				EntityID string         `json:"entity_id"`
				Payload  map[string]any `json:"payload"`
			}
			if err = json.Unmarshal([]byte(data), &item); err != nil {
				t.Fatal(err)
			}
			if item.SpaceID != f.space || item.EntityID != "" || len(item.Payload) != 0 || strings.Contains(data, node.ID) || strings.Contains(data, folder.ID) || strings.Contains(data, "renamed") {
				t.Fatalf("unsafe old event %s", data)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("old scope received no invalidation %v", scanner.Err())
	}
	assertDocUnavailable(t, reader, conv.ID, node.ID, "renamed")
	private := e.createSpace("private")
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id<>$3::uuid`, e.org, private, agOwnerID)
	movedFolder := d4Get(owner, f.space, folder.ID)
	targetSet := d4Set(owner, private)
	owner.must(200, http.MethodPost, "spaces/"+f.space+"/docs/"+folder.ID+"/move", map[string]any{"version": movedFolder.Version, "target_space_id": private, "container": map[string]any{"kind": "set", "set_id": targetSet}}, nil)
	page := referencePage(t, owner, conv.ID)
	if page[0].Reference.SpaceID != private {
		t.Fatal("link did not move")
	}
	assertDocUnavailable(t, reader, conv.ID, node.ID, private)
	e.addSpaceMember(private, agMemberID, "VIEWER")
	if !referencePage(t, reader, conv.ID)[0].Reference.Available {
		t.Fatal("new permission")
	}
	owner.must(204, http.MethodDelete, "spaces/"+private+"/members/"+agMemberID, nil, nil)
	assertDocUnavailable(t, reader, conv.ID, node.ID, private)
	current := d4Get(owner, private, folder.ID)
	owner.must(204, http.MethodDelete, fmt.Sprintf("spaces/%s/docs/%s?version=%d", private, folder.ID, current.Version), nil, nil)
	assertDocUnavailable(t, owner, conv.ID, node.ID, private, "renamed")
	replay := sendDocReference(t, owner, conv.ID, node.ID, "move")
	if replay.ID != original.ID || replay.Reference.Available {
		t.Fatal("deleted replay")
	}
	n := d4Node(owner, private, targetSet, "", "doc", "set-deleted")
	sendDocReference(t, owner, conv.ID, n.ID, "set-delete")
	var version uint64
	e.scalar(&version, `SELECT version FROM openfox.doc_sets WHERE id=$1::uuid`, targetSet)
	owner.must(204, http.MethodDelete, fmt.Sprintf("spaces/%s/doc-sets/%s?version=%d", private, targetSet, version), nil, nil)
	assertDocUnavailable(t, owner, conv.ID, n.ID, node.ID)
}

func TestDocReferencesIdempotencyTargetAccessAndSchema(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, member := e.client(agOwnerID), e.client(agMemberID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID) + "/references"
	node := d4Node(owner, f.space, d4Set(owner, f.space), "", "doc", "source")
	var wg sync.WaitGroup
	responses := make([]agResponse, 8)
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = owner.do(http.MethodPost, path, docReferenceBody(node.ID, "same"))
		}(i)
	}
	wg.Wait()
	id := ""
	for _, r := range responses {
		if r.Status != 201 {
			t.Fatalf("concurrent %d %s", r.Status, r.Body)
		}
		var m conversations.Message
		r.decode(t, &m)
		if id == "" {
			id = m.ID
		}
		if id != m.ID || m.Seq != 1 {
			t.Fatal("duplicate")
		}
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1::uuid AND resource_id=$2 AND action='conversation.message_created'`, e.org, id)
	if count != 1 {
		t.Fatal(count)
	}
	for _, bad := range []map[string]any{{"type": "DOC", "doc_id": node.ID, "task_id": nil, "client_message_id": "mixed-null"}, {"type": "TASK", "task_id": node.ID, "doc_id": "", "client_message_id": "mixed-empty"}, {"type": "DOC", "doc_id": node.ID, "body": "copy", "client_message_id": "body"}, {"type": "DOC", "doc_id": nil, "client_message_id": "null"}, docReferenceBody(node.ID, ""), docReferenceBody(node.ID, strings.Repeat("k", 257)), docReferenceBody("bad", "bad")} {
		owner.expectError(400, "INVALID_REQUEST", http.MethodPost, path, bad)
	}
	owner.expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, docReferenceBody(agNewID(t), "same"))
	owner.expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, referenceBody(node.ID, "same"))
	owner.expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, conversationPath(conv.ID)+"/messages", map[string]any{"body": "text", "client_message_id": "same"})
	member.expectError(404, "NOT_FOUND", http.MethodPost, path, docReferenceBody(node.ID, "member"))
	owner.must(204, http.MethodPut, conversationPath(conv.ID)+"/members/"+agMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	sendDocReference(t, member, conv.ID, node.ID, "member")
	member.expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, docReferenceBody(node.ID, "same"))
	owner.must(204, http.MethodPut, conversationPath(conv.ID)+"/members/"+agMemberID, map[string]any{"access_level": "VIEWER"}, nil)
	member.expectError(403, "ACTION_FORBIDDEN", http.MethodPost, path, docReferenceBody(node.ID, "member"))
	owner.must(204, http.MethodDelete, conversationPath(conv.ID)+"/members/"+agMemberID, nil, nil)
	member.expectError(404, "NOT_FOUND", http.MethodPost, path, docReferenceBody(node.ID, "member"))
	for _, payload := range []any{nil, `{}`, fmt.Sprintf(`{"type":"DOC","id":%q,"title":"copied"}`, node.ID), fmt.Sprintf(`{"type":"TASK","id":%q}`, node.ID), `{"type":"DOC","id":"bad"}`} {
		if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.messages SET payload=$2::jsonb WHERE id=$1::uuid`, id, payload); err == nil {
			t.Fatalf("invalid payload %v", payload)
		}
	}
	e.exec(`UPDATE openfox.conversations SET lifecycle='COMPLETED' WHERE id=$1::uuid`, conv.ID)
	owner.expectError(409, "CONVERSATION_CLOSED", http.MethodPost, path, docReferenceBody(node.ID, "same"))
}

func TestDocReferencesPassiveRuntimeAndHistory(t *testing.T)    { testPassiveReferences(t, true) }
func TestDocReferencesAssistantCrewAndCrossTenant(t *testing.T) { testAssistantCrewReferences(t, true) }
