package yunxiao

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type FieldOption struct {
	ID           string `json:"id"`
	DisplayValue string `json:"displayValue"`
	Value        string `json:"value"`
}
type Field struct {
	Named
	Format  string        `json:"format"`
	Type    string        `json:"type"`
	Options []FieldOption `json:"options"`
}
type FieldValue struct {
	FieldID     string `json:"fieldId"`
	FieldFormat string `json:"fieldFormat"`
	FieldName   string `json:"fieldName"`
	Values      []struct {
		Identifier   string `json:"identifier"`
		DisplayValue string `json:"displayValue"`
	} `json:"values"`
}
type Workitem struct {
	ID                string       `json:"id"`
	Subject           string       `json:"subject"`
	Description       string       `json:"description"`
	FormatType        string       `json:"formatType"`
	CategoryID        string       `json:"categoryId"`
	LogicalStatus     string       `json:"logicalStatus"`
	ParentID          string       `json:"parentId"`
	SerialNumber      string       `json:"serialNumber"`
	Space             Named        `json:"space"`
	Status            Named        `json:"status"`
	WorkitemType      Named        `json:"workitemType"`
	AssignedTo        Named        `json:"assignedTo"`
	CustomFieldValues []FieldValue `json:"customFieldValues"`
}

// Fields returns only explicitly selectable single-choice fields. Native and
// custom IDs remain source IDs; neither labels nor option order imply priority.
func (c *Client) Fields(ctx context.Context, token, org, project, kind string) ([]Field, error) {
	if !ValidID(org) || !ValidID(project) || !ValidID(kind) {
		return nil, ErrInvalid
	}
	var rows []Field
	path := base(org) + "/projects/" + url.PathEscape(project) + "/workitemTypes/" + url.PathEscape(kind) + "/fields"
	if _, err := c.request(ctx, token, http.MethodGet, path, nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, ErrProtocol
	}
	if len(rows) > MaxItems {
		return nil, ErrLimit
	}
	out := make([]Field, 0)
	seen := map[string]bool{}
	for _, f := range rows {
		if !ValidID(f.ID) || seen[f.ID] || f.Name == "" {
			return nil, ErrProtocol
		}
		seen[f.ID] = true
		if f.Format != "list" {
			continue
		}
		if f.Type != "NativeField" && f.Type != "CustomField" {
			return nil, ErrProtocol
		}
		if len(f.Options) > MaxItems {
			return nil, ErrLimit
		}
		options := map[string]bool{}
		for _, opt := range f.Options {
			if !ValidID(opt.ID) || options[opt.ID] {
				return nil, ErrProtocol
			}
			options[opt.ID] = true
		}
		out = append(out, f)
	}
	return out, nil
}

// Workitems reads a complete bounded snapshot, using categories from the actual
// type catalog. It never turns a failed page into a partial import.
func (c *Client) Workitems(ctx context.Context, token, org, project string) ([]Workitem, error) {
	if !ValidID(org) || !ValidID(project) {
		return nil, ErrInvalid
	}
	all, err := c.Types(ctx, token, org, "", "")
	if err != nil {
		return nil, err
	}
	categorySet := map[string]bool{}
	for _, kind := range all {
		categorySet[kind.CategoryID] = true
	}
	categories := make([]string, 0, len(categorySet))
	for cat := range categorySet {
		categories = append(categories, cat)
	}
	sort.Strings(categories)
	if len(categories) == 0 {
		return []Workitem{}, nil
	}
	types := map[string]WorkitemType{}
	for _, cat := range categories {
		list, err := c.Types(ctx, token, org, project, cat)
		if err != nil {
			return nil, err
		}
		for _, kind := range list {
			if _, exists := types[kind.ID]; exists {
				return nil, ErrProtocol
			}
			types[kind.ID] = kind
		}
	}
	rows, err := pages[Workitem](ctx, c, token, base(org)+"/workitems:search", map[string]any{
		"category": strings.Join(categories, ","), "spaceId": project, "spaceType": "Project",
		"conditions": `{"conditionGroups":[]}`,
	}, func(w Workitem) string { return w.ID })
	if err != nil {
		return nil, err
	}
	for _, w := range rows {
		kind, exists := types[w.WorkitemType.ID]
		if w.Space.ID != project || !exists || w.CategoryID != kind.CategoryID || !ValidID(w.Status.ID) || w.Status.Name == "" ||
			w.Subject == "" || w.WorkitemType.Name == "" || (w.ParentID != "" && !ValidID(w.ParentID)) ||
			(w.AssignedTo.ID != "" && !ValidID(w.AssignedTo.ID)) || (w.LogicalStatus != "normal" && w.LogicalStatus != "archived") {
			return nil, ErrProtocol
		}
		if w.FormatType != "MARKDOWN" && w.FormatType != "RICHTEXT" {
			return nil, ErrProtocol
		}
	}
	return rows, nil
}

// Description preserves the rich-text source without giving it Markdown/HTML
// execution semantics. The fence cannot be closed by the source text.
func Description(raw, format string) (string, error) {
	switch format {
	case "MARKDOWN":
		return raw, nil
	case "RICHTEXT":
		longest, run := 0, 0
		for _, r := range raw {
			if r == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		return "云效富文本源码\n\n" + fence + "text\n" + raw + "\n" + fence, nil
	default:
		return "", ErrProtocol
	}
}
