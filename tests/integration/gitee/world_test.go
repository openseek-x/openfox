package giteeint

import (
	"fmt"
	"net/http"
	"testing"
)

type world struct {
	*env
	org, orgB                           string
	ownerID, adminID, memberID, guestID string
	ownerBID                            string
	owner, admin, member, guest         *client
	ownerB                              *client
	foxSpace, secSpace                  string
	prefix                              string
	connID                              string // openfox-dev 组织连接
	webhookURL, webhookSecret           string
}

func (w *world) path(suffix string) string { return "/v1/organizations/" + w.org + suffix }

func setup(t *testing.T) *world {
	e := newEnv(t)
	w := &world{env: e}
	w.org, w.orgB = e.newUUID(), e.newUUID()
	suffix := w.org[:8]
	mk := func(name string, target *string) {
		email := fmt.Sprintf("ge-%s-%s@example.test", name, suffix)
		*target = e.user(email)
	}
	mk("owner", &w.ownerID)
	mk("admin", &w.adminID)
	mk("member", &w.memberID)
	mk("guest", &w.guestID)
	mk("ownerb", &w.ownerBID)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Gitee A'), ($3::uuid, $4, 'Gitee B')`,
		w.org, "ge-a-"+suffix, w.orgB, "ge-b-"+suffix)
	// 控制面函数（Webhook 令牌、OAuth state 解析）通过组织目录定位组织。
	e.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at) VALUES ($1::uuid, $2, 'Gitee A', now()), ($3::uuid, $4, 'Gitee B', now())`,
		w.org, "ge-a-"+suffix, w.orgB, "ge-b-"+suffix)
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
		($1::uuid, $2::uuid, 'OWNER'), ($1::uuid, $3::uuid, 'ADMIN'), ($1::uuid, $4::uuid, 'MEMBER'), ($1::uuid, $5::uuid, 'GUEST'),
		($6::uuid, $7::uuid, 'OWNER')`, w.org, w.ownerID, w.adminID, w.memberID, w.guestID, w.orgB, w.ownerBID)
	// 状态：第一个进行中是 In Progress，第一个已完成是 Done；各有第二个，用来验证「第一个」。
	for _, org := range []string{w.org, w.orgB} {
		e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Triage', '', 'TRIAGE', 0), ($1::uuid, 'Backlog', '', 'BACKLOG', 1), ($1::uuid, 'Todo', '', 'TODO', 2),
			($1::uuid, 'In Progress', '', 'IN_PROGRESS', 3), ($1::uuid, 'Done', '', 'DONE', 4), ($1::uuid, 'Cancelled', '', 'CANCELLED', 5),
			($1::uuid, 'Doing', '', 'IN_PROGRESS', 6), ($1::uuid, 'Shipped', '', 'DONE', 7)`, org)
	}
	w.owner = e.login(e.server.URL+"/api", "ge-owner-"+suffix+"@example.test", w.ownerID)
	w.admin = e.login(e.server.URL+"/api", "ge-admin-"+suffix+"@example.test", w.adminID)
	w.member = e.login(e.server.URL+"/api", "ge-member-"+suffix+"@example.test", w.memberID)
	w.guest = e.login(e.server.URL+"/api", "ge-guest-"+suffix+"@example.test", w.guestID)
	w.ownerB = e.login(e.server.URL+"/api", "ge-ownerb-"+suffix+"@example.test", w.ownerBID)
	var space struct {
		ID string `json:"id"`
	}
	w.prefix = "fox"
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Fox", "prefix": w.prefix, "visibility": "SHARED"}, &space)
	w.foxSpace = space.ID
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces"), map[string]any{"name": "Secret", "prefix": "sec", "visibility": "PRIVATE"}, &space)
	w.secSpace = space.ID
	return w
}

type taskInfo struct {
	ID, DisplayID string
	Sequence      int64
}

func (w *world) task(spaceID, title string) taskInfo {
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Sequence  int64  `json:"sequence"`
	}
	w.owner.json(http.StatusCreated, http.MethodPost, w.path("/spaces/"+spaceID+"/tasks"), map[string]any{"title": title}, &task)
	return taskInfo{ID: task.ID, DisplayID: task.DisplayID, Sequence: task.Sequence}
}

// taskStatus 通过 API 读取任务的状态名与类别。
func (w *world) taskStatus(spaceID string, task taskInfo) (name, category string) {
	var got struct {
		StatusName     string `json:"status_name"`
		StatusCategory string `json:"status_category"`
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/spaces/"+spaceID+"/tasks/"+task.ID), nil, &got)
	return got.StatusName, got.StatusCategory
}

func (w *world) setTaskStatus(task taskInfo, statusName string) {
	w.exec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2),
		version = version + 1 WHERE organization_id = $1::uuid AND id = $3::uuid`, w.org, statusName, task.ID)
}

func (w *world) gitLinks(c *client, spaceID string, task taskInfo) []map[string]any {
	var got struct {
		Links []map[string]any `json:"links"`
	}
	c.json(http.StatusOK, http.MethodGet, w.path("/spaces/"+spaceID+"/tasks/"+task.ID+"/git-links"), nil, &got)
	return got.Links
}
