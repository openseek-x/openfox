package imports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/imports"
	"openfox/internal/platform/postgres"
)

// Uses the real authenticated HTTP harness and FORCE RLS app role; no provider credentials.
func priorityHTTP(t *testing.T, ctx context.Context, super *pgx.Conn, pool *postgres.Pool, owner, member *httpClient, org, space string) {
	base := "/v1/organizations/" + org
	type settings struct {
		Version uint64 `json:"version"`
		Levels  int    `json:"priority_levels"`
	}
	current := func() settings { var v settings; owner.json(200, "GET", base+"/settings", nil, &v); return v }
	levels := func(n int) {
		owner.json(200, "PATCH", base+"/settings", map[string]any{"version": current().Version, "priority_levels": n}, nil)
	}
	upload := func(spaceID, data string) imports.Job {
		code, body := owner.upload(base+"/imports/jira-csv", spaceID, []byte(data))
		if code != 201 {
			t.Fatalf("upload %d %s", code, body)
		}
		var j imports.Job
		if err := json.Unmarshal([]byte(body), &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	save := func(j imports.Job, cfg imports.Config) imports.Job {
		var out imports.Job
		owner.json(200, "PUT", base+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &out)
		return out
	}
	start := func(j imports.Job, confirm bool) map[string]any {
		v := map[string]any{"config_version": j.ConfigVersion}
		if confirm {
			v["confirm_priority_upgrade"] = true
			v["organization_version"] = j.Analysis.PriorityImpact.OrganizationVersion
		}
		return v
	}
	path := func(j imports.Job) string { return base + "/imports/" + j.ID }
	cancel := func(j imports.Job) {
		owner.json(200, "POST", path(j)+"/cancel", map[string]any{"version": j.Version}, nil)
	}
	get := func(j imports.Job) imports.Job {
		var out imports.Job
		owner.json(200, "GET", path(j), nil, &out)
		return out
	}
	worker := &imports.Worker{Pool: pool, Instance: "o01-priority-http"}
	drain := func(j imports.Job) imports.Job {
		for i := 0; i < 20; i++ {
			if err := worker.RunOrganization(ctx, org); err != nil {
				t.Fatal(err)
			}
			j = get(j)
			if j.Status == "SUCCEEDED" || j.Status == "FAILED" || j.Status == "ROLLED_BACK" {
				return j
			}
		}
		t.Fatalf("not terminal %+v", j)
		return j
	}
	csv := func(id, priority string) string {
		return "Summary,Issue key,Issue id,Status,Priority\nPriority " + id + ",P-" + id + "," + id + ",Todo," + priority + "\n"
	}
	levels(4)
	t.Run("priority impact excludes filtered invalid and ignored column", func(t *testing.T) {
		j := upload(space, "Summary,Issue key,Issue id,Status,Priority\nOpen,P-10,10,Todo,High\nDone,P-11,11,Done,Lowest\n,P-12,12,Todo,Lowest\n")
		cfg := *j.Config
		cfg.Options.ImportDone = false
		j = save(j, cfg)
		if j.Analysis.PriorityImpact.LowestCount != 0 || j.Analysis.PriorityImpact.UpgradeRequired {
			t.Fatalf("impact %+v", j.Analysis.PriorityImpact)
		}
		cancel(j)
		j = upload(space, csv("13", "Lowest"))
		cfg = *j.Config
		for i := range cfg.Columns {
			if cfg.Columns[i].Field == imports.FieldPriority {
				cfg.Columns[i].Field = "ignore"
			}
		}
		j = save(j, cfg)
		if j.Analysis.PriorityImpact.LowestCount != 0 {
			t.Fatal(j.Analysis.PriorityImpact)
		}
		cancel(j)
		j = upload(space, csv("14", "4"))
		if j.Analysis.PriorityImpact.UpgradeRequired {
			t.Fatal("numeric guess")
		}
		cancel(j)
	})
	var preserved imports.Job
	t.Run("confirmation auth version CAS atomic upgrade and replay", func(t *testing.T) {
		j := upload(space, csv("20", "Lowest"))
		if j.Analysis.PriorityImpact.LowestCount != 1 || !j.Analysis.PriorityImpact.UpgradeRequired {
			t.Fatal(j.Analysis)
		}
		member.expect(403, "FORBIDDEN", "POST", path(j)+"/start", start(j, true))
		owner.expect(409, "PRIORITY_UPGRADE_CONFIRMATION_REQUIRED", "POST", path(j)+"/start", start(j, false))
		if current().Levels != 4 || get(j).Status != "DRAFT" {
			t.Fatal("rejected start wrote data")
		}
		levels(4)
		owner.expect(409, "ORGANIZATION_VERSION_CONFLICT", "POST", path(j)+"/start", start(j, true))
		j = save(j, *j.Config)
		before := current()
		owner.json(200, "POST", path(j)+"/start", start(j, true), &preserved)
		if after := current(); after.Levels != 5 || after.Version != before.Version+1 {
			t.Fatal(after)
		}
		var snapshot imports.PriorityImpact
		var raw []byte
		if err := super.QueryRow(ctx, `SELECT preview->'analysis'->'priority_impact' FROM openfox.import_jobs WHERE id=$1::uuid`, preserved.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		if !snapshot.UpgradeApplied || snapshot.CurrentLevels != 4 {
			t.Fatal(snapshot)
		}
		owner.json(200, "POST", path(j)+"/start", map[string]any{"config_version": 1}, nil)
		if current().Version != before.Version+1 {
			t.Fatal("replay upgraded twice")
		}
		preserved = drain(preserved)
		if preserved.Status != "SUCCEEDED" || preserved.CreatedCount != 1 {
			t.Fatalf("%+v", preserved)
		}
		var got string
		if err := super.QueryRow(ctx, `SELECT t.priority FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid`, preserved.ID).Scan(&got); err != nil || got != "LOWEST" {
			t.Fatalf("priority %s %v", got, err)
		}
	})
	t.Run("existing mapping skip avoids upgrade and overwrite requires it", func(t *testing.T) {
		levels(4)
		j := upload(space, csv("20", "Trivial"))
		if j.Analysis.PriorityImpact.LowestCount != 0 {
			t.Fatal(j.Analysis.PriorityImpact)
		}
		cfg := *j.Config
		cfg.Options.Overwrite = true
		j = save(j, cfg)
		if j.Analysis.PriorityImpact.LowestCount != 1 {
			t.Fatal(j.Analysis.PriorityImpact)
		}
		cancel(j)
	})
	t.Run("queue failure rolls back the organization upgrade", func(t *testing.T) {
		active := upload(space, csv("30", "High"))
		owner.json(200, "POST", path(active)+"/start", start(active, false), &active)
		j := upload(space, csv("31", "Lowest"))
		before := current()
		owner.expect(409, "IMPORT_ACTIVE", "POST", path(j)+"/start", start(j, true))
		if after := current(); after != before {
			t.Fatalf("upgrade leaked: %+v %+v", before, after)
		}
		if get(j).Status != "DRAFT" {
			t.Fatal("queued despite conflict")
		}
		cancel(active)
		cancel(j)
	})
	t.Run("concurrent confirmations cannot overwrite a settings revision", func(t *testing.T) {
		jobs := make([]imports.Job, 2)
		for i := range jobs {
			var sp struct {
				ID string `json:"id"`
			}
			owner.json(201, "POST", base+"/spaces", map[string]any{"name": fmt.Sprintf("O01 %d", i), "visibility": "SHARED"}, &sp)
			jobs[i] = upload(sp.ID, csv(fmt.Sprint(40+i), "Lowest"))
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		bodies := make([]string, 2)
		for i := range jobs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i], bodies[i] = owner.do("POST", path(jobs[i])+"/start", start(jobs[i], true))
			}(i)
		}
		wg.Wait()
		if !((codes[0] == 200 && codes[1] == 409) || (codes[1] == 200 && codes[0] == 409)) {
			t.Fatalf("statuses %v %v", codes, bodies)
		}
		for i, j := range jobs {
			if codes[i] == 409 && !strings.Contains(bodies[i], "ORGANIZATION_VERSION_CONFLICT") {
				t.Fatal(bodies[i])
			}
			cancel(get(j))
		}
	})
	t.Run("old queued job fails after manual downgrade without losing lowest", func(t *testing.T) {
		j := upload(space, csv("50", "Lowest"))
		owner.json(200, "POST", path(j)+"/start", start(j, false), &j)
		// A queued job from before O01 has no priority snapshot; runtime enforcement still applies.
		if _, err := super.Exec(ctx, `UPDATE openfox.import_jobs SET preview=preview #- '{analysis,priority_impact}' WHERE id=$1::uuid`, j.ID); err != nil {
			t.Fatal(err)
		}
		levels(4)
		j = drain(j)
		if j.Status != "FAILED" || j.ErrorCode != "PRIORITY_LEVEL_UNAVAILABLE" || j.CreatedCount != 0 || current().Levels != 4 {
			t.Fatalf("%+v", j)
		}
	})
	t.Run("rollback preserves upgraded organization and independently created lowest", func(t *testing.T) {
		j := upload(space, csv("60", "最低"))
		owner.json(200, "POST", path(j)+"/start", start(j, true), &j)
		j = drain(j)
		var external struct {
			ID string `json:"id"`
		}
		owner.json(http.StatusCreated, "POST", base+"/spaces/"+space+"/tasks", map[string]any{"title": "external lowest", "priority": "LOWEST"}, &external)
		owner.json(200, "POST", path(j)+"/rollback", map[string]any{"version": j.Version}, &j)
		j = drain(j)
		if j.Status != "ROLLED_BACK" || current().Levels != 5 {
			t.Fatalf("%+v", j)
		}
		var priority string
		if err := super.QueryRow(ctx, `SELECT priority FROM openfox.tasks WHERE id=$1::uuid AND deleted_at IS NULL`, external.ID).Scan(&priority); err != nil || priority != "LOWEST" {
			t.Fatalf("external priority %s %v", priority, err)
		}
	})
}
