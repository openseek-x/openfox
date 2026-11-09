package stage2

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/executiondelivery"
)

// evStream 模拟一条事件流连接：按服务层 ListVisibleEvents 轮询，记录收到的标记与 SSE id。
type evStream struct {
	env    *dvEnv
	userID string
	cursor executiondelivery.StreamCursor
	// marks 按到达顺序记录 payload.mark（Run 事件前缀 run:，组织事件前缀 org:）。
	marks []string
	ids   []string
}

func (e *dvEnv) openStream(userID, raw string) *evStream {
	e.t.Helper()
	cursor, err := executiondelivery.ParseStreamCursor(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	stream := &evStream{env: e, userID: userID, cursor: cursor}
	stream.poll()
	return stream
}

func (s *evStream) poll() {
	s.env.t.Helper()
	selection, role := s.env.selection(s.userID)
	actor := conversations.Actor{UserID: s.userID, Role: role, AuthorizationVersion: selection.AuthorizationVersion}
	var page executiondelivery.StreamPage
	if err := s.env.inOrg(s.userID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = (executiondelivery.Service{}).ListVisibleEvents(ctx, tx, s.env.org, actor, s.cursor, executiondelivery.MaxPageSize)
		return err
	}); err != nil {
		s.env.t.Fatal(err)
	}
	if len(page.RunEvents) != len(page.RunCursors) || len(page.OrgEvents) != len(page.OrgCursors) {
		s.env.t.Fatalf("cursor count mismatch: %+v", page)
	}
	for index, event := range page.RunEvents {
		s.marks = append(s.marks, "run:"+payloadMark(event.Payload))
		s.ids = append(s.ids, page.RunCursors[index].String())
	}
	for index, event := range page.OrgEvents {
		s.marks = append(s.marks, "org:"+payloadMark(event.Payload))
		s.ids = append(s.ids, page.OrgCursors[index].String())
	}
	s.cursor = page.Cursor
}

// until 反复轮询直到收到 mark（集群里其他事务可能暂时压住水位）。
func (s *evStream) until(mark string) {
	s.env.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !s.has(mark) {
		if time.Now().After(deadline) {
			s.env.t.Fatalf("%s never received %s; got %v", dvRoles[s.userID], mark, s.marks)
		}
		time.Sleep(100 * time.Millisecond)
		s.poll()
	}
}

func (s *evStream) has(mark string) bool {
	for _, got := range s.marks {
		if got == mark {
			return true
		}
	}
	return false
}

// mine 返回带指定前缀标记（本测试写入）的事件，保持到达顺序。
func (s *evStream) mine(prefix string) []string {
	result := []string{}
	for _, mark := range s.marks {
		if strings.HasPrefix(strings.SplitN(mark, ":", 2)[1], prefix) {
			result = append(result, mark)
		}
	}
	return result
}

