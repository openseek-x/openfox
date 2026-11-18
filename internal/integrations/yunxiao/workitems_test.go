package yunxiao

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestWorkitemsUseActualCategoriesAndRejectPartialSource(t *testing.T) {
	for _, problem := range []string{"", "cross_project", "unknown_type", "unknown_format", "unknown_logic"} {
		t.Run(problem, func(t *testing.T) {
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case base("org-a") + "/workitemTypes":
					fmt.Fprint(w, `[{"id":"kind-a","name":"Custom","categoryId":"CustomCategory"}]`)
				case base("org-a") + "/projects/project-a/workitemTypes":
					if r.URL.Query().Get("category") != "CustomCategory" {
						t.Error("guessed category")
					}
					fmt.Fprint(w, `[{"id":"kind-a","name":"Custom","categoryId":"CustomCategory"}]`)
				case base("org-a") + "/workitems:search":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["spaceId"] != "project-a" || body["category"] != "CustomCategory" || body["spaceType"] != "Project" {
						t.Fatal("wrong read request")
					}
					var condition map[string]any
					if json.Unmarshal([]byte(body["conditions"].(string)), &condition) != nil {
						t.Error("conditions not JSON string")
					}
					row := Workitem{ID: "item-a", Subject: "Title", Description: "# source", FormatType: "MARKDOWN", CategoryID: "CustomCategory", LogicalStatus: "normal", Space: Named{ID: "project-a"}, Status: Named{ID: "same-status", Name: "Open"}, WorkitemType: Named{ID: "kind-a", Name: "Custom"}}
					switch problem {
					case "cross_project":
						row.Space.ID = "project-b"
					case "unknown_type":
						row.WorkitemType.ID = "unknown"
					case "unknown_format":
						row.FormatType = "HTML"
					case "unknown_logic":
						row.LogicalStatus = "deleted"
					}
					for k, v := range map[string]string{"x-total": "1", "x-page": "1", "x-per-page": "200", "x-total-pages": "1"} {
						w.Header().Set(k, v)
					}
					_ = json.NewEncoder(w).Encode([]Workitem{row})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			rows, err := client.Workitems(context.Background(), testToken, "org-a", "project-a")
			if problem == "" {
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%v err=%v", rows, err)
				}
			} else if err == nil || rows != nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
}

func TestFieldsAndRichTextAreExplicitAndInert(t *testing.T) {
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != base("org-a")+"/projects/project-a/workitemTypes/kind-a/fields" {
			t.Fatal("wrong field endpoint")
		}
		fmt.Fprint(w, `[{"id":"custom-rank","name":"Urgency","type":"CustomField","format":"list","options":[{"id":"900","displayValue":"Later","value":"later"},{"id":"1","displayValue":"Soon","value":"soon"}]},{"id":"tags","name":"Tags","type":"NativeField","format":"multiList"}]`)
	})
	fields, err := client.Fields(context.Background(), testToken, "org-a", "project-a", "kind-a")
	if err != nil || len(fields) != 1 || fields[0].Options[0].ID != "900" {
		t.Fatalf("fields=%v err=%v", fields, err)
	}
	raw := "<script>bad()</script>&amp;```\n`````\n中文🙂"
	rich, err := Description(raw, "RICHTEXT")
	if err != nil || !strings.Contains(rich, "``````text\n"+raw+"\n``````") {
		t.Fatalf("unsafe rich text: %q %v", rich, err)
	}
	if plain, e := Description(raw, "MARKDOWN"); e != nil || plain != raw {
		t.Fatal("markdown changed")
	}
	if _, e := Description(raw, "HTML"); e == nil {
		t.Fatal("unknown format accepted")
	}
}
