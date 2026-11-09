package collaboration

// 推荐任务类型模板（后端常量）。
// 名称与描述为英文默认值，界面按 key 翻译；Purposes 是适用的空间用途（13 个推荐分类映射到 14 个用途）。

// TaskTypeTemplate 是一个推荐任务类型。
type TaskTypeTemplate struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Color       string   `json:"color"`
	Purposes    []string `json:"purposes"`
}

// templatePurposeOrder 是空间用途的全部取值（与 spaces_purpose_valid 约束一致），用于模板输出顺序。
var templatePurposeOrder = []string{
	"software_development", "product_management", "marketing", "design", "project_management",
	"operations", "it_support", "human_resources", "customer_service", "legal", "finance", "sales",
	"data_science", "other",
}

// 推荐分类 → 空间用途。
var (
	purposeEngineering  = []string{"software_development", "product_management", "data_science"}
	purposeMarketing    = []string{"marketing"}
	purposeCreative     = []string{"design"}
	purposePMO          = []string{"project_management"}
	purposeOperations   = []string{"operations"}
	purposeIT           = []string{"it_support"}
	purposeHR           = []string{"human_resources"}
	purposeSupport      = []string{"customer_service"}
	purposeProfessional = []string{"legal"}
	purposeFinance      = []string{"finance"}
	purposeSales        = []string{"sales"}
	purposeOther        = []string{"other"}
)

func purposes(groups ...[]string) []string {
	seen := map[string]bool{}
	for _, group := range groups {
		for _, purpose := range group {
			seen[purpose] = true
		}
	}
	// 按 templatePurposeOrder 的固定顺序输出。
	result := make([]string, 0, len(seen))
	for _, purpose := range templatePurposeOrder {
		if seen[purpose] {
			result = append(result, purpose)
		}
	}
	return result
}

// TaskTypeTemplates 是全部推荐模板，按界面展示顺序排列。
var TaskTypeTemplates = []TaskTypeTemplate{
	{Key: "task", Name: "Task", Description: "Track general tasks and follow-up tasks", Icon: "check-square", Color: "#64748B",
		Purposes: append([]string(nil), templatePurposeOrder...)},
	{Key: "requirement", Name: "Requirement", Description: "Track product requirements and specifications", Icon: "file-text", Color: "#7C3AED",
		Purposes: purposes(purposeEngineering, purposePMO)},
	{Key: "asset", Name: "Asset", Description: "Track and manage physical or digital assets", Icon: "image", Color: "#0284C7",
		Purposes: purposes(purposeCreative, purposeFinance, purposeIT)},
	{Key: "campaign", Name: "Campaign", Description: "Plan and monitor marketing or promotional campaigns", Icon: "megaphone", Color: "#EA580C",
		Purposes: purposes(purposeCreative, purposeMarketing, purposeSales)},
	{Key: "content", Name: "Content", Description: "Manage creation and publication of content pieces", Icon: "shapes", Color: "#DB2777",
		Purposes: purposes(purposeCreative, purposeMarketing)},
	{Key: "deal", Name: "Deal", Description: "Track progress and details of business deals and negotiations", Icon: "briefcase", Color: "#16A34A",
		Purposes: purposes(purposeFinance, purposeOperations, purposeSales)},
	{Key: "account", Name: "Account", Description: "Manage client or customer account details and interactions", Icon: "building", Color: "#2563EB",
		Purposes: purposes(purposeFinance, purposeProfessional, purposeSales)},
	{Key: "bug", Name: "Bug", Description: "Identify and track software bugs for resolution", Icon: "bug", Color: "#DC2626",
		Purposes: purposes(purposeEngineering, purposeIT, purposeSupport)},
	{Key: "feature", Name: "Feature", Description: "Plan and deliver new product capabilities and enhancements", Icon: "lightbulb", Color: "#7C3AED",
		Purposes: purposes(purposeEngineering)},
	{Key: "issue", Name: "Issue", Description: "Track problems, questions, or follow-up items that need resolution", Icon: "circle-dot", Color: "#F97316",
		Purposes: purposes(purposeEngineering, purposeIT, purposeSupport)},
	{Key: "ticket", Name: "Ticket", Description: "Track a feature, bug, improvement, or other work item", Icon: "inbox", Color: "#2563EB",
		Purposes: purposes(purposeEngineering, purposeIT, purposeSupport)},
	{Key: "initiative", Name: "Initiative", Description: "Organize efforts aimed at achieving broader business goals", Icon: "boxes", Color: "#7C3AED",
		Purposes: purposes(purposeHR, purposeOther, purposePMO)},
	{Key: "user_story", Name: "User Story", Description: "Detail requirements or features from an end-user perspective", Icon: "app-window", Color: "#0284C7",
		Purposes: purposes(purposeEngineering, purposeSupport)},
	{Key: "story", Name: "Story", Description: "Describe requirements from the user's perspective", Icon: "bookmark", Color: "#7C3AED",
		Purposes: purposes(purposeEngineering)},
	{Key: "goal", Name: "Goal", Description: "Set and monitor progress towards specific objectives", Icon: "trophy", Color: "#CA8A04",
		Purposes: purposes(purposeOther, purposePMO)},
	{Key: "lead", Name: "Lead", Description: "Manage potential clients or customers in the sales pipeline", Icon: "user-check", Color: "#0D9488",
		Purposes: purposes(purposeMarketing, purposeSales)},
	{Key: "key_result", Name: "Key Result", Description: "Measure outcomes used to track progress toward goals", Icon: "key", Color: "#7C3AED",
		Purposes: purposes(purposePMO)},
	{Key: "request", Name: "Request", Description: "Handle incoming requests or tasks from clients or team members", Icon: "message-circle", Color: "#16A34A",
		Purposes: purposes(purposeEngineering, purposeSupport)},
	{Key: "person", Name: "Person", Description: "Track interactions and information related to individuals", Icon: "user", Color: "#64748B",
		Purposes: purposes(purposeHR)},
	{Key: "resource", Name: "Resource", Description: "Allocate and monitor resources such as staff or equipment", Icon: "book", Color: "#16A34A",
		Purposes: purposes(purposeHR, purposeOperations, purposeIT)},
}

// FindTaskTypeTemplate 按 key 查找模板。
func FindTaskTypeTemplate(key string) (TaskTypeTemplate, bool) {
	for _, template := range TaskTypeTemplates {
		if template.Key == key {
			return template, true
		}
	}
	return TaskTypeTemplate{}, false
}

// TemplatesForPurpose 返回适用于该用途的模板；purpose 为空时返回全部；用途不合法时 ok = false。
func TemplatesForPurpose(purpose string) (items []TaskTypeTemplate, ok bool) {
	if purpose != "" {
		known := false
		for _, candidate := range templatePurposeOrder {
			if candidate == purpose {
				known = true
				break
			}
		}
		if !known {
			return nil, false
		}
	}
	items = make([]TaskTypeTemplate, 0, len(TaskTypeTemplates))
	for _, template := range TaskTypeTemplates {
		if purpose == "" {
			items = append(items, template)
			continue
		}
		for _, candidate := range template.Purposes {
			if candidate == purpose {
				items = append(items, template)
				break
			}
		}
	}
	return items, true
}