func payloadMark(payload json.RawMessage) string {
	var body struct {
		Mark string `json:"mark"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Mark
}

// insertOrgEvent 用给定连接写一条组织事件（payload.mark = mark），返回序号与事务号。
func insertOrgEvent(t *testing.T, ctx context.Context, conn pgx.Tx, org, mark string, spaceID, conversationID, recipient any) (int64, string) {
	t.Helper()
	var id int64
	var txid string
	if err := conn.QueryRow(ctx, `
		INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, conversation_id, recipient_user_id, payload)
		VALUES ($1::uuid, 'task.changed', 'task', $2::uuid, $3::uuid, $4::uuid, jsonb_build_object('mark', $5::text))
		RETURNING id, txid::text`, org, spaceID, conversationID, recipient, mark).Scan(&id, &txid); err != nil {
		t.Fatal(err)
	}
	return id, txid
}

// insertRunEvent 写一条 Run 事件（safe_payload.mark = mark），返回 stream_seq。
func insertRunEvent(t *testing.T, ctx context.Context, conn pgx.Tx, org, runID, mark string, sequence int) int64 {
	t.Helper()
	var seq int64
	if err := conn.QueryRow(ctx, `
		INSERT INTO openfox.run_events (organization_id, run_id, generation, sequence, type, safe_payload, payload_hash)
		VALUES ($1::uuid, $2::uuid, 1, $3, 'run.output', jsonb_build_object('mark', $4::text), repeat('0', 64))
		RETURNING stream_seq`, org, runID, sequence, mark).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// autoTx 在超级用户连接上开一个立即提交的事务执行 fn。
func (e *dvEnv) autoTx(fn func(pgx.Tx)) {
	e.t.Helper()
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	fn(tx)
	if err := tx.Commit(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dvEnv) connect() *pgx.Conn {
	e.t.Helper()
	conn, err := pgx.Connect(e.ctx, e.url)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// runFixture 建一个共享空间里、member 拥有的 SHARED 对话及其 RUNNING Run，返回空间、对话与 Run。
func (e *dvEnv) runFixture(members ...string) (string, string, string) {
	e.t.Helper()
	space := e.createSpace("shared")
	device := e.insertDevice(dvOwnerID, "ORGANIZATION", "")
	agent := e.insertAgent(space, dvMemberID, device, "SHARED")
	conversation := e.insertConversation(space, agent, "", dvMemberID, "SHARED", members...)
	return space, conversation, e.insertRun(conversation, dvMemberID, "RUNNING")
}

// 两个并发事务交错提交：T2 先取号（序号、事务号都更小）但后提交，T1 后取号先提交。
// 只按 stream_seq / id 推进的游标会在 T1 提交后越过 T2 的序号而漏发；按 (txid, 序号) 与快照水位投递则不漏不重。
func TestEventsConcurrentCommitsNoGapNoDuplicate(t *testing.T) {
	env := newDvEnv(t)
	_, _, run := env.runFixture()
	stream := env.openStream(dvMemberID, "")

	c1, c2 := env.connect(), env.connect()
	t2, err := c2.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer t2.Rollback(context.Background())
	t1, err := c1.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer t1.Rollback(context.Background())
	seq2 := insertRunEvent(t, env.ctx, t2, env.org, run, "cc-t2", 1)
	id2, txid2 := insertOrgEvent(t, env.ctx, t2, env.org, "cc-t2", nil, nil, nil)
	seq1 := insertRunEvent(t, env.ctx, t1, env.org, run, "cc-t1", 2)
	id1, txid1 := insertOrgEvent(t, env.ctx, t1, env.org, "cc-t1", nil, nil, nil)
	if !(seq2 < seq1 && id2 < id1) || txid2 == txid1 {
		t.Fatalf("unexpected numbering seq %d/%d id %d/%d txid %s/%s", seq2, seq1, id2, id1, txid2, txid1)
	}
	if err := t1.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	// T2 仍未提交：水位停在 T2 之前，T1 的事件也不能发出（否则游标越过 T2 的位置）。
	for i := 0; i < 3; i++ {
		stream.poll()
		time.Sleep(50 * time.Millisecond)
	}
	if got := stream.mine("cc-"); len(got) != 0 {
		t.Fatalf("events delivered before the older transaction committed: %v", got)
	}
	if err := t2.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	stream.until("org:cc-t1")
	stream.until("run:cc-t1")
	// 同一轮里 Run 事件先于组织事件；各自按事务号排序：T2 在前。
	runs, orgs := []string{}, []string{}
	for _, mark := range stream.mine("cc-") {
		if strings.HasPrefix(mark, "run:") {
			runs = append(runs, mark)
		} else {
			orgs = append(orgs, mark)
		}
	}
	if strings.Join(runs, ",") != "run:cc-t2,run:cc-t1" || strings.Join(orgs, ",") != "org:cc-t2,org:cc-t1" {
		t.Fatalf("runs=%v orgs=%v", runs, orgs)
	}
	// 再轮询几次也不会重复。
	for i := 0; i < 3; i++ {
		stream.poll()
	}
	if got := stream.mine("cc-"); len(got) != 4 {
		t.Fatalf("duplicates after more polls: %v", got)
	}
	// SSE id 严格递增（按两段游标比较）。
	for index := 1; index < len(stream.ids); index++ {
		previous, _ := executiondelivery.ParseStreamCursor(stream.ids[index-1])
		current, _ := executiondelivery.ParseStreamCursor(stream.ids[index])
		if cursorLess(current, previous) {
			t.Fatalf("cursor went backwards: %s -> %s", stream.ids[index-1], stream.ids[index])
		}
	}
}

func cursorLess(a, b executiondelivery.StreamCursor) bool {
	runLess := a.RunTxid < b.RunTxid || (a.RunTxid == b.RunTxid && a.RunSeq < b.RunSeq)
	orgLess := a.OrgTxid < b.OrgTxid || (a.OrgTxid == b.OrgTxid && a.OrgID < b.OrgID)
	return runLess || orgLess
}

// 长事务期间水位停住：其后提交的事件暂不投递；长事务提交后按事务号顺序补发，一条不漏。
func TestEventsLongTransactionHoldsWatermark(t *testing.T) {
	env := newDvEnv(t)
	stream := env.openStream(dvMemberID, "")
	long, err := env.connect().Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer long.Rollback(context.Background())
	insertOrgEvent(t, env.ctx, long, env.org, "lt-long", nil, nil, nil)
	for _, mark := range []string{"lt-a", "lt-b", "lt-c"} {
		env.autoTx(func(tx pgx.Tx) { insertOrgEvent(t, env.ctx, tx, env.org, mark, nil, nil, nil) })
	}
	for i := 0; i < 3; i++ {
		stream.poll()
		time.Sleep(50 * time.Millisecond)
	}
	if got := stream.mine("lt-"); len(got) != 0 {
		t.Fatalf("delivered while the long transaction was open: %v", got)
	}
	if err := long.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	stream.until("org:lt-c")
	if got := strings.Join(stream.mine("lt-"), ","); got != "org:lt-long,org:lt-a,org:lt-b,org:lt-c" {
		t.Fatalf("after commit = %s", got)
	}
	// 从最后一个 SSE id 恢复：只收到之后的事件。
	resumed := env.openStream(dvMemberID, stream.ids[len(stream.ids)-1])
	env.autoTx(func(tx pgx.Tx) { insertOrgEvent(t, env.ctx, tx, env.org, "lt-after", nil, nil, nil) })
	resumed.until("org:lt-after")
	if got := strings.Join(resumed.mine("lt-"), ","); got != "org:lt-after" {
		t.Fatalf("resumed = %s", got)
	}
}

// 可见性：定向事件只给收件人；空间事件按协作策略（SHARED 空间对非访客可读，私有空间要空间成员）；
// 对话事件与 Run 事件还要求对话可见（所有者或 SHARED 对话成员）；访客看不到 SHARED 空间。
func TestEventsVisibility(t *testing.T) {
	env := newDvEnv(t)
	shared, conversation, run := env.runFixture(dvMember2ID, dvGuestID)
	private := env.createSpace("private")
	env.addSpaceMember(private, dvMemberID, "MEMBER", "ACTIVE")
	// 私有空间里的共享对话：member2 是对话成员但不是空间成员，看不到。
	privateDevice := env.insertDevice(dvOwnerID, "ORGANIZATION", "")
	privateAgent := env.insertAgent(private, dvMemberID, privateDevice, "SHARED")
	privateConversation := env.insertConversation(private, privateAgent, "", dvMemberID, "SHARED", dvMember2ID)
	privateRun := env.insertRun(privateConversation, dvMemberID, "RUNNING")

	users := []string{dvOwnerID, dvAdminID, dvMemberID, dvMember2ID, dvGuestID}
	streams := map[string]*evStream{}
	for _, userID := range users {
		// 旧的纯数字游标：Run 事件从头扫描，组织事件从当前水位开始。
		streams[userID] = env.openStream(userID, "0")
	}
	env.autoTx(func(tx pgx.Tx) {
		insertOrgEvent(t, env.ctx, tx, env.org, "v-org", nil, nil, nil)
		insertOrgEvent(t, env.ctx, tx, env.org, "v-shared", shared, nil, nil)
		insertOrgEvent(t, env.ctx, tx, env.org, "v-private", private, nil, nil)
		insertOrgEvent(t, env.ctx, tx, env.org, "v-inbox", nil, nil, dvMemberID)
		insertOrgEvent(t, env.ctx, tx, env.org, "v-conversation", shared, conversation, nil)
		insertOrgEvent(t, env.ctx, tx, env.org, "v-private-conversation", private, privateConversation, nil)
		insertRunEvent(t, env.ctx, tx, env.org, run, "v-run", 1)
		insertRunEvent(t, env.ctx, tx, env.org, privateRun, "v-private-run", 1)
	})
	// 哨兵：组织可见，提交在上面之后；收到它时上面的事件都已扫描过。
	env.autoTx(func(tx pgx.Tx) { insertOrgEvent(t, env.ctx, tx, env.org, "v-end", nil, nil, nil) })
	want := map[string][]string{
		// owner 是两个空间的创建者（空间成员），但不是对话成员。
		dvOwnerID:   {"org:v-org", "org:v-shared", "org:v-private", "org:v-end"},
		dvAdminID:   {"org:v-org", "org:v-shared", "org:v-end"},
		dvMemberID:  {"org:v-org", "org:v-shared", "org:v-private", "org:v-inbox", "org:v-conversation", "org:v-private-conversation", "run:v-run", "run:v-private-run", "org:v-end"},
		dvMember2ID: {"org:v-org", "org:v-shared", "org:v-conversation", "run:v-run", "org:v-end"},
		dvGuestID:   {"org:v-org", "org:v-end"},
	}
	for _, userID := range users {
		stream := streams[userID]
		stream.until("org:v-end")
		got := stream.mine("v-")
		sort.Strings(got)
		expected := append([]string{}, want[userID]...)
		sort.Strings(expected)
		if strings.Join(got, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s got %v, want %v", dvRoles[userID], got, expected)
		}
	}
}

// 经 HTTP 的 SSE：帧格式（id / event / data）与 after 参数。
func TestEventsSSEHandlerFrames(t *testing.T) {
	env := newDvEnv(t)
	member := env.client(dvMemberID)
	start := env.openStream(dvMemberID, "")
	ctx, cancel := context.WithTimeout(env.ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		env.server.URL+"/v1/organizations/"+env.org+"/events?after="+start.cursor.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "invalid-ignored-when-after-is-set")
	response, err := member.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	env.autoTx(func(tx pgx.Tx) { insertOrgEvent(t, env.ctx, tx, env.org, "sse-1", nil, nil, nil) })
	reader := bufio.NewReader(response.Body)
	frame := map[string]string{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v (frame %v)", err, frame)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			if strings.Contains(frame["data"], `"sse-1"`) {
				break
			}
			frame = map[string]string{}
			continue
		}
		if key, value, ok := strings.Cut(line, ": "); ok {
			frame[key] = value
		}
	}
	var data struct {
		Type    string          `json:"type"`
		Entity  string          `json:"entity"`
		ID      *string         `json:"id"`
		Version *int64          `json:"version"`
		Space   *string         `json:"space_id"`
		Conv    *string         `json:"conversation_id"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(frame["data"]), &data); err != nil {
		t.Fatal(err)
	}
	cursor, err := executiondelivery.ParseStreamCursor(frame["id"])
	if err != nil || !strings.HasPrefix(frame["id"], "v2.") || cursor.OrgID == 0 {
		t.Fatalf("id = %q (%v)", frame["id"], err)
	}
	if frame["event"] != "task.changed" || data.Type != "task.changed" || data.Entity != "task" || data.ID != nil || data.Version != nil ||
		data.Space != nil || data.Conv != nil || !strings.Contains(frame["data"], `"space_id":null`) {
		t.Fatalf("frame = %v", frame)
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "events?after=v2.x", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "events?after=-1", nil)
}

