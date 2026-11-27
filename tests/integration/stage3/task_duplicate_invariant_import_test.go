package stage3

// OPE-17 · D1 第二期：导入的覆盖更新（ImportUpdateTask）也要守住不变量。
// 不带状态、或仍在重复类别时保留目标；改到别的类别时同一条 UPDATE 清空目标。

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

func TestDuplicateInvariantImportUpdate(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	todo := env.statusID(env.org, "TODO")
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "status_id": todo, "title": title + " " + s3Suffix(t)})
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}
	mark := func(task triageTaskView, targetID string) triageTaskView {
		var marked triageTaskView
		env.member().must(http.StatusOK, http.MethodPost, base+task.ID+"/duplicate",
			map[string]any{"version": task.Version, "duplicate_of_task_id": targetID}, &marked)
		if marked.StatusCategory != "DUPLICATE" || !hasTarget(task.ID) {
			t.Fatalf("marked = %+v", marked)
		}
		return marked
	}
	importUpdate := func(taskID, statusID string) {
		t.Helper()
		err := completionTx(env, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job','controlled-test',true)`); err != nil {
				return err
			}
			_, err := (collaboration.Service{}).ImportUpdateTask(ctx, tx, env.org, space.ID, actor, taskID,
				collaboration.ImportTaskInput{Title: "overwritten", StatusID: statusID, Source: "test"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	target := create("Target")
	source := mark(create("Source"), target.ID)

	// 不带状态的覆盖导入：状态不变，重复目标保留。
	importUpdate(source.ID, "")
	if got := getTriageTask(t, env.member(), space.ID, source.ID); got.StatusCategory != "DUPLICATE" || got.DuplicateOf == nil || got.DuplicateOf.ID != target.ID || !hasTarget(source.ID) {
		t.Fatalf("import without status lost the duplicate target: %+v", got)
	}

	// 仍然写同一个重复状态：目标保留。
	importUpdate(source.ID, source.StatusID)
	if got := getTriageTask(t, env.member(), space.ID, source.ID); got.StatusCategory != "DUPLICATE" || got.DuplicateOf == nil || !hasTarget(source.ID) {
		t.Fatalf("import into the same duplicate status lost the target: %+v", got)
	}

	// 改到 TODO：任务离开重复类别，目标在同一条 UPDATE 里清空。
	importUpdate(source.ID, todo)
	if got := getTriageTask(t, env.member(), space.ID, source.ID); got.StatusCategory != "TODO" || got.DuplicateOf != nil || hasTarget(source.ID) {
		t.Fatalf("import out of duplicate kept the target: %+v", got)
	}
}
