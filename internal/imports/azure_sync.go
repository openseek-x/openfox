package imports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"strconv"
)

var errAzureHistoryFailed = errors.New("Azure history import failed; explicitly request history again after correcting the source")

func markAzureNode(ctx context.Context, tx pgx.Tx, j *syncJob, id string) error {
	tag, e := tx.Exec(ctx, `UPDATE openfox.import_azure_nodes n SET applied=true FROM openfox.import_azure_crawls c WHERE n.organization_id=openfox_private.current_organization_id() AND n.job_id=$1::uuid AND n.cycle_id=$2::uuid AND n.external_id=$3::integer AND c.organization_id=n.organization_id AND c.job_id=n.job_id AND c.cycle_id=n.cycle_id AND c.state='READY'`, j.job.ID, j.azureCycle, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}
func (w *Worker) runAzureSync(ctx context.Context, j *syncJob) error {
	var c azureCrawl
	var restart bool
	var access integrations.AzureAccess
	e := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		access, e = integrations.LockAzureAccess(ctx, tx, j.connection, j.azureVersion, j.azureGeneration)
		if e != nil {
			return e
		}
		j.settings, e = loadSettings(ctx, tx, j.job.SpaceID)
		if e != nil {
			return e
		}
		c, e = readAzureCrawl(ctx, tx, j.job.ID)
		if e != nil {
			return e
		}
		if c.kind == "HISTORY" && c.state == "FAILED" {
			return errAzureHistoryFailed
		}
		restart = c.state == "DONE" || c.state == "FAILED" || c.version != j.azureVersion || c.generation != j.azureGeneration
		ready, e := integrations.IsAzureBoardsReady(ctx, tx, j.connection, j.project)
		if e != nil {
			return e
		}
		if !ready {
			return ErrImportState
		}
		return nil
	})
	if e != nil {
		return e
	}
	if restart {
		project, meta, _, e := ReadAzureMetadata(ctx, w.azureRuntime(), j.orgID, access, j.project, c.metadata.PriorityField)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(meta)
		if e != nil {
			return e
		}
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			current, e := readAzureCrawl(ctx, tx, j.job.ID)
			if e != nil {
				return e
			}
			if current.cycle != c.cycle {
				return ErrVersionConflict
			}
			if _, e = tx.Exec(ctx, `DELETE FROM openfox.import_azure_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, j.job.ID); e != nil {
				return e
			}
			// Watermark advances only after every page and item has committed. Inclusive
			// change time catches boundary revisions; source_revision suppresses repeats.
			kind, history := "SYNC", false
			if c.kind == "HISTORY" && c.state != "DONE" {
				kind, history = "HISTORY", true
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET cycle_id=gen_random_uuid(),kind=$6,state='FETCHING',connection_version=$2,grant_generation=$3,project_name=$4,metadata=$5::jsonb,include_history=$7,snapshot_at=NULL,changed_from=CASE WHEN $7 THEN NULL ELSE watermark END,cursor_id=0,page_ids='{}',item_count=0,data_bytes=0,failures=0,lease=NULL,lease_until=NULL,retry_at=NULL,deadline=clock_timestamp()+interval '2 hours' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, j.job.ID, j.azureVersion, j.azureGeneration, project.Name, raw, kind, history)
			if e != nil {
				return e
			}
			c, e = readAzureCrawl(ctx, tx, j.job.ID)
			return e
		})
		if e != nil {
			return e
		}
	}
	if c.state != "READY" {
		ready, e := w.crawlAzurePass(ctx, j.orgID, j.job.ID, j.lease)
		if e != nil {
			return e
		}
		if !ready {
			return errSyncBacklog
		}
	}
	var records []Record
	items := map[string]syncedItem{}
	ignored := map[string]bool{}
	applied := map[string]bool{}
	e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		c, e = readAzureCrawl(ctx, tx, j.job.ID)
		if e != nil {
			return e
		}
		if c.state != "READY" || c.version != j.azureVersion || c.generation != j.azureGeneration {
			return ErrVersionConflict
		}
		j.azureCycle = c.cycle
		records, e = loadAzureRecords(ctx, tx, c)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT i.external_id,i.task_id::text,i.source_title,i.source_description,t.title,t.description,t.version,t.status_id::text,i.source_status,i.source_status_id,i.local_status_id,i.source_completed_at,i.source_revision,t.deleted_at IS NOT NULL OR t.space_id<>$2::uuid FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.organization_id=i.organization_id AND t.id=i.task_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.job_id=$1::uuid LIMIT 5001`, j.job.ID, j.job.SpaceID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			var v syncedItem
			var deleted bool
			if e = rows.Scan(&id, &v.task, &v.baseline.title, &v.baseline.description, &v.local.title, &v.local.description, &v.version, &v.status, &v.baselineSourceStatus, &v.baselineMappedStatus, &v.baselineStatus, &v.baselineCompleted, &v.sourceRevision, &deleted); e != nil {
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
			return errAzureLimit
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
		rows, e = tx.Query(ctx, `SELECT external_id FROM openfox.import_azure_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND applied`, j.job.ID, c.cycle)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				return e
			}
			applied[strconv.FormatInt(id, 10)] = true
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	remoteItems := make([]pendingItem, 0, len(records))
	for i := range records {
		remoteItems = append(remoteItems, pendingItem{ExternalID: records[i].ID, Record: &records[i]})
	}
	if e = w.discoverSyncStatuses(ctx, j, remoteItems); e != nil {
		return e
	}
	// Explicit process mappings must cover the current source before any writes.
	infos := make([]rowInfo, 0, len(records))
	for i := range records {
		infos = append(infos, rowInfo{Data: j.plan.record(&records[i])})
	}
	e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		l, e := loadLookups(ctx, tx, j.job.SpaceID)
		if e != nil {
			return e
		}
		a := Analysis{Types: []AnalysisType{}, Statuses: []AnalysisStatus{}, Issues: []Issue{}}
		if e = analyzeAzure(ctx, tx, j.job.ID, j.job.SpaceID, j.job.Cfg, infos, l, &a); e != nil {
			return e
		}
		if len(a.Issues) > 0 {
			return ErrConfigIncomplete
		}
		return nil
	})
	if e != nil {
		return e
	}
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
		d := j.plan.record(&r)
		v, exists := items[r.ID]
		skip := ignored[r.ID] || exists && v.sourceRevision >= r.Azure.Revision || !exists && ((rowDone(d) && !j.job.Cfg.Options.ImportDone) || (!rowDone(d) && !j.job.Cfg.Options.ImportOpen))
		if !skip {
			if changes >= 50 {
				return errSyncBacklog
			}
			if !exists && len(items) >= 5000 {
				return errAzureLimit
			}
			var truncated bool
			d.Title, truncated = TruncateRunes(d.Title, maxTitleRunes)
			if truncated {
				j.warns.add("title_truncated", "Azure Boards 标题超过长度限制")
			}
			d.Description, truncated = TruncateRunes(d.Description, maxDescriptionRunes)
			if truncated {
				j.warns.add("description_truncated", "Azure Boards 正文超过长度限制")
			}
			e = w.applySyncItem(ctx, j, pendingItem{ExternalID: r.ID, Record: &r}, d, v, exists)
			if e != nil && !errors.Is(e, errSyncSkipped) {
				return e
			}
			changes++
			if !exists {
				items[r.ID] = syncedItem{}
			}
			if e == nil {
				continue
			}
		}
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			return markAzureNode(ctx, tx, j, r.ID)
		})
		if e != nil {
			return e
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
		tag, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET state='DONE',watermark=snapshot_at,lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND state='READY'`, j.job.ID, c.cycle)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='DONE',lease=NULL,lease_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND project_id=$2::uuid AND kind='WORK_ITEM' AND state='PENDING' AND requested_at<=$3 AND EXISTS(SELECT 1 FROM openfox.import_azure_nodes n WHERE n.organization_id=integration_azure_work.organization_id AND n.job_id=$4::uuid AND n.cycle_id=$5::uuid AND n.external_id=integration_azure_work.object_id AND n.applied)`, j.connection, j.project, c.snapshot, j.job.ID, c.cycle)
		return e
	})
}

