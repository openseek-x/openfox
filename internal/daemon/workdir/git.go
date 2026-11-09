package workdir

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxGitOutput = 128 << 10

type GitResult struct {
	Stdout string
	Stderr string
}

type GitRunner interface {
	Run(context.Context, string, ...string) (GitResult, error)
}

type execGitRunner struct{}

func (execGitRunner) Run(ctx context.Context, dir string, args ...string) (GitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 {
		return GitResult{}, fmt.Errorf("%w: Git arguments are empty", ErrInvalidRequest)
	}
	stdout := &limitedBuffer{limit: maxGitOutput}
	stderr := &limitedBuffer{limit: maxGitOutput}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := GitResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if stdout.exceeded || stderr.exceeded {
		return result, fmt.Errorf("%w: output exceeded %d bytes", ErrGitOperation, maxGitOutput)
	}
	if err != nil {
		return result, &GitError{Dir: dir, Args: append([]string(nil), args...), Result: result, Err: err}
	}
	return result, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return originalLen, nil
	}
	if len(p) > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	return originalLen, err
}

type GitError struct {
	Dir    string
	Args   []string
	Result GitResult
	Err    error
}

func (e *GitError) Error() string {
	if e == nil {
		return ErrGitOperation.Error()
	}
	detail := strings.TrimSpace(e.Result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(e.Result.Stdout)
	}
	if detail == "" {
		return fmt.Sprintf("%s: git %s: %v", ErrGitOperation, strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("%s: git %s: %s", ErrGitOperation, strings.Join(e.Args, " "), detail)
}

func (e *GitError) Unwrap() error { return ErrGitOperation }

func gitRefValid(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && !strings.HasPrefix(ref, "-") &&
		!strings.Contains(ref, "..") && !strings.Contains(ref, "@{") &&
		!strings.ContainsAny(ref, " \t\r\n")
}

func branchName(branch string) error {
	if strings.TrimSpace(branch) == "" {
		return nil
	}
	if !gitRefValid(branch) {
		return fmt.Errorf("%w: invalid branch name %q", ErrInvalidRequest, branch)
	}
	return nil
}

func resolveGitRoot(ctx context.Context, runner GitRunner, source string) (string, error) {
	result, err := runner.Run(ctx, source, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: resolve repository root: %v", ErrNotGitRepository, err)
	}
	root := strings.TrimSpace(result.Stdout)
	if root == "" {
		return "", fmt.Errorf("%w: Git returned an empty root", ErrNotGitRepository)
	}
	resolved, err := canonicalPath(root)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize repository root: %v", ErrNotGitRepository, err)
	}
	return resolved, nil
}

func resolveBase(ctx context.Context, runner GitRunner, repo, requested string) (string, []Diagnostic, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "HEAD", nil, nil
	}
	if !gitRefValid(requested) {
		return "", nil, fmt.Errorf("%w: invalid base ref %q", ErrInvalidRequest, requested)
	}

	diagnostics := make([]Diagnostic, 0, 1)
	if _, err := runner.Run(ctx, repo, "fetch", "origin", requested); err == nil {
		if _, err := runner.Run(ctx, repo, "rev-parse", "--verify", requested+"^{commit}"); err == nil {
			return requested, diagnostics, nil
		}
		if _, err := runner.Run(ctx, repo, "rev-parse", "--verify", "FETCH_HEAD^{commit}"); err == nil {
			return "FETCH_HEAD", diagnostics, nil
		}
	} else {
		diagnostics = append(diagnostics, Diagnostic{
			Code:      "GIT_FETCH_FAILED",
			Operation: "fetch",
			Message:   "explicit base fetch failed; checking local references",
			Detail:    err.Error(),
			Retriable: true,
		})
	}
	if _, err := runner.Run(ctx, repo, "rev-parse", "--verify", requested+"^{commit}"); err == nil {
		return requested, diagnostics, nil
	}
	return "", diagnostics, fmt.Errorf("%w: base ref %q is unavailable", ErrRepositoryUnavailable, requested)
}

