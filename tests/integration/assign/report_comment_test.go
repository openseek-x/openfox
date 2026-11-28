package assign

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// OPE-19：智能体正式报告评论（task_comment + artifact_ids）在真实 PostgreSQL（FORCE RLS、应用角色、迁移 000176 的约束）下的行为：
// 首次发布只出一条评论、重试复用同一条评论、追加产物只补缺失附件、不存在的产物被拒绝、报告附件不能单独删除。

// newIsolatedHarness 在独立的已迁移数据库上构造 harness。其他用例会故意留下排队中的 Run，占满智能体槽位
// （max_concurrency），共用库时新 Run 拿不到 offer；与 working_hours_test.go 一样改用独立库，不依赖用例执行顺序。
func newIsolatedHarness(t *testing.T) *harness {
	t.Helper()
	url := newWorkingHoursDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 5 * time.Second, LockTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}}
	h.seed()
	return h
}

// addArtifact 给 Run 登记一个可用产物（存储对象在这里不需要真实存在：复制发生在 API 层，本测试只覆盖数据库侧）。
func (h *harness) addArtifact(runID, name string, n int) string {
	h.t.Helper()
	id := newID(h.t)
	h.exec(`INSERT INTO openfox.artifacts (organization_id, id, run_id, name, storage_key, size_bytes, sha256, mime_type)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 2048, $6, 'text/plain')`,
		orgA, id, runID, name, orgA+"/"+runID+"/"+id, fmt.Sprintf("%064x", n))
	return id
}

// prepareReport 在设备事务里校验要附带的产物，返回仍需复制的产物与业务拒绝原因。
func (h *harness) prepareReport(lease dispatch.Lease, ids []string) ([]dispatch.ReportArtifact, string) {
	h.t.Helper()
	var pending []dispatch.ReportArtifact
	var problem string
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		pending, problem, err = h.store.PrepareReport(ctx, tx, orgA, device1, lease.RunID, lease.Generation, ids)
		return err
	})
	return pending, problem
}

// reportFiles 模拟 API 层的复制结果：为每个待复制产物生成一个独立的附件对象描述。
func reportFiles(t *testing.T, pending []dispatch.ReportArtifact) []attachments.ReportFile {
	t.Helper()
	files := make([]attachments.ReportFile, 0, len(pending))
	for _, item := range pending {
		id := newID(t)
		files = append(files, attachments.ReportFile{
			ArtifactID: item.ID, AttachmentID: id, Name: item.Name,
			Info: attachments.FileInfo{Key: attachments.StorageKey(orgA, id), ContentType: "text/plain; charset=utf-8",
				SizeBytes: item.SizeBytes, SHA256: fmt.Sprintf("%064x", len(files)+1)},
		})
	}
	return files
}

// reportTool 以 task_comment 工具发布报告评论，ReportFiles 为 API 层事务外复制好的附件。
func (h *harness) reportTool(lease dispatch.Lease, callID string, ids []string, files []attachments.ReportFile) dispatch.ToolResult {
	h.t.Helper()
	raw, err := json.Marshal(map[string]any{"body": "测试报告：14 例通过，1 例失败", "artifact_ids": ids})
	if err != nil {
		h.t.Fatal(err)
	}
	var result dispatch.ToolResult
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = h.store.CallTool(ctx, tx, orgA, device1, lease.RunID, "task_comment", dispatch.ToolCall{
			Generation: lease.Generation, CallID: callID, Arguments: raw, ReportFiles: files,
		})
		return err
	})
	return result
}

