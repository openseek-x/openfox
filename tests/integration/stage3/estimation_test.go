package stage3

import (
	"net/http"
	"testing"
)

func estPatch(env *s3Env, client *s3Client, spaceID string, body map[string]any) s3Response {
	env.t.Helper()
	if _, ok := body["version"]; !ok {
		body["version"] = env.getSpace(env.owner(), spaceID).Version
	}
	return client.do(http.MethodPatch, spacePath(spaceID)+"/estimation", body)
}

func estTask(env *s3Env, spaceID, taskID string) TaskView {
	env.t.Helper()
	var task TaskView
	env.owner().must(http.StatusOK, http.MethodGet, spacePath(spaceID)+"/tasks/"+taskID, nil, &task)
	return task
}

func estValue(task TaskView) float64 {
	if task.Estimate == nil {
		return -1
	}
	return *task.Estimate
}

func TestEstimationRules(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space

	// 未启用：带估时建任务 → ESTIMATION_DISABLED；null 允许。
	owner.expectError(http.StatusBadRequest, "ESTIMATION_DISABLED", http.MethodPost, spacePath(space.ID)+"/tasks",
		map[string]any{"title": "x", "estimate": 3})
	plain := env.createTask(owner, space.ID, map[string]any{"estimate": nil})
	if plain.Estimate != nil {
		t.Fatalf("estimate = %v", *plain.Estimate)
	}

	// 权限：普通成员 403、非成员 404、跨租户 404。
	if response := estPatch(env, env.member(), space.ID, map[string]any{"scale": "fibonacci"}); response.Status != http.StatusForbidden {
		t.Fatalf("member = %d %s", response.Status, response.Body)
	}
	if response := estPatch(env, env.other(), space.ID, map[string]any{"scale": "fibonacci"}); response.Status != http.StatusNotFound {
		t.Fatalf("non-member = %d %s", response.Status, response.Body)
	}
	if response := env.outsider().doIn(env.otherOrg, http.MethodPatch, spacePath(space.ID)+"/estimation",
		map[string]any{"version": 1, "scale": "fibonacci"}); response.Status != http.StatusNotFound {
		t.Fatalf("cross-tenant = %d %s", response.Status, response.Body)
	}
	if response := estPatch(env, env.manager(), space.ID, map[string]any{"scale": "bogus"}); response.Status != http.StatusBadRequest {
		t.Fatalf("bad scale = %d %s", response.Status, response.Body)
	}

	// fibonacci：1,2,3,5,8 合法；4、0、13（未扩展）不合法。
	var updated struct {
		Space          SpaceView `json:"space"`
		ConvertedTasks int       `json:"converted_tasks"`
	}
	estPatch(env, env.manager(), space.ID, map[string]any{"scale": "fibonacci"}).decode(t, &updated)
	if updated.Space.Estimation.Scale != "fibonacci" || updated.ConvertedTasks != 0 {
		t.Fatalf("updated = %+v", updated)
	}
	five := env.createTask(owner, space.ID, map[string]any{"estimate": 5})
	if estValue(five) != 5 {
		t.Fatalf("estimate = %v", estValue(five))
	}
	for _, bad := range []float64{4, 0, 13, 2.5} {
		owner.expectError(http.StatusBadRequest, "INVALID_ESTIMATE", http.MethodPost, spacePath(space.ID)+"/tasks",
			map[string]any{"title": "x", "estimate": bad})
	}
	// 乐观锁：旧 version → 409。
	if response := estPatch(env, env.manager(), space.ID, map[string]any{"version": updated.Space.Version - 1, "scale": "linear"}); response.Status != http.StatusConflict || response.code() != "VERSION_CONFLICT" {
		t.Fatalf("stale = %d %s", response.Status, response.Body)
	}
	// extended + allow_zero：13 与 0 合法。
	estPatch(env, env.manager(), space.ID, map[string]any{"scale": "fibonacci", "extended": true, "allow_zero": true}).decode(t, &updated)
	zero := env.createTask(owner, space.ID, map[string]any{"estimate": 0})
	thirteen := env.createTask(owner, space.ID, map[string]any{"estimate": 13})
	// 更新：设置、清空、非法值；动态记录。
	current := estTask(env, space.ID, plain.ID)
	var patched TaskView
	owner.must(http.StatusOK, http.MethodPatch, spacePath(space.ID)+"/tasks/"+plain.ID,
		map[string]any{"version": current.Version, "estimate": 8}, &patched)
	if estValue(patched) != 8 {
		t.Fatalf("patched estimate = %v", estValue(patched))
	}
	owner.expectError(http.StatusBadRequest, "INVALID_ESTIMATE", http.MethodPatch, spacePath(space.ID)+"/tasks/"+plain.ID,
		map[string]any{"version": patched.Version, "estimate": 7})
	if count := env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.estimate_changed'`, plain.ID); count != 1 {
		t.Fatalf("estimate activities = %d", count)
	}

	// 预览（目标 linear）：按值分组计数、合法性、建议值。
	var preview struct {
		Scale         string    `json:"scale"`
		AllowedValues []float64 `json:"allowed_values"`
		Values        []struct {
			Value     float64  `json:"value"`
			Count     int      `json:"count"`
			Valid     bool     `json:"valid"`
			Suggested *float64 `json:"suggested"`
		} `json:"values"`
		UnestimatedCount int `json:"unestimated_count"`
		TotalCount       int `json:"total_count"`
	}
	env.member().must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/estimation/preview?scale=linear", nil, &preview)
	if preview.TotalCount != 4 || preview.UnestimatedCount != 0 || len(preview.Values) != 4 || len(preview.AllowedValues) != 5 {
		t.Fatalf("preview = %+v", preview)
	}
	for _, value := range preview.Values {
		switch value.Value {
		case 5:
			if !value.Valid || value.Suggested == nil || *value.Suggested != 5 {
				t.Fatalf("preview 5 = %+v", value)
			}
		case 13:
			if value.Valid || value.Suggested == nil || *value.Suggested != 5 {
				t.Fatalf("preview 13 = %+v", value)
			}
		}
	}
	env.member().expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/estimation/preview?scale=bogus", nil)

	// 切换到 linear 并转换：13→5、0→null、8→4；已删除任务与其他空间不受影响；from 重复 / to 不合法 → 400。
	other := env.createSpace("private")
	estPatch(env, owner, other.ID, map[string]any{"scale": "fibonacci", "extended": true}).decode(t, &updated)
	otherTask := env.createTask(owner, other.ID, map[string]any{"estimate": 13})
	deleted := env.createTask(owner, space.ID, map[string]any{"estimate": 13})
	owner.must(http.StatusNoContent, http.MethodDelete, spacePath(space.ID)+"/tasks/"+deleted.ID+"?version=1", nil, nil)
	if response := estPatch(env, owner, space.ID, map[string]any{"scale": "linear",
		"convert": []map[string]any{{"from": 13, "to": 5}, {"from": 13, "to": 4}}}); response.Status != http.StatusBadRequest {
		t.Fatalf("duplicate from = %d %s", response.Status, response.Body)
	}
	if response := estPatch(env, owner, space.ID, map[string]any{"scale": "linear",
		"convert": []map[string]any{{"from": 13, "to": 9}}}); response.Code() != "INVALID_ESTIMATE" {
		t.Fatalf("invalid to = %d %s", response.Status, response.Body)
	}
	estPatch(env, owner, space.ID, map[string]any{"scale": "linear",
		"convert": []map[string]any{{"from": 13, "to": 5}, {"from": 0, "to": nil}, {"from": 8, "to": 4}, {"from": 5, "to": 3}}}).decode(t, &updated)
	// 映射同时生效：5→3 不会把 13→5 的结果再转成 3。
	if updated.ConvertedTasks != 4 || updated.Space.Estimation.Scale != "linear" || updated.Space.Estimation.AllowZero {
		t.Fatalf("convert = %+v", updated)
	}
	for _, check := range []struct {
		id   string
		want float64
	}{{thirteen.ID, 5}, {zero.ID, -1}, {plain.ID, 4}, {five.ID, 3}} {
		if got := estValue(estTask(env, space.ID, check.id)); got != check.want {
			t.Fatalf("task %s estimate = %v, want %v", check.id, got, check.want)
		}
	}
	var deletedEstimate float64
	env.scalar(&deletedEstimate, `SELECT estimate FROM openfox.tasks WHERE id = $1::uuid`, deleted.ID)
	if deletedEstimate != 13 || estValue(estTask(env, other.ID, otherTask.ID)) != 13 {
		t.Fatalf("deleted / other space task converted")
	}

	// storyPoints：任意非负、最多两位小数；time：整数秒。
	estPatch(env, owner, space.ID, map[string]any{"scale": "storyPoints"}).decode(t, &updated)
	env.createTask(owner, space.ID, map[string]any{"estimate": 2.75})
	owner.expectError(http.StatusBadRequest, "INVALID_ESTIMATE", http.MethodPost, spacePath(space.ID)+"/tasks",
		map[string]any{"title": "x", "estimate": 1.234})
	owner.expectError(http.StatusBadRequest, "INVALID_ESTIMATE", http.MethodPost, spacePath(space.ID)+"/tasks",
		map[string]any{"title": "x", "estimate": -1})
	estPatch(env, owner, space.ID, map[string]any{"scale": "time"}).decode(t, &updated)
	env.createTask(owner, space.ID, map[string]any{"estimate": 3600})
	owner.expectError(http.StatusBadRequest, "INVALID_ESTIMATE", http.MethodPost, spacePath(space.ID)+"/tasks",
		map[string]any{"title": "x", "estimate": 1.5})
	// 停用估时后：已有值保留，新值拒绝。
	estPatch(env, owner, space.ID, map[string]any{"scale": "notUsed"}).decode(t, &updated)
	owner.expectError(http.StatusBadRequest, "ESTIMATION_DISABLED", http.MethodPost, spacePath(space.ID)+"/tasks",
		map[string]any{"title": "x", "estimate": 1})
}

