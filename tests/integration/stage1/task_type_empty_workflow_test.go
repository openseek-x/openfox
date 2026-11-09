package stage1

import (
	"net/http"
	"testing"

	"openfox/internal/collaboration"
)

func TestTaskTypeEmptyWorkflowArray(t *testing.T) {
	env := newCfgEnv(t)
	space := env.createSpace("shared")
	typeID := cfgNewID(t)
	env.exec(`INSERT INTO openfox.task_types (organization_id, space_id, id, name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'Empty workflow')`, env.org, space.ID, typeID)
	var response struct {
		TaskTypes []collaboration.TaskType `json:"task_types"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, "spaces/"+space.ID+"/task-types", nil, &response)
	foundEmpty, foundDefault := false, false
	for _, taskType := range response.TaskTypes {
		if taskType.ID == typeID {
			foundEmpty = true
			if taskType.Statuses == nil || len(taskType.Statuses) != 0 {
				t.Fatalf("empty workflow must serialize as [], got %#v", taskType.Statuses)
			}
		}
		if taskType.ID == space.TypeID {
			foundDefault = true
			if len(taskType.Statuses) == 0 {
				t.Fatal("default workflow lost its statuses")
			}
		}
	}
	if !foundEmpty || !foundDefault {
		t.Fatalf("missing task types: empty=%t default=%t", foundEmpty, foundDefault)
	}
}
