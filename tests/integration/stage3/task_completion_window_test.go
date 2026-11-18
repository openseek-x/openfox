package stage3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func completionAt(e *s3Env, id string) *time.Time {
	var at *time.Time
	e.scalar(&at, `SELECT completed_at FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, id)
	return at
}

func completionTx(e *s3Env, fn func(context.Context, pgx.Tx, collaboration.Actor) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: s3OwnerID, AuthorizationVersion: 1}
	actor := collaboration.Actor{UserID: s3OwnerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, actor) })
}

func TestTaskCompletionMoveAndRestore(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	task := e.createTask(o, e.space.ID, map[string]any{"status_id": e.statusID(e.org, "DONE")})
	first := completionAt(e, task.ID)
	if first == nil {
		t.Fatal("native DONE has no completion time")
	}
	target := e.createSpace("private")
	var result struct {
		Task TaskView `json:"task"`
	}
	o.must(http.StatusOK, http.MethodPost, completionTaskPath(e.space.ID, task.ID)+"/move",
		map[string]any{"version": task.Version, "target_space_id": target.ID}, &result, "Idempotency-Key", "completion-move-"+s3Suffix(t))
	if at := completionAt(e, task.ID); at == nil || !at.Equal(*first) {
		t.Fatal("move changed real completion time")
	}
	path := completionTaskPath(target.ID, task.ID)
	o.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s?version=%d", path, result.Task.Version), nil, nil)
	o.must(http.StatusOK, http.MethodPost, path+"/restore", map[string]any{"version": result.Task.Version + 1}, &task)
	if at := completionAt(e, task.ID); at == nil || !at.Equal(*first) {
		t.Fatal("soft delete or restore changed real completion time")
	}
}

func TestTaskCompletionLifecycleAndImports(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	space := e.space.ID
	done, todo := e.statusID(e.org, "DONE"), e.statusID(e.org, "TODO")
	native := e.createTask(o, space, map[string]any{"status_id": done})
	first := completionAt(e, native.ID)
	if first == nil || time.Since(*first) > time.Minute {
		t.Fatal("native DONE creation has no real time")
	}
	patch := func(body map[string]any) {
		body["version"] = native.Version
		o.must(http.StatusOK, http.MethodPatch, completionTaskPath(space, native.ID), body, &native)
	}
	patch(map[string]any{"title": "edited after completion"})
	if at := completionAt(e, native.ID); at == nil || !at.Equal(*first) {
		t.Fatal("ordinary edit moved completion")
	}
	patch(map[string]any{"status_id": todo})
	if completionAt(e, native.ID) != nil {
		t.Fatal("reopen retained completion")
	}
	patch(map[string]any{"status_id": done})
	second := completionAt(e, native.ID)
	if second == nil || !second.After(*first) {
		t.Fatal("recompletion did not get a new time")
	}
	patch(map[string]any{"status_id": e.statusID(e.org, "CANCELLED")})
	if completionAt(e, native.ID) != nil {
		t.Fatal("cancel retained completion")
	}
	patch(map[string]any{"status_id": todo})
	statuses := parallel(2, func(i int) int {
		return o.do(http.MethodPatch, completionTaskPath(space, native.ID), map[string]any{"version": native.Version, "status_id": done}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 1 || completionAt(e, native.ID) == nil {
		t.Fatalf("completion CAS: %v", statuses)
	}

	known := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	var unknownID, knownID, issueID string
	err := completionTx(e, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		svc := collaboration.Service{}
		input := collaboration.ImportTaskInput{Title: "historical done", StatusID: done, Source: "test"}
		var err error
		// No import_job marker: includes the live Issue initial-import entry.
		unknownID, _, err = svc.ImportCreateTask(ctx, tx, e.org, space, actor, input)
		if err != nil {
			return err
		}
		issueID, _, err = svc.CreateIssueTask(ctx, tx, e.org, space, actor, input)
		if err != nil {
			return err
		}
		input.CompletedAt = &known
		knownID, _, err = svc.ImportCreateTask(ctx, tx, e.org, space, actor, input)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if completionAt(e, unknownID) != nil || completionAt(e, issueID) != nil {
		t.Fatal("import invented a completion time without source")
	}
	if at := completionAt(e, knownID); at == nil || !at.Equal(known) {
		t.Fatal("known import completion was lost")
	}
	for index, status := range []string{done, todo, done} {
		err = completionTx(e, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job','controlled-test',true)`); err != nil {
				return err
			}
			_, err := (collaboration.Service{}).ImportUpdateTask(ctx, tx, e.org, space, actor, knownID,
				collaboration.ImportTaskInput{Title: "overwritten", StatusID: status, Source: "test"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if status == todo && completionAt(e, knownID) != nil {
			t.Fatal("import reopen retained time")
		}
		if index == 0 {
			if at := completionAt(e, knownID); at == nil || !at.Equal(known) {
				t.Fatal("same-DONE import without a new time discarded the known time")
			}
		}
	}
	if completionAt(e, knownID) != nil {
		t.Fatal("historical recompletion invented now")
	}

	git := e.createTask(o, space, nil)
	for _, closed := range []bool{true, false, true} {
		if err := completionTx(e, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
			_, err := (collaboration.Service{}).GitSyncStatus(ctx, tx, git.ID, closed, false, actor)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if (completionAt(e, git.ID) != nil) != closed {
			t.Fatal("Git status did not maintain completion")
		}
	}
	rollback := e.createTask(o, space, nil)
	failure := errors.New("controlled rollback")
	err = completionTx(e, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		if _, err := (collaboration.Service{}).GitApplyStatus(ctx, tx, rollback.ID, "DONE", actor); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) || completionAt(e, rollback.ID) != nil {
		t.Fatal("rolled-back completion persisted")
	}
}

func TestTaskCompletionReclassifyAndReplace(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	var status collaboration.TaskStatus
	o.must(http.StatusCreated, http.MethodPost, "task-statuses", map[string]any{"name": "T02 movable", "category": "TODO"}, &status)
	var typ collaboration.TaskType
	todo, done := e.statusID(e.org, "TODO"), e.statusID(e.org, "DONE")
	o.must(http.StatusCreated, http.MethodPost, spacePath(e.space.ID)+"/task-types", map[string]any{
		"name": "T02 type", "status_ids": []string{todo, status.ID, done}, "default_status_id": todo,
	}, &typ)
	task := e.createTask(o, e.space.ID, map[string]any{"type_id": typ.ID, "status_id": status.ID})
	move := func(category string) {
		var version int
		e.scalar(&version, `SELECT version FROM openfox.task_statuses WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, status.ID)
		o.must(http.StatusOK, http.MethodPost, "task-statuses/"+status.ID+"/move", map[string]any{"version": version, "category": category}, nil)
	}
	move("DONE")
	first := completionAt(e, task.ID)
	if first == nil {
		t.Fatal("category change did not record completion")
	}
	o.must(http.StatusOK, http.MethodPatch, completionTaskPath(e.space.ID, task.ID), map[string]any{"version": task.Version, "status_id": done}, &task)
	if at := completionAt(e, task.ID); at == nil || !at.Equal(*first) {
		t.Fatal("DONE to DONE changed time")
	}
	o.must(http.StatusOK, http.MethodPatch, completionTaskPath(e.space.ID, task.ID), map[string]any{"version": task.Version, "status_id": status.ID}, &task)
	move("TODO")
	if completionAt(e, task.ID) != nil {
		t.Fatal("category reopen retained time")
	}
	o.must(http.StatusNoContent, http.MethodDelete, "task-statuses/"+status.ID+"?replacement_status_id="+done, nil, nil)
	if completionAt(e, task.ID) == nil {
		t.Fatal("deletion replacement completion missing")
	}
}

func TestTaskCompletionWindowQueries(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	space := e.space.ID
	// US DST ends within this window: elapsed 168h remains exact.
	anchor := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	done := e.statusID(e.org, "DONE")
	for i, at := range []*time.Time{nil, ptrTime(anchor), ptrTime(anchor.Add(-168 * time.Hour)), ptrTime(anchor.Add(-168*time.Hour - time.Microsecond)), ptrTime(anchor.Add(-720 * time.Hour)), ptrTime(anchor.Add(-720*time.Hour - time.Microsecond)), ptrTime(anchor.Add(time.Microsecond))} {
		task := e.createTask(o, space, map[string]any{"title": fmt.Sprintf("completed %d", i), "status_id": done})
		e.exec(`UPDATE openfox.tasks SET completed_at=$3 WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, task.ID, at)
	}
	e.createTask(o, space, nil)
	e.createTask(o, space, map[string]any{"status_id": e.statusID(e.org, "CANCELLED")})
	var duplicate collaboration.TaskStatus
	o.must(http.StatusCreated, http.MethodPost, "task-statuses", map[string]any{"name": "T02 duplicate", "category": "DUPLICATE"}, &duplicate)
	e.createTask(o, space, map[string]any{"status_id": duplicate.ID})
	type counts struct {
		Total         int        `json:"total"`
		CompletedAsOf *time.Time `json:"completed_as_of"`
	}
	type page struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
		Next string `json:"next_cursor"`
	}
	paths := []string{spacePath(space) + "/tasks", "my-tasks"}
	for _, path := range paths {
		for _, test := range []struct {
			mode string
			want int
		}{{"all", 10}, {"hide", 1}, {"last_7_days", 3}, {"last_30_days", 5}} {
			t.Run(path+"/"+test.mode, func(t *testing.T) {
				query := "completed=" + test.mode
				if test.mode != "all" && test.mode != "hide" {
					query += "&completed_as_of=" + url.QueryEscape(anchor.Format(time.RFC3339Nano))
				}
				var c counts
				o.must(http.StatusOK, http.MethodGet, path+"/counts?"+query, nil, &c)
				if c.Total != test.want {
					t.Fatalf("counts=%d want %d", c.Total, test.want)
				}
				if (c.CompletedAsOf != nil) != (test.mode == "last_7_days" || test.mode == "last_30_days") {
					t.Fatal("anchor presence")
				}
				var stats counts
				o.must(http.StatusOK, http.MethodGet, path+"/stats?"+query, nil, &stats)
				if stats.Total != test.want {
					t.Fatalf("stats=%d", stats.Total)
				}
				for _, order := range []string{"asc", "desc"} {
					var full page
					o.must(http.StatusOK, http.MethodGet, path+"?"+query+"&sort=created&order="+order, nil, &full)
					cursor := ""
					var ids []string
					for i := 0; i < 20; i++ {
						var p page
						o.must(http.StatusOK, http.MethodGet, path+"?"+query+"&sort=created&order="+order+"&limit=2&cursor="+url.QueryEscape(cursor), nil, &p)
						for _, task := range p.Tasks {
							ids = append(ids, task.ID)
						}
						if p.Next == "" {
							break
						}
						cursor = p.Next
					}
					var wanted []string
					for _, task := range full.Tasks {
						wanted = append(wanted, task.ID)
					}
					if len(ids) != test.want || !reflect.DeepEqual(ids, wanted) {
						t.Fatalf("page mismatch %v %v", ids, wanted)
					}
				}
			})
		}
		var c counts
		o.must(http.StatusOK, http.MethodGet, path+"/counts?completed=last_7_days", nil, &c)
		if c.CompletedAsOf == nil || time.Since(*c.CompletedAsOf) > time.Minute {
			t.Fatal("missing server anchor")
		}
		var first page
		o.must(http.StatusOK, http.MethodGet, path+"?completed=last_30_days&completed_as_of="+url.QueryEscape(anchor.Format(time.RFC3339Nano))+"&sort=created&limit=1", nil, &first)
		// Cursor alone keeps the old anchor, not the current server time.
		var next page
		o.must(http.StatusOK, http.MethodGet, path+"?completed=last_30_days&sort=created&limit=1&cursor="+url.QueryEscape(first.Next), nil, &next)
		if len(next.Tasks) != 1 {
			t.Fatal("cursor lost historical window")
		}
		o.expectError(http.StatusBadRequest, "INVALID_CURSOR", http.MethodGet, path+"?completed=last_7_days&sort=created&cursor="+url.QueryEscape(first.Next), nil)
		o.expectError(http.StatusBadRequest, "INVALID_CURSOR", http.MethodGet, path+"?completed=last_30_days&sort=created&completed_as_of="+url.QueryEscape(anchor.Add(time.Hour).Format(time.RFC3339))+"&cursor="+url.QueryEscape(first.Next), nil)
		for _, bad := range []string{"completed=week", "completed=all&completed_as_of=2026-01-01T00:00:00Z", "completed=last_7_days&completed_as_of=garbage"} {
			o.expectError(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, path+"?"+bad, nil)
		}
	}
	// Other reachable users of the common builder must normalize an omitted anchor.
	o.must(http.StatusOK, http.MethodGet, "search?types=task&offset=0&completed=last_7_days", nil, nil)
	triage, _ := triageSpace(t, e)
	patchTriage(t, e, triage.ID, map[string]any{"enabled": true})
	o.must(http.StatusOK, http.MethodGet, spacePath(triage.ID)+"/triage/candidates?field=priority&completed=last_7_days", nil, nil)
	// Private-space visibility and cross-tenant boundaries apply before range filtering.
	e.other().expectError(http.StatusNotFound, "", http.MethodGet, paths[0]+"?completed=last_7_days", nil)
	e.outsider().expectError(http.StatusNotFound, "", http.MethodGet, paths[0]+"?completed=last_7_days", nil)
}

func ptrTime(value time.Time) *time.Time { return &value }

func completionTaskPath(space, task string) string { return spacePath(space) + "/tasks/" + task }
