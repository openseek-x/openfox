package stage3

// 迭代与调度器集成测试。
// 调度类用例用固定时钟直接调用 collaboration.Service（不 sleep 等真实时间）；
// 接口类用例走真实 HTTP 处理器。固定时钟都落在 2030 年（2030-01-07 是周一）。

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

var shanghai = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return loc
}()

func cstAt(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2030, month, day, hour, minute, 0, 0, shanghai)
}

func ptr[T any](value T) *T { return &value }

type iterView struct {
	ID              string     `json:"id"`
	Number          int        `json:"number"`
	Name            string     `json:"name"`
	DisplayName     string     `json:"display_name"`
	Status          string     `json:"status"`
	StartsAt        time.Time  `json:"starts_at"`
	EndsAt          time.Time  `json:"ends_at"`
	StartDate       string     `json:"start_date"`
	EndDate         string     `json:"end_date"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	ScopeCount      *int       `json:"scope_count"`
	CompletedCount  *int       `json:"completed_count"`
	ScopePoints     *float64   `json:"scope_points"`
	CompletedPoints *float64   `json:"completed_points"`
	CapacityPercent *float64   `json:"capacity_percent"`
	Live            struct {
		ScopeCount int `json:"scope_count"`
	} `json:"live"`
	Version int `json:"version"`
}

type iterSettingsView struct {
	Enabled       bool      `json:"enabled"`
	Mode          string    `json:"mode"`
	NextStartDate *string   `json:"next_start_date"`
	Current       *iterView `json:"current"`
	Version       int       `json:"version"`
}

func iterPath(spaceID string) string { return spacePath(spaceID) + "/iterations" }

func iterList(env *s3Env, client *s3Client, spaceID, query string) []iterView {
	env.t.Helper()
	var body struct {
		Iterations []iterView `json:"iterations"`
	}
	client.must(http.StatusOK, http.MethodGet, iterPath(spaceID)+"?limit=100"+query, nil, &body)
	return body.Iterations
}

func iterSettings(env *s3Env, client *s3Client, spaceID string) iterSettingsView {
	env.t.Helper()
	var settings iterSettingsView
	client.must(http.StatusOK, http.MethodGet, spacePath(spaceID)+"/iteration-settings", nil, &settings)
	return settings
}

// patchIterSettings 取当前版本后提交 body。
func patchIterSettings(env *s3Env, client *s3Client, spaceID string, body map[string]any) s3Response {
	env.t.Helper()
	body["version"] = iterSettings(env, env.owner(), spaceID).Version
	return client.do(http.MethodPatch, spacePath(spaceID)+"/iteration-settings", body)
}

func createIteration(env *s3Env, spaceID, start, end string) iterView {
	env.t.Helper()
	var item iterView
	env.owner().must(http.StatusCreated, http.MethodPost, iterPath(spaceID), map[string]any{"start_date": start, "end_date": end}, &item)
	return item
}

func iterAction(env *s3Env, spaceID string, item iterView, action string, body map[string]any) iterView {
	env.t.Helper()
	body["version"] = env.getIteration(spaceID, item.ID).Version
	var out iterView
	env.owner().must(http.StatusOK, http.MethodPost, iterPath(spaceID)+"/"+item.ID+"/"+action, body, &out)
	return out
}

func (e *s3Env) getIteration(spaceID, id string) iterView {
	e.t.Helper()
	var item iterView
	e.owner().must(http.StatusOK, http.MethodGet, iterPath(spaceID)+"/"+id, nil, &item)
	return item
}

// inService 在组织 worker 上下文里以固定时钟运行 fn（与派发 worker 同一事务边界）。
func (e *s3Env) inService(now time.Time, fn func(context.Context, collaboration.Service, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			return fn(ctx, collaboration.Service{Now: func() time.Time { return now }}, tx)
		})
}

func (e *s3Env) schedule(now time.Time) {
	e.t.Helper()
	if err := e.inService(now, func(ctx context.Context, s collaboration.Service, tx pgx.Tx) error {
		return s.ScheduleIterations(ctx, tx, e.org, now)
	}); err != nil {
		e.t.Fatalf("schedule %s: %v", now, err)
	}
}

func (e *s3Env) setIterationSettings(spaceID string, now time.Time, patch collaboration.IterationSettingsPatch) {
	e.t.Helper()
	if err := e.inService(now, func(ctx context.Context, s collaboration.Service, tx pgx.Tx) error {
		current, err := s.GetIterationSettings(ctx, tx, e.org, spaceID)
		if err != nil {
			return err
		}
		patch.Version = current.Version
		_, err = s.UpdateIterationSettings(ctx, tx, e.org, spaceID, patch)
		return err
	}); err != nil {
		e.t.Fatalf("settings: %v", err)
	}
}

// autoSpace 新建空间并在固定时间 now 启用自动模式（周一开始、Asia/Shanghai）。
func (e *s3Env) autoSpace(now time.Time, duration, cooldown, upcoming int, autoAdd bool) SpaceView {
	e.t.Helper()
	space := e.createSpace("private")
	e.setIterationSettings(space.ID, now, collaboration.IterationSettingsPatch{
		Enabled: ptr(true), Mode: ptr("automatic"), DurationWeeks: ptr(duration), CooldownWeeks: ptr(cooldown),
		StartWeekday: ptr(1), Timezone: ptr("Asia/Shanghai"), UpcomingCount: ptr(upcoming),
		AutoAddStarted: ptr(autoAdd), AutoAddCompleted: ptr(autoAdd),
	})
	return space
}

type iterRow struct {
	ID                      string
	Status                  string
	Number                  int
	StartsAt, EndsAt        time.Time
	StartedAt, CompletedAt  *time.Time
	Scope, Done             *int
	ScopePoints, DonePoints *float64
}

func (e *s3Env) iterRows(spaceID string) []iterRow {
	e.t.Helper()
	rows, err := e.superuser.Query(e.ctx, `SELECT id::text, status, number, starts_at, ends_at, started_at, completed_at, scope_count, completed_count, scope_points::float8, completed_points::float8 FROM openfox.iterations WHERE space_id = $1::uuid ORDER BY number`, spaceID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []iterRow
	for rows.Next() {
		var r iterRow
		if err := rows.Scan(&r.ID, &r.Status, &r.Number, &r.StartsAt, &r.EndsAt, &r.StartedAt, &r.CompletedAt, &r.Scope, &r.Done, &r.ScopePoints, &r.DonePoints); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *s3Env) taskIteration(taskID string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT COALESCE(iteration_id::text, '') FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	return id
}

func (e *s3Env) iterationActivities(taskID string) int {
	return e.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.iteration_changed'`, taskID)
}

