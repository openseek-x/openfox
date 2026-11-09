package stage1

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 任务字段取值：只能写绑定到任务类型的字段；按类型校验；清空；动态带字段名；创建任务时一并写入。
func TestTasksFieldValues(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	createField := func(body map[string]any) cfgField {
		t.Helper()
		var field cfgField
		env.owner.must(http.StatusCreated, http.MethodPost, "custom-fields", body, &field)
		return field
	}
	points := createField(map[string]any{"name": "Points " + cfgSuffix(t), "kind": "NUMBER"})
	severity := createField(map[string]any{"name": "Severity " + cfgSuffix(t), "kind": "SELECT",
		"options": []map[string]any{{"label": "高"}, {"label": "低"}}})
	unbound := createField(map[string]any{"name": "Unbound " + cfgSuffix(t), "kind": "TEXT"})
	for _, field := range []cfgField{points, severity} {
		env.owner.must(http.StatusNoContent, http.MethodPut,
			"spaces/"+space.ID+"/task-types/"+space.TypeID+"/fields/"+field.ID, nil, nil)
	}

	task := env.newTask(env.member, space, map[string]any{
		"field_values": map[string]any{points.ID: 3}})
	base := taskPath(space, task.ID) + "/fields"
	var listed struct {
		Fields []struct {
			Field struct {
				ID string `json:"id"`
			} `json:"field"`
			Value json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	env.member.must(http.StatusOK, http.MethodGet, base, nil, &listed)
	if len(listed.Fields) != 2 || listed.Fields[0].Field.ID != points.ID || string(listed.Fields[0].Value) != "3" {
		t.Fatalf("fields after create = %+v", listed.Fields)
	}

	env.member.must(http.StatusOK, http.MethodPut, base+"/"+severity.ID, map[string]any{"value": severity.Options[0].ID}, nil)
	for _, invalid := range []struct {
		field string
		value any
	}{
		{points.ID, "3"},
		{severity.ID, cfgNewID(t)},
		{severity.ID, []string{severity.Options[0].ID}},
	} {
		env.member.expectError(http.StatusBadRequest, "", http.MethodPut, base+"/"+invalid.field, map[string]any{"value": invalid.value})
	}
	// 没绑定到这个任务类型的字段不能写。
	env.member.expectError(http.StatusBadRequest, "", http.MethodPut, base+"/"+unbound.ID, map[string]any{"value": "x"})

	env.member.must(http.StatusNoContent, http.MethodDelete, base+"/"+points.ID, nil, nil)
	var count int
	env.scalar(&count, `SELECT count(*) FROM openfox.task_field_values WHERE organization_id = $1::uuid AND task_id = $2::uuid`, env.org, task.ID)
	if count != 1 {
		t.Fatalf("field values after clear = %d", count)
	}

	// 动态 task.field_changed 带字段名（创建时写入的取值走同一路径，不单独记动态）。
	var joined string
	env.scalar(&joined, `SELECT string_agg(safe_diff->>'field_name', '|' ORDER BY created_at, id)
		FROM openfox.task_activities
		WHERE organization_id = $1::uuid AND task_id = $2::uuid AND kind = 'task.field_changed'`, env.org, task.ID)
	if got := splitPipe(joined); len(got) < 2 || got[len(got)-2] != severity.Name || got[len(got)-1] != points.Name {
		t.Fatalf("field_changed names = %v", got)
	}
}

func splitPipe(value string) []string {
	var out []string
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] == '|' {
			out = append(out, value[start:i])
			start = i + 1
		}
	}
	return append(out, value[start:])
}
