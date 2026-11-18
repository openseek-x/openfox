package stage3

import (
	"net/http"
	"openfox/internal/artifactreview"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"testing"
)

func TestWorkspaceCommentsIndependentVersionedSnapshots(t *testing.T) {
	e := newS3Env(t)
	owner := e.owner()
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	var conversation struct {
		ID string `json:"id"`
	}
	owner.must(201, "POST", "conversations", map[string]any{"space_id": e.space.ID, "agent_id": agent}, &conversation)
	node := e.docNodeIn(owner, e.space.ID, docPersonal(), "", docs.KindDoc, "评论源")
	e.exec(`UPDATE openfox.doc_nodes SET body='abcdef',version=version+1 WHERE id=$1`, node.ID)
	var version int64
	e.scalar(&version, `SELECT version FROM openfox.doc_nodes WHERE id=$1`, node.ID)
	file := artifactreview.Input{ID: s3NewID(t), NodeID: node.ID, SpaceID: e.space.ID, NodeVersion: version, SHA256: artifactreview.Digest("abcdef"), Comments: []artifactreview.Snapshot{{ThreadID: s3NewID(t), Body: "原始讨论", Anchor: artifactreview.Anchor{Kind: "text", Start: 0, End: 3, Quote: "abc"}}}}
	source := artifactreview.CommentSource{Name: "评论源.md", File: &file}
	id := s3NewID(t)
	path := "conversations/" + conversation.ID + "/comments/" + id
	var thread, replayed artifactreview.CommentThread
	owner.must(200, "PUT", path, source, &thread)
	owner.must(200, "PUT", path, source, &replayed)
	if thread.Version != 1 || replayed.Version != 1 {
		t.Fatal("creation replay changed revision")
	}
	edit := artifactreview.CommentMutation{OperationID: s3NewID(t), Version: 1, Action: "edit", CommentID: id, Body: "后来编辑"}
	owner.must(200, "PATCH", path, edit, &thread)
	owner.must(200, "PATCH", path, edit, &replayed)
	if thread.Version != 2 || replayed.Version != 2 {
		t.Fatal("mutation replay duplicated")
	}
	owner.must(409, "PATCH", path, artifactreview.CommentMutation{OperationID: edit.OperationID, Version: 1, Action: "resolve"}, nil)
	// Overlapping ordinary threads are independent and never queue a model.
	owner.must(200, "PUT", "conversations/"+conversation.ID+"/comments/"+s3NewID(t), source, nil)
	for _, table := range []string{"messages", "conversation_inputs", "runs"} {
		if e.count(`SELECT count(*) FROM openfox.`+table+` WHERE conversation_id=$1`, conversation.ID) != 0 {
			t.Fatal("discussion started chat work:", table)
		}
	}
	ids := []string{s3NewID(t), s3NewID(t)}
	results := parallel(2, func(i int) int {
		return owner.do("PATCH", path, artifactreview.CommentMutation{OperationID: ids[i], Version: 2, Action: "reply", Body: "回复"}).Status
	})
	if countStatus(results, 200) != 1 || countStatus(results, 409) != 1 {
		t.Fatal("lost update:", results)
	}
	// An explicitly selected old revision remains exactly what the user selected.
	input := file
	input.ID = s3NewID(t)
	input.Comments = append([]artifactreview.Snapshot(nil), file.Comments...)
	input.Comments[0].Discussion = &conversations.CommentReference{ID: id, Version: 1, CommentID: id}
	request := map[string]any{"body": "处理已选评论", "client_message_id": s3NewID(t), "file_contexts": []artifactreview.Input{input}}
	var message conversations.Message
	messagePath := "conversations/" + conversation.ID + "/messages"
	owner.must(201, "POST", messagePath, request, &message)
	var snapshot string
	e.scalar(&snapshot, `SELECT input_snapshot->'comments'->0->>'body' FROM openfox.message_file_contexts WHERE message_id=$1`, message.ID)
	if snapshot != "原始讨论" {
		t.Fatal("in-flight snapshot followed edits")
	}
	owner.must(200, "GET", path, nil, &thread)
	if thread.Status != "OPEN" {
		t.Fatal("sending auto-resolved discussion")
	}
	// Mutating a request's declared snapshot must roll back the whole message.
	input.Comments[0].Body = "伪造的快照"
	input.ID = s3NewID(t)
	request["file_contexts"] = []artifactreview.Input{input}
	request["client_message_id"] = s3NewID(t)
	owner.must(409, "POST", messagePath, request, nil)
	if e.count(`SELECT count(*) FROM openfox.messages WHERE conversation_id=$1`, conversation.ID) != 1 {
		t.Fatal("invalid snapshot committed message")
	}
	owner.must(200, "PATCH", path, artifactreview.CommentMutation{OperationID: s3NewID(t), Version: thread.Version, Action: "resolve"}, &thread)
	owner.must(200, "PATCH", path, artifactreview.CommentMutation{OperationID: s3NewID(t), Version: thread.Version, Action: "reopen"}, &thread)
	e.admin().must(404, "GET", path, nil, nil)
	if r := owner.doIn(e.otherOrg, "GET", path, nil); r.Status < 400 {
		t.Fatal("cross tenant disclosure")
	}
	// Document updates do not destroy the discussion's immutable captured source.
	e.exec(`UPDATE openfox.doc_nodes SET body='new document',version=version+1 WHERE id=$1`, node.ID)
	owner.must(200, "GET", path, nil, &thread)
	owner.must(200, "PUT", path, source, &replayed)
	if replayed.Version != 1 {
		t.Fatal("creation replay returned mutable latest")
	}
	var page artifactreview.CommentPage
	owner.must(200, "GET", "conversations/"+conversation.ID+"/comments", nil, &page)
	if len(page.Threads) != 2 {
		t.Fatal("refresh lost threads")
	}
}

