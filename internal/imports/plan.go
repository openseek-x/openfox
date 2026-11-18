package imports

import (
	"encoding/json"
	"openfox/internal/integrations/yunxiao"
	"slices"
	"strings"
	"time"

	"openfox/internal/imports/jiraapi"
)

// colPerson 是人员列：name 为姓名列，id 为同名「X Id」列（可为 -1，此时用姓名作标识）。
type colPerson struct{ name, id int }

// plan 把列映射整理成「字段 → 列序号」，供分析与 worker 共用。
type plan struct {
	zoho                                                                   bool
	azure                                                                  bool
	headers                                                                []string
	yunxiaoMeta                                                            int
	priorityFields                                                         map[string]string
	priorities                                                             map[string]string
	jiraFile                                                               bool
	apiProjects                                                            bool
	project, statusCategory                                                int
	custom                                                                 []ColumnMapping
	title, sourceID, sourceKey, typ, status, priority, description, parent int
	created, updated, start, due, points                                   int
	assignee, reporter, creator                                            colPerson
	labels, comments                                                       []int
	duplicates                                                             []string // 被映射到多列的单值字段
	unsupported                                                            []string // 本阶段不导入、但被映射的字段
}

// unsupportedFields 是契约里「本阶段不导入」的字段。
var unsupportedFields = map[string]bool{
	FieldRelations: true, FieldAttachment: true, FieldSprint: true, FieldResolution: true,
	FieldWatchers: true, FieldOriginalEstimate: true,
}

func buildPlan(source string, headers []string, columns []ColumnMapping) plan {
	p := plan{yunxiaoMeta: -1, jiraFile: source == SourceJiraCSV, project: -1, statusCategory: -1, headers: headers, title: -1, sourceID: -1, sourceKey: -1, typ: -1, status: -1, priority: -1,
		description: -1, parent: -1, created: -1, updated: -1, start: -1, due: -1, points: -1,
		assignee: colPerson{-1, -1}, reporter: colPerson{-1, -1}, creator: colPerson{-1, -1}}
	if source == "yunxiao_api" {
		p.yunxiaoMeta = slices.Index(headers, yunxiao.MetadataColumn)
	}
	p.apiProjects = source == SourceJiraCloud || source == SourceJiraServer
	p.azure = source == SourceAzure
	p.zoho = source == SourceZoho
	if source == "teambition_api" || source == "shortcut_api" || source == "pingcode_api" {
		p.statusCategory = slices.Index(headers, "Status category")
	}
	single := map[string]*int{
		FieldProject: &p.project, FieldTitle: &p.title, FieldSourceID: &p.sourceID, FieldSourceKey: &p.sourceKey, FieldType: &p.typ,
		FieldStatus: &p.status, FieldPriority: &p.priority, FieldDescription: &p.description, FieldParent: &p.parent,
		FieldCreatedAt: &p.created, FieldUpdatedAt: &p.updated, FieldStartDate: &p.start, FieldDueDate: &p.due,
		FieldStoryPoints: &p.points, FieldAssignee: &p.assignee.name, FieldReporter: &p.reporter.name, FieldCreator: &p.creator.name,
	}
	seenUnsupported := map[string]bool{}
	dupSeen := map[string]bool{}
	for _, c := range columns {
		if c.Index < 0 || c.Index >= len(headers) {
			continue
		}
		switch {
		case c.Field == FieldCustom:
			c.CustomName = strings.TrimSpace(c.CustomName)
			if c.CustomName != "" {
				p.custom = append(p.custom, c)
			}
		case c.Field == FieldLabels:
			p.labels = append(p.labels, c.Index)
		case c.Field == FieldComment:
			p.comments = append(p.comments, c.Index)
		case unsupportedFields[c.Field]:
			if !seenUnsupported[c.Field] {
				seenUnsupported[c.Field] = true
				p.unsupported = append(p.unsupported, c.Field)
			}
		default:
			target, ok := single[c.Field]
			if !ok {
				continue // ignore / source_url 等
			}
			if *target >= 0 {
				if !dupSeen[c.Field] {
					dupSeen[c.Field] = true
					p.duplicates = append(p.duplicates, c.Field)
				}
				continue
			}
			*target = c.Index
		}
	}
	p.assignee.id = p.idColumn(p.assignee.name)
	p.reporter.id = p.idColumn(p.reporter.name)
	p.creator.id = p.idColumn(p.creator.name)
	return p
}

