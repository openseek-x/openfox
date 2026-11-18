package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func decodeFixture(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if e := json.NewDecoder(r.Body).Decode(out); e != nil {
		t.Fatal(e)
	}
}

var snapshot = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func workFixture(id int64) map[string]any {
	return map[string]any{"id": id, "rev": 3, "fields": map[string]any{"System.Id": id, "System.TeamProject": "Project", "System.Title": "Bounded title", "System.WorkItemType": "Custom Bug", "System.State": "Custom Active", "System.CreatedDate": snapshot.Add(-time.Hour), "System.ChangedDate": snapshot.Add(-time.Minute), "System.Description": "<p>raw HTML only</p>", "System.AssignedTo": map[string]any{"id": subjectID, "displayName": "Verified user"}}, "relations": []any{map[string]any{"rel": "System.LinkTypes.Hierarchy-Reverse", "url": "https://dev.azure.com/org/_apis/wit/workItems/9"}}}
}
func TestBoardsMetadataNamesAndSafePath(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Test-Origin") != "https://dev.azure.com" || r.URL.Query().Get("api-version") != "7.1" {
			t.Error("origin/version")
		}
		switch r.URL.Path {
		case projectPath("org", projectID) + "/_apis/wit/workitemtypes":
			jsonReply(w, map[string]any{"value": []WorkItemType{{Name: "Custom Bug", ReferenceName: "Custom.Bug"}}})
		case projectPath("org", projectID) + "/_apis/wit/workitemtypes/Custom Bug/states":
			if !strings.Contains(r.RequestURI, "Custom%20Bug") || strings.Contains(r.RequestURI, "%2520") {
				t.Error("wrong escaping")
			}
			jsonReply(w, map[string]any{"value": []WorkItemState{{Name: "Custom Active", Category: "InProgress"}}})
		case projectPath("org", projectID) + "/_apis/wit/fields":
			jsonReply(w, map[string]any{"value": []WorkItemField{{Name: "Body", ReferenceName: "System.Description", Type: "html"}, {Name: "Assigned to", ReferenceName: "System.AssignedTo", Type: "string", IsIdentity: true}}})
		default:
			t.Error("path")
			w.WriteHeader(404)
		}
	})
	types, e := c.WorkItemTypes(context.Background(), "token", "org", projectID)
	if e != nil || types[0].ReferenceName != "Custom.Bug" {
		t.Fatal(types, e)
	}
	states, e := c.WorkItemStates(context.Background(), "token", "org", projectID, types[0].Name)
	if e != nil || states[0].Category != "InProgress" {
		t.Fatal(states, e)
	}
	fields, e := c.WorkItemFields(context.Background(), "token", "org", projectID)
	if e != nil || fields[0].Type != "html" || !fields[1].IsIdentity {
		t.Fatal(fields, e)
	}
	for _, name := range []string{"../x", "a/b", "a\\b", "..", "a\n"} {
		_, e := c.WorkItemStates(context.Background(), "token", "org", projectID, name)
		errorCode(t, e, "invalid_request", false)
	}
	if calls.Load() != 3 {
		t.Fatal("invalid path reached upstream")
	}
}
func TestWIQLFixedSnapshotInclusiveBoundsAndAscendingIDPages(t *testing.T) {
	lower := snapshot.Add(-time.Hour)
	var queries []string
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		decodeFixture(t, r, &body)
		queries = append(queries, body.Query)
		if r.URL.Query().Get("timePrecision") != "true" || r.URL.Path != projectPath("org", projectID)+"/_apis/wit/wiql" || r.Method != "POST" {
			t.Error("request shape")
		}
		ids := []map[string]int64{}
		if r.URL.Query().Get("$top") == "1" {
			ids = append(ids, map[string]int64{"id": 1})
		} else {
			if r.URL.Query().Get("$top") != "200" || !strings.Contains(body.Query, "[System.Id] > 200") || !strings.Contains(body.Query, "[System.TeamProject] = @project") || !strings.Contains(body.Query, "[System.ChangedDate] >= '2026-11-14T11:00:00Z'") || !strings.Contains(body.Query, "[System.ChangedDate] <= '2026-11-14T12:00:00Z'") || !strings.HasSuffix(body.Query, "ORDER BY [System.Id] ASC ASOF '2026-11-14T12:00:00Z'") {
				t.Error(body.Query)
			}
			for i := int64(201); i <= 400; i++ {
				ids = append(ids, map[string]int64{"id": i})
			}
		}
		jsonReply(w, map[string]any{"queryType": "flat", "asOf": snapshot, "workItems": ids})
	})
	cutover, e := c.WorkItemCutover(context.Background(), "token", "org", projectID)
	if e != nil || !cutover.Equal(snapshot) {
		t.Fatal(cutover, e)
	}
	page, e := c.QueryWorkItems(context.Background(), "token", "org", projectID, WorkItemQuery{AsOf: cutover, AfterID: 200, ChangedFrom: &lower, ChangedThrough: &cutover})
	if e != nil || len(page.IDs) != 200 || page.IDs[199] != 400 {
		t.Fatal(page, e)
	}
	if strings.Contains(queries[0], "ASOF") {
		t.Fatal("cutover uses unverified local clock")
	}
	_, e = c.QueryWorkItems(context.Background(), "token", "org", projectID, WorkItemQuery{AsOf: cutover, AfterID: -1})
	errorCode(t, e, "invalid_request", false)
}
func TestWIQLRejectsMissingSnapshotDuplicateDescendingAndOverLimit(t *testing.T) {
	for _, mode := range []string{"duplicate", "descending", "old_id", "over", "wrong_asof", "missing", "relation_query"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				ids := []map[string]int64{{"id": 11}, {"id": 12}}
				asof := snapshot
				queryType := "flat"
				switch mode {
				case "duplicate":
					ids[1]["id"] = 11
				case "descending":
					ids[1]["id"] = 9
				case "old_id":
					ids[0]["id"] = 10
				case "over":
					ids = make([]map[string]int64, 201)
				case "wrong_asof":
					asof = asof.Add(time.Second)
				case "relation_query":
					queryType = "tree"
				case "missing":
					jsonReply(w, map[string]any{})
					return
				}
				jsonReply(w, map[string]any{"queryType": queryType, "asOf": asof, "workItems": ids})
			})
			_, e := c.QueryWorkItems(context.Background(), "token", "org", projectID, WorkItemQuery{AsOf: snapshot, AfterID: 10})
			errorCode(t, e, "invalid_response", false)
		})
	}
}
func TestWorkItemsBatchSnapshotFieldsRawHTMLAndNoPartialResults(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/workitemsbatch") {
			var body struct {
				IDs         []int64  `json:"ids"`
				Fields      []string `json:"fields"`
				AsOf        string   `json:"asOf"`
				ErrorPolicy string   `json:"errorPolicy"`
				Expand      string   `json:"$expand"`
			}
			decodeFixture(t, r, &body)
			if len(body.IDs) != 200 || body.AsOf != snapshot.Format(time.RFC3339Nano) || body.ErrorPolicy != "Fail" || body.Expand != "Relations" || !strings.Contains(strings.Join(body.Fields, ","), "System.Description") {
				t.Error("batch contract")
			}
			items := []any{}
			for _, id := range body.IDs {
				items = append(items, workFixture(id))
			}
			jsonReply(w, map[string]any{"value": items})
		} else {
			if r.URL.Query().Get("asOf") != snapshot.Format(time.RFC3339Nano) || r.URL.Query().Get("$expand") != "Relations" {
				t.Error("single contract")
			}
			jsonReply(w, workFixture(1))
		}
	})
	ids := []int64{}
	for i := int64(1); i <= 200; i++ {
		ids = append(ids, i)
	}
	items, e := c.WorkItems(context.Background(), "token", "org", Project{ID: projectID, Name: "Project"}, ids, snapshot, []string{"System.Description", "System.AssignedTo"})
	if e != nil || len(items) != 200 || string(items[0].Fields["System.Description"]) != "\"\\u003cp\\u003eraw HTML only\\u003c/p\\u003e\"" {
		t.Fatal(len(items), e)
	}
	single, e := c.WorkItem(context.Background(), "token", "org", Project{ID: projectID, Name: "Project"}, 1, snapshot, nil)
	if e != nil || single.Rev != 3 || len(single.Relations) != 1 {
		t.Fatal(single, e)
	}
	_, e = c.WorkItems(context.Background(), "token", "org", Project{ID: projectID, Name: "Project"}, append(ids, 201), snapshot, nil)
	errorCode(t, e, "invalid_request", false)
	_, e = c.WorkItems(context.Background(), "token", "org", Project{ID: projectID, Name: "Project"}, []int64{1, 1}, snapshot, nil)
	errorCode(t, e, "invalid_request", false)
}
func TestWorkItemsRejectMalformedCrossProjectAndOversizedValues(t *testing.T) {
	for _, mode := range []string{"wrong_project", "missing_id", "float_rev", "zero_rev", "future", "oversized", "assigned_email", "missing_row", "wrong_id", "duplicate", "malformed_relation"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				v := workFixture(1)
				f := v["fields"].(map[string]any)
				items := []any{v}
				switch mode {
				case "wrong_project":
					f["System.TeamProject"] = "Other"
				case "missing_id":
					delete(f, "System.Id")
				case "float_rev":
					v["rev"] = 1.2
				case "zero_rev":
					v["rev"] = 0
				case "future":
					f["System.ChangedDate"] = snapshot.Add(time.Second)
				case "oversized":
					f["System.Description"] = strings.Repeat("x", MaxCellBytes+1)
				case "assigned_email":
					f["System.AssignedTo"] = map[string]string{"id": "email@example.test"}
				case "missing_row":
					items = []any{}
				case "wrong_id":
					v["id"] = 2
				case "duplicate":
					items = append(items, v)
				case "malformed_relation":
					v["relations"] = []any{map[string]any{"rel": 123}}
				}
				jsonReply(w, map[string]any{"value": items})
			})
			items, e := c.WorkItems(context.Background(), "token", "org", Project{ID: projectID, Name: "Project"}, []int64{1}, snapshot, nil)
			errorCode(t, e, "invalid_response", false)
			if items != nil {
				t.Fatal("returned partial batch")
			}
		})
	}
}
func TestBoardsReadOnlyPostErrorAndResponseLimit(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, "credential must not leak")
	})
	_, e := c.QueryWorkItems(context.Background(), "token", "org", projectID, WorkItemQuery{AsOf: snapshot})
	errorCode(t, e, "upstream_error", false)
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", MaxResponseBytes+1)) })
	_, e = c.WorkItemTypes(context.Background(), "token", "org", projectID)
	errorCode(t, e, "response_limit_or_read", false)
}
