package stage3

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/directchat"
	"openfox/internal/executiondelivery"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func dmChat(t *testing.T, c *s3Client, peer string) directchat.Chat {
	t.Helper()
	var chat directchat.Chat
	c.must(200, "POST", "direct-chats", map[string]any{"user_id": peer}, &chat)
	return chat
}
func dmSend(t *testing.T, c *s3Client, chat string, i int, body string, files ...string) directchat.Message {
	t.Helper()
	var m directchat.Message
	c.must(200, "POST", "direct-chats/"+chat+"/messages", map[string]any{"client_message_id": fmt.Sprintf("b0000000-0000-0000-0000-%012d", i), "body": body, "attachment_ids": files}, &m)
	return m
}
func TestDirectChatPrivacyReplayPagingAndRead(t *testing.T) {
	env := newS3Env(t)
	a, b := env.manager(), env.member()
	chat := dmChat(t, a, s3MemberID)
	base := "direct-chats/" + chat.ID
	if reverse := dmChat(t, b, s3ManagerID); reverse.ID != chat.ID {
		t.Fatal("reverse entry created another conversation")
	}
	a.expectError(400, "DIRECT_CHAT_SELF", "POST", "direct-chats", map[string]any{"user_id": s3ManagerID})
	a.expectError(400, "INVALID_REQUEST", "POST", "direct-chats", map[string]any{"user_id": "invalid"})
	a.expectError(404, "NOT_FOUND", "POST", "direct-chats", map[string]any{"user_id": s3OutsiderID})
	env.owner().expectError(404, "NOT_FOUND", "GET", base, nil)
	env.other().expectError(404, "NOT_FOUND", "GET", base+"/messages", nil)
	env.outsider().expectError(404, "NOT_FOUND", "GET", base, nil)
	empty := map[string]any{"body": "  ", "client_message_id": "b0000000-0000-0000-0000-000000000001"}
	a.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", empty)
	first := dmSend(t, a, chat.ID, 1, "你好，真人消息")
	replay := dmSend(t, a, chat.ID, 1, "你好，真人消息")
	if first.ID != replay.ID || first.Seq != 1 {
		t.Fatal("replay duplicated")
	}
	a.expectError(409, "IDEMPOTENCY_CONFLICT", "POST", base+"/messages", map[string]any{"body": "changed", "client_message_id": "b0000000-0000-0000-0000-000000000001"})
	var c directchat.Chat
	b.must(200, "GET", base, nil, &c)
	if c.UnreadCount != 1 {
		t.Fatalf("unread=%d", c.UnreadCount)
	}
	var page struct {
		Messages []directchat.Message `json:"messages"`
		Next     int64                `json:"next_before"`
	}
	b.must(200, "GET", base+"/messages", nil, &page)
	if len(page.Messages) != 1 || page.Messages[0].Body != "你好，真人消息" {
		t.Fatalf("messages=%+v", page)
	}
	dmSend(t, b, chat.ID, 1, "回复")
	b.must(200, "GET", base, nil, &c)
	if c.UnreadCount != 1 {
		t.Fatal("own send must not mark unread incoming messages as read")
	}
	b.must(204, "POST", base+"/read", map[string]any{"seq": 999}, nil)
	b.must(204, "POST", base+"/read", map[string]any{"seq": 0}, nil)
	b.must(200, "GET", base, nil, &c)
	if c.UnreadCount != 0 || c.ReadSeq != 2 {
		t.Fatalf("read=%+v", c)
	}
	for i := 2; i <= 54; i++ {
		dmSend(t, a, chat.ID, i, fmt.Sprintf("message %d", i))
	}
	b.must(200, "GET", base+"/messages", nil, &page)
	if len(page.Messages) != 50 || page.Next != 6 || page.Messages[49].Seq != 55 {
		t.Fatalf("latest page=%+v", page)
	}
	b.must(200, "GET", base+"/messages?before=6", nil, &page)
	if len(page.Messages) != 5 || page.Next != 0 || page.Messages[0].Seq != 1 {
		t.Fatalf("older=%+v", page)
	}
	a.expectError(400, "INVALID_REQUEST", "GET", base+"/messages?before=-1", nil)
	a.expectError(400, "INVALID_REQUEST", "GET", "direct-chats?limit=101", nil)
	a.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", map[string]any{"body": strings.Repeat("a", 65537), "client_message_id": "b0000000-0000-0000-0000-000000000099"})
	if n := env.count(`SELECT count(*) FROM openfox.runs WHERE organization_id=$1::uuid`, env.org); n != 0 {
		t.Fatal("human message dispatched Agent")
	}
	if n := env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id=$1::uuid AND type='direct_chat.changed' AND (recipient_user_id NOT IN ($2::uuid,$3::uuid) OR recipient_user_id IS NULL OR payload::text LIKE '%你好%')`, env.org, s3ManagerID, s3MemberID); n != 0 {
		t.Fatal("event leaked content or recipient")
	}
	env.exec(`UPDATE openfox.organization_members SET status='REVOKED' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, env.org, s3MemberID)
	a.expectError(404, "NOT_FOUND", "GET", base, nil)
	a.expectError(404, "NOT_FOUND", "POST", base+"/messages", map[string]any{"body": "after revoke", "client_message_id": "b0000000-0000-0000-0000-000000000099"})
}