func TestWorkspaceCommentBrowserScreenshotAndPermissions(t *testing.T) {
	e := newS3Env(t)
	withAvatarStorage(e, t.TempDir())
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	var conversation struct {
		ID string `json:"id"`
	}
	e.owner().must(201, "POST", "conversations", map[string]any{"space_id": e.space.ID, "agent_id": agent}, &conversation)
	base := "conversations/" + conversation.ID
	id := s3NewID(t)
	path := base + "/comments/" + id
	image := mustUpload(t, e.owner(), base+"/attachments", "region.png", testPNG)
	b := conversations.BrowserContext{ID: s3NewID(t), SourceURL: "https://example.com/page?token=private#fragment", Title: "Page", CapturedAt: "2026-11-17T00:00:00Z", Comments: []conversations.BrowserComment{{ID: s3NewID(t), Body: "检查区域", ScreenshotAttachmentID: image.ID, Anchor: conversations.BrowserAnchor{Kind: "region", Live: conversations.BrowserLive{HostInstanceID: s3NewID(t), TabID: s3NewID(t), FrameID: "main", DocumentGeneration: 1, Ref: s3NewID(t)}, Visual: conversations.BrowserVisual{Width: 800, Height: 600, Rect: conversations.BrowserRect{Width: 100, Height: 50}}}}}}
	var thread artifactreview.CommentThread
	e.owner().must(200, "PUT", path, artifactreview.CommentSource{Name: "Page", Browser: &b}, &thread)
	if thread.Source.Browser.SourceURL != "https://example.com/page" {
		t.Fatal("saved sensitive URL query")
	}
	e.owner().must(403, "DELETE", "attachments/"+image.ID, nil, nil)
	e.exec(`UPDATE openfox.conversations SET visibility='SHARED' WHERE id=$1`, conversation.ID)
	e.addSpaceMember(e.space.ID, s3AdminID, "MEMBER")
	e.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id) VALUES($1,$2,$3)`, e.org, conversation.ID, s3AdminID)
	e.admin().must(200, "GET", path, nil, &thread)
	e.admin().must(200, "GET", "attachments/"+image.ID, nil, nil)
	e.admin().must(403, "PATCH", path, artifactreview.CommentMutation{OperationID: s3NewID(t), Version: 1, Action: "reply", Body: "viewer"}, nil)
	e.exec(`UPDATE openfox.conversation_members SET access_level='COMMENTER' WHERE conversation_id=$1 AND user_id=$2`, conversation.ID, s3AdminID)
	e.admin().must(403, "PATCH", path, artifactreview.CommentMutation{OperationID: s3NewID(t), Version: 1, Action: "edit", CommentID: id, Body: "改别人的评论"}, nil)
	// The screenshot is copied into a new message upload; the saved image remains reusable.
	copy := mustUpload(t, e.owner(), base+"/attachments", "region.png", testPNG)
	selected := *thread.Source.Browser
	selected.Comments = append([]conversations.BrowserComment(nil), selected.Comments...)
	selected.Comments[0].ScreenshotAttachmentID = copy.ID
	selected.Comments[0].Discussion = &conversations.CommentReference{ID: id, Version: 1, CommentID: id}
	var msg conversations.Message
	request := map[string]any{"body": "处理区域", "client_message_id": s3NewID(t), "browser_contexts": []conversations.BrowserContext{selected}, "attachment_ids": []string{copy.ID}}
	e.owner().must(201, http.MethodPost, base+"/messages", request, &msg)
	e.owner().must(201, http.MethodPost, base+"/messages", request, nil)
	var unbound bool
	e.scalar(&unbound, `SELECT message_id IS NULL AND comment_thread_id=$2::uuid FROM openfox.attachments WHERE id=$1`, image.ID, id)
	if !unbound {
		t.Fatal("message stole discussion screenshot")
	}
	wrong := mustUpload(t, e.owner(), base+"/attachments", "wrong.png", append(append([]byte(nil), testPNG...), 2))
	selected.Comments[0].ScreenshotAttachmentID = wrong.ID
	request["browser_contexts"] = []conversations.BrowserContext{selected}
	request["attachment_ids"] = []string{wrong.ID}
	request["client_message_id"] = s3NewID(t)
	e.owner().must(409, "POST", base+"/messages", request, nil)
	if e.count(`SELECT count(*) FROM openfox.messages WHERE conversation_id=$1`, conversation.ID) != 1 {
		t.Fatal("forged image started message")
	}
	// Another conversation cannot submit or read the selected discussion version.
	var other struct {
		ID string `json:"id"`
	}
	e.owner().must(201, "POST", "conversations", map[string]any{"space_id": e.space.ID, "agent_id": agent}, &other)
	e.owner().must(404, "GET", "conversations/"+other.ID+"/comments/"+id, nil, nil)
	request["client_message_id"] = s3NewID(t)
	e.owner().must(404, "POST", "conversations/"+other.ID+"/messages", request, nil)
	e.exec(`DELETE FROM openfox.conversation_members WHERE conversation_id=$1 AND user_id=$2`, conversation.ID, s3AdminID)
	e.admin().must(404, "GET", path, nil, nil)
	e.admin().must(404, "GET", "attachments/"+image.ID, nil, nil)
}
