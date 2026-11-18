package stage3

import (
	"net/http"
	"testing"
	"time"
)

func TestReviewInactiveSpaceSchedules(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "停用", true: "删除"}[deleted], func(t *testing.T) {
			env := newS3Env(t)
			ensureDirectory(env)
			agent := env.insertAgent(env.space.ID)
			a := env.createAutomation(agent, scheduleTrigger("hourly", map[string]any{"minute": 0}))
			env.exec(`UPDATE openfox.automation_triggers SET next_run_at=clock_timestamp()-interval '2 hours' WHERE automation_id=$1::uuid`, a.Automation.ID)
			env.exec(`UPDATE openfox.spaces SET status='ARCHIVED',deleted_by=CASE WHEN $2 THEN $3::uuid ELSE NULL END,deleted_at=CASE WHEN $2 THEN clock_timestamp() ELSE NULL END WHERE id=$1::uuid`, env.space.ID, deleted, s3OwnerID)
			env.runWorker(time.Now())
			if n := env.runCount(a.Automation.ID, "schedule"); n != 0 {
				t.Fatalf("非活动空间产生 %d 次运行", n)
			}
		})
	}
}
func TestReviewOrdinaryListRejectsTriageSort(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	for range 2 {
		env.createTask(owner, env.space.ID, map[string]any{"title": "普通任务"})
	}
	owner.expectError(400, "", http.MethodGet, "spaces/"+env.space.ID+"/tasks?sort=triage_entered&limit=1", nil)
	owner.expectError(400, "", http.MethodGet, "my-tasks?sort=triage_entered&limit=1", nil)
}
func TestReviewMergePreservesProjectLabels(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	p := env.newProject(owner, env.space.ID, nil)
	var src, dst string
	env.scalar(&src, `INSERT INTO openfox.labels(organization_id,name) VALUES($1::uuid,'来源') RETURNING id::text`, env.org)
	env.scalar(&dst, `INSERT INTO openfox.labels(organization_id,name) VALUES($1::uuid,'目标') RETURNING id::text`, env.org)
	owner.must(204, http.MethodPut, "projects/"+p.ID+"/labels", map[string]any{"label_ids": []string{src}}, nil)
	owner.must(200, http.MethodPost, "labels/"+src+"/merge", map[string]any{"target_label_id": dst}, nil)
	got := env.getProject(owner, p.ID)
	if len(got.Labels) != 1 || got.Labels[0].ID != dst {
		t.Fatalf("合并丢失专项标签: %+v", got.Labels)
	}
}
