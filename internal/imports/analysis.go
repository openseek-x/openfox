package imports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/imports/jiraapi"
)

// Analysis 对应契约 ImportAnalysis。
type Analysis struct {
	Statuses  []AnalysisStatus  `json:"statuses"`
	Users     []AnalysisUser    `json:"users"`
	Types     []AnalysisType    `json:"types"`
	Counts    AnalysisCounts    `json:"counts"`
	Overwrite AnalysisOverwrite `json:"overwrite"`
	Members   AnalysisMembers   `json:"members"`
	Issues    []Issue           `json:"issues"`
	// Capabilities 说明可开启的导入选项（6c-2）。
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities 对应契约 ImportAnalysis.capabilities。
type Capabilities struct {
	ProjectSync bool `json:"project_sync,omitempty"`
	Sprints     bool `json:"sprints"`
	KeepNumbers bool `json:"keep_numbers"`
	SprintCount int  `json:"sprint_count"`
}

type AnalysisStatus struct {
	Source            string  `json:"source"`
	Count             int     `json:"count"`
	Done              bool    `json:"done"`
	SuggestedStatusID *string `json:"suggested_status_id"`
}

type AnalysisUser struct {
	SourceID        string  `json:"source_id"`
	Name            string  `json:"name"`
	Count           int     `json:"count"`
	SuggestedUserID *string `json:"suggested_user_id"`
	// Email 是来源平台上的邮箱（只有 API 来源有，没有时为空串）。
	Email string `json:"email"`
	// SuggestReason 是 suggested_user_id 的依据：空串（没有建议）/ email / name。
	SuggestReason string `json:"suggest_reason"`
}

type AnalysisType struct {
	Source string  `json:"source"`
	Count  int     `json:"count"`
	TypeID *string `json:"type_id"`
}

type AnalysisCounts struct {
	Projects int `json:"projects,omitempty"`
	Total    int `json:"total"`
	Open     int `json:"open"`
	Done     int `json:"done"`
	Invalid  int `json:"invalid"`
}

type AnalysisOverwrite struct {
	Existing int `json:"existing"`
	Modified int `json:"modified"`
}

type AnalysisMembers struct {
	Member   int `json:"member"`
	Invite   int `json:"invite"`
	External int `json:"external"`
	Skip     int `json:"skip"`
}

// Issue 是开始导入前必须解决的问题。
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Value   string `json:"value,omitempty"`
}

const (
	IssueTitleMissing    = "title_column_missing"
	IssueSourceIDMissing = "source_id_column_missing"
	IssueDuplicateColumn = "duplicate_column_field"
	IssueStatusUnmapped  = "status_unmapped"
	IssueNothingToImport = "nothing_to_import"
)

type memberRef struct{ ID, FullName, Email string }

// lookups 是分析与执行共用的空间 / 组织查找表。
type lookups struct {
	instance     string
	statusByName map[string]string // 小写名称 → 状态 ID（未归档）
	typeByName   map[string]string // 小写名称 → 任务类型 ID（空间内启用且未归档）
	members      []memberRef
	// jiraVerified：小写 Jira 账号标识 → 已在「第三方账号绑定」页验证过该身份的成员 ID（同站点，仅 API 导入）。
	jiraVerified   map[string]string
	priorityLevels int
	estimation     string
	location       *time.Location
	// 目标空间的迭代设置（spaceID 为空时都是零值）。
	iterationsEnabled bool
	iterationMode     string
}

