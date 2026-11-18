package stage3

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"strings"
	"testing"
)

func TestAnchorResolutionNativeSnapshotsAndRequestBinding(t *testing.T) {
	h := newProposalEnv(t)
	e := h.e
	owner := e.owner()
	node := e.docNodeIn(owner, e.space.ID, docPersonal(), "", docs.KindDoc, "定位源")
	old := "前🙂内容后"
	next := "新增\n" + old
	e.exec(`UPDATE openfox.doc_nodes SET body=$2,version=version+1 WHERE id=$1`, node.ID, old)
	var version int64
	e.scalar(&version, `SELECT version FROM openfox.doc_nodes WHERE id=$1`, node.ID)
	file := artifactreview.Input{ID: s3NewID(t), NodeID: node.ID, SpaceID: e.space.ID, NodeVersion: version, SHA256: artifactreview.Digest(old), Comments: []artifactreview.Snapshot{{ThreadID: s3NewID(t), Body: "改这里", Anchor: artifactreview.Anchor{Kind: "text", Start: len("前"), End: len("前🙂内容"), Quote: "🙂内容"}}}}
	id := s3NewID(t)
	path := "conversations/" + h.conversation + "/comments/" + id
	owner.must(200, "PUT", path, artifactreview.CommentSource{Name: "源.md", File: &file}, nil)
	e.exec(`UPDATE openfox.doc_nodes SET body=$2,version=version+1 WHERE id=$1`, node.ID, next)
	rid := s3NewID(t)
	resolve := path + "/resolutions/" + rid
	request := artifactreview.ResolveRequest{Version: 1}
	var result artifactreview.AnchorResolution
	statuses := parallel(2, func(int) int { return owner.do("PUT", resolve, request).Status })
	if countStatus(statuses, 200) != 2 {
		t.Fatal(statuses)
	}
	owner.must(200, "PUT", resolve, request, &result)
	if result.Status != "exact" || result.Target.File.NodeVersion != version+1 || result.Anchor.Start != len("新增\n前") {
		t.Fatalf("%+v", result)
	}
	if e.count(`SELECT count(*) FROM openfox.workspace_anchor_resolutions WHERE thread_id=$1`, id) != 1 {
		t.Fatal("duplicate resolution")
	}
	var thread artifactreview.CommentThread
	owner.must(200, "GET", path, nil, &thread)
	if thread.Version != 1 || thread.Source.File.NodeVersion != version || thread.Source.File.Comments[0].Anchor.Start != len("前") {
		t.Fatal("original rewritten")
	}
	// An exact derived reference is validated against its immutable server result.
	target := *result.Target.File
	target.ID = s3NewID(t)
	target.Comments[0].Discussion = &conversations.CommentReference{ID: id, Version: 1, CommentID: id, ResolutionID: rid}
	body := map[string]any{"body": "修改当前位置", "client_message_id": s3NewID(t), "file_contexts": []artifactreview.Input{target}}
	owner.must(201, "POST", "conversations/"+h.conversation+"/messages", body, nil)
	validTarget := target
	validTarget.Comments = append([]artifactreview.Snapshot(nil), target.Comments...)
	target.Comments[0].Anchor.Start = 0
	target.Comments[0].Anchor.End = len("新增")
	target.Comments[0].Anchor.Quote = "新增"
	body["file_contexts"] = []artifactreview.Input{target}
	body["client_message_id"] = s3NewID(t)
	owner.must(409, "POST", "conversations/"+h.conversation+"/messages", body, nil)
	owner.must(409, "PUT", resolve, artifactreview.ResolveRequest{Version: 2}, nil)
	e.admin().must(404, "PUT", resolve, request, nil)
	if r := owner.doIn(e.otherOrg, "PUT", resolve, request); r.Status < 400 {
		t.Fatal("tenant leak")
	}
	// Existing receipt is stable after the current document changes again.
	e.exec(`UPDATE openfox.doc_nodes SET body='gone',version=version+1 WHERE id=$1`, node.ID)
	var replay artifactreview.AnchorResolution
	owner.must(200, "PUT", resolve, request, &replay)
	if replay.Target.File.SHA256 != artifactreview.Digest(next) {
		t.Fatal("receipt changed")
	}
	replay = artifactreview.AnchorResolution{}
	owner.must(200, "PUT", path+"/resolutions/"+s3NewID(t), request, &replay)
	if replay.Status != "orphaned" || replay.Target != nil {
		t.Fatal("lost location became exact")
	}
	body["file_contexts"] = []artifactreview.Input{validTarget}
	body["client_message_id"] = s3NewID(t)
	owner.must(409, "POST", "conversations/"+h.conversation+"/messages", body, nil)
	invalid := validTarget
	invalid.NodeVersion = version + 2
	invalid.SHA256 = artifactreview.Digest("gone")
	invalid.Comments = append([]artifactreview.Snapshot(nil), validTarget.Comments...)
	invalid.Comments[0].Anchor = artifactreview.Anchor{Kind: "text", Start: 0, End: 4, Quote: "gone"}
	invalid.Comments[0].Discussion = &conversations.CommentReference{ID: id, Version: 1, CommentID: id, ResolutionID: replay.ID}
	body["file_contexts"] = []artifactreview.Input{invalid}
	body["client_message_id"] = s3NewID(t)
	owner.must(409, "POST", "conversations/"+h.conversation+"/messages", body, nil)

	if err := h.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.workspace_anchor_resolutions SET snapshot='{}' WHERE id=$1`, rid)
		return err
	}); err == nil {
		t.Fatal("resolution mutable")
	}
}

func TestAnchorResolutionArtifactsAndBrowserRebind(t *testing.T) {
	h := newProposalEnv(t)
	e := h.e
	owner := e.owner()
	add := func(body, parent string) string {
		id := s3NewID(t)
		key := e.org + "/" + h.run + "/" + id
		f, err := h.storage.Put(e.ctx, key, strings.NewReader(body), 10<<20)
		if err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,parent_artifact_id) VALUES($1,$2,$3,'source.md',$4,$5,$6,'text/markdown',NULLIF($7,'')::uuid)`, e.org, id, h.run, key, f.Size, f.SHA256, parent)
		return id
	}
	first := add("first target end", "")
	file := artifactreview.Input{ID: s3NewID(t), ArtifactID: first, SHA256: artifactreview.Digest("first target end"), Comments: []artifactreview.Snapshot{{ThreadID: s3NewID(t), Body: "review", Anchor: artifactreview.Anchor{Kind: "text", Start: 6, End: 12, Quote: "target"}}}}
	id := s3NewID(t)
	path := "conversations/" + h.conversation + "/comments/" + id
	owner.must(200, "PUT", path, artifactreview.CommentSource{Name: "source.md", File: &file}, nil)
	head := add("prefix first target end", first)
	var r artifactreview.AnchorResolution
	owner.must(200, "PUT", path+"/resolutions/"+s3NewID(t), artifactreview.ResolveRequest{Version: 1}, &r)
	if r.Status != "exact" || r.Target.File.ArtifactID != head || r.Anchor.Start != 13 {
		t.Fatalf("%+v", r)
	}
	e.exec(`UPDATE openfox.conversations SET visibility='SHARED' WHERE id=$1`, h.conversation)
	e.addSpaceMember(e.space.ID, s3AdminID, "MEMBER")
	e.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id) VALUES($1,$2,$3)`, e.org, h.conversation, s3AdminID)
	e.admin().must(403, "PUT", path+"/resolutions/"+s3NewID(t), artifactreview.ResolveRequest{Version: 1}, nil)
	b := conversations.BrowserContext{ID: s3NewID(t), SourceURL: "https://example.com/a", Title: "A", CapturedAt: "2026-11-17T00:00:00Z", Comments: []conversations.BrowserComment{{ID: s3NewID(t), Body: "review", Anchor: conversations.BrowserAnchor{Kind: "element", Live: conversations.BrowserLive{HostInstanceID: s3NewID(t), TabID: s3NewID(t), FrameID: "main", DocumentGeneration: 1, Ref: s3NewID(t)}, Visual: conversations.BrowserVisual{Width: 800, Height: 600, Rect: conversations.BrowserRect{Width: 100, Height: 50}}}}}}
	bid := s3NewID(t)
	bp := "conversations/" + h.conversation + "/comments/" + bid
	owner.must(200, "PUT", bp, artifactreview.CommentSource{Name: "A", Browser: &b}, nil)
	b.Comments[0].Anchor.Live.DocumentGeneration = 2
	b.Comments[0].Anchor.Live.Ref = s3NewID(t)
	b.SourceURL = "https://example.com/b?private=1"
	image := mustUpload(t, owner, "conversations/"+h.conversation+"/attachments", "new.png", testPNG)
	b.Comments[0].ScreenshotAttachmentID = image.ID
	request := artifactreview.ResolveRequest{Version: 1, Browser: &artifactreview.CommentSource{Name: "B", Browser: &b}}
	rid := s3NewID(t)
	owner.must(200, "PUT", bp+"/resolutions/"+rid, request, &r)
	owner.must(200, "PUT", bp+"/resolutions/"+rid, request, nil)
	if r.Status != "exact" || r.Reason != "explicit_rebind" || r.Target.Browser.SourceURL != "https://example.com/b" {
		t.Fatal(r)
	}
	var original artifactreview.CommentThread
	owner.must(200, "GET", bp, nil, &original)
	if original.Source.Browser.Comments[0].Anchor.Live.DocumentGeneration != 1 {
		t.Fatal("original live overwritten")
	}
	owner.must(403, "DELETE", "attachments/"+image.ID, nil, nil)
}