func (m *Manager) prepareGit(ctx context.Context, req Request) (PreparedWorkspace, error) {
	source, err := canonicalPath(req.SourcePath)
	if err != nil {
		return PreparedWorkspace{}, fmt.Errorf("%w: source path: %v", ErrRepositoryUnavailable, err)
	}
	if _, err := osStatDir(source); err != nil {
		return PreparedWorkspace{}, fmt.Errorf("%w: source path: %v", ErrRepositoryUnavailable, err)
	}
	repo, err := resolveGitRoot(ctx, m.git, source)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	var base string
	var diagnostics []Diagnostic
	if req.LocalBaseOnly {
		if repo != source {
			return PreparedWorkspace{}, ErrPathOutsideAllowed
		}
		checkout := repo
		if req.BaseRef == "" && req.LocalHeadPath != "" {
			checkout = req.LocalHeadPath
		}
		base, err = localBase(ctx, m.git, checkout, req.BaseRef)
	} else {
		base, diagnostics, err = resolveBase(ctx, m.git, repo, req.BaseRef)
	}
	if err != nil {
		return PreparedWorkspace{}, &PrepareError{Diagnostics: diagnostics, Err: err}
	}
	branch := strings.TrimSpace(req.Branch)
	if err := branchName(branch); err != nil {
		return PreparedWorkspace{}, err
	}
	if branch == "" {
		branch = "openfox/work/" + m.nextID()
	}
	workspace := req.RequestedPath
	if workspace == "" {
		workspace, err = m.newManagedPath(branch)
		if err != nil {
			return PreparedWorkspace{}, err
		}
	} else {
		root, rootErr := m.managedRoot()
		if rootErr != nil {
			return PreparedWorkspace{}, rootErr
		}
		workspace, err = m.policy.check(workspace)
		if err != nil {
			return PreparedWorkspace{}, err
		}
		if workspace == root || !pathContains(root, workspace) {
			return PreparedWorkspace{}, ErrPathOutsideAllowed
		}
	}
	if err := m.repoLock(ctx, repo); err != nil {
		return PreparedWorkspace{}, err
	}
	defer m.releaseRepoLock(repo)
	if _, err := m.git.Run(ctx, repo, "worktree", "add", "-b", branch, workspace, base); err != nil {
		return PreparedWorkspace{}, &PrepareError{
			Diagnostics: diagnostics,
			Err:         fmt.Errorf("%w: create worktree: %v", ErrRepositoryUnavailable, err),
		}
	}
	resolved, err := m.policy.check(workspace)
	if err != nil {
		return PreparedWorkspace{}, &PrepareError{Diagnostics: diagnostics, Err: err}
	}
	return PreparedWorkspace{
		Path:           resolved,
		RepositoryRoot: repo,
		Branch:         branch,
		IsGit:          true,
		Owned:          true,
		Diagnostics:    diagnostics,
	}, nil
}

func (m *Manager) commitResult(ctx context.Context, workspace *PreparedWorkspace, req CommitRequest) (string, error) {
	if workspace == nil || !workspace.Owned || !workspace.IsGit || workspace.Binding.Mode == ModeDirect || workspace.Binding.Mode == ModeNoRepo {
		return "", ErrCommitNotAllowed
	}
	if strings.TrimSpace(req.Message) == "" || strings.ContainsRune(req.Message, '\x00') {
		return "", fmt.Errorf("%w: commit message is required", ErrInvalidRequest)
	}
	status, err := m.git.Run(ctx, workspace.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status.Stdout) == "" {
		return "", ErrNoChanges
	}
	if _, err := m.git.Run(ctx, workspace.Path, "add", "--all"); err != nil {
		return "", err
	}
	commit := []string{"commit", "-m", req.Message}
	name, email := strings.TrimSpace(req.AuthorName), strings.TrimSpace(req.AuthorEmail)
	if strings.ContainsAny(name+email, "\x00\r\n<>") {
		return "", fmt.Errorf("%w: invalid commit author", ErrInvalidRequest)
	}
	// user.name / user.email 同时决定 author 与 committer；只对本次 git 进程生效。空值表示沿用电脑上的 Git 配置。
	if email != "" {
		commit = append([]string{"-c", "user.email=" + email}, commit...)
	}
	if name != "" {
		commit = append([]string{"-c", "user.name=" + name}, commit...)
	}
	if _, err := m.git.Run(ctx, workspace.Path, commit...); err != nil {
		return "", err
	}
	head, err := m.git.Run(ctx, workspace.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head.Stdout), nil
}

func osStatDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("not a directory")
	}
	return true, nil
}
