package stage1

import (
	"net/http"
	"sort"
	"strings"
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

// ---- 阶段 11b：@全体成员 / @小队 / @Agent 触发运行 ----

// mentionToken 返回正文里的提及令牌。
func mentionToken(name, kind, id string) string {
	if kind == "all" {
		return "@[" + name + "](openfox://mention/all)"
	}
	return "@[" + name + "](openfox://mention/" + kind + "/" + id + ")"
}

// seedMentionAgent 建一个可被提及的 Agent，并设置 trigger_mention。
func (e *tkEnv) seedMentionAgent(spaceID string, trigger bool) string {
	e.t.Helper()
	id := e.insertAgent(spaceID, cfgOwnerID, "SHARED")
	e.exec(`UPDATE openfox.agents SET trigger_mention = $3 WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, id, trigger)
	return id
}

// seedMentionCrew 建小队；members 第一个为队长，humans 为小队的真人成员。
func (e *tkEnv) seedMentionCrew(spaceID, owner, visibility, instructions string, members []string, humans []string) string {
	e.t.Helper()
	crew := cfgNewID(e.t)
	leader := any(nil)
	if len(members) > 0 {
		leader = members[0]
	}
	e.exec(`INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name, visibility, leader_agent_id, instructions)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7::uuid, $8)`,
		e.org, crew, spaceID, owner, "Crew "+cfgSuffix(e.t), visibility, leader, instructions)
	for position, agent := range members {
		e.exec(`INSERT INTO openfox.agent_crew_members (organization_id, crew_id, agent_id, position) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`,
			e.org, crew, agent, position)
	}
	for _, user := range humans {
		e.exec(`INSERT INTO openfox.agent_crew_human_members (organization_id, crew_id, user_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`,
			e.org, crew, user, cfgOwnerID)
	}
	return crew
}

// mentionEvents 返回评论触发的 MENTIONED 事件 "agent:crew"（crew 为空表示直接 @Agent），排序后。
func (e *tkEnv) mentionEvents(commentID string) []string {
	e.t.Helper()
	rows, err := e.superuser.Query(e.ctx, `
		SELECT agent_id::text || ':' || COALESCE(crew_id::text, '') FROM openfox.task_agent_events
		 WHERE organization_id = $1::uuid AND kind = 'MENTIONED' AND comment_id = $2::uuid`, e.org, commentID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	events := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			e.t.Fatal(err)
		}
		events = append(events, item)
	}
	sort.Strings(events)
	return events
}

func (e *tkEnv) postMentionComment(client *cfgClient, space cfgSpace, taskID, body string, mentions []map[string]any, clientID string) (status int, id string) {
	e.t.Helper()
	if clientID == "" {
		clientID = "mention-" + cfgSuffix(e.t)
	}
	response := client.do(http.MethodPost, taskPath(space, taskID)+"/comments",
		map[string]any{"body": body, "client_comment_id": clientID, "mentions": mentions})
	if response.Status == http.StatusCreated {
		var comment struct {
			ID string `json:"id"`
		}
		response.decode(e.t, &comment)
		id = comment.ID
	}
	return response.Status, id
}

// @全体成员：受众 = 空间可读的 ACTIVE 成员，不含访客和作者；读不到空间的组织成员不收；
// 同一人只收一条 MENTION（已关注的人不再收 TASK_COMMENT）；摘要里的令牌换成 @名字。
func TestTasksCommentMentionAll(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	author := env.addMember(tkUserA, "MEMBER")
	env.addMember(tkUserB, "MEMBER")
	env.addMember(tkUserC, "MEMBER") // 组织成员，但不是私有空间成员
	env.addMember(tkGuest, "GUEST")
	env.addSpaceMember(space.ID, tkUserA, "MEMBER", "ACTIVE")
	env.addSpaceMember(space.ID, tkUserB, "MEMBER", "ACTIVE")
	env.addSpaceMember(space.ID, tkGuest, "MEMBER", "ACTIVE") // 访客即使是空间成员也被排除
	task := env.newTask(env.owner, space, nil)
	env.exec(`INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'HUMAN', $4::uuid, $4::uuid)`, env.org, space.ID, task.ID, tkUserB)

	before := map[string][]string{}
	for _, user := range []string{cfgOwnerID, tkUserA, tkUserB, tkUserC, tkGuest} {
		before[user] = env.notifications(user)
	}
	body := "各位请看 " + mentionToken("全体成员", "all", "")
	status, commentID := env.postMentionComment(author, space, task.ID, body, []map[string]any{{"all": true}}, "")
	if status != http.StatusCreated {
		t.Fatalf("post all mention = %d", status)
	}
	added := func(user string) int { return len(env.notifications(user)) - len(before[user]) }
	for user, want := range map[string]int{cfgOwnerID: 1, tkUserB: 1, tkUserA: 0, tkUserC: 0, tkGuest: 0} {
		if got := added(user); got != want {
			t.Errorf("notifications for %s = +%d, want +%d", user, got, want)
		}
	}
	// B 同时是关注者：只有 MENTION，没有 TASK_COMMENT。
	var mentionCount, commentCount int
	env.scalar(&mentionCount, `SELECT count(*) FROM openfox.notifications WHERE recipient_user_id=$1 AND comment_id=$2 AND kind='MENTION'`, tkUserB, commentID)
	env.scalar(&commentCount, `SELECT count(*) FROM openfox.notifications WHERE recipient_user_id=$1 AND comment_id=$2 AND kind='TASK_COMMENT'`, tkUserB, commentID)
	if mentionCount != 1 || commentCount != 0 {
		t.Fatalf("B notifications: mention=%d task_comment=%d", mentionCount, commentCount)
	}
	var summary string
	env.scalar(&summary, `SELECT safe_summary FROM openfox.notifications WHERE recipient_user_id=$1 AND comment_id=$2 AND kind='MENTION'`, tkUserB, commentID)
	if summary != "各位请看 @全体成员" {
		t.Fatalf("summary = %q", summary)
	}
	// 重放同一 client_comment_id 不重复通知。
	status, _ = env.postMentionComment(author, space, task.ID, body, []map[string]any{{"all": true}}, "mention-replay")
	status2, _ := env.postMentionComment(author, space, task.ID, body, []map[string]any{{"all": true}}, "mention-replay")
	if status != http.StatusCreated || status2 != http.StatusCreated {
		t.Fatalf("replay statuses = %d %d", status, status2)
	}
	if got := added(tkUserB); got != 2 {
		t.Fatalf("B notifications after replay = +%d, want +2 (两条不同评论各一条)", got)
	}
	// 时间线带 all 标记。
	var timeline struct {
		Items []struct {
			Comment *struct {
				ID       string `json:"id"`
				Mentions []struct {
					All bool `json:"all"`
				} `json:"mentions"`
			} `json:"comment"`
		} `json:"items"`
	}
	author.must(http.StatusOK, http.MethodGet, taskPath(space, task.ID)+"/timeline", nil, &timeline)
	found := false
	for _, item := range timeline.Items {
		if item.Comment != nil && item.Comment.ID == commentID {
			found = len(item.Comment.Mentions) == 1 && item.Comment.Mentions[0].All
		}
	}
	if !found {
		t.Fatal("timeline comment missing the all mention")
	}
}

// @全体成员的受众上限 200：刚好 200 人通过，201 人返回 422 MENTION_AUDIENCE_TOO_LARGE 且整条评论回滚。
func TestTasksCommentMentionAllAudienceLimit(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	// 受众（不含作者 owner）：member + 198 个批量成员 = 199 人。
	env.exec(`WITH bulk AS (
			INSERT INTO openfox.users (id, email)
			SELECT gen_random_uuid(), 'bulk-' || n || '-' || $2::text || '@example.test' FROM generate_series(1, 198) n
			RETURNING id)
		INSERT INTO openfox.organization_members (organization_id, user_id, role)
		SELECT $1::uuid, id, 'MEMBER' FROM bulk`, env.org, cfgSuffix(t))
	task := env.newTask(env.owner, space, nil)
	audience := func() int {
		var n int
		env.scalar(&n, `SELECT count(*) FROM openfox.organization_members WHERE organization_id=$1::uuid AND status='ACTIVE' AND role<>'GUEST' AND user_id<>$2::uuid`, env.org, cfgOwnerID)
		return n
	}
	if audience() != 199 {
		t.Fatalf("audience = %d, want 199", audience())
	}
	mentions := []map[string]any{{"all": true}}
	body := mentionToken("全体成员", "all", "")
	if status, _ := env.postMentionComment(env.owner, space, task.ID, body, mentions, ""); status != http.StatusCreated {
		t.Fatalf("199 人 = %d, want 201", status)
	}
	// 再加一人 = 200，仍可以。
	env.addMember(tkUserA, "MEMBER")
	if status, _ := env.postMentionComment(env.owner, space, task.ID, body, mentions, ""); status != http.StatusCreated {
		t.Fatalf("200 人 = %d, want 201", status)
	}
	// 201 人：拒绝，评论与通知都不留。
	env.addMember(tkUserB, "MEMBER")
	var comments int
	env.scalar(&comments, `SELECT count(*) FROM openfox.task_comments WHERE task_id=$1::uuid`, task.ID)
	response := env.owner.do(http.MethodPost, taskPath(space, task.ID)+"/comments",
		map[string]any{"body": body, "client_comment_id": "too-large", "mentions": mentions})
	if response.Status != http.StatusUnprocessableEntity || !strings.Contains(string(response.Body), "MENTION_AUDIENCE_TOO_LARGE") {
		t.Fatalf("201 人 = %d %s", response.Status, response.Body)
	}
	var after int
	env.scalar(&after, `SELECT count(*) FROM openfox.task_comments WHERE task_id=$1::uuid`, task.ID)
	if after != comments {
		t.Fatalf("comments %d -> %d，超限评论应整体回滚", comments, after)
	}
}

// @小队：小队里读得到空间的真人成员收 MENTION（读不到的不收），队长 Agent 入队一次运行；
// 同一 Agent 被 @ 和被 @小队 同时命中只触发一次。
func TestTasksCommentMentionCrew(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	author := env.addMember(tkUserA, "MEMBER")
	env.addMember(tkUserB, "MEMBER")
	env.addMember(tkUserC, "MEMBER") // 小队成员，但读不到私有空间
	env.addSpaceMember(space.ID, tkUserA, "MEMBER", "ACTIVE")
	env.addSpaceMember(space.ID, tkUserB, "MEMBER", "ACTIVE")
	task := env.newTask(env.owner, space, nil)
	leader := env.seedMentionAgent(space.ID, true)
	other := env.seedMentionAgent(space.ID, true)
	crew := env.seedMentionCrew(space.ID, cfgOwnerID, "SHARED", "先评审再动手", []string{leader, other}, []string{tkUserB, tkUserC, tkUserA})

	before := map[string]int{}
	for _, user := range []string{cfgOwnerID, tkUserA, tkUserB, tkUserC} {
		before[user] = len(env.notifications(user))
	}
	body := "麻烦 " + mentionToken("小队", "crew", crew) + " 看一下，" + mentionToken("队长", "agent", leader)
	status, commentID := env.postMentionComment(author, space, task.ID, body, []map[string]any{{"crew_id": crew}, {"agent_id": leader}}, "")
	if status != http.StatusCreated {
		t.Fatalf("post crew mention = %d", status)
	}
	// B 收 MENTION；C 读不到空间不收；作者 A 不收自己的。
	var mentions int
	env.scalar(&mentions, `SELECT count(*) FROM openfox.notifications WHERE comment_id=$1::uuid AND kind='MENTION'`, commentID)
	if mentions != 1 {
		t.Fatalf("MENTION 通知数 = %d, want 1 (仅 B)", mentions)
	}
	var recipient string
	env.scalar(&recipient, `SELECT recipient_user_id::text FROM openfox.notifications WHERE comment_id=$1::uuid AND kind='MENTION'`, commentID)
	if recipient != tkUserB {
		t.Fatalf("MENTION 收件人 = %s, want B", recipient)
	}
	// 队长只触发一次（直接 @ 在前，队长命中被去重），且 crew_id 记在先命中的一条上。
	events := env.mentionEvents(commentID)
	if len(events) != 1 || !strings.HasPrefix(events[0], leader+":") {
		t.Fatalf("events = %v, want 仅队长一条", events)
	}
	// 只 @小队：事件带 crew_id。
	status, solo := env.postMentionComment(author, space, task.ID, mentionToken("小队", "crew", crew), []map[string]any{{"crew_id": crew}}, "")
	if status != http.StatusCreated {
		t.Fatalf("solo crew mention = %d", status)
	}
	if got := env.mentionEvents(solo); len(got) != 1 || got[0] != leader+":"+crew {
		t.Fatalf("solo events = %v, want [%s:%s]", got, leader, crew)
	}
	// 列表接口返回 crew_id。
	var list struct {
		Comments []struct {
			ID       string `json:"id"`
			Mentions []struct {
				CrewID string `json:"crew_id"`
			} `json:"mentions"`
		} `json:"comments"`
	}
	author.must(http.StatusOK, http.MethodGet, taskPath(space, task.ID)+"/comments", nil, &list)
	ok := false
	for _, comment := range list.Comments {
		if comment.ID == solo && len(comment.Mentions) == 1 && comment.Mentions[0].CrewID == crew {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("comments list missing crew mention: %+v", list)
	}
}

// @Agent 触发条件：trigger_mention 关闭、Agent 已归档时只保留提及；
// 重复提交 client_comment_id 不重复入队；每条评论最多触发 5 个 Agent。
func TestTasksCommentMentionAgentTrigger(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	task := env.newTask(env.owner, space, nil)
	on := env.seedMentionAgent(space.ID, true)
	off := env.seedMentionAgent(space.ID, false)
	archived := env.seedMentionAgent(space.ID, true)

	status, id := env.postMentionComment(env.owner, space, task.ID, "hi", []map[string]any{{"agent_id": on}, {"agent_id": off}}, "trigger-1")
	if status != http.StatusCreated {
		t.Fatalf("status = %d", status)
	}
	if got := env.mentionEvents(id); len(got) != 1 || got[0] != on+":" {
		t.Fatalf("events = %v, want 仅开启 trigger_mention 的 Agent", got)
	}
	// 提及本身两条都保存。
	var stored int
	env.scalar(&stored, `SELECT count(*) FROM openfox.comment_mentions WHERE comment_id=$1::uuid`, id)
	if stored != 2 {
		t.Fatalf("stored mentions = %d, want 2", stored)
	}
	// 重放：同一评论，事件仍是 1 条。
	env.postMentionComment(env.owner, space, task.ID, "hi", []map[string]any{{"agent_id": on}, {"agent_id": off}}, "trigger-1")
	if got := env.mentionEvents(id); len(got) != 1 {
		t.Fatalf("replay events = %v", got)
	}
	// 数据库唯一索引兜底：同一评论同一 Agent 再插一条被拒绝。
	if _, err := env.superuser.Exec(env.ctx, `INSERT INTO openfox.task_agent_events (organization_id, kind, space_id, task_id, agent_id, actor_user_id, comment_id)
		VALUES ($1::uuid, 'MENTIONED', $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid)`, env.org, space.ID, task.ID, on, cfgOwnerID, id); err == nil {
		t.Fatal("重复 MENTIONED 事件应被唯一索引拒绝")
	}
	// 已归档的 Agent 不能被提及（校验阶段 400）。
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, env.org, archived)
	if status, _ := env.postMentionComment(env.owner, space, task.ID, "x", []map[string]any{{"agent_id": archived}}, ""); status != http.StatusBadRequest {
		t.Fatalf("归档 Agent = %d, want 400", status)
	}
	// 上限 5：7 个 Agent 只触发前 5 个。
	var mentions []map[string]any
	var firstFive []string
	for index := 0; index < 7; index++ {
		agent := env.seedMentionAgent(space.ID, true)
		mentions = append(mentions, map[string]any{"agent_id": agent})
		if index < 5 {
			firstFive = append(firstFive, agent+":")
		}
	}
	status, many := env.postMentionComment(env.owner, space, task.ID, "all hands", mentions, "")
	if status != http.StatusCreated {
		t.Fatalf("many = %d", status)
	}
	if got := env.mentionEvents(many); !sameStrings(got, sortedCopy(firstFive)) {
		t.Fatalf("events = %v, want %v", got, sortedCopy(firstFive))
	}
}

// 私有 Agent 可见性：成员 @ 他人的 PRIVATE Agent / 含该 Agent 的小队只保存提及，不入队运行；
// @ 自己的 PRIVATE Agent 可以触发。
func TestTasksCommentMentionPrivateAgentVisibility(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	author := env.addMember(tkUserA, "MEMBER")
	env.addSpaceMember(space.ID, tkUserA, "MEMBER", "ACTIVE")
	task := env.newTask(env.owner, space, nil)

	othersPrivate := env.insertAgent(space.ID, cfgOwnerID, "PRIVATE")
	ownPrivate := env.insertAgent(space.ID, tkUserA, "PRIVATE")
	for _, id := range []string{othersPrivate, ownPrivate} {
		env.exec(`UPDATE openfox.agents SET trigger_mention = true WHERE organization_id=$1::uuid AND id=$2::uuid`, env.org, id)
	}
	// 他人的私有 Agent：提及保存，不触发。
	status, id := env.postMentionComment(author, space, task.ID, "hi", []map[string]any{{"agent_id": othersPrivate}}, "")
	if status != http.StatusCreated {
		t.Fatalf("status = %d", status)
	}
	var stored int
	env.scalar(&stored, `SELECT count(*) FROM openfox.comment_mentions WHERE comment_id=$1::uuid AND mentioned_agent_id=$2::uuid`, id, othersPrivate)
	if stored != 1 {
		t.Fatalf("提及应照常保存, got %d", stored)
	}
	if got := env.mentionEvents(id); len(got) != 0 {
		t.Fatalf("他人私有 Agent 不应触发, events = %v", got)
	}
	// 以他人私有 Agent 为队长的共享小队：同样不触发。
	crew := env.seedMentionCrew(space.ID, cfgOwnerID, "SHARED", "", []string{othersPrivate}, nil)
	_, crewComment := env.postMentionComment(author, space, task.ID, "crew", []map[string]any{{"crew_id": crew}}, "")
	if got := env.mentionEvents(crewComment); len(got) != 0 {
		t.Fatalf("他人私有队长不应触发, events = %v", got)
	}
	// 自己的私有 Agent：可以触发。
	_, own := env.postMentionComment(author, space, task.ID, "mine", []map[string]any{{"agent_id": ownPrivate}}, "")
	if got := env.mentionEvents(own); len(got) != 1 || got[0] != ownPrivate+":" {
		t.Fatalf("自己的私有 Agent 应触发, events = %v", got)
	}
}

// 校验：跨空间 Agent / 小队、他人私有小队、已归档小队、令牌与 mentions 不一致、重复 all、多目标都被拒绝（400）。
func TestTasksCommentMentionValidation(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	otherSpace := env.createSpace("shared")
	member := env.addMember(tkUserA, "MEMBER")
	task := env.newTask(env.owner, space, nil)
	foreignAgent := env.seedMentionAgent(otherSpace.ID, true)
	foreignCrew := env.seedMentionCrew(otherSpace.ID, cfgOwnerID, "SHARED", "", []string{foreignAgent}, nil)
	localAgent := env.seedMentionAgent(space.ID, true)
	privateCrew := env.seedMentionCrew(space.ID, cfgOwnerID, "PRIVATE", "", []string{localAgent}, nil)
	archivedCrew := env.seedMentionCrew(space.ID, cfgOwnerID, "SHARED", "", []string{localAgent}, nil)
	env.exec(`UPDATE openfox.agent_crews SET archived_at = clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, env.org, archivedCrew)
	sharedCrew := env.seedMentionCrew(space.ID, cfgOwnerID, "SHARED", "", []string{localAgent}, nil)

	cases := []struct {
		name     string
		client   *cfgClient
		body     string
		mentions []map[string]any
		want     int
	}{
		{"跨空间 Agent", env.owner, "x", []map[string]any{{"agent_id": foreignAgent}}, http.StatusBadRequest},
		{"跨空间小队", env.owner, "x", []map[string]any{{"crew_id": foreignCrew}}, http.StatusBadRequest},
		{"他人的私有小队", member, "x", []map[string]any{{"crew_id": privateCrew}}, http.StatusBadRequest},
		{"自己的私有小队", env.owner, "x", []map[string]any{{"crew_id": privateCrew}}, http.StatusCreated},
		{"已归档小队", env.owner, "x", []map[string]any{{"crew_id": archivedCrew}}, http.StatusBadRequest},
		{"小队不存在", env.owner, "x", []map[string]any{{"crew_id": cfgNewID(t)}}, http.StatusBadRequest},
		{"重复 all", env.owner, "x", []map[string]any{{"all": true}, {"all": true}}, http.StatusBadRequest},
		{"all 与 user 同时设置", env.owner, "x", []map[string]any{{"all": true, "user_id": tkUserA}}, http.StatusBadRequest},
		{"空提及", env.owner, "x", []map[string]any{{}}, http.StatusBadRequest},
		{"令牌不在 mentions 里", env.owner, "hi " + mentionToken("小队", "crew", sharedCrew), nil, http.StatusBadRequest},
		{"令牌与 mentions 种类不同", env.owner, "hi " + mentionToken("小队", "crew", sharedCrew), []map[string]any{{"agent_id": localAgent}}, http.StatusBadRequest},
		{"令牌与 mentions 一致", env.owner, "hi " + mentionToken("小队", "crew", sharedCrew), []map[string]any{{"crew_id": sharedCrew}}, http.StatusCreated},
		{"没有令牌的旧式正文仍可用", env.owner, "hi @a", []map[string]any{{"user_id": tkUserA}}, http.StatusCreated},
	}
	for _, c := range cases {
		if status, _ := env.postMentionComment(c.client, space, task.ID, c.body, c.mentions, ""); status != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, status, c.want)
		}
	}
}
