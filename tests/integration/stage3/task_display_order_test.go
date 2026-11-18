package stage3

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"testing"
)

func TestTaskDirectionPagination(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space.ID
	if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "manual"}); response.Status != http.StatusOK {
		t.Fatalf("enable iterations: %d %s", response.Status, response.Body)
	}
	if response := estPatch(env, owner, space, map[string]any{"scale": "fibonacci"}); response.Status != http.StatusOK {
		t.Fatalf("enable estimation: %d %s", response.Status, response.Body)
	}
	iteration := createIteration(env, space, "2030-01-07", "2030-01-20")
	project := env.newProject(owner, space, map[string]any{"name": "A project"})
	for i := 0; i < 7; i++ {
		body := map[string]any{"title": fmt.Sprintf("ordered %d", i), "priority": "HIGH"}
		if i < 3 {
			body["iteration_id"] = iteration.ID
			body["project_id"] = project.ID
			body["estimate"] = 3
			body["due_date"] = "2030-02-01"
		} else if i == 3 {
			body["estimate"] = 1
			body["due_date"] = "2030-01-01"
			body["priority"] = "LOW"
		}
		env.createTask(owner, space, body)
	}
	// Controlled equal-value samples exercise the UUID tie-breaker independently of wall-clock timing.
	env.exec(`UPDATE openfox.tasks SET created_at = '2026-11-01T00:00:00Z', updated_at = '2026-11-01T00:00:00Z'
		WHERE organization_id = $1::uuid AND space_id = $2::uuid`, env.org, space)
	type orderedTask struct {
		ID          string   `json:"id"`
		IterationID string   `json:"iteration_id"`
		ProjectID   string   `json:"project_id"`
		DueDate     string   `json:"due_date"`
		Estimate    *float64 `json:"estimate"`
	}
	type taskPage struct {
		Tasks      []orderedTask `json:"tasks"`
		NextCursor string        `json:"next_cursor"`
	}
	ids := func(tasks []orderedTask) []string {
		result := make([]string, len(tasks))
		for i, task := range tasks {
			result[i] = task.ID
		}
		return result
	}
	path := spacePath(space) + "/tasks"
	for _, sort := range []string{"manual", "created", "updated", "priority", "due", "estimate", "iteration", "project"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(sort+"_"+order, func(t *testing.T) {
				query := "?sort=" + sort + "&order=" + order
				if sort == "manual" && order == "desc" {
					owner.expectError(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, path+query, nil)
					return
				}
				var full taskPage
				owner.must(http.StatusOK, http.MethodGet, path+query+"&limit=100", nil, &full)
				if len(full.Tasks) != 7 {
					t.Fatalf("full count %d", len(full.Tasks))
				}
				var combined []string
				cursor := ""
				for page := 0; page < 10; page++ {
					var part taskPage
					owner.must(http.StatusOK, http.MethodGet, path+query+"&limit=2&cursor="+url.QueryEscape(cursor), nil, &part)
					combined = append(combined, ids(part.Tasks)...)
					if part.NextCursor == "" {
						break
					}
					cursor = part.NextCursor
					if sort == "iteration" && part.Tasks[len(part.Tasks)-1].IterationID == "" {
						// Continue from an old server's v2 null cursor during a mixed-version rollout.
						raw, err := base64.RawURLEncoding.DecodeString(cursor)
						if err != nil {
							t.Fatal(err)
						}
						var legacy map[string]any
						if err := json.Unmarshal(raw, &legacy); err != nil {
							t.Fatal(err)
						}
						legacy["k"] = "0"
						raw, err = json.Marshal(legacy)
						if err != nil {
							t.Fatal(err)
						}
						cursor = base64.RawURLEncoding.EncodeToString(raw)
					}
				}
				if !reflect.DeepEqual(combined, ids(full.Tasks)) {
					t.Fatalf("pagination differs from full: %v != %v", combined, ids(full.Tasks))
				}
				if sort != "manual" {
					opposite := "asc"
					if order == "asc" {
						opposite = "desc"
					}
					owner.expectError(http.StatusBadRequest, "INVALID_CURSOR", http.MethodGet,
						path+"?sort="+sort+"&order="+opposite+"&cursor="+url.QueryEscape(cursor), nil)
				}
				if sort == "created" || sort == "updated" {
					expected := append([]string{}, combined...)
					slices.Sort(expected)
					if order == "desc" {
						slices.Reverse(expected)
					}
					if !reflect.DeepEqual(combined, expected) {
						t.Fatalf("unstable equal-value order: %v", combined)
					}
				}
				if sort == "iteration" {
					// Preserve legacy placement: unassigned iterations are last ASC and first DESC.
					if (full.Tasks[0].IterationID == "") != (order == "desc") ||
						(full.Tasks[6].IterationID == "") != (order == "asc") {
						t.Fatalf("iteration null order changed")
					}
				}
				if sort == "due" && full.Tasks[6].DueDate != "" {
					t.Fatal("due null is not last")
				}
				if sort == "estimate" && full.Tasks[6].Estimate != nil {
					t.Fatal("estimate null is not last")
				}
				if sort == "project" && full.Tasks[6].ProjectID != "" {
					t.Fatal("project null is not last")
				}
			})
		}
	}
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, path+"?sort=created&order=asc", nil)
}

