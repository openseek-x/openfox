package stage12g

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestSingleMessageSnapshotAndRecover(t *testing.T) {
	s := newShareEnv(t)
	var first string
	s.scalar(&first, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1::uuid ORDER BY seq LIMIT 1`, s.conversation)
	var selected struct {
		ID string `json:"id"`
	}
	s.member().must(201, http.MethodPost, "conversations/"+s.conversation+"/messages", map[string]any{"body": "selected 中文🙂", "reply_to_id": first, "client_message_id": qcNewID(t)}, &selected)
	s.exec(`UPDATE openfox.conversations SET title='unselected context title' WHERE id=$1::uuid`, s.conversation)
	body := s.convBody("ORGANIZATION")
	body["message_id"] = selected.ID
	out := s.create(s.member(), body)
	var view viewBody
	s.member().must(200, http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": out.Token}, &view)
	if len(view.Items) != 1 || view.Items[0].Body != "selected 中文🙂" || view.Items[0].ReplyTo != 0 || view.Title != "selected 中文🙂" {
		t.Fatalf("single-message scope: %+v", view)
	}
	var recovered created
	r := s.member().must(200, http.MethodGet, "share-snapshots/"+strings.ToUpper(out.ID)+"/link", nil, &recovered)
	if recovered.Token != out.Token || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("recovery must retain token and prevent cache")
	}
	for _, c := range []*qcClient{s.owner(), s.client(qcPeerID), s.outsider()} {
		c.expectError(404, "", http.MethodGet, "share-snapshots/"+out.ID+"/link", nil)
	}
	var stored []byte
	s.scalar(&stored, `SELECT token_ciphertext FROM openfox.share_snapshots WHERE id=$1::uuid`, out.ID)
	if bytes.Contains(stored, []byte(out.Token)) || len(stored) != 59 {
		t.Fatal("token must be encrypted")
	}
	body["message_id"] = qcNewID(t)
	s.member().expectError(404, "NOT_FOUND", http.MethodPost, "share-snapshots", body)
	// Agent text is a controlled local fixture; no Runtime or external account is contacted.
	agentMessage := qcNewID(t)
	s.exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_agent_id,kind,body)
 VALUES($1::uuid,$2::uuid,$3::uuid,99,'AGENT',$4::uuid,'TEXT','Agent selected')`, s.org, agentMessage, s.conversation, s.agent)
	body["message_id"] = agentMessage
	agentOut := s.create(s.member(), body)
	s.member().must(200, http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": agentOut.Token}, &view)
	if len(view.Items) != 1 || view.Items[0].Body != "Agent selected" {
		t.Fatal("agent source mismatch")
	}
	// The snapshot and its recovery are independent of later source edits/deletion.
	s.exec(`DELETE FROM openfox.messages WHERE conversation_id=$1::uuid`, s.conversation)
	s.member().must(200, http.MethodGet, "share-snapshots/"+out.ID+"/link", nil, &recovered)
	s.member().must(200, http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": recovered.Token}, &view)
	if view.Items[0].Body != "selected 中文🙂" {
		t.Fatal("snapshot mutated")
	}
	s.member().must(200, http.MethodDelete, "share-snapshots/"+out.ID, nil, nil)
	s.member().expectError(404, "NOT_FOUND", http.MethodGet, "share-snapshots/"+out.ID+"/link", nil)
}

func TestShareCreateIdempotencyAndRecoveryLifetime(t *testing.T) {
	s := newShareEnv(t)
	c := s.member()
	body := s.convBody("ORGANIZATION")
	key := qcNewID(t)
	const n = 8
	results := make([]qcResponse, n)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = c.do(http.MethodPost, "share-snapshots", body, "Idempotency-Key", key)
		}(i)
	}
	wg.Wait()
	var initial created
	createdCount := 0
	for _, r := range results {
		var got created
		r.decode(t, &got)
		if r.Status == 201 {
			createdCount++
		} else if r.Status != 200 || r.Header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("unexpected replay %d %s", r.Status, r.Body)
		}
		if initial.ID == "" {
			initial = got
		}
		if got.ID != initial.ID || got.Token != initial.Token {
			t.Fatal("parallel retries changed resource or token")
		}
	}
	if createdCount != 1 || s.count(`SELECT count(*) FROM openfox.share_snapshots WHERE organization_id=$1::uuid`, s.org) != 1 {
		t.Fatal("duplicate snapshot")
	}
	if s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1::uuid AND action='share_snapshot.created'`, s.org) != 1 {
		t.Fatal("duplicate create audit")
	}
	body["title"] = "different"
	c.expectError(409, "IDEMPOTENCY_CONFLICT", http.MethodPost, "share-snapshots", body, "Idempotency-Key", key)
	delete(body, "title")
	// Replay must precede fresh quota/source work, including when the original source disappears.
	s.exec(`DELETE FROM openfox.messages WHERE conversation_id=$1::uuid`, s.conversation)
	var replay created
	c.must(200, http.MethodPost, "share-snapshots", body, &replay, "Idempotency-Key", key)
	if replay.Token != initial.Token {
		t.Fatal("lost-response retry did not recover token")
	}
	s.exec(`DELETE FROM openfox.api_idempotency_keys WHERE organization_id=$1::uuid`, s.org)
	c.must(200, http.MethodGet, "share-snapshots/"+initial.ID+"/link", nil, &replay)
	if replay.Token != initial.Token {
		t.Fatal("recovery depends on idempotency retention")
	}
	s.exec(`UPDATE openfox.share_snapshots SET created_at=clock_timestamp()-interval '2 days',expires_at=clock_timestamp()-interval '1 day' WHERE id=$1::uuid`, initial.ID)
	c.expectError(404, "NOT_FOUND", http.MethodGet, "share-snapshots/"+initial.ID+"/link", nil)
}

func TestShareRecoveryPublicSwitchTamperAndMembership(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	out := s.create(s.member(), s.convBody("PUBLIC"))
	s.allowPublic(false)
	s.member().expectError(404, "NOT_FOUND", http.MethodGet, "share-snapshots/"+out.ID+"/link", nil)
	s.allowPublic(true)
	s.member().must(200, http.MethodGet, "share-snapshots/"+out.ID+"/link", nil, nil)
	s.exec(`UPDATE openfox.share_snapshots SET token_key_id='0000000000000000' WHERE id=$1::uuid`, out.ID)
	s.member().expectError(503, "SHARE_LINK_UNAVAILABLE", http.MethodGet, "share-snapshots/"+out.ID+"/link", nil)
	if s.view(out.Token).Status != 200 {
		t.Fatal("hash-based access must not depend on encryption key")
	}
	s.exec(`UPDATE openfox.organization_members SET status='REVOKED',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, s.org, qcMemberID)
	r := s.member().do(http.MethodGet, "share-snapshots/"+out.ID+"/link", nil)
	if r.Status == 200 || bytes.Contains(r.Body, []byte(out.Token)) {
		t.Fatal("former member recovered token")
	}
}