// idColumn 找名为「<人员列表头> Id」的列。
func (p plan) idColumn(nameCol int) int {
	if nameCol < 0 {
		return -1
	}
	want := NormalizeHeader(p.headers[nameCol]) + " id"
	for i, h := range p.headers {
		if NormalizeHeader(h) == want {
			return i
		}
	}
	return -1
}

type person struct{ ID, Name, Email string }

// rowData 是按计划从一行单元格提取的值（均已 TrimSpace）。
type rowData struct {
	Zoho                                                                          *ZohoRecordMetadata
	Azure                                                                         *AzureRecordMetadata
	CompletedAt                                                                   *time.Time
	Yunxiao                                                                       *yunxiao.RowMetadata
	PriorityUnmapped                                                              bool
	Title, SourceID, SourceKey, TypeName, Status, Priority, Description, ParentID string
	Created, Updated, Start, Due, Points                                          string
	Assignee, Reporter, Creator                                                   person
	LabelCells, CommentCells                                                      []string

	// 以下只有 API 来源有值。
	StatusCategory string           // new / indeterminate / done
	SourceURL      string           // 来源链接
	Sprints        []jiraapi.Sprint // 所属 Sprint
	People         []person         // 出现过的人员（含评论作者），带邮箱
}

func cell(cells []string, i int) string {
	if i < 0 || i >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[i])
}

func (p plan) person(cells []string, c colPerson) person {
	name := cell(cells, c.name)
	id := cell(cells, c.id)
	if id == "" {
		id = name
	}
	if name == "" {
		name = id
	}
	return person{ID: id, Name: name}
}

func (p plan) row(cells []string) rowData {
	d := rowData{
		Title: cell(cells, p.title), SourceID: cell(cells, p.sourceID), SourceKey: cell(cells, p.sourceKey),
		StatusCategory: cell(cells, p.statusCategory), TypeName: cell(cells, p.typ), Status: cell(cells, p.status), Priority: cell(cells, p.priority),
		Description: strings.TrimSpace(strings.TrimRight(cellRaw(cells, p.description), " \t")), ParentID: cell(cells, p.parent),
		Created: cell(cells, p.created), Updated: cell(cells, p.updated), Start: cell(cells, p.start), Due: cell(cells, p.due),
		Points:   cell(cells, p.points),
		Assignee: p.person(cells, p.assignee), Reporter: p.person(cells, p.reporter), Creator: p.person(cells, p.creator),
	}
	if p.yunxiaoMeta >= 0 {
		var meta yunxiao.RowMetadata
		if json.Unmarshal([]byte(cellRaw(cells, p.yunxiaoMeta)), &meta) == nil {
			d.Yunxiao = &meta
			// Immutable source columns override all editable column mappings.
			d.SourceID = cell(cells, 0)
			d.Title = cell(cells, 1)
			d.Description = cellRaw(cells, 2)
			d.Status = yunxiao.StatusSource(meta.TypeID, meta.StatusID)
			d.TypeName = yunxiao.TypeSource(meta.TypeID)
			d.Assignee = person{Name: cell(cells, 4), ID: cell(cells, 7)}
			d.ParentID = cell(cells, 6)
			d.SourceKey = cell(cells, 9)
			d.Priority = "NONE"
			for _, value := range meta.Priorities {
				if p.priorityFields[d.TypeName] == value.FieldID {
					var ok bool
					d.Priority, ok = p.priorities[value.Source]
					d.PriorityUnmapped = !ok
					break
				}
			}
		}
	}
	for _, i := range p.labels {
		if v := cell(cells, i); v != "" {
			d.LabelCells = append(d.LabelCells, v)
		}
	}
	for _, i := range p.comments {
		if v := cell(cells, i); v != "" {
			d.CommentCells = append(d.CommentCells, v)
		}
	}
	return d
}

