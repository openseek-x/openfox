package stage3

import (
	"net/http"
	"reflect"
	"testing"
)

// V04: zero is an estimated value, while nil remains unestimated, including
// persisted pre-T03 shared views after estimation is disabled and re-enabled.
func TestHiFoxEstimationZeroAndLegacyView(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	space := e.space.ID
	if r := estPatch(e, o, space, map[string]any{"scale": "storyPoints", "allow_zero": true}); r.Status != http.StatusOK {
		t.Fatalf("enable: %d %s", r.Status, r.Body)
	}
	zero := e.createTask(o, space, map[string]any{"estimate": 0})
	none := e.createTask(o, space, map[string]any{"estimate": nil})
	positive := e.createTask(o, space, map[string]any{"estimate": 2.5})
	for _, check := range []struct {
		query string
		ids   []string
	}{
		{"estimate_min=0&estimate_max=0", []string{zero.ID}},
		{"estimate_none=true", []string{none.ID}},
		{"estimate_none=true&estimate_min=0&estimate_max=0&sort=estimate&order=asc", []string{zero.ID, none.ID}},
		{"sort=estimate&order=asc", []string{zero.ID, positive.ID, none.ID}},
		{"sort=estimate&order=desc", []string{positive.ID, zero.ID, none.ID}},
	} {
		var page struct {
			Tasks []TaskView `json:"tasks"`
		}
		e.member().must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?"+check.query, nil, &page)
		ids := make([]string, len(page.Tasks))
		for i, task := range page.Tasks {
			ids[i] = task.ID
		}
		if !reflect.DeepEqual(ids, check.ids) {
			t.Fatalf("%s: got %v want %v", check.query, ids, check.ids)
		}
		var counts struct {
			Total int `json:"total"`
		}
		e.member().must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks/counts?"+check.query, nil, &counts)
		if counts.Total != len(check.ids) {
			t.Fatalf("%s count=%d", check.query, counts.Total)
		}
	}
	type savedView struct {
		ID      string         `json:"id"`
		Filters map[string]any `json:"filters"`
		Display map[string]any `json:"display"`
		Version int            `json:"version"`
	}
	var view savedView
	o.must(http.StatusCreated, http.MethodPost, spacePath(space)+"/views", map[string]any{
		"name": "Legacy zero estimate view", "visibility": "space",
		"filters": map[string]any{"estimate_min": []string{"0"}, "estimate_max": []string{"0"}},
		// Old display blobs omit T01 sortDirection, T02 completed, and T03 defaults.
		"display": map[string]any{"layout": "list", "sort": "estimate", "properties": []string{"estimate"}},
	}, &view)
	setDefault(e, space, view.ID)
	for _, scale := range []string{"notUsed", "storyPoints"} {
		if r := estPatch(e, o, space, map[string]any{"scale": scale}); r.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", scale, r.Status, r.Body)
		}
		var loaded savedView
		e.member().must(http.StatusOK, http.MethodGet, "views/"+view.ID, nil, &loaded)
		if !reflect.DeepEqual(loaded, view) {
			t.Fatalf("%s changed saved view: %#v != %#v", scale, loaded, view)
		}
		state := defaultState(e, space)
		if state.DefaultViewID == nil || *state.DefaultViewID != view.ID {
			t.Fatalf("%s lost default source", scale)
		}
		if estValue(estTask(e, space, zero.ID)) != 0 || estTask(e, space, none.ID).Estimate != nil || estValue(estTask(e, space, positive.ID)) != 2.5 {
			t.Fatalf("%s changed stored estimates", scale)
		}
		var page struct {
			Tasks []TaskView `json:"tasks"`
		}
		e.member().must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?estimate_min=0&estimate_max=0", nil, &page)
		if len(page.Tasks) != 1 || page.Tasks[0].ID != zero.ID {
			t.Fatalf("%s cannot load old filter: %+v", scale, page.Tasks)
		}
		if scale == "notUsed" {
			current := estTask(e, space, zero.ID)
			o.expectError(http.StatusBadRequest, "ESTIMATION_DISABLED", http.MethodPatch, spacePath(space)+"/tasks/"+zero.ID, map[string]any{"version": current.Version, "estimate": 1})
		}
	}
	// Read access to the shared source never broadens the private space boundary.
	e.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+view.ID, nil)
	if r := e.outsider().doIn(e.otherOrg, http.MethodGet, "views/"+view.ID, nil); r.Status != http.StatusNotFound {
		t.Fatalf("cross tenant view: %d %s", r.Status, r.Body)
	}
}

func TestHiFoxEstimationZeroAndUnestimatedAggregates(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	space := e.space.ID
	if r := patchIterSettings(e, o, space, map[string]any{"enabled": true, "mode": "manual"}); r.Status != http.StatusOK {
		t.Fatalf("iteration settings: %d %s", r.Status, r.Body)
	}
	iteration := createIteration(e, space, "2030-01-07", "2030-01-20")
	if r := estPatch(e, o, space, map[string]any{"scale": "storyPoints", "allow_zero": true, "count_unestimated": false}); r.Status != http.StatusOK {
		t.Fatalf("estimation: %d %s", r.Status, r.Body)
	}
	for _, item := range []struct {
		estimate any
		category string
	}{{0, "DONE"}, {nil, "DONE"}, {2.5, "TODO"}} {
		e.createTask(o, space, map[string]any{"estimate": item.estimate, "status_id": e.statusID(e.org, item.category), "iteration_id": iteration.ID})
	}
	for _, check := range []struct {
		count       bool
		total, done float64
	}{{false, 2.5, 0}, {true, 3.5, 1}, {false, 2.5, 0}} {
		if r := estPatch(e, o, space, map[string]any{"scale": "storyPoints", "allow_zero": true, "count_unestimated": check.count}); r.Status != http.StatusOK {
			t.Fatalf("count unestimated: %d %s", r.Status, r.Body)
		}
		var response struct {
			Live struct {
				ScopeCount      int     `json:"scope_count"`
				CompletedCount  int     `json:"completed_count"`
				ScopePoints     float64 `json:"scope_points"`
				CompletedPoints float64 `json:"completed_points"`
			} `json:"live"`
		}
		e.member().must(http.StatusOK, http.MethodGet, iterPath(space)+"/"+iteration.ID, nil, &response)
		if response.Live.ScopeCount != 3 || response.Live.CompletedCount != 2 || response.Live.ScopePoints != check.total || response.Live.CompletedPoints != check.done {
			t.Fatalf("count_unestimated=%v: %+v", check.count, response.Live)
		}
	}
}