func TestShareWithoutKeyAndLongMessage(t *testing.T) {
	e := newQcEnvWithBox(t, nil)
	space := e.createSpace(nil)
	e.addSpaceMember(space.ID, qcMemberID, "MEMBER")
	agent := e.seedAgent(space.ID, qcOwnerID, "SHARED")
	var conv struct {
		ID string `json:"id"`
	}
	e.member().must(201, http.MethodPost, "conversations", map[string]any{"space_id": space.ID, "agent_id": agent}, &conv)
	key := qcNewID(t)
	e.member().expectError(503, "SECRETS_UNAVAILABLE", http.MethodPost, "share-snapshots", map[string]any{"kind": "CONVERSATION", "scope": "ORGANIZATION", "conversation_id": conv.ID}, "Idempotency-Key", key)
	if e.count(`SELECT count(*) FROM openfox.api_idempotency_keys WHERE organization_id=$1::uuid`, e.org) != 0 || e.count(`SELECT count(*) FROM openfox.share_snapshots WHERE organization_id=$1::uuid`, e.org) != 0 {
		t.Fatal("failed creation left state")
	}
	s := newShareEnv(t)
	long := strings.Repeat("中文🙂", 5000)
	var msg struct {
		ID string `json:"id"`
	}
	s.member().must(201, http.MethodPost, "conversations/"+s.conversation+"/messages", map[string]any{"body": long, "client_message_id": qcNewID(t)}, &msg)
	body := s.convBody("ORGANIZATION")
	body["message_id"] = msg.ID
	out := s.create(s.member(), body)
	var v viewBody
	s.member().must(200, http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": out.Token}, &v)
	if v.Items[0].Body != long {
		t.Fatal("long unicode content truncated")
	}
}

func TestShareReadableRolesCrossConversationAndReplayQuota(t *testing.T) {
	s := newShareEnv(t)
	var source string
	s.scalar(&source, `SELECT id::text FROM openfox.messages WHERE conversation_id=$1::uuid ORDER BY seq LIMIT 1`, s.conversation)
	body := s.convBody("ORGANIZATION")
	body["message_id"] = strings.ToUpper(source)
	for _, level := range []string{"VIEWER", "COMMENTER"} {
		s.member().must(204, http.MethodPut, "conversations/"+s.conversation+"/members/"+qcPeerID, map[string]any{"access_level": level}, nil)
		out := s.create(s.client(qcPeerID), body)
		s.client(qcPeerID).must(200, http.MethodGet, "share-snapshots/"+out.ID+"/link", nil, nil)
	}
	s.member().must(204, http.MethodDelete, "conversations/"+s.conversation+"/members/"+qcPeerID, nil, nil)
	s.client(qcPeerID).expectError(404, "NOT_FOUND", http.MethodPost, "share-snapshots", body)
	var other struct {
		ID string `json:"id"`
	}
	s.member().must(201, http.MethodPost, "conversations", map[string]any{"space_id": s.space, "agent_id": s.agent}, &other)
	body["conversation_id"] = other.ID
	s.member().expectError(404, "NOT_FOUND", http.MethodPost, "share-snapshots", body)
	body["conversation_id"] = s.conversation
	key := qcNewID(t)
	var first created
	s.member().must(201, http.MethodPost, "share-snapshots", body, &first, "Idempotency-Key", key)
	s.exec(`INSERT INTO openfox.share_snapshots(organization_id,id,kind,scope,title,content,item_count,token_hash,created_by,token_ciphertext,token_nonce,token_key_id)
 SELECT $1::uuid,gen_random_uuid(),'CONVERSATION','ORGANIZATION','quota','{"items":[]}'::jsonb,1,sha256(($1::text||g::text)::bytea),$2::uuid,decode(repeat('00',59),'hex'),decode(repeat('00',12),'hex'),'0000000000000000'
 FROM generate_series(1,99)g`, s.org, qcMemberID)
	var replay created
	s.member().must(200, http.MethodPost, "share-snapshots", body, &replay, "Idempotency-Key", key)
	if replay.Token != first.Token {
		t.Fatal("quota replay changed token")
	}
	s.member().expectError(409, "SHARE_QUOTA_EXCEEDED", http.MethodPost, "share-snapshots", body, "Idempotency-Key", qcNewID(t))
	s.member().must(200, http.MethodDelete, "share-snapshots/"+first.ID, nil, nil)
	s.member().expectError(404, "NOT_FOUND", http.MethodPost, "share-snapshots", body, "Idempotency-Key", key)
}

func TestShareCiphertextCannotMoveBetweenSnapshots(t *testing.T) {
	s := newShareEnv(t)
	a := s.create(s.member(), s.convBody("ORGANIZATION"))
	b := s.create(s.member(), s.convBody("ORGANIZATION"))
	s.exec(`UPDATE openfox.share_snapshots dst SET token_ciphertext=src.token_ciphertext,token_nonce=src.token_nonce,token_key_id=src.token_key_id
 FROM openfox.share_snapshots src WHERE src.id=$1::uuid AND dst.id=$2::uuid`, a.ID, b.ID)
	s.member().expectError(503, "SHARE_LINK_UNAVAILABLE", http.MethodGet, "share-snapshots/"+b.ID+"/link", nil)
	s.member().must(200, http.MethodGet, "share-snapshots/"+a.ID+"/link", nil, nil)
}
