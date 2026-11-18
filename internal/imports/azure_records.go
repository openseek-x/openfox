package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
)

const SourceAzure = "azure_devops_api"

var errAzureShape = errors.New("imports: Azure work item contract changed")
var errAzureLimit = errors.New("imports: Azure project exceeds bounded source limits")

type AzureTypeMetadata struct {
	Type   azuredevops.WorkItemType    `json:"type"`
	States []azuredevops.WorkItemState `json:"states"`
}
type AzureMetadata struct {
	Types         []AzureTypeMetadata         `json:"types"`
	Fields        []azuredevops.WorkItemField `json:"fields"`
	PriorityField string                      `json:"priority_field,omitempty"`
}
type AzureRecordMetadata struct {
	Revision      int64  `json:"revision"`
	TypeLabel     string `json:"type_label"`
	StateLabel    string `json:"state_label"`
	PriorityField string `json:"priority_field,omitempty"`
	PriorityValue string `json:"priority_value,omitempty"`
}

// ReadAzureMetadata proves a single project and its actual process definitions.
// Each request owns its own grant lease, with an overall bounded HTTP budget.
func ReadAzureMetadata(ctx context.Context, runtime integrations.AzureRuntime, org string, source integrations.AzureAccess, projectID, priorityField string) (azuredevops.Project, AzureMetadata, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var project azuredevops.Project
	meta := AzureMetadata{Types: []AzureTypeMetadata{}, Fields: []azuredevops.WorkItemField{}, PriorityField: priorityField}
	var cutover time.Time
	call := func(fn func(context.Context, *azuredevops.Client, string) error) error {
		_, e := runtime.Do(ctx, org, source.ConnectionID, source.ConnectionVersion, source.GrantGeneration, fn)
		return e
	}
	err := call(func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		project, e = c.Project(ctx, token, source.OrganizationName, projectID)
		return e
	})
	if err != nil {
		return project, meta, cutover, err
	}
	if utf8.RuneCountInString(project.Name) > 255 {
		return project, meta, cutover, errAzureLimit
	}
	err = call(func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		meta.Fields, e = c.WorkItemFields(ctx, token, source.OrganizationName, projectID)
		return e
	})
	if err != nil {
		return project, meta, cutover, err
	}
	var types []azuredevops.WorkItemType
	err = call(func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		types, e = c.WorkItemTypes(ctx, token, source.OrganizationName, projectID)
		return e
	})
	if err != nil {
		return project, meta, cutover, err
	}
	if len(types) > 50 {
		return project, meta, cutover, errAzureLimit
	}
	for _, kind := range types {
		if kind.Disabled {
			continue
		}
		entry := AzureTypeMetadata{Type: kind}
		err = call(func(ctx context.Context, c *azuredevops.Client, token string) error {
			var e error
			entry.States, e = c.WorkItemStates(ctx, token, source.OrganizationName, projectID, kind.Name)
			return e
		})
		if err != nil {
			return project, meta, cutover, err
		}
		meta.Types = append(meta.Types, entry)
	}
	if _, err = meta.selectedFields(); err != nil {
		return project, meta, cutover, err
	}
	raw, _ := json.Marshal(meta)
	if len(raw) > 262144 {
		return project, meta, cutover, errAzureLimit
	}
	err = call(func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		cutover, e = c.WorkItemCutover(ctx, token, source.OrganizationName, projectID)
		return e
	})
	return project, meta, cutover, err
}

func (m AzureMetadata) selectedFields() ([]string, error) {
	fields := []string{}
	seenPriority := m.PriorityField == ""
	for _, f := range m.Fields {
		switch f.ReferenceName {
		case "System.Description", "Microsoft.VSTS.TCM.ReproSteps":
			if f.Type != "html" && f.Type != "string" && f.Type != "plainText" {
				return nil, errAzureShape
			}
			fields = append(fields, f.ReferenceName)
		case "System.AssignedTo", "System.CreatedBy", "Microsoft.VSTS.Common.ClosedDate":
			fields = append(fields, f.ReferenceName)
		}
		if f.ReferenceName == m.PriorityField {
			if f.IsIdentity || (f.Type != "integer" && f.Type != "double" && f.Type != "string") {
				return nil, ErrInvalidRequest
			}
			seenPriority = true
			fields = append(fields, f.ReferenceName)
		}
	}
	if !seenPriority {
		return nil, ErrInvalidRequest
	}
	return fields, nil
}
func azureInstance(account, project string) string {
	sum := sha256.Sum256([]byte("https://dev.azure.com\x00" + account + "\x00" + project))
	return hex.EncodeToString(sum[:])
}
func azurePrioritySource(field, value string) string { return field + "=" + value }

