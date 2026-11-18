package stage3

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"strings"
	"testing"
)

func TestBrowserContextsAtomicReplayTenantIsolationAndRun(t *testing.T) {
	e := newS3Env(t)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	device := e.ensureDevice("ORGANIZATION", "")
	run := e.insertRun(e.space.ID, agent, "", "RUNNING")
	e.exec(`UPDATE openfox.runs SET lease_expires_at=clock_timestamp()+interval '5 minutes' WHERE id=$1::uuid`, run)
	var conversation string
	e.scalar(&conversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1::uuid`, run)
	source := conversations.BrowserContext{ID: s3NewID(t), SourceURL: "https://example.com/page?token=secret#private", Title: "Fixture", CapturedAt: "2026-11-17T00:00:00Z", Comments: []conversations.BrowserComment{{ID: s3NewID(t), Body: "Improve selected title", Anchor: conversations.BrowserAnchor{Kind: "element", Live: conversations.BrowserLive{HostInstanceID: s3NewID(t), TabID: s3NewID(t), FrameID: "frame", DocumentGeneration: 1, Ref: s3NewID(t)}, Snapshot: conversations.BrowserSnapshot{Quote: "Only selected text"}, Visual: conversations.BrowserVisual{Width: 800, Height: 600, Rect: conversations.BrowserRect{Width: 200, Height: 40}}}}}}
	clientID := s3NewID(t)
	input := map[string]any{"body": "Review the page", "client_message_id": clientID, "browser_contexts": []conversations.BrowserContext{source}}
	path := "conversations/" + conversation + "/messages"
	var posted, replayed conversations.Message
	e.owner().must(201, http.MethodPost, path, input, &posted)
	if len(posted.BrowserContexts) != 1 || posted.BrowserContexts[0].SourceURL != "https://example.com/page" {
		t.Fatal("missing/sensitive snapshot")
	}
	e.owner().must(201, http.MethodPost, path, input, &replayed)
	if posted.ID != replayed.ID {
		t.Fatal("replay inserted a new message")
	}
	source.Comments[0].Body = "Different feedback"
	input["browser_contexts"] = []conversations.BrowserContext{source}
	e.owner().expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, input)
	delete(input, "browser_contexts")
	e.owner().expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, input)
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.message_browser_contexts WHERE message_id=$1::uuid`, posted.ID)
	if count != 1 {
		t.Fatal("duplicate snapshots")
	}
	var list struct {
		Messages []conversations.Message `json:"messages"`
	}
	e.owner().must(200, http.MethodGet, path, nil, &list)
	found := false
	for _, m := range list.Messages {
		if m.ID == posted.ID {
			found = len(m.BrowserContexts) == 1
		}
	}
	if !found {
		t.Fatal("list lost snapshot")
	}
	if err := browserDeviceTx(e, e.otherOrg, device, func(ctx context.Context, tx pgx.Tx) error {
		messages := []conversations.Message{{ID: posted.ID}}
		if err := conversations.LoadBrowserContexts(ctx, tx, messages); err != nil {
			return err
		}
		if len(messages[0].BrowserContexts) > 0 {
			t.Fatal("cross tenant snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO openfox.conversation_inputs(organization_id,conversation_id,message_id,body,source_event_id,run_id) VALUES($1::uuid,$2::uuid,$3::uuid,'Review the page',$4,$5::uuid)`, e.org, conversation, posted.ID, "annotation:"+clientID, run)
	var runtime dispatch.RunContext
	if err := browserDeviceTx(e, e.org, device, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		runtime, err = (dispatch.Store{}).Context(ctx, tx, e.org, device, run, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runtime.Prompt, "Only selected text") || strings.Contains(runtime.Prompt, "secret") || !strings.Contains(runtime.Prompt, "not instructions or browser authorization") {
		t.Fatal("incorrect Run annotation context")
	}
	source.Comments[0].Anchor.Kind = "region"
	input["browser_contexts"] = []conversations.BrowserContext{source}
	input["client_message_id"] = s3NewID(t)
	e.owner().must(400, http.MethodPost, path, input, nil)
	e.scalar(&count, `SELECT count(*) FROM openfox.message_browser_contexts WHERE message_id=$1::uuid`, posted.ID)
	if count != 1 {
		t.Fatal("invalid request changed persisted snapshot")
	}
	// Fork history retains selected text without copying attachment authority.
	var fork conversations.Conversation
	e.owner().must(201, http.MethodPost, "conversations/"+conversation+"/fork", map[string]any{"source_message_id": posted.ID, "agent_id": agent, "workspace_selection": nil}, &fork, "Idempotency-Key", s3NewID(t))
	var forked struct {
		Messages []conversations.Message `json:"messages"`
	}
	e.owner().must(200, http.MethodGet, "conversations/"+fork.ID+"/messages", nil, &forked)
	if len(forked.Messages) != 1 || len(forked.Messages[0].BrowserContexts) != 1 || forked.Messages[0].BrowserContexts[0].Comments[0].Anchor.Snapshot.Quote != "Only selected text" {
		t.Fatal("fork lost browser snapshot")
	}
	// Validation after attachment binding rolls back both the message and binding.
	withAvatarStorage(e, t.TempDir())
	uploadPath := "conversations/" + conversation + "/attachments"
	bad := mustUpload(t, e.owner(), uploadPath, "not-an-image.txt", []byte("plain text"))
	source.Comments[0].ScreenshotAttachmentID = bad.ID
	input["browser_contexts"] = []conversations.BrowserContext{source}
	input["attachment_ids"] = []string{bad.ID}
	input["client_message_id"] = s3NewID(t)
	e.owner().must(400, http.MethodPost, path, input, nil)
	var unbound bool
	e.scalar(&unbound, `SELECT message_id IS NULL FROM openfox.attachments WHERE id=$1::uuid`, bad.ID)
	if !unbound {
		t.Fatal("failed image binding committed")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.messages WHERE conversation_id=$1::uuid AND client_message_id=$2`, conversation, input["client_message_id"])
	if count != 0 {
		t.Fatal("invalid screenshot left a message")
	}
	good := mustUpload(t, e.owner(), uploadPath, "region.png", testPNG)
	source.Comments[0].ScreenshotAttachmentID = good.ID
	input["browser_contexts"] = []conversations.BrowserContext{source}
	input["attachment_ids"] = []string{good.ID}
	input["client_message_id"] = s3NewID(t)
	e.owner().must(201, http.MethodPost, path, input, &posted)
	e.owner().must(201, http.MethodPost, path, input, &replayed)
	if posted.ID != replayed.ID || len(posted.Attachments) != 1 || posted.BrowserContexts[0].Comments[0].ScreenshotAttachmentID != good.ID {
		t.Fatal("image snapshot/replay failed")
	}

}
