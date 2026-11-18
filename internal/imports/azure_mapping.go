package imports

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/integrations/yunxiao"
	"openfox/internal/membership"
)

func analyzeAzure(ctx context.Context, tx pgx.Tx, job, space string, cfg Config, infos []rowInfo, l lookups, a *Analysis) error {
	var raw []byte
	if e := tx.QueryRow(ctx, `SELECT metadata FROM openfox.import_azure_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, job).Scan(&raw); e != nil {
		return e
	}
	var meta AzureMetadata
	if json.Unmarshal(raw, &meta) != nil {
		return errAzureShape
	}
	if len(cfg.Types) > 50 || len(cfg.Priorities) > 5000 || len(cfg.PriorityFields) != 0 {
		return invalidConfig("Azure Boards 映射超过上限或包含不支持的字段配置")
	}
	types := map[string]TypeMapping{}
	ids := []string{}
	for _, m := range cfg.Types {
		if m.Source == "" || len(m.Source) > 256 || types[m.Source].Source != "" || m.UseDefault == (m.TypeID != "") {
			return invalidConfig("Azure Boards 类型须明确选择目标类型或默认类型")
		}
		types[m.Source] = m
		if m.TypeID != "" {
			if space == "" || !membership.ValidateUUID(m.TypeID) {
				return invalidConfig("目标类型无效")
			}
			ids = append(ids, m.TypeID)
		}
	}
	if len(ids) > 0 {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_types WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=ANY($2::uuid[]) AND enabled AND archived_at IS NULL`, space, uniqueStrings(ids)).Scan(&n); e != nil {
			return e
		}
		if n != len(uniqueStrings(ids)) {
			return invalidConfig("目标类型不在当前空间或已停用")
		}
	}
	typeIndex, statusIndex := map[string]int{}, map[string]int{}
	for i, v := range a.Types {
		typeIndex[v.Source] = i
	}
	for i, v := range a.Statuses {
		statusIndex[v.Source] = i
	}
	for _, entry := range meta.Types {
		kind := entry.Type.ReferenceName
		i, ok := typeIndex[kind]
		if !ok && cfg.Options.ContinuousSync {
			i = len(a.Types)
			typeIndex[kind] = i
			a.Types = append(a.Types, AnalysisType{Source: kind})
		}
		if ok || cfg.Options.ContinuousSync {
			a.Types[i].SourceLabel = entry.Type.Name
		}
		for _, state := range entry.States {
			source := kind + "/" + state.Name
			i, ok := statusIndex[source]
			if !ok && cfg.Options.ContinuousSync {
				i = len(a.Statuses)
				statusIndex[source] = i
				a.Statuses = append(a.Statuses, AnalysisStatus{Source: source, Done: state.Category == "Completed" || state.Category == "Removed"})
			}
			if ok || cfg.Options.ContinuousSync {
				a.Statuses[i].SourceLabel = entry.Type.Name + " / " + state.Name
				a.Statuses[i].SuggestedStatusID = strPtr(l.statusByName[strings.ToLower(state.Name)])
			}
		}
	}
	for i := range a.Types {
		m, ok := types[a.Types[i].Source]
		a.Types[i].TypeID = nil
		if !ok {
			a.Issues = append(a.Issues, Issue{Code: "type_unmapped", Value: a.Types[i].Source, Message: "Azure Boards 来源类型未映射"})
		} else if !m.UseDefault {
			a.Types[i].TypeID = strPtr(m.TypeID)
		}
	}
	statusMappings := map[string]bool{}
	for _, m := range cfg.Statuses {
		statusMappings[strings.ToLower(m.Source)] = true
	}
	issueSeen := map[string]bool{}
	for _, i := range a.Issues {
		issueSeen[i.Code+"/"+i.Value] = true
	}
	for _, s := range a.Statuses {
		if !statusMappings[strings.ToLower(s.Source)] && !issueSeen[IssueStatusUnmapped+"/"+s.Source] {
			a.Issues = append(a.Issues, Issue{Code: IssueStatusUnmapped, Value: s.Source, Message: "Azure Boards 来源状态未显式映射"})
		}
	}
	priorities := map[string]string{}
	for _, m := range cfg.Priorities {
		if meta.PriorityField == "" || !strings.HasPrefix(m.Source, meta.PriorityField+"=") || len(m.Source) > 513 || priorities[m.Source] != "" {
			return invalidConfig("Azure Boards 来源优先级映射无效或重复")
		}
		switch m.Priority {
		case "NONE", "URGENT", "HIGH", "NORMAL", "LOW", "LOWEST":
		default:
			return invalidConfig("目标优先级无效")
		}
		priorities[m.Source] = m.Priority
	}
	if meta.PriorityField != "" {
		label := meta.PriorityField
		for _, f := range meta.Fields {
			if f.ReferenceName == meta.PriorityField {
				label = f.Name
			}
		}
		field := AnalysisPriorityField{FieldID: meta.PriorityField, FieldLabel: label, Values: []yunxiao.PriorityValue{}}
		seen := map[string]bool{}
		for _, info := range infos {
			if info.Data.Azure == nil || info.Data.Azure.PriorityValue == "" {
				continue
			}
			value := info.Data.Azure.PriorityValue
			source := azurePrioritySource(meta.PriorityField, value)
			if seen[source] {
				continue
			}
			seen[source] = true
			field.Values = append(field.Values, yunxiao.PriorityValue{FieldID: meta.PriorityField, FieldLabel: label, OptionID: value, OptionLabel: value, Source: source})
			if priorities[source] == "" {
				a.Issues = append(a.Issues, Issue{Code: "priority_unmapped", Value: source, Message: "Azure Boards 所选优先级值未显式映射"})
			}
		}
		a.PriorityFields = []AnalysisPriorityField{field}
	}
	if cfg.Options.ContinuousSync && len(infos) == 0 {
		kept := a.Issues[:0]
		for _, i := range a.Issues {
			if i.Code != IssueNothingToImport {
				kept = append(kept, i)
			}
		}
		a.Issues = kept
	}
	return nil
}

