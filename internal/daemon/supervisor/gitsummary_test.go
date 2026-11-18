package supervisor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

// 阶段 12b：运行结束后 daemon 只读采集 Git 摘要并随结果上报。

func commitInWorkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "made.txt"), []byte("a\nb\n"), 0o600); err != nil {
		t.Error(err)
		return
	}
	for _, args := range [][]string{{"add", "made.txt"}, {"commit", "-q", "-m", "agent commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("git %v: %v %s", args, err, out)
			return
		}
	}
	// 再留一个未提交改动。
	if err := os.WriteFile(filepath.Join(dir, "left.txt"), []byte("x"), 0o600); err != nil {
		t.Error(err)
	}
}

func decodeSummary(t *testing.T, raw json.RawMessage) workdir.GitSummary {
	t.Helper()
	var s workdir.GitSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("git_summary = %s: %v", raw, err)
	}
	return s
}

func TestSupervisorReportsGitSummaryAndUnverified(t *testing.T) {
	repo := gitRepository(t)
	center := newCenter()
	adapter := &fakeAdapter{onStart: func(request runtime.RunRequest) { commitInWorkdir(t, request.WorkingDirectory) }}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	result := center.snapshot().results[0]
	if result.GitSummary == nil {
		t.Fatalf("missing git_summary: %+v", result)
	}
	s := decodeSummary(t, result.GitSummary)
	if !s.Repo || !s.Available || s.CommitTotal != 1 || len(s.Commits) != 1 || s.Commits[0].Title != "agent commit" {
		t.Fatalf("summary = %+v", s)
	}
	if s.Uncommitted != 1 || s.Impact == nil || s.Impact.Files != 1 || s.Impact.Added != 2 {
		t.Fatalf("summary = %+v impact=%+v", s, s.Impact)
	}
	if !result.Unverified || result.BranchRef == "" || result.BranchRef != s.Branch {
		t.Fatalf("result = %+v", result)
	}
	var checks []map[string]any
	if err := json.Unmarshal(result.Checks, &checks); err != nil || len(checks) != 1 || checks[0]["reason"] != workdir.UnverifiedUncommitted {
		t.Fatalf("checks = %s", result.Checks)
	}
}

func TestSupervisorGitSummaryFallsBackForOldCenter(t *testing.T) {
	repo := gitRepository(t)
	center := newCenter()
	center.rejectGitSummary = true
	adapter := &fakeAdapter{onStart: func(request runtime.RunRequest) { commitInWorkdir(t, request.WorkingDirectory) }}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	result := center.snapshot().results[0]
	if result.GitSummary != nil || result.Status != "SUCCEEDED" || !result.Unverified || result.BranchRef == "" {
		t.Fatalf("old-center result = %+v", result)
	}
}

func TestSupervisorNoRepoDirectoryReportsNoRepo(t *testing.T) {
	center := newCenter()
	newHarness(t, center, &fakeAdapter{}, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	result := center.snapshot().results[0]
	s := decodeSummary(t, result.GitSummary)
	if s.Repo || !s.Available || result.Unverified || result.BranchRef != "" {
		t.Fatalf("no-repo result = %+v summary=%+v", result, s)
	}
}
