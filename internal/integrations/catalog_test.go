package integrations

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"openfox/internal/policy"
	"openfox/internal/secrets"
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
		if p.Bindable && !slices.Contains([]string{"feishu", "lark", "dingtalk", "wecom", "tapd", "teambition", "pingcode", "yunxiao", "codeup", "gitcode", "gongfeng", "codecommit", "cnb", "asana", "zoho_bugtracker", "shortcut", "jira", "zentao", "bugly", "slack", "teams", "discord", "qq", "telegram"}, p.ID) {
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
	want := []string{"bugly", "jira", "tapd", "zentao", "teambition", "pingcode", "yunxiao", "asana", "zoho_bugtracker", "shortcut", "github", "cnb", "gitlab_cn", "gitlab", "gitee", "codeup", "gitcode", "gongfeng", "bitbucket", "codecommit", "azure_devops", "feishu", "lark", "dingtalk", "wecom", "wechat", "slack", "teams", "discord", "qq", "telegram"}
	if strings.Join(available, ",") != strings.Join(want, ",") {
		t.Fatalf("available = %v, want %v", available, want)
	}
	for id, want := range map[string]string{"github": CategoryGit, "gitlab": CategoryGit, "jira": CategoryTask, "yunxiao": CategoryTask, "codeup": CategoryGit, "gitcode": CategoryGit, "slack": CategoryCollaboration} {
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
	box, err := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"jira", "zentao", "yunxiao", "gitcode"} {
		p, _ := Lookup(id)
		if v := (Service{}).providerView(p, ProviderSummary{}); v.Configured || v.Bindable {
			t.Fatalf("%s binding requires encryption", id)
		}
		if v := (Service{Box: box}).providerView(p, ProviderSummary{}); !v.Configured || !v.Bindable {
			t.Fatalf("%s binding not enabled with encryption", id)
		}
	}
}

func TestGitCodeCatalogDoesNotAdvertisePersonalOrClone(t *testing.T) {
	p, ok := Lookup(ProviderGitCode)
	if !ok || !p.Bindable || p.Category != CategoryGit || slices.Contains(p.Capabilities, capPersonal) {
		t.Fatal(p)
	}
	if !slices.Contains(p.Capabilities, capSync) || !slices.Contains(p.Capabilities, capPRLink) {
		t.Fatal(p)
	}
}

func TestDedicatedIMCatalogConfiguration(t *testing.T) {
	box, err := secrets.New(bytes.Repeat([]byte{2}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qq", "teams", "discord"} {
		p, ok := Lookup(id)
		if !ok || p.Availability != AvailabilityAvailable || !slices.Contains(p.Capabilities, capPersonal) || !slices.Contains(p.Capabilities, capAgentBinding) || slices.Contains(p.Capabilities, capIMNotify) || IsIMProvider(id) {
			t.Fatalf("unsupported provider contract %+v", p)
		}
		if id == "qq" && slices.Contains(p.Capabilities, capTaskAssistant) {
			t.Fatal("QQ task assistant must not be advertised")
		}
		if v := (Service{}).providerView(p, ProviderSummary{}); v.Configured || v.Bindable {
			t.Fatal("missing secret box", id)
		}
		v := (Service{Box: box}).providerView(p, ProviderSummary{})
		if v.Configured != (id == "discord") {
			t.Fatalf("public URL requirement %s %+v", id, v)
		}
		v = (Service{Box: box, PublicURL: "https://openfox.example.test"}).providerView(p, ProviderSummary{})
		if !v.Configured || !v.Bindable || v.OAuthRedirectURI != "" {
			t.Fatalf("configured dedicated flow %s %+v", id, v)
		}
	}
}
