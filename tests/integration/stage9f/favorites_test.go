package stage9f

// 阶段 12f 收藏集成测试：可见性校验、幂等、置顶、条目删除后收藏失效、跨租户隔离。

import (
	"net/http"
	"testing"
)

type qcReply struct {
	ID       string `json:"id"`
	Favorite bool   `json:"favorite"`
}

type qcReplyList struct {
	Personal     []qcReply `json:"personal"`
	Organization []qcReply `json:"organization"`
}

func TestQuickCommandFavorites(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member, peer, outsider := e.member(), e.client(qcPeerID), e.outsider()
	path := commandsPath(s.id)

	first := member.create(t, s.id, agentBody("第一条", "SHARED", s.shared))
	second := member.create(t, s.id, agentBody("第二条", "SHARED", s.shared))
	private := member.create(t, s.id, agentBody("私有", "PRIVATE", s.mine))

	// 收藏第二条：幂等，列表中置顶；收藏只属于本人
	var got qcCommand
	for i := 0; i < 2; i++ {
		peer.must(http.StatusOK, http.MethodPut, path+"/"+second.ID+"/favorite", nil, &got)
		if !got.Favorite {
			t.Fatalf("favorite = %+v", got)
		}
	}
	if n := e.count(`SELECT count(*) FROM openfox.quick_entry_favorites WHERE entry_id = $1::uuid`, second.ID); n != 1 {
		t.Fatalf("favorite rows = %d", n)
	}
	list := peer.list(t, s.id)
	if len(list.Shared) != 2 || list.Shared[0].ID != second.ID || !list.Shared[0].Favorite || list.Shared[1].Favorite {
		t.Fatalf("peer shared = %+v", list.Shared)
	}
	if other := member.list(t, s.id); other.Shared[0].ID != first.ID || other.Shared[0].Favorite {
		t.Fatalf("member must not see peer favorite: %+v", other.Shared)
	}

	// 他人私有、不存在的条目：404，且不写入收藏行
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, path+"/"+private.ID+"/favorite", nil)
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, path+"/"+private.ID+"/favorite", nil)
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, path+"/"+qcNewID(t)+"/favorite", nil)
	if n := e.count(`SELECT count(*) FROM openfox.quick_entry_favorites WHERE entry_id = $1::uuid`, private.ID); n != 0 {
		t.Fatalf("private favorite rows = %d", n)
	}

	// 跨租户：另一个组织的成员既不能收藏，也看不到
	if r := outsider.do(http.MethodPut, path+"/"+first.ID+"/favorite", nil); r.Status == http.StatusOK {
		t.Fatalf("outsider favorited: %d %s", r.Status, r.Body)
	}

	// 取消收藏两次都成功（幂等）
	for i := 0; i < 2; i++ {
		peer.must(http.StatusOK, http.MethodDelete, path+"/"+second.ID+"/favorite", nil, &got)
		if got.Favorite {
			t.Fatalf("unfavorite = %+v", got)
		}
	}
	if list := peer.list(t, s.id); list.Shared[0].ID != first.ID {
		t.Fatalf("order after unfavorite = %+v", list.Shared)
	}

	// 条目被删除后收藏失效：列表不再出现，重新收藏 404
	peer.must(http.StatusOK, http.MethodPut, path+"/"+first.ID+"/favorite", nil, nil)
	member.must(http.StatusNoContent, http.MethodDelete, path+"/"+first.ID, nil, nil)
	if list := peer.list(t, s.id); len(list.Shared) != 1 || list.Shared[0].ID != second.ID {
		t.Fatalf("after delete = %+v", list.Shared)
	}
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, path+"/"+first.ID+"/favorite", nil)
}

func TestQuickReplyFavorites(t *testing.T) {
	e := newQcEnv(t)
	member, peer, owner, outsider := e.member(), e.client(qcPeerID), e.owner(), e.outsider()
	const base = "quick-replies"
	mk := func(c *qcClient, scope, title string) qcReply {
		var out qcReply
		c.must(http.StatusCreated, http.MethodPost, base, map[string]any{"scope": scope, "title": title, "body": "正文 " + title}, &out)
		return out
	}
	org1 := mk(owner, "ORG", "组织一")
	org2 := mk(owner, "ORG", "组织二")
	mine := mk(member, "PERSONAL", "我的")

	var got qcReply
	for i := 0; i < 2; i++ {
		peer.must(http.StatusOK, http.MethodPut, base+"/"+org2.ID+"/favorite", nil, &got)
		if !got.Favorite {
			t.Fatalf("favorite = %+v", got)
		}
	}
	var list qcReplyList
	peer.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Organization) != 2 || list.Organization[0].ID != org2.ID || !list.Organization[0].Favorite {
		t.Fatalf("peer org = %+v", list.Organization)
	}
	owner.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if list.Organization[0].ID != org1.ID || list.Organization[0].Favorite {
		t.Fatalf("owner must not see peer favorite: %+v", list.Organization)
	}

	// 他人的个人回复不可见：404，不泄露存在性
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, base+"/"+mine.ID+"/favorite", nil)
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, base+"/"+mine.ID+"/favorite", nil)
	if n := e.count(`SELECT count(*) FROM openfox.quick_entry_favorites WHERE entry_id = $1::uuid`, mine.ID); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	// 跨租户
	if r := outsider.do(http.MethodPut, base+"/"+org1.ID+"/favorite", nil); r.Status == http.StatusOK {
		t.Fatalf("outsider favorited: %d %s", r.Status, r.Body)
	}
	// 取消幂等；删除后收藏失效
	for i := 0; i < 2; i++ {
		peer.must(http.StatusOK, http.MethodDelete, base+"/"+org2.ID+"/favorite", nil, nil)
	}
	peer.must(http.StatusOK, http.MethodPut, base+"/"+org1.ID+"/favorite", nil, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, base+"/"+org1.ID, nil, nil)
	peer.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Organization) != 1 || list.Organization[0].ID != org2.ID {
		t.Fatalf("after delete = %+v", list.Organization)
	}
	peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, base+"/"+org1.ID+"/favorite", nil)
}