func (e *s3Env) setStatus(client *s3Client, spaceID string, task TaskView, category string) TaskView {
	e.t.Helper()
	var out TaskView
	client.must(http.StatusOK, http.MethodPatch, spacePath(spaceID)+"/tasks/"+task.ID,
		map[string]any{"version": task.Version, "status_id": e.statusID(e.org, category)}, &out)
	return out
}

func statusesOf(rows []iterRow) string {
	out := ""
	for _, r := range rows {
		out += fmt.Sprintf("%d:%s ", r.Number, r.Status)
	}
	return out
}

func TestIterationPermissions(t *testing.T) {
	env := newS3Env(t)
	space := env.space.ID
	settingsPath := spacePath(space) + "/iteration-settings"
	if response := patchIterSettings(env, env.manager(), space, map[string]any{"enabled": true, "mode": "manual"}); response.Status != http.StatusOK {
		t.Fatalf("manager enable = %d %s", response.Status, response.Body)
	}
	// 读 = 空间可读；写 = 空间管理。
	cases := []struct {
		name        string
		client      *s3Client
		read, write int
		create      int
	}{
		{"owner", env.owner(), 200, 200, 201},
		{"admin", env.admin(), 404, 200, 201}, // 现有策略：组织 ADMIN 可管理空间，但读不到自己不是成员的私有空间
		{"space-owner", env.manager(), 200, 200, 201},
		{"space-member", env.member(), 200, 403, 403},
		{"non-member", env.other(), 404, 404, 404},
		{"guest", env.guest(), 404, 404, 404},
	}
	for index, tc := range cases {
		if got := tc.client.do(http.MethodGet, settingsPath, nil).Status; got != tc.read {
			t.Errorf("%s read settings = %d, want %d", tc.name, got, tc.read)
		}
		if got := tc.client.do(http.MethodGet, iterPath(space)+"?limit=10", nil).Status; got != tc.read {
			t.Errorf("%s list = %d, want %d", tc.name, got, tc.read)
		}
		if got := patchIterSettings(env, tc.client, space, map[string]any{"upcoming_count": 2}).Status; got != tc.write {
			t.Errorf("%s patch settings = %d, want %d", tc.name, got, tc.write)
		}
		day := fmt.Sprintf("2031-%02d-01", index+1)
		if got := tc.client.do(http.MethodPost, iterPath(space), map[string]any{"start_date": day, "end_date": day}).Status; got != tc.create {
			t.Errorf("%s create iteration = %d, want %d", tc.name, got, tc.create)
		}
	}
	// 跨租户：另一个组织的用户看不到本组织的空间。
	item := iterList(env, env.owner(), space, "")[0]
	outsider := env.outsider()
	for _, path := range []string{settingsPath, iterPath(space), iterPath(space) + "/" + item.ID} {
		if got := outsider.doIn(env.otherOrg, http.MethodGet, path, nil).Status; got != http.StatusNotFound {
			t.Errorf("cross-tenant GET %s = %d", path, got)
		}
	}
	if got := outsider.doIn(env.otherOrg, http.MethodDelete, iterPath(space)+"/"+item.ID+"?version=1", nil).Status; got != http.StatusNotFound {
		t.Errorf("cross-tenant delete = %d", got)
	}
	// 停用的空间：设置与迭代写入都是 409，读仍可用。
	var space2 SpaceView = env.createSpace("private")
	patchIterSettings(env, env.owner(), space2.ID, map[string]any{"enabled": true, "mode": "manual"})
	env.owner().must(http.StatusOK, http.MethodPost, spacePath(space2.ID)+"/retire",
		map[string]any{"version": env.getSpace(env.owner(), space2.ID).Version, "open_tasks": "cancel"}, nil)
	if response := patchIterSettings(env, env.owner(), space2.ID, map[string]any{"upcoming_count": 3}); response.Status != http.StatusConflict || response.code() != "SPACE_RETIRED" {
		t.Errorf("retired patch settings = %d %s", response.Status, response.Body)
	}
	env.owner().expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, iterPath(space2.ID), map[string]any{"start_date": "2031-01-01", "end_date": "2031-01-02"})
	iterList(env, env.owner(), space2.ID, "")
}