// A client can cause a change as soon as EventSource.onopen fires.
func TestEventsSSEInitialWatermark(t *testing.T) {
	env := newDvEnv(t)
	member := env.client(dvMemberID)
	for _, tc := range []struct{ name, query string }{{"new", ""}, {"legacy", "?after=0"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(env.ctx, 8*time.Second)
			defer cancel()
			open := func(lastID string) *http.Response {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.server.URL+"/v1/organizations/"+env.org+"/events"+tc.query, nil)
				if err != nil {
					t.Fatal(err)
				}
				if lastID != "" {
					req.Header.Set("Last-Event-ID", lastID)
				}
				resp, err := member.http.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { resp.Body.Close() })
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("opening stream: %s", resp.Status)
				}
				return resp
			}
			readMark := func(resp *http.Response, want string) string {
				t.Helper()
				scan := bufio.NewScanner(resp.Body)
				var id string
				for scan.Scan() {
					line := scan.Text()
					if value, ok := strings.CutPrefix(line, "id: "); ok {
						id = value
					}
					if strings.HasPrefix(line, "data: ") && strings.Contains(line, tc.name+"-startup-") {
						if !strings.Contains(line, want) || id == "" {
							t.Fatalf("unexpected SSE event %s, id %s", line, id)
						}
						return id
					}
				}
				t.Fatalf("missing %s: %v", want, scan.Err())
				return ""
			}
			write := func(suffix string) {
				env.autoTx(func(tx pgx.Tx) {
					insertOrgEvent(t, env.ctx, tx, env.org, tc.name+"-startup-"+suffix, nil, nil, dvMemberID)
				})
			}
			write("old")
			response := open("")
			write("just-after-open")
			id := readMark(response, tc.name+"-startup-just-after-open")
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.query == "" {
				write("disconnected")
				resumed := open(id)
				readMark(resumed, tc.name+"-startup-disconnected")
			}
		})
	}
}

