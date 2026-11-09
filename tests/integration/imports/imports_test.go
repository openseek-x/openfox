// Package imports 在真实 PostgreSQL（FORCE RLS、应用角色）下验证导入流程：
// 上传 → 配置 → 开始 → worker 执行 → 结果、重复导入、覆盖导入、取消、断点续跑、回滚、权限与邀请。
package imports

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/invitations"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	orgA     = "00000000-0000-0000-0000-0000000000a1"
	orgB     = "00000000-0000-0000-0000-0000000000b1"
	alice    = "00000000-0000-0000-0000-000000000001" // orgA OWNER
	bob      = "00000000-0000-0000-0000-000000000002" // orgB OWNER
	privateA = "00000000-0000-0000-0000-0000000000aa"
	sharedA  = "00000000-0000-0000-0000-0000000000a5"
	carol    = "00000000-0000-0000-0000-0000000000c1" // orgA ADMIN
	dave     = "00000000-0000-0000-0000-0000000000c2" // orgA MEMBER
	erin     = "00000000-0000-0000-0000-0000000000c3" // orgA GUEST
	newbie   = "00000000-0000-0000-0000-0000000000c4" // 邀请接受后加入

	stTodo = "00000000-0000-0000-0000-00000000d001"
	stDoin = "00000000-0000-0000-0000-00000000d002"
	stDone = "00000000-0000-0000-0000-00000000d003"
	stCanc = "00000000-0000-0000-0000-00000000d004"
	typeT  = "00000000-0000-0000-0000-00000000d101"
	typeB  = "00000000-0000-0000-0000-00000000d102"
)

// 样例 Jira CSV：重名的 Labels / Comment 列，父子任务，未知优先级，无法解析的日期，Assignee 与 Assignee Id 列，一个空行和一个缺标题行。
const sampleCSV = "\xEF\xBB\xBFSummary,Issue key,Issue id,Issue Type,Status,Priority,Assignee,Assignee Id,Created,Due date,Labels,Labels,Comment,Comment,Parent id,Description\n" +
	`Parent task,PRJ-1,10001,Task,To Do,High,alice,acc-alice,15/Mar/24 2:30 PM,20/Mar/24 10:00 AM,backend urgent,Backend,20/Mar/24 10:00 AM;acc-alice;First comment,21/Mar/24 9:00 AM;acc-zed;Hello from zed,,Parent body` + "\n" +
	`Child task,PRJ-2,10002,Task,In Review,Weird,Zed Zhang,acc-zed,not a date,,,,,,10001,` + "\n" +
	`Done task,PRJ-3,10003,Bug,Done,Low,,,2024-01-02 08:00,,,,,,,` + "\n" +
	",,,,,,,,,,,,,,,\n" +
	`,PRJ-9,10009,Task,To Do,High,,,,,,,,,,` + "\n"

type harness struct {
	t    *testing.T
	ctx  context.Context
	url  string
	pool *postgres.Pool
	svc  imports.Service
}

func (h *harness) worker(instance string) *imports.Worker {
	return &imports.Worker{Pool: h.pool, Instance: instance}
}

