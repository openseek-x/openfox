package asana

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func taskFixture(id string) map[string]any {
	return map[string]any{
		"gid": id, "name": "Example task", "notes": "<b>plain text</b> & literal notes\n第二行", "completed": false, "completed_at": nil,
		"created_at": "2026-11-01T03:00:00.000Z", "modified_at": "2026-11-02T03:00:00.000Z",
		"assignee": map[string]any{"gid": "user", "name": "User"}, "resource_subtype": "default_task", "parent": nil,
		"workspace": map[string]any{"gid": "workspace"}, "projects": []any{map[string]any{"gid": "project"}}, "num_subtasks": 0,
		"permalink_url": Origin + "/0/project/" + id, "due_on": "2026-12-01", "due_at": nil, "start_on": nil,
	}
}

func TestResourceEndpointsAndOffsetPagination(t *testing.T) {
	var paths []string
	largeID := "900719925474099312345"
	c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer access-sentinel" || r.Header.Get("Accept") != "application/json" {
			t.Error("resource request method/authentication changed")
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/1.0/users/me":
			if q.Get("opt_fields") != "gid,name,email,workspaces" || len(q) != 1 {
				t.Error("unexpected current-user fields")
			}
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": largeID, "name": "User", "email": "self@example.test", "workspaces": []any{map[string]any{"gid": "workspace", "name": "Workspace"}}}})
		case "/api/1.0/workspaces/workspace/users/" + largeID:
			if q.Get("opt_fields") != "gid,name,email" || len(q) != 1 {
				t.Error("unexpected workspace-user fields")
			}
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": largeID, "name": "User", "email": "self@example.test"}})
		case "/api/1.0/projects/project":
			if q.Get("opt_fields") != "gid,name,workspace,archived" || len(q) != 1 {
				t.Error("unexpected project fields")
			}
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": "project", "name": "Project", "workspace": map[string]any{"gid": "workspace"}, "archived": false}})
		case "/api/1.0/workspaces/workspace/projects":
			if q.Get("archived") != "false" || q.Get("limit") != "100" || q.Get("opt_fields") != "gid,name,workspace,archived" {
				t.Error("project list is not bounded or scoped")
			}
			if q.Get("offset") == "" {
				jsonResponse(w, map[string]any{"data": []any{map[string]any{"gid": "project", "name": "Project", "workspace": map[string]any{"gid": "workspace"}, "archived": false}},
					"next_page": map[string]any{"offset": "opaque+/=&offset", "uri": "https://attacker.test/capture?token=access-sentinel", "path": "/api/other"}})
			} else {
				if q.Get("offset") != "opaque+/=&offset" || len(q) != 4 {
					t.Error("offset did not survive query encoding")
				}
				jsonResponse(w, map[string]any{"data": []any{}, "next_page": nil})
			}
		case "/api/1.0/projects/project/tasks":
			if q.Get("opt_fields") != taskFields || q.Get("limit") != "100" || len(q) != 2 {
				t.Error("task request excludes fields or filters out historical completed tasks")
			}
			task := taskFixture("parent")
			task["num_subtasks"] = 1
			jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
		case "/api/1.0/tasks/parent/subtasks":
			if q.Get("opt_fields") != taskFields || q.Get("limit") != "100" || len(q) != 2 {
				t.Error("subtask request is not paginated")
			}
			task := taskFixture("child")
			task["parent"] = map[string]any{"gid": "parent"}
			task["projects"] = []any{} // Valid descendant without direct project membership.
			task["completed"] = true
			task["completed_at"] = "2026-11-02T11:04:05.123+08:00"
			jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
		default:
			t.Errorf("unplanned resource request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	user, err := c.UserMe(context.Background(), "access-sentinel")
	if err != nil || user.GID != largeID || user.Email != "self@example.test" || len(user.Workspaces) != 1 {
		t.Fatalf("current user: %v", err)
	}
	member, err := c.WorkspaceUser(context.Background(), "access-sentinel", "workspace", user.GID)
	if err != nil || member.GID != user.GID {
		t.Fatalf("workspace user: %v", err)
	}
	project, err := c.Project(context.Background(), "access-sentinel", "project")
	if err != nil || project.Workspace.GID != "workspace" || project.Archived {
		t.Fatalf("project: %v", err)
	}
	first, err := c.ProjectsPage(context.Background(), "access-sentinel", "workspace", "")
	if err != nil || len(first.Data) != 1 || first.NextOffset != "opaque+/=&offset" || transport.calls.Load() != 4 {
		t.Fatalf("first page followed uri or lost offset: %v", err)
	}
	second, err := c.ProjectsPage(context.Background(), "access-sentinel", "workspace", first.NextOffset)
	if err != nil || len(second.Data) != 0 || second.NextOffset != "" {
		t.Fatalf("last page: %v", err)
	}
	tasks, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
	if err != nil || len(tasks.Data) != 1 || tasks.Data[0].NumSubtasks != 1 || tasks.Data[0].Notes != taskFixture("x")["notes"] {
		t.Fatalf("project tasks: %v", err)
	}
	children, err := c.SubtasksPage(context.Background(), "access-sentinel", tasks.Data[0].GID, "")
	if err != nil || len(children.Data) != 1 || children.Data[0].Parent.GID != "parent" || !children.Data[0].Completed || len(children.Data[0].Projects) != 0 {
		t.Fatalf("subtasks: %v", err)
	}
	wantCompleted, _ := time.Parse(time.RFC3339Nano, "2026-11-02T03:04:05.123Z")
	if children.Data[0].CompletedAt == nil || !children.Data[0].CompletedAt.Equal(wantCompleted) {
		t.Fatal("completed_at was replaced or timezone lost")
	}
	if len(paths) != 7 {
		t.Fatalf("unexpected automatic traversal: %v", paths)
	}
}

func TestTaskRequiredFieldsAndDates(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"numeric_gid", func(m map[string]any) { m["gid"] = 9007199254740993 }},
		{"missing_name", func(m map[string]any) { delete(m, "name") }},
		{"missing_notes", func(m map[string]any) { delete(m, "notes") }},
		{"null_notes", func(m map[string]any) { m["notes"] = nil }},
		{"missing_completed", func(m map[string]any) { delete(m, "completed") }},
		{"null_completed", func(m map[string]any) { m["completed"] = nil }},
		{"string_completed", func(m map[string]any) { m["completed"] = "false" }},
		{"missing_num_subtasks", func(m map[string]any) { delete(m, "num_subtasks") }},
		{"null_num_subtasks", func(m map[string]any) { m["num_subtasks"] = nil }},
		{"fractional_num_subtasks", func(m map[string]any) { m["num_subtasks"] = 1.5 }},
		{"negative_num_subtasks", func(m map[string]any) { m["num_subtasks"] = -1 }},
		{"missing_workspace", func(m map[string]any) { delete(m, "workspace") }},
		{"numeric_workspace_gid", func(m map[string]any) { m["workspace"] = map[string]any{"gid": 12} }},
		{"missing_parent", func(m map[string]any) { delete(m, "parent") }},
		{"empty_parent", func(m map[string]any) { m["parent"] = map[string]any{} }},
		{"self_parent", func(m map[string]any) { m["parent"] = map[string]any{"gid": "task"} }},
		{"missing_assignee", func(m map[string]any) { delete(m, "assignee") }},
		{"empty_assignee", func(m map[string]any) { m["assignee"] = map[string]any{} }},
		{"missing_projects", func(m map[string]any) { delete(m, "projects") }},
		{"null_projects", func(m map[string]any) { m["projects"] = nil }},
		{"numeric_project_gid", func(m map[string]any) { m["projects"] = []any{map[string]any{"gid": 12}} }},
		{"bad_completion_time", func(m map[string]any) { m["completed"] = true; m["completed_at"] = "yesterday" }},
		{"incomplete_with_completion_time", func(m map[string]any) { m["completed_at"] = "2026-11-01T00:00:00Z" }},
		{"bad_created_time", func(m map[string]any) { m["created_at"] = "today" }},
		{"bad_modified_time", func(m map[string]any) { m["modified_at"] = "2026-11-13" }},
		{"bad_due_date", func(m map[string]any) { m["due_on"] = "2026-02-30" }},
		{"bad_due_time", func(m map[string]any) { m["due_at"] = "2026-11-13" }},
		{"unsafe_source_url", func(m map[string]any) { m["permalink_url"] = "javascript:alert(1)" }},
		{"wrong_source_origin", func(m map[string]any) { m["permalink_url"] = "https://attacker.test/task" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := taskFixture("task")
			tc.change(task)
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
			})
			_, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
			assertCode(t, err, CodeProtocol)
		})
	}
	t.Run("unknown_completion_and_empty_values", func(t *testing.T) {
		task := taskFixture("task")
		task["completed"], task["completed_at"], task["assignee"] = true, nil, nil
		task["name"], task["notes"] = "", ""
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
		})
		page, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
		if err != nil || page.Data[0].CompletedAt != nil || page.Data[0].Assignee != nil || page.Data[0].Parent != nil {
			t.Fatalf("unknown completion/explicit nulls rejected: %v", err)
		}
	})
	t.Run("cell_bounds", func(t *testing.T) {
		for _, size := range []int{MaxCellBytes, MaxCellBytes + 1} {
			task := taskFixture("task")
			task["notes"] = strings.Repeat("n", size)
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
			})
			page, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
			if size > MaxCellBytes {
				assertCode(t, err, CodeLimit)
			} else if err != nil || len(page.Data[0].Notes) != MaxCellBytes {
				t.Fatalf("exact cell limit failed: %v", err)
			}
		}
	})
}

