package stage3

// 分拣台集成测试：设置与权限、新任务进入分拣台、分拣动作、责任动作、规则（条件、动作、顺序、重排、上限）。

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
	"slices"
	"testing"
	"time"
)

type triageTaskView struct {
	ID             string   `json:"id"`
	SpaceID        string   `json:"space_id"`
	StatusID       string   `json:"status_id"`
	StatusCategory string   `json:"status_category"`
	Priority       string   `json:"priority"`
	Version        int      `json:"version"`
	ProjectID      *string  `json:"project_id"`
	Labels         []string `json:"labels"`
	LabelItems     []struct {
		ID string `json:"id"`
	} `json:"label_items"`
	Assignees []struct {
		ID string `json:"id"`
	} `json:"assignees"`
	Triage *struct {
		Source       string  `json:"source"`
		SnoozedUntil *string `json:"snoozed_until"`
		Creator      struct {
			ID string `json:"id"`
		} `json:"creator"`
	} `json:"triage"`
	DuplicateOf *struct {
		ID string `json:"id"`
	} `json:"duplicate_of"`
}

func (v triageTaskView) labelIDs() []string {
	ids := []string{}
	for _, item := range v.LabelItems {
		ids = append(ids, item.ID)
	}
	slices.Sort(ids)
	return ids
}

type triageSettingsView struct {
	Enabled         bool   `json:"enabled"`
	RequirePriority bool   `json:"require_priority"`
	Action          string `json:"action"`
	CanManage       bool   `json:"can_manage"`
	Version         int    `json:"version"`
	Members         []struct {
		ID string `json:"id"`
	} `json:"members"`
}

type triageRuleView struct {
	ID         string  `json:"id"`
	Position   int     `json:"position"`
	Enabled    bool    `json:"enabled"`
	Version    int     `json:"version"`
	LastUsedAt *string `json:"last_used_at"`
}

// triageSpace 给组织补上待分拣 / 重复状态后建一个新空间（工作流按组织状态生成），
// manager 为空间 OWNER，member 为空间 MEMBER，other 为空间 VIEWER，guest 为空间 MEMBER；返回空间与设置。
func triageSpace(t *testing.T, env *s3Env) (SpaceView, triageSettingsView) {
	t.Helper()
	if env.count(`SELECT count(*) FROM openfox.task_statuses WHERE organization_id = $1::uuid AND category = 'TRIAGE'`, env.org) == 0 {
		env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 10), ($1::uuid, 'Duplicate', '', 'DUPLICATE', 11)`, env.org)
	}
	space := env.createSpace("private")
	env.addSpaceMember(space.ID, s3ManagerID, "OWNER")
	env.addSpaceMember(space.ID, s3MemberID, "MEMBER")
	env.addSpaceMember(space.ID, s3OtherID, "VIEWER")
	env.addSpaceMember(space.ID, s3GuestID, "MEMBER")
	return space, triageSettings(t, env, env.manager(), space.ID)
}

func triageSettings(t *testing.T, env *s3Env, client *s3Client, spaceID string) triageSettingsView {
	t.Helper()
	var settings triageSettingsView
	client.must(http.StatusOK, http.MethodGet, "spaces/"+spaceID+"/triage-settings", nil, &settings)
	return settings
}

func patchTriage(t *testing.T, env *s3Env, spaceID string, body map[string]any) triageSettingsView {
	t.Helper()
	body["version"] = triageSettings(t, env, env.manager(), spaceID).Version
	var settings triageSettingsView
	env.manager().must(http.StatusOK, http.MethodPatch, "spaces/"+spaceID+"/triage-settings", body, &settings)
	return settings
}

func triageTask(t *testing.T, client *s3Client, spaceID string, body map[string]any) triageTaskView {
	t.Helper()
	if _, ok := body["submit_to_triage"]; !ok {
		body["submit_to_triage"] = true
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Triage " + s3Suffix(t)
	}
	var task triageTaskView
	client.must(http.StatusCreated, http.MethodPost, "spaces/"+spaceID+"/tasks", body, &task)
	return task
}

func getTriageTask(t *testing.T, client *s3Client, spaceID, taskID string) triageTaskView {
	t.Helper()
	var task triageTaskView
	client.must(http.StatusOK, http.MethodGet, "spaces/"+spaceID+"/tasks/"+taskID, nil, &task)
	return task
}

func TestTriageSettingsAndPermissions(t *testing.T) {
	env := newS3Env(t)
	// 组织还没有待分拣状态时不能启用。
	settings := triageSettings(t, env, env.manager(), env.space.ID)
	env.manager().expectError(http.StatusConflict, "TRIAGE_STATUS_MISSING", http.MethodPatch, "spaces/"+env.space.ID+"/triage-settings",
		map[string]any{"version": settings.Version, "enabled": true})
	env.manager().expectError(http.StatusConflict, "TRIAGE_STATUS_MISSING", http.MethodPatch, "spaces/"+env.space.ID,
		map[string]any{"version": env.getSpace(env.manager(), env.space.ID).Version, "features": map[string]any{"triage": true}})

	space, _ := triageSpace(t, env)
	path := "spaces/" + space.ID + "/triage-settings"
	for _, tc := range []struct {
		name      string
		client    *s3Client
		status    int
		canManage bool
	}{
		{"org owner", env.owner(), 200, true},
		// 组织 ADMIN 能管理私有空间但读不到它。
		{"org admin", env.admin(), 404, false},
		{"space owner", env.manager(), 200, true},
		{"space member", env.member(), 200, false},
		{"space viewer", env.other(), 200, false},
		{"cross tenant", env.outsider(), 404, false},
	} {
		response := tc.client.do(http.MethodGet, path, nil)
		if response.Status != tc.status {
			t.Fatalf("%s: GET settings = %d %s", tc.name, response.Status, response.Body)
		}
		if tc.status == 200 {
			var got triageSettingsView
			response.decode(t, &got)
			if got.CanManage != tc.canManage {
				t.Fatalf("%s: can_manage = %v", tc.name, got.CanManage)
			}
		}
	}
	version := triageSettings(t, env, env.manager(), space.ID).Version
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, path, map[string]any{"version": version, "enabled": true})
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "spaces/"+space.ID+"/triage-rules",
		map[string]any{"name": "x", "conditions": []any{}, "actions": []any{map[string]any{"type": "set_priority", "value": "HIGH"}}})
	env.manager().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, path, map[string]any{"version": version, "action": "shout"})

	got := patchTriage(t, env, space.ID, map[string]any{"enabled": true, "action": "notify"})
	if !got.Enabled || got.Action != "notify" {
		t.Fatalf("settings = %+v", got)
	}
	members := "spaces/" + space.ID + "/triage-members"
	env.manager().expectError(http.StatusBadRequest, "INVALID_TRIAGE_MEMBER", http.MethodPut, members, map[string]any{"user_ids": []string{s3GuestID}})
	env.manager().expectError(http.StatusBadRequest, "INVALID_TRIAGE_MEMBER", http.MethodPut, members, map[string]any{"user_ids": []string{s3MemberID, s3MemberID}})
	env.manager().must(http.StatusOK, http.MethodPut, members, map[string]any{"user_ids": []string{s3MemberID, s3ManagerID}}, nil)
	if got := triageSettings(t, env, env.manager(), space.ID); len(got.Members) != 2 || got.Members[0].ID != s3MemberID {
		t.Fatalf("members = %+v", got.Members)
	}
	// 改为不执行操作时清空责任成员。
	if got := patchTriage(t, env, space.ID, map[string]any{"action": "none"}); len(got.Members) != 0 {
		t.Fatalf("members after none = %+v", got.Members)
	}
	if env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'triage.changed' AND space_id = $2::uuid`, env.org, space.ID) == 0 {
		t.Fatal("triage.changed was not emitted")
	}
	// 停用空间只读。
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED', retired_at = clock_timestamp(), retired_by = $2::uuid WHERE id = $1::uuid`, space.ID, s3OwnerID)
	env.manager().expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch, path,
		map[string]any{"version": triageSettings(t, env, env.manager(), space.ID).Version, "require_priority": true})
}