func TestTaskDisplayViewPersistencePermissionsAndConflict(t *testing.T) {
	env := newS3Env(t)
	owner, manager := env.owner(), env.manager()
	type displayView struct {
		ID      string         `json:"id"`
		Version uint64         `json:"version"`
		Display map[string]any `json:"display"`
	}
	display := map[string]any{"layout": "list", "sort": "created", "sortDirection": "asc",
		"groupOrder": map[string]any{"status": "reverse", "assignee": "default"}, "existing": "preserved", "completed": "last_7_days"}
	var view displayView
	owner.must(http.StatusCreated, http.MethodPost, spacePath(env.space.ID)+"/views",
		map[string]any{"name": "Ordered view", "visibility": "space", "display": display}, &view)
	path := "views/" + view.ID
	var fetched displayView
	env.member().must(http.StatusOK, http.MethodGet, path, nil, &fetched)
	if !reflect.DeepEqual(fetched.Display, display) {
		t.Fatalf("display not persisted: %+v", fetched.Display)
	}
	patch := map[string]any{"version": view.Version, "display": display}
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, path, patch)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, path, nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPatch, path, patch)
	for _, bad := range []map[string]any{{"completed": "week"}, {"completed": nil}, {"sortDirection": "bad"}, {"sortDirection": nil}, {"groupOrder": []string{}}, {"groupOrder": map[string]any{"status": "abc"}}} {
		owner.expectError(http.StatusBadRequest, "", http.MethodPatch, path, map[string]any{"version": view.Version, "display": bad})
	}
	statuses := parallel(2, func(index int) int {
		client := owner
		if index == 1 {
			client = manager
		}
		return client.do(http.MethodPatch, path, patch).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 1 {
		t.Fatalf("concurrent updates = %v", statuses)
	}
	owner.must(http.StatusOK, http.MethodGet, path, nil, &fetched)
	if fetched.Version != view.Version+1 || !reflect.DeepEqual(fetched.Display, display) {
		t.Fatalf("post-conflict view = %+v", fetched)
	}
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path, patch)
	// The existing replacement contract remains: old clients may save their original display shape.
	legacy := map[string]any{"layout": "board", "sort": "priority"}
	version := fetched.Version
	fetched = displayView{}
	owner.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": version, "display": legacy}, &fetched)
	if !reflect.DeepEqual(fetched.Display, legacy) {
		t.Fatalf("legacy display changed: %+v", fetched.Display)
	}
}