func TestPaginationRejectsIncompleteOrLoopingPages(t *testing.T) {
	cases := []struct{ name, body, offset string }{
		{"data_missing", `{"next_page":null}`, ""},
		{"data_null", `{"data":null,"next_page":null}`, ""},
		{"data_object", `{"data":{},"next_page":null}`, ""},
		{"next_missing", `{"data":[]}`, ""},
		{"next_empty", `{"data":[],"next_page":{}}`, ""},
		{"offset_null", `{"data":[],"next_page":{"offset":null}}`, ""},
		{"offset_number", `{"data":[],"next_page":{"offset":123}}`, ""},
		{"offset_empty", `{"data":[],"next_page":{"offset":""}}`, ""},
		{"same_offset", `{"data":[],"next_page":{"offset":"same"}}`, "same"},
		{"trailing_body", `{"data":[],"next_page":null}[]`, ""},
		{"malformed", `{"data":`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })
			_, err := c.TasksPage(context.Background(), "access-sentinel", "project", tc.offset)
			assertCode(t, err, CodeProtocol)
		})
	}
	t.Run("oversized_offset", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": []any{}, "next_page": map[string]any{"offset": strings.Repeat("o", maxOffsetBytes+1)}})
		})
		_, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
		assertCode(t, err, CodeProtocol)
	})
	t.Run("page_size", func(t *testing.T) {
		for _, size := range []int{0, 1, 100, 101} {
			tasks := make([]any, size)
			for i := range tasks {
				tasks[i] = taskFixture("task")
			}
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"data": tasks, "next_page": nil})
			})
			page, err := c.TasksPage(context.Background(), "access-sentinel", "project", "")
			if size > 100 {
				assertCode(t, err, CodeProtocol)
			} else if err != nil || len(page.Data) != size {
				t.Fatalf("page size %d: %v", size, err)
			}
		}
	})
}

