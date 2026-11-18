package imports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
	"openfox/internal/integrations/asana"
	"strings"
	"time"
)

var errAsanaArchived = errors.New("imports: Asana project is archived")

func sameAsanaTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (w *Worker) runAsanaSync(ctx context.Context, j *syncJob) error {
	var c asanaCrawl
	var restart bool
	err := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		j.settings, e = loadSettings(ctx, tx, j.job.SpaceID)
		if e != nil {
			return e
		}
		c, e = readAsanaCrawl(ctx, tx, j.job.ID, true)
		if e != nil {
			return e
		}
		restart = c.state == "DONE" || c.state == "FAILED" || c.version != j.asanaVersion || c.generation != j.asanaGeneration
		return nil
	})
	if err != nil {
		return err
	}
	if restart {
		var project asana.Project
		_, err = w.asanaRuntime().Do(ctx, j.orgID, j.connection, j.asanaVersion, j.asanaGeneration, func(token string) error { var e error; project, e = w.Asana.Project(ctx, token, j.project); return e })
		if err != nil {
			return err
		}
		if project.Workspace.GID != c.workspace {
			return errAsanaShape
		}
		if project.Archived {
			err = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
				if e := w.checkProjectSync(ctx, tx, j); e != nil {
					return e
				}
				_, e := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET state='PAUSED',lease=NULL,error_code='PROJECT_ARCHIVED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, j.job.ID, j.lease)
				return e
			})
			if err != nil {
				return err
			}
			return errAsanaArchived
		}
	}
	err = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		c, e = readAsanaCrawl(ctx, tx, j.job.ID, true)
		if e != nil {
			return e
		}
		if restart {
			if _, e = tx.Exec(ctx, `DELETE FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, j.job.ID); e != nil {
				return e
			}
			access, e := integrations.LockAsanaAccess(ctx, tx, j.connection, j.asanaVersion, j.asanaGeneration)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET cycle_id=gen_random_uuid(),kind='SYNC',state='FETCHING',connection_version=$2,grant_generation=$3,workspace_id=$4,root_offset='',root_offsets='{}',roots_done=false,request_count=0,data_bytes=0,item_count=0,failures=0,offset_restarts=0,lease=NULL,lease_until=NULL,retry_at=NULL,started_at=clock_timestamp(),deadline=clock_timestamp()+interval '2 hours' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, j.job.ID, access.ConnectionVersion, access.GrantGeneration, access.WorkspaceID)
			if e != nil {
				return e
			}
			c, e = readAsanaCrawl(ctx, tx, j.job.ID, false)
		}
		return e
	})
	if err != nil {
		return err
	}
	ready := c.state == "READY"
	if !ready {
		ready, err = w.crawlAsanaPass(ctx, j.orgID, j.job.ID, j.lease)
		if err != nil {
			return err
		}
		if !ready {
			return errSyncBacklog
		}
	}
	var records []Record
	items := map[string]syncedItem{}
	ignored := map[string]bool{}
	applied := map[string]bool{}
	err = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		c, e = readAsanaCrawl(ctx, tx, j.job.ID, false)
		if e != nil {
			return e
		}
		if c.state != "READY" || c.version != j.asanaVersion || c.generation != j.asanaGeneration {
			return integrations.ErrVersionConflict
		}
		records, e = loadAsanaRecords(ctx, tx, c)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT i.external_id,i.task_id::text,i.source_title,i.source_description,t.title,t.description,t.version,t.status_id::text,i.source_status,i.source_status_id,i.local_status_id,i.source_completed_at,t.deleted_at IS NOT NULL OR t.space_id<>$2::uuid FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.organization_id=i.organization_id AND t.id=i.task_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.job_id=$1::uuid LIMIT 5001`, j.job.ID, j.job.SpaceID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			var v syncedItem
			var deleted bool
			if e = rows.Scan(&id, &v.task, &v.baseline.title, &v.baseline.description, &v.local.title, &v.local.description, &v.version, &v.status, &v.baselineSourceStatus, &v.baselineMappedStatus, &v.baselineStatus, &v.baselineCompleted, &deleted); e != nil {
				rows.Close()
				return e
			}
			items[id] = v
			ignored[id] = deleted
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(items) > 5000 {
			return errAsanaLimit
		}
		rows, e = tx.Query(ctx, `SELECT external_id FROM openfox.import_items WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND status IN('FAILED','SKIPPED')`, j.job.ID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ignored[id] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT external_id FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND applied`, j.job.ID, c.cycle)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			applied[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	remoteItems := make([]pendingItem, 0, len(records))
	for i := range records {
		remoteItems = append(remoteItems, pendingItem{ExternalID: records[i].ID, Record: &records[i]})
	}
	if err = w.discoverSyncStatuses(ctx, j, remoteItems); err != nil {
		return err
	}
	// Parents precede new descendants, including when a batch ends between them.
	byID := map[string]Record{}
	for _, r := range records {
		byID[r.ID] = r
	}
	ordered := []Record{}
	seen := map[string]bool{}
	var visit func(Record)
	visit = func(r Record) {
		if seen[r.ID] {
			return
		}
		seen[r.ID] = true
		if p, ok := byID[r.ParentID]; ok {
			visit(p)
		}
		ordered = append(ordered, r)
	}
	for _, r := range records {
		visit(r)
	}
	changes := 0
	for _, r := range ordered {
		if applied[r.ID] {
			continue
		}
		d := rowFromRecord(&r)
		v, exists := items[r.ID]
		skip := ignored[r.ID] || !exists && ((rowDone(d) && !j.job.Cfg.Options.ImportDone) || (!rowDone(d) && !j.job.Cfg.Options.ImportOpen))
		d.Title, _ = TruncateRunes(d.Title, maxTitleRunes)
		d.Description, _ = TruncateRunes(d.Description, maxDescriptionRunes)
		unchanged := exists && (textPair{d.Title, d.Description}) == v.baseline && sameAsanaTime(v.baselineCompleted, d.CompletedAt) && v.baselineSourceStatus != nil && *v.baselineSourceStatus == d.Status && v.baselineMappedStatus != nil && *v.baselineMappedStatus == j.mappings[strings.ToLower(d.Status)]
		if !skip && !unchanged {
			if changes >= 50 {
				return errSyncBacklog
			}
			if !exists && len(items) >= 5000 {
				return errAsanaLimit
			}
			if e := w.applySyncItem(ctx, j, pendingItem{ExternalID: r.ID, Record: &r}, d, v, exists); e != nil && !errors.Is(e, errSyncSkipped) {
				return e
			}
			changes++
			if !exists {
				items[r.ID] = syncedItem{}
			}
		}
		err = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_nodes SET applied=true WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND external_id=$3`, j.job.ID, c.cycle, r.ID)
			return e
		})
		if err != nil {
			return err
		}
	}
	return w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		raw, _ := json.Marshal(j.warns.list)
		if _, e := tx.Exec(ctx, `UPDATE openfox.import_jobs SET warnings=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND $2::jsonb<>'null'::jsonb`, j.job.ID, raw); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET state='DONE' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, j.job.ID, c.cycle)
		return e
	})
}
