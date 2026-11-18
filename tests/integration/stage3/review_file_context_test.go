package stage3

import (
	"net/http"
	"openfox/internal/artifactreview"
	"openfox/internal/docs"
	"testing"
)

func TestReviewFileContextsValidateBeforeEnqueueAndReplay(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space.ID
	agent := env.insertAgent(space)
	env.configureAgent(agent)
	var conversation struct {
		ID string `json:"id"`
	}
	owner.must(201, http.MethodPost, "conversations", map[string]any{"space_id": space, "agent_id": agent}, &conversation)
	node := env.docNodeIn(owner, space, docPersonal(), "", docs.KindDoc, "锚点回归")
	env.exec(`UPDATE openfox.doc_nodes SET body='abcdef',version=version+1 WHERE id=$1::uuid`, node.ID)
	var version int64
	env.scalar(&version, `SELECT version FROM openfox.doc_nodes WHERE id=$1::uuid`, node.ID)
	input := artifactreview.Input{ID: s3NewID(t), NodeID: node.ID, SpaceID: space, NodeVersion: version, SHA256: artifactreview.Digest("abcdef"), Comments: []artifactreview.Snapshot{{ThreadID: s3NewID(t), Body: "改成大写", Anchor: artifactreview.Anchor{Kind: "text", Start: 0, End: 1, Quote: "错"}}}}
	path := "conversations/" + conversation.ID + "/messages"
	message := map[string]any{"body": "处理批注", "client_message_id": s3NewID(t), "mentions": []map[string]string{{"agent_id": agent}}, "file_contexts": []artifactreview.Input{input}}
	owner.expectError(400, "INVALID_ANNOTATION", http.MethodPost, path, message)
	if count := env.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid`, conversation.ID); count != 0 {
		t.Fatal("无效锚点已经入队")
	}
	input.Comments[0].Anchor.Quote = "a"
	message["file_contexts"] = []artifactreview.Input{input}
	var first, second struct {
		ID string `json:"id"`
	}
	owner.must(201, http.MethodPost, path, message, &first)
	env.exec(`UPDATE openfox.doc_nodes SET body='changed',version=version+1 WHERE id=$1::uuid`, node.ID)
	owner.must(201, http.MethodPost, path, message, &second)
	if first.ID != second.ID {
		t.Fatal("响应丢失重试重复发消息")
	}
	message["client_message_id"] = s3NewID(t)
	owner.expectError(409, "REVIEW_CONFLICT", http.MethodPost, path, message)
}
