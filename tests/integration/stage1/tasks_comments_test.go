package stage1

import (
	"net/http"
	"testing"
)

// 评论通知：作者自动关注；被 @ 的成员收到 MENTION；其余成员关注者收到 TASK_COMMENT；
// 读不到空间的人（包括被 @ 的）不收任何通知；重复提交同一 client_comment_id 不重复通知。
func TestTasksCommentNotifications(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	follower := env.addMember(tkUserA, "MEMBER")
	mentioned := env.addMember(tkUserB, "MEMBER")
	commenter := env.addMember(tkUserC, "MEMBER")
	for _, user := range []string{tkUserA, tkUserB, tkUserC} {
		env.addSpaceMember(space.ID, user, "MEMBER", "ACTIVE")
	}
	_ = follower
	_ = mentioned
	// owner 创建并把 A 设为处理人：owner（创建人）与 A（处理人）自动关注。
	task := env.newTask(env.owner, space, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": tkUserA}}})
	// member 不是私有空间成员，却残留了一条关注记录（例如被移出空间前关注过）。
	env.exec(`INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'HUMAN', $4::uuid, $4::uuid)`, env.org, space.ID, task.ID, cfgMemberID)
	// B 也关注（会被 @，应只收 MENTION）。
	mentioned.must(http.StatusNoContent, http.MethodPut, taskPath(space, task.ID)+"/followers/human/"+tkUserB, nil, nil)

	before := map[string][]string{}
	for _, user := range []string{cfgOwnerID, cfgMemberID, tkUserA, tkUserB, tkUserC} {
		before[user] = env.notifications(user)
	}
	clientID := "comment-" + cfgSuffix(t)
	body := map[string]any{"body": "please review @B @member", "client_comment_id": clientID,
		"mentions": []map[string]any{{"user_id": tkUserB}, {"user_id": cfgMemberID}}}
	var comment struct {
		ID string `json:"id"`
	}
	commenter.must(http.StatusCreated, http.MethodPost, taskPath(space, task.ID)+"/comments", body, &comment)
	// 同一 client_comment_id 重放：返回同一条评论，不重复通知。
	var replay struct {
		ID string `json:"id"`
	}
	commenter.must(http.StatusCreated, http.MethodPost, taskPath(space, task.ID)+"/comments", body, &replay)
	if replay.ID != comment.ID {
		t.Fatalf("comment replay id = %s, want %s", replay.ID, comment.ID)
	}

	added := func(user string) []string {
		t.Helper()
		seen := map[string]int{}
		for _, item := range before[user] {
			seen[item]++
		}
		var result []string
		for _, item := range env.notifications(user) {
			if seen[item] > 0 {
				seen[item]--
				continue
			}
			result = append(result, item)
		}
		return result
	}
	expect := func(user string, want ...string) {
		t.Helper()
		if got := added(user); !sameStrings(sortedCopy(got), sortedCopy(want)) {
			t.Fatalf("notifications for %s = %v, want %v", user, got, want)
		}
	}
	expect(tkUserB, kindTask("MENTION", task.ID))
	expect(tkUserA, kindTask("TASK_COMMENT", task.ID))
	expect(cfgOwnerID, kindTask("TASK_COMMENT", task.ID))
	expect(cfgMemberID) // 被 @ 且是关注者，但读不到私有空间
	expect(tkUserC)     // 作者自己

	// MENTION 带评论 ID 与摘要。
	var commentID, summary string
	env.scalar(&commentID, `SELECT comment_id::text FROM openfox.notifications
		WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND kind = 'MENTION'`, env.org, tkUserB)
	env.scalar(&summary, `SELECT safe_summary FROM openfox.notifications
		WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND kind = 'MENTION'`, env.org, tkUserB)
	if commentID != comment.ID || summary != "please review @B @member" {
		t.Fatalf("mention notification = %s %q", commentID, summary)
	}

	// 作者自动关注；之后别人评论时作者收到 TASK_COMMENT。
	var followers struct {
		Followers []struct {
			ID string `json:"id"`
		} `json:"followers"`
		Following bool `json:"following"`
	}
	commenter.must(http.StatusOK, http.MethodGet, taskPath(space, task.ID)+"/followers", nil, &followers)
	if !followers.Following {
		t.Fatalf("commenter not following: %+v", followers)
	}
	for _, user := range []string{cfgOwnerID, cfgMemberID, tkUserA, tkUserB, tkUserC} {
		before[user] = env.notifications(user)
	}
	env.owner.must(http.StatusCreated, http.MethodPost, taskPath(space, task.ID)+"/comments",
		map[string]any{"body": "thanks", "client_comment_id": "comment-" + cfgSuffix(t)}, nil)
	expect(tkUserC, kindTask("TASK_COMMENT", task.ID))
	expect(tkUserA, kindTask("TASK_COMMENT", task.ID))
	expect(tkUserB, kindTask("TASK_COMMENT", task.ID))
	expect(cfgOwnerID)
	expect(cfgMemberID)

	// 取消关注后仍是处理人，使用处理人事件；不会重复投递关注者事件。
	follower.must(http.StatusNoContent, http.MethodDelete, taskPath(space, task.ID)+"/followers/human/"+tkUserA, nil, nil)
	for _, user := range []string{tkUserA} {
		before[user] = env.notifications(user)
	}
	commenter.must(http.StatusCreated, http.MethodPost, taskPath(space, task.ID)+"/comments",
		map[string]any{"body": "again", "client_comment_id": "comment-" + cfgSuffix(t)}, nil)
	expect(tkUserA, kindTask("TASK_COMMENT", task.ID))
	var classified int
	env.scalar(&classified, `SELECT count(*) FROM openfox.notifications WHERE recipient_user_id=$1 AND task_id=$2 AND safe_summary='again' AND inbox_event='comment_created_member_assignee' AND inbox_level='important'`, tkUserA, task.ID)
	if classified != 1 {
		t.Fatalf("unfollowed assignee received %d classified comments", classified)
	}

	// 时间线合并动态与评论，评论带作者名、提及与表情汇总。
	commenter.must(http.StatusNoContent, http.MethodPut, taskPath(space, task.ID)+"/reactions",
		map[string]any{"emoji": "👍", "comment_id": comment.ID}, nil)
	env.owner.must(http.StatusNoContent, http.MethodPut, taskPath(space, task.ID)+"/reactions",
		map[string]any{"emoji": "👍", "comment_id": comment.ID}, nil)
	var timeline struct {
		Items []struct {
			Type    string `json:"type"`
			Comment *struct {
				ID         string `json:"id"`
				AuthorName string `json:"author_name"`
				Mentions   []struct {
					UserID string `json:"user_id"`
				} `json:"mentions"`
				Reactions []struct {
					Emoji   string `json:"emoji"`
					Count   int    `json:"count"`
					Reacted bool   `json:"reacted"`
				} `json:"reactions"`
			} `json:"comment"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	commenter.must(http.StatusOK, http.MethodGet, taskPath(space, task.ID)+"/timeline", nil, &timeline)
	if len(timeline.Items) < 4 || timeline.Items[0].Type != "activity" || timeline.NextCursor != nil {
		t.Fatalf("timeline = %+v", timeline)
	}
	found := false
	for _, item := range timeline.Items {
		if item.Comment != nil && item.Comment.ID == comment.ID {
			found = true
			if item.Comment.AuthorName != tkEmails[tkUserC] || len(item.Comment.Mentions) != 2 ||
				len(item.Comment.Reactions) != 1 || item.Comment.Reactions[0].Count != 2 || !item.Comment.Reactions[0].Reacted {
				t.Fatalf("timeline comment = %+v", item.Comment)
			}
		}
	}
	if !found {
		t.Fatal("comment missing from timeline")
	}
	// 分页：limit=1 逐页取完，与一次取完一致。
	var ids []string
	cursor := ""
	for page := 0; page < 50; page++ {
		path := taskPath(space, task.ID) + "/timeline?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var paged struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		}
		commenter.must(http.StatusOK, http.MethodGet, path, nil, &paged)
		for range paged.Items {
			ids = append(ids, "x")
		}
		if paged.NextCursor == nil {
			break
		}
		cursor = *paged.NextCursor
	}
	if len(ids) != len(timeline.Items) {
		t.Fatalf("paged timeline = %d items, want %d", len(ids), len(timeline.Items))
	}
}

// The tab counts activities and all comments across all pages. Nested legacy replies
// remain accessible in the original flat timeline and in their root topic.
func TestTasksCommentThreadTimeline(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	task := env.newTask(env.owner, space, nil)
	path := taskPath(space, task.ID)
	post := func(parent, body string) string {
		t.Helper()
		input := map[string]any{"body": body, "client_comment_id": cfgSuffix(t)}
		if parent != "" {
			input["reply_to_id"] = parent
		}
		var result struct{ ID string }
		env.owner.must(http.StatusCreated, http.MethodPost, path+"/comments", input, &result)
		return result.ID
	}
	type response struct {
		Items []struct {
			Comment *struct {
				ID         string
				ReplyCount int                         `json:"reply_count"`
				Recent     []struct{ ID, Body string } `json:"recent_replies"`
			}
		}
		Total *int
		Next  string `json:"next_cursor"`
	}
	read := func(path string) response {
		t.Helper()
		var got response
		env.owner.must(http.StatusOK, http.MethodGet, path, nil, &got)
		return got
	}
	root := post("", "root")
	other := post("", "other")
	first := read(path + "/timeline?limit=1&thread=roots")
	if len(first.Items) != 1 || first.Next == "" || first.Total == nil || *first.Total != 3 {
		t.Fatalf("first roots page: %+v", first)
	}
	post(root, "first older")
	oldestVisible := post(root, "second older")
	r1 := post(root, "first")
	r2 := post(r1, "nested")
	r3 := post(root, "third")
	r4 := post(root, "latest")
	post(other, "separate")
	roots := read(path + "/timeline?thread=roots")
	if roots.Total == nil || *roots.Total != 10 || len(roots.Items) != 3 {
		t.Fatalf("root page %+v", roots)
	}
	card := roots.Items[1].Comment
	if card == nil || card.ID != root || card.ReplyCount != 6 || len(card.Recent) != 5 || card.Recent[0].ID != oldestVisible || card.Recent[4].ID != r4 {
		t.Fatalf("reply preview: %+v", card)
	}
	next := read(path + "/timeline?thread=roots&limit=1&cursor=" + first.Next)
	if next.Total != nil || len(next.Items) != 1 || next.Items[0].Comment.ID != root {
		t.Fatalf("second roots page %+v", next)
	}
	newest := read(path + "/timeline?limit=2&thread=" + root)
	if len(newest.Items) != 2 || newest.Items[0].Comment.ID != r4 || newest.Items[1].Comment.ID != r3 || newest.Next == "" {
		t.Fatalf("newest replies %+v", newest)
	}
	older := read(path + "/timeline?limit=2&thread=" + root + "&cursor=" + newest.Next)
	if len(older.Items) != 2 || older.Items[0].Comment.ID != r2 || older.Items[1].Comment.ID != r1 || older.Next == "" {
		t.Fatalf("older replies %+v", older)
	}
	if flat := read(path + "/timeline"); len(flat.Items) != 10 || flat.Total != nil {
		t.Fatalf("flat timeline %+v", flat)
	}
	otherTask := env.newTask(env.owner, space, nil)
	if foreign := read(taskPath(space, otherTask.ID) + "/timeline?thread=" + root); len(foreign.Items) != 0 {
		t.Fatalf("cross-task replies: %+v", foreign)
	}
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/timeline?thread="+root, nil)
	env.owner.expectError(http.StatusBadRequest, "INVALID_THREAD", http.MethodGet, path+"/timeline?thread=bad", nil)
}
