package dispatch

import (
	"reflect"
	"testing"

	"openfox/internal/reposource"
)

func TestRepositoryURLKey(t *testing.T) {
	for raw, want := range map[string]string{
		"https://GitHub.com/acme/app.git":   "https://github.com/acme/app",
		"https://github.com/acme/app/":      "https://github.com/acme/app",
		" https://github.com/acme/app.git/": "https://github.com/acme/app",
		"https://github.com/Acme/App":       "https://github.com/Acme/App",
		"not a url":                         "",
	} {
		if got := repositoryURLKey(raw); got != want {
			t.Errorf("repositoryURLKey(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestMergeRepositoryBranches(t *testing.T) {
	agent := []reposource.Repository{
		{URL: "https://github.com/acme/app", Branch: "agent-branch"},
		{URL: "https://github.com/acme/lib.git"},
		{URL: "https://github.com/acme/other"},
	}
	space := []reposource.Repository{
		{URL: "https://github.com/acme/app", Branch: "space-app"},
		{URL: "https://GITHUB.com/acme/lib", Branch: ""},
		{URL: "https://github.com/acme/lib/", Branch: "space-lib"},
		{URL: "https://github.com/acme/space-only", Branch: "main"},
	}
	got := mergeRepositoryBranches(agent, space)
	want := []reposource.Repository{
		// Agent 自己的分支优先。
		{URL: "https://github.com/acme/app", Branch: "agent-branch"},
		// 同 URL（规范化后）且分支非空的空间设置补上。
		{URL: "https://github.com/acme/lib.git", Branch: "space-lib"},
		// 空间没有同 URL 仓库：远程默认分支。
		{URL: "https://github.com/acme/other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %+v, want %+v", got, want)
	}
	// 不修改输入，也不把空间独有的仓库加进来。
	if agent[1].Branch != "" || len(got) != len(agent) {
		t.Fatal("merge mutated the agent repositories or added space-only repositories")
	}
}
