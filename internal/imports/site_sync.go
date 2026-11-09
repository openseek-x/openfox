package imports

import (
	"context"
	"encoding/json"
	"strings"

	"openfox/internal/imports/jiraapi"
)

func (j *syncJob) remoteItems(ctx context.Context) ([]pendingItem, error) {
	if j.jira != nil {
		api := j.job.Preview.API
		ids := api.FieldIDs
		fields := jiraapi.FieldIDs{Sprint: ids.Sprint, Points: ids.Points, Start: ids.Start, EpicLink: ids.EpicLink}
		var cursor jiraapi.Cursor
		var items []pendingItem
		size := 0
		seen := map[string]bool{}
		for pageNo := 0; pageNo <= MaxDataRows; pageNo++ {
			page, e := j.jira.Search(ctx, ProjectJQL(api.ProjectKey, api.JQL), cursor, fields.Ids(), 0)
			if e != nil {
				return nil, e
			}
			for _, issue := range page.Issues {
				if issue.ID == "" || seen[issue.ID] || !strings.HasPrefix(issue.Key, api.ProjectKey+"-") {
					return nil, ErrInvalidRequest
				}
				seen[issue.ID] = true
				rec := BuildRecord(issue, ids, api.SiteURL)
				raw, e := json.Marshal(rec)
				if e != nil {
					return nil, e
				}
				size += len(raw)
				if size > MaxFileBytes || len(items) >= MaxDataRows {
					return nil, ErrInvalidRequest
				}
				items = append(items, pendingItem{Record: &rec, ExternalID: rec.ID})
			}
			if page.Done {
				return items, nil
			}
			if page.Next == cursor || len(page.Issues) == 0 {
				return nil, ErrInvalidRequest
			}
			cursor = page.Next
		}
		return nil, ErrInvalidRequest
	}
	var rows [][]string
	var err error
	if j.zen != nil {
		rows, err = j.zen.Rows(ctx, j.project)
	} else {
		rows, err = j.src.ProjectRows(ctx, j.project)
	}
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || strings.Join(rows[0], "\x00") != strings.Join(j.plan.headers, "\x00") {
		return nil, ErrInvalidRequest
	}
	items := make([]pendingItem, 0, len(rows)-1)
	for _, cells := range rows[1:] {
		items = append(items, pendingItem{Cells: cells})
	}
	return items, nil
}
func (j *syncJob) writeText(ctx context.Context, d rowData, title, desc *string) error {
	if j.jira != nil {
		return j.jira.UpdateText(ctx, d.SourceID, title, desc)
	}
	if j.zen != nil {
		return j.zen.UpdateText(ctx, j.project, d.SourceID, title, desc)
	}
	return j.src.UpdateProjectText(ctx, j.project, d.SourceID, d.TypeName, title, desc)
}

func (j *syncJob) writeStatus(ctx context.Context, d rowData, status string) error {
	if j.jira != nil {
		return j.jira.UpdateStatus(ctx, d.SourceID, status)
	}
	if j.zen != nil {
		return j.zen.UpdateStatus(ctx, d.SourceID, status)
	}
	return j.src.UpdateProjectStatus(ctx, j.project, d.SourceID, d.TypeName, status)
}
