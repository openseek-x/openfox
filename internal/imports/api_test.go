package imports

import (
	"encoding/json"
	"testing"
	"time"

	"openfox/internal/imports/jiraapi"
)

func TestParseSourceKey(t *testing.T) {
	for _, tc := range []struct {
		key    string
		prefix string
		number int64
		ok     bool
	}{
		{"PRJ-123", "PRJ", 123, true}, {" prj-7 ", "prj", 7, true}, {"A-B-12", "A-B", 12, true},
		{"PRJ", "", 0, false}, {"PRJ-", "", 0, false}, {"-5", "", 0, false}, {"PRJ-0", "", 0, false}, {"PRJ-x", "", 0, false},
	} {
		prefix, number, ok := ParseSourceKey(tc.key)
		if prefix != tc.prefix || number != tc.number || ok != tc.ok {
			t.Fatalf("%q => %q %d %v", tc.key, prefix, number, ok)
		}
	}
}

func TestDominantPrefix(t *testing.T) {
	if got := dominantPrefix([]string{"zed-1", "ZED-2", "OTH-3", "bad", ""}); got != "ZED" {
		t.Fatalf("got %q", got)
	}
	if got := dominantPrefix([]string{"A-1", "B-2"}); got != "A" {
		t.Fatalf("并列时取先出现的: %q", got)
	}
	if got := dominantPrefix([]string{"x", "y"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeNewSpace(t *testing.T) {
	ns, err := NormalizeNewSpace(NewSpace{Name: " Space ", Prefix: "ab1"}, "")
	if err != nil || ns.Name != "Space" || ns.Prefix != "AB1" || ns.Visibility != "PRIVATE" {
		t.Fatalf("ns = %+v err = %v", ns, err)
	}
	// 默认前缀：合法才采用，不合法留空。
	if ns, _ := NormalizeNewSpace(NewSpace{Name: "S"}, "proj"); ns.Prefix != "PROJ" {
		t.Fatalf("default prefix = %q", ns.Prefix)
	}
	if ns, _ := NormalizeNewSpace(NewSpace{Name: "S", Visibility: "shared"}, "TOO_LONG_KEY"); ns.Prefix != "" || ns.Visibility != "SHARED" {
		t.Fatalf("ns = %+v", ns)
	}
	if _, err := NormalizeNewSpace(NewSpace{Name: "S", Prefix: "a"}, ""); err == nil {
		t.Fatal("过短的前缀应被拒绝")
	}
	if _, err := NormalizeNewSpace(NewSpace{Name: " "}, ""); err == nil {
		t.Fatal("空名称应被拒绝")
	}
	if _, err := NormalizeNewSpace(NewSpace{Name: "S", Visibility: "PUBLIC"}, ""); err == nil {
		t.Fatal("未知可见性应被拒绝")
	}
}

func TestBuildRecordAndRow(t *testing.T) {
	var issue jiraapi.Issue
	raw := `{"id":"10001","key":"PRJ-1","fields":{
		"summary":"Title","status":{"name":"Closed","statusCategory":{"key":"done"}},"issuetype":{"name":"Bug"},
		"priority":{"name":"High"},"created":"2026-01-02T03:04:05.000+0800","labels":["a","b"],"description":"wiki *text*",
		"parent":{"id":"10000"},"assignee":{"accountId":"acc-1","displayName":"Alice","emailAddress":"a@x.test"},
		"customfield_1":3.5,"customfield_2":"2026-01-05",
		"customfield_3":[{"id":7,"name":"S7","state":"closed","startDate":"2026-01-05T09:00:00.000Z","endDate":"2026-01-19T09:00:00.000Z"}],
		"comment":{"total":1,"comments":[{"id":"1","author":{"accountId":"acc-2","displayName":"Bob","emailAddress":"b@x.test"},"body":"hi","created":"2026-01-04T00:00:00.000+0800"}]}}}`
	if err := json.Unmarshal([]byte(raw), &issue); err != nil {
		t.Fatal(err)
	}
	rec := BuildRecord(issue, FieldIDs{Points: "customfield_1", Start: "customfield_2", Sprint: "customfield_3"}, "https://jira.test/jira/")
	if rec.URL != "https://jira.test/jira/browse/PRJ-1" || rec.Points != "3.5" || rec.Start != "2026-01-05" || rec.ParentID != "10000" ||
		rec.Labels != "a b" || len(rec.Sprints) != 1 || len(rec.Comments) != 1 || len(rec.People) != 1 {
		t.Fatalf("rec = %+v", rec)
	}
	d := rowFromRecord(&rec)
	if d.SourceID != "10001" || d.SourceKey != "PRJ-1" || d.Assignee.ID != "acc-1" || d.Assignee.Email != "a@x.test" ||
		len(d.LabelCells) != 1 || d.StatusCategory != "done" || !rowDone(d) {
		t.Fatalf("row = %+v", d)
	}
	pc := ParseCommentCell(d.CommentCells[0], nil)
	if !pc.HasAt || pc.Author != "acc-2" || pc.Body != "hi" {
		t.Fatalf("comment = %+v", pc)
	}
	// 评论作者的姓名与邮箱可通过 People 查到。
	users := collectUsers([]rowInfo{{Data: d}}, nil)
	byID := map[string]AnalysisUser{}
	for _, u := range users {
		byID[u.SourceID] = u
	}
	if byID["acc-2"].Email != "b@x.test" || byID["acc-2"].Name != "Bob" || byID["acc-1"].Count != 1 {
		t.Fatalf("users = %+v", users)
	}
}

func TestRowDone(t *testing.T) {
	cases := []struct {
		d    rowData
		want bool
	}{
		{rowData{Status: "Anything", StatusCategory: "done"}, true},
		{rowData{Status: "Anything", StatusCategory: "new"}, false},
		{rowData{Status: "Anything", StatusCategory: "indeterminate"}, false},
		{rowData{Status: "Won't Do", StatusCategory: "new"}, true}, // 取消类状态名
		{rowData{Status: "Done"}, true},                            // 没有类别时按名称
		{rowData{Status: "Doing"}, false},
	}
	for _, tc := range cases {
		if got := rowDone(tc.d); got != tc.want {
			t.Fatalf("%+v => %v", tc.d, got)
		}
	}
}

func TestSuggestMemberByEmail(t *testing.T) {
	l := lookups{members: []memberRef{{ID: "u1", FullName: "Alice Liu", Email: "alice@x.test"}, {ID: "u2", FullName: "Bob", Email: "Bob.K@X.test"}}}
	if id, _ := l.suggestMember("acc", "Someone", "ALICE@x.TEST"); id == nil || *id != "u1" {
		t.Fatalf("邮箱不区分大小写: %v", id)
	}
	// 邮箱优先于姓名。
	if id, _ := l.suggestMember("acc", "Alice Liu", "bob.k@x.test"); id == nil || *id != "u2" {
		t.Fatalf("邮箱优先: %v", id)
	}
	// 邮箱匹配不上再按姓名。
	if id, reason := l.suggestMember("acc", "Alice Liu", "nobody@x.test"); id == nil || *id != "u1" || reason != "name" {
		t.Fatalf("回退到姓名: %v %q", id, reason)
	}
	if id, _ := l.suggestMember("acc", "Nobody", ""); id != nil {
		t.Fatalf("不应匹配: %v", *id)
	}
}

func TestPickIteration(t *testing.T) {
	mapped := map[string]string{"1": "it-1", "2": "it-2", "3": "it-3"}
	sprints := []jiraapi.Sprint{
		{ID: "1", State: "CLOSED", Start: "2026-01-01T00:00:00.000Z"},
		{ID: "2", State: "CLOSED", Start: "2026-02-01T00:00:00.000Z"},
		{ID: "9", State: "ACTIVE", Start: "2026-03-01T00:00:00.000Z"}, // 没有映射
	}
	if got := pickIteration(sprints, mapped, time.UTC); got != "it-2" {
		t.Fatalf("开始最晚: %q", got)
	}
	sprints = append(sprints, jiraapi.Sprint{ID: "3", State: "ACTIVE", Start: "2025-12-01T00:00:00.000Z"})
	if got := pickIteration(sprints, mapped, time.UTC); got != "it-3" {
		t.Fatalf("优先进行中: %q", got)
	}
	if got := pickIteration(nil, mapped, time.UTC); got != "" {
		t.Fatalf("无 Sprint: %q", got)
	}
}

func TestProjectJQL(t *testing.T) {
	if got := ProjectJQL("PRJ", " "); got != `project = "PRJ"` {
		t.Fatal(got)
	}
	if got := ProjectJQL("PRJ", "status = Open ORDER BY created DESC"); got != `project = "PRJ" AND (status = Open) ORDER BY created DESC` {
		t.Fatalf("order by: %s", got)
	}
	if got := ProjectJQL("PRJ", "order by key"); got != `project = "PRJ" ORDER BY key` {
		t.Fatalf("order only: %s", got)
	}
	if got := ProjectJQL("PRJ", "status = Open"); got != `project = "PRJ" AND (status = Open)` {
		t.Fatal(got)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_\x`); got != `50\%\_\\x` {
		t.Fatal(got)
	}
}