func (w *Worker) resolveAzureMappings(ctx context.Context, tx pgx.Tx, b *batchCtx, res *Resolved, infos []rowInfo, l lookups) error {
	a := Analysis{Types: []AnalysisType{}, Statuses: []AnalysisStatus{}, Issues: []Issue{}}
	seen := map[string]bool{}
	for _, info := range infos {
		if !info.Invalid && !info.Filtered && !seen[info.Data.TypeName] {
			seen[info.Data.TypeName] = true
			a.Types = append(a.Types, AnalysisType{Source: info.Data.TypeName})
		}
	}
	if e := analyzeAzure(ctx, tx, b.job.ID, b.job.SpaceID, b.job.Cfg, infos, l, &a); e != nil {
		return e
	}
	if len(a.Issues) > 0 {
		return ErrConfigIncomplete
	}
	used := []string{}
	for _, m := range b.job.Cfg.Types {
		if m.UseDefault {
			delete(res.Types, strings.ToLower(m.Source))
		} else {
			res.Types[strings.ToLower(m.Source)] = m.TypeID
		}
	}
	for _, m := range b.job.Cfg.Statuses {
		k := strings.ToLower(m.Source)
		if res.Statuses[k] != "" {
			continue
		}
		id, e := w.resolveStatus(ctx, tx, b, l, m, k)
		if e != nil {
			return e
		}
		if id == "" {
			return ErrConfigIncomplete
		}
		res.Statuses[k] = id
		used = append(used, id)
	}
	return w.ensureWorkflow(ctx, tx, b, used)
}

func loadAzureVerifiedBindings(ctx context.Context, tx pgx.Tx, origin ProjectOrigin, ids []string, lock bool) (map[string]VerifiedBinding, error) {
	out := map[string]VerifiedBinding{}
	a, e := integrations.LockAzureAccess(ctx, tx, origin.Connection, 0, 0)
	if e != nil {
		if e == integrations.ErrNotFound || e == integrations.ErrVersionConflict {
			return out, nil
		}
		return out, e
	}
	if origin.Instance != azureInstance(a.AccountID, origin.Project) {
		return out, nil
	}
	subjects := []string{}
	for _, id := range ids {
		prefix := a.AccountID + "/"
		if strings.HasPrefix(id, prefix) && azuredevops.ValidUUID(strings.TrimPrefix(id, prefix)) {
			subjects = append(subjects, strings.TrimPrefix(id, prefix))
		}
	}
	if len(subjects) == 0 {
		return out, nil
	}
	suffix := ""
	if lock {
		// Claim comes before member/child, matching bind/unlink. A joined lock can
		// otherwise lock child first while a competing bind already owns its claim.
		claims, e := tx.Query(ctx, `SELECT subject_id FROM openfox.integration_azure_identity_claims WHERE organization_id=openfox_private.current_organization_id() AND account_id=$1 AND subject_id=ANY($2::text[]) ORDER BY subject_id FOR SHARE`, a.AccountID, subjects)
		if e != nil {
			return out, e
		}
		for claims.Next() {
		}
		e = claims.Err()
		claims.Close()
		if e != nil {
			return out, e
		}
		suffix = " FOR SHARE OF m,c"
	}
	rows, e := tx.Query(ctx, `SELECT c.external_id,c.id::text,c.owner_user_id::text,c.version FROM openfox.integration_connections c JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id JOIN openfox.integration_azure_identity_claims q ON q.organization_id=c.organization_id AND q.account_id=c.azure_identity_account AND q.subject_id=c.azure_identity_subject AND q.owner_user_id=c.owner_user_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.provider='azure_devops' AND c.scope='PERSONAL' AND c.status='ACTIVE' AND c.parent_connection_id=$1::uuid AND c.settings->>'auth_method'='azure_devops_identity' AND c.settings->>'account_id'=$2 AND c.settings->>'parent_version'=$3 AND c.external_id=ANY($4::text[]) AND m.status='ACTIVE' AND m.role<>'GUEST' ORDER BY c.id`+suffix, origin.Connection, a.AccountID, strconv.FormatUint(a.ConnectionVersion, 10), subjects)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b VerifiedBinding
		if e = rows.Scan(&id, &b.ID, &b.UserID, &b.Version); e != nil {
			return out, e
		}
		out[a.AccountID+"/"+id] = b
	}
	return out, rows.Err()
}
