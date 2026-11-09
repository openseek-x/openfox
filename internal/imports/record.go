package imports

import (
	"encoding/json"
	"strings"

	"openfox/internal/imports/jiraapi"
)

// Person 是 API 来源的人员。ID 在 cloud 为 accountId，在 server 为 name 或 key。
type Person struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// Record 是 API 来源的一条任务（保存在 import_items.raw 的 record 键下）。日期保持 Jira 的 ISO 原样。
type Record struct {
	ID             string           `json:"id"`
	Key            string           `json:"key"`
	Title          string           `json:"title"`
	TypeName       string           `json:"type,omitempty"`
	Status         string           `json:"status,omitempty"`
	StatusCategory string           `json:"status_category,omitempty"` // new / indeterminate / done
	Priority       string           `json:"priority,omitempty"`
	Description    string           `json:"description,omitempty"`
	ParentID       string           `json:"parent_id,omitempty"`
	Created        string           `json:"created,omitempty"`
	Updated        string           `json:"updated,omitempty"`
	Due            string           `json:"due,omitempty"`
	Start          string           `json:"start,omitempty"`
	Points         string           `json:"points,omitempty"`
	Assignee       Person           `json:"assignee"`
	Reporter       Person           `json:"reporter"`
	Creator        Person           `json:"creator"`
	Labels         string           `json:"labels,omitempty"` // 空格连接
	Comments       []string         `json:"comments,omitempty"`
	CommentIDs     []string         `json:"comment_ids,omitempty"`
	People         []Person         `json:"people,omitempty"` // 评论作者
	Sprints        []jiraapi.Sprint `json:"sprints,omitempty"`
	URL            string           `json:"url,omitempty"`

	// CommentTotal 是来源报告的评论总数（用于判断是否需要补拉），不参与后续流程。
	CommentTotal int `json:"comment_total,omitempty"`
}

const (
	maxStoredDescriptionRunes = 50000
	maxStoredCommentRunes     = 20000
)

func personOf(raw json.RawMessage) Person {
	var u jiraapi.User
	if len(raw) == 0 || json.Unmarshal(raw, &u) != nil {
		return Person{}
	}
	return Person{ID: u.Identity(), Name: u.DisplayName, Email: u.EmailAddress}
}

func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func nameOf(raw json.RawMessage) string {
	var v struct {
		Name string `json:"name"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.Name
}

// commentCell 把评论写成「created;作者标识;body」。
func commentCell(c jiraapi.Comment) (string, Person) {
	author := personOf(mustRaw(c.Author))
	body, _ := TruncateRunes(c.Body, maxStoredCommentRunes)
	return c.Created + ";" + author.ID + ";" + body, author
}

func mustRaw(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// BuildRecord 把 Jira 的 issue 转成 Record。siteURL 用来生成 SourceURL。
func BuildRecord(issue jiraapi.Issue, ids FieldIDs, siteURL string) Record {
	f := issue.Fields
	rec := Record{ID: issue.ID, Key: issue.Key, Title: rawString(f["summary"]), TypeName: nameOf(f["issuetype"]),
		Priority: nameOf(f["priority"]), Created: rawString(f["created"]), Updated: rawString(f["updated"]), Due: rawString(f["duedate"]),
		Assignee: personOf(f["assignee"]), Reporter: personOf(f["reporter"]), Creator: personOf(f["creator"]),
		URL: strings.TrimRight(siteURL, "/") + "/browse/" + issue.Key}
	var status struct {
		Name     string `json:"name"`
		Category struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	}
	if raw := f["status"]; len(raw) > 0 && json.Unmarshal(raw, &status) == nil {
		rec.Status, rec.StatusCategory = status.Name, status.Category.Key
	}
	desc := rawString(f["description"])
	rec.Description, _ = TruncateRunes(desc, maxStoredDescriptionRunes)
	var parent struct {
		ID string `json:"id"`
	}
	if raw := f["parent"]; len(raw) > 0 && json.Unmarshal(raw, &parent) == nil {
		rec.ParentID = parent.ID
	}
	if rec.ParentID == "" && ids.EpicLink != "" {
		rec.ParentID = rawString(f[ids.EpicLink])
	}
	var labels []string
	if raw := f["labels"]; len(raw) > 0 && json.Unmarshal(raw, &labels) == nil {
		rec.Labels = strings.Join(labels, " ")
	}
	if ids.Points != "" {
		var n json.Number
		if raw := f[ids.Points]; len(raw) > 0 && json.Unmarshal(raw, &n) == nil {
			rec.Points = n.String()
		}
	}
	if ids.Start != "" {
		rec.Start = rawString(f[ids.Start])
	}
	if ids.Sprint != "" {
		rec.Sprints = jiraapi.ParseSprints(f[ids.Sprint])
	}
	var comment struct {
		Comments []jiraapi.Comment `json:"comments"`
		Total    int               `json:"total"`
	}
	if raw := f["comment"]; len(raw) > 0 && json.Unmarshal(raw, &comment) == nil {
		rec.CommentTotal = comment.Total
		rec.AddComments(comment.Comments)
	}
	return rec
}

// AddComments 追加评论（单个任务最多 maxCommentsPerTask 条），同时记录评论作者。
func (r *Record) AddComments(comments []jiraapi.Comment) {
	for _, c := range comments {
		if len(r.Comments) >= maxCommentsPerTask {
			return
		}
		cell, author := commentCell(c)
		r.Comments = append(r.Comments, cell)
		r.CommentIDs = append(r.CommentIDs, c.ID)
		if author.ID != "" {
			r.People = append(r.People, author)
		}
	}
}

// rowFromRecord 把 Record 转成 rowData（与 CSV 的 plan.row 对应）。
func rowFromRecord(r *Record) rowData {
	d := rowData{
		Title: strings.TrimSpace(r.Title), SourceID: r.ID, SourceKey: r.Key, TypeName: strings.TrimSpace(r.TypeName),
		Status: strings.TrimSpace(r.Status), StatusCategory: strings.ToLower(r.StatusCategory), Priority: strings.TrimSpace(r.Priority),
		Description: strings.TrimSpace(r.Description), ParentID: r.ParentID,
		Created: r.Created, Updated: r.Updated, Start: r.Start, Due: r.Due, Points: r.Points,
		Assignee: toPerson(r.Assignee), Reporter: toPerson(r.Reporter), Creator: toPerson(r.Creator),
		CommentCells: r.Comments, Sprints: r.Sprints, SourceURL: r.URL,
	}
	if r.Labels != "" {
		d.LabelCells = []string{r.Labels}
	}
	for _, p := range append([]Person{r.Assignee, r.Reporter, r.Creator}, r.People...) {
		if p.ID != "" {
			d.People = append(d.People, toPerson(p))
		}
	}
	return d
}

func toPerson(p Person) person {
	name := p.Name
	if name == "" {
		name = p.ID
	}
	return person{ID: p.ID, Name: name, Email: p.Email}
}