func loadLookups(ctx context.Context, tx pgx.Tx, spaceID string) (lookups, error) {
	l := lookups{statusByName: map[string]string{}, typeByName: map[string]string{}}
	tz := "Asia/Shanghai"
	if spaceID == "" {
		// 选择「新建空间」的草稿还没有空间：时区取默认，任务类型都匹配不上（用默认类型）。
		if err := tx.QueryRow(ctx, `SELECT priority_levels FROM openfox.organizations
			 WHERE id = openfox_private.current_organization_id()`).Scan(&l.priorityLevels); err != nil {
			return l, err
		}
	} else if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(s.iteration_timezone, ''), 'Asia/Shanghai'), s.estimation_scale, o.priority_levels,
		       s.iterations_enabled, s.iteration_mode
		  FROM openfox.spaces s JOIN openfox.organizations o ON o.id = s.organization_id
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $1::uuid`,
		spaceID).Scan(&tz, &l.estimation, &l.priorityLevels, &l.iterationsEnabled, &l.iterationMode); err != nil {
		return l, err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	l.location = loc
	rows, err := tx.Query(ctx, `
		SELECT id::text, lower(name) FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND archived_at IS NULL`)
	if err != nil {
		return l, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return l, err
		}
		l.statusByName[name] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return l, err
	}
	if spaceID == "" {
		return l.loadMembers(ctx, tx)
	}
	rows, err = tx.Query(ctx, `
		SELECT id::text, lower(name) FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND enabled AND archived_at IS NULL ORDER BY is_default DESC, created_at, id`, spaceID)
	if err != nil {
		return l, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return l, err
		}
		if _, ok := l.typeByName[name]; !ok {
			l.typeByName[name] = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return l, err
	}
	return l.loadMembers(ctx, tx)
}

func (l lookups) loadMembers(ctx context.Context, tx pgx.Tx) (lookups, error) {
	rows, err := tx.Query(ctx, `
		SELECT m.user_id::text, COALESCE(u.full_name, ''), u.email
		  FROM openfox.organization_members m JOIN openfox.users u ON u.id = m.user_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.status = 'ACTIVE'
		 ORDER BY m.created_at, m.user_id`)
	if err != nil {
		return l, err
	}
	for rows.Next() {
		var m memberRef
		if err := rows.Scan(&m.ID, &m.FullName, &m.Email); err != nil {
			rows.Close()
			return l, err
		}
		l.members = append(l.members, m)
	}
	rows.Close()
	return l, rows.Err()
}

// suggestMember 先按成员已验证的 Jira 身份（reason verified）匹配，再按邮箱（不区分大小写相等），
// 最后按姓名或邮箱 @ 前的部分匹配；reason 为 verified、email 或 name。
func (l lookups) suggestMember(sourceID, name, email string) (id *string, reason string) {
	if uid, ok := l.jiraVerified[strings.ToLower(strings.TrimSpace(sourceID))]; ok && uid != "" {
		return &uid, "verified"
	}
	if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
		for _, m := range l.members {
			if strings.ToLower(m.Email) == e {
				id := m.ID
				return &id, "email"
			}
		}
	}
	for _, key := range []string{name, sourceID} {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "" {
			continue
		}
		for _, m := range l.members {
			local := strings.ToLower(m.Email)
			if i := strings.Index(local, "@"); i >= 0 {
				local = local[:i]
			}
			if (m.FullName != "" && strings.ToLower(m.FullName) == k) || local == k {
				id := m.ID
				return &id, "name"
			}
		}
	}
	return nil, ""
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// collectUsers 汇总来源用户：处理人 / 报告人 / 创建者（含标识列）与评论作者；返回按首次出现排序的列表。
func collectUsers(infos []rowInfo, loc *time.Location) []AnalysisUser {
	var users []AnalysisUser
	index := map[string]int{}
	add := func(id, name, email string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		i, ok := index[id]
		if !ok {
			index[id] = len(users)
			users = append(users, AnalysisUser{SourceID: id, Name: name, Count: 1, Email: email})
			return
		}
		users[i].Count++
		if users[i].Name == users[i].SourceID && name != "" {
			users[i].Name = name
		}
		if users[i].Email == "" {
			users[i].Email = email
		}
	}
	for _, info := range infos {
		if info.Invalid {
			continue
		}
		d := info.Data
		// API 来源的评论作者标识只出现在评论单元格里，姓名与邮箱从 People 里查。
		known := map[string]person{}
		for _, p := range d.People {
			known[p.ID] = p
		}
		add(d.Assignee.ID, d.Assignee.Name, d.Assignee.Email)
		add(d.Reporter.ID, d.Reporter.Name, d.Reporter.Email)
		add(d.Creator.ID, d.Creator.Name, d.Creator.Email)
		for _, c := range d.CommentCells {
			pc := ParseCommentCell(c, loc)
			if pc.HasAt && pc.Author != "" {
				p, ok := known[pc.Author]
				if !ok {
					p = person{ID: pc.Author, Name: pc.Author}
				}
				add(p.ID, p.Name, p.Email)
			}
		}
	}
	return users
}

// datedSprints 返回去重后带起止日期的 Sprint（ID → Sprint 与起止时间）。
type datedSprint struct {
	jiraapi.Sprint
	Starts, Ends time.Time
}

func collectSprints(infos []rowInfo, loc *time.Location) []datedSprint {
	var out []datedSprint
	seen := map[string]bool{}
	for _, info := range infos {
		if info.Invalid || info.Filtered {
			continue
		}
		for _, sp := range info.Data.Sprints {
			if sp.ID == "" || seen[sp.ID] {
				continue
			}
			starts, ok1 := ParseDate(sp.Start, loc)
			ends, ok2 := ParseDate(sp.End, loc)
			if !ok1 || !ok2 || !ends.After(starts) {
				continue
			}
			seen[sp.ID] = true
			out = append(out, datedSprint{Sprint: sp, Starts: starts, Ends: ends})
		}
	}
	return out
}

// analyze 按已保存的列映射扫描全部行，产出契约 ImportAnalysis。
func analyze(ctx context.Context, tx pgx.Tx, spaceID string, cfg Config, infos []rowInfo, l lookups, api bool, provider string) (Analysis, error) {
	a := Analysis{Statuses: []AnalysisStatus{}, Users: []AnalysisUser{}, Types: []AnalysisType{}, Issues: []Issue{}}
	a.Counts.Total = len(infos)
	// 状态：按名称分组（不区分大小写，保留首次写法）；importable 记录过滤后真正会导入的条数。
	statusIdx := map[string]int{}
	importable := map[string]int{}
	typeIdx := map[string]int{}
	validIDs := make([]string, 0, len(infos))
	toImport := 0
	for _, info := range infos {
		if info.Invalid {
			a.Counts.Invalid++
			continue
		}
		if info.Epic {
			a.Counts.Projects++
			if !info.Filtered {
				toImport++
			}
			continue
		}
		validIDs = append(validIDs, info.Data.SourceID)
		if info.Done {
			a.Counts.Done++
		} else {
			a.Counts.Open++
		}
		if !info.Filtered {
			toImport++
		}
		if s := info.Data.Status; s != "" {
			k := strings.ToLower(s)
			i, ok := statusIdx[k]
			if !ok {
				i = len(a.Statuses)
				statusIdx[k] = i
				a.Statuses = append(a.Statuses, AnalysisStatus{Source: s, Done: info.Done, SuggestedStatusID: strPtr(l.statusByName[k])})
			}
			a.Statuses[i].Count++
			if !info.Filtered {
				importable[k]++
			}
		}
		if t := info.Data.TypeName; t != "" {
			k := strings.ToLower(t)
			i, ok := typeIdx[k]
			if !ok {
				i = len(a.Types)
				typeIdx[k] = i
				a.Types = append(a.Types, AnalysisType{Source: t, TypeID: strPtr(l.typeByName[k])})
			}
			a.Types[i].Count++
		}
	}
	a.Users = collectUsers(infos, l.location)
	if a.Users == nil {
		a.Users = []AnalysisUser{}
	}
	userModes := map[string]UserMapping{}
	for _, u := range cfg.Users {
		userModes[u.SourceID] = u
	}
	for i := range a.Users {
		a.Users[i].SuggestedUserID, a.Users[i].SuggestReason = l.suggestMember(a.Users[i].SourceID, a.Users[i].Name, a.Users[i].Email)
		switch userModes[a.Users[i].SourceID].Mode {
		case "member":
			a.Members.Member++
		case "invite":
			a.Members.Invite++
		case "skip":
			a.Members.Skip++
		default:
			a.Members.External++
		}
	}
	// 覆盖导入：已有映射且任务未删除；modified 为任务当前版本大于最近一次导入写入时的版本。
	if len(validIDs) > 0 && spaceID != "" {
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE t.version > COALESCE(li.task_version, t.version - 1))
			  FROM (SELECT DISTINCT ON (external_id) organization_id,space_id,task_id FROM openfox.import_mappings
                    WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND source_system=$2
                      AND (lower(source_instance)=$4 OR (NOT $5 AND source_instance='')) AND released_at IS NULL AND external_id=ANY($3::text[])
                    ORDER BY external_id,(lower(source_instance)=$4) DESC) m
			  JOIN openfox.tasks t ON t.organization_id = m.organization_id AND t.id = m.task_id AND t.deleted_at IS NULL
			  LEFT JOIN LATERAL (
			       SELECT i.task_version FROM openfox.import_items i
			        WHERE i.organization_id = m.organization_id AND i.task_id = m.task_id AND i.status IN ('CREATED', 'UPDATED')
			        ORDER BY i.processed_at DESC LIMIT 1) li ON true
			 WHERE t.space_id=m.space_id`, spaceID, provider, validIDs, l.instance, cfg.Options.ContinuousSync).Scan(&a.Overwrite.Existing, &a.Overwrite.Modified); err != nil {
			return a, err
		}
	}
	// 问题。
	fieldCount := map[string]int{}
	for _, c := range cfg.Columns {
		fieldCount[c.Field]++
	}
	// API 来源没有列映射，跳过与列有关的检查。
	if !api {
		if fieldCount[FieldTitle] == 0 {
			a.Issues = append(a.Issues, Issue{Code: IssueTitleMissing, Message: "没有映射标题列"})
		}
		if fieldCount[FieldSourceID] == 0 {
			a.Issues = append(a.Issues, Issue{Code: IssueSourceIDMissing, Message: "没有映射源 ID 列（Issue id）"})
		}
		for _, field := range duplicateFields(cfg.Columns) {
			a.Issues = append(a.Issues, Issue{Code: IssueDuplicateColumn, Value: field, Message: fmt.Sprintf("字段 %s 被映射到了多列", field)})
		}
	}
	mapped := map[string]StatusMapping{}
	for _, s := range cfg.Statuses {
		mapped[strings.ToLower(s.Source)] = s
	}
	for _, s := range a.Statuses {
		k := strings.ToLower(s.Source)
		if importable[k] == 0 {
			continue
		}
		if _, ok := mapped[k]; ok || s.SuggestedStatusID != nil {
			continue
		}
		a.Issues = append(a.Issues, Issue{Code: IssueStatusUnmapped, Value: s.Source, Message: fmt.Sprintf("来源状态「%s」还没有映射", s.Source)})
	}
	// 选项能力：保留票号只在新空间；迭代只在 API 来源且有带日期的 Sprint，并且目标是新空间或手动迭代空间。
	a.Capabilities.SprintCount = len(collectSprints(infos, l.location))
	a.Capabilities.KeepNumbers = spaceID == ""
	a.Capabilities.Sprints = api && a.Capabilities.SprintCount > 0 && (spaceID == "" || (l.iterationsEnabled && l.iterationMode == "manual"))
	if toImport == 0 {
		a.Issues = append(a.Issues, Issue{Code: IssueNothingToImport, Message: "按当前映射与选项没有可导入的条目"})
	}
	return a, nil
}

var singleValueFields = map[string]bool{
	FieldProject: true,
	FieldTitle:   true, FieldSourceID: true, FieldSourceKey: true, FieldType: true, FieldStatus: true, FieldPriority: true,
	FieldDescription: true, FieldParent: true, FieldCreatedAt: true, FieldUpdatedAt: true, FieldStartDate: true,
	FieldDueDate: true, FieldStoryPoints: true, FieldAssignee: true, FieldReporter: true, FieldCreator: true,
}

// duplicateFields 返回被映射到多列的单值字段（按首次出现顺序）。
func duplicateFields(columns []ColumnMapping) []string {
	count := map[string]int{}
	var out []string
	for _, c := range columns {
		if !singleValueFields[c.Field] {
			continue
		}
		count[c.Field]++
		if count[c.Field] == 2 {
			out = append(out, c.Field)
		}
	}
	return out
}
