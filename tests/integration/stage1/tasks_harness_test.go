package stage1

// 任务、我的任务与收件箱集成测试的夹具：复用 config_harness_test.go 的 HTTP 环境（独占组织、owner / member / outsider），
// 另外按需加入更多成员、空间成员与 Agent。

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// 任务测试额外使用的固定账号；用户行全局共享（ON CONFLICT DO NOTHING），组织成员关系按测试组织各自插入。
const (
	tkUserA = "00000000-0000-0000-0000-0000000d5a01"
	tkUserB = "00000000-0000-0000-0000-0000000d5a02"
	tkUserC = "00000000-0000-0000-0000-0000000d5a03"
	tkGuest = "00000000-0000-0000-0000-0000000d5a04"
)

var tkEmails = map[string]string{
	tkUserA: "tk-a@example.test",
	tkUserB: "tk-b@example.test",
	tkUserC: "tk-c@example.test",
	tkGuest: "tk-guest@example.test",
}

type tkEnv struct {
	*cfgEnv
	deviceID string
}

func newTkEnv(t *testing.T) *tkEnv {
	t.Helper()
	return &tkEnv{cfgEnv: newCfgEnv(t)}
}

// addMember 把一个额外账号加入测试组织并登录。
func (e *tkEnv) addMember(userID, role string) *cfgClient {
	e.t.Helper()
	email := tkEmails[userID]
	e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, userID, email)
	e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, userID, cfgHash)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`,
		e.org, userID, role)
	return e.login(email)
}

// addSpaceMember 以超级用户写入（或改写）空间成员关系。
func (e *tkEnv) addSpaceMember(spaceID, userID, role, status string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)
		ON CONFLICT (organization_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = EXCLUDED.status`,
		e.org, spaceID, userID, role, status)
}

// insertAgent 以超级用户直接写一个受管 Agent（不需要真实设备在线），返回 ID。
func (e *tkEnv) insertAgent(spaceID, ownerID, visibility string) string {
	e.t.Helper()
	if e.deviceID == "" {
		e.deviceID = cfgNewID(e.t)
		e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES ($1::uuid, $2, 1)`,
			e.deviceID, "tk-device-"+cfgSuffix(e.t))
		e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, e.org, e.deviceID)
	}
	id := cfgNewID(e.t)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, 1)`,
		e.org, id, e.deviceID, spaceID, ownerID, "Agent "+cfgSuffix(e.t), visibility)
	return id
}

type tkAssignee struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tkTask struct {
	ID           string       `json:"id"`
	SpaceID      string       `json:"space_id"`
	DisplayID    string       `json:"display_id"`
	Sequence     int64        `json:"sequence"`
	TypeID       string       `json:"type_id"`
	StatusID     string       `json:"status_id"`
	Title        string       `json:"title"`
	Priority     string       `json:"priority"`
	Version      int          `json:"version"`
	StartDate    *string      `json:"start_date"`
	DueDate      *string      `json:"due_date"`
	ParentTaskID *string      `json:"parent_task_id"`
	Rank         string       `json:"rank"`
	Assignees    []tkAssignee `json:"assignees"`
	LabelItems   []struct {
		ID      string  `json:"id"`
		GroupID *string `json:"group_id"`
	} `json:"label_items"`
	SubtaskCount int     `json:"subtask_count"`
	CreatedBy    *string `json:"created_by"`
	Deleted      bool    `json:"deleted"`
}

func tasksPath(space cfgSpace) string { return "spaces/" + space.ID + "/tasks" }

func taskPath(space cfgSpace, taskID string) string { return tasksPath(space) + "/" + taskID }

// newTask 由 client 在空间里创建任务；body 缺省时补上类型和标题。
func (e *tkEnv) newTask(client *cfgClient, space cfgSpace, body map[string]any) tkTask {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["type_id"]; !ok {
		body["type_id"] = space.TypeID
	}
	if _, ok := body["title"]; !ok {
		body["title"] = "Task " + cfgSuffix(e.t)
	}
	var task tkTask
	client.must(http.StatusCreated, http.MethodPost, tasksPath(space), body, &task)
	return task
}

func (e *tkEnv) getTask(client *cfgClient, space cfgSpace, taskID string) tkTask {
	e.t.Helper()
	var task tkTask
	client.must(http.StatusOK, http.MethodGet, taskPath(space, taskID), nil, &task)
	return task
}

// patchTask 以任务当前版本发送 PATCH（自动读取最新 version）。
func (e *tkEnv) patchTask(client *cfgClient, space cfgSpace, taskID string, body map[string]any) cfgResponse {
	e.t.Helper()
	body["version"] = e.getTask(e.owner, space, taskID).Version
	return client.do(http.MethodPatch, taskPath(space, taskID), body)
}

// listIDs 返回列表接口（query 为原始查询串）里的任务 ID，按返回顺序。
func (e *tkEnv) listIDs(client *cfgClient, path, query string) []string {
	e.t.Helper()
	var page struct {
		Tasks      []tkTask `json:"tasks"`
		NextCursor string   `json:"next_cursor"`
	}
	ids := []string{}
	cursor := ""
	for {
		full := path + "?limit=100"
		if query != "" {
			full += "&" + query
		}
		if cursor != "" {
			full += "&cursor=" + url.QueryEscape(cursor)
		}
		client.must(http.StatusOK, http.MethodGet, full, nil, &page)
		for _, task := range page.Tasks {
			ids = append(ids, task.ID)
		}
		if page.NextCursor == "" {
			return ids
		}
		cursor = page.NextCursor
	}
}

type tkCounts struct {
	Total    int `json:"total"`
	ByStatus []struct {
		StatusID string `json:"status_id"`
		Count    int    `json:"count"`
	} `json:"by_status"`
}

func (e *tkEnv) counts(client *cfgClient, path, query string) tkCounts {
	e.t.Helper()
	var counts tkCounts
	full := path
	if query != "" {
		full += "?" + query
	}
	client.must(http.StatusOK, http.MethodGet, full, nil, &counts)
	sum := 0
	for _, item := range counts.ByStatus {
		sum += item.Count
	}
	if sum != counts.Total {
		e.t.Fatalf("counts %s: by_status sum %d != total %d", full, sum, counts.Total)
	}
	return counts
}

// notifications 以超级用户读取某成员在本组织收到的通知 "kind:task_id"（排序后）。
func (e *tkEnv) notifications(recipient string) []string {
	e.t.Helper()
	rows, err := e.superuser.Query(e.ctx, `
		SELECT kind || ':' || COALESCE(task_id::text, conversation_id::text, '')
		  FROM openfox.notifications
		 WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid`, e.org, recipient)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			e.t.Fatal(err)
		}
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func joinQuery(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		parts = append(parts, url.QueryEscape(pairs[index])+"="+url.QueryEscape(pairs[index+1]))
	}
	return strings.Join(parts, "&")
}

func kindTask(kind, taskID string) string { return fmt.Sprintf("%s:%s", kind, taskID) }
