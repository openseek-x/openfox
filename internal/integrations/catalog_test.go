package integrations

import (
	"slices"
	"strings"
	"testing"

	"openfox/internal/policy"
)

func TestCatalogUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Catalog() {
		if seen[p.ID] {
			t.Fatalf("provider %s appears twice; multi-group providers must be deduplicated", p.ID)
		}
		seen[p.ID] = true
		if p.Name == "" || len(p.Capabilities) == 0 {
			t.Fatalf("provider %s is incomplete: %+v", p.ID, p)
		}
		// 可绑定的协作和任务服务商由目录标记；GitHub 的可绑定状态由服务端配置决定（providerView）。
		if p.Bindable && !slices.Contains([]string{"feishu", "lark", "dingtalk", "wecom", "tapd", "teambition", "pingcode", "cnb", "shortcut"}, p.ID) {
			t.Fatalf("provider %s must not be bindable in the static catalog", p.ID)
		}
	}
	for _, id := range []string{"github", "gitlab_cn", "gitlab", "gitee", "yunxiao", "codeup"} {
		if !seen[id] {
			t.Fatalf("missing provider %s", id)
		}
	}
	if seen["github_issues"] {
		t.Fatal("github_issues must be merged into github")
	}
}

func TestCatalogAvailabilityAndCategories(t *testing.T) {
	available := []string{}
	for _, p := range Catalog() {
		if p.Availability == AvailabilityAvailable {
			available = append(available, p.ID)
		}
	}
	// / 7g-3：GitLab（极狐、自部署）与 Gitee 已开放。
	want := []string{"jira", "tapd", "zentao", "teambition", "pingcode", "shortcut", "github", "cnb", "gitlab_cn", "gitlab", "gitee", "feishu", "lark", "dingtalk", "wecom", "wechat"}
	if strings.Join(available, ",") != strings.Join(want, ",") {
		t.Fatalf("available = %v, want %v", available, want)
	}
	for id, want := range map[string]string{"github": CategoryGit, "gitlab": CategoryGit, "jira": CategoryTask, "yunxiao": CategoryTask, "codeup": CategoryGit, "slack": CategoryCollaboration} {
		p, _ := Lookup(id)
		if p.Category != want {
			t.Fatalf("%s category = %s, want %s", id, p.Category, want)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown provider must not be found")
	}
}

func TestLookupReturnsCopy(t *testing.T) {
	p, _ := Lookup("jira")
	p.Capabilities[0] = "changed"
	q, _ := Lookup("jira")
	if q.Capabilities[0] != "sync" {
		t.Fatal("Lookup must return a copy of capabilities")
	}
}

func TestSummaryStatusAndPhraseAndManage(t *testing.T) {
	if summaryStatus(true, true) != "attention" || summaryStatus(false, true) != "ok" || summaryStatus(false, false) != "none" {
		t.Fatal("summaryStatus precedence is wrong")
	}
	if ConfirmPhrase("Jira") != "断开 Jira" {
		t.Fatalf("phrase = %q", ConfirmPhrase("Jira"))
	}
	if !CanManage(policy.OrganizationRoleOwner) || !CanManage(policy.OrganizationRoleAdmin) ||
		CanManage(policy.OrganizationRoleMember) || CanManage(policy.OrganizationRoleGuest) {
		t.Fatal("CanManage must be owner/admin only")
	}
	view := (Service{GitHubConfigured: false}).providerView(Provider{ID: "github"}, ProviderSummary{})
	if view.Configured {
		t.Fatal("github must be unconfigured when the server is not configured")
	}
	if !(Service{}).providerView(Provider{ID: "jira"}, ProviderSummary{}).Configured {
		t.Fatal("other providers are always configured")
	}
}