func TestImports(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 6, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool}
	h.seed()

	owner := h.actor(orgA, alice, policy.OrganizationRoleOwner)
	admin := h.actor(orgA, carol, policy.OrganizationRoleAdmin)
	member := h.actor(orgA, dave, policy.OrganizationRoleMember)
	guest := h.actor(orgA, erin, policy.OrganizationRoleGuest)
	w1 := h.worker("inst-1")

	file, err := imports.ParseJiraCSV(strings.NewReader(sampleCSV))
	if err != nil {
		t.Fatal(err)
	}
	if file.SkippedRows != 1 || len(file.Rows) != 4 || len(file.RowErrors) != 0 {
		t.Fatalf("parsed = skipped %d rows %d errors %v", file.SkippedRows, len(file.Rows), file.RowErrors)
	}

	var job imports.Job
	t.Run("upload and draft analysis", func(t *testing.T) {
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			job, err = h.svc.Upload(ctx, tx, orgA, owner, sharedA, "../dir/export.csv", file)
			return err
		})
		if job.Status != "DRAFT" || job.FileName != "export.csv" || job.SourceLabel != "Jira CSV" || job.ConfigVersion != 1 {
			t.Fatalf("job = %+v", job)
		}
		if !strings.HasPrefix(job.OperationID, "op_") || job.Preview == nil || job.Preview.TotalRows != 4 || job.Preview.SkippedRows != 1 {
			t.Fatalf("preview = %+v op=%s", job.Preview, job.OperationID)
		}
		a := job.Analysis
		if a == nil || a.Counts.Total != 4 || a.Counts.Invalid != 1 || a.Counts.Done != 1 || a.Counts.Open != 2 {
			t.Fatalf("analysis = %+v", a)
		}
		// "In Review" 没有同名状态也没有映射 → status_unmapped。
		if len(a.Issues) != 1 || a.Issues[0].Code != "status_unmapped" || a.Issues[0].Value != "In Review" {
			t.Fatalf("issues = %+v", a.Issues)
		}
		users := map[string]imports.AnalysisUser{}
		for _, u := range a.Users {
			users[u.SourceID] = u
		}
		if u := users["acc-alice"]; u.SuggestedUserID == nil || *u.SuggestedUserID != alice || u.Count != 2 {
			t.Fatalf("acc-alice = %+v", u)
		}
		if u := users["acc-zed"]; u.SuggestedUserID != nil || u.Name != "Zed Zhang" {
			t.Fatalf("acc-zed = %+v", u)
		}
		var withBug *string
		for _, ty := range a.Types {
			if ty.Source == "Bug" {
				withBug = ty.TypeID
			}
		}
		if withBug == nil || *withBug != typeB {
			t.Fatalf("types = %+v", a.Types)
		}
		// 草稿只有所有者 / 管理员可见；普通成员 404（看不到），访客 403。
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Get(ctx, tx, orgA, member, job.ID)
			return err
		}); !errors.Is(err, imports.ErrNotFound) {
			t.Fatalf("member get draft err = %v", err)
		}
		if err := h.try(orgA, guest, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Get(ctx, tx, orgA, guest, job.ID)
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("guest get err = %v", err)
		}
	})

	cfg := job.Config
	cfg.Statuses = []imports.StatusMapping{{Source: "In Review", NewName: "In Review", NewCategory: "IN_PROGRESS"}}
	cfg.Users = []imports.UserMapping{{SourceID: "acc-alice", Mode: "member", UserID: alice}, {SourceID: "acc-zed", Mode: "external"}}

	t.Run("config validation and cas", func(t *testing.T) {
		bad := *cfg
		bad.Columns = append([]imports.ColumnMapping(nil), cfg.Columns...)
		bad.Columns[0].Index = 99
		err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.PutConfig(ctx, tx, orgA, owner, job.ID, 1, bad)
			return err
		})
		if !errors.Is(err, imports.ErrInvalidConfig) {
			t.Fatalf("bad column err = %v", err)
		}
		bad = *cfg
		bad.Users = []imports.UserMapping{{SourceID: "x", Mode: "member"}}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.PutConfig(ctx, tx, orgA, owner, job.ID, 1, bad)
			return err
		}); !errors.Is(err, imports.ErrInvalidConfig) {
			t.Fatalf("member without user err = %v", err)
		}
		bad.Users = []imports.UserMapping{{SourceID: "x", Mode: "invite", Email: "not-an-email"}}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.PutConfig(ctx, tx, orgA, owner, job.ID, 1, bad)
			return err
		}); !errors.Is(err, imports.ErrInvalidConfig) {
			t.Fatalf("bad email err = %v", err)
		}
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.PutConfig(ctx, tx, orgA, member, job.ID, 1, *cfg)
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("member put err = %v", err)
		}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.PutConfig(ctx, tx, orgA, owner, job.ID, 7, *cfg)
			return err
		}); !errors.Is(err, imports.ErrVersionConflict) {
			t.Fatalf("stale config_version err = %v", err)
		}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			job, err = h.svc.PutConfig(ctx, tx, orgA, owner, job.ID, 1, *cfg)
			return err
		})
		if job.ConfigVersion != 2 || len(job.Analysis.Issues) != 0 {
			t.Fatalf("after put: cv=%d issues=%+v", job.ConfigVersion, job.Analysis.Issues)
		}
	})

	t.Run("start and run", func(t *testing.T) {
		// 缺确认 / 版本冲突之外的主流程：第一次 start 用旧 config_version 冲突。
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, owner, job.ID, 1, false)
			return err
		}); !errors.Is(err, imports.ErrVersionConflict) {
			t.Fatalf("stale start err = %v", err)
		}
		// member 不能开始。
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, member, job.ID, 2, false)
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("member start err = %v", err)
		}
		// 自动化：先在空间建启用的 comment / task_status 触发器，导入不应产生事件。
		h.exec(`INSERT INTO openfox.automations (organization_id, id, space_id, title, prompt, assignee_kind, assignee_id, created_by)
		        VALUES ('` + orgA + `', '00000000-0000-0000-0000-00000000aa01', '` + sharedA + `', 'on comment', 'p', 'AGENT',
		                '00000000-0000-0000-0000-0000000000a2', '` + alice + `');
		        INSERT INTO openfox.automation_triggers (organization_id, id, automation_id, space_id, kind, enabled) VALUES
		            ('` + orgA + `', gen_random_uuid(), '00000000-0000-0000-0000-00000000aa01', '` + sharedA + `', 'comment', true),
		            ('` + orgA + `', gen_random_uuid(), '00000000-0000-0000-0000-00000000aa01', '` + sharedA + `', 'task_status', true)`)
		var res imports.StartResult
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			res, err = h.svc.Start(ctx, tx, orgA, owner, job.ID, 2, false)
			return err
		})
		if res.Job.Status != "QUEUED" || !res.Started || res.Job.TotalCount != 4 || res.Job.FailedCount != 1 {
			t.Fatalf("start = %+v", res.Job)
		}
		// 幂等：再次 start 返回当前状态。
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			again, err := h.svc.Start(ctx, tx, orgA, owner, job.ID, 2, false)
			if err == nil && (again.Started || again.Job.Status != "QUEUED") {
				t.Fatalf("idempotent start = %+v", again)
			}
			return err
		})
		job = h.drain(w1, owner, job.ID)
		if job.Status != "SUCCEEDED" {
			t.Fatalf("status = %s code=%s msg=%s", job.Status, job.ErrorCode, job.ErrorMessage)
		}
		if job.CreatedCount != 3 || job.FailedCount != 1 || job.UpdatedCount != 0 || job.SkippedCount != 0 {
			t.Fatalf("counts = %+v", job)
		}
		warn := map[string]int{}
		for _, w := range job.Warnings {
			warn[w.Code] = w.Count
		}
		if warn["priority_unmapped"] != 1 || warn["date_unparsed"] != 1 {
			t.Fatalf("warnings = %+v", job.Warnings)
		}
		if len(job.Failures) != 1 || job.Failures[0].Ordinal != 3 {
			t.Fatalf("failures = %+v", job.Failures)
		}
		// 任务：数量、标题、状态、优先级、处理人、标签、评论、父子、created_at。
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, sharedA, 3)
		h.expectStr(`SELECT priority FROM openfox.tasks WHERE title = 'Parent task'`, "HIGH")
		h.expectStr(`SELECT priority FROM openfox.tasks WHERE title = 'Child task'`, "NONE")
		h.expectStr(`SELECT priority FROM openfox.tasks WHERE title = 'Done task'`, "LOW")
		h.expectStr(`SELECT ts.name FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.id = t.status_id WHERE t.title = 'Child task'`, "In Review")
		h.expectStr(`SELECT ts.name FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.id = t.status_id WHERE t.title = 'Done task'`, "Done")
		h.expectStr(`SELECT tt.name FROM openfox.tasks t JOIN openfox.task_types tt ON tt.id = t.type_id WHERE t.title = 'Done task'`, "Bug")
		h.expectStr(`SELECT assignee_id::text FROM openfox.tasks WHERE title = 'Parent task'`, alice)
		h.expectStr(`SELECT a.display_name FROM openfox.tasks t JOIN openfox.external_accounts a ON a.id = t.external_assignee_id WHERE t.title = 'Child task'`, "Zed Zhang")
		h.expectStr(`SELECT string_agg(l.name, ',' ORDER BY l.name) FROM openfox.task_labels tl JOIN openfox.labels l ON l.id = tl.label_id
		             JOIN openfox.tasks t ON t.id = tl.task_id WHERE t.title = 'Parent task'`, "backend,urgent")
		h.expectInt(`SELECT count(*) FROM openfox.task_comments c JOIN openfox.tasks t ON t.id = c.task_id WHERE t.title = 'Parent task'`, "", 2)
		h.expectStr(`SELECT c.author_user_id::text FROM openfox.task_comments c WHERE c.body = 'First comment'`, alice)
		h.expectInt(`SELECT count(*) FROM openfox.task_comments WHERE body LIKE '**Zed Zhang**%' AND body LIKE '%Hello from zed' AND author_user_id = '`+alice+`'::uuid`, "", 1)
		h.expectStr(`SELECT p.title FROM openfox.tasks c JOIN openfox.tasks p ON p.id = c.parent_task_id WHERE c.title = 'Child task'`, "Parent task")
		h.expectStr(`SELECT to_char(created_at AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD HH24:MI') FROM openfox.tasks WHERE title = 'Parent task'`, "2024-03-15 14:30")
		h.expectStr(`SELECT due_date::text FROM openfox.tasks WHERE title = 'Parent task'`, "2024-03-20")
		// 自动化没有产生事件。
		h.expectInt(`SELECT count(*) FROM openfox.automation_events WHERE space_id = $1::uuid`, sharedA, 0)
		// 条目的 task_version 与任务当前版本一致（父子关联后同步更新），回滚预览因此没有「被改过」的任务。
		h.expectInt(`SELECT count(*) FROM openfox.import_items i JOIN openfox.tasks t ON t.id = i.task_id WHERE i.job_id = $1::uuid AND t.version <> i.task_version`, job.ID, 0)
		// 列表可见性：member 可见共享空间的记录。
		var list imports.ListResult
		h.run(orgA, member, func(ctx context.Context, tx pgx.Tx) (err error) {
			list, err = h.svc.List(ctx, tx, orgA, member, 0, "")
			return err
		})
		if list.Total != 1 || len(list.Imports) != 1 || list.CanManage || list.HasActive {
			t.Fatalf("member list = %+v", list)
		}
		if list.Imports[0].Rollback.Eligible != false && list.Imports[0].Rollback.Reason != "" {
			t.Fatalf("rollback info = %+v", list.Imports[0].Rollback)
		}
	})

	t.Run("reimport without overwrite skips", func(t *testing.T) {
		second := h.uploadAndStart(owner, file, cfg, sharedA, false)
		second = h.drain(w1, owner, second.ID)
		if second.Status != "SUCCEEDED" || second.SkippedCount != 3+0 || second.CreatedCount != 0 || second.FailedCount != 1 {
			t.Fatalf("second = %+v", second)
		}
		h.expectInt(`SELECT count(*) FROM openfox.import_items WHERE job_id = $1::uuid AND action = 'skipped_existing'`, second.ID, 3)
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, sharedA, 3)
	})

	t.Run("overwrite requires confirmation and overwrites", func(t *testing.T) {
		h.exec(`UPDATE openfox.tasks SET title = 'Local edit', version = version + 1 WHERE title = 'Parent task'`)
		ocfg := *cfg
		ocfg.Options.Overwrite = true
		draft := h.upload(owner, file, sharedA)
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			draft, err = h.svc.PutConfig(ctx, tx, orgA, owner, draft.ID, 1, ocfg)
			return err
		})
		if draft.Analysis.Overwrite.Existing != 3 || draft.Analysis.Overwrite.Modified != 1 {
			t.Fatalf("overwrite analysis = %+v", draft.Analysis.Overwrite)
		}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, owner, draft.ID, 2, false)
			return err
		}); !errors.Is(err, imports.ErrOverwriteConfirmation) {
			t.Fatalf("unconfirmed start err = %v", err)
		}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, owner, draft.ID, 2, true)
			return err
		})
		done := h.drain(w1, owner, draft.ID)
		if done.Status != "SUCCEEDED" || done.UpdatedCount != 3 || done.CreatedCount != 0 {
			t.Fatalf("overwrite job = %+v", done)
		}
		h.expectStr(`SELECT title FROM openfox.tasks WHERE id = (SELECT task_id FROM openfox.import_mappings WHERE external_id = '10001' AND released_at IS NULL)`, "Parent task")
		// 评论只补不重。
		h.expectInt(`SELECT count(*) FROM openfox.task_comments c JOIN openfox.tasks t ON t.id = c.task_id WHERE t.title = 'Parent task'`, "", 2)
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, sharedA, 3)
	})

	t.Run("cancel queued and running", func(t *testing.T) {
		queued := h.uploadAndStart(owner, file, cfg, sharedA, false)
		var cancelled imports.Job
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			cancelled, err = h.svc.Cancel(ctx, tx, orgA, owner, queued.ID, queued.Version)
			return err
		})
		if cancelled.Status != "CANCELLED" || cancelled.FinishedAt == nil {
			t.Fatalf("queued cancel = %+v", cancelled)
		}
		// RUNNING：用只跑一批的 worker 让 job 进入 RUNNING，再请求取消，下一批收尾。
		running := h.uploadAndStart(owner, file, cfg, sharedA, false)
		one := h.worker("inst-1")
		one.MaxBatches = 1
		if err := one.RunOrganization(h.ctx, orgA); err != nil {
			t.Fatal(err)
		}
		current := h.get(owner, running.ID)
		if current.Status != "RUNNING" {
			t.Fatalf("expected RUNNING, got %s", current.Status)
		}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			cancelled, err = h.svc.Cancel(ctx, tx, orgA, owner, current.ID, current.Version)
			return err
		})
		if cancelled.Status != "CANCEL_REQUESTED" {
			t.Fatalf("running cancel = %s", cancelled.Status)
		}
		if err := one.RunOrganization(h.ctx, orgA); err != nil {
			t.Fatal(err)
		}
		if final := h.get(owner, running.ID); final.Status != "CANCELLED" || final.FinishedAt == nil {
			t.Fatalf("final = %+v", final)
		}
	})

	t.Run("resume after lease expiry by another instance", func(t *testing.T) {
		// 新空间：没有映射，创建 3 个任务；实例 1 只处理一批，租约过期后实例 2 接手。
		h.exec(`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ('` + orgA + `', '` + resumeSpace + `', 'Resume space', 'SHARED', 'RSM')`)
		h.seedTypes(resumeSpace, "00000000-0000-0000-0000-00000000d201")
		j := h.uploadAndStart(owner, file, cfg, resumeSpace, false)
		first := h.worker("inst-1")
		first.MaxBatches = 2 // validating + 一批 importing（全部 4 行在一批内处理完）
		if err := first.RunOrganization(h.ctx, orgA); err != nil {
			t.Fatal(err)
		}
		h.exec(`UPDATE openfox.import_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = '` + j.ID + `'`)
		second := h.worker("inst-2")
		done := h.drain(second, owner, j.ID)
		if done.Status != "SUCCEEDED" || done.CreatedCount != 3 {
			t.Fatalf("resumed job = %+v", done)
		}
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, resumeSpace, 3)
		h.expectInt(`SELECT attempts FROM openfox.import_jobs WHERE id = $1::uuid`, j.ID, 2)
	})

	t.Run("rollback", func(t *testing.T) {
		// 使用独立空间，保证状态 / 标签清理只受本 job 影响。
		h.exec(`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ('` + orgA + `', '` + rollbackSpace + `', 'Rollback space', 'SHARED', 'RBK')`)
		h.seedTypes(rollbackSpace, "00000000-0000-0000-0000-00000000d301")
		// 先用另一份 CSV 创建一个新状态 "Rollbackable"（其它空间不存在的标签 / 状态）。
		csv := "Summary,Issue key,Issue id,Status,Labels\nR one,RB-1,20001,Rollbackable,rbtag\nR two,RB-2,20002,Rollbackable,rbtag\n"
		parsed, err := imports.ParseJiraCSV(strings.NewReader(csv))
		if err != nil {
			t.Fatal(err)
		}
		draft := h.uploadParsed(owner, parsed, rollbackSpace)
		rcfg := *draft.Config
		rcfg.Statuses = []imports.StatusMapping{{Source: "Rollbackable", NewName: "Rollbackable", NewCategory: "TODO"}}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			draft, err = h.svc.PutConfig(ctx, tx, orgA, owner, draft.ID, 1, rcfg)
			return err
		})
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, owner, draft.ID, 2, false)
			return err
		})
		done := h.drain(w1, owner, draft.ID)
		if done.Status != "SUCCEEDED" || done.CreatedCount != 2 || !done.Rollback.Eligible {
			t.Fatalf("rollback job = %+v", done)
		}
		h.expectInt(`SELECT count(*) FROM openfox.import_created_resources WHERE job_id = $1::uuid AND kind = 'STATUS'`, done.ID, 1)
		h.expectInt(`SELECT count(*) FROM openfox.import_created_resources WHERE job_id = $1::uuid AND kind = 'LABEL'`, done.ID, 1)
		// 本地改一个任务 → 预览里列出。
		h.exec(`UPDATE openfox.tasks SET title = 'R one edited', version = version + 1 WHERE title = 'R one'`)
		var preview imports.RollbackPreview
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			preview, err = h.svc.RollbackPreview(ctx, tx, orgA, owner, done.ID)
			return err
		})
		if !preview.Eligible || preview.CreatedCount != 2 || preview.ModifiedTotal != 1 || len(preview.Modified) != 1 ||
			preview.Modified[0].Title != "R one edited" || !strings.HasPrefix(preview.Modified[0].DisplayID, "RBK-") ||
			preview.Cleanup.Statuses != 1 || preview.Cleanup.Labels != 1 {
			t.Fatalf("preview = %+v", preview)
		}
		// member 不能预览 / 回滚。
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Rollback(ctx, tx, orgA, member, done.ID, done.Version)
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("member rollback err = %v", err)
		}
		// 版本冲突。
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Rollback(ctx, tx, orgA, owner, done.ID, done.Version+5)
			return err
		}); !errors.Is(err, imports.ErrVersionConflict) {
			t.Fatalf("stale rollback err = %v", err)
		}
		var rolled imports.Job
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			rolled, err = h.svc.Rollback(ctx, tx, orgA, owner, done.ID, done.Version)
			return err
		})
		if rolled.Status != "ROLLED_BACK" || rolled.RolledBackAt == nil || rolled.Rollback.Reason != "rolled_back" {
			t.Fatalf("rolled = %+v", rolled)
		}
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, rollbackSpace, 0)
		h.expectInt(`SELECT count(*) FROM openfox.import_mappings WHERE space_id = $1::uuid AND released_at IS NULL`, rollbackSpace, 0)
		h.expectInt(`SELECT count(*) FROM openfox.task_statuses WHERE name = 'Rollbackable' AND archived_at IS NOT NULL`, "", 1)
		h.expectInt(`SELECT count(*) FROM openfox.workflow_statuses ws JOIN openfox.task_statuses ts ON ts.id = ws.status_id WHERE ts.name = 'Rollbackable' AND ws.space_id = $1::uuid`, rollbackSpace, 0)
		h.expectInt(`SELECT count(*) FROM openfox.labels WHERE name = 'rbtag' AND archived_at IS NOT NULL`, "", 1)
		h.expectInt(`SELECT count(*) FROM openfox.import_created_resources WHERE job_id = $1::uuid AND cleaned_at IS NULL`, done.ID, 0)
		// 再次回滚：不可回滚。
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Rollback(ctx, tx, orgA, owner, done.ID, rolled.Version)
			return err
		}); !errors.Is(err, imports.ErrRollbackUnavailable) || imports.Detail(err) != "rolled_back" {
			t.Fatalf("second rollback err = %v", err)
		}
		// 再次导入会重新创建（映射已释放；归档的状态 / 标签恢复）。
		again := h.uploadParsed(owner, parsed, rollbackSpace)
		acfg := *again.Config
		acfg.Statuses = rcfg.Statuses
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			again, err = h.svc.PutConfig(ctx, tx, orgA, owner, again.ID, 1, acfg)
			return err
		})
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Start(ctx, tx, orgA, owner, again.ID, 2, false)
			return err
		})
		again = h.drain(w1, owner, again.ID)
		if again.Status != "SUCCEEDED" || again.CreatedCount != 2 {
			t.Fatalf("reimport after rollback = %+v", again)
		}
		h.expectInt(`SELECT count(*) FROM openfox.task_statuses WHERE name = 'Rollbackable' AND archived_at IS NULL`, "", 1)
		// 超过 7 天：expired。
		h.exec(`UPDATE openfox.import_jobs SET finished_at = now() - interval '8 days' WHERE id = '` + again.ID + `'`)
		got := h.get(owner, again.ID)
		if got.Rollback.Eligible || got.Rollback.Reason != "expired" {
			t.Fatalf("expired rollback info = %+v", got.Rollback)
		}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Rollback(ctx, tx, orgA, owner, got.ID, got.Version)
			return err
		}); !errors.Is(err, imports.ErrRollbackUnavailable) || imports.Detail(err) != "expired" {
			t.Fatalf("expired rollback err = %v", err)
		}
	})

	t.Run("visibility and permissions", func(t *testing.T) {
		// 私有空间的记录：member 看不到（列表与详情），admin 看到全部。
		priv := h.uploadParsed(owner, file, privateA)
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Cancel(ctx, tx, orgA, owner, priv.ID, priv.Version)
			return err
		})
		// 已丢弃的草稿不在列表里；改为在私有空间造一条完成的记录。
		privDone := h.uploadAndStart(owner, file, cfg, privateA, false)
		privDone = h.drain(w1, owner, privDone.ID)
		var memberList, adminList imports.ListResult
		h.run(orgA, member, func(ctx context.Context, tx pgx.Tx) (err error) {
			memberList, err = h.svc.List(ctx, tx, orgA, member, 50, "")
			return err
		})
		h.run(orgA, admin, func(ctx context.Context, tx pgx.Tx) (err error) {
			adminList, err = h.svc.List(ctx, tx, orgA, admin, 50, "")
			return err
		})
		for _, item := range memberList.Imports {
			if item.Space() == privateA {
				t.Fatalf("member can see private-space import %s", item.ID)
			}
		}
		if adminList.Total != memberList.Total+1 || !adminList.CanManage {
			t.Fatalf("totals: admin %d member %d", adminList.Total, memberList.Total)
		}
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Get(ctx, tx, orgA, member, privDone.ID)
			return err
		}); !errors.Is(err, imports.ErrNotFound) {
			t.Fatalf("member get private err = %v", err)
		}
		if err := h.try(orgA, guest, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.List(ctx, tx, orgA, guest, 5, "")
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("guest list err = %v", err)
		}
		if err := h.try(orgA, member, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Upload(ctx, tx, orgA, member, sharedA, "x.csv", file)
			return err
		}); !errors.Is(err, imports.ErrForbidden) {
			t.Fatalf("member upload err = %v", err)
		}
		// 分页：limit=1 的游标。
		var page1, page2 imports.ListResult
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			page1, err = h.svc.List(ctx, tx, orgA, owner, 1, "")
			return err
		})
		if len(page1.Imports) != 1 || page1.NextCursor == "" {
			t.Fatalf("page1 = %+v", page1)
		}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			page2, err = h.svc.List(ctx, tx, orgA, owner, 1, page1.NextCursor)
			return err
		})
		if len(page2.Imports) != 1 || page2.Imports[0].ID == page1.Imports[0].ID {
			t.Fatalf("page2 = %+v", page2)
		}
		if err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.List(ctx, tx, orgA, owner, 1, "not-a-cursor!")
			return err
		}); !errors.Is(err, imports.ErrInvalidRequest) {
			t.Fatalf("bad cursor err = %v", err)
		}
		// 跨组织：orgB 的所有者看不到 orgA 的记录。
		bobActor := h.actor(orgB, bob, policy.OrganizationRoleOwner)
		var other imports.ListResult
		h.run(orgB, bobActor, func(ctx context.Context, tx pgx.Tx) (err error) {
			other, err = h.svc.List(ctx, tx, orgB, bobActor, 50, "")
			return err
		})
		if other.Total != 0 {
			t.Fatalf("cross-org total = %d", other.Total)
		}
		if err := h.try(orgB, bobActor, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.svc.Get(ctx, tx, orgB, bobActor, privDone.ID)
			return err
		}); !errors.Is(err, imports.ErrNotFound) {
			t.Fatalf("cross-org get err = %v", err)
		}
	})

	t.Run("invite flow", func(t *testing.T) {
		h.exec(`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ('` + orgA + `', '` + inviteSpace + `', 'Invite space', 'SHARED', 'INV')`)
		h.seedTypes(inviteSpace, "00000000-0000-0000-0000-00000000d401")
		csv := "Summary,Issue id,Assignee,Assignee Id\nInvite one,30001,Newbie Nina,acc-nina\n"
		parsed, err := imports.ParseJiraCSV(strings.NewReader(csv))
		if err != nil {
			t.Fatal(err)
		}
		draft := h.uploadParsed(owner, parsed, inviteSpace)
		icfg := *draft.Config
		icfg.Users = []imports.UserMapping{{SourceID: "acc-nina", Mode: "invite", Email: " Newbie@Example.test "}}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			draft, err = h.svc.PutConfig(ctx, tx, orgA, owner, draft.ID, 1, icfg)
			return err
		})
		var res imports.StartResult
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			res, err = h.svc.Start(ctx, tx, orgA, owner, draft.ID, 2, false)
			return err
		})
		if len(res.Invites) != 1 || res.Invites[0].Email != "newbie@example.test" {
			t.Fatalf("invites = %+v", res.Invites)
		}
		// 第二段（handler 里做的事）：没有 EmailSender 时邀请仍创建。
		selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: owner.AuthorizationVersion}
		invit, _, err := (invitations.Service{Pool: h.pool}).Create(h.ctx, selection, "OWNER",
			invitations.CreateInput{Email: res.Invites[0].Email, Role: "MEMBER", SpaceIDs: []string{inviteSpace}})
		if err != nil {
			t.Fatal(err)
		}
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			return h.svc.RecordInvitations(ctx, tx, draft.ID, map[string]string{res.Invites[0].AccountID: invit.ID},
				[]imports.InvitationResult{{SourceID: "acc-nina", Email: res.Invites[0].Email, Result: "failed"}})
		})
		done := h.drain(w1, owner, draft.ID)
		if done.Status != "SUCCEEDED" || len(done.Invitations) != 1 || done.Invitations[0].Result != "failed" {
			t.Fatalf("invite job = %+v", done)
		}
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND external_assignee_id IS NOT NULL AND assignee_id IS NULL`, inviteSpace, 1)
		// 外部账号处理人在任务查询里输出为 external_assignee，且标记待邀请。
		var task collaboration.Task
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			page, err := (collaboration.Service{}).ListTasks(ctx, tx, orgA, inviteSpace, owner, collaboration.TaskListOptions{Limit: 10})
			if err != nil {
				return err
			}
			if len(page.Items) != 1 {
				t.Fatalf("tasks = %d", len(page.Items))
			}
			task = page.Items[0]
			return nil
		})
		if task.ExternalAssignee == nil || task.ExternalAssignee.Name != "Newbie Nina" || !task.ExternalAssignee.PendingInvitation ||
			task.ExternalAssignee.Email != "newbie@example.test" {
			t.Fatalf("external assignee = %+v", task.ExternalAssignee)
		}
		// 模拟接受邀请：邀请置 ACCEPTED，用户入组。
		h.exec(`INSERT INTO openfox.users (id, email) VALUES ('` + newbie + `', 'newbie@example.test');
		        INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ('` + orgA + `', '` + newbie + `', 'MEMBER');
		        UPDATE openfox.invitations SET status = 'ACCEPTED', consumed_at = now() WHERE id = '` + invit.ID + `'`)
		if err := w1.RunOrganization(h.ctx, orgA); err != nil {
			t.Fatal(err)
		}
		h.expectStr(`SELECT assignee_id::text FROM openfox.tasks WHERE space_id = '`+inviteSpace+`'`, "", newbie)
		h.expectInt(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND external_assignee_id IS NOT NULL`, inviteSpace, 0)
		h.expectInt(`SELECT count(*) FROM openfox.task_assignees WHERE user_id = $1::uuid`, newbie, 1)
		h.expectStr(`SELECT linked_user_id::text FROM openfox.external_accounts WHERE external_user_id = 'acc-nina'`, newbie)
		h.expectInt(`SELECT count(*) FROM openfox.task_activities a JOIN openfox.tasks t ON t.id = a.task_id WHERE a.kind = 'task.assignee_linked' AND t.space_id = $1::uuid`, inviteSpace, 1)
	})
}

