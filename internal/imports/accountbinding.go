package imports

// 第三方账号绑定：绑定页汇总、个人 Jira 绑定（只记身份、不存凭证）与外部账号自动关联。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	// ErrJiraSiteUnknown 站点不在本组织的 Jira 导入记录里（防止向任意地址发请求）。
	ErrJiraSiteUnknown = errors.New("imports: jira site is not one of the organization's imported sites")
	// ErrJiraAccountInUse 这个 Jira 账号已绑定到本组织的其他成员。
	ErrJiraAccountInUse = errors.New("imports: jira account is already bound to another member")
)

const maxJiraSites = 20

// maxBindingInstances 限制绑定页汇总的 GitLab 实例数。
const maxBindingInstances = 50

// JiraSite 是本组织导入过的 Jira 站点。
type JiraSite struct {
	SiteURL    string `json:"site_url"`
	Deployment string `json:"deployment"`
	Host       string `json:"host"`
}

// AccountBinding 对应契约 AccountBinding。
type AccountBinding struct {
	ConnectionID string    `json:"connection_id"`
	DisplayName  string    `json:"display_name"`
	Detail       string    `json:"detail"`
	AvatarURL    string    `json:"avatar_url"`
	CreatedAt    time.Time `json:"created_at"`
	Version      int64     `json:"version"`
}

// AccountBindingInstance 对应契约 AccountBindingInstance：GitLab 系在本组织的一个实例（组织连接）与本人在该实例的个人绑定。
type AccountBindingInstance struct {
	ConnectionID string          `json:"connection_id"`
	Host         string          `json:"host"`
	DisplayName  string          `json:"display_name"`
	Binding      *AccountBinding `json:"binding"`
}

// AccountBindingProvider 对应契约 AccountBindingProvider。
type AccountBindingProvider struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Availability      string          `json:"availability"`
	Method            string          `json:"method"`
	OrganizationReady bool            `json:"organization_ready"`
	Binding           *AccountBinding `json:"binding"`
	// Instances 只有 GitLab 系（gitlab、gitlab_cn）返回，其余省略。
	Instances []AccountBindingInstance `json:"instances,omitempty"`
}

// AccountBindingsView 对应契约 AccountBindingsView。
type AccountBindingsView struct {
	Providers []AccountBindingProvider `json:"providers"`
	JiraSites []JiraSite               `json:"jira_sites"`
	Email     string                   `json:"email"`
}

// bindingProviders 是绑定页固定的行顺序与名称。
var bindingProviders = []struct{ id, name string }{
	{"wechat", "微信"}, {"github", "GitHub"}, {"jira", "Jira"}, {"tapd", "TAPD"}, {"zentao", "禅道"},
	{"pingcode", "PingCode"}, {"teambition", "Teambition"}, {"slack", "Slack"}, {"feishu", "飞书"},
	{"lark", "Lark"}, {"dingtalk", "钉钉"},
}

// gitBindingRows 是 Git 服务商的绑定行：插在 GitHub 后面，名称与集成目录一致。
var gitBindingRows = []struct{ id, name string }{
	{"gitlab_cn", "极狐 GitLab.cn"}, {"gitlab", "GitLab 自部署版"}, {"gitee", "Gitee"},
}

func isGitLabBinding(id string) bool { return id == "gitlab" || id == "gitlab_cn" }

// bindingRowList 返回 14 行：GitHub 之后依次插入极狐、GitLab 自部署版、Gitee。
func bindingRowList() []struct{ id, name string } {
	out := make([]struct{ id, name string }, 0, len(bindingProviders)+len(gitBindingRows))
	for _, p := range bindingProviders {
		out = append(out, p)
		if p.id == "github" {
			out = append(out, gitBindingRows...)
		}
	}
	return out
}

// SiteHost 返回站点地址去掉协议后的「主机[:端口][/路径前缀]」，同时也是外部账号的 source_label。
func SiteHost(siteURL string) string {
	label, _ := TruncateRunes(strings.TrimPrefix(siteURL, "https://"), 255)
	return label
}