func TestIterationAutomaticSettings(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	// 避开 00:00–00:01（迭代从 00:01 开始），否则「今天已开始」不成立。
	if now := time.Now().In(shanghai); now.Hour() == 0 && now.Minute() == 0 {
		time.Sleep(61 * time.Second)
	}
	today := time.Now().In(shanghai)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	space := env.createSpace("private").ID

	if settings := iterSettings(env, owner, space); settings.Enabled {
		t.Fatal("iterations enabled by default")
	}
	// 启用必须同时给出模式。
	if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true}); response.Status != http.StatusBadRequest {
		t.Fatalf("enable without mode = %d %s", response.Status, response.Body)
	}
	for _, bad := range []map[string]any{{"timezone": "Mars/Base"}, {"upcoming_count": 5}, {"duration_weeks": 9}, {"cooldown_weeks": 4}, {"start_weekday": 7}} {
		bad["enabled"], bad["mode"] = true, "automatic"
		if response := patchIterSettings(env, owner, space, bad); response.Status != http.StatusBadRequest {
			t.Fatalf("invalid %v = %d %s", bad, response.Status, response.Body)
		}
	}
	if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "automatic", "timezone": "Mars/Base"}); response.code() != "INVALID_TIMEZONE" {
		t.Fatalf("timezone code = %s", response.code())
	}
	// 启用：立即生成 upcoming_count 个计划中迭代，起始日取今天（周几与今天相同），今天落在第一个区间内 → 立即开始。
	if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "automatic", "start_weekday": int(today.Weekday()), "upcoming_count": 3}); response.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", response.Status, response.Body)
	}
	items := iterList(env, owner, space, "")
	if len(items) != 4 || items[0].Status != "active" || items[0].StartedAt == nil {
		t.Fatalf("after enable: %+v", items)
	}
	for index, item := range items {
		start := index * 14
		if item.Number != index+1 || item.StartDate != day(start) || item.EndDate != day(start+13) || (index > 0 && item.Status != "planned") {
			t.Fatalf("iteration %d = %+v, want start %s", index, item, day(start))
		}
	}
	settings := iterSettings(env, owner, space)
	if settings.Current == nil || settings.Current.ID != items[0].ID || settings.NextStartDate == nil || *settings.NextStartDate != items[1].StartDate {
		t.Fatalf("settings = %+v", settings)
	}
	// 禁用保留迭代；再次启用必须给模式，且不重复生成。
	patchIterSettings(env, owner, space, map[string]any{"enabled": false})
	if len(iterList(env, owner, space, "")) != 4 {
		t.Fatal("disable dropped iterations")
	}
	if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true}); response.Status != http.StatusBadRequest {
		t.Fatalf("re-enable without mode = %d", response.Status)
	}
	patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "automatic"})
	// 自动 → 手动 → 自动：保留已有迭代，不重排、不重复生成。
	patchIterSettings(env, owner, space, map[string]any{"mode": "manual"})
	patchIterSettings(env, owner, space, map[string]any{"mode": "automatic"})
	again := iterList(env, owner, space, "")
	if len(again) != 4 {
		t.Fatalf("mode round trip: %d iterations", len(again))
	}
	for index := range again {
		if again[index].ID != items[index].ID || again[index].StartDate != items[index].StartDate {
			t.Fatalf("mode round trip changed iteration %d", index)
		}
	}
	// 自动模式只允许改名称 / 描述 / 日期。
	owner.expectError(http.StatusConflict, "ITERATION_AUTOMATIC", http.MethodPost, iterPath(space)+"/"+items[1].ID+"/start", map[string]any{"version": items[1].Version})
	owner.expectError(http.StatusConflict, "ITERATION_AUTOMATIC", http.MethodPost, iterPath(space), map[string]any{"start_date": day(100), "end_date": day(101)})
	owner.expectError(http.StatusConflict, "ITERATION_AUTOMATIC", http.MethodPost, iterPath(space)+"/"+items[0].ID+"/complete", map[string]any{"version": items[0].Version, "move_unfinished_to": "backlog"})
	var renamed iterView
	owner.must(http.StatusOK, http.MethodPatch, iterPath(space)+"/"+items[1].ID, map[string]any{"version": items[1].Version, "name": "冲刺"}, &renamed)
	if renamed.DisplayName != "冲刺" || items[2].Name != "" || items[2].DisplayName != "迭代 3" {
		t.Fatalf("names: %+v / %+v", renamed, items[2])
	}
	// 时长改为 1 周：计划中的迭代按新规则重排（编号不变），活跃迭代不变。
	patchIterSettings(env, owner, space, map[string]any{"duration_weeks": 1})
	week := iterList(env, owner, space, "")
	if week[0].EndDate != day(13) {
		t.Fatalf("active iteration changed: %+v", week[0])
	}
	for index := 1; index < 4; index++ {
		start := 14 + (index-1)*7
		if week[index].Number != index+1 || week[index].StartDate != day(start) || week[index].EndDate != day(start+6) {
			t.Fatalf("1-week iteration %d = %+v, want start %s", index, week[index], day(start))
		}
	}
	// 冷却 1 周：计划中迭代之间空一周。
	patchIterSettings(env, owner, space, map[string]any{"cooldown_weeks": 1})
	cool := iterList(env, owner, space, "")
	if cool[2].StartDate != day(14+14) || cool[3].StartDate != day(14+28) {
		t.Fatalf("cooldown dates = %s %s", cool[2].StartDate, cool[3].StartDate)
	}
	// 手动改「下个迭代开始日」：整体顺移计划中迭代；早于活跃迭代结束则 409。
	if response := patchIterSettings(env, owner, space, map[string]any{"next_start_date": day(5)}); response.code() != "ITERATION_OVERLAP" {
		t.Fatalf("overlapping next start = %d %s", response.Status, response.Body)
	}
	patchIterSettings(env, owner, space, map[string]any{"next_start_date": day(20)})
	if moved := iterList(env, owner, space, ""); moved[1].StartDate != day(20) || moved[1].Number != 2 {
		t.Fatalf("moved = %+v", moved[1])
	}
}