func TestReportCommentAttachesArtifacts(t *testing.T) {
	h := newIsolatedHarness(t)
	taskID := newID(t)
	assignees := []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}}
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{
			ID: taskID, TypeID: typeA, StatusID: statusTodo, Title: "报告评论", Description: "OPE-19", Assignees: &assignees,
		})
		return err
	})
	pass := h.runOnce()
	if pass.RunsCreated != 1 || pass.OffersCreated != 1 {
		t.Fatalf("pass = %+v, want 1 run and 1 offer", pass)
	}
	lease := h.claim(pass.Offers[0].OfferID)
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})

	a1 := h.addArtifact(lease.RunID, "report.html", 1)
	a2 := h.addArtifact(lease.RunID, "TC-03.webm", 2)
	a3 := h.addArtifact(lease.RunID, "evidence.json", 3)

	// 1. 不存在（或不属于本轮）的产物：业务拒绝，不写任何评论。
	if pending, problem := h.prepareReport(lease, []string{a1, newID(t)}); problem == "" || pending != nil {
		t.Fatalf("unknown artifact: pending=%v problem=%q, want a rejection", pending, problem)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, "0")

	// 2. 首次发布：一条评论 + 两个附件，上传者是智能体。
	first := []string{a1, a2}
	pending, problem := h.prepareReport(lease, first)
	if problem != "" || len(pending) != 2 {
		t.Fatalf("prepare = %d pending, problem %q, want 2 pending", len(pending), problem)
	}
	posted := h.reportTool(lease, "report-1", first, reportFiles(t, pending))
	if !posted.OK || !strings.Contains(posted.Text, "with 2 attachments") || len(posted.KeptFiles) != 2 {
		t.Fatalf("first report = %+v, want OK with 2 kept files", posted)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, "1")
	h.expectRow(`SELECT count(*)::text || ':' || min(uploaded_by_agent_id::text) || ':' || (min(uploaded_by::text) IS NULL)::text
		FROM openfox.attachments WHERE task_id = $1::uuid AND comment_id IS NOT NULL`, []any{taskID}, "2:"+agentA+":true")
	var commentID string
	h.queryRow(`SELECT id::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, &commentID)

	// 3. 同样的产物再次发布（重试）：没有待复制的产物，复用同一条评论，不重复登记附件。
	pending, problem = h.prepareReport(lease, first)
	if problem != "" || len(pending) != 0 {
		t.Fatalf("retry prepare = %d pending, problem %q, want nothing to copy", len(pending), problem)
	}
	retry := h.reportTool(lease, "report-2", first, nil)
	if !retry.OK || !strings.Contains(retry.Text, "already has a report comment") || len(retry.KeptFiles) != 0 {
		t.Fatalf("retry = %+v, want the existing comment reused with nothing kept", retry)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, "1")
	h.expectRow(`SELECT count(*)::text FROM openfox.attachments WHERE comment_id = $1::uuid`, []any{commentID}, "2")

	// 4. 追加一个新产物：只补缺失的附件，仍然只有一条评论。
	all := []string{a1, a2, a3}
	pending, problem = h.prepareReport(lease, all)
	if problem != "" || len(pending) != 1 || pending[0].ID != a3 {
		t.Fatalf("extend prepare = %+v, problem %q, want only the third artifact", pending, problem)
	}
	extended := h.reportTool(lease, "report-3", all, reportFiles(t, pending))
	if !extended.OK || !strings.Contains(extended.Text, "attached 1 missing") || len(extended.KeptFiles) != 1 {
		t.Fatalf("extend = %+v, want 1 attachment added to the existing comment", extended)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, "1")
	h.expectRow(`SELECT count(*)::text FROM openfox.attachments WHERE comment_id = $1::uuid`, []any{commentID}, "3")

	// 5. 时间线用的批量查询：按评论分组返回三个附件。
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		grouped, err := attachments.ListForComments(ctx, tx, []string{commentID})
		if err != nil {
			return err
		}
		if len(grouped[commentID]) != 3 {
			t.Errorf("ListForComments = %d attachments, want 3", len(grouped[commentID]))
		}
		return nil
	})

	// 6. 报告附件随评论存在，不能单独删除。
	var attachmentID string
	h.queryRow(`SELECT id::text FROM openfox.attachments WHERE comment_id = $1::uuid ORDER BY id LIMIT 1`, []any{commentID}, &attachmentID)
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) error {
		_, err := attachments.Delete(ctx, tx, attachmentID)
		return err
	})
	if err == nil {
		t.Fatal("deleting a report attachment succeeded, want a refusal")
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.attachments WHERE comment_id = $1::uuid`, []any{commentID}, "3")
}

// 报告评论被删除后（墓碑表 + RLS 对读者隐藏）再次发布：撞上每轮唯一索引，返回明确原因，
// 不会悄悄新建第二条报告评论，也不会给已删除的评论追加附件。
func TestReportCommentDeletedIsNotRecreated(t *testing.T) {
	h := newIsolatedHarness(t)
	taskID := newID(t)
	assignees := []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}}
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{
			ID: taskID, TypeID: typeA, StatusID: statusTodo, Title: "报告评论被删除", Description: "OPE-19", Assignees: &assignees,
		})
		return err
	})
	pass := h.runOnce()
	if pass.RunsCreated != 1 || pass.OffersCreated != 1 {
		t.Fatalf("pass = %+v, want 1 run and 1 offer", pass)
	}
	lease := h.claim(pass.Offers[0].OfferID)
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})
	ids := []string{h.addArtifact(lease.RunID, "report.html", 1)}

	// 1. 首次发布成功。
	pending, problem := h.prepareReport(lease, ids)
	if problem != "" || len(pending) != 1 {
		t.Fatalf("prepare = %d pending, problem %q, want 1 pending", len(pending), problem)
	}
	if posted := h.reportTool(lease, "report-1", ids, reportFiles(t, pending)); !posted.OK {
		t.Fatalf("first report = %+v, want OK", posted)
	}
	var commentID string
	h.queryRow(`SELECT id::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, &commentID)

	// 2. 评论被删除：写墓碑，读者（RLS）看不到这条评论。
	h.exec(`INSERT INTO openfox.task_comment_deletions (organization_id, comment_id, deleted_by) VALUES ($1::uuid, $2::uuid, $3::uuid)`, orgA, commentID, alice)

	// 3. 再次发布：评论对读者不可见，产物重新变为待复制；发布撞上每轮唯一索引，得到明确的拒绝原因，附件不被留用。
	pending, problem = h.prepareReport(lease, ids)
	if problem != "" || len(pending) != 1 {
		t.Fatalf("prepare after delete = %d pending, problem %q, want 1 pending", len(pending), problem)
	}
	again := h.reportTool(lease, "report-2", ids, reportFiles(t, pending))
	if again.OK || !strings.Contains(again.Text, "unavailable") || len(again.KeptFiles) != 0 {
		t.Fatalf("report after delete = %+v, want a refusal that keeps no files", again)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.task_comments WHERE run_id = $1::uuid AND run_report`, []any{lease.RunID}, "1")
	h.expectRow(`SELECT count(*)::text FROM openfox.attachments WHERE task_id = $1::uuid AND comment_id IS NOT NULL`, []any{taskID}, "1")
}
