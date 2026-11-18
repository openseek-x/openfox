package imports

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/yunxiao"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/membership"
	"strings"
)

func analyzeZoho(ctx context.Context, tx pgx.Tx, space string, cfg Config, infos []rowInfo, l lookups, a *Analysis) error {
	if cfg.Options.ContinuousSync || cfg.Options.KeepNumbers || cfg.Options.FileProjects != nil && *cfg.Options.FileProjects || len(cfg.PriorityFields) != 0 || len(cfg.Types) > 1 || len(cfg.Priorities) > 500 {
		return invalidConfig("Zoho 只支持一次性 Bug 导入及已公开字段映射")
	}
	for _, u := range cfg.Users {
		if u.Mode == "invite" {
			return invalidConfig("Zoho 导入不自动邀请成员")
		}
	}
	statuses := map[string]bool{}
	for _, m := range cfg.Statuses {
		statuses[m.Source] = true
	}
	mappedType := false
	for _, m := range cfg.Types {
		if m.Source != "Bug" || m.UseDefault == (m.TypeID != "") {
			return invalidConfig("请选择 Bug 的目标类型或空间默认类型")
		}
		mappedType = true
		if m.TypeID != "" {
			if space == "" || !membership.ValidateUUID(m.TypeID) {
				return invalidConfig("新空间只能选择默认类型")
			}
			var count int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_types WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=$2::uuid AND enabled AND archived_at IS NULL`, space, m.TypeID).Scan(&count); e != nil {
				return e
			}
			if count != 1 {
				return invalidConfig("目标类型已不可用")
			}
		}
	}
	priorities := map[string]string{}
	for _, m := range cfg.Priorities {
		if !zohobugtracker.ValidID(m.Source) || priorities[m.Source] != "" {
			return invalidConfig("Zoho severity 映射无效或重复")
		}
		switch m.Priority {
		case "NONE", "URGENT", "HIGH", "NORMAL", "LOW":
		case "LOWEST":
			if l.priorityLevels != 5 {
				return invalidConfig("当前组织不提供最低优先级，请选择现有档位")
			}
		default:
			return invalidConfig("目标优先级无效")
		}
		priorities[m.Source] = m.Priority
	}
	seen := map[string]bool{}
	values := []yunxiao.PriorityValue{}
	issueSeen := map[string]bool{}
	issue := func(code, value, message string) {
		if !issueSeen[code+value] {
			a.Issues = append(a.Issues, Issue{Code: code, Value: value, Message: message})
			issueSeen[code+value] = true
		}
	}
	for _, info := range infos {
		if info.Invalid || info.Filtered {
			continue
		}
		if !mappedType {
			issue("type_unmapped", "Bug", "请选择 Bug 的目标类型或空间默认类型")
		}
		if info.Data.Zoho == nil {
			return invalidConfig("Zoho 来源元数据缺失")
		}
		meta := info.Data.Zoho
		if !statuses[info.Data.Status] {
			issue(IssueStatusUnmapped, info.Data.Status, "请显式选择每个来源状态的目标状态")
		}
		for i := range a.Statuses {
			if a.Statuses[i].Source == info.Data.Status {
				a.Statuses[i].SourceLabel = meta.StatusLabel
				a.Statuses[i].SuggestedStatusID = nil
			}
		}
		source := meta.SeverityID
		if source != "" && !seen[source] {
			seen[source] = true
			values = append(values, yunxiao.PriorityValue{Source: source, FieldID: "severity", FieldLabel: "Severity", OptionID: source, OptionLabel: meta.SeverityLabel})
		}
		if info.Data.PriorityUnmapped {
			issue("priority_unmapped", source, "请显式选择每个来源 severity 的目标优先级")
		}
	}
	if len(values) > 0 {
		a.PriorityFields = []AnalysisPriorityField{{TypeSource: "Bug", TypeLabel: "Bug", FieldID: "severity", FieldLabel: "Severity", Values: values}}
	}
	a.Capabilities.ProjectSync = false
	a.Capabilities.KeepNumbers = false
	return nil
}
func resolveZohoMappings(ctx context.Context, tx pgx.Tx, b *batchCtx, res *Resolved, infos []rowInfo, l lookups) error {
	a := Analysis{Types: []AnalysisType{}, Issues: []Issue{}}
	if e := analyzeZoho(ctx, tx, b.job.SpaceID, b.job.Cfg, infos, l, &a); e != nil {
		return e
	}
	if len(a.Issues) > 0 {
		return ErrConfigIncomplete
	}
	for _, m := range b.job.Cfg.Types {
		if m.UseDefault {
			delete(res.Types, strings.ToLower(m.Source))
		} else {
			res.Types[strings.ToLower(m.Source)] = m.TypeID
		}
	}
	return nil
}
