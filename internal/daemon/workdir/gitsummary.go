package workdir

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 阶段 12b：运行结束后只读采集工作区的 Git 摘要与改动影响。
// daemon 不提交、不修改工作区；所有查询都走只读 Git 子命令，失败只意味着“摘要不可用”，不影响运行结果。

const (
	// GitSummaryTimeout 是整次摘要采集（含所有 Git 子进程）的总超时。
	GitSummaryTimeout = 10 * time.Second
	// MaxSummaryCommits、MaxSummaryPaths 与 contracts/openapi.yaml 的 RunGitSummary 一致。
	MaxSummaryCommits = 20
	MaxSummaryPaths   = 50
	maxCommitTitle    = 200
	maxSummaryPath    = 512
	maxSummaryBranch  = 255
	// summaryGitOutput 是单条只读查询的输出上限；超出视为查询失败（摘要不可用），不截断后继续猜。
	summaryGitOutput = 4 << 20
)

// 摘要不可用原因，与 OpenAPI 的 unavailable_reason 枚举一致。
const (
	SummaryNoBaseline = "NO_BASELINE"
	SummaryTimeout    = "TIMEOUT"
	SummaryGitError   = "GIT_ERROR"
)

type SummaryCommit struct {
	Hash  string `json:"hash"`
	Title string `json:"title"`
}

type SummaryImpact struct {
	Files      int      `json:"files"`
	Added      int      `json:"added"`
	Deleted    int      `json:"deleted"`
	Paths      []string `json:"paths"`
	PathsTotal int      `json:"paths_total"`
}

