package clone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/platform/postgres"
	"openfox/internal/spacebundle"
	"openfox/internal/tenancy"
)

func priorityCloneHTTP(t *testing.T, e *env, pool *postgres.Pool, storage artifacts.Local, owner *client, space string) {
	sourceExec := func(sql string, args ...any) {
		version := e.count(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, orgS, uOwner.id)
		selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return tenancy.OrganizationSelection{OrganizationID: orgS, ActorID: uOwner.id, AuthorizationVersion: uint64(version)}, nil
		})
		if err := pool.RunForOrganization(e.ctx, orgS, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/organizations/" + orgS + "/spaces/" + space + "/clone"
	setting := func(org string) (int, uint64) {
		var v struct {
			Levels  int    `json:"priority_levels"`
			Version uint64 `json:"version"`
		}
		owner.decode(200, "GET", "/v1/organizations/"+org+"/settings", nil, nil, &v)
		return v.Levels, v.Version
	}
	levels := func(org string, n int) {
		_, v := setting(org)
		owner.decode(200, "PATCH", "/v1/organizations/"+org+"/settings", map[string]any{"version": v, "priority_levels": n}, nil, nil)
	}
	preview := func(body map[string]any) imports.ClonePreview {
		var p imports.ClonePreview
		owner.decode(200, "POST", base+"/preview", body, nil, &p)
		return p
	}
	finish := func(j imports.CloneJob) imports.CloneJob {
		w := &imports.Worker{Pool: pool, Storage: storage, Instance: "o01-clone"}
		for i := 0; i < 30; i++ {
			if err := w.RunOrganization(e.ctx, orgT); err != nil {
				t.Fatal(err)
			}
			owner.decode(200, "GET", "/v1/organizations/"+orgT+"/clone-jobs/"+j.ID, nil, nil, &j)
			if j.Status == "SUCCEEDED" || j.Status == "CLEANED" || j.Status == "FAILED" {
				return j
			}
		}
		t.Fatalf("clone not terminal %+v", j)
		return j
	}
	levels(orgS, 5)
	levels(orgT, 4)
	sourceExec(`UPDATE openfox.tasks SET priority='LOWEST' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, t1)
	sourceExec(`INSERT INTO openfox.projects (organization_id,name,status,priority,created_by) VALUES ($1::uuid,'O01 lowest project','planned','LOWEST',$2::uuid)`, orgS, uOwner.id)
	sourceExec(`UPDATE openfox.tasks SET project_id=(SELECT id FROM openfox.projects WHERE organization_id=$1::uuid AND name='O01 lowest project') WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, t1)
	t.Run("lowest task and project clone upgrades once and preserves both", func(t *testing.T) {
		body := map[string]any{"target_organization_id": orgT, "name": "Priority copy", "prefix": "PRI"}
		p := preview(body)
		if p.PriorityImpact.LowestCount != 2 || !p.PriorityImpact.UpgradeRequired {
			t.Fatal(p.PriorityImpact)
		}
		key := map[string]string{"Idempotency-Key": "o01-clone-preserve"}
		owner.expect(409, "PRIORITY_UPGRADE_CONFIRMATION_REQUIRED", "POST", base, body, key)
		body["confirm_priority_upgrade"] = true
		body["organization_version"] = p.PriorityImpact.OrganizationVersion
		levels(orgT, 4)
		owner.expect(409, "ORGANIZATION_VERSION_CONFLICT", "POST", base, body, key)
		p = preview(body)
		body["organization_version"] = p.PriorityImpact.OrganizationVersion
		var j imports.CloneJob
		owner.decode(201, "POST", base, body, key, &j)
		_, v := setting(orgT)
		if v != p.PriorityImpact.OrganizationVersion+1 {
			t.Fatalf("version %d", v)
		}
		owner.decode(200, "POST", base, body, key, nil)
		_, v2 := setting(orgT)
		if v2 != v {
			t.Fatal("replay upgraded")
		}
		j = finish(j)
		if j.Status != "SUCCEEDED" || j.TargetSpaceID == nil {
			t.Fatalf("%+v", j)
		}
		if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND space_id=$2::uuid AND priority='LOWEST'`, orgT, *j.TargetSpaceID); n != 1 {
			t.Fatal(n)
		}
		if p := e.str(`SELECT priority FROM openfox.projects WHERE organization_id=$1::uuid AND name='O01 lowest project'`, orgT); p != "LOWEST" {
			t.Fatal(p)
		}
		if n := e.count(`SELECT count(*) FROM openfox.import_jobs WHERE id=$1::uuid AND (preview->'priority_impact'->>'upgrade_applied')::boolean`, j.ID); n != 1 {
			t.Fatal("missing upgrade snapshot")
		}
	})
	t.Run("reused project alone does not require upgrade", func(t *testing.T) {
		levels(orgT, 4)
		sourceExec(`UPDATE openfox.tasks SET priority='NORMAL' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, t1)
		body := map[string]any{"target_organization_id": orgT, "name": "Reuse priority", "prefix": "REU"}
		p := preview(body)
		if p.PriorityImpact.LowestCount != 0 || p.PriorityImpact.UpgradeRequired {
			t.Fatal(p.PriorityImpact)
		}
		var j imports.CloneJob
		owner.decode(201, "POST", base, body, map[string]string{"Idempotency-Key": "o01-clone-reused"}, &j)
		j = finish(j)
		if j.Status != "SUCCEEDED" {
			t.Fatalf("%+v", j)
		}
		if n, _ := setting(orgT); n != 4 {
			t.Fatal(n)
		}
	})
	t.Run("queued clone detects downgrade and preserves partial space", func(t *testing.T) {
		sourceExec(`UPDATE openfox.tasks SET priority='LOWEST' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, t1)
		body := map[string]any{"target_organization_id": orgT, "name": "Downgraded priority", "prefix": "DWN"}
		p := preview(body)
		body["confirm_priority_upgrade"] = true
		body["organization_version"] = p.PriorityImpact.OrganizationVersion
		var j imports.CloneJob
		owner.decode(201, "POST", base, body, map[string]string{"Idempotency-Key": "o01-clone-downgraded"}, &j)
		levels(orgT, 4)
		e.exec(`UPDATE openfox.import_jobs SET preview=preview-'priority_impact' WHERE id=$1::uuid`, j.ID)
		j = finish(j)
		if j.Status != "FAILED" || j.ErrorCode != "PRIORITY_LEVEL_UNAVAILABLE" {
			t.Fatalf("%+v", j)
		}
		if n, _ := setting(orgT); n != 4 {
			t.Fatal(n)
		}
		if n := e.count(`SELECT count(*) FROM openfox.projects WHERE organization_id=$1::uuid AND name='O01 lowest project'`, orgT); n != 1 {
			t.Fatal("existing project removed")
		}
	})
	t.Run("priority failure preserves human task and concurrent task creation", func(t *testing.T) {
		body := map[string]any{"target_organization_id": orgT, "name": "Protected priority", "prefix": "PRO"}
		p := preview(body)
		body["confirm_priority_upgrade"] = true
		body["organization_version"] = p.PriorityImpact.OrganizationVersion
		var j imports.CloneJob
		owner.decode(201, "POST", base, body, map[string]string{"Idempotency-Key": "o01-clone-protected"}, &j)
		w := &imports.Worker{Pool: pool, Storage: storage, Instance: "o01-protected", MaxBatches: 2}
		if err := w.RunOrganization(e.ctx, orgT); err != nil {
			t.Fatal(err)
		}
		jobPath := "/v1/organizations/" + orgT + "/clone-jobs/" + j.ID
		owner.decode(200, "GET", jobPath, nil, nil, &j)
		if j.TargetSpaceID == nil || j.Phase != "importing" {
			t.Fatalf("%+v", j)
		}
		spPath := "/v1/organizations/" + orgT + "/spaces/" + *j.TargetSpaceID
		var human struct {
			ID string `json:"id"`
		}
		owner.decode(201, "POST", spPath+"/tasks", map[string]any{"title": "Human lowest during clone", "priority": "LOWEST"}, nil, &human)
		levels(orgT, 4)
		w.MaxBatches = 0
		workerDone := make(chan error, 1)
		go func() { workerDone <- w.RunOrganization(e.ctx, orgT) }()
		var concurrent struct {
			ID string `json:"id"`
		}
		owner.decode(201, "POST", spPath+"/tasks", map[string]any{"title": "Concurrent human during failure", "priority": "NORMAL"}, nil, &concurrent)
		if err := <-workerDone; err != nil {
			t.Fatal(err)
		}
		owner.decode(200, "GET", jobPath, nil, nil, &j)
		if j.Status != "FAILED" || j.ErrorCode != "PRIORITY_LEVEL_UNAVAILABLE" || !strings.Contains(j.ErrorMessage, "保留") {
			t.Fatalf("%+v", j)
		}
		owner.decode(200, "GET", spPath, nil, nil, nil)
		for _, id := range []string{human.ID, concurrent.ID} {
			if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL`, orgT, id); n != 1 {
				t.Fatal("human task deleted", id)
			}
		}
		list := owner.expect(200, "", "GET", spPath+"/tasks", nil, nil)
		if !strings.Contains(list, "Human lowest during clone") || !strings.Contains(list, "Concurrent human during failure") {
			t.Fatal("preserved tasks not visible", list)
		}
		if n, _ := setting(orgT); n != 4 {
			t.Fatal(n)
		}
	})
	// Final artifact mismatch simulates a source update between preview and export.
	sourceExec(`UPDATE openfox.tasks SET priority='NORMAL' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgS, t1)
	for _, kind := range []string{spacebundle.KindTask, spacebundle.KindProject} {
		t.Run("final bundle newly lowest "+kind, func(t *testing.T) {
			hasher, _ := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
			changed := &priorityArtifact{Storage: storage, kind: kind}
			srv := httptest.NewServer(api.NewWithStorage(identity.Service{Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}, pool, false, changed))
			defer srv.Close()
			c := login(t, srv.URL, uOwner)
			// A new project name is needed because the normal clone reuses existing names.
			if kind == spacebundle.KindProject {
				sourceExec(`UPDATE openfox.projects SET priority='NORMAL',name='O01 late project' WHERE organization_id=$1::uuid AND name='O01 lowest project'`, orgS)
			}
			body := map[string]any{"target_organization_id": orgT, "name": "Late " + kind, "prefix": "L" + fmt.Sprint(len(kind))}
			filesBefore, _ := filepath.Glob(filepath.Join(storage.Root, "space-clone", orgT, "*.gz"))
			c.expect(http.StatusConflict, "PRIORITY_UPGRADE_CONFIRMATION_REQUIRED", "POST", base, body, map[string]string{"Idempotency-Key": "o01-late-" + kind})
			filesAfter, _ := filepath.Glob(filepath.Join(storage.Root, "space-clone", orgT, "*.gz"))
			if len(filesAfter) != len(filesBefore) {
				t.Fatal("orphaned bundle")
			}
			if n, _ := setting(orgT); n != 4 {
				t.Fatal("upgraded without preview")
			}
			if n := e.count(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1::uuid AND preview->>'target_name'=$2`, orgT, "Late "+kind); n != 0 {
				t.Fatal("queued without consent")
			}
		})
	}
}

// Test-only storage rewrites one record after the source transaction produced its stream.
type priorityArtifact struct {
	artifacts.Storage
	kind string
}

func (p *priorityArtifact) Put(ctx context.Context, key string, src io.Reader, max int64) (artifacts.Stored, error) {
	r, err := spacebundle.NewReader(src)
	if err != nil {
		return artifacts.Stored{}, err
	}
	defer r.Close()
	var data bytes.Buffer
	w := spacebundle.NewWriter(&data)
	changed := false
	for {
		kind, raw, err := r.Next()
		if err != nil {
			return artifacts.Stored{}, err
		}
		if kind == spacebundle.KindEnd {
			break
		}
		if kind == p.kind && !changed {
			var v map[string]any
			if err := json.Unmarshal(raw, &v); err != nil {
				return artifacts.Stored{}, err
			}
			v["priority"] = "LOWEST"
			raw, _ = json.Marshal(v)
			changed = true
		}
		if err := w.Write(kind, json.RawMessage(raw)); err != nil {
			return artifacts.Stored{}, err
		}
	}
	if err := w.Close(); err != nil {
		return artifacts.Stored{}, err
	}
	return p.Storage.Put(ctx, key, &data, max)
}