func TestEstimationFilterAndSort(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space
	estPatch(env, owner, space.ID, map[string]any{"scale": "storyPoints"})
	values := []any{3, nil, 1, 8, nil, 5}
	ids := map[float64]string{}
	for _, value := range values {
		task := env.createTask(owner, space.ID, map[string]any{"estimate": value})
		if value != nil {
			ids[float64(value.(int))] = task.ID
		}
	}
	list := func(query string) []TaskView {
		var body struct {
			Tasks      []TaskView `json:"tasks"`
			NextCursor string     `json:"next_cursor"`
		}
		owner.must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/tasks?"+query, nil, &body)
		return body.Tasks
	}
	if got := list("estimate_min=3&estimate_max=5"); len(got) != 2 {
		t.Fatalf("range = %d", len(got))
	}
	if got := list("estimate_none=true"); len(got) != 2 {
		t.Fatalf("none = %d", len(got))
	}
	if got := list("estimate_none=true&estimate_min=8"); len(got) != 3 {
		t.Fatalf("none or >= 8 = %d", len(got))
	}
	var counts struct {
		Total int `json:"total"`
	}
	owner.must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/tasks/counts?estimate_min=2", nil, &counts)
	if counts.Total != 3 {
		t.Fatalf("counts = %d", counts.Total)
	}
	owner.expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/tasks?estimate_min=-1", nil)
	owner.expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/tasks?estimate_min=abc", nil)

	// 排序：升序 1,3,5,8，空值最后；降序 8,5,3,1，空值仍最后；分页（每页 2）结果与一次取完一致。
	order := func(sortOrder string, limit int) []float64 {
		result := []float64{}
		cursor := ""
		for page := 0; page < 10; page++ {
			var body struct {
				Tasks      []TaskView `json:"tasks"`
				NextCursor string     `json:"next_cursor"`
			}
			path := spacePath(space.ID) + "/tasks?sort=estimate&order=" + sortOrder + "&limit=" + itoa(limit)
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			owner.must(http.StatusOK, http.MethodGet, path, nil, &body)
			for _, task := range body.Tasks {
				result = append(result, estValue(task))
			}
			if body.NextCursor == "" {
				break
			}
			cursor = body.NextCursor
		}
		return result
	}
	for _, check := range []struct {
		order string
		want  []float64
	}{{"asc", []float64{1, 3, 5, 8, -1, -1}}, {"desc", []float64{8, 5, 3, 1, -1, -1}}} {
		for _, limit := range []int{50, 2} {
			got := order(check.order, limit)
			if len(got) != len(check.want) {
				t.Fatalf("%s limit %d = %v", check.order, limit, got)
			}
			for index := range got {
				if got[index] != check.want[index] {
					t.Fatalf("%s limit %d = %v", check.order, limit, got)
				}
			}
		}
	}
}

func itoa(value int) string {
	digits := []byte{}
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// Code 与 code 相同（导出给估时用例的链式调用）。
func (r s3Response) Code() string { return r.code() }