// GitSummary 是随运行结果上报的 Git 摘要（JSON 形状见 contracts/openapi.yaml 的 RunGitSummary）。
type GitSummary struct {
	V                 int             `json:"v"`
	Repo              bool            `json:"repo"`
	Available         bool            `json:"available"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
	Branch            string          `json:"branch,omitempty"`
	Detached          bool            `json:"detached,omitempty"`
	Head              string          `json:"head,omitempty"`
	Base              string          `json:"base,omitempty"`
	Commits           []SummaryCommit `json:"commits,omitempty"`
	CommitTotal       int             `json:"commit_total,omitempty"`
	Uncommitted       int             `json:"uncommitted,omitempty"`
	Impact            *SummaryImpact  `json:"impact,omitempty"`
	// UnverifiedReasons 是 daemon 能客观判断的未验证原因（不解析 Runtime 的自述）。
	UnverifiedReasons []string `json:"unverified_reasons,omitempty"`
}

// NewReadOnlyGitRunner 返回只读采集专用的 Git 执行器：不经 shell、不读取调用方的 GIT_* 环境、
// 不写锁（GIT_OPTIONAL_LOCKS=0）、不触发 fsmonitor 钩子、不弹交互提示。
func NewReadOnlyGitRunner() GitRunner { return isolatedGitRunner{} }

type isolatedGitRunner struct{}

func (isolatedGitRunner) Run(ctx context.Context, dir string, args ...string) (GitResult, error) {
	if len(args) == 0 {
		return GitResult{}, fmt.Errorf("%w: Git arguments are empty", ErrInvalidRequest)
	}
	full := append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}, args...)
	stdout := &limitedBuffer{limit: summaryGitOutput}
	stderr := &limitedBuffer{limit: maxGitOutput}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = readOnlyGitEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	result := GitResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if stdout.exceeded || stderr.exceeded {
		return result, fmt.Errorf("%w: output exceeded %d bytes", ErrGitOperation, summaryGitOutput)
	}
	if err != nil {
		return result, &GitError{Dir: dir, Args: append([]string(nil), args...), Result: result, Err: err}
	}
	return result, nil
}

// readOnlyGitEnv 去掉全部 GIT_* 变量（可能改变仓库位置、外部 diff、钩子路径等），再固定只读相关设置。
func readOnlyGitEnv(base []string) []string {
	env := make([]string, 0, len(base)+4)
	for _, item := range base {
		if strings.HasPrefix(item, "GIT_") || strings.HasPrefix(item, "LC_") || strings.HasPrefix(item, "LANG=") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
}

// CaptureBaseline 记录运行开始时的基线提交（完整哈希）。必须在 Runtime 进程启动之前调用。
// 目录不是仓库、仓库还没有任何提交或查询失败时返回错误，调用方据此把摘要标为 NO_BASELINE。
func CaptureBaseline(ctx context.Context, runner GitRunner, dir string) (string, error) {
	if runner == nil {
		runner = NewReadOnlyGitRunner()
	}
	ctx, cancel := context.WithTimeout(ctx, GitSummaryTimeout)
	defer cancel()
	result, err := runner.Run(ctx, dir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(result.Stdout)
	if !validObjectName(hash) {
		return "", fmt.Errorf("%w: unexpected HEAD %q", ErrGitOperation, hash)
	}
	return hash, nil
}

func validObjectName(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// CollectGitSummary 在工作区只读采集摘要。isRepo 为 false 时直接返回“无仓库”。
// baseline 为空表示运行开始时没有记录到基线（例如 daemon 中途重启或仓库为空），摘要标为 NO_BASELINE。
// sanitize 在内置的控制字符清理与截断之前先对提交标题、路径、分支名做脱敏（可为 nil）。
func CollectGitSummary(ctx context.Context, runner GitRunner, dir string, isRepo bool, baseline string, sanitize func(string) string) GitSummary {
	summary := GitSummary{V: 1, Repo: isRepo, Available: true}
	if !isRepo {
		return summary
	}
	unavailable := func(reason string) GitSummary {
		return GitSummary{V: 1, Repo: true, UnavailableReason: reason}
	}
	if !validObjectName(baseline) {
		return unavailable(SummaryNoBaseline)
	}
	if runner == nil {
		runner = NewReadOnlyGitRunner()
	}
	ctx, cancel := context.WithTimeout(ctx, GitSummaryTimeout)
	defer cancel()
	clean := func(value string, limit int) string {
		if sanitize != nil {
			value = sanitize(value)
		}
		return cleanSummaryText(value, limit)
	}
	fail := func(err error) GitSummary {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return unavailable(SummaryTimeout)
		}
		return unavailable(SummaryGitError)
	}
	run := func(args ...string) (string, error) {
		result, err := runner.Run(ctx, dir, args...)
		if err != nil {
			return "", err
		}
		return result.Stdout, nil
	}

	summary.Base = shortHash(baseline)
	// 分支：detached HEAD 时 symbolic-ref 以退出码 1 结束且无输出。
	if out, err := run("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		summary.Branch = clean(strings.TrimSpace(out), maxSummaryBranch)
	} else if ctx.Err() != nil {
		return fail(err)
	} else if exitCode(err) == 1 {
		summary.Detached = true
	} else {
		return fail(err)
	}
	head, err := run("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return fail(err)
	}
	head = strings.TrimSpace(head)
	if !validObjectName(head) {
		return unavailable(SummaryGitError)
	}
	summary.Head = shortHash(head)

	// 新增提交：基线之后的提交，取最近 MaxSummaryCommits 条，同时给出总数。
	rng := baseline + "..HEAD"
	total, err := run("rev-list", "--count", rng)
	if err != nil {
		return fail(err)
	}
	if summary.CommitTotal, err = strconv.Atoi(strings.TrimSpace(total)); err != nil || summary.CommitTotal < 0 {
		return unavailable(SummaryGitError)
	}
	if summary.CommitTotal > 0 {
		logOut, err := run("log", "-n", strconv.Itoa(MaxSummaryCommits), "--no-color", "--format=%h%x1f%s%x1e", rng)
		if err != nil {
			return fail(err)
		}
		for _, record := range strings.Split(logOut, "\x1e") {
			record = strings.Trim(record, "\r\n")
			if record == "" {
				continue
			}
			hash, title, _ := strings.Cut(record, "\x1f")
			summary.Commits = append(summary.Commits, SummaryCommit{Hash: clean(hash, 64), Title: clean(title, maxCommitTitle)})
			if len(summary.Commits) == MaxSummaryCommits {
				break
			}
		}
	}

	// 未提交改动：已跟踪的修改 + 未跟踪文件（未跟踪目录按一项计）。-z 输出以 NUL 分隔，文件名中的换行不会串行。
	statusOut, err := run("status", "--porcelain=v1", "-z", "--untracked-files=normal", "--no-renames")
	if err != nil {
		return fail(err)
	}
	for _, entry := range strings.Split(statusOut, "\x00") {
		if len(entry) > 3 {
			summary.Uncommitted++
		}
	}

	// 影响：工作区相对基线的改动统计（含未提交的已跟踪文件改动；未跟踪文件不计行数）。
	diffOut, err := run("diff", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", baseline)
	if err != nil {
		return fail(err)
	}
	impact := &SummaryImpact{Paths: []string{}}
	for _, entry := range strings.Split(diffOut, "\x00") {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) != 3 {
			return unavailable(SummaryGitError)
		}
		impact.Files++
		// 二进制文件的增删列为 "-"，不计行数。
		if added, err := strconv.Atoi(parts[0]); err == nil {
			impact.Added += added
		}
		if deleted, err := strconv.Atoi(parts[1]); err == nil {
			impact.Deleted += deleted
		}
		if len(impact.Paths) < MaxSummaryPaths {
			impact.Paths = append(impact.Paths, clean(parts[2], maxSummaryPath))
		}
	}
	impact.PathsTotal = impact.Files
	summary.Impact = impact
	return summary
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		err = gitErr.Err
	}
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// cleanSummaryText 把控制字符（含换行、转义序列、DEL）替换为空格并按字符数截断，
// 避免恶意提交标题或文件名污染界面与日志；截断处补省略号。
func cleanSummaryText(value string, limit int) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	var out bytes.Buffer
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			r = ' '
		}
		if count == limit {
			out.WriteString("…")
			break
		}
		out.WriteRune(r)
		count++
	}
	return strings.TrimSpace(out.String())
}

// 未验证原因枚举，与 OpenAPI 的 unverified_reasons 一致。
const (
	UnverifiedUncommitted = "UNCOMMITTED_CHANGES"
	UnverifiedNoSummary   = "GIT_SUMMARY_UNAVAILABLE"
)

// Unverified 按摘要客观判断未验证原因：有未提交改动，或仓库目录的摘要不可用。无仓库目录不属于“不可用”。
func (s GitSummary) Unverified() []string {
	var reasons []string
	if s.Repo && !s.Available {
		reasons = append(reasons, UnverifiedNoSummary)
	}
	if s.Uncommitted > 0 {
		reasons = append(reasons, UnverifiedUncommitted)
	}
	return reasons
}
