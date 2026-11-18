package imports

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/imports"
)

func (p *p02Env) draft(parent, visibility string) imports.Job {
	var j imports.Job
	p.owner.json(201, "POST", p.prefix+"/imports/teambition/project", map[string]any{"connection_id": parent, "project_id": "project", "new_space": map[string]any{"name": "P02 import", "visibility": visibility}}, &j)
	return j
}
func (p *p02Env) saveStart(j imports.Job, mapping []imports.UserMapping, sync bool) imports.Job {
	cfg := *j.Config
	cfg.Users = mapping
	cfg.Options.ContinuousSync = sync
	p.owner.json(200, "PUT", p.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
	p.owner.json(200, "POST", p.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
	return j
}
func (p *p02Env) finish(j imports.Job) imports.Job {
	done := p.drain(j.ID)
	if done.Status != "SUCCEEDED" {
		p.t.Fatalf("import failed=%+v", done)
	}
	p.owner.json(200, "GET", p.prefix+"/imports/"+j.ID, nil, &j)
	return j
}
func (p *p02Env) syncMore(j imports.Job, count int) {
	p.count.Store(int32(count))
	p.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp()-interval '1 minute' WHERE job_id=$1::uuid`, j.ID)
	if err := p.worker.RunOrganization(p.ctx, p.org); err != nil {
		p.t.Fatal(err)
	}
	if err := p.str(`SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, j.ID); err != "" {
		p.t.Fatalf("sync=%s", err)
	}
}
func (p *p02Env) assignee(space string, n int) string {
	return p.str(`SELECT coalesce(assignee_id::text,'') FROM openfox.tasks WHERE organization_id=$1::uuid AND space_id=$2::uuid AND title=$3`, p.org, space, fmt.Sprintf("P02 imported %d", n))
}
func (p *p02Env) external(space string, n int) string {
	return p.str(`SELECT coalesce(external_assignee_id::text,'') FROM openfox.tasks WHERE organization_id=$1::uuid AND space_id=$2::uuid AND title=$3`, p.org, space, fmt.Sprintf("P02 imported %d", n))
}

func TestTeambitionVerifiedMappingWorkerAndSync(t *testing.T) {
	p := newP02Env(t)
	binding := p.bindMember()
	j := p.draft(p.parent.ID, "SHARED")
	if j.Analysis == nil || len(j.Analysis.Users) != 1 || j.Analysis.Users[0].VerifiedBinding == nil || j.Analysis.Users[0].VerifiedBinding.ID != binding.ID || j.Analysis.Users[0].SuggestedUserID != nil {
		t.Fatalf("analysis=%+v", j.Analysis)
	}
	mapping := []imports.UserMapping{{SourceID: "remote-member", Mode: "member", UserID: siteMember, VerifiedBindingID: binding.ID}}
	j = p.finish(p.saveStart(j, mapping, true))
	if got := p.assignee(*j.SpaceID, 1); got != siteMember {
		t.Fatalf("initial assignee=%s", got)
	}
	if p.num(`SELECT count(*) FROM openfox.external_accounts WHERE organization_id=$1::uuid AND provider='teambition' AND linked_user_id IS NOT NULL`, p.org) != 0 {
		t.Fatal("scoped binding leaked into global link")
	}
	p.member.json(200, "POST", p.prefix+"/integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	p.syncMore(j, 2)
	if p.assignee(*j.SpaceID, 2) != "" || p.external(*j.SpaceID, 2) == "" || p.assignee(*j.SpaceID, 1) != siteMember {
		t.Fatal("unlink changed history or reused stale automatic mapping")
	}
	// A later explicit manual mapping remains a manual choice after revocation.
	p.exec(`UPDATE openfox.import_sync_links SET state='PAUSED' WHERE job_id=$1::uuid`, j.ID)
	manual := p.draft(p.parent.ID, "SHARED")
	manual = p.finish(p.saveStart(manual, []imports.UserMapping{{SourceID: "remote-member", Mode: "member", UserID: siteOwner}}, false))
	if p.assignee(*manual.SpaceID, 2) != siteOwner {
		t.Fatal("manual selection lost")
	}
	// A previously unseen source also consults current binding, without requiring preview.resolved.
	binding = p.bindMember()
	p.exec(`UPDATE openfox.import_sync_links SET state='ACTIVE' WHERE job_id=$1::uuid`, j.ID)
	p.exec(`UPDATE openfox.import_jobs SET config=jsonb_set(config,'{users}','[]') WHERE id=$1::uuid`, j.ID)
	p.syncMore(j, 3)
	if p.assignee(*j.SpaceID, 3) != siteMember {
		t.Fatal("new identity did not resolve")
	}
	p.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, p.parent.ID)
	p.syncMore(j, 4)
	if p.assignee(*j.SpaceID, 4) != "" || p.external(*j.SpaceID, 4) == "" {
		t.Fatal("parent revision reused stale proof")
	}
}

func TestTeambitionVerifiedMappingScopeAndRevocation(t *testing.T) {
	for _, change := range []string{"email", "role", "unlink", "parent", "space"} {
		t.Run(change, func(t *testing.T) {
			p := newP02Env(t)
			binding := p.bindMember()
			visibility := "SHARED"
			if change == "space" {
				visibility = "PRIVATE"
			}
			j := p.draft(p.parent.ID, visibility)
			j = p.saveStart(j, []imports.UserMapping{{SourceID: "remote-member", Mode: "member", UserID: siteMember, VerifiedBindingID: binding.ID}}, false)
			// Validation builds preview.resolved in the first worker pass; revoke before writing.
			p.worker.MaxBatches = 1
			if err := p.worker.RunOrganization(p.ctx, p.org); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "email":
				p.exec(`UPDATE openfox.users SET email='changed-member@example.test' WHERE id=$1::uuid`, siteMember)
			case "role":
				p.exec(`UPDATE openfox.organization_members SET role='GUEST',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, p.org, siteMember)
			case "unlink":
				p.member.json(200, "POST", p.prefix+"/integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
			case "parent":
				p.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, p.parent.ID)
			}
			j = p.finish(j)
			if p.assignee(*j.SpaceID, 1) != "" {
				t.Fatal("stale/inaccessible member assigned")
			}
			if change != "space" && p.external(*j.SpaceID, 1) == "" {
				t.Fatal("external identity lost")
			}
		})
	}
	p := newP02Env(t)
	binding := p.bindMember()
	other := p.addParent("other-company")
	j := p.draft(other.ID, "SHARED")
	if len(j.Analysis.Users) != 1 || j.Analysis.Users[0].VerifiedBinding != nil {
		t.Fatal("identity crossed enterprises")
	}
	bad := *j.Config
	bad.Users = []imports.UserMapping{{SourceID: "remote-member", Mode: "member", UserID: siteMember, VerifiedBindingID: binding.ID}}
	p.owner.expect(400, "INVALID_IMPORT_CONFIG", "PUT", p.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": bad})
}

func TestTeambitionMappingEmailLock(t *testing.T) {
	p := newP02Env(t)
	binding := p.bindMember()
	j := p.draft(p.parent.ID, "SHARED")
	j = p.saveStart(j, []imports.UserMapping{{SourceID: "remote-member", Mode: "member", UserID: siteMember, VerifiedBindingID: binding.ID}}, false)
	p.worker.MaxBatches = 1
	if err := p.worker.RunOrganization(p.ctx, p.org); err != nil {
		t.Fatal(err)
	}
	// A test-only trigger creates a deterministic barrier after the mapping read,
	// at the first actual task write. It never exists outside this isolated DB.
	const key = 90302134
	p.exec(`CREATE OR REPLACE FUNCTION public.p02_pause_task() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(90302134); RETURN NEW; END $$`)
	p.exec(`CREATE TRIGGER p02_pause_task BEFORE INSERT ON openfox.tasks FOR EACH ROW WHEN (NEW.organization_id='` + p.org + `'::uuid) EXECUTE FUNCTION public.p02_pause_task()`)
	t.Cleanup(func() {
		p.exec(`DROP TRIGGER IF EXISTS p02_pause_task ON openfox.tasks`)
		p.exec(`DROP FUNCTION IF EXISTS public.p02_pause_task()`)
	})
	blocker, err := pgx.Connect(p.ctx, p.url)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(context.Background())
	_, err = blocker.Exec(p.ctx, `SELECT pg_advisory_lock($1)`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	workDone := make(chan error, 1)
	go func() { workDone <- p.worker.RunOrganization(p.ctx, p.org) }()
	deadline := time.Now().Add(5 * time.Second)
	for p.num(`SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=90302134 AND NOT granted`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not reach task barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	emailConn, err := pgx.Connect(p.ctx, p.url)
	if err != nil {
		t.Fatal(err)
	}
	defer emailConn.Close(context.Background())
	changed := make(chan error, 1)
	go func() {
		_, err := emailConn.Exec(p.ctx, `UPDATE openfox.users SET email='changed-member@example.test' WHERE id=$1::uuid`, siteMember)
		changed <- err
	}()
	select {
	case err := <-changed:
		t.Fatalf("email changed while automatic attribution in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	_, err = blocker.Exec(p.ctx, `SELECT pg_advisory_unlock($1)`, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-workDone; err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	j = p.finish(j)
	if p.assignee(*j.SpaceID, 1) != siteMember {
		t.Fatal("protected batch did not retain verified identity")
	}
	if !strings.Contains(p.str(`SELECT email FROM openfox.users WHERE id=$1::uuid`, siteMember), "changed-") {
		t.Fatal("email writer did not resume")
	}
}
