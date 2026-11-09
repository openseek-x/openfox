package stage1

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
)

// 标签与标签分组：权限、跨租户、合并、批量、删除分组、幂等创建（含并发）。
func TestConfigLabelsAndGroups(t *testing.T) {
	env := newCfgEnv(t)
	space := env.createSpace("shared")

	// 读：任意组织成员；写：OWNER / ADMIN。
	env.member.must(http.StatusOK, http.MethodGet, "labels", nil, nil)
	env.member.must(http.StatusOK, http.MethodGet, "label-groups", nil, nil)
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "labels", map[string]any{"name": "nope"})
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "label-groups", map[string]any{"name": "g", "color": "#112233"})

	// 跨租户：非成员访问别的组织 → 404；用本组织路径访问别组织的资源 → 404。
	env.outsider.expectError(http.StatusNotFound, "", http.MethodGet, "labels", nil)
	foreign := struct {
		ID string `json:"id"`
	}{}
	env.outsider.doIn(env.otherOrg, http.MethodPost, "labels", map[string]any{"name": "foreign"}).decode(t, &foreign)
	if foreign.ID == "" {
		t.Fatal("outsider could not create a label in their own organization")
	}
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, "labels/"+foreign.ID,
		map[string]any{"name": "steal", "version": 1})
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "labels/"+foreign.ID, nil)
	var foreignName string
	env.scalar(&foreignName, `SELECT name FROM openfox.labels WHERE id = $1::uuid`, foreign.ID)
	if foreignName != "foreign" {
		t.Fatalf("foreign label changed to %q", foreignName)
	}

	// 重名 → 409 NAME_TAKEN（大小写不敏感）；颜色校验 → 400。
	first := env.createLabel(map[string]any{"name": "Bug", "color": "#ff0000"})
	env.owner.expectError(http.StatusConflict, "NAME_TAKEN", http.MethodPost, "labels", map[string]any{"name": "bug"})
	env.owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "labels",
		map[string]any{"name": "x", "color": "red"})
	if first.SpaceID != nil || first.GroupID != nil || first.ArchivedAt != nil || first.LastAppliedAt != nil || first.TaskCount != 0 {
		t.Fatalf("new organization label = %+v", first)
	}

	// 分组：创建时带 label_ids 把标签移入；删除分组把组内标签移出（version + 1）。
	second := env.createLabel(map[string]any{})
	var group struct {
		ID         string `json:"id"`
		LabelCount int    `json:"label_count"`
		Version    int    `json:"version"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, "label-groups",
		map[string]any{"name": "Area", "color": "#123456", "label_ids": []string{first.ID, second.ID}}, &group)
	if group.LabelCount != 2 {
		t.Fatalf("group label_count = %d", group.LabelCount)
	}
	env.owner.expectError(http.StatusConflict, "NAME_TAKEN", http.MethodPost, "label-groups",
		map[string]any{"name": "area", "color": "#123456"})
	// 空间标签不能进分组。
	spaceLabel := env.createLabel(map[string]any{"space_id": space.ID})
	if spaceLabel.SpaceID == nil || *spaceLabel.SpaceID != space.ID {
		t.Fatalf("space label = %+v", spaceLabel)
	}
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "label-groups",
		map[string]any{"name": "Bad", "color": "#123456", "label_ids": []string{spaceLabel.ID}})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPatch, "labels/"+spaceLabel.ID,
		map[string]any{"group_id": group.ID, "version": spaceLabel.Version})
	// 版本冲突。
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "label-groups/"+group.ID,
		map[string]any{"name": "Area 2", "version": group.Version + 5})

	var removed struct {
		RemovedLabelIDs []string `json:"removed_label_ids"`
	}
	env.member.expectError(http.StatusForbidden, "", http.MethodDelete, "label-groups/"+group.ID, nil)
	env.owner.must(http.StatusOK, http.MethodDelete, "label-groups/"+group.ID, nil, &removed)
	if len(removed.RemovedLabelIDs) != 2 {
		t.Fatalf("removed_label_ids = %v", removed.RemovedLabelIDs)
	}
	var groupID *string
	var version int
	env.scalar(&groupID, `SELECT group_id::text FROM openfox.labels WHERE id = $1::uuid`, first.ID)
	env.scalar(&version, `SELECT version FROM openfox.labels WHERE id = $1::uuid`, first.ID)
	if groupID != nil || version != first.Version+2 {
		t.Fatalf("after group delete group_id=%v version=%d (created %d)", groupID, version, first.Version)
	}
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "label-groups/"+group.ID, nil)

	// PATCH group_id：未传不变；显式 null 移出分组。
	var regroup struct {
		ID string `json:"id"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, "label-groups",
		map[string]any{"name": "Area again", "color": "#654321", "label_ids": []string{first.ID}}, &regroup)
	var current cfgLabel
	env.owner.must(http.StatusOK, http.MethodPatch, "labels/"+first.ID,
		map[string]any{"description": "kept in group", "version": version + 1}, &current)
	if current.GroupID == nil || *current.GroupID != regroup.ID {
		t.Fatalf("group_id after unrelated patch = %v", current.GroupID)
	}
	env.owner.must(http.StatusOK, http.MethodPatch, "labels/"+first.ID,
		`{"group_id":null,"version":`+strconv.Itoa(current.Version)+`}`, &current)
	if current.GroupID != nil {
		t.Fatalf("group_id after explicit null = %v", *current.GroupID)
	}

	// 合并：源的任务关联转移到目标（重复关联忽略），源被删除；task_count 只算未删除任务。
	taskA := env.createTask(space, "")
	taskB := env.createTask(space, "")
	taskDeleted := env.createTask(space, "")
	env.attachLabel(space, taskA, second.ID)
	env.attachLabel(space, taskB, second.ID)
	env.attachLabel(space, taskB, first.ID)
	env.attachLabel(space, taskDeleted, first.ID)
	env.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE id = $1::uuid`,
		taskDeleted, cfgOwnerID)
	var merged cfgLabel
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "labels/"+second.ID+"/merge",
		map[string]any{"target_label_id": spaceLabel.ID})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "labels/"+second.ID+"/merge",
		map[string]any{"target_label_id": second.ID})
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "labels/"+second.ID+"/merge",
		map[string]any{"target_label_id": first.ID})
	env.owner.must(http.StatusOK, http.MethodPost, "labels/"+second.ID+"/merge",
		map[string]any{"target_label_id": first.ID}, &merged)
	if merged.ID != first.ID || merged.TaskCount != 2 || merged.LastAppliedAt == nil {
		t.Fatalf("merged target = %+v", merged)
	}
	var sourceLeft, links int
	env.scalar(&sourceLeft, `SELECT count(*) FROM openfox.labels WHERE id = $1::uuid`, second.ID)
	env.scalar(&links, `SELECT count(*) FROM openfox.task_labels WHERE label_id = $1::uuid`, first.ID)
	if sourceLeft != 0 || links != 3 {
		t.Fatalf("after merge source rows=%d target links=%d", sourceLeft, links)
	}

	// 批量：ARCHIVE / UNARCHIVE / MERGE / DELETE，只作用于组织级标签。
	bulkA := env.createLabel(map[string]any{})
	bulkB := env.createLabel(map[string]any{})
	bulkC := env.createLabel(map[string]any{})
	env.attachLabel(space, taskA, bulkB.ID)
	bulk := func(body map[string]any) int {
		t.Helper()
		var result struct {
			Affected int `json:"affected"`
		}
		env.owner.must(http.StatusOK, http.MethodPost, "labels/bulk", body, &result)
		return result.Affected
	}
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "ARCHIVE", "label_ids": []string{bulkA.ID}})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "ARCHIVE", "label_ids": []string{}})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "EXPLODE", "label_ids": []string{bulkA.ID}})
	if got := bulk(map[string]any{"action": "ARCHIVE", "label_ids": []string{bulkA.ID, bulkB.ID, spaceLabel.ID}}); got != 2 {
		t.Fatalf("bulk archive affected = %d", got)
	}
	var archived struct {
		Labels []cfgLabel `json:"labels"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, "labels?scope=archived", nil, &archived)
	if len(archived.Labels) != 2 || archived.Labels[0].ArchivedAt == nil {
		t.Fatalf("archived labels = %+v", archived.Labels)
	}
	// 已归档的不能作为合并目标。
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "MERGE", "label_ids": []string{bulkC.ID}, "target_label_id": bulkA.ID})
	if got := bulk(map[string]any{"action": "UNARCHIVE", "label_ids": []string{bulkA.ID, bulkB.ID}}); got != 2 {
		t.Fatalf("bulk unarchive affected = %d", got)
	}
	if got := bulk(map[string]any{"action": "MERGE", "label_ids": []string{bulkB.ID, bulkC.ID, bulkA.ID}, "target_label_id": bulkA.ID}); got != 2 {
		t.Fatalf("bulk merge affected = %d", got)
	}
	env.scalar(&links, `SELECT count(*) FROM openfox.task_labels WHERE label_id = $1::uuid`, bulkA.ID)
	if links != 1 {
		t.Fatalf("bulk merge target links = %d", links)
	}
	if got := bulk(map[string]any{"action": "DELETE", "label_ids": []string{bulkA.ID, spaceLabel.ID}}); got != 1 {
		t.Fatalf("bulk delete affected = %d", got)
	}
	env.scalar(&links, `SELECT count(*) FROM openfox.labels WHERE id = ANY($1::uuid[])`, []string{bulkA.ID, bulkB.ID, bulkC.ID})
	if links != 0 {
		t.Fatalf("labels left after bulk merge/delete = %d", links)
	}

	// picker：组织级未归档 + 该空间标签；成员需要能读该空间。
	var picker struct {
		Labels []cfgLabel `json:"labels"`
	}
	env.member.must(http.StatusOK, http.MethodGet, "labels?scope=picker&space_id="+space.ID, nil, &picker)
	if !hasLabel(picker.Labels, spaceLabel.ID) || !hasLabel(picker.Labels, first.ID) {
		t.Fatalf("picker labels = %+v", picker.Labels)
	}
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, "labels?scope=picker", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, "labels?scope=everything", nil)

	// 删除标签：任务关联一起删除。
	env.owner.must(http.StatusNoContent, http.MethodDelete, "labels/"+first.ID, nil, nil)
	env.scalar(&links, `SELECT count(*) FROM openfox.task_labels WHERE label_id = $1::uuid`, first.ID)
	if links != 0 {
		t.Fatalf("task_labels left after delete = %d", links)
	}

	// 审计：配置写操作都留下审计事件（按动作计数，只断言存在）。
	for _, action := range []string{"label.created", "label.updated", "label.deleted", "label.merged", "label.bulk",
		"label_group.created", "label_group.deleted"} {
		var events int
		env.scalar(&events, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = $2`, env.org, action)
		if events == 0 {
			t.Fatalf("no audit event for %s", action)
		}
	}
}

// 同一幂等键：并发创建只产生一条（其余为 200 重放）；不同请求体 → 409 IDEMPOTENCY_CONFLICT。
func TestConfigIdempotentCreate(t *testing.T) {
	env := newCfgEnv(t)
	key := "cfg-label-" + cfgSuffix(t)
	name := "concurrent-" + cfgSuffix(t)
	const workers = 8
	var wg sync.WaitGroup
	results := make([]cfgResponse, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = env.owner.do(http.MethodPost, "labels", map[string]any{"name": name}, "Idempotency-Key", key)
		}(index)
	}
	wg.Wait()
	created, replayed := 0, 0
	ids := map[string]bool{}
	for _, response := range results {
		var label cfgLabel
		switch response.Status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			if response.Header.Get("Idempotent-Replayed") != "true" {
				t.Fatalf("replay without header: %v", response.Header)
			}
			replayed++
		default:
			t.Fatalf("concurrent create = %d %s", response.Status, response.Body)
		}
		response.decode(t, &label)
		ids[label.ID] = true
	}
	if created != 1 || replayed != workers-1 || len(ids) != 1 {
		t.Fatalf("created=%d replayed=%d ids=%v", created, replayed, ids)
	}
	var count int
	env.scalar(&count, `SELECT count(*) FROM openfox.labels WHERE organization_id = $1::uuid AND name = $2`, env.org, name)
	if count != 1 {
		t.Fatalf("labels created = %d", count)
	}
	env.owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, "labels",
		map[string]any{"name": name + "-other"}, "Idempotency-Key", key)
	env.owner.expectError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", http.MethodPost, "labels",
		map[string]any{"name": "short-key"}, "Idempotency-Key", "short")

	// 其他创建接口同样接入幂等。
	for _, item := range []struct {
		path string
		body map[string]any
	}{
		{"label-groups", map[string]any{"name": "G " + cfgSuffix(t), "color": "#abcdef"}},
		{"task-statuses", map[string]any{"name": "S " + cfgSuffix(t), "category": "TODO"}},
		{"task-relation-types", map[string]any{"name": "R " + cfgSuffix(t), "outward_label": "out", "inward_label": "in"}},
		{"custom-fields", map[string]any{"name": "F " + cfgSuffix(t), "kind": "TEXT"}},
	} {
		key := "cfg-" + cfgSuffix(t)
		var first, second struct {
			ID string `json:"id"`
		}
		env.owner.must(http.StatusCreated, http.MethodPost, item.path, item.body, &first, "Idempotency-Key", key)
		replay := env.owner.must(http.StatusOK, http.MethodPost, item.path, item.body, &second, "Idempotency-Key", key)
		if first.ID == "" || first.ID != second.ID || replay.Header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("%s replay ids %q %q", item.path, first.ID, second.ID)
		}
		item.body["name"] = "changed " + cfgSuffix(t)
		env.owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, item.path, item.body, "Idempotency-Key", key)
	}
}

func hasLabel(labels []cfgLabel, id string) bool {
	for _, label := range labels {
		if label.ID == id {
			return true
		}
	}
	return false
}
