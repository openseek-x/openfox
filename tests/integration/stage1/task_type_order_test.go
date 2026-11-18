package stage1

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"openfox/internal/collaboration"
)

func TestTaskTypeOrderPermissionsVersionsAndConcurrency(t *testing.T) {
	e := newTkEnv(t)
	space := e.createSpace("shared")
	path := "spaces/" + space.ID + "/task-types"
	list := func() []collaboration.TaskType {
		var response struct {
			Types []collaboration.TaskType `json:"task_types"`
		}
		e.owner.must(200, "GET", path, nil, &response)
		return response.Types
	}
	create := func(name string) collaboration.TaskType {
		var item collaboration.TaskType
		e.owner.must(201, "POST", path, map[string]any{"name": name}, &item)
		return item
	}
	payload := func(items []collaboration.TaskType) map[string]any {
		order := make([]collaboration.TaskTypeOrderItem, len(items))
		for i, item := range items {
			order[i] = collaboration.TaskTypeOrderItem{ID: item.ID, Version: item.Version}
		}
		return map[string]any{"types": order}
	}
	ids := func(items []collaboration.TaskType) []string {
		result := make([]string, len(items))
		for i, item := range items {
			result[i] = item.ID
		}
		return result
	}
	archived := create("Archived slot")
	third := create("Third")
	create("Fourth")
	e.owner.must(200, "PATCH", path+"/"+archived.ID, map[string]any{"version": archived.Version, "archived": true}, nil)
	current := list()
	if current[1].ID != archived.ID {
		t.Fatal("creation did not append")
	}
	next := append([]collaboration.TaskType{}, current...)
	next[0], next[3] = next[3], next[0]
	request := payload(next)
	e.member.expectError(403, "", "POST", path+"/reorder", request)
	e.outsider.expectError(404, "", "POST", "/v1/organizations/"+e.otherOrg+"/"+path+"/reorder", request)
	bad := append([]collaboration.TaskType{}, current...)
	bad[0], bad[1] = bad[1], bad[0]
	e.owner.expectError(400, "", "POST", path+"/reorder", payload(bad))
	bad = append([]collaboration.TaskType{}, current...)
	bad[0] = bad[2]
	e.owner.expectError(400, "", "POST", path+"/reorder", payload(bad))
	bad = append([]collaboration.TaskType{}, current...)
	bad[0].ID = cfgNewID(t)
	e.owner.expectError(409, "VERSION_CONFLICT", "POST", path+"/reorder", payload(bad))
	e.owner.expectError(409, "VERSION_CONFLICT", "POST", path+"/reorder", payload(current[:3]))
	e.owner.must(200, "POST", path+"/reorder", request, nil)
	if !reflect.DeepEqual(ids(list()), ids(next)) {
		t.Fatal("reorder did not persist")
	}
	e.owner.expectError(409, "VERSION_CONFLICT", "POST", path+"/reorder", request)
	for _, item := range list() {
		if item.IsDefault != (item.ID == space.TypeID) {
			t.Fatal("reorder changed default")
		}
	}

	// Same versions from two clients: only one may win.
	current = list()
	current[0], current[2] = current[2], current[0]
	request = payload(current)
	start := make(chan struct{})
	responses := make(chan cfgResponse, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; responses <- e.owner.do("POST", path+"/reorder", request) }()
	}
	close(start)
	wg.Wait()
	close(responses)
	counts := map[int]int{}
	for response := range responses {
		counts[response.Status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent reorder results %v", counts)
	}

	// A new type invalidates the complete-list snapshot, and goes to the tail.
	stale := list()
	added := create("New tail")
	e.owner.expectError(409, "VERSION_CONFLICT", "POST", path+"/reorder", payload(stale))
	current = list()
	if current[len(current)-1].ID != added.ID {
		t.Fatal("new type is not last")
	}
	// Legacy SQL writers omit position. A later normal create must append after them.
	legacy := cfgNewID(t)
	e.exec(`INSERT INTO openfox.task_types (organization_id,space_id,id,name) VALUES ($1::uuid,$2::uuid,$3::uuid,'Legacy null')`, e.org, space.ID, legacy)
	last := create("After legacy")
	current = list()
	if current[len(current)-2].ID != legacy || current[len(current)-1].ID != last.ID {
		t.Fatal("legacy null order changed on append")
	}
	for _, item := range current {
		if item.Position == nil {
			t.Fatal("legacy positions were not normalized")
		}
	}
	// Disabling/restoring a non-default type does not change its position.
	for _, item := range current {
		if item.ID == third.ID {
			e.owner.must(200, "PATCH", path+"/"+item.ID, map[string]any{"version": item.Version, "enabled": false}, nil)
		}
	}
	if !reflect.DeepEqual(ids(current), ids(list())) {
		t.Fatal("disable changed order")
	}
	// An organization workflow writer may already own a type row without the
	// space lock. Reorder must return a retryable conflict, not wait in a cycle.
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `SELECT id FROM openfox.task_types WHERE id=$1::uuid FOR UPDATE`, third.ID); err != nil {
		t.Fatal(err)
	}
	e.owner.expectError(409, "VERSION_CONFLICT", "POST", path+"/reorder", payload(list()))
	if err := tx.Rollback(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.owner.must(200, "POST", path+"/reorder", payload(list()), nil)

	// Concurrent ordinary creates serialize at the space and append distinct slots.
	start = make(chan struct{})
	responses = make(chan cfgResponse, 2)
	for i := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			responses <- e.owner.do("POST", path, map[string]any{"name": fmt.Sprintf("Concurrent %d", index)})
		}(i)
	}
	close(start)
	wg.Wait()
	close(responses)
	for response := range responses {
		if response.Status != 201 {
			t.Fatalf("concurrent create=%+v", response)
		}
	}
	current = list()
	if *current[len(current)-1].Position == *current[len(current)-2].Position {
		t.Fatal("concurrent creates share a slot")
	}
}
