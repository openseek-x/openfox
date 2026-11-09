package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/secrets"
)

// ErrSecretsUnavailable 表示 Agent 有环境变量，但中心未配置主密钥或解密失败；Run 上下文不能下发。
var ErrSecretsUnavailable = errors.New("dispatch: agent secrets are unavailable")

// maxRunEnvironment 与 agents.MaxEnvVars 一致；多读一行只为发现超限的异常数据。
const maxRunEnvironment = 100

// EnvironmentVariable 是下发给执行设备的明文环境变量。不得写日志、审计或事件。
type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GitAuthor 是 worktree 提交使用的作者与提交者；空串表示该项沿用电脑上的 Git 配置。
type GitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// loadRunSettings 读取配置版本里的运行设置（自定义参数、隔离方式、Git 提交人），
// 并按模式把提交人解析为具体名称与邮箱。
func loadRunSettings(ctx context.Context, tx pgx.Tx, run dispatchRun, result *RunContext) error {
	var customArgs []byte
	var nameMode, manualName, emailMode, manualEmail string
	var agentName, ownerEmail, actorEmail string
	if err := tx.QueryRow(ctx, `
		SELECT c.custom_args, c.workdir_isolation,
		       c.git_author_name_mode, c.git_author_name, c.git_author_email_mode, c.git_author_email,
		       a.name, COALESCE(owner.email, ''), COALESCE(actor.email, '')
		  FROM openfox.agent_config_versions c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  LEFT JOIN openfox.users owner ON owner.id = a.owner_user_id
		  LEFT JOIN openfox.users actor ON actor.id = $3::uuid
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.agent_id = $1::uuid AND c.config_version = $2`, run.AgentID, run.ConfigVersion, run.ActorID).
		Scan(&customArgs, &result.WorkdirIsolation, &nameMode, &manualName, &emailMode, &manualEmail,
			&agentName, &ownerEmail, &actorEmail); err != nil {
		return err
	}
	result.CustomArgs = []string{}
	if len(customArgs) > 0 {
		if err := json.Unmarshal(customArgs, &result.CustomArgs); err != nil {
			return err
		}
	}
	if ownerEmail == "" {
		// 没有所有者的旧 Agent：所有者模式退回到发起人。
		ownerEmail = actorEmail
	}
	result.GitAuthor = resolveGitAuthor(nameMode, manualName, emailMode, manualEmail, agentName, actorEmail, ownerEmail)
	return nil
}

// resolveGitAuthor 把提交人模式解析为具体值。「使用者」= 发起该 Run 的成员，「所有者」= Agent 所有者；
// 名称取邮箱 @ 之前的部分（用户没有单独的显示名）。两项都是 MACHINE 时返回 nil。
func resolveGitAuthor(nameMode, manualName, emailMode, manualEmail, agentName, actorEmail, ownerEmail string) *GitAuthor {
	author := GitAuthor{}
	switch nameMode {
	case "USER_AND_AGENT":
		author.Name = joinAuthorName(emailLocalPart(actorEmail), agentName)
	case "USER":
		author.Name = emailLocalPart(actorEmail)
	case "OWNER_AND_AGENT":
		author.Name = joinAuthorName(emailLocalPart(ownerEmail), agentName)
	case "OWNER":
		author.Name = emailLocalPart(ownerEmail)
	case "MANUAL":
		author.Name = strings.TrimSpace(manualName)
	}
	switch emailMode {
	case "USER":
		author.Email = actorEmail
	case "OWNER":
		author.Email = ownerEmail
	case "MANUAL":
		author.Email = strings.TrimSpace(manualEmail)
	}
	if author.Name == "" && author.Email == "" {
		return nil
	}
	return &author
}

func emailLocalPart(email string) string {
	if index := strings.IndexByte(email, '@'); index > 0 {
		return email[:index]
	}
	return email
}

func joinAuthorName(user, agent string) string {
	user, agent = strings.TrimSpace(user), strings.TrimSpace(agent)
	switch {
	case user == "":
		return agent
	case agent == "":
		return user
	default:
		return user + " & " + agent
	}
}

// LoadRunEnvironment 解密 Agent 当前的环境变量（不固定到配置版本），按名称排序。
// 没有环境变量时返回空切片；有环境变量但 box 为 nil 或任一值解密失败时返回 ErrSecretsUnavailable。
// 返回值是明文：调用方只能把它写进设备认证的 HTTPS 响应，不得记录。
func LoadRunEnvironment(ctx context.Context, tx pgx.Tx, box *secrets.Box, organizationID, agentID string) ([]EnvironmentVariable, error) {
	rows, err := tx.Query(ctx, `
		SELECT name, value_ciphertext, value_nonce, key_id
		  FROM openfox.agent_env_vars
		 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid
		 ORDER BY name
		 LIMIT $2`, agentID, maxRunEnvironment+1)
	if err != nil {
		return nil, err
	}
	type sealedRow struct {
		name   string
		sealed secrets.Sealed
	}
	var sealedRows []sealedRow
	for rows.Next() {
		var row sealedRow
		if err := rows.Scan(&row.name, &row.sealed.Ciphertext, &row.sealed.Nonce, &row.sealed.KeyID); err != nil {
			rows.Close()
			return nil, err
		}
		sealedRows = append(sealedRows, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	environment := make([]EnvironmentVariable, 0, len(sealedRows))
	if len(sealedRows) == 0 {
		return environment, nil
	}
	if box == nil || len(sealedRows) > maxRunEnvironment {
		return nil, ErrSecretsUnavailable
	}
	for _, row := range sealedRows {
		plaintext, err := box.Open(row.sealed, secrets.EnvAAD(organizationID, agentID, row.name))
		if err != nil {
			// 不包装原始错误：错误链里不出现变量名以外的任何内容。
			return nil, ErrSecretsUnavailable
		}
		environment = append(environment, EnvironmentVariable{Name: row.name, Value: string(plaintext)})
	}
	return environment, nil
}