func TestIterationTimezones(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	// 迭代从起始日 00:01（空间时区）开始，ends_at 是结束日次日 00:01。America/New_York 跨 2026-03-08 夏令时。
	cases := []struct{ timezone, startsAt, endsAt string }{
		{"Asia/Shanghai", "2026-03-01T16:01:00Z", "2026-03-15T16:01:00Z"},
		{"America/New_York", "2026-03-02T05:01:00Z", "2026-03-16T04:01:00Z"},
	}
	for _, tc := range cases {
		space := env.createSpace("private").ID
		if response := patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "manual", "timezone": tc.timezone}); response.Status != http.StatusOK {
			t.Fatalf("%s enable = %d %s", tc.timezone, response.Status, response.Body)
		}
		created := createIteration(env, space, "2026-03-02", "2026-03-15")
		got := env.getIteration(space, created.ID)
		wantStart, _ := time.Parse(time.RFC3339, tc.startsAt)
		wantEnd, _ := time.Parse(time.RFC3339, tc.endsAt)
		if !got.StartsAt.Equal(wantStart) || !got.EndsAt.Equal(wantEnd) || got.StartDate != "2026-03-02" || got.EndDate != "2026-03-15" {
			t.Errorf("%s: %+v, want %s – %s", tc.timezone, got, tc.startsAt, tc.endsAt)
		}
	}
}