const (
	resumeSpace   = "00000000-0000-0000-0000-0000000000a6"
	rollbackSpace = "00000000-0000-0000-0000-0000000000a7"
	inviteSpace   = "00000000-0000-0000-0000-0000000000a8"
)

// ---- 辅助 ----

func (h *harness) upload(owner collaboration.Actor, file imports.ParsedFile, space string) imports.Job {
	return h.uploadParsed(owner, file, space)
}

func (h *harness) uploadParsed(owner collaboration.Actor, file imports.ParsedFile, space string) imports.Job {
	h.t.Helper()
	var job imports.Job
	h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
		job, err = h.svc.Upload(ctx, tx, orgA, owner, space, "sample.csv", file)
		return err
	})
	return job
}

func (h *harness) uploadAndStart(owner collaboration.Actor, file imports.ParsedFile, cfg *imports.Config, space string, confirm bool) imports.Job {
	h.t.Helper()
	draft := h.uploadParsed(owner, file, space)
	h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
		draft, err = h.svc.PutConfig(ctx, tx, orgA, owner, draft.ID, 1, *cfg)
		return err
	})
	var res imports.StartResult
	h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
		res, err = h.svc.Start(ctx, tx, orgA, owner, draft.ID, 2, confirm)
		return err
	})
	return res.Job
}

