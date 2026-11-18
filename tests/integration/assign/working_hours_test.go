package assign

import (
	"context"
	"encoding/json"
	"openfox/internal/deviceauth"
	"openfox/internal/executiondelivery"
	"openfox/internal/resourcepolicy"
	"os"
	"sync"
	"testing"
	"time"

	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// Scheduling assertions need their own migrated fixture database: other tests
// deliberately leave queued work. No external Runtime, platform or paid call.
func TestWorkingHoursAdmissionAndMentions(t *testing.T) {
	url := newWorkingHoursDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 5 * time.Second, LockTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store, worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}}
	h.seed()
	closed := json.RawMessage(`{"enabled":true,"timezone":"UTC","days":[[],[],[],[],[],[],[]],"triggers":{"assignment":true,"comment_mention":true,"direct_chat":false}}`)
	actor := func() conversations.Actor {
		a := h.actor()
		return conversations.Actor{UserID: a.UserID, Role: a.Role, AuthorizationVersion: a.AuthorizationVersion}
	}
	setConfig := func(raw json.RawMessage) agents.Agent {
		var grant string
		var version uint64
		h.queryRow(`SELECT g.id::text,a.version FROM openfox.device_grants g JOIN openfox.agents a ON a.organization_id=g.organization_id AND a.device_id=g.device_id WHERE a.id=$1`, []any{agentA}, &grant, &version)
		a := actor()
		out, e := (agents.PostgresStore{Pool: pool}).CreateConfig(ctx, tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: a.AuthorizationVersion}, agents.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: a.AuthorizationVersion}, spaceA, agentA, version, agents.ConfigInput{ComputerID: device1, GrantID: grant, Runtime: "codex", Model: "default", MaxConcurrency: 2, QueueTimeoutSeconds: 60, RunTimeoutSeconds: 600, RunSettings: agents.RunSettings{WorkingHours: raw}})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	createTask := func(assigned bool, future *time.Time) string {
		id := newID(t)
		var refs []collaboration.TaskAssigneeRef
		if assigned {
			refs = []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}}
		}
		h.inUser(func(ctx context.Context, tx pgx.Tx) error {
			_, e := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{ID: id, TypeID: typeA, StatusID: statusTodo, Title: "A04 schedule", Assignees: &refs, AssignmentNotBefore: future})
			return e
		})
		return id
	}
	runForTask := func(id string) string {
		var run string
		h.queryRow(`SELECT r.id::text FROM openfox.runs r JOIN openfox.conversations c ON c.id=r.conversation_id WHERE c.task_id=$1 ORDER BY r.created_at DESC LIMIT 1`, []any{id}, &run)
		return run
	}
	cancelRun := func(id string) {
		h.inUser(func(ctx context.Context, tx pgx.Tx) error {
			_, e := store.Cancel(ctx, tx, orgA, id, actor(), "a04-cancel-"+id)
			return e
		})
	}
	t.Run("old omission preserves config and null disables", func(t *testing.T) {
		setConfig(closed)
		a := setConfig(nil)
		if len(a.Config.WorkingHours) == 0 {
			t.Fatal("omitted schedule lost")
		}
		a = setConfig(json.RawMessage("null"))
		if len(a.Config.WorkingHours) != 0 {
			t.Fatalf("null did not clear: %s", a.Config.WorkingHours)
		}
		setConfig(closed)
	})
	t.Run("indefinite wait no reservation and trigger now exactly once", func(t *testing.T) {
		future := time.Now().UTC().Add(48 * time.Hour)
		id := createTask(true, &future)
		r := h.runOnce()
		if r.RunsCreated != 1 || r.OffersCreated != 0 {
			t.Fatalf("pass %+v", r)
		}
		run := runForTask(id)
		h.expectRow(`SELECT schedule_pending::text||':'||(not_before IS NULL)::text||':'||(queue_expires_at IS NULL)::text||':'||wait_reason FROM openfox.runs WHERE id=$1`, []any{run}, "true:true:true:SCHEDULED")
		h.expectRow(`SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id=$1`, []any{run}, "0")
		h.expectRow(`SELECT count(*)::text FROM openfox_private.list_dispatch_organizations(NULL,100) WHERE organization_id=$1`, []any{orgA}, "0")
		if e := h.tryDevice(func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.runs SET state='PREPARING' WHERE id=$1`, run)
			return e
		}); e == nil {
			t.Fatal("scheduled run accepted preparing")
		}
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a := actor()
				errs <- pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: a.AuthorizationVersion}), func(ctx context.Context, tx pgx.Tx) error {
					_, e := store.TriggerNow(ctx, tx, orgA, run, a, "a04-now-concurrent")
					return e
				})
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		h.expectRow(`SELECT count(*)::text FROM openfox.run_idempotency WHERE operation='run.trigger_now' AND run_id=$1`, []any{run}, "1")
		h.expectRow(`SELECT schedule_pending::text||':'||working_hours_bypassed::text||':'||(requested_start_at>clock_timestamp())::text||':'||(queue_expires_at IS NOT NULL)::text FROM openfox.runs WHERE id=$1`, []any{run}, "false:true:true:true")
		r = h.runOnce()
		if r.OffersCreated != 1 {
			t.Fatalf("released pass %+v", r)
		}
		lease := h.claim(r.Offers[0].OfferID)
		h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
			_, e := store.Start(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
			return e
		})
		setConfig(closed)
		h.runOnce()
		h.expectRow(`SELECT state FROM openfox.runs WHERE id=$1`, []any{run}, "RUNNING")
		// Finish this controlled lease without invoking any Runtime.
		h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
			_, e := (executiondelivery.Service{Resources: resourcepolicy.Store{}}).AppendResult(ctx, tx, orgA, deviceauth.Principal{DeviceID: device1}, run, executiondelivery.ResultInput{ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Summary: "controlled A04 completion", Output: "done"})
			return e
		})
	})
	t.Run("only pending reschedules and offline queue times out", func(t *testing.T) {
		setConfig(closed)
		id := createTask(true, nil)
		h.runOnce()
		run := runForTask(id)
		setConfig(json.RawMessage("null"))
		h.exec(`UPDATE openfox.physical_devices SET last_seen_at=clock_timestamp()-interval '1 day' WHERE id=$1`, device1)
		r, e := h.worker.RunOrganization(ctx, orgA)
		if e != nil {
			t.Fatal(e)
		}
		if r.OffersCreated != 0 {
			t.Fatal("offline offer")
		}
		h.expectRow(`SELECT schedule_pending::text||':'||(queue_expires_at IS NOT NULL)::text FROM openfox.runs WHERE id=$1`, []any{run}, "false:true")
		setConfig(closed)
		h.worker.RunOrganization(ctx, orgA)
		h.expectRow(`SELECT schedule_pending::text FROM openfox.runs WHERE id=$1`, []any{run}, "false")
		h.exec(`UPDATE openfox.runs SET queue_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, run)
		h.worker.RunOrganization(ctx, orgA)
		h.expectRow(`SELECT state FROM openfox.runs WHERE id=$1`, []any{run}, "FAILED")
	})
	t.Run("manual lower bound also applies when schedule disabled", func(t *testing.T) {
		setConfig(json.RawMessage("null"))
		future := time.Now().UTC().Add(24 * time.Hour)
		id := createTask(true, &future)
		h.runOnce()
		run := runForTask(id)
		h.expectRow(`SELECT schedule_pending::text||':'||(not_before=requested_start_at)::text FROM openfox.runs WHERE id=$1`, []any{run}, "true:true")
		cancelRun(run)
	})
	t.Run("comment original author and duplicate gate", func(t *testing.T) {
		setConfig(closed)
		id := createTask(false, nil)
		comment := newID(t)
		for i := 0; i < 2; i++ {
			h.inUser(func(ctx context.Context, tx pgx.Tx) error {
				_, e := (conversations.Service{}).PostComment(ctx, tx, orgA, spaceA, id, actor(), conversations.CommentInput{ID: comment, Body: "please inspect", ClientCommentID: "a04-comment", Mentions: []conversations.Mention{{AgentID: agentA}}})
				return e
			})
		}
		h.expectRow(`SELECT count(*)::text FROM openfox.task_agent_events WHERE comment_id=$1`, []any{comment}, "1")
		r := h.runOnce()
		if r.RunsCreated != 1 || r.OffersCreated != 0 {
			t.Fatalf("mention pass %+v", r)
		}
		run := runForTask(id)
		h.expectRow(`SELECT actor_id::text||':'||trigger_kind||':'||schedule_pending::text FROM openfox.runs WHERE id=$1`, []any{run}, alice+":COMMENT_MENTION:true")
		cancelRun(run)
	})
	t.Run("offline edits replay in order and bounded pages", func(t *testing.T) {
		setConfig(closed)
		base := time.Now().UTC().Truncate(24 * time.Hour).Add(48 * time.Hour)
		id := createTask(true, nil)
		h.runOnce()
		run := runForTask(id)
		initial := base.Add(13 * time.Hour)
		h.exec(`UPDATE openfox.runs SET created_at=$2,not_before=$3 WHERE id=$1`, run, base.Add(11*time.Hour), initial)
		addVersion := func(at time.Time, raw json.RawMessage) int64 {
			var v int64
			h.queryRow(`SELECT current_config_version FROM openfox.agents WHERE id=$1`, []any{agentA}, &v)
			v++
			h.exec(`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,queue_timeout_seconds,run_timeout_seconds,created_by,created_at,working_hours)
 SELECT organization_id,agent_id,$2,computer_id,grant_id,runtime,model,queue_timeout_seconds,run_timeout_seconds,created_by,$3,$4 FROM openfox.agent_config_versions WHERE agent_id=$1 AND config_version=$2::bigint-1`, agentA, v, at, raw)
			h.exec(`UPDATE openfox.agents SET current_config_version=$2,version=version+1 WHERE id=$1`, agentA, v)
			return v
		}
		window := func(start string) json.RawMessage {
			cfg := map[string]any{"enabled": true, "timezone": "UTC", "days": [][]map[string]string{{}, {}, {}, {}, {}, {}, {}}, "triggers": map[string]bool{"assignment": true, "comment_mention": true, "direct_chat": false}}
			days := cfg["days"].([][]map[string]string)
			days[(int(base.Weekday())+6)%7] = []map[string]string{{"start": start, "end": "23:59"}}
			raw, _ := json.Marshal(cfg)
			return raw
		}
		addVersion(base.Add(12*time.Hour), window("14:00"))
		latest := addVersion(base.Add(13*time.Hour+30*time.Minute), window("15:00"))
		now := base.Add(14 * time.Hour)
		controlled := store
		controlled.Now = func() time.Time { return now }
		admit := func() int {
			count := 0
			h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				count, e = controlled.AdmitScheduled(ctx, tx, orgA, 50)
				return e
			})
			return count
		}
		var eventsBefore int
		h.queryRow(`SELECT count(*) FROM openfox.organization_events WHERE entity_id=$1 AND type='run.state'`, []any{run}, &eventsBefore)
		admit()
		var eventsAfter int
		h.queryRow(`SELECT count(*) FROM openfox.organization_events WHERE entity_id=$1 AND type='run.state'`, []any{run}, &eventsAfter)
		if eventsAfter != eventsBefore+1 {
			t.Fatalf("planned-time event count %d -> %d", eventsBefore, eventsAfter)
		}
		var before time.Time
		var version int64
		var pending bool
		h.queryRow(`SELECT not_before,schedule_config_version,schedule_pending FROM openfox.runs WHERE id=$1`, []any{run}, &before, &version, &pending)
		if !before.Equal(base.Add(15*time.Hour)) || version != latest || !pending {
			t.Fatalf("offline replay %v %d %v", before, version, pending)
		}
		// A later close must not suppress a window reached before that edit.
		addVersion(base.Add(16*time.Hour), closed)
		now = base.Add(17 * time.Hour)
		admit()
		h.expectRow(`SELECT schedule_pending::text||':'||(queue_expires_at=not_before+interval '60 seconds')::text FROM openfox.runs WHERE id=$1`, []any{run}, "false:true")
		// Device/capacity gates still refuse a future timestamp on the real DB clock.
		reserveErr := pool.RunForOrganization(ctx, orgA, tenancy.SourceOrganizationWorker, selector(tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}), func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `SELECT openfox_private.dispatch_reserve_capacity($1,$2,$3,1,$4,$5,$6,clock_timestamp()+interval '30 seconds')`, orgA, newID(t), run, device1, agentA, newID(t))
			return e
		})
		var pgerr *pgconn.PgError
		if !errors.As(reserveErr, &pgerr) || pgerr.Code != "40001" || pgerr.Message != "run is awaiting its scheduled time" {
			t.Fatalf("reservation gate: %v", reserveErr)
		}
		cancelRun(run)
		setConfig(closed)
		id = createTask(true, nil)
		h.runOnce()
		run = runForTask(id)
		h.exec(`UPDATE openfox.runs SET created_at=$2 WHERE id=$1`, run, base.Add(11*time.Hour))
		var startVersion int64
		h.queryRow(`SELECT schedule_config_version FROM openfox.runs WHERE id=$1`, []any{run}, &startVersion)
		// Equal timestamps use version order; all-closed followed by an open window.
		for i := 0; i < 65; i++ {
			raw := closed
			if i == 64 {
				raw = window("15:00")
			}
			latest = addVersion(base.Add(12*time.Hour), raw)
		}
		now = base.Add(14 * time.Hour)
		h.queryRow(`SELECT count(*) FROM openfox.organization_events WHERE entity_id=$1 AND type='run.state'`, []any{run}, &eventsBefore)
		if n := admit(); n != 1 {
			t.Fatalf("first page %d", n)
		}
		h.queryRow(`SELECT schedule_config_version,schedule_pending FROM openfox.runs WHERE id=$1`, []any{run}, &version, &pending)
		if version != startVersion+64 || !pending {
			t.Fatalf("checkpoint %d %v", version, pending)
		}
		h.queryRow(`SELECT count(*) FROM openfox.organization_events WHERE entity_id=$1 AND type='run.state'`, []any{run}, &eventsAfter)
		if eventsAfter != eventsBefore {
			t.Fatalf("cursor-only update emitted an event")
		}
		if n := admit(); n != 1 {
			t.Fatalf("second page %d", n)
		}
		h.queryRow(`SELECT not_before,schedule_config_version,schedule_pending FROM openfox.runs WHERE id=$1`, []any{run}, &before, &version, &pending)
		if version != latest || !pending || !before.Equal(base.Add(15*time.Hour)) {
			t.Fatalf("last page %v %d %v", before, version, pending)
		}
		if n := admit(); n != 0 {
			t.Fatalf("unchanged future repeated %d", n)
		}
		cancelRun(run)
	})

	t.Run("rollback gate refuses enabled schedules and allows drained disabled state", func(t *testing.T) {
		script, e := os.ReadFile("testdata/working-hours-rollback-check.sql")
		if e != nil {
			t.Fatal(e)
		}
		check := func() error {
			conn, e := pgx.Connect(ctx, url)
			if e != nil {
				return e
			}
			defer conn.Close(ctx)
			_, e = conn.Exec(ctx, string(script))
			return e
		}
		if e := check(); e == nil {
			t.Fatal("rollback accepted enabled schedule")
		}
		setConfig(json.RawMessage("null"))
		if e := check(); e != nil {
			t.Fatal(e)
		}
	})

}
