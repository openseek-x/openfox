package workdir

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func summaryRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	return repo, git("rev-parse", "HEAD")
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", name}, {"commit", "-q", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestSummaryNewCommitsAndUncommitted(t *testing.T) {
	repo, base := summaryRepo(t)
	commitFile(t, repo, "b.txt", "x\ny\n", "add b")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("u"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := CollectGitSummary(context.Background(), nil, repo, true, base, nil)
	if !s.Available || !s.Repo || s.Branch != "main" || s.Detached {
		t.Fatalf("summary = %+v", s)
	}
	if s.CommitTotal != 1 || len(s.Commits) != 1 || s.Commits[0].Title != "add b" {
		t.Fatalf("commits = %+v total=%d", s.Commits, s.CommitTotal)
	}
	if s.Uncommitted != 2 {
		t.Fatalf("uncommitted = %d, want 2 (modified + untracked)", s.Uncommitted)
	}
	if s.Impact == nil || s.Impact.Files != 2 || s.Impact.Added != 3 || s.Impact.Deleted != 0 {
		t.Fatalf("impact = %+v", s.Impact)
	}
	if got := s.Unverified(); len(got) != 1 || got[0] != UnverifiedUncommitted {
		t.Fatalf("unverified = %v", got)
	}
}

func TestSummaryCleanTreeHasNoUnverified(t *testing.T) {
	repo, base := summaryRepo(t)
	s := CollectGitSummary(context.Background(), nil, repo, true, base, nil)
	if !s.Available || s.CommitTotal != 0 || s.Uncommitted != 0 || len(s.Unverified()) != 0 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSummaryDetachedHead(t *testing.T) {
	repo, base := summaryRepo(t)
	cmd := exec.Command("git", "checkout", "-q", "--detach")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := CollectGitSummary(context.Background(), nil, repo, true, base, nil)
	if !s.Available || !s.Detached || s.Branch != "" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSummaryNoRepoAndMissingBaseline(t *testing.T) {
	s := CollectGitSummary(context.Background(), nil, t.TempDir(), false, "", nil)
	if s.Repo || !s.Available || len(s.Unverified()) != 0 {
		t.Fatalf("no repo summary = %+v", s)
	}
	repo, _ := summaryRepo(t)
	s = CollectGitSummary(context.Background(), nil, repo, true, "", nil)
	if !s.Repo || s.Available || s.UnavailableReason != SummaryNoBaseline {
		t.Fatalf("missing baseline summary = %+v", s)
	}
	if got := s.Unverified(); len(got) != 1 || got[0] != UnverifiedNoSummary {
		t.Fatalf("unverified = %v", got)
	}
}

func TestSummaryTruncatesCommitsAndPaths(t *testing.T) {
	repo, base := summaryRepo(t)
	for i := 0; i < 25; i++ {
		commitFile(t, repo, "f"+strings.Repeat("x", i%3)+string(rune('a'+i))+".txt", "c\n", "commit "+string(rune('a'+i)))
	}
	for i := 0; i < 60; i++ {
		name := "p" + string(rune('A'+i/26)) + string(rune('a'+i%26)) + ".txt"
		if err := os.WriteFile(filepath.Join(repo, name), []byte("z"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 未跟踪文件不进入 diff 统计；把它们加入索引后才会计入。
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := CollectGitSummary(context.Background(), nil, repo, true, base, nil)
	if s.CommitTotal != 25 || len(s.Commits) != MaxSummaryCommits {
		t.Fatalf("commits total=%d len=%d", s.CommitTotal, len(s.Commits))
	}
	if s.Impact == nil || s.Impact.Files != 85 || len(s.Impact.Paths) != MaxSummaryPaths || s.Impact.PathsTotal != 85 {
		t.Fatalf("impact = %+v", s.Impact)
	}
}

func TestSummarySanitizesMaliciousNamesAndTitles(t *testing.T) {
	repo, base := summaryRepo(t)
	title := "evil\x1b[31m title\nsecret-token-123 " + strings.Repeat("长", 400)
	commitFile(t, repo, "new\nline\t$(touch pwned).txt", "c\n", title)
	s := CollectGitSummary(context.Background(), nil, repo, true, base, func(v string) string {
		return strings.ReplaceAll(v, "secret-token-123", "[REDACTED]")
	})
	if len(s.Commits) != 1 {
		t.Fatalf("commits = %+v", s.Commits)
	}
	got := s.Commits[0].Title
	if strings.ContainsAny(got, "\x1b\n") || strings.Contains(got, "secret-token-123") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("title not sanitized: %q", got)
	}
	if len([]rune(got)) > maxCommitTitle+1 {
		t.Fatalf("title not truncated: %d runes", len([]rune(got)))
	}
	if s.Impact == nil || len(s.Impact.Paths) != 1 || strings.ContainsAny(s.Impact.Paths[0], "\n\t") {
		t.Fatalf("paths = %+v", s.Impact)
	}
	if _, err := os.Stat(filepath.Join(repo, "pwned")); err == nil {
		t.Fatal("file name was executed by a shell")
	}
}

type failingRunner struct{ err error }

func (f failingRunner) Run(context.Context, string, ...string) (GitResult, error) {
	return GitResult{}, f.err
}

func TestSummaryQueryFailureAndTimeout(t *testing.T) {
	repo, base := summaryRepo(t)
	s := CollectGitSummary(context.Background(), failingRunner{err: errors.New("boom")}, repo, true, base, nil)
	if s.Available || s.UnavailableReason != SummaryGitError {
		t.Fatalf("failure summary = %+v", s)
	}
	s = CollectGitSummary(context.Background(), failingRunner{err: context.DeadlineExceeded}, repo, true, base, nil)
	if s.Available || s.UnavailableReason != SummaryTimeout {
		t.Fatalf("timeout summary = %+v", s)
	}
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ ...string) (GitResult, error) {
	<-ctx.Done()
	return GitResult{}, ctx.Err()
}

func TestSummaryHonorsOverallDeadline(t *testing.T) {
	repo, base := summaryRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	s := CollectGitSummary(ctx, blockingRunner{}, repo, true, base, nil)
	if s.Available || s.UnavailableReason != SummaryTimeout {
		t.Fatalf("summary = %+v", s)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("collector ignored the deadline")
	}
}

func TestSummaryDoesNotModifyWorkspace(t *testing.T) {
	repo, base := summaryRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, ".git", "hooks", "post-index-change")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	CollectGitSummary(context.Background(), nil, repo, true, base, nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("summary triggered a git hook")
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "M a.txt" {
		t.Fatalf("workspace changed: %q", out)
	}
}

func TestCaptureBaseline(t *testing.T) {
	repo, base := summaryRepo(t)
	got, err := CaptureBaseline(context.Background(), nil, repo)
	if err != nil || got != base {
		t.Fatalf("baseline = %q, %v; want %q", got, err, base)
	}
	if _, err := CaptureBaseline(context.Background(), nil, t.TempDir()); err == nil {
		t.Fatal("expected an error outside a repository")
	}
	empty := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = empty
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := CaptureBaseline(context.Background(), nil, empty); err == nil {
		t.Fatal("expected an error for a repository without commits")
	}
}

func TestReadOnlyGitEnvDropsInheritedGitVariables(t *testing.T) {
	env := readOnlyGitEnv([]string{"PATH=/bin", "GIT_DIR=/x", "GIT_EXTERNAL_DIFF=/evil", "LANG=zh_CN.UTF-8", "HOME=/h"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"GIT_DIR=/x", "GIT_EXTERNAL_DIFF", "zh_CN"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("env kept %s: %v", bad, env)
		}
	}
	for _, want := range []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "PATH=/bin", "HOME=/h"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %s: %v", want, env)
		}
	}
}