func azurePerson(raw json.RawMessage, account string) Person {
	if len(raw) == 0 || string(raw) == "null" {
		return Person{}
	}
	var i azuredevops.IdentityRef
	if json.Unmarshal(raw, &i) != nil || !azuredevops.ValidUUID(i.ID) {
		return Person{}
	}
	return Person{ID: account + "/" + strings.ToLower(i.ID), Name: azuredevops.DisplayName(i.DisplayName, i.ID)}
}

func azureRecord(item azuredevops.WorkItem, account, organization, project string, meta AzureMetadata) (Record, error) {
	r := Record{ID: strconv.FormatInt(item.ID, 10), Key: strconv.FormatInt(item.ID, 10), PlainNotes: true, Title: rawString(item.Fields["System.Title"]), Created: rawString(item.Fields["System.CreatedDate"]), Updated: rawString(item.Fields["System.ChangedDate"])}
	r.Azure = &AzureRecordMetadata{Revision: item.Rev}
	kind, state := rawString(item.Fields["System.WorkItemType"]), rawString(item.Fields["System.State"])
	found := false
	for _, entry := range meta.Types {
		if entry.Type.Name != kind {
			continue
		}
		r.TypeName = entry.Type.ReferenceName
		r.Azure.TypeLabel = kind
		for _, s := range entry.States {
			if s.Name != state {
				continue
			}
			r.Status = entry.Type.ReferenceName + "/" + s.Name
			r.Azure.StateLabel = s.Name
			found = true
			switch s.Category {
			case "Proposed":
				r.StatusCategory = "new"
			case "InProgress", "Resolved":
				r.StatusCategory = "indeterminate"
			case "Completed", "Removed":
				r.StatusCategory = "done"
			}
		}
	}
	if !found || r.StatusCategory == "" || !azuredevops.ValidObjectID(item.ID) || !azuredevops.ValidObjectID(item.Rev) {
		return Record{}, errAzureShape
	}
	descriptions := []string{}
	for _, f := range meta.Fields {
		if f.ReferenceName != "System.Description" && f.ReferenceName != "Microsoft.VSTS.TCM.ReproSteps" {
			continue
		}
		raw := rawString(item.Fields[f.ReferenceName])
		if raw == "" {
			continue
		}
		if f.Type == "html" {
			var e error
			raw, e = azuredevops.PlainDescription(raw)
			if e != nil {
				return Record{}, e
			}
		} else if f.Type != "string" && f.Type != "plainText" {
			return Record{}, errAzureShape
		}
		if len(raw) > MaxCellBytes {
			return Record{}, errAzureLimit
		}
		descriptions = append(descriptions, raw)
	}
	r.Description = strings.Join(descriptions, "\n\n")
	if len(r.Description) > MaxCellBytes {
		return Record{}, errAzureLimit
	}
	r.Assignee = azurePerson(item.Fields["System.AssignedTo"], account)
	r.Creator = azurePerson(item.Fields["System.CreatedBy"], account)
	if raw := item.Fields["Microsoft.VSTS.Common.ClosedDate"]; len(raw) > 0 && string(raw) != "null" {
		var completed time.Time
		if json.Unmarshal(raw, &completed) != nil || completed.IsZero() {
			return Record{}, errAzureShape
		}
		if r.StatusCategory == "done" {
			r.CompletedAt = &completed
		}
	}
	if meta.PriorityField != "" {
		raw := item.Fields[meta.PriorityField]
		var value string
		if len(raw) > 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &value) != nil {
				var n json.Number
				if json.Unmarshal(raw, &n) != nil {
					return Record{}, errAzureShape
				}
				value = string(n)
			}
			if len(value) > 256 {
				return Record{}, errAzureLimit
			}
		}
		r.Azure.PriorityField = meta.PriorityField
		r.Azure.PriorityValue = value
	}
	r.URL = "https://dev.azure.com/" + url.PathEscape(organization) + "/" + project + "/_workitems/edit/" + r.ID
	for _, rel := range item.Relations {
		if rel.Rel != "System.LinkTypes.Hierarchy-Reverse" {
			continue
		}
		u, e := url.Parse(rel.URL)
		if e != nil || u.Scheme != "https" || u.Host != "dev.azure.com" || u.User != nil {
			r.SourceWarnings = append(r.SourceWarnings, "azure_parent_outside_project")
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 6 && parts[0] == organization && parts[1] == project {
			parts = append(parts[:1], parts[2:]...)
		}
		if len(parts) != 5 || parts[0] != organization || parts[1] != "_apis" || parts[2] != "wit" || !strings.EqualFold(parts[3], "workItems") {
			r.SourceWarnings = append(r.SourceWarnings, "azure_parent_outside_project")
			continue
		}
		id, e := strconv.ParseInt(parts[4], 10, 32)
		if e != nil || id <= 0 || r.ParentID != "" {
			return Record{}, errAzureShape
		}
		r.ParentID = strconv.FormatInt(id, 10)
	}
	return r, nil
}
