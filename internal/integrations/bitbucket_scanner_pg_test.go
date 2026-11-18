package integrations

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestBitbucketControlPlaneFindsDueWork(t *testing.T) {
	b := newBBPG(t)
	// The auth fixture inserts organization rows directly. A real provisioned
	// organization also has the control-plane directory entry.
	b.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, b.org)
	c := b.connect()
	space := b.repoSpace("PRIVATE", "OWNER")
	in := bbRepoInput(space, c.Version)
	r, err := b.repoSave(b.repoTarget(c.ID, in), in, bbRepoProof())
	if err != nil {
		t.Fatal(err)
	}
	b.exec(`INSERT INTO openfox.integration_bitbucket_work(organization_id,connection_id,selection_id,workspace_id,repository_id,kind,resource_key,payload,connection_version,credential_generation,selection_version,hook_generation) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'PULL_REQUEST','PULL_REQUEST:1','{}',1,1,1,1)`, b.org, c.ID, r.ID, bbWorkspace, bbRepository)
	scan := func(want bool) {
		t.Helper()
		var found bool
		err := b.pool.RunControlPlane(b.ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox_private.list_integration_sync_organizations(NULL,500) WHERE organization_id=$1::uuid)`, b.org).Scan(&found)
		})
		if err != nil || found != want {
			t.Fatalf("control-plane scan found=%v want=%v error=%v", found, want, err)
		}
	}
	scan(true)
	b.exec(`UPDATE openfox.integration_bitbucket_repositories SET status='REMOVED' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, r.ID)
	scan(false)
	b.exec(`UPDATE openfox.integration_bitbucket_repositories SET status='ACTIVE' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, r.ID)
	b.exec(`UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, c.ID)
	scan(false)
}