func TestIterationScheduler(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	monday := cstAt(time.January, 7, 10, 0)

	t.Run("complete_carry_over_freeze_start_replenish", func(t *testing.T) {
		space := env.autoSpace(monday, 1, 0, 2, false)
		env.exec(`UPDATE openfox.spaces SET estimation_scale = 'fibonacci' WHERE id = $1::uuid`, space.ID)
		rows := env.iterRows(space.ID)
		if len(rows) != 3 || rows[0].Status != "active" || rows[1].Status != "planned" {
			t.Fatalf("initial: %s", statusesOf(rows))
		}
		first, second := rows[0].ID, rows[1].ID
		mk := func(category string, estimate any) TaskView {
			return env.createTask(owner, space.ID, map[string]any{"status_id": env.statusID(env.org, category), "iteration_id": first, "estimate": estimate})
		}
		done, progress, todo, canceled, doneUnestimated := mk("DONE", 3), mk("IN_PROGRESS", 5), mk("TODO", 2), mk("CANCELLED", 8), mk("DONE", nil)
		env.schedule(cstAt(time.January, 14, 0, 30))
		rows = env.iterRows(space.ID)
		if len(rows) != 4 || rows[0].Status != "completed" || rows[1].Status != "active" || rows[2].Status != "planned" || rows[3].Status != "planned" {
			t.Fatalf("after first period: %s", statusesOf(rows))
		}
		c := rows[0]
		// 范围不含已取消；完成 = DONE；未估时且空间未开启「未估时按 1 点」时点数为 0。
		if *c.Scope != 4 || *c.Done != 2 || *c.ScopePoints != 10 || *c.DonePoints != 3 {
			t.Fatalf("frozen counts = %d/%d %v/%v", *c.Scope, *c.Done, *c.ScopePoints, *c.DonePoints)
		}
		if !c.CompletedAt.Equal(c.EndsAt) || c.StartedAt == nil || !rows[1].StartedAt.Equal(rows[1].StartsAt) {
			t.Fatalf("times: completed_at=%v ends_at=%v started=%v", c.CompletedAt, c.EndsAt, rows[1].StartedAt)
		}
		for _, moved := range []TaskView{progress, todo} {
			if env.taskIteration(moved.ID) != second || env.iterationActivities(moved.ID) != 1 {
				t.Fatalf("unfinished task %s not carried over exactly once", moved.ID)
			}
		}
		for _, stay := range []TaskView{done, canceled, doneUnestimated} {
			if env.taskIteration(stay.ID) != first || env.iterationActivities(stay.ID) != 0 {
				t.Fatalf("finished task %s moved", stay.ID)
			}
		}
		var actor string
		env.scalar(&actor, `SELECT actor_id::text FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.iteration_changed'`, todo.ID)
		if actor != dispatch.WorkerActorID {
			t.Fatalf("activity actor = %s", actor)
		}
		// 幂等：同一时刻再跑一次不产生变化。
		env.schedule(cstAt(time.January, 14, 0, 30))
		if again := env.iterRows(space.ID); statusesOf(again) != statusesOf(rows) || env.iterationActivities(todo.ID) != 1 {
			t.Fatalf("not idempotent: %s", statusesOf(again))
		}
	})

	t.Run("cooldown_blocks_start_and_auto_add_joins_next_planned", func(t *testing.T) {
		space := env.autoSpace(monday, 1, 1, 2, true)
		rows := env.iterRows(space.ID)
		task := env.createTask(owner, space.ID, map[string]any{"status_id": env.statusID(env.org, "TODO"), "iteration_id": rows[0].ID})
		env.schedule(cstAt(time.January, 16, 10, 0))
		rows = env.iterRows(space.ID)
		if rows[0].Status != "completed" || rows[1].Status != "planned" || len(rows) != 3 {
			t.Fatalf("cooldown: %s", statusesOf(rows))
		}
		if env.taskIteration(task.ID) != rows[1].ID {
			t.Fatal("unfinished task not carried to the next planned iteration")
		}
		// 冷却期没有活跃迭代：开始 / 完成状态的任务自动加入下一个计划中迭代。
		fresh := env.createTask(owner, space.ID, map[string]any{"status_id": env.statusID(env.org, "TODO")})
		env.setStatus(owner, space.ID, fresh, "IN_PROGRESS")
		if env.taskIteration(fresh.ID) != rows[1].ID {
			t.Fatal("auto-add during cooldown did not join the next planned iteration")
		}
		env.schedule(cstAt(time.January, 21, 0, 30))
		if rows = env.iterRows(space.ID); rows[1].Status != "active" || !rows[1].StartedAt.Equal(rows[1].StartsAt) {
			t.Fatalf("after cooldown: %s", statusesOf(rows))
		}
	})

	t.Run("catch_up_after_downtime", func(t *testing.T) {
		space := env.autoSpace(monday, 1, 0, 2, false)
		first := env.iterRows(space.ID)[0].ID
		task := env.createTask(owner, space.ID, map[string]any{"status_id": env.statusID(env.org, "TODO"), "iteration_id": first})
		now := cstAt(time.January, 30, 10, 0) // 停机三周多
		cycles := 0
		for ; cycles < 10; cycles++ {
			before := statusesOf(env.iterRows(space.ID))
			env.schedule(now)
			if statusesOf(env.iterRows(space.ID)) == before {
				break
			}
		}
		rows := env.iterRows(space.ID)
		t.Logf("catch-up changed state in %d cycles: %s", cycles, statusesOf(rows))
		active, planned := 0, 0
		for _, r := range rows {
			switch r.Status {
			case "active":
				active++
				if r.StartsAt.After(now) || !r.EndsAt.After(now) {
					t.Fatalf("active iteration %d does not contain now", r.Number)
				}
				if env.taskIteration(task.ID) != r.ID {
					t.Fatal("task not carried into the current iteration")
				}
			case "planned":
				planned++
			case "completed":
				if r.StartedAt == nil || r.CompletedAt == nil {
					t.Fatalf("completed iteration %d lacks started_at/completed_at", r.Number)
				}
			}
		}
		if active != 1 || planned != 2 || cycles != 3 {
			t.Fatalf("active=%d planned=%d cycles=%d: %s", active, planned, cycles, statusesOf(rows))
		}
		if env.iterationActivities(task.ID) != 3 {
			t.Fatalf("hops = %d, want 3", env.iterationActivities(task.ID))
		}
	})

	t.Run("two_workers_same_organization", func(t *testing.T) {
		space := env.autoSpace(monday, 1, 0, 2, false)
		first := env.iterRows(space.ID)[0].ID
		var tasks []TaskView
		for i := 0; i < 5; i++ {
			tasks = append(tasks, env.createTask(owner, space.ID, map[string]any{"status_id": env.statusID(env.org, "TODO"), "iteration_id": first}))
		}
		now := cstAt(time.January, 14, 0, 30)
		var wait sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wait.Add(1)
			go func() {
				defer wait.Done()
				errs[i] = env.inService(now, func(ctx context.Context, s collaboration.Service, tx pgx.Tx) error {
					return s.ScheduleIterations(ctx, tx, env.org, now)
				})
			}()
		}
		wait.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		// 结果与一个 worker 相同：4 个迭代、编号连续、只有一个活跃、每个任务只顺延一次、动态不重复。
		rows := env.iterRows(space.ID)
		if len(rows) != 4 || statusesOf(rows) != "1:completed 2:active 3:planned 4:planned " {
			t.Fatalf("after two workers: %s", statusesOf(rows))
		}
		for _, task := range tasks {
			if env.taskIteration(task.ID) != rows[1].ID || env.iterationActivities(task.ID) != 1 {
				t.Fatalf("task %s: iteration=%s activities=%d", task.ID, env.taskIteration(task.ID), env.iterationActivities(task.ID))
			}
		}
	})

	t.Run("skips_retired_disabled_and_manual_spaces", func(t *testing.T) {
		retired := env.autoSpace(monday, 1, 0, 2, false)
		disabled := env.autoSpace(monday, 1, 0, 2, false)
		manual := env.autoSpace(monday, 1, 0, 2, false)
		env.setIterationSettings(disabled.ID, monday, collaboration.IterationSettingsPatch{Enabled: ptr(false)})
		env.setIterationSettings(manual.ID, monday, collaboration.IterationSettingsPatch{Mode: ptr("manual")})
		owner.must(http.StatusOK, http.MethodPost, spacePath(retired.ID)+"/retire",
			map[string]any{"version": env.getSpace(owner, retired.ID).Version, "open_tasks": "cancel"}, nil)
		env.schedule(cstAt(time.January, 14, 0, 30))
		for name, id := range map[string]string{"retired": retired.ID, "disabled": disabled.ID, "manual": manual.ID} {
			if rows := env.iterRows(id); rows[0].Status != "active" || len(rows) != 3 {
				t.Errorf("%s space was scheduled: %s", name, statusesOf(rows))
			}
		}
	})
}

