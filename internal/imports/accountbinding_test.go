package imports

import "testing"

func TestSameSiteAndHost(t *testing.T) {
	if !SameSite("https://Acme.atlassian.net", "https://acme.atlassian.net") {
		t.Fatal("站点比较应不区分大小写")
	}
	if SameSite("https://acme.atlassian.net", "https://acme.atlassian.net/jira") {
		t.Fatal("路径前缀不同不是同一站点")
	}
	if got := SiteHost("https://jira.example.com/jira"); got != "jira.example.com/jira" {
		t.Fatalf("host = %q", got)
	}
}

func TestEmailMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Alice@X.test", " alice@x.test ", true},
		{"alice@x.test", "bob@x.test", false},
		{"", "", false},
		{"alice@x.test", "", false},
	}
	for _, c := range cases {
		if got := EmailMatches(c.a, c.b); got != c.want {
			t.Fatalf("EmailMatches(%q,%q) = %v", c.a, c.b, got)
		}
	}
}

func TestSuggestMemberVerifiedFirst(t *testing.T) {
	l := lookups{
		members:      []memberRef{{ID: "u1", FullName: "Alice", Email: "alice@x.test"}, {ID: "u2", FullName: "Bob", Email: "bob@x.test"}},
		jiraVerified: map[string]string{"acc-1": "u2"},
	}
	// 已验证的标识优先于邮箱匹配。
	if id, reason := l.suggestMember("ACC-1", "Alice", "alice@x.test"); id == nil || *id != "u2" || reason != "verified" {
		t.Fatalf("verified 优先: %v %q", id, reason)
	}
	if id, reason := l.suggestMember("other", "Alice", "alice@x.test"); id == nil || *id != "u1" || reason != "email" {
		t.Fatalf("其余回退: %v %q", id, reason)
	}
}