// An inbox mute leaves the direct chat itself and the independent desktop feed intact.
func TestDirectChatInboxDelivery(t *testing.T) {
	env := newS3Env(t)
	sender, recipient := env.manager(), env.member()
	c := dmChat(t, sender, s3MemberID)
	list := func(who *s3Client, q string) []conversations.InboxGroup {
		t.Helper()
		var page conversations.InboxPage
		who.must(200, "GET", "inbox?"+q, nil, &page)
		return page.Groups
	}
	first := dmSend(t, sender, c.ID, 1, "first private body")
	if got := list(recipient, ""); len(got) != 0 {
		t.Fatalf("default inbox: %+v", got)
	}
	if got := list(recipient, "delivery=banner"); len(got) != 1 || got[0].DirectMessageID != first.ID || strings.Contains(got[0].SafeSummary, "private body") {
		t.Fatalf("banner: %+v", got)
	}
	recipient.must(200, "PATCH", "/v1/me/inbox-preferences", map[string]string{"key": "chat_message_received_recipient", "level": "important"}, nil)
	defer recipient.must(200, "DELETE", "/v1/me/inbox-preferences", nil, nil)
	second := dmSend(t, sender, c.ID, 2, "second private body")
	dmSend(t, sender, c.ID, 2, "second private body")
	if got := list(recipient, "importance=primary"); len(got) != 1 || got[0].Count != 1 || got[0].DirectChatID != c.ID || got[0].DirectMessageID != second.ID {
		t.Fatalf("inbox: %+v", got)
	}
	if len(list(sender, "")) != 0 || len(list(env.owner(), "")) != 0 {
		t.Fatal("direct alert leaked")
	}
	// An older client changing the channel must not opt in to private IM notifications.
	const path = "/v1/me/notification-channels/feishu"
	var ch struct {
		Direct bool `json:"direct_messages"`
	}
	replace := map[string]any{"enabled": true, "disabled_events": []string{}}
	recipient.must(200, "PUT", path, replace, &ch)
	if ch.Direct {
		t.Fatal("old client enabled IM")
	}
	replace["direct_messages"] = true
	recipient.must(200, "PUT", path, replace, &ch)
	if !ch.Direct {
		t.Fatal("explicit opt-in lost")
	}
	delete(replace, "direct_messages")
	recipient.must(200, "PUT", path, replace, &ch)
	if !ch.Direct {
		t.Fatal("old client erased explicit choice")
	}
	recipient.must(200, "DELETE", path, nil, &ch)
	if ch.Direct {
		t.Fatal("reset must disable private IM")
	}
	env.exec(`UPDATE openfox.organization_members SET status='REVOKED' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, env.org, s3ManagerID)
	if len(list(recipient, "")) != 0 || len(list(recipient, "delivery=banner")) != 0 {
		t.Fatal("revoked chat remained visible")
	}
}

func TestDirectChatConcurrentPairAndMessage(t *testing.T) {
	env := newS3Env(t)
	a, b := env.manager(), env.member()
	start := make(chan struct{})
	results := make(chan s3Response, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c, peer := a, s3MemberID
			if i%2 == 1 {
				c, peer = b, s3ManagerID
			}
			results <- c.do("POST", "direct-chats", map[string]any{"user_id": peer})
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var id string
	for r := range results {
		if r.Status != 200 {
			t.Fatalf("concurrent pair=%d %s", r.Status, r.Body)
		}
		var c directchat.Chat
		r.decode(t, &c)
		if id != "" && id != c.ID {
			t.Fatal("duplicate pair")
		}
		id = c.ID
	}
	results = make(chan s3Response, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.do("POST", "direct-chats/"+id+"/messages", map[string]any{"body": "same", "client_message_id": "b0000000-0000-0000-0000-000000000001"})
		}()
	}
	wg.Wait()
	close(results)
	var mid string
	for r := range results {
		if r.Status != 200 {
			t.Fatalf("concurrent send=%d %s", r.Status, r.Body)
		}
		var m directchat.Message
		r.decode(t, &m)
		if mid != "" && mid != m.ID {
			t.Fatal("duplicate message")
		}
		mid = m.ID
	}
	if n := env.count(`SELECT count(*) FROM openfox.direct_messages WHERE organization_id=$1::uuid`, env.org); n != 1 {
		t.Fatalf("messages=%d", n)
	}
}
func TestDirectChatAttachmentsAndIsolation(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	a, b := env.manager(), env.member()
	chat := dmChat(t, a, s3MemberID)
	base := "direct-chats/" + chat.ID
	f := mustUpload(t, a, base+"/attachments", "native.txt", []byte("ATTACHMENT_BYTES"))
	path := base + "/attachments/" + f.ID
	b.expectError(404, "NOT_FOUND", "GET", path, nil)
	env.owner().expectError(404, "NOT_FOUND", "GET", path, nil)
	b.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", map[string]any{"body": "steal pending", "client_message_id": "b0000000-0000-0000-0000-000000000001", "attachment_ids": []string{f.ID}})
	m := dmSend(t, a, chat.ID, 1, "", f.ID)
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "native.txt" {
		t.Fatalf("attachment=%+v", m)
	}
	r := b.must(200, "GET", path, nil, nil)
	if string(r.Body) != "ATTACHMENT_BYTES" {
		t.Fatalf("download=%s", r.Body)
	}
	a.expectError(403, "ACTION_FORBIDDEN", "DELETE", path, nil)
	replay := dmSend(t, a, chat.ID, 1, "", f.ID)
	if replay.ID != m.ID {
		t.Fatal("attachment retry duplicated")
	}
	a.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", map[string]any{"body": "reuse", "client_message_id": "b0000000-0000-0000-0000-000000000002", "attachment_ids": []string{f.ID}})
	pending := mustUpload(t, a, base+"/attachments", "remove.txt", []byte("REMOVE"))
	a.must(204, "DELETE", base+"/attachments/"+pending.ID, nil, nil)
	a.expectError(404, "NOT_FOUND", "GET", base+"/attachments/"+pending.ID, nil)
	otherChat := dmChat(t, a, s3OtherID)
	f2 := mustUpload(t, a, "direct-chats/"+otherChat.ID+"/attachments", "other.txt", []byte("OTHER"))
	a.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", map[string]any{"body": "wrong chat", "client_message_id": "b0000000-0000-0000-0000-000000000002", "attachment_ids": []string{f2.ID}})
	env.other().expectError(404, "NOT_FOUND", "GET", path, nil)
	resp := uploadFile(t, a, base+"/attachments", "unsafe.exe", []byte("MZ"))
	if resp.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("type check=%d %s", resp.Status, resp.Body)
	}
	for i := 0; i < 20; i++ {
		mustUpload(t, a, base+"/attachments", fmt.Sprintf("pending%d.txt", i), []byte("P"))
	}
	resp = uploadFile(t, a, base+"/attachments", "too-many.txt", []byte("P"))
	if resp.Status != 409 || resp.code() != "ATTACHMENT_PENDING_LIMIT" {
		t.Fatalf("pending limit=%d %s", resp.Status, resp.Body)
	}
}

func TestDirectChatRLSAndPrivateEvents(t *testing.T) {
	env := newS3Env(t)
	a := env.manager()
	chat := dmChat(t, a, s3MemberID)
	dmSend(t, a, chat.ID, 1, "private body")
	for _, user := range []string{s3ManagerID, s3MemberID, s3OwnerID, s3OtherID} {
		selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: user, AuthorizationVersion: 1}
		err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.direct_messages WHERE organization_id=$1::uuid`, env.org).Scan(&count); err != nil {
				return err
			}
			want := 0
			if user == s3ManagerID || user == s3MemberID {
				want = 1
			}
			if count != want {
				t.Errorf("%s RLS messages=%d want=%d", user, count, want)
			}
			page, err := (executiondelivery.Service{}).ListVisibleEvents(ctx, tx, env.org, conversations.Actor{UserID: user, Role: policy.OrganizationRole(s3Roles[user]), AuthorizationVersion: 1}, executiondelivery.StreamCursor{}, 100)
			if err != nil {
				return err
			}
			events := 0
			for _, event := range page.OrgEvents {
				if event.Type == "direct_chat.changed" {
					events++
				}
			}
			if events != want {
				t.Errorf("%s events=%d want=%d", user, events, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The server also enforces bounded upload bodies; an oversized attempt leaves no metadata/object.
	withAvatarStorage(env, t.TempDir())
	a = env.manager()
	r := uploadFile(t, a, "direct-chats/"+chat.ID+"/attachments", "large.txt", []byte(strings.Repeat("a", (20<<20)+1)))
	if r.Status != 413 {
		t.Fatalf("large upload=%d %s", r.Status, r.Body)
	}
	if n := env.count(`SELECT count(*) FROM openfox.direct_attachments WHERE organization_id=$1::uuid`, env.org); n != 0 {
		t.Fatal("rejected upload left metadata")
	}
}