// 派发 worker 经 list_iteration_due_organizations 发现到期组织并推进（真实时钟）。
func TestIterationWorkerRunIterations(t *testing.T) {
	env := newS3Env(t)
	if now := time.Now().In(shanghai); now.Hour() == 0 && now.Minute() == 0 {
		time.Sleep(61 * time.Second)
	}
	space := env.createSpace("private").ID
	if response := patchIterSettings(env, env.owner(), space, map[string]any{"enabled": true, "mode": "automatic", "start_weekday": int(time.Now().In(shanghai).Weekday()), "upcoming_count": 2}); response.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", response.Status, response.Body)
	}
	if rows := env.iterRows(space); rows[0].Status != "active" {
		t.Fatalf("not started: %s", statusesOf(rows))
	}
	env.exec(`UPDATE openfox.iterations SET starts_at = now() - interval '8 days', ends_at = now() - interval '1 minute' WHERE id = $1::uuid`, env.iterRows(space)[0].ID)
	worker := &dispatch.Worker{Pool: env.database}
	if err := worker.RunIterations(env.ctx); err != nil {
		t.Fatal(err)
	}
	rows := env.iterRows(space)
	if rows[0].Status != "completed" || rows[0].CompletedAt == nil {
		t.Fatalf("worker did not complete the due iteration: %s", statusesOf(rows))
	}
}

