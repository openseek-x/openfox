package integrations

import (
	"context"
	"encoding/json"

	"openfox/internal/integrations/yunxiao"
)

func (s ProjectSource) yunxiaoClient() *yunxiao.Client {
	if s.yunxiao != nil {
		return s.yunxiao
	}
	return defaultYunxiaoClient
}

// YunxiaoProjectRows also returns the observed excluded count for an empty snapshot.
func (s ProjectSource) YunxiaoProjectRows(ctx context.Context, project string) ([][]string, int, error) {
	items, err := s.yunxiaoClient().Workitems(ctx, s.auth.Secret, s.auth.CompanyID, project)
	if err != nil {
		return nil, 0, err
	}
	rows := [][]string{{"ID", "Summary", "Description", "Status", "Assignee", "Issue type", "Parent id", "Assignee Id", "Priority", "Issue key", yunxiao.MetadataColumn}}
	fields := map[string]map[string]yunxiao.Field{}
	archived := 0
	for _, item := range items {
		if item.LogicalStatus == "archived" {
			archived++
		}
	}
	size := 0
	for _, item := range items {
		if item.LogicalStatus == "archived" {
			continue
		}
		desc, err := yunxiao.Description(item.Description, item.FormatType)
		if err != nil {
			return nil, 0, err
		}
		meta := yunxiao.RowMetadata{TypeID: item.WorkitemType.ID, TypeLabel: item.WorkitemType.Name, StatusID: item.Status.ID, StatusLabel: item.Status.Name}
		// Only inspect field configuration when actual source values need an explicit
		// priority choice, and at most once for each type in this snapshot.
		if len(item.CustomFieldValues) > 0 {
			known, ok := fields[item.WorkitemType.ID]
			if !ok {
				fs, err := s.yunxiaoClient().Fields(ctx, s.auth.Secret, s.auth.CompanyID, project, item.WorkitemType.ID)
				if err != nil {
					return nil, 0, err
				}
				known = map[string]yunxiao.Field{}
				for _, f := range fs {
					known[f.ID] = f
				}
				fields[item.WorkitemType.ID] = known
			}
			seen := map[string]bool{}
			for _, v := range item.CustomFieldValues {
				if seen[v.FieldID] {
					return nil, 0, yunxiao.ErrProtocol
				}
				seen[v.FieldID] = true
				f, ok := known[v.FieldID]
				if !ok {
					continue
				}
				if len(v.Values) > 1 {
					return nil, 0, yunxiao.ErrProtocol
				}
				if len(v.Values) == 0 {
					continue
				}
				choice := v.Values[0]
				found := false
				for _, option := range f.Options {
					if option.ID == choice.Identifier {
						label := option.DisplayValue
						if label == "" {
							label = option.Value
						}
						meta.Priorities = append(meta.Priorities, yunxiao.PriorityValue{FieldID: f.ID, FieldLabel: f.Name, OptionID: option.ID, OptionLabel: label, Source: yunxiao.PrioritySource(item.WorkitemType.ID, f.ID, option.ID)})
						found = true
						break
					}
				}
				if !found {
					return nil, 0, yunxiao.ErrProtocol
				}
			}
		}
		raw, _ := json.Marshal(meta)
		assigneeID := ""
		if item.AssignedTo.ID != "" {
			assigneeID = yunxiao.UserIdentity(s.auth.CompanyID, item.AssignedTo.ID)
		}
		row := []string{item.ID, item.Subject, desc, yunxiao.StatusSource(item.WorkitemType.ID, item.Status.ID), item.AssignedTo.Name, yunxiao.TypeSource(item.WorkitemType.ID), item.ParentID, assigneeID, "", item.SerialNumber, string(raw)}
		for _, v := range row {
			size += len(v)
			if len(v) > 65536 || size > 10<<20 {
				return nil, 0, yunxiao.ErrLimit
			}
		}
		rows = append(rows, row)
	}
	return rows, archived, nil
}