func (h *harness) get(actor collaboration.Actor, id string) imports.Job {
	h.t.Helper()
	var job imports.Job
	h.run(orgA, actor, func(ctx context.Context, tx pgx.Tx) (err error) {
		job, err = h.svc.Get(ctx, tx, orgA, actor, id)
		return err
	})
	return job
}

// drain 反复跑 worker 直到 job 进入终态（最多 40 轮）。
func (h *harness) drain(w *imports.Worker, actor collaboration.Actor, id string) imports.Job {
	h.t.Helper()
	for i := 0; i < 40; i++ {
		if err := w.RunOrganization(h.ctx, orgA); err != nil {
			h.t.Fatalf("worker pass: %v", err)
		}
		job := h.get(actor, id)
		switch job.Status {
		case "SUCCEEDED", "FAILED", "CANCELLED", "ROLLED_BACK":
			return job
		}
	}
	h.t.Fatalf("job %s did not finish", id)
	return imports.Job{}
}

func (h *harness) seed() {
	h.t.Helper()
	h.exec(`
INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
    SELECT id, slug, name, created_at FROM openfox.organizations ON CONFLICT DO NOTHING;
INSERT INTO openfox.users (id, email, full_name) VALUES
    ('` + carol + `', 'carol@example.test', 'Carol'), ('` + dave + `', 'dave@example.test', 'Dave'), ('` + erin + `', 'erin@example.test', 'Erin');
INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
    ('` + orgA + `', '` + carol + `', 'ADMIN'), ('` + orgA + `', '` + dave + `', 'MEMBER'), ('` + orgA + `', '` + erin + `', 'GUEST');
INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ('` + orgA + `', '` + sharedA + `', 'Shared space', 'SHARED', 'SHR');
INSERT INTO openfox.task_statuses (organization_id, id, name, category, position, is_default) VALUES
    ('` + orgA + `', '` + stTodo + `', 'To Do', 'TODO', 1, true), ('` + orgA + `', '` + stDoin + `', 'Doing', 'IN_PROGRESS', 2, false),
    ('` + orgA + `', '` + stDone + `', 'Done', 'DONE', 3, false), ('` + orgA + `', '` + stCanc + `', 'Cancelled', 'CANCELLED', 4, false);`)
	h.seedTypes(sharedA, typeT)
	h.seedTypes(privateA, "00000000-0000-0000-0000-00000000d501")
	// Bug 类型（同样的工作流）。
	h.exec(`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ('` + orgA + `', '` + sharedA + `', '` + typeB + `', 'Bug', false);
	        INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
	        SELECT '` + orgA + `', '` + sharedA + `', '` + typeB + `', id, position, is_default FROM openfox.task_statuses WHERE organization_id = '` + orgA + `'`)
}

