package imports

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/membership"
	"strings"
)

func analyzeAsana(ctx context.Context, tx pgx.Tx, space string, cfg Config, a *Analysis) error {
	if len(cfg.Types) > 50 || len(cfg.PriorityFields) != 0 || len(cfg.Priorities) != 0 {
		return invalidConfig("Asana 类型映射超限或包含不支持的优先级配置")
	}
	mappings := map[string]TypeMapping{}
	ids := []string{}
	for _, m := range cfg.Types {
		if m.Source == "" || len(m.Source) > 100 || mappings[m.Source].Source != "" || m.UseDefault == (m.TypeID != "") {
			return invalidConfig("Asana 类型须选择目标类型或空间默认类型")
		}
		mappings[m.Source] = m
		if m.TypeID != "" {
			if space == "" || !membership.ValidateUUID(m.TypeID) {
				return invalidConfig("新空间只允许默认类型")
			}
			ids = append(ids, m.TypeID)
		}
	}
	if len(ids) > 0 {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_types WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=ANY($2::uuid[]) AND enabled AND archived_at IS NULL`, space, uniqueStrings(ids)).Scan(&count); e != nil {
			return e
		}
		if count != len(uniqueStrings(ids)) {
			return invalidConfig("目标类型不在当前空间或已停用")
		}
	}
	for i := range a.Types {
		m, ok := mappings[a.Types[i].Source]
		a.Types[i].TypeID = nil
		if !ok {
			a.Issues = append(a.Issues, Issue{Code: "type_unmapped", Value: a.Types[i].Source, Message: "Asana 来源类型未映射"})
		} else if !m.UseDefault {
			a.Types[i].TypeID = strPtr(m.TypeID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(category) category,id::text FROM openfox.task_statuses WHERE organization_id=openfox_private.current_organization_id() AND category IN('TODO','DONE') AND archived_at IS NULL ORDER BY category,position,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	categories := map[string]string{}
	for rows.Next() {
		var category, id string
		if err = rows.Scan(&category, &id); err != nil {
			return err
		}
		categories[category] = id
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range a.Statuses {
		category := "TODO"
		if a.Statuses[i].Source == "completed" {
			category = "DONE"
		}
		a.Statuses[i].SuggestedStatusID = strPtr(categories[category])
	}
	return nil
}
func resolveAsanaTypes(ctx context.Context, tx pgx.Tx, b *batchCtx, res *Resolved, infos []rowInfo) error {
	a := Analysis{Types: []AnalysisType{}}
	seen := map[string]bool{}
	for _, info := range infos {
		if !info.Invalid && !info.Filtered && !seen[info.Data.TypeName] {
			seen[info.Data.TypeName] = true
			a.Types = append(a.Types, AnalysisType{Source: info.Data.TypeName})
		}
	}
	if err := analyzeAsana(ctx, tx, b.job.SpaceID, b.job.Cfg, &a); err != nil {
		return err
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
