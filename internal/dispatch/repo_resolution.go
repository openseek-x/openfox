package dispatch

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/reposource"
)

// repoBranchRuleSplit 写在 runs.git_settings_snapshot.repo_branch_rule 中，标记该 Run 按阶段 12d（F-02）的
// “仓库来源与分支分开解析”规则下发仓库：
//   - 仓库来源：Agent 配置了仓库就只用 Agent 的仓库，否则用空间仓库（与旧规则相同）；
//   - 分支：Agent 仓库自己的 base_branch > 空间中同 URL 仓库的 base_branch > 空串（远程默认分支）。
//
// 旧规则把分支与仓库来源一起机械覆盖：用了 Agent 仓库就完全忽略空间里同一仓库的分支设置（审计 R-18）。
// 分支是 daemon 工作区身份的一部分（reposource.Repository.Identity），对已有会话改变解析结果会换一个检出目录，
// 因此只有会话里所有更早的 Run 都带该标记时才用新规则；升级前创建过 Run 的会话继续用旧规则（只影响新运行）。
const repoBranchRuleSplit = "split"

const maxRunRepositories = 64

// loadRunRepositories 按规则解析 WORKSPACE 目录模式下本次 Run 的仓库清单（最多 64 个，按 position、id 排序）。
func loadRunRepositories(ctx context.Context, tx pgx.Tx, run dispatchRun) ([]reposource.Repository, error) {
	split, err := runUsesSplitRepoBranchRule(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	agentRepos, err := queryRepositories(ctx, tx, `SELECT url, base_branch, COALESCE(connection_id::text, '')
		  FROM openfox.agent_repos WHERE organization_id = $1::uuid AND agent_id = $2::uuid
		 ORDER BY position, id LIMIT 64`, run.OrganizationID, run.AgentID)
	if err != nil {
		return nil, err
	}
	if len(agentRepos) > 0 && !split {
		return validRepositories(agentRepos)
	}
	spaceRepos, err := queryRepositories(ctx, tx, `SELECT url, base_branch, COALESCE(connection_id::text, '')
		  FROM openfox.space_repos WHERE organization_id = $1::uuid AND space_id = $2::uuid
		 ORDER BY position, id LIMIT 64`, run.OrganizationID, run.SpaceID)
	if err != nil {
		return nil, err
	}
	if len(agentRepos) == 0 {
		return validRepositories(spaceRepos)
	}
	return validRepositories(mergeRepositoryBranches(agentRepos, spaceRepos))
}

// mergeRepositoryBranches 只为没有自己分支的 Agent 仓库补上空间中同 URL 仓库的分支；
// 空间里同一 URL 出现多次时取排序靠前且分支非空的一条。不会把空间独有的仓库加进来（仓库来源仍是 Agent）。
func mergeRepositoryBranches(agentRepos, spaceRepos []reposource.Repository) []reposource.Repository {
	spaceBranches := make(map[string]string, len(spaceRepos))
	for _, repo := range spaceRepos {
		key := repositoryURLKey(repo.URL)
		if key == "" || repo.Branch == "" {
			continue
		}
		if _, exists := spaceBranches[key]; !exists {
			spaceBranches[key] = repo.Branch
		}
	}
	merged := make([]reposource.Repository, len(agentRepos))
	for i, repo := range agentRepos {
		if repo.Branch == "" {
			repo.Branch = spaceBranches[repositoryURLKey(repo.URL)]
		}
		merged[i] = repo
	}
	return merged
}

// repositoryURLKey 把仓库 URL 规范化后用于“同 URL”比较：协议与主机名不区分大小写，忽略结尾的 “/” 与 “.git”。
// 路径大小写保留（不同托管平台大小写语义不同，宁可不合并也不误合并）；解析失败返回空串，不参与合并。
func repositoryURLKey(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + path
}

// runUsesSplitRepoBranchRule 判断本次 Run 是否使用新规则：自身快照带标记，且同一会话中没有更早的不带标记的 Run。
func runUsesSplitRepoBranchRule(ctx context.Context, tx pgx.Tx, run dispatchRun) (bool, error) {
	var split bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(r.git_settings_snapshot->>'repo_branch_rule', '') = $2
		   AND NOT EXISTS (
		       SELECT 1 FROM openfox.runs earlier
		 WHERE earlier.organization_id = r.organization_id
		   AND earlier.conversation_id = r.conversation_id
		   AND (earlier.created_at, earlier.id) < (r.created_at, r.id)
		   AND COALESCE(earlier.git_settings_snapshot->>'repo_branch_rule', '') <> $2)
		  FROM openfox.runs r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = $1::uuid`,
		run.ID, repoBranchRuleSplit).Scan(&split)
	return split, err
}

func queryRepositories(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]reposource.Repository, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []reposource.Repository
	for rows.Next() {
		var repo reposource.Repository
		if err := rows.Scan(&repo.URL, &repo.Branch, &repo.ConnectionID); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
		if len(repos) >= maxRunRepositories {
			break
		}
	}
	return repos, rows.Err()
}

func validRepositories(repos []reposource.Repository) ([]reposource.Repository, error) {
	for _, repo := range repos {
		if !reposource.Valid(repo) {
			return nil, errors.New("configured repository requires a public HTTPS source without credentials")
		}
	}
	return repos, nil
}