func TestTriageEntryActionsAndResponsibility(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	patchTriage(t, env, space.ID, map[string]any{"enabled": true, "action": "notify"})
	env.manager().must(http.StatusOK, http.MethodPut, "spaces/"+space.ID+"/triage-members", map[string]any{"user_ids": []string{s3ManagerID, s3MemberID}}, nil)
	todo := env.statusID(env.org, "TODO")
	base := "spaces/" + space.ID

	// 普通成员按常规工作流；显式创建和访客进入队列。
	regular := triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "status_id": todo})
	if regular.Triage != nil || regular.StatusID != todo {
		t.Fatalf("regular task entered triage: %+v", regular)
	}
	fromMember := triageTask(t, env.member(), space.ID, map[string]any{"status_id": todo})
	fromGuest := triageTask(t, env.guest(), space.ID, map[string]any{"submit_to_triage": false})
	subtask := triageTask(t, env.member(), space.ID, map[string]any{"parent_task_id": fromMember.ID})
	for _, tc := range []struct {
		task   triageTaskView
		source string
	}{{fromMember, "member"}, {fromGuest, "guest"}, {subtask, "member"}} {
		if tc.task.StatusCategory != "TRIAGE" || tc.task.Triage == nil || tc.task.Triage.Source != tc.source {
			t.Fatalf("task %s = %+v", tc.task.ID, tc.task)
		}
	}
	notified := func(userID, taskID string) int {
		return env.count(`SELECT count(*) FROM openfox.notifications WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid
			AND task_id = $3::uuid AND kind = 'TRIAGE_NEW'`, env.org, userID, taskID)
	}
	if notified(s3ManagerID, fromMember.ID) != 1 || notified(s3MemberID, fromMember.ID) != 0 || notified(s3MemberID, fromGuest.ID) != 1 {
		t.Fatal("triage notifications are wrong")
	}

	// 队列、计数、稍后处理。
	queue := func(query string) []string {
		var page struct {
			Tasks []triageTaskView `json:"tasks"`
			Total int              `json:"total"`
		}
		env.member().must(http.StatusOK, http.MethodGet, base+"/triage"+query, nil, &page)
		ids := []string{}
		for _, task := range page.Tasks {
			ids = append(ids, task.ID)
		}
		if page.Total != len(ids) {
			t.Fatalf("total = %d, tasks = %d", page.Total, len(ids))
		}
		return ids
	}
	if got := queue(""); len(got) != 3 || got[0] != subtask.ID {
		t.Fatalf("queue = %v", got)
	}
	// 队列复用任务筛选参数。
	if got := queue("?creator_id=" + s3GuestID); len(got) != 1 || got[0] != fromGuest.ID {
		t.Fatalf("filtered queue = %v", got)
	}
	fieldID := s3NewID(t)
	env.exec(`INSERT INTO openfox.custom_fields (organization_id,id,name,kind) VALUES ($1,$2,'Browser','TEXT')`, env.org, fieldID)
	env.exec(`INSERT INTO openfox.task_type_fields (organization_id,space_id,task_type_id,field_id,created_by) SELECT organization_id,space_id,type_id,$2,$3 FROM openfox.tasks WHERE id=$1`, fromMember.ID, fieldID, s3ManagerID)
	env.exec(`INSERT INTO openfox.task_field_values (organization_id,space_id,task_id,field_id,value,updated_by) VALUES ($1,$2,$3,$4,'"Safari"',$5)`, env.org, space.ID, fromMember.ID, fieldID, s3ManagerID)
	env.exec(`INSERT INTO openfox.task_followers (organization_id,space_id,task_id,kind,user_id,created_by) VALUES ($1,$2,$3,'HUMAN',$4,$4) ON CONFLICT DO NOTHING`, env.org, space.ID, fromMember.ID, s3OtherID)
	for query, want := range map[string]string{"relation=parent": fromMember.ID, "relation=subtask": subtask.ID, "subscriber=" + s3OtherID: fromMember.ID, "cf_" + fieldID + "=Safari": fromMember.ID} {
		if got := queue("?" + query); len(got) != 1 || got[0] != want {
			t.Fatalf("%s = %v, want %s", query, got, want)
		}
	}
	if got := queue("?cf_" + fieldID + "_not=Safari"); len(got) != 2 {
		t.Fatalf("negated custom field: %v", got)
	}
	hiddenSpace := env.createSpace("private")
	hidden := triageTask(t, env.owner(), hiddenSpace.ID, map[string]any{"submit_to_triage": false})
	env.exec(`INSERT INTO openfox.task_relations (organization_id,source_space_id,source_task_id,target_space_id,target_task_id,builtin_type,created_by) VALUES ($1,$2,$3,$4,$5,'BLOCKS',$6)`, env.org, hiddenSpace.ID, hidden.ID, space.ID, fromMember.ID, s3OwnerID)
	if got := queue("?relation=blocked"); len(got) != 0 {
		t.Fatalf("private relation leaked: %v", got)
	}
	env.addSpaceMember(hiddenSpace.ID, s3MemberID, "VIEWER")
	if got := queue("?relation=blocked"); len(got) != 1 || got[0] != fromMember.ID {
		t.Fatalf("visible relation: %v", got)
	}
	env.member().expectError(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, base+"/triage?relation=unknown", nil)
	env.member().expectError(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, base+"/triage?cf_bad=1", nil)
	for field, want := range map[string]string{"subscriber": s3OtherID, "priority": "NONE", "creator": s3MemberID, "assignee": "none", "iteration": "none", "project": "none", "type": "", "crew": "", "label": ""} {
		var candidates struct {
			Counts   map[string]int
			Complete bool
		}
		env.member().must(http.StatusOK, http.MethodGet, base+"/triage/candidates?field="+field, nil, &candidates)
		if !candidates.Complete || (want != "" && candidates.Counts[want] == 0) {
			t.Fatalf("%s candidates = %+v", field, candidates)
		}
	}
	var hiddenCounts struct{ Counts map[string]int }
	env.member().must(http.StatusOK, http.MethodGet, base+"/triage/candidates?field=subscriber&creator_id="+s3GuestID, nil, &hiddenCounts)
	if hiddenCounts.Counts[s3OtherID] != 0 {
		t.Fatal("candidate counts ignored other filters")
	}
	if got := queue("?order=asc"); got[0] != fromMember.ID {
		t.Fatalf("ascending queue = %v", got)
	}
	// Equal descending sort values keep ascending identifiers across page boundaries.
	all := queue("?sort=priority&sort_mode=workbench&order=desc")
	if !slices.IsSorted(all) {
		t.Fatalf("workbench ties = %v", all)
	}
	var first, second struct {
		Tasks  []triageTaskView
		Cursor string `json:"next_cursor"`
	}
	workbenchPath := base + "/triage?sort=priority&sort_mode=workbench&order=desc&limit=1"
	env.member().must(http.StatusOK, http.MethodGet, workbenchPath, nil, &first)
	env.member().must(http.StatusOK, http.MethodGet, workbenchPath+"&cursor="+first.Cursor, nil, &second)
	if first.Tasks[0].ID != all[0] || second.Tasks[0].ID != all[1] {
		t.Fatal("workbench cursor skipped or repeated a task")
	}
	snooze := base + "/tasks/" + fromGuest.ID + "/triage-snooze"
	env.member().expectError(http.StatusBadRequest, "INVALID_SNOOZE", http.MethodPut, snooze, map[string]any{"until": time.Now().Add(-time.Hour)})
	env.member().expectError(http.StatusBadRequest, "INVALID_SNOOZE", http.MethodPut, snooze, map[string]any{"until": time.Now().AddDate(2, 0, 0)})
	env.member().must(http.StatusOK, http.MethodPut, snooze, map[string]any{"until": time.Now().Add(time.Hour)}, nil)
	if got := queue(""); slices.Contains(got, fromGuest.ID) || len(queue("?include_snoozed=true")) != 3 {
		t.Fatalf("snoozed task visibility is wrong: %v", got)
	}
	var count struct {
		Total, Snoozed int
		Next           *time.Time `json:"next_snooze_at"`
	}
	env.member().must(http.StatusOK, http.MethodGet, base+"/triage/count", nil, &count)
	if count.Total != 3 || count.Snoozed != 1 || count.Next == nil || !count.Next.After(time.Now()) {
		t.Fatalf("count = %+v", count)
	}
	env.member().must(http.StatusOK, http.MethodPut, snooze, map[string]any{"until": nil}, nil)
	if !slices.Contains(queue(""), fromGuest.ID) {
		t.Fatal("unsnoozed task is hidden")
	}
	env.other().expectError(http.StatusForbidden, "", http.MethodPut, snooze, map[string]any{"until": nil})

	// 接受：要求明确优先级、目标不得仍是待分拣、评论写入、离开分拣台。
	patchTriage(t, env, space.ID, map[string]any{"require_priority": true})
	accept := base + "/tasks/" + fromMember.ID + "/triage/accept"
	current := getTriageTask(t, env.member(), space.ID, fromMember.ID)
	env.member().expectError(http.StatusBadRequest, "PRIORITY_REQUIRED", http.MethodPost, accept, map[string]any{"version": current.Version})
	env.member().expectError(http.StatusBadRequest, "INVALID_STATUS", http.MethodPost, accept,
		map[string]any{"version": current.Version, "priority": "HIGH", "status_id": env.statusID(env.org, "TRIAGE")})
	var accepted triageTaskView
	env.member().must(http.StatusOK, http.MethodPost, accept, map[string]any{"version": current.Version, "priority": "HIGH",
		"status_id": todo, "assignee_ids": []string{s3ManagerID}, "comment": "看过了"}, &accepted)
	if accepted.Triage != nil || accepted.StatusID != todo || accepted.Priority != "HIGH" || len(accepted.Assignees) != 1 {
		t.Fatalf("accepted = %+v", accepted)
	}
	if env.count(`SELECT count(*) FROM openfox.task_comments WHERE task_id = $1::uuid`, fromMember.ID) != 1 ||
		env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND triage_entered_at IS NULL`, fromMember.ID) != 1 {
		t.Fatal("accept did not write the comment or clear triage")
	}
	env.member().expectError(http.StatusConflict, "NOT_IN_TRIAGE", http.MethodPost, accept, map[string]any{"version": accepted.Version, "priority": "HIGH"})

	// 标记重复：不能指向自己；目标任务记一条动态。
	duplicate := base + "/tasks/" + subtask.ID + "/triage/duplicate"
	current = getTriageTask(t, env.member(), space.ID, subtask.ID)
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPost, duplicate,
		map[string]any{"version": current.Version, "duplicate_of_task_id": subtask.ID})
	env.member().must(http.StatusOK, http.MethodPost, duplicate, map[string]any{"version": current.Version, "duplicate_of_task_id": fromMember.ID}, nil)
	if got := getTriageTask(t, env.member(), space.ID, subtask.ID); got.StatusCategory != "DUPLICATE" || got.DuplicateOf == nil || got.DuplicateOf.ID != fromMember.ID {
		t.Fatalf("duplicate = %+v", got)
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.duplicate_marked'`, fromMember.ID) != 1 {
		t.Fatal("duplicate activity missing on the target task")
	}

	// 同一任务同时接受与拒绝只成功一个；拒绝原因写成评论。
	race := triageTask(t, env.member(), space.ID, map[string]any{"priority": "LOW"})
	statuses := parallel(2, func(index int) int {
		action := []string{"accept", "decline"}[index]
		return env.member().do(http.MethodPost, base+"/tasks/"+race.ID+"/triage/"+action, map[string]any{"version": race.Version, "comment": action}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 {
		t.Fatalf("accept/decline race statuses = %v", statuses)
	}
	declined := triageTask(t, env.member(), space.ID, map[string]any{})
	env.member().must(http.StatusOK, http.MethodPost, base+"/tasks/"+declined.ID+"/triage/decline", map[string]any{"version": declined.Version, "comment": "不做"}, nil)
	if got := getTriageTask(t, env.member(), space.ID, declined.ID); got.StatusCategory != "CANCELLED" {
		t.Fatalf("declined = %+v", got)
	}

	// 手动把状态改回待分拣时重新计入分拣台。
	triageStatus := env.statusID(env.org, "TRIAGE")
	current = getTriageTask(t, env.member(), space.ID, fromMember.ID)
	env.member().must(http.StatusOK, http.MethodPatch, base+"/tasks/"+fromMember.ID, map[string]any{"version": current.Version, "status_id": triageStatus}, nil)
	if got := getTriageTask(t, env.member(), space.ID, fromMember.ID); got.Triage == nil {
		t.Fatal("task moved back to triage is not in triage")
	}

	// 责任动作「指派」：只在没有处理人时指派给第一个可指派的责任成员。
	patchTriage(t, env, space.ID, map[string]any{"action": "assign"})
	env.manager().must(http.StatusOK, http.MethodPut, base+"/triage-members", map[string]any{"user_ids": []string{s3ManagerID}}, nil)
	if got := triageTask(t, env.member(), space.ID, map[string]any{}); len(got.Assignees) != 1 || got.Assignees[0].ID != s3ManagerID {
		t.Fatalf("assign responsibility = %+v", got.Assignees)
	}
	kept := triageTask(t, env.member(), space.ID, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": s3MemberID}}})
	if len(kept.Assignees) != 1 || kept.Assignees[0].ID != s3MemberID {
		t.Fatalf("explicit assignee replaced = %+v", kept.Assignees)
	}

	// 关闭分拣台：剩余的待分拣任务移到待规划，并写状态变更动态。
	remaining := env.count(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND triage_entered_at IS NOT NULL`, space.ID)
	patchTriage(t, env, space.ID, map[string]any{"enabled": false})
	if env.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.task_statuses s ON s.id = t.status_id
		WHERE t.space_id = $1::uuid AND (t.triage_entered_at IS NOT NULL OR s.category = 'TRIAGE')`, space.ID) != 0 ||
		env.count(`SELECT count(*) FROM openfox.task_activities WHERE space_id = $1::uuid AND source_id LIKE 'triage-off:%'`, space.ID) != remaining {
		t.Fatal("disabling triage did not move the remaining tasks")
	}
	if got := triageTask(t, env.member(), space.ID, map[string]any{}); got.Triage != nil {
		t.Fatal("task entered triage after disabling")
	}
}

// 属性、评论、订阅和离队必须一起提交，兼容没有 subscribe 的旧请求。
func TestTriageDecisionSubscriptionAndAtomicFields(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	patchTriage(t, env, space.ID, map[string]any{"enabled": true})
	makeItem := func() (triageTaskView, string) {
		task := triageTask(t, env.manager(), space.ID, map[string]any{})
		return task, "spaces/" + space.ID + "/tasks/" + task.ID + "/triage/"
	}
	follows := func(id string) int {
		return env.count(`SELECT count(*) FROM openfox.task_followers WHERE task_id=$1 AND user_id=$2`, id, s3MemberID)
	}
	item, path := makeItem()
	args := map[string]any{"version": item.Version, "status_id": env.statusID(env.org, "DONE"), "start_date": "2026-11-06", "due_date": "2026-11-07", "field_values": map[string]string{s3GuestID: "no binding"}, "comment": "decision", "subscribe": false}
	env.member().expectError(http.StatusBadRequest, "FIELD_NOT_BOUND", http.MethodPost, path+"accept", args)
	if getTriageTask(t, env.member(), space.ID, item.ID).Triage == nil || follows(item.ID) != 0 || env.count(`SELECT count(*) FROM openfox.task_comments WHERE task_id=$1`, item.ID) != 0 {
		t.Fatal("a rejected field value partially committed the decision")
	}
	delete(args, "field_values")
	env.member().must(http.StatusOK, http.MethodPost, path+"accept", args, nil)
	if follows(item.ID) != 0 || env.count(`SELECT count(*) FROM openfox.tasks WHERE id=$1 AND triage_entered_at IS NULL AND start_date='2026-11-06' AND due_date='2026-11-07' AND status_id=$2`, item.ID, args["status_id"]) != 1 || env.count(`SELECT count(*) FROM openfox.task_comments WHERE task_id=$1`, item.ID) != 1 {
		t.Fatal("accept failed to persist status, dates, comment or subscribe opt-out")
	}
	for _, tc := range []struct {
		args map[string]any
		want int
	}{
		{map[string]any{"subscribe": true}, 1},
		{map[string]any{"comment": "legacy reason"}, 1},
		{map[string]any{"comment": "not following", "subscribe": false}, 0},
	} {
		item, path = makeItem()
		tc.args["version"] = item.Version
		env.member().must(http.StatusOK, http.MethodPost, path+"decline", tc.args, nil)
		if n := follows(item.ID); n != tc.want {
			t.Fatalf("decline %v: followers %d, want %d", tc.args, n, tc.want)
		}
	}
}

func TestTriageRules(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	patchTriage(t, env, space.ID, map[string]any{"enabled": true})
	base := "spaces/" + space.ID
	rules := base + "/triage-rules"
	var labelA, labelB, deletedLabel, typeID string
	env.scalar(&labelA, `INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, $2::uuid, 'la') RETURNING id::text`, env.org, space.ID)
	env.scalar(&labelB, `INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, $2::uuid, 'lb') RETURNING id::text`, env.org, space.ID)
	env.scalar(&deletedLabel, `INSERT INTO openfox.labels (organization_id, space_id, name, archived_at) VALUES ($1::uuid, $2::uuid, 'gone', clock_timestamp()) RETURNING id::text`, env.org, space.ID)
	env.scalar(&typeID, `SELECT id::text FROM openfox.task_types WHERE space_id = $1::uuid ORDER BY created_at LIMIT 1`, space.ID)
	urgent := []any{map[string]any{"type": "set_priority", "value": "URGENT"}}
	cond := func(field, op string, values ...string) map[string]any {
		return map[string]any{"field": field, "op": op, "values": append([]string{}, values...)}
	}
	group := func(conditions ...map[string]any) map[string]any { return map[string]any{"conditions": conditions} }
	clearRules := func() { env.exec(`DELETE FROM openfox.triage_rules WHERE space_id = $1::uuid`, space.ID) }

	// 保存时校验。
	for name, body := range map[string]map[string]any{
		"unknown field":     {"name": "x", "conditions": []any{group(cond("relation", "has_any", s3NewID(t)))}, "actions": urgent},
		"wrong op":          {"name": "x", "conditions": []any{group(cond("priority", "contains", "HIGH"))}, "actions": urgent},
		"no value":          {"name": "x", "conditions": []any{group(cond("label", "has_any"))}, "actions": urgent},
		"between needs two": {"name": "x", "conditions": []any{group(cond("due_date", "between", "2030-01-01"))}, "actions": urgent},
		"bad source":        {"name": "x", "conditions": []any{group(cond("source", "is", "robot"))}, "actions": urgent},
		"no action":         {"name": "x", "conditions": []any{}, "actions": []any{}},
		"list for scalar":   {"name": "x", "conditions": []any{}, "actions": []any{map[string]any{"type": "set_status", "value": []string{s3NewID(t)}}}},
		"scalar for list":   {"name": "x", "conditions": []any{}, "actions": []any{map[string]any{"type": "assign", "value": s3MemberID}}},
		"blank name":        {"name": " ", "conditions": []any{}, "actions": urgent},
	} {
		if response := env.manager().do(http.MethodPost, rules, body); response.Status != http.StatusBadRequest || response.code() != "INVALID_TRIAGE_RULE" {
			t.Fatalf("%s: %d %s", name, response.Status, response.Body)
		}
	}

	// 条件：每种字段与运算符；组内且、组间或。命中时把优先级设为 URGENT。
	due := "2029-12-01"
	for _, tc := range []struct {
		name       string
		conditions []any
		task       map[string]any
		client     *s3Client
		match      bool
	}{
		{"no conditions match all", []any{}, map[string]any{}, env.member(), true},
		{"source is member", []any{group(cond("source", "is", "member"))}, map[string]any{}, env.member(), true},
		{"source is guest", []any{group(cond("source", "is", "guest"))}, map[string]any{}, env.member(), false},
		{"source is_not member for guest", []any{group(cond("source", "is_not", "member"))}, map[string]any{}, env.guest(), true},
		{"priority is", []any{group(cond("priority", "is", "HIGH"))}, map[string]any{"priority": "HIGH"}, env.member(), true},
		{"type is", []any{group(cond("type", "is", typeID))}, map[string]any{"type_id": typeID}, env.member(), true},
		{"creator is_not", []any{group(cond("creator", "is_not", s3MemberID))}, map[string]any{}, env.member(), false},
		{"assignee is", []any{group(cond("assignee", "is", s3MemberID))}, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": s3MemberID}}}, env.member(), true},
		{"label has_any", []any{group(cond("label", "has_any", labelA))}, map[string]any{"label_ids": []string{labelA}}, env.member(), true},
		{"label has_all", []any{group(cond("label", "has_all", labelA, labelB))}, map[string]any{"label_ids": []string{labelA}}, env.member(), false},
		{"label has_none", []any{group(cond("label", "has_none", labelB))}, map[string]any{"label_ids": []string{labelA}}, env.member(), true},
		{"content contains ignores case", []any{group(cond("content", "contains", "BUG"))}, map[string]any{"title": "a bug here"}, env.member(), true},
		{"content % is literal", []any{group(cond("content", "contains", "%"))}, map[string]any{"title": "100 percent"}, env.member(), false},
		{"content % matches literal", []any{group(cond("content", "contains", "%"))}, map[string]any{"title": "100%"}, env.member(), true},
		{"content starts_with", []any{group(cond("content", "starts_with", "导出"))}, map[string]any{"title": "导出 CSV 乱码"}, env.member(), true},
		{"content ends_with", []any{group(cond("content", "ends_with", "乱码"))}, map[string]any{"title": "导出 CSV 乱码"}, env.member(), true},
		{"content exactly", []any{group(cond("content", "exactly", "导出"))}, map[string]any{"title": "导出 CSV"}, env.member(), false},
		{"content not_contains description", []any{group(cond("content", "not_contains", "secret"))}, map[string]any{"description": "a SECRET"}, env.member(), false},
		{"due before", []any{group(cond("due_date", "before", "2030-01-01"))}, map[string]any{"due_date": due}, env.member(), true},
		{"due between", []any{group(cond("due_date", "between", "2030-01-01", "2030-02-01"))}, map[string]any{"due_date": due}, env.member(), false},
		{"due none", []any{group(cond("due_date", "none"))}, map[string]any{}, env.member(), true},
		{"project none", []any{group(cond("project", "is", "none"))}, map[string]any{}, env.member(), true},
		{"iteration is_not none", []any{group(cond("iteration", "is_not", "none"))}, map[string]any{}, env.member(), false},
		{"and within group", []any{group(cond("source", "is", "member"), cond("priority", "is", "LOW"))}, map[string]any{"priority": "HIGH"}, env.member(), false},
		{"or across groups", []any{group(cond("priority", "is", "LOW")), group(cond("source", "is", "member"))}, map[string]any{"priority": "HIGH"}, env.member(), true},
	} {
		clearRules()
		env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": tc.name, "conditions": tc.conditions, "actions": urgent}, nil)
		task := triageTask(t, tc.client, space.ID, tc.task)
		if (task.Priority == "URGENT") != tc.match {
			t.Fatalf("%s: priority = %s, want match %v", tc.name, task.Priority, tc.match)
		}
	}

	// 动作：多条规则按顺序执行，同一字段以后执行的为准，标签取并集；失效的引用跳过；记录命中与最近使用时间。
	clearRules()
	var first, second, disabled triageRuleView
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "first", "conditions": []any{},
		"actions": []any{map[string]any{"type": "set_priority", "value": "LOW"}, map[string]any{"type": "add_labels", "value": []string{labelA}}}}, &first)
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "second", "conditions": []any{},
		"actions": []any{map[string]any{"type": "set_priority", "value": "HIGH"}, map[string]any{"type": "add_labels", "value": []string{labelB}},
			map[string]any{"type": "add_labels", "value": []string{deletedLabel}}, map[string]any{"type": "assign", "value": []string{s3MemberID}}}}, &second)
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "off", "enabled": false, "conditions": []any{}, "actions": urgent}, &disabled)
	if first.Position != 0 || second.Position != 1 || disabled.Enabled {
		t.Fatalf("rules = %+v %+v %+v", first, second, disabled)
	}
	task := triageTask(t, env.member(), space.ID, map[string]any{})
	want := []string{labelA, labelB}
	slices.Sort(want)
	if task.Priority != "HIGH" || !slices.Equal(task.labelIDs(), want) || len(task.Assignees) != 1 || task.Triage == nil {
		t.Fatalf("rule actions = %+v", task)
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'triage.rule_applied'`, task.ID) != 2 ||
		env.count(`SELECT count(*) FROM openfox.triage_rules WHERE space_id = $1::uuid AND last_used_at IS NOT NULL`, space.ID) != 2 {
		t.Fatal("rule usage was not recorded")
	}

	// 设置状态（离开分拣台）、设置专项、移动到其他空间（未启用分拣台，按目标空间默认状态建）。
	todo := env.statusID(env.org, "TODO")
	clearRules()
	var project struct {
		ID string `json:"id"`
	}
	env.manager().must(http.StatusCreated, http.MethodPost, "projects", map[string]any{"space_id": space.ID, "name": "P " + s3Suffix(t)}, &project)
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "route", "conditions": []any{group(cond("content", "contains", "route"))},
		"actions": []any{map[string]any{"type": "set_status", "value": todo}, map[string]any{"type": "set_project", "value": project.ID}}}, nil)
	routed := triageTask(t, env.member(), space.ID, map[string]any{"title": "please route"})
	if routed.Triage != nil || routed.StatusID != todo || routed.ProjectID == nil || *routed.ProjectID != project.ID {
		t.Fatalf("routed = %+v", routed)
	}
	target := env.createSpace("private")
	env.addSpaceMember(target.ID, s3MemberID, "MEMBER")
	viewerOnly := env.createSpace("private")
	env.addSpaceMember(viewerOnly.ID, s3MemberID, "VIEWER")
	clearRules()
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "move", "conditions": []any{group(cond("content", "contains", "move"))},
		"actions": []any{map[string]any{"type": "move_space", "value": target.ID}}}, nil)
	env.manager().must(http.StatusCreated, http.MethodPost, rules, map[string]any{"name": "blocked", "conditions": []any{group(cond("content", "contains", "viewer"))},
		"actions": []any{map[string]any{"type": "move_space", "value": viewerOnly.ID}}}, nil)
	moved := triageTask(t, env.member(), space.ID, map[string]any{"title": "move me"})
	if moved.SpaceID != target.ID || moved.Triage != nil || env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'triage.rule_applied'`, moved.ID) != 1 {
		t.Fatalf("moved = %+v", moved)
	}
	if stayed := triageTask(t, env.member(), space.ID, map[string]any{"title": "viewer only"}); stayed.SpaceID != space.ID || stayed.Triage == nil {
		t.Fatalf("move to a read-only space was not skipped: %+v", stayed)
	}

	// 修改、停用、删除、重排、上限。
	var listed struct {
		Rules []triageRuleView `json:"rules"`
	}
	env.member().must(http.StatusOK, http.MethodGet, rules, nil, &listed)
	if len(listed.Rules) != 2 {
		t.Fatalf("rules = %+v", listed.Rules)
	}
	a, b := listed.Rules[0], listed.Rules[1]
	var updated triageRuleView
	env.manager().must(http.StatusOK, http.MethodPut, rules+"/"+a.ID, map[string]any{"version": a.Version, "name": "renamed"}, &updated)
	env.manager().expectError(http.StatusConflict, "", http.MethodPut, rules+"/"+a.ID, map[string]any{"version": a.Version, "name": "stale"})
	env.manager().must(http.StatusOK, http.MethodPut, rules+"/"+a.ID+"/enabled", map[string]any{"version": updated.Version, "enabled": false}, &updated)
	if updated.Enabled {
		t.Fatal("rule still enabled")
	}
	env.manager().expectError(http.StatusBadRequest, "INVALID_TRIAGE_RULE", http.MethodPut, rules+"/reorder", map[string]any{"rule_ids": []string{b.ID}})
	env.manager().expectError(http.StatusBadRequest, "INVALID_TRIAGE_RULE", http.MethodPut, rules+"/reorder", map[string]any{"rule_ids": []string{b.ID, b.ID}})
	orders := [][]string{{b.ID, a.ID}, {a.ID, b.ID}}
	statuses := parallel(2, func(index int) int {
		return env.manager().do(http.MethodPut, rules+"/reorder", map[string]any{"rule_ids": orders[index]}).Status
	})
	if countStatus(statuses, http.StatusOK) != 2 {
		t.Fatalf("reorder statuses = %v", statuses)
	}
	env.member().must(http.StatusOK, http.MethodGet, rules, nil, &listed)
	got := []string{listed.Rules[0].ID, listed.Rules[1].ID}
	if !slices.Equal(got, orders[0]) && !slices.Equal(got, orders[1]) {
		t.Fatalf("order after concurrent reorder = %v", got)
	}
	env.manager().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, rules+"/"+s3NewID(t)+"?version=1", nil)
	env.manager().expectError(http.StatusConflict, "", http.MethodDelete, fmt.Sprintf("%s/%s?version=%d", rules, b.ID, b.Version+5), nil)
	env.manager().must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s/%s?version=%d", rules, listed.Rules[0].ID, listed.Rules[0].Version), nil, nil)
	env.member().must(http.StatusOK, http.MethodGet, rules, nil, &listed)
	if len(listed.Rules) != 1 || listed.Rules[0].Position != 0 {
		t.Fatalf("positions after delete = %+v", listed.Rules)
	}
	env.exec(`INSERT INTO openfox.triage_rules (organization_id, space_id, name, position, actions, created_by, updated_by)
		SELECT $1::uuid, $2::uuid, 'bulk ' || n, n, '[{"type":"set_priority","value":"LOW"}]', $3::uuid, $3::uuid FROM generate_series(1, 48) AS n`,
		env.org, space.ID, s3ManagerID)
	body := map[string]any{"name": "last", "conditions": []any{}, "actions": urgent}
	if statuses := parallel(3, func(int) int { return env.manager().do(http.MethodPost, rules, body).Status }); countStatus(statuses, http.StatusCreated) != 1 {
		t.Fatalf("limit statuses = %v", statuses)
	}
	env.manager().expectError(http.StatusConflict, "LIMIT_REACHED", http.MethodPost, rules, body)
}