func cellRaw(cells []string, i int) string {
	if i < 0 || i >= len(cells) {
		return ""
	}
	return cells[i]
}

// rowInfo 是一行的分类结果。
type rowInfo struct {
	Ordinal  int
	Data     rowData
	Invalid  bool
	ErrMsg   string
	Epic     bool
	Filtered bool
	Done     bool
}

// classify 对全部行做有效性（标题 / 源 ID / 重复）与选项过滤的分类；源 ID 重复时第二次及以后出现的算无效。
func classify(p plan, rows []rawItem, opts Options) []rowInfo {
	out := make([]rowInfo, len(rows))
	seen := map[string]bool{}
	for i, r := range rows {
		var d rowData
		if r.Record != nil {
			d = p.record(r.Record)
		} else {
			d = p.row(r.Cells)
		}
		info := rowInfo{Ordinal: r.Ordinal, Data: d}
		switch {
		case p.azure && d.Azure == nil:
			info.Invalid, info.ErrMsg = true, "Azure Boards 来源元数据无效"
		case p.yunxiaoMeta >= 0 && d.Yunxiao == nil:
			info.Invalid, info.ErrMsg = true, "云效来源元数据无效"
		case d.Title == "":
			info.Invalid, info.ErrMsg = true, "缺少标题"
		case d.SourceID == "":
			info.Invalid, info.ErrMsg = true, "缺少源 ID"
		case len(d.SourceID) > 255:
			info.Invalid, info.ErrMsg = true, "源 ID 过长"
		case seen[d.SourceID]:
			info.Invalid, info.ErrMsg = true, "源 ID 在文件内重复"
		}
		if !info.Invalid {
			seen[d.SourceID] = true
			info.Done = rowDone(d)
			info.Epic = (p.jiraFile || p.apiProjects) && opts.FileProjects != nil && strings.EqualFold(d.TypeName, "Epic")
			info.Filtered = (info.Done && !opts.ImportDone) || (!info.Done && !opts.ImportOpen)
			if info.Epic {
				info.Filtered = !*opts.FileProjects
				if !info.Filtered && len([]rune(d.Title)) > 80 {
					info.Invalid = true
					info.ErrMsg = "专项名称超过 80 字"
				}
			}
		}
		out[i] = info
	}
	return out
}

// rowDone 判断一行是否已结束：API 来源以状态类别为准（done），取消类状态名也算结束；没有类别时按状态名推断。
func rowDone(d rowData) bool {
	if d.Yunxiao != nil {
		return StatusIsDone(d.Yunxiao.StatusLabel)
	}
	if d.StatusCategory != "" {
		return d.StatusCategory == "done" || InferStatusCategory(d.Status) == "CANCELLED"
	}
	return StatusIsDone(d.Status)
}

// rawItem 是从 import_items 读出的原始行。
type rawItem struct {
	Ordinal int
	Line    int
	Cells   []string
	// Record 非空表示 API 来源，此时优先于 Cells。
	Record *Record
}

func configuredPlan(source string, headers []string, cfg Config) plan {
	p := buildPlan(source, headers, cfg.Columns)
	if source == "yunxiao_api" || source == SourceAzure || source == SourceZoho {
		p.priorityFields = map[string]string{}
		p.priorities = map[string]string{}
		for _, f := range cfg.PriorityFields {
			p.priorityFields[f.TypeSource] = f.FieldID
		}
		for _, v := range cfg.Priorities {
			p.priorities[v.Source] = v.Priority
		}
	}
	return p
}

func (p plan) record(r *Record) rowData {
	d := rowFromRecord(r)
	if p.zoho {
		d.Zoho = r.Zoho
		d.Priority = "NONE"
		if r.Priority != "" {
			var ok bool
			d.Priority, ok = p.priorities[r.Priority]
			d.PriorityUnmapped = !ok
		}
	}
	if p.azure {
		d.Azure = r.Azure
		d.Priority = "NONE"
		if r.Azure != nil && r.Azure.PriorityValue != "" {
			var ok bool
			d.Priority, ok = p.priorities[azurePrioritySource(r.Azure.PriorityField, r.Azure.PriorityValue)]
			d.PriorityUnmapped = !ok
		}
	}
	return d
}