// SameSite 判断两个已规范化的站点地址是否相同（不区分大小写）。
func SameSite(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// EmailMatches 比较 Jira 邮箱与 OpenFox 邮箱：去空白、不区分大小写，且都不能为空。
func EmailMatches(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func checkBindingActor(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	return nil
}

// loadJiraSites 读取本组织导入过的 Jira 站点：未丢弃的 API 导入，按站点去重，最近一次导入优先，最多 20 个。
func loadJiraSites(ctx context.Context, tx pgx.Tx) ([]JiraSite, error) {
	rows, err := tx.Query(ctx, `
		SELECT source, preview->'api'->>'site_url' FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND source IN ('jira_cloud', 'jira_server')
		   AND status <> 'DISCARDED' AND COALESCE(preview->'api'->>'site_url', '') <> ''
		 ORDER BY created_at DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sites := []JiraSite{}
	seen := map[string]bool{}
	for rows.Next() {
		var source, site string
		if err := rows.Scan(&source, &site); err != nil {
			return nil, err
		}
		key := strings.ToLower(site)
		if seen[key] || len(sites) >= maxJiraSites {
			continue
		}
		seen[key] = true
		deployment := jiraapi.DeploymentCloud
		if source == SourceJiraServer {
			deployment = jiraapi.DeploymentServer
		}
		sites = append(sites, JiraSite{SiteURL: site, Deployment: deployment, Host: SiteHost(site)})
	}
	return sites, rows.Err()
}

func memberEmail(ctx context.Context, tx pgx.Tx, userID string) (string, error) {
	var email string
	err := tx.QueryRow(ctx, `SELECT email FROM openfox.users WHERE id = $1::uuid`, userID).Scan(&email)
	return email, err
}

// ListAccountBindings 组装绑定页的 14 行，包含极狐、GitLab 自部署版、Gitee。所有非访客成员可读。
func (s Service) ListAccountBindings(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, githubConfigured, giteeConfigured, wechatConfigured bool) (AccountBindingsView, error) {
	if err := checkBindingActor(ctx, tx, organizationID, actor); err != nil {
		return AccountBindingsView{}, err
	}
	rowList := bindingRowList()
	view := AccountBindingsView{Providers: make([]AccountBindingProvider, 0, len(rowList))}
	var err error
	if view.Email, err = memberEmail(ctx, tx, actor.UserID); err != nil {
		return AccountBindingsView{}, err
	}
	if view.JiraSites, err = loadJiraSites(ctx, tx); err != nil {
		return AccountBindingsView{}, err
	}
	var githubOrg, giteeOrg, zentaoOrg, pingCodeOrg, feishuApp, larkApp, dingTalkApp bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(bool_or(provider = 'github' AND account_kind <> 'app'), false),
		       COALESCE(bool_or(provider = 'gitee' AND account_kind <> 'app'), false),
		       COALESCE(bool_or(provider = 'zentao'), false),
		       COALESCE(bool_or(provider = 'pingcode'), false),
		       COALESCE(bool_or(provider = 'feishu' AND account_kind = 'app'), false),
		       COALESCE(bool_or(provider = 'lark' AND account_kind = 'app'), false),
		       COALESCE(bool_or(provider = 'dingtalk' AND account_kind = 'app'), false)
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND scope = 'ORGANIZATION' AND status = 'ACTIVE'
		   AND provider IN ('github', 'gitee', 'zentao', 'pingcode', 'feishu', 'lark', 'dingtalk')`).Scan(&githubOrg, &giteeOrg, &zentaoOrg, &pingCodeOrg, &feishuApp, &larkApp, &dingTalkApp); err != nil {
		return AccountBindingsView{}, err
	}
	// GitLab 系的实例：本组织未断开且状态正常的组织连接（个人连接按 parent_connection_id 区分实例）。
	instanceRows, err := tx.Query(ctx, `
		SELECT provider, id::text, display_name, COALESCE(settings->>'instance_url', '')
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND scope = 'ORGANIZATION' AND status = 'ACTIVE'
		   AND provider IN ('gitlab', 'gitlab_cn')
		 ORDER BY created_at, id LIMIT $1`, maxBindingInstances)
	if err != nil {
		return AccountBindingsView{}, err
	}
	instances := map[string][]AccountBindingInstance{}
	instanceIndex := map[string]*AccountBindingInstance{}
	for instanceRows.Next() {
		var provider, instanceURL string
		var in AccountBindingInstance
		if err := instanceRows.Scan(&provider, &in.ConnectionID, &in.DisplayName, &instanceURL); err != nil {
			instanceRows.Close()
			return AccountBindingsView{}, err
		}
		// 与集成页一致只显示主机（本机开发实例是 http，也去掉协议）。
		in.Host = SiteHost(strings.TrimPrefix(instanceURL, "http://"))
		instances[provider] = append(instances[provider], in)
	}
	instanceRows.Close()
	if err := instanceRows.Err(); err != nil {
		return AccountBindingsView{}, err
	}
	for provider := range instances {
		for i := range instances[provider] {
			instanceIndex[instances[provider][i].ConnectionID] = &instances[provider][i]
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT provider, id::text, display_name, COALESCE(settings->>'avatar_url', ''), COALESCE(settings->>'site_url', ''), created_at, version,
		       COALESCE(parent_connection_id::text, '')
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND scope = 'PERSONAL' AND owner_user_id = $1::uuid
		   AND status <> 'DISCONNECTED'
		   AND (provider IN ('github', 'gitee', 'gitlab', 'gitlab_cn', 'jira', 'feishu', 'lark', 'dingtalk')
		        OR (provider = 'pingcode' AND account_kind = 'sync_comments')
		        OR (provider = 'zentao' AND account_kind = 'sync_comments'))
		 ORDER BY created_at, id`, actor.UserID)
	if err != nil {
		return AccountBindingsView{}, err
	}
	bindings := map[string]*AccountBinding{}
	for rows.Next() {
		var provider, site, parent string
		var b AccountBinding
		if err := rows.Scan(&provider, &b.ConnectionID, &b.DisplayName, &b.AvatarURL, &site, &b.CreatedAt, &b.Version, &parent); err != nil {
			rows.Close()
			return AccountBindingsView{}, err
		}
		if provider == "jira" {
			b.Detail = SiteHost(site)
		}
		if isGitLabBinding(provider) {
			// 只汇总挂在本组织可用实例下的个人连接；同一实例只取最早的一条。
			in := instanceIndex[parent]
			if in == nil || in.Binding != nil {
				continue
			}
			b.Detail = in.Host
			bound := b
			in.Binding = &bound
			continue
		}
		if _, dup := bindings[provider]; !dup {
			bindings[provider] = &b
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AccountBindingsView{}, err
	}
	// 微信：不做微信登录，这里只汇总本人在本组织的 Agent ↔ 微信绑定（数量 + 脱敏的第一个微信），管理在集成详情页。
	var wechatCount int
	var wechatFirst AccountBinding
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE((array_agg(id::text ORDER BY created_at, id))[1], ''),
		       COALESCE((array_agg(left(ilink_user_id, 2) || '***' || right(ilink_user_id, 4) ORDER BY created_at, id))[1], ''),
		       COALESCE(min(created_at), clock_timestamp())
		  FROM openfox.wechat_bindings
		 WHERE organization_id = openfox_private.current_organization_id() AND created_by = $1::uuid AND status <> 'REMOVED'`, actor.UserID).
		Scan(&wechatCount, &wechatFirst.ConnectionID, &wechatFirst.DisplayName, &wechatFirst.CreatedAt); err != nil {
		return AccountBindingsView{}, err
	}
	if wechatCount > 0 {
		wechatFirst.Detail = fmt.Sprintf("%d 个 Agent 绑定", wechatCount)
		wechatFirst.Version = 1
		bindings["wechat"] = &wechatFirst
	}
	for _, p := range rowList {
		row := AccountBindingProvider{ID: p.id, Name: p.name, Availability: "coming_soon", Method: "none"}
		switch p.id {
		case "wechat":
			row.Availability, row.Method, row.OrganizationReady = "available", "link", wechatConfigured
		case "github":
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", githubOrg && githubConfigured
		case "gitee":
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", giteeOrg && giteeConfigured
		case "gitlab", "gitlab_cn":
			list := instances[p.id]
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", len(list) > 0
			row.Instances = append([]AccountBindingInstance{}, list...)
			// 行上的绑定取第一个已绑定实例；detail 显示实例主机，多个时显示「N 个实例」。
			bound := 0
			for i := range list {
				if list[i].Binding == nil {
					continue
				}
				bound++
				if row.Binding == nil {
					first := *list[i].Binding
					row.Binding = &first
				}
			}
			if row.Binding != nil && bound > 1 {
				row.Binding.Detail = fmt.Sprintf("%d 个实例", bound)
			}
		case "jira":
			row.Availability, row.Method, row.OrganizationReady = "available", "form", len(view.JiraSites) > 0
		case "zentao":
			row.Availability, row.Method, row.OrganizationReady = "available", "link", zentaoOrg
		case "pingcode":
			row.Availability, row.Method, row.OrganizationReady = "available", "link", pingCodeOrg
		case "feishu":
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", feishuApp
		case "lark":
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", larkApp
		case "dingtalk":
			row.Availability, row.Method, row.OrganizationReady = "available", "oauth", dingTalkApp
		}
		if row.Availability == "available" && !isGitLabBinding(p.id) {
			row.Binding = bindings[p.id]
		}
		view.Providers = append(view.Providers, row)
	}
	return view, nil
}

// JiraTarget 是 PrepareJiraBinding 校验通过的绑定目标。
type JiraTarget struct {
	SiteURL    string // 规范化后的站点地址
	Deployment string
	Email      string // 当前成员的 OpenFox 邮箱
}

// PrepareJiraBinding 是绑定的第一段短事务：成员为非访客，站点与部署方式必须是本组织导入过的。
func (s Service) PrepareJiraBinding(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, siteURL, deployment string) (JiraTarget, error) {
	if err := checkBindingActor(ctx, tx, organizationID, actor); err != nil {
		return JiraTarget{}, err
	}
	normalized, err := jiraapi.NormalizeSiteURL(siteURL)
	if err != nil {
		return JiraTarget{}, ErrJiraSiteUnknown
	}
	sites, err := loadJiraSites(ctx, tx)
	if err != nil {
		return JiraTarget{}, err
	}
	found := false
	for _, site := range sites {
		if SameSite(site.SiteURL, normalized) && site.Deployment == deployment {
			normalized, found = site.SiteURL, true
			break
		}
	}
	if !found {
		return JiraTarget{}, ErrJiraSiteUnknown
	}
	email, err := memberEmail(ctx, tx, actor.UserID)
	if err != nil {
		return JiraTarget{}, err
	}
	return JiraTarget{SiteURL: normalized, Deployment: deployment, Email: email}, nil
}

// JiraBindingResult 是 SaveJiraBinding 的结果。
type JiraBindingResult struct {
	Binding         AccountBinding `json:"binding"`
	LinkedAccounts  int            `json:"linked_accounts"`
	ReassignedTasks int            `json:"reassigned_tasks"`
	SkippedAccounts int            `json:"skipped_accounts"`
}

func recordConnectionAudit(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, action, connectionID string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actor.UserID, Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: "integration_connection", OrganizationID: organizationID, ID: connectionID},
		RequestID: newOperationID(), AuthorizationVersion: actor.AuthorizationVersion,
		SafeMetadata: map[string]string{"source": "jira", "result": "PERSONAL"}, CreatedAt: time.Now().UTC(),
	})
}