// A delete/move/permission-loss hint must not silently disappear from the WIQL
// result. Read unmatched event IDs at this same snapshot; 403/404 are visible
// source-unavailable work, preserving every previously imported local task.
func (w *Worker) reconcileAzureBoardEvents(ctx context.Context, org string, c *azureCrawl, syncLease, organization string) (bool, error) {
	type event struct {
		id      string
		object  int64
		version uint64
	}
	events := []event{}
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.azureCrawlFence(ctx, tx, *c, syncLease); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT w.id::text,w.object_id,w.version FROM openfox.integration_azure_work w WHERE w.organization_id=openfox_private.current_organization_id() AND w.connection_id=$1::uuid AND w.project_id=$2::uuid AND w.kind='WORK_ITEM' AND w.state='PENDING' AND w.connection_version=$3 AND w.grant_generation=$4 AND w.requested_at<=$5 AND NOT EXISTS(SELECT 1 FROM openfox.import_azure_nodes n WHERE n.organization_id=w.organization_id AND n.job_id=$6::uuid AND n.cycle_id=$7::uuid AND n.external_id=w.object_id) ORDER BY w.id LIMIT 20`, c.connection, c.project, c.version, c.generation, c.snapshot, c.job, c.cycle)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v event
			if e = rows.Scan(&v.id, &v.object, &v.version); e != nil {
				return e
			}
			events = append(events, v)
		}
		return rows.Err()
	})
	if e != nil {
		return false, e
	}
	fields, e := c.metadata.selectedFields()
	if e != nil {
		return false, e
	}
	for _, event := range events {
		var item azuredevops.WorkItem
		_, cause := w.azureRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(ctx context.Context, client *azuredevops.Client, token string) error {
			var e error
			item, e = client.WorkItem(ctx, token, organization, azuredevops.Project{ID: c.project, Name: c.projectName}, event.object, *c.snapshot, fields)
			return e
		})
		var upstream *azuredevops.Error
		unavailable := errors.As(cause, &upstream) && (upstream.HTTPStatus == 403 || upstream.HTTPStatus == 404)
		if cause != nil && !unavailable {
			return false, cause
		}
		var raw []byte
		if !unavailable {
			if _, e = azureRecord(item, c.account, organization, c.project, c.metadata); e != nil {
				return false, e
			}
			raw, e = json.Marshal(item)
			if e != nil {
				return false, e
			}
			if c.count+1 > 5000 || c.bytes+len(raw) > MaxFileBytes || len(raw) > 262144 {
				return false, errAzureLimit
			}
		}
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.azureCrawlFence(ctx, tx, *c, syncLease); e != nil {
				return e
			}
			if unavailable {
				_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='FAILED',last_error='source_unavailable',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND state='PENDING'`, event.id, event.version)
				return e
			}
			tag, e := tx.Exec(ctx, `INSERT INTO openfox.import_azure_nodes(organization_id,job_id,cycle_id,external_id,remote_revision,source_data) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::jsonb) ON CONFLICT DO NOTHING`, c.job, c.cycle, item.ID, item.Rev, raw)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return nil
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET item_count=item_count+1,data_bytes=data_bytes+$3 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle, len(raw))
			return e
		})
		if e != nil {
			return false, e
		}
		if !unavailable {
			c.count++
			c.bytes += len(raw)
		}
	}
	return len(events) < 20, nil
}
