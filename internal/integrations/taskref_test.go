package integrations

import (
	"reflect"
	"strings"
	"testing"
)

func refKeys(refs []TaskRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.Prefix+"-"+ref.MatchedIn+"-"+itoa(ref.Sequence))
	}
	return keys
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestExtractTaskRefs(t *testing.T) {
	cases := []struct {
		name    string
		sources []RefSource
		want    []string
	}{
		{"title", []RefSource{{"title", "Fix login FOX-12"}}, []string{"fox-title-12"}},
		{"case insensitive and dedupe", []RefSource{{"title", "fox-12 and Fox-12 and FOX-12"}}, []string{"fox-title-12"}},
		{"first hit position wins", []RefSource{{"title", "FOX-1"}, {"body", "FOX-1 FOX-2"}, {"branch", "lin/fox-3-x"}},
			[]string{"fox-title-1", "fox-body-2", "fox-branch-3"}},
		{"branch with slash", []RefSource{{"branch", "lin/fox-7-fix-bug"}}, []string{"fox-branch-7"}},
		{"branch without username", []RefSource{{"branch", "fox-7-fix"}}, []string{"fox-branch-7"}},
		{"utf8 body", []RefSource{{"body", "需求（FOX-5）已完成，见 项目-9"}}, []string{"fox-body-5"}},
		{"url number", []RefSource{{"body", "see https://example.com/issues/123 and v1.2-3"}}, []string{}},
		{"embedded in word", []RefSource{{"title", "prefoxFOX-1x"}}, []string{}},
		{"digit suffix too long", []RefSource{{"title", "FOX-1234567890"}}, []string{}},
		{"underscore prefix", []RefSource{{"title", "my_space-4"}}, []string{"my_space-title-4"}},
		{"none", []RefSource{{"title", "no reference here"}}, []string{}},
	}
	for _, c := range cases {
		got := refKeys(ExtractTaskRefs(c.sources...))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExtractTaskRefsBounds(t *testing.T) {
	var text strings.Builder
	for i := 1; i <= 200; i++ {
		text.WriteString("FOX-" + itoa(int64(i)) + " ")
	}
	refs := ExtractTaskRefs(RefSource{Name: "body", Text: text.String()})
	if len(refs) != maxRawCandidates {
		t.Fatalf("candidates = %d, want %d", len(refs), maxRawCandidates)
	}
	// 描述只看前 64 KiB。
	long := strings.Repeat("a ", maxBodyScanBytes) + "FOX-1"
	if got := ExtractTaskRefs(BodySource(long)); len(got) != 0 {
		t.Fatalf("reference beyond 64 KiB must be ignored, got %v", got)
	}
	// 截断落在多字节字符中间时保持 UTF-8 合法。
	multi := strings.Repeat("中", maxBodyScanBytes)
	if source := BodySource(multi); !strings.HasSuffix(source.Text, "中") || len(source.Text) > maxBodyScanBytes {
		t.Fatalf("truncated body is invalid: %d bytes", len(source.Text))
	}
}

func TestMatchTasksLimit(t *testing.T) {
	refs := make([]TaskRef, 0, 15)
	resolved := map[string]resolvedTask{}
	for i := int64(1); i <= 15; i++ {
		refs = append(refs, TaskRef{Prefix: "fox", Sequence: i, MatchedIn: "title"})
		if i != 2 {
			resolved["fox-"+itoa(i)] = resolvedTask{TaskID: itoa(i)}
		}
	}
	matched := matchTasks(refs, resolved)
	if len(matched) != MaxTaskRefsPerObject || matched[0].TaskID != "1" || matched[1].TaskID != "3" {
		t.Fatalf("matched = %+v", matched)
	}
}

func TestBacklinkBody(t *testing.T) {
	body := BacklinkBody("https://openfox.example.com/", "org1", []BacklinkItem{
		{DisplayID: "FOX-1", SpaceID: "sp1"}, {DisplayID: "FOX-2", SpaceID: "sp2"}})
	want := "FOX-1 已关联到 OpenFox：https://openfox.example.com/o/org1/spaces/sp1/tasks?task=FOX-1\n" +
		"FOX-2 已关联到 OpenFox：https://openfox.example.com/o/org1/spaces/sp2/tasks?task=FOX-2"
	if body != want {
		t.Fatalf("body = %q", body)
	}
}

func TestPRState(t *testing.T) {
	merged := &ghPullRequest{State: "closed", Merged: true}
	closed := &ghPullRequest{State: "closed"}
	draft := &ghPullRequest{State: "open", Draft: true}
	open := &ghPullRequest{State: "open"}
	for pr, want := range map[*ghPullRequest]string{merged: "MERGED", closed: "CLOSED", draft: "DRAFT", open: "OPEN"} {
		if got := prState(pr); got != want {
			t.Errorf("state = %s, want %s", got, want)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	values := DefaultSettings()
	if !values.Valid() || !values.LinkPullRequests || values.LinkCommits || !values.PRBacklinkPrivate || values.PRBacklinkPublic {
		t.Fatalf("defaults = %+v", values)
	}
	values.BranchFormat = "other"
	if values.Valid() {
		t.Fatal("unknown branch format accepted")
	}
}

func TestValidSHA(t *testing.T) {
	for sha, want := range map[string]bool{"abcdef1": true, "ABCDEF1": false, "abc": false, "zzzzzzz": false,
		strings.Repeat("a", 40): true, strings.Repeat("a", 65): false} {
		if validSHA(sha) != want {
			t.Errorf("validSHA(%q) != %v", sha, want)
		}
	}
}