// seedTypes 给空间建默认类型 Task，并带上组织全部状态。
func (h *harness) seedTypes(space, typeID string) {
	h.t.Helper()
	h.exec(`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ('` + orgA + `', '` + space + `', '` + typeID + `', 'Task', true);
	        INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
	        SELECT '` + orgA + `', '` + space + `', '` + typeID + `', id, position, is_default FROM openfox.task_statuses WHERE organization_id = '` + orgA + `' AND archived_at IS NULL`)
}

func (h *harness) actor(org, user string, role policy.OrganizationRole) collaboration.Actor {
	h.t.Helper()
	var version int64
	h.queryRow(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`,
		[]any{org, user}, &version)
	return collaboration.Actor{UserID: user, Role: role, AuthorizationVersion: uint64(version)}
}

func (h *harness) try(org string, actor collaboration.Actor, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: actor.UserID, AuthorizationVersion: actor.AuthorizationVersion}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
	return h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector, fn)
}

func (h *harness) run(org string, actor collaboration.Actor, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.try(org, actor, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if _, err := conn.Exec(h.ctx, query, args...); err != nil {
		h.t.Fatalf("%v", err)
	}
}

func (h *harness) queryRow(query string, args []any, dest ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if err := conn.QueryRow(h.ctx, query, args...).Scan(dest...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

// expectStr 的 SQL 可不带参数（args 为空时用无参查询）。
func (h *harness) expectStr(query, want string, more ...string) {
	h.t.Helper()
	if len(more) > 0 { // expectStr(query, ignoredArg, want)
		want = more[0]
	}
	var got *string
	h.queryRow(query, nil, &got)
	if got == nil || *got != want {
		h.t.Fatalf("%s = %v, want %q", query, got, want)
	}
}

func (h *harness) expectInt(query string, arg any, want int) {
	h.t.Helper()
	var got int
	if s, ok := arg.(string); ok && s == "" {
		h.queryRow(query, nil, &got)
	} else {
		h.queryRow(query, []any{arg}, &got)
	}
	if got != want {
		h.t.Fatalf("%s [%v] = %d, want %d", query, arg, got, want)
	}
}
