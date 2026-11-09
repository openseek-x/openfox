package stage3

import (
	"strconv"
	"testing"
	"time"
)

// A hit thousands of rows from the end must be available in one read.
// A valid message ID from another accessible chat must still not resolve here.
func TestSearchMessageWindow(t *testing.T) {
	e := newS3Env(t)
	a := e.owner()
	space := e.createSpace("SHARED")
	agent := e.insertAgent(space.ID)
	conv := s3NewID(t)
	e.exec(`INSERT INTO openfox.conversations (organization_id,id,space_id,agent_id,owner_user_id,next_message_seq)
	     VALUES ($1,$2,$3,$4,$5,2001)`, e.org, conv, space.ID, agent, s3OwnerID)
	e.exec(`INSERT INTO openfox.messages (organization_id,id,conversation_id,seq,author_kind,author_user_id,body)
		SELECT $1,gen_random_uuid(),$2,i,'USER',$3,'Text ' || i FROM generate_series(1,2000) i`, e.org, conv, s3OwnerID)
	// A full page and its count must reflect the same visible messages. Exercise
	// the authenticated HTTP path and actual snippets against a non-trivial history.
	start := time.Now()
	search := e.search(a, "Text", "types=message", "limit=50", "offset=0")
	group := search.Groups[0]
	if group.Total == nil || *group.Total != 2000 || len(group.Items) != 50 || group.Items[0].ConversationID != conv || group.Items[0].Subtitle == "" {
		t.Fatalf("unexpected history search: total=%v returned=%d", group.Total, len(group.Items))
	}
	t.Logf("authenticated message search, 2,000 visible matches with snippets and total: %s", time.Since(start))
	var center string
	e.scalar(&center, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1 AND seq=70`, conv)
	type page struct {
		Messages []struct {
			ID  string
			Seq int
		}
	}
	path := "conversations/" + conv + "/messages"
	var p page
	a.must(200, "GET", path+"?around="+center+"&limit=4", nil, &p)
	if len(p.Messages) != 4 || p.Messages[2].ID != center || p.Messages[0].Seq != 68 || p.Messages[3].Seq != 71 {
		t.Fatalf("unexpected anchored window: %v", p)
	}
	a.must(200, "GET", path+"?order=asc&after=71&limit=2", nil, &p)
	if len(p.Messages) != 2 || p.Messages[0].Seq != 72 {
		t.Fatalf("unexpected continuation: %v", p)
	}
	a.expectError(404, "NOT_FOUND", "GET", path+"?around="+s3NewID(t), nil)
	e.member().expectError(404, "NOT_FOUND", "GET", path+"?around="+center, nil)
	a.expectError(400, "INVALID_REQUEST", "GET", path+"?around="+center+"&after=1", nil)

	chat := dmChat(t, a, s3MemberID)
	directPath := "direct-chats/" + chat.ID + "/messages"
	var mid string
	for i := 1; i <= 8; i++ {
		m := dmSend(t, a, chat.ID, i, "DM"+strconv.Itoa(i))
		if i == 4 {
			mid = m.ID
		}
	}
	a.must(200, "GET", directPath+"?around="+mid+"&limit=4", nil, &p)
	if len(p.Messages) != 4 || p.Messages[2].ID != mid || p.Messages[0].Seq != 2 || p.Messages[3].Seq != 5 {
		t.Fatalf("unexpected direct window: %v", p)
	}
	a.must(200, "GET", directPath+"?after=5&limit=2", nil, &p)
	if len(p.Messages) != 2 || p.Messages[0].Seq != 6 || p.Messages[1].Seq != 7 {
		t.Fatalf("unexpected newer direct messages: %v", p)
	}
	e.manager().expectError(404, "NOT_FOUND", "GET", directPath+"?around="+mid, nil)
	a.expectError(404, "NOT_FOUND", "GET", directPath+"?around="+center, nil)
	a.expectError(404, "NOT_FOUND", "GET", path+"?around="+mid, nil)
}