// SaveJiraBinding 是绑定的第二段短事务（凭证已在事务外校验并丢弃）：建立或替换个人 Jira 连接，
// 并把该站点下这个 Jira 身份、尚未关联的外部账号关联到本人。
func (s Service) SaveJiraBinding(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, target JiraTarget, user jiraapi.User) (JiraBindingResult, error) {
	if err := checkBindingActor(ctx, tx, organizationID, actor); err != nil {
		return JiraBindingResult{}, err
	}
	identity := strings.TrimSpace(user.Identity())
	if identity == "" || len(identity) > 255 {
		return JiraBindingResult{}, ErrInvalidRequest
	}
	displayName, _ := TruncateRunes(strings.TrimSpace(user.DisplayName), 200)
	if displayName == "" {
		displayName, _ = TruncateRunes(identity, 200)
	}
	settings, _ := json.Marshal(map[string]string{"site_url": target.SiteURL, "deployment": target.Deployment,
		"display_name": displayName, "email": strings.ToLower(strings.TrimSpace(target.Email))})

	var connectionID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'jira' AND scope = 'PERSONAL'
		   AND owner_user_id = $1::uuid AND parent_connection_id IS NULL AND status <> 'DISCONNECTED'
		 FOR UPDATE`, actor.UserID).Scan(&connectionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return JiraBindingResult{}, err
	}
	created := connectionID == ""
	if created {
		if connectionID, err = membership.NewID(); err != nil {
			return JiraBindingResult{}, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, owner_user_id, external_id, display_name,
			       account_kind, status, settings, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'jira', 'PERSONAL', $2::uuid, $3, $4, 'user', 'ACTIVE', $5::jsonb, $2::uuid)`,
			connectionID, actor.UserID, identity, displayName, settings)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET external_id = $2, display_name = $3, settings = $4::jsonb, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			connectionID, identity, displayName, settings)
	}
	if err != nil {
		if isUniqueViolation(err, "") {
			return JiraBindingResult{}, ErrJiraAccountInUse
		}
		return JiraBindingResult{}, err
	}
	action := "integration.connection_reauthorized"
	if created {
		action = "integration.connection_created"
	}
	if err := recordConnectionAudit(ctx, tx, organizationID, actor, action, connectionID); err != nil {
		return JiraBindingResult{}, err
	}

	result := JiraBindingResult{}
	// 关联外部账号；转任务不触发自动化。
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', 'link', true)`); err != nil {
		return JiraBindingResult{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(linked_user_id::text, '') FROM openfox.external_accounts
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = $1
		   AND lower(external_user_id) = lower($2) AND lower(source_label) = lower($3)
		 ORDER BY id FOR UPDATE`, SourceSystem, identity, SiteHost(target.SiteURL))
	if err != nil {
		return JiraBindingResult{}, err
	}
	type account struct{ id, linked string }
	var accounts []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.id, &a.linked); err != nil {
			rows.Close()
			return JiraBindingResult{}, err
		}
		accounts = append(accounts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return JiraBindingResult{}, err
	}
	for _, a := range accounts {
		switch {
		case a.linked == actor.UserID:
			continue // 已经是本人：幂等
		case a.linked != "":
			result.SkippedAccounts++
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.external_accounts SET linked_user_id = $2::uuid, linked_at = clock_timestamp(), link_method = 'verified',
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND linked_user_id IS NULL`,
			a.id, actor.UserID); err != nil {
			return JiraBindingResult{}, err
		}
		n, err := reassignTasks(ctx, tx, a.id, actor.UserID, actor.UserID)
		if err != nil {
			return JiraBindingResult{}, err
		}
		result.LinkedAccounts++
		result.ReassignedTasks += n
		if err := recordAccountAudit(ctx, tx, organizationID, actor, "external_account.linked", a.id,
			map[string]string{"result": "verified"}); err != nil {
			return JiraBindingResult{}, err
		}
	}

	b := AccountBinding{Detail: SiteHost(target.SiteURL)}
	if err := tx.QueryRow(ctx, `
		SELECT id::text, display_name, COALESCE(settings->>'avatar_url', ''), created_at, version
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, connectionID).
		Scan(&b.ConnectionID, &b.DisplayName, &b.AvatarURL, &b.CreatedAt, &b.Version); err != nil {
		return JiraBindingResult{}, err
	}
	result.Binding = b
	return result, nil
}

// loadJiraVerified 读取与导入任务同站点的 Jira 个人绑定：小写 Jira 账号标识 → 成员 ID（仅活跃成员）。
func loadJiraVerified(ctx context.Context, tx pgx.Tx, jobID string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT lower(c.external_id), c.owner_user_id::text
		  FROM openfox.import_jobs j
		  JOIN openfox.integration_connections c ON c.organization_id = j.organization_id AND c.provider = 'jira' AND c.scope = 'PERSONAL'
		       AND c.status <> 'DISCONNECTED' AND lower(c.settings->>'site_url') = lower(j.preview->'api'->>'site_url')
		  JOIN openfox.organization_members m ON m.organization_id = c.organization_id AND m.user_id = c.owner_user_id AND m.status = 'ACTIVE'
		 WHERE j.organization_id = openfox_private.current_organization_id() AND j.id = $1::uuid`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var identity, userID string
		if err := rows.Scan(&identity, &userID); err != nil {
			return nil, err
		}
		out[identity] = userID
	}
	return out, rows.Err()
}