func TestResourceIdentityMismatchAndIncompleteUser(t *testing.T) {
	t.Run("workspace_user_mismatch", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": "other", "name": "Other", "email": "self@example.test"}})
		})
		_, err := c.WorkspaceUser(context.Background(), "access-sentinel", "workspace", "self")
		assertCode(t, err, CodeProtocol)
	})
	t.Run("project_mismatch", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": "other", "name": "Project", "workspace": map[string]any{"gid": "workspace"}, "archived": false}})
		})
		_, err := c.Project(context.Background(), "access-sentinel", "project")
		assertCode(t, err, CodeProtocol)
	})
	for _, field := range []string{"workspace", "archived", "wrong_workspace", "archived_true"} {
		t.Run("project_"+field, func(t *testing.T) {
			p := map[string]any{"gid": "project", "name": "Project", "workspace": map[string]any{"gid": "workspace"}, "archived": false}
			switch field {
			case "wrong_workspace":
				p["workspace"] = map[string]any{"gid": "other"}
			case "archived_true":
				p["archived"] = true
			default:
				delete(p, field)
			}
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"data": []any{p}, "next_page": nil})
			})
			_, err := c.ProjectsPage(context.Background(), "access-sentinel", "workspace", "")
			assertCode(t, err, CodeProtocol)
		})
	}
	t.Run("subtask_wrong_parent", func(t *testing.T) {
		task := taskFixture("child")
		task["parent"] = map[string]any{"gid": "other"}
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": []any{task}, "next_page": nil})
		})
		_, err := c.SubtasksPage(context.Background(), "access-sentinel", "parent", "")
		assertCode(t, err, CodeProtocol)
	})
	for _, body := range []string{`{"data":null}`, `{"data":{"gid":123,"name":"User","workspaces":[]}}`, `{"data":{"gid":"u","workspaces":[]}}`, `{"data":{"gid":"u","name":"User"}}`, `{"data":{"gid":"u","name":"User","workspaces":null}}`} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		_, err := c.UserMe(context.Background(), "access-sentinel")
		assertCode(t, err, CodeProtocol)
	}
	t.Run("missing_email_is_not_fabricated", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": "u", "name": "User", "workspaces": []any{}}})
		})
		user, err := c.UserMe(context.Background(), "access-sentinel")
		if err != nil || user.Email != "" || !reflect.DeepEqual(user.Workspaces, []Named{}) {
			t.Fatalf("missing email must remain unknown for identity-layer rejection: %v", err)
		}
	})
}

func TestTaskJSONRoundTripRetainsCompletionAndParent(t *testing.T) {
	completed := time.Date(2026, 11, 14, 2, 4, 0, 0, time.UTC)
	task := Task{GID: "child", Name: "Child", Notes: "Plain text", Completed: true, CompletedAt: &completed,
		Workspace: Ref{GID: "workspace"}, Parent: &Ref{GID: "parent"}, Projects: []Ref{}, NumSubtasks: 0}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTask(raw)
	if err != nil || !reflect.DeepEqual(parsed, task) {
		t.Fatalf("staging roundtrip changed source semantics: %v", err)
	}
}
