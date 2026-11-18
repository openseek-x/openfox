package stage3

import (
	"fmt"
	"testing"

	"openfox/internal/imports"
	"openfox/internal/integrations"
)

func (e *tapdPersonalEnv) setItems(items ...tapdFixtureItem) {
	e.fake.mu.Lock()
	e.fake.items = items
	e.fake.mu.Unlock()
}

func (e *tapdPersonalEnv) bind() integrations.Connection {
	var binding integrations.Connection
	e.member().must(200, "PUT", "me/account-bindings/tapd", e.input("101", "member", 0), &binding)
	return binding
}

func (e *tapdPersonalEnv) importJob(project string, continuous bool) imports.Job {
	var job imports.Job
	e.owner().must(201, "POST", "imports/tapd/project", map[string]any{"connection_id": e.parent.ID, "project_id": project, "space_id": e.space.ID}, &job)
	if job.Analysis == nil || job.Config == nil {
		e.t.Fatalf("missing analysis: %+v", job)
	}
	cfg := *job.Config
	cfg.Users = nil
	for _, u := range job.Analysis.Users {
		mapping := imports.UserMapping{SourceID: u.SourceID, Mode: "external"}
		if b := u.VerifiedBinding; b != nil {
			if u.SuggestedUserID != nil {
				e.t.Fatal("verified suggestion leaked into legacy suggestion")
			}
			mapping.Mode, mapping.UserID, mapping.VerifiedBindingID = "member", b.UserID, b.ID
		}
		cfg.Users = append(cfg.Users, mapping)
	}
	cfg.Options.ContinuousSync = continuous
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	return job
}

func (e *tapdPersonalEnv) worker() *imports.Worker {
	return &imports.Worker{Pool: e.database, Secrets: e.box, ProjectTransport: e.transport, Instance: "tapd-identity-test", MaxBatches: 1}
}

func (e *tapdPersonalEnv) start(job imports.Job) imports.Job {
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, &job)
	return job
}

func (e *tapdPersonalEnv) drain(worker *imports.Worker, job imports.Job) imports.Job {
	for i := 0; i < 20; i++ {
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
		if job.Status == "SUCCEEDED" {
			return job
		}
		if job.Status == "FAILED" || job.Status == "CANCELLED" {
			e.t.Fatalf("import failed: %+v", job)
		}
	}
	e.t.Fatalf("import did not finish: %+v", job)
	return job
}

func TestTAPDPersonalActualWorkerRevokesNextBatch(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	binding := e.bind()
	items := []tapdFixtureItem{}
	for i := 0; i < 51; i++ {
		items = append(items, tapdFixtureItem{fmt.Sprint(1000 + i), "member.nick"})
	}
	items = append(items, tapdFixtureItem{"2000", "other.nick"}, tapdFixtureItem{"2001", "member.nick;other.nick"}, tapdFixtureItem{"2002", "Member.Nick"})
	e.setItems(items...)
	job := e.importJob("101", false)
	matched := 0
	for _, u := range job.Analysis.Users {
		if u.VerifiedBinding != nil {
			matched++
			if u.SourceID != "member.nick" || u.VerifiedBinding.ID != binding.ID {
				t.Fatal("non-exact source received verified mapping")
			}
		}
	}
	if matched != 1 {
		t.Fatalf("verified suggestions=%d", matched)
	}
	job = e.start(job)
	w := e.worker()
	for i := 0; i < 10 && job.CreatedCount == 0; i++ {
		if err := w.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	}
	if job.CreatedCount != 50 {
		t.Fatalf("expected one batch: %+v", job)
	}
	e.member().must(200, "POST", "integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	job = e.drain(w, job)
	var assigned, external, linked int
	e.scalar(&assigned, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND t.assignee_id=$2::uuid`, job.ID, s3MemberID)
	e.scalar(&external, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND t.external_assignee_id IS NOT NULL`, job.ID)
	e.scalar(&linked, `SELECT count(*) FROM openfox.external_accounts WHERE organization_id=$1::uuid AND provider='tapd' AND linked_user_id IS NOT NULL`, e.org)
	if assigned != 50 || external != 4 || linked != 0 {
		t.Fatalf("assigned=%d external=%d global links=%d", assigned, external, linked)
	}
	warned := false
	for _, warning := range job.Warnings {
		if warning.Code == "verified_identity_unavailable" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing revocation warning")
	}
}

func TestTAPDPersonalContinuousSyncAndManualMapping(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	binding := e.bind()
	e.setItems(tapdFixtureItem{"3000", "member.nick"})
	w := e.worker()
	job := e.drain(w, e.start(e.importJob("101", true)))
	e.member().must(200, "DELETE", "me/account-bindings/tapd/projects/101", map[string]any{"binding_id": binding.ID, "version": binding.Version}, nil)
	e.setItems(tapdFixtureItem{"3000", "member.nick"}, tapdFixtureItem{"3001", "member.nick"})
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	if err := w.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	var oldAssigned, newExternal int
	e.scalar(&oldAssigned, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND i.external_id='3000' AND t.assignee_id=$2::uuid`, job.ID, s3MemberID)
	e.scalar(&newExternal, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND i.external_id='3001' AND t.external_assignee_id IS NOT NULL`, job.ID)
	if oldAssigned != 1 || newExternal != 1 {
		t.Fatalf("historical assigned=%d new external=%d", oldAssigned, newExternal)
	}
	e.exec(`UPDATE openfox.import_sync_links SET state='PAUSED' WHERE job_id=$1::uuid`, job.ID)
	// Explicit member mapping has no provenance and retains the pre-existing contract.
	e.setItems(tapdFixtureItem{"4000", "member.nick"})
	manual := e.importJob("101", false)
	cfg := *manual.Config
	cfg.Users = []imports.UserMapping{{SourceID: "member.nick", Mode: "member", UserID: s3MemberID}}
	e.owner().must(200, "PUT", "imports/"+manual.ID+"/config", map[string]any{"config_version": manual.ConfigVersion, "config": cfg}, &manual)
	manual = e.drain(w, e.start(manual))
	var assigned int
	e.scalar(&assigned, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND t.assignee_id=$2::uuid`, manual.ID, s3MemberID)
	if assigned != 1 {
		t.Fatal("manual member mapping was revoked")
	}
}