func TestIterationManualFlow(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.createSpace("private").ID
	patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "manual"})
	a := createIteration(env, space, "2030-02-01", "2030-02-14")
	b := createIteration(env, space, "2030-02-15", "2030-02-28")
	c := createIteration(env, space, "2030-03-01", "2030-03-14")
	d := createIteration(env, space, "2030-03-15", "2030-03-28")
	if a.Number != 1 || d.Number != 4 {
		t.Fatalf("numbers %d..%d", a.Number, d.Number)
	}
	// 日期重叠 / 逆序 / 过去的日期。
	owner.expectError(http.StatusConflict, "ITERATION_OVERLAP", http.MethodPost, iterPath(space), map[string]any{"start_date": "2030-02-10", "end_date": "2030-02-20"})
	owner.expectError(http.StatusBadRequest, "INVALID_DATE_RANGE", http.MethodPost, iterPath(space), map[string]any{"start_date": "2030-05-10", "end_date": "2030-05-01"})
	owner.expectError(http.StatusConflict, "ITERATION_OVERLAP", http.MethodPost, iterPath(space)+"/"+d.ID+"/dates", map[string]any{"version": d.Version, "start_date": "2030-03-10"})
	owner.expectError(http.StatusBadRequest, "INVALID_DATE", http.MethodPost, iterPath(space)+"/"+d.ID+"/dates", map[string]any{"version": d.Version, "start_date": "2020-01-01", "end_date": "2020-01-05"})

	mk := func(category, iteration string) TaskView {
		return env.createTask(owner, space, map[string]any{"status_id": env.statusID(env.org, category), "iteration_id": iteration})
	}
	doneTask, todoTask, progressTask := mk("DONE", a.ID), mk("TODO", a.ID), mk("IN_PROGRESS", a.ID)
	inD := mk("TODO", d.ID)

	a = iterAction(env, space, a, "start", map[string]any{})
	if a.Status != "active" || a.StartedAt == nil {
		t.Fatalf("start: %+v", a)
	}
	owner.expectError(http.StatusConflict, "ITERATION_ACTIVE_EXISTS", http.MethodPost, iterPath(space)+"/"+b.ID+"/start", map[string]any{"version": b.Version})
	owner.expectError(http.StatusConflict, "ITERATION_STATE", http.MethodDelete, iterPath(space)+"/"+a.ID+"?version="+itoa(a.Version), nil)

	// 完成：未完成任务去 next（最早的计划中迭代）。
	a = iterAction(env, space, a, "complete", map[string]any{"move_unfinished_to": "next"})
	if a.Status != "completed" || a.CompletedAt == nil || *a.ScopeCount != 3 || *a.CompletedCount != 1 {
		t.Fatalf("complete: %+v", a)
	}
	if env.taskIteration(doneTask.ID) != a.ID || env.taskIteration(todoTask.ID) != b.ID || env.taskIteration(progressTask.ID) != b.ID {
		t.Fatal("complete → next: wrong destinations")
	}
	// 完成：指定另一个计划中迭代。
	b = iterAction(env, space, b, "start", map[string]any{})
	owner.expectError(http.StatusBadRequest, "INVALID_ITERATION_TARGET", http.MethodPost, iterPath(space)+"/"+b.ID+"/complete", map[string]any{"version": b.Version, "target_iteration_id": a.ID})
	iterAction(env, space, b, "complete", map[string]any{"move_unfinished_to": "next", "target_iteration_id": c.ID})
	if env.taskIteration(todoTask.ID) != c.ID {
		t.Fatal("complete → explicit target: wrong destination")
	}
	// 完成：回到 backlog。
	c = iterAction(env, space, c, "start", map[string]any{})
	iterAction(env, space, c, "complete", map[string]any{"move_unfinished_to": "backlog"})
	if env.taskIteration(todoTask.ID) != "" || env.taskIteration(progressTask.ID) != "" || env.taskIteration(doneTask.ID) != a.ID {
		t.Fatal("complete → backlog: wrong destinations")
	}
	// 删除：必须带版本；只能删计划中 / 已取消；任务移出迭代。
	owner.expectError(http.StatusBadRequest, "", http.MethodDelete, iterPath(space)+"/"+d.ID, nil)
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, iterPath(space)+"/"+d.ID+"?version=99", nil)
	owner.expectError(http.StatusConflict, "ITERATION_STATE", http.MethodDelete, iterPath(space)+"/"+a.ID+"?version="+itoa(env.getIteration(space, a.ID).Version), nil)
	owner.must(http.StatusNoContent, http.MethodDelete, iterPath(space)+"/"+d.ID+"?version="+itoa(d.Version), nil, nil)
	if env.taskIteration(inD.ID) != "" {
		t.Fatal("task still in deleted iteration")
	}
	owner.expectError(http.StatusNotFound, "", http.MethodGet, iterPath(space)+"/"+d.ID, nil)

	// 链接：非 http(s) 拒绝；每个迭代至多 20 条，第 21 条 409 LIMIT_REACHED；删除后可再加。
	linkPath := iterPath(space) + "/" + a.ID + "/links"
	owner.expectError(http.StatusBadRequest, "INVALID_LINK", http.MethodPost, linkPath, map[string]any{"title": "x", "url": "ftp://example.test/a"})
	var link struct {
		ID string `json:"id"`
	}
	for position := 0; position < 20; position++ {
		owner.must(http.StatusCreated, http.MethodPost, linkPath, map[string]any{"title": "doc", "url": "https://example.test/doc", "position": position}, &link)
	}
	owner.expectError(http.StatusConflict, "LIMIT_REACHED", http.MethodPost, linkPath, map[string]any{"title": "x", "url": "https://example.test/21", "position": 0})
	owner.must(http.StatusNoContent, http.MethodDelete, linkPath+"/"+link.ID, nil, nil)
	owner.must(http.StatusCreated, http.MethodPost, linkPath, map[string]any{"title": "x", "url": "https://example.test/21", "position": 19}, nil)

	// 手动 → 自动：新迭代的编号接在现有之后，日期从现有计划之后开始、不与现有重叠。
	switched := env.createSpace("private").ID
	patchIterSettings(env, owner, switched, map[string]any{"enabled": true, "mode": "manual"})
	createIteration(env, switched, "2030-06-03", "2030-06-16")
	createIteration(env, switched, "2030-06-17", "2030-06-30")
	patchIterSettings(env, owner, switched, map[string]any{"mode": "automatic", "start_weekday": 1, "timezone": "Asia/Shanghai", "upcoming_count": 4})
	rows := iterList(env, owner, switched, "")
	if len(rows) != 4 {
		t.Fatalf("after switching to automatic: %d iterations", len(rows))
	}
	for index, row := range rows {
		if row.Number != index+1 || row.Status != "planned" || (index > 0 && row.StartDate <= rows[index-1].EndDate) {
			t.Fatalf("iteration %d = %+v (previous end %s)", index, row, rows[max(index-1, 0)].EndDate)
		}
	}
	if rows[2].StartDate != "2030-07-01" {
		t.Fatalf("first generated iteration starts %s, want 2030-07-01", rows[2].StartDate)
	}
}

