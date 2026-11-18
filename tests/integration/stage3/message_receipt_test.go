package stage3

import (
	"net/http"
	"openfox/internal/conversations"
	"testing"
)

func TestMessageReceiptVisibilityAndReplay(t *testing.T) {
	e := newS3Env(t)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	run := e.insertRun(e.space.ID, agent, "", "RUNNING")
	var conversation string
	e.scalar(&conversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1::uuid`, run)
	key := s3NewID(t)
	path := "conversations/" + conversation
	var posted conversations.Message
	input := map[string]any{"body": "recover once", "client_message_id": key}
	e.owner().must(201, http.MethodPost, path+"/messages", input, &posted)
	var receipt struct {
		Message *conversations.Message `json:"message"`
	}
	e.owner().must(200, http.MethodGet, path+"/message-receipt?client_message_id="+key, nil, &receipt)
	if receipt.Message == nil || receipt.Message.ID != posted.ID {
		t.Fatal("missing receipt")
	}
	receipt.Message = nil
	e.owner().must(200, http.MethodGet, path+"/message-receipt?client_message_id=absent", nil, &receipt)
	if receipt.Message != nil {
		t.Fatal("unknown key exposed another message")
	}
	e.owner().must(400, http.MethodGet, path+"/message-receipt", nil, nil)
	// A visible conversation does not expose another author's receipt; private conversations remain hidden.
	e.admin().must(404, http.MethodGet, path+"/message-receipt?client_message_id="+key, nil, nil)
	e.exec(`UPDATE openfox.conversations SET visibility='SHARED' WHERE id=$1::uuid`, conversation)
	e.addSpaceMember(e.space.ID, s3AdminID, "MEMBER")
	e.exec(`INSERT INTO openfox.conversation_members(organization_id, conversation_id, user_id) VALUES($1,$2,$3)`, e.org, conversation, s3AdminID)
	e.admin().must(200, http.MethodGet, path+"/message-receipt?client_message_id="+key, nil, &receipt)
	if receipt.Message != nil {
		t.Fatal("other author's receipt leaked")
	}
	if r := e.owner().doIn(e.otherOrg, http.MethodGet, path+"/message-receipt?client_message_id="+key, nil); r.Status < 400 {
		t.Fatal("cross tenant lookup allowed")
	}
	// Closed sessions still confirm already accepted sends.
	e.exec(`UPDATE openfox.conversations SET lifecycle='COMPLETED' WHERE id=$1::uuid`, conversation)
	e.owner().must(200, http.MethodGet, path+"/message-receipt?client_message_id="+key, nil, &receipt)
	if receipt.Message == nil || receipt.Message.ID != posted.ID {
		t.Fatal("closed session lost receipt")
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.messages WHERE conversation_id=$1::uuid AND client_message_id=$2`, conversation, key)
	if count != 1 {
		t.Fatal("lookup inserted a message")
	}
}