// 保留期清理：超过 7 天的事件（含定向事件）被删除，新事件保留。
func TestEventsRetentionPurge(t *testing.T) {
	env := newDvEnv(t)
	env.exec(`INSERT INTO openfox.organization_events (organization_id, type, entity, recipient_user_id, created_at, payload)
		VALUES ($1::uuid, 'inbox.changed', 'notification', $2::uuid, clock_timestamp() - interval '8 days', '{"mark":"old-inbox"}'),
		       ($1::uuid, 'task.changed', 'task', NULL, clock_timestamp() - interval '8 days', '{"mark":"old-task"}'),
		       ($1::uuid, 'task.changed', 'task', NULL, clock_timestamp() - interval '6 days', '{"mark":"recent"}')`,
		env.org, dvMemberID)
	worker := &dispatch.Worker{Pool: env.pool}
	deleted, err := worker.PurgeOrganizationEvents(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 2 {
		t.Fatalf("deleted = %d", deleted)
	}
	var remaining []string
	// 只看本用例写入的事件：夹具建成员时还会产生 member.changed。
	rows, err := env.superuser.Query(env.ctx, `SELECT payload->>'mark' FROM openfox.organization_events
		WHERE organization_id = $1::uuid AND payload ? 'mark' ORDER BY id`, env.org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var mark string
		if err := rows.Scan(&mark); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, mark)
	}
	rows.Close()
	if strings.Join(remaining, ",") != "recent" {
		t.Fatalf("remaining = %v", remaining)
	}
}