func TestIterationAutoAdd(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	monday := cstAt(time.January, 7, 10, 0)
	on := env.autoSpace(monday, 2, 0, 2, true).ID
	off := env.autoSpace(monday, 2, 0, 2, false).ID
	manual := env.autoSpace(monday, 2, 0, 2, true).ID
	env.setIterationSettings(manual, monday, collaboration.IterationSettingsPatch{Mode: ptr("manual")})
	for _, tc := range []struct {
		name, space string
		started     bool
	}{{"on", on, true}, {"off", off, false}, {"manual", manual, false}} {
		toStarted := env.createTask(owner, tc.space, map[string]any{"status_id": env.statusID(env.org, "TODO")})
		toDone := env.createTask(owner, tc.space, map[string]any{"status_id": env.statusID(env.org, "TODO")})
		env.setStatus(owner, tc.space, toStarted, "IN_PROGRESS")
		env.setStatus(owner, tc.space, toDone, "DONE")
		active := env.iterRows(tc.space)[0].ID
		for _, task := range []TaskView{toStarted, toDone} {
			want := ""
			if tc.started {
				want = active
			}
			if got := env.taskIteration(task.ID); got != want {
				t.Errorf("%s: iteration = %q, want %q", tc.name, got, want)
			}
			if wantActivities := map[bool]int{true: 1, false: 0}[tc.started]; env.iterationActivities(task.ID) != wantActivities {
				t.Errorf("%s: iteration activities = %d", tc.name, env.iterationActivities(task.ID))
			}
		}
	}
	// 已在其他迭代里的任务不会被改动。
	planned := env.iterRows(on)[1].ID
	pinned := env.createTask(owner, on, map[string]any{"status_id": env.statusID(env.org, "TODO"), "iteration_id": planned})
	env.setStatus(owner, on, pinned, "IN_PROGRESS")
	if env.taskIteration(pinned.ID) != planned {
		t.Error("auto-add moved a task that already had an iteration")
	}
}

func TestIterationTaskAssignmentAndFilters(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	// 空间未启用迭代：任务不能带 iteration_id。
	owner.expectError(http.StatusBadRequest, "ITERATIONS_DISABLED", http.MethodPost, spacePath(env.space.ID)+"/tasks",
		map[string]any{"title": "x", "iteration_id": s3NewID(t)})

	space := env.createSpace("private").ID
	patchIterSettings(env, owner, space, map[string]any{"enabled": true, "mode": "manual"})
	i1 := createIteration(env, space, "2030-04-01", "2030-04-14")
	i2 := createIteration(env, space, "2030-04-15", "2030-04-28")
	i1 = iterAction(env, space, i1, "start", map[string]any{})
	a := env.createTask(owner, space, map[string]any{"iteration_id": i1.ID})
	b := env.createTask(owner, space, map[string]any{"iteration_id": i2.ID})
	c := env.createTask(owner, space, nil)

	ids := func(query string) map[string]bool {
		var body struct {
			Tasks []TaskView `json:"tasks"`
		}
		owner.must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?"+query, nil, &body)
		out := map[string]bool{}
		for _, task := range body.Tasks {
			out[task.ID] = true
		}
		return out
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"iteration_id=none", []string{c.ID}},
		{"iteration_id=current", []string{a.ID}},
		{"iteration_id=" + i2.ID, []string{b.ID}},
		{"iteration_id_not=" + i1.ID, []string{b.ID, c.ID}},
		{"iteration_id_not=current", []string{b.ID, c.ID}},
	} {
		got := ids(tc.query)
		if len(got) != len(tc.want) {
			t.Errorf("%s = %v, want %v", tc.query, got, tc.want)
			continue
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Errorf("%s missing %s", tc.query, id)
			}
		}
	}
	// 批量规划：加入 / 移出。
	var moved struct {
		Updated int `json:"updated"`
	}
	owner.must(http.StatusOK, http.MethodPost, iterPath(space)+"/"+i2.ID+"/tasks", map[string]any{"task_ids": []string{c.ID, a.ID}, "action": "add"}, &moved)
	if env.taskIteration(c.ID) != i2.ID || env.taskIteration(a.ID) != i2.ID {
		t.Fatal("batch add failed")
	}
	owner.must(http.StatusOK, http.MethodPost, iterPath(space)+"/"+i2.ID+"/tasks", map[string]any{"task_ids": []string{c.ID}, "action": "remove"}, nil)
	if env.taskIteration(c.ID) != "" {
		t.Fatal("batch remove failed")
	}
	// 其他空间的迭代、已完成的迭代都不能作为任务的迭代。
	otherSpace := env.createSpace("private").ID
	patchIterSettings(env, owner, otherSpace, map[string]any{"enabled": true, "mode": "manual"})
	foreign := createIteration(env, otherSpace, "2030-04-01", "2030-04-14")
	owner.expectError(http.StatusBadRequest, "INVALID_ITERATION", http.MethodPatch, spacePath(space)+"/tasks/"+c.ID, map[string]any{"version": estTask(env, space, c.ID).Version, "iteration_id": foreign.ID})
	iterAction(env, space, i1, "complete", map[string]any{"move_unfinished_to": "backlog"})
	owner.expectError(http.StatusBadRequest, "INVALID_ITERATION", http.MethodPatch, spacePath(space)+"/tasks/"+c.ID, map[string]any{"version": estTask(env, space, c.ID).Version, "iteration_id": i1.ID})
	owner.must(http.StatusOK, http.MethodPatch, spacePath(space)+"/tasks/"+c.ID, map[string]any{"version": estTask(env, space, c.ID).Version, "iteration_id": i2.ID}, nil)
}
