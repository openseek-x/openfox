// Package integrations 实现外部应用集成的服务商目录、总览、详情与断开连接。
// 服务商目录是代码里的静态清单，不入库；调用方必须已在组织事务中（RLS 生效）。
package integrations

// 分类、可用性、模板与能力的取值与契约 IntegrationProvider 一致。
const (
	CategoryTask          = "task"
	CategoryGit           = "git"
	CategoryCollaboration = "collaboration"

	AvailabilityAvailable  = "available"
	AvailabilityComingSoon = "coming_soon"
)

// Provider 是目录里的一个服务商。同一服务商在多个分组出现时只保留一项，Category 取主分组。
type Provider struct {
	ID           string
	Name         string
	Category     string
	Availability string
	Template     string
	Capabilities []string
	// Bindable 表示当前版本是否已开放绑定；对应子阶段实现后改为 true。
	Bindable bool
}

const (
	capSync          = "sync"
	capImport        = "import"
	capPRLink        = "pr_link"
	capIMNotify      = "im_notify"
	capTaskAssistant = "task_assistant"
	capAgentBinding  = "agent_binding"
	capPersonal      = "personal_account"
)

func taskProvider(id, name, availability string) Provider {
	return Provider{ID: id, Name: name, Category: CategoryTask, Availability: availability, Template: "A",
		Capabilities: []string{capSync, capImport, capPersonal}}
}

func gitProvider(id, name, availability string) Provider {
	return Provider{ID: id, Name: name, Category: CategoryGit, Availability: availability, Template: "B",
		Capabilities: []string{capSync, capPRLink, capPersonal}}
}

func collabProvider(id, name, availability, template string, capabilities ...string) Provider {
	return Provider{ID: id, Name: name, Category: CategoryCollaboration, Availability: availability, Template: template,
		Capabilities: capabilities}
}

// withBindable 标记目录中可绑定的服务商。
func withBindable(p Provider) Provider {
	p.Bindable = true
	return p
}

// catalog 的顺序即展示顺序：先任务管理，再 Git 仓库，最后协作。
// github_issues 并入 github，gitlab_cn / gitlab / gitee 的 issues 入口与 Git 入口共用一项（主分组 git）。
var catalog = []Provider{
	taskProvider("jira", "Jira", AvailabilityAvailable),
	withBindable(taskProvider("tapd", "TAPD", AvailabilityAvailable)),
	taskProvider("zentao", "禅道", AvailabilityAvailable),
	withBindable(taskProvider("teambition", "Teambition", AvailabilityAvailable)),
	withBindable(taskProvider("pingcode", "PingCode", AvailabilityAvailable)),
	taskProvider("yunxiao", "云效", AvailabilityComingSoon),
	taskProvider("linear", "Linear", AvailabilityComingSoon),
	taskProvider("ones", "ONES", AvailabilityComingSoon),
	taskProvider("asana", "Asana", AvailabilityComingSoon),
	withBindable(taskProvider("shortcut", "Shortcut", AvailabilityAvailable)),
	taskProvider("clickup", "ClickUp", AvailabilityComingSoon),

	gitProvider("github", "GitHub", AvailabilityAvailable),
	withBindable(gitProvider("cnb", "CNB", AvailabilityAvailable)),
	gitProvider("gitlab_cn", "极狐 GitLab.cn", AvailabilityAvailable),
	gitProvider("gitlab", "GitLab 自部署版", AvailabilityAvailable),
	gitProvider("gitee", "Gitee", AvailabilityAvailable),
	gitProvider("codeup", "云效 Codeup", AvailabilityComingSoon),
	gitProvider("bitbucket", "Bitbucket", AvailabilityComingSoon),

	withBindable(collabProvider("feishu", "飞书", AvailabilityAvailable, "C1", capIMNotify, capTaskAssistant, capAgentBinding, capPersonal)),
	withBindable(collabProvider("lark", "Lark", AvailabilityAvailable, "C1", capIMNotify, capTaskAssistant, capAgentBinding, capPersonal)),
	withBindable(collabProvider("dingtalk", "钉钉", AvailabilityAvailable, "C1", capIMNotify, capTaskAssistant, capAgentBinding, capPersonal)),
	withBindable(collabProvider("wecom", "企业微信", AvailabilityAvailable, "C2", capIMNotify, capTaskAssistant, capAgentBinding, capPersonal)),
	collabProvider("wechat", "微信", AvailabilityAvailable, "C3", capAgentBinding, capPersonal),
	collabProvider("slack", "Slack", AvailabilityComingSoon, "C4", capIMNotify, capAgentBinding, capPersonal),
	collabProvider("teams", "Microsoft Teams", AvailabilityComingSoon, "C2", capIMNotify, capAgentBinding),
	collabProvider("discord", "Discord", AvailabilityComingSoon, "C2", capIMNotify, capAgentBinding),
	collabProvider("telegram", "Telegram", AvailabilityComingSoon, "C2", capIMNotify, capAgentBinding),
}

// Catalog 返回目录副本（调用方可安全修改）。
func Catalog() []Provider {
	items := make([]Provider, len(catalog))
	for i, item := range catalog {
		item.Capabilities = append([]string(nil), item.Capabilities...)
		items[i] = item
	}
	return items
}

// Lookup 按服务商 ID 查找；未知返回 false。
func Lookup(id string) (Provider, bool) {
	for _, item := range catalog {
		if item.ID == id {
			item.Capabilities = append([]string(nil), item.Capabilities...)
			return item, true
		}
	}
	return Provider{}, false
}
