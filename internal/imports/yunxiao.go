package imports

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/yunxiao"
	"openfox/internal/membership"
)

type AnalysisPriorityField struct {
	TypeSource string                  `json:"type_source"`
	TypeLabel  string                  `json:"type_label"`
	FieldID    string                  `json:"field_id"`
	FieldLabel string                  `json:"field_label"`
	Values     []yunxiao.PriorityValue `json:"values"`
}

func analyzeYunxiao(ctx context.Context, tx pgx.Tx, space string, cfg Config, infos []rowInfo, l lookups, a *Analysis) error {
	types := map[string]TypeMapping{}
	for _, m := range cfg.Types {
		types[m.Source] = m
	}
	fields := map[string]int{}
	fieldChoices := map[string]string{}
	for _, f := range cfg.PriorityFields {
		fieldChoices[f.TypeSource] = f.FieldID
	}
	seenOptions := map[string]bool{}
	issueSeen := map[string]bool{}
	issue := func(code, value, message string) {
		if !issueSeen[code+value] {
			a.Issues = append(a.Issues, Issue{Code: code, Value: value, Message: message})
			issueSeen[code+value] = true
		}
	}
	for _, info := range infos {
		d := info.Data
		meta := d.Yunxiao
		if meta == nil {
			continue
		}
		for i := range a.Statuses {
			if a.Statuses[i].Source == d.Status {
				a.Statuses[i].SourceLabel = meta.TypeLabel + " / " + meta.StatusLabel
				a.Statuses[i].SuggestedStatusID = strPtr(l.statusByName[strings.ToLower(meta.StatusLabel)])
			}
		}
		for i := range a.Types {
			if a.Types[i].Source == d.TypeName {
				a.Types[i].SourceLabel = meta.TypeLabel
				a.Types[i].TypeID = strPtr(l.typeByName[strings.ToLower(meta.TypeLabel)])
			}
		}
		if info.Invalid || info.Filtered {
			continue
		}
		if m, ok := types[d.TypeName]; !ok || (!m.UseDefault && m.TypeID == "") {
			issue("type_unmapped", d.TypeName, "请选择云效类型的目标或空间默认类型")
		}
		for _, v := range meta.Priorities {
			k := d.TypeName + "/" + v.FieldID
			i, ok := fields[k]
			if !ok {
				i = len(a.PriorityFields)
				fields[k] = i
				a.PriorityFields = append(a.PriorityFields, AnalysisPriorityField{TypeSource: d.TypeName, TypeLabel: meta.TypeLabel, FieldID: v.FieldID, FieldLabel: v.FieldLabel, Values: []yunxiao.PriorityValue{}})
			}
			if !seenOptions[v.Source] {
				a.PriorityFields[i].Values = append(a.PriorityFields[i].Values, v)
				seenOptions[v.Source] = true
			}
			// Presence of actual single-select values requires an explicit field or
			// explicit empty choice; omission is never silently interpreted as NONE.
			if _, ok := fieldChoices[d.TypeName]; !ok {
				issue("priority_field_unmapped", d.TypeName, "请选择来源优先级字段，或明确选择未设置")
			}
		}
		if d.PriorityUnmapped {
			issue("priority_unmapped", d.TypeName, "所选来源优先级值尚未映射")
		}
	}
	for kind, field := range fieldChoices {
		if field != "" {
			if _, ok := fields[kind+"/"+field]; !ok {
				return invalidConfig("所选优先级字段不在云效来源中")
			}
		}
	}
	return validateYunxiaoTypes(ctx, tx, space, cfg)
}
func validateYunxiaoTypes(ctx context.Context, tx pgx.Tx, space string, cfg Config) error {
	if len(cfg.Types) > 500 || len(cfg.PriorityFields) > 500 || len(cfg.Priorities) > 5000 {
		return invalidConfig("云效映射超过上限")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, m := range cfg.Types {
		if len(m.Source) != 64 || seen[m.Source] || m.UseDefault == (m.TypeID != "") {
			return invalidConfig("云效类型必须选择已有类型或空间默认类型")
		}
		seen[m.Source] = true
		if m.TypeID != "" {
			if space == "" || !membership.ValidateUUID(m.TypeID) {
				return invalidConfig("新空间只允许默认类型")
			}
			ids = append(ids, m.TypeID)
		}
	}
	if len(ids) > 0 {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_types WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=ANY($2::uuid[]) AND enabled AND archived_at IS NULL`, space, uniqueStrings(ids)).Scan(&n)
		if err != nil {
			return err
		}
		if n != len(uniqueStrings(ids)) {
			return invalidConfig("目标类型不在当前空间或已停用")
		}
	}
	seen = map[string]bool{}
	for _, m := range cfg.PriorityFields {
		if len(m.TypeSource) != 64 || seen[m.TypeSource] || m.FieldID != "" && !yunxiao.ValidID(m.FieldID) {
			return invalidConfig("来源优先级字段无效或重复")
		}
		seen[m.TypeSource] = true
	}
	seen = map[string]bool{}
	for _, m := range cfg.Priorities {
		if len(m.Source) != 64 || seen[m.Source] {
			return invalidConfig("来源优先级映射无效或重复")
		}
		seen[m.Source] = true
		switch m.Priority {
		case "NONE", "URGENT", "HIGH", "NORMAL", "LOW", "LOWEST":
		default:
			return invalidConfig("目标优先级无效")
		}
	}
	return nil
}
func resolveYunxiaoTypes(ctx context.Context, tx pgx.Tx, b *batchCtx, res *Resolved, infos []rowInfo) error {
	if err := validateYunxiaoTypes(ctx, tx, b.job.SpaceID, b.job.Cfg); err != nil {
		return err
	}
	mappings := map[string]TypeMapping{}
	for _, m := range b.job.Cfg.Types {
		mappings[m.Source] = m
	}
	for _, info := range infos {
		if info.Invalid || info.Filtered {
			continue
		}
		m, ok := mappings[info.Data.TypeName]
		if !ok {
			return withDetail(ErrConfigIncomplete, "云效类型未映射")
		}
		if !m.UseDefault {
			res.Types[info.Data.TypeName] = m.TypeID
		}
		if info.Data.PriorityUnmapped {
			return withDetail(ErrConfigIncomplete, "云效优先级未映射")
		}
	}
	return nil
}

func checkYunxiaoImportSource(ctx context.Context, tx pgx.Tx, origin ProjectOrigin) error {
	var version uint64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='yunxiao' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, origin.Connection).Scan(&version)
	if err != nil {
		return err
	}
	if version != origin.ConnectionVersion {
		return ErrVersionConflict
	}
	return nil
}
func initialSyncDirection(source string) string {
	if source == "yunxiao_api" || source == SourceAsana || source == SourceAzure {
		return "INBOUND"
	}
	return "BIDIRECTIONAL"
}
