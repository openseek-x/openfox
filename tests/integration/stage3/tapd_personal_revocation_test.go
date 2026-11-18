package stage3

import (
	"testing"

	"openfox/internal/imports"
	"openfox/internal/integrations"
)

func TestTAPDPersonalChangedProofRequiresEachProject(t *testing.T) {
	for _, reason := range []string{"parent", "email", "nick"} {
		t.Run(reason, func(t *testing.T) {
			e := newTAPDPersonalEnv(t)
			binding := e.bind()
			e.member().must(200, "PUT", "me/account-bindings/tapd", e.input("102", "member", binding.Version), &binding)
			if len(e.view(e.member()).Binding.TAPDIdentity.Projects) != 2 {
				t.Fatal("same proof failed to extend scope")
			}
			switch reason {
			case "parent":
				e.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, e.parent.ID)
			case "email":
				email := "changed-tapd-member@example.test"
				e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, email)
				t.Cleanup(func() { e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, s3Email(s3MemberID)) })
				e.fake.mu.Lock()
				e.fake.email = email
				e.fake.mu.Unlock()
			case "nick":
				e.fake.mu.Lock()
				e.fake.nick = "renamed.nick"
				e.fake.mu.Unlock()
			}
			e.member().must(200, "PUT", "me/account-bindings/tapd", e.input("101", "member", binding.Version), &binding)
			projects := e.view(e.member()).Binding.TAPDIdentity.Projects
			if len(projects) != 1 || projects[0] != "101" {
				t.Fatalf("unverified projects revived: %v", projects)
			}
		})
	}
}

func TestTAPDPersonalWorkerChecksCurrentContextAndTrustedOrigin(t *testing.T) {
	for _, reason := range []string{"parent_revision", "parent_disconnect", "member_removed", "email_changed", "project_removed"} {
		t.Run(reason, func(t *testing.T) {
			e := newTAPDPersonalEnv(t)
			binding := e.bind()
			e.setItems(tapdFixtureItem{"5000", "member.nick"})
			job := e.start(e.importJob("101", false))
			switch reason {
			case "parent_revision":
				e.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, e.parent.ID)
			case "parent_disconnect":
				e.owner().must(200, "POST", "integrations/connections/"+e.parent.ID+"/disconnect", map[string]any{"version": e.parent.Version, "confirm_phrase": integrations.ConfirmPhrase("TAPD")}, nil)
			case "member_removed":
				e.exec(`UPDATE openfox.organization_members SET status='REVOKED',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3MemberID)
			case "email_changed":
				e.exec(`UPDATE openfox.users SET email='changed-tapd-member@example.test' WHERE id=$1::uuid`, s3MemberID)
				t.Cleanup(func() { e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, s3Email(s3MemberID)) })
			case "project_removed":
				e.member().must(200, "DELETE", "me/account-bindings/tapd/projects/101", map[string]any{"binding_id": binding.ID, "version": binding.Version}, nil)
			}
			job = e.drain(e.worker(), job)
			var count int
			if job.CreatedCount != 1 {
				t.Fatalf("job %+v", job)
			}
			e.scalar(&count, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND t.external_assignee_id IS NOT NULL AND t.assignee_id IS NULL`, job.ID)
			if count != 1 {
				t.Fatal("stale verified mapping survived")
			}
		})
	}
	t.Run("other project and forged references", func(t *testing.T) {
		e := newTAPDPersonalEnv(t)
		binding := e.bind()
		e.setItems(tapdFixtureItem{"6000", "member.nick"})
		job := e.importJob("102", false)
		if job.Analysis.Users[0].VerifiedBinding != nil {
			t.Fatal("cross-project verified suggestion")
		}
		cfg := *job.Config
		cfg.Users = []imports.UserMapping{{SourceID: "member.nick", Mode: "member", UserID: s3MemberID, VerifiedBindingID: binding.ID}}
		e.owner().expectError(400, "INVALID_IMPORT_CONFIG", "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg})
	})
}