func TestTriageFavoriteIsolation(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	path := "spaces/" + space.ID
	for i := 0; i < 2; i++ {
		env.other().must(http.StatusNoContent, http.MethodPut, path+"/triage/favorite", nil, nil)
	}
	var mine, other struct {
		Favorite bool `json:"triage_favorited"`
	}
	env.other().must(http.StatusOK, http.MethodGet, path, nil, &mine)
	env.member().must(http.StatusOK, http.MethodGet, path, nil, &other)
	if !mine.Favorite || other.Favorite {
		t.Fatal("favorite leaked across users or did not persist")
	}
	for i := 0; i < 2; i++ {
		env.other().must(http.StatusNoContent, http.MethodDelete, path+"/triage/favorite", nil, nil)
	}
	env.other().must(http.StatusOK, http.MethodGet, path, nil, &mine)
	if mine.Favorite {
		t.Fatal("favorite still present")
	}
}

func TestTriageIncomingIssueAndHistoricalImport(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	patchTriage(t, env, space.ID, map[string]any{"enabled": true})
	selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: s3MemberID, AuthorizationVersion: 1}
	actor := collaboration.Actor{UserID: s3MemberID, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	var issue, historical string
	err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
		svc := collaboration.Service{}
		input := collaboration.ImportTaskInput{Source: "github", SourceKey: "repo#1", Title: "Incoming issue", CreatedAt: time.Now().UTC().Add(-time.Hour)}
		var err error
		issue, _, err = svc.CreateIssueTask(ctx, tx, env.org, space.ID, actor, input)
		if err != nil {
			return err
		}
		historical, _, err = svc.ImportCreateTask(ctx, tx, env.org, space.ID, actor, input)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if task := getTriageTask(t, env.member(), space.ID, issue); task.Triage == nil || task.Triage.Source != "integration" {
		t.Fatalf("issue source: %+v", task)
	}
	if task := getTriageTask(t, env.member(), space.ID, historical); task.Triage != nil {
		t.Fatal("historical import entered queue")
	}
}
