package imports

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
)

type AzureDraftInput struct {
	Source         integrations.AzureAccess
	Project        azuredevops.Project
	Metadata       AzureMetadata
	Cutover        time.Time
	IncludeHistory bool
	SpaceID        string
	NewSpace       *NewSpace
}

func (s Service) CreateAzureDraft(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in AzureDraftInput) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !azuredevops.ValidUUID(in.Project.ID) || !azuredevops.ValidUUID(in.Source.AccountID) || in.Cutover.IsZero() || (in.SpaceID == "") == (in.NewSpace == nil) {
		return Job{}, ErrInvalidRequest
	}
	if _, err := integrations.LockAzureAccess(ctx, tx, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.GrantGeneration); err != nil {
		return Job{}, err
	}
	if _, err := in.Metadata.selectedFields(); err != nil {
		return Job{}, err
	}
	metaRaw, _ := json.Marshal(in.Metadata)
	if len(metaRaw) > 262144 {
		return Job{}, errAzureLimit
	}
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.organizations WHERE id=openfox_private.current_organization_id() FOR NO KEY UPDATE`).Scan(&locked); err != nil {
		return Job{}, err
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND status='DRAFT' AND phase='fetching'`).Scan(&active); err != nil {
		return Job{}, err
	}
	if active >= maxActiveFetches {
		return Job{}, ErrImportActive
	}
	id, err := membership.NewID()
	if err != nil {
		return Job{}, err
	}
	cfg := Config{Columns: []ColumnMapping{}, Statuses: []StatusMapping{}, Users: []UserMapping{}, Options: Options{ImportOpen: true, ImportDone: true, ContinuousSync: true}}
	doc := previewDoc{Origin: ProjectOrigin{Connection: in.Source.ConnectionID, ConnectionVersion: in.Source.ConnectionVersion, Project: in.Project.ID, Instance: azureInstance(in.Source.AccountID, in.Project.ID)}, Preview: Preview{Columns: []PreviewColumn{}, RowErrors: []RowError{}}, Fetch: &FetchInfo{}, NewSpace: in.NewSpace}
	cfgRaw, _ := json.Marshal(cfg)
	previewRaw, _ := json.Marshal(doc)
	operation := newOperationID()
	_, err = tx.Exec(ctx, `INSERT INTO openfox.import_jobs(organization_id,id,space_id,source,source_label,status,phase,config,preview,operation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,NULLIF($2,'')::uuid,'azure_devops_api',$3,'DRAFT','fetching',$4::jsonb,$5::jsonb,$6,$7::uuid)`, id, in.SpaceID, asanaProjectLabel(in.Project.Name, in.Project.ID), cfgRaw, previewRaw, operation, actor.UserID)
	if err != nil {
		return Job{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.import_azure_crawls(organization_id,job_id,kind,connection_id,connection_version,grant_generation,account_id,project_id,project_name,metadata,include_history,cutover,watermark) VALUES(openfox_private.current_organization_id(),$1::uuid,'INITIAL',$2::uuid,$3,$4,$5::uuid,$6::uuid,$7,$8::jsonb,$9,$10,$10)`, id, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.GrantGeneration, in.Source.AccountID, in.Project.ID, in.Project.Name, metaRaw, in.IncludeHistory, in.Cutover)
	if err != nil {
		return Job{}, err
	}
	if err = recordAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "import.draft_created", in.SpaceID, id, operation, map[string]string{"source": SourceAzure}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, org, actor, id)
}

type azureCrawl struct {
	job, cycle, kind, state, connection, account, project, projectName, lease string
	version, generation                                                       uint64
	cursor                                                                    int64
	page                                                                      []int64
	count, bytes, failures                                                    int
	history                                                                   bool
	cutover, watermark, deadline                                              time.Time
	snapshot, changedFrom                                                     *time.Time
	metadata                                                                  AzureMetadata
}

func readAzureCrawl(ctx context.Context, tx pgx.Tx, job string) (azureCrawl, error) {
	c := azureCrawl{job: job}
	var meta []byte
	err := tx.QueryRow(ctx, `SELECT cycle_id::text,kind,state,connection_id::text,connection_version,grant_generation,account_id::text,project_id::text,project_name,metadata,include_history,cutover,watermark,snapshot_at,changed_from,cursor_id,page_ids,item_count,data_bytes,failures,deadline FROM openfox.import_azure_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, job).Scan(&c.cycle, &c.kind, &c.state, &c.connection, &c.version, &c.generation, &c.account, &c.project, &c.projectName, &meta, &c.history, &c.cutover, &c.watermark, &c.snapshot, &c.changedFrom, &c.cursor, &c.page, &c.count, &c.bytes, &c.failures, &c.deadline)
	if err == nil {
		err = json.Unmarshal(meta, &c.metadata)
	}
	return c, err
}
func (w *Worker) azureRuntime() integrations.AzureRuntime {
	return integrations.AzureRuntime{Pool: w.Pool, Service: integrations.Service{Azure: w.Azure, Box: w.Secrets}}
}
func (w *Worker) azureJobFence(ctx context.Context, tx pgx.Tx, c azureCrawl, syncLease string) error {
	var id string
	if c.kind == "INITIAL" {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND source='azure_devops_api' AND status='DRAFT' AND phase='fetching' AND lease_owner=$2 AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.job, w.instance()).Scan(&id)
	}
	return tx.QueryRow(ctx, `SELECT l.job_id::text FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.lease=$2::uuid AND l.state='ACTIVE' AND j.status='SUCCEEDED' FOR UPDATE OF l`, c.job, syncLease).Scan(&id)
}
func (w *Worker) azureCrawlFence(ctx context.Context, tx pgx.Tx, c azureCrawl, syncLease string) error {
	if _, e := integrations.LockAzureAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
		return e
	}
	var user, space string
	if e := tx.QueryRow(ctx, `SELECT coalesce(started_by,created_by)::text,coalesce(space_id::text,'') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job).Scan(&user, &space); e != nil {
		return e
	}
	if e := LockAzureSpaceWriter(ctx, tx, space, user); e != nil {
		return e
	}
	if e := checkProjectCrawlActor(ctx, tx, c.job); e != nil {
		return e
	}
	if e := w.azureJobFence(ctx, tx, c, syncLease); e != nil {
		return e
	}
	if c.lease != "" {
		var cycle string
		if e := tx.QueryRow(ctx, `SELECT cycle_id::text FROM openfox.import_azure_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid AND lease_until>clock_timestamp() FOR UPDATE`, c.job, c.lease).Scan(&cycle); e != nil {
			return e
		}
		if cycle != c.cycle {
			return ErrVersionConflict
		}
	}
	return nil
}
func (w *Worker) fetchAzurePass(ctx context.Context, org, job string) error {
	_, err := w.crawlAzurePass(ctx, org, job, "")
	return err
}

// One pass advances at most a clock read, one persisted WIQL page and one batch.
// Successful query IDs survive rate limits/restarts before the batch read.
func (w *Worker) crawlAzurePass(ctx context.Context, org, job, syncLease string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var c azureCrawl
	var acquired bool
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = readAzureCrawl(ctx, tx, job)
		if e != nil {
			return e
		}
		if e = w.azureCrawlFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		lease, e := membership.NewID()
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET lease=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND state IN ('FETCHING','READY') AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND (retry_at IS NULL OR retry_at<=clock_timestamp())`, job, lease)
		if e == nil {
			acquired = tag.RowsAffected() == 1
			c.lease = lease
		}
		return e
	})
	if err != nil {
		if c.kind == "INITIAL" && (errors.Is(err, integrations.ErrNotFound) || errors.Is(err, integrations.ErrVersionConflict) || errors.Is(err, integrations.ErrNeedsReauth) || errors.Is(err, ErrForbidden)) {
			err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				if e := w.azureJobFence(ctx, tx, c, syncLease); e != nil {
					return e
				}
				if _, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET state='FAILED',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle); e != nil {
					return e
				}
				return discardFetch(ctx, tx, c.job, "SOURCE_CHANGED", "Azure DevOps 连接或权限已改变，请重新创建导入")
			})
		}
		return false, err
	}
	if !acquired {
		return c.state == "DONE", nil
	}
	fail := func(e error) (bool, error) { return false, w.failAzureCrawl(ctx, org, c, syncLease, e) }
	if !time.Now().Before(c.deadline) {
		return fail(errAzureLimit)
	}
	if c.state == "READY" {
		err = w.finishAzureInitial(ctx, org, c, syncLease)
		if err != nil {
			return fail(err)
		}
		return true, nil
	}
	// Default incremental activation must wait for all four owned hooks. Full
	// history remains a snapshot import; failed hooks are visible in configuration.
	if c.kind == "INITIAL" && !c.history {
		var ready bool
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*)=4 AND bool_and(state='ACTIVE' AND connection_version=$3 AND grant_generation=$4) FROM openfox.integration_azure_hooks WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND project_id=$2::uuid AND repository_id IS NULL AND desired`, c.connection, c.project, c.version, c.generation).Scan(&ready)
		})
		if err != nil {
			return fail(err)
		}
		if !ready {
			return fail(&integrations.AzureRetry{After: 30 * time.Second})
		}
	}
	var organization string
	err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := integrations.LockAzureAccess(ctx, tx, c.connection, c.version, c.generation)
		organization = a.OrganizationName
		return e
	})
	if err != nil {
		return fail(err)
	}
	if c.snapshot == nil {
		var snapshot time.Time
		_, err = w.azureRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(ctx context.Context, client *azuredevops.Client, token string) error {
			var e error
			snapshot, e = client.WorkItemCutover(ctx, token, organization, c.project)
			return e
		})
		// Organization slug is read through the same source fence, never supplied by a job.
		if err != nil {
			return fail(err)
		}
		c.snapshot = &snapshot
		if !c.history && c.changedFrom == nil {
			from := c.cutover
			c.changedFrom = &from
		}
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.azureCrawlFence(ctx, tx, c, syncLease); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET snapshot_at=$3,changed_from=$4 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, job, c.cycle, c.snapshot, c.changedFrom)
			return e
		})
		if err != nil {
			return fail(err)
		}
	}
	if len(c.page) == 0 {
		var page azuredevops.WorkItemPage
		var through *time.Time
		if c.changedFrom != nil {
			through = c.snapshot
		}
		_, err = w.azureRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(ctx context.Context, client *azuredevops.Client, token string) error {
			var e error
			page, e = client.QueryWorkItems(ctx, token, organization, c.project, azuredevops.WorkItemQuery{AsOf: *c.snapshot, AfterID: c.cursor, ChangedFrom: c.changedFrom, ChangedThrough: through})
			return e
		})
		if err != nil {
			return fail(err)
		}
		c.page = page.IDs
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.azureCrawlFence(ctx, tx, c, syncLease); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET page_ids=$3::integer[] WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, job, c.cycle, c.page)
			return e
		})
		if err != nil {
			return fail(err)
		}
	}
	if len(c.page) > 0 {
		if c.count+len(c.page) > MaxDataRows {
			return fail(errAzureLimit)
		}
		fields, e := c.metadata.selectedFields()
		if e != nil {
			return fail(e)
		}
		var items []azuredevops.WorkItem
		_, err = w.azureRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(ctx context.Context, client *azuredevops.Client, token string) error {
			var e error
			items, e = client.WorkItems(ctx, token, organization, azuredevops.Project{ID: c.project, Name: c.projectName}, c.page, *c.snapshot, fields)
			return e
		})
		if err != nil {
			return fail(err)
		}
		if err = w.commitAzurePage(ctx, org, &c, syncLease, items, organization); err != nil {
			return fail(err)
		}
	} else {
		c.state = "READY"
	}
	if c.state == "READY" {
		err = w.finishAzureInitial(ctx, org, c, syncLease)
		if err != nil {
			return fail(err)
		}
		return true, nil
	}
	err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.azureCrawlFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, job, c.cycle)
		return e
	})
	return false, err
}

func (w *Worker) commitAzurePage(ctx context.Context, org string, c *azureCrawl, syncLease string, items []azuredevops.WorkItem, organization string) error {
	ids, revs, raws := []int64{}, []int64{}, []string{}
	bytes := 0
	for _, v := range items {
		if _, e := azureRecord(v, c.account, organization, c.project, c.metadata); e != nil {
			return e
		}
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if len(raw) > 262144 {
			return errAzureLimit
		}
		bytes += len(raw)
		ids = append(ids, v.ID)
		revs = append(revs, v.Rev)
		raws = append(raws, string(raw))
	}
	if c.bytes+bytes > MaxFileBytes {
		return errAzureLimit
	}
	cursor := c.cursor
	for _, id := range c.page {
		cursor = max(cursor, id)
	}
	state := "FETCHING"
	if len(c.page) < 200 {
		state = "READY"
	}
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.azureCrawlFence(ctx, tx, *c, syncLease); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO openfox.import_azure_nodes(organization_id,job_id,cycle_id,external_id,remote_revision,source_data) SELECT openfox_private.current_organization_id(),$1::uuid,$2::uuid,v.id,v.rev,v.raw::jsonb FROM unnest($3::integer[],$4::integer[],$5::text[]) AS v(id,rev,raw)`, c.job, c.cycle, ids, revs, raws)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET cursor_id=$3,page_ids='{}',item_count=item_count+$4,data_bytes=data_bytes+$5,state=$6,failures=0,retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle, cursor, len(items), bytes, state)
		if e != nil {
			return e
		}
		if c.kind == "INITIAL" {
			_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=jsonb_set(preview,'{fetch,fetched}',to_jsonb($2::integer)),total_count=$2,lease_expires_at=clock_timestamp()+interval '60 seconds',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, c.count+len(items))
		}
		return e
	})
	if err == nil {
		c.count += len(items)
		c.bytes += bytes
		c.cursor = cursor
		c.page = nil
		c.state = state
	}
	return err
}

func loadAzureRecords(ctx context.Context, tx pgx.Tx, c azureCrawl) ([]Record, error) {
	var organization string
	if err := tx.QueryRow(ctx, `SELECT settings->>'organization_name' FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.connection).Scan(&organization); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT source_data FROM openfox.import_azure_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid ORDER BY external_id LIMIT 5001`, c.job, c.cycle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []Record{}
	parents := map[string]string{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v azuredevops.WorkItem
		if json.Unmarshal(raw, &v) != nil {
			return nil, errAzureShape
		}
		r, e := azureRecord(v, c.account, organization, c.project, c.metadata)
		if e != nil {
			return nil, e
		}
		records = append(records, r)
		parents[r.ID] = r.ParentID
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(records) > MaxDataRows {
		return nil, errAzureLimit
	}
	// Incremental pages omit unchanged parents. An existing local copy from
	// this exact source and space remains an eligible parent; this is not a
	// claim that its current remote project membership was re-verified.
	rows.Close()
	if c.kind != "INITIAL" {
		wanted := []string{}
		for _, r := range records {
			if r.ParentID != "" {
				if _, ok := parents[r.ParentID]; !ok {
					wanted = append(wanted, r.ParentID)
				}
			}
		}
		if len(wanted) > 0 {
			mapped, e := tx.Query(ctx, `SELECT m.external_id FROM openfox.import_mappings m JOIN openfox.import_jobs j ON j.organization_id=m.organization_id AND j.space_id=m.space_id AND m.source_system='azure_devops' AND m.source_instance=j.preview->'project_origin'->>'instance' WHERE j.organization_id=openfox_private.current_organization_id() AND j.id=$1::uuid AND m.external_id=ANY($2::text[]) AND m.released_at IS NULL`, c.job, wanted)
			if e != nil {
				return nil, e
			}
			for mapped.Next() {
				var id string
				if e = mapped.Scan(&id); e != nil {
					mapped.Close()
					return nil, e
				}
				parents[id] = ""
			}
			e = mapped.Err()
			mapped.Close()
			if e != nil {
				return nil, e
			}
		}
	}
	for i := range records {
		r := &records[i]
		if r.ParentID != "" {
			if _, ok := parents[r.ParentID]; !ok {
				r.SourceWarnings = append(r.SourceWarnings, "azure_parent_outside_project")
				r.ParentID = ""
			}
		}
		seen := map[string]bool{}
		for id, depth := r.ID, 0; id != ""; id, depth = parents[id], depth+1 {
			if seen[id] || depth >= 100 {
				return nil, errAzureShape
			}
			seen[id] = true
		}
	}
	return records, nil
}

func (w *Worker) finishAzureInitial(ctx context.Context, org string, c azureCrawl, syncLease string) error {
	var organization string
	if e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := integrations.LockAzureAccess(ctx, tx, c.connection, c.version, c.generation)
		organization = a.OrganizationName
		return e
	}); e != nil {
		return e
	}
	ready, e := w.reconcileAzureBoardEvents(ctx, org, &c, syncLease, organization)
	if e != nil {
		return e
	}
	if !ready {
		return &integrations.AzureRetry{After: time.Second}
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.azureCrawlFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		records, err := loadAzureRecords(ctx, tx, c)
		if err != nil {
			return err
		}
		if c.kind != "INITIAL" {
			_, err = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET state='READY',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
			return err
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		for start := 0; start < len(records); start += insertChunk {
			end := min(start+insertChunk, len(records))
			ordinals, ids, raws := []int32{}, []string{}, []string{}
			for i := start; i < end; i++ {
				raw, e := marshalRecord(records[i])
				if e != nil {
					return e
				}
				ordinals = append(ordinals, int32(i))
				ids = append(ids, records[i].ID)
				raws = append(raws, raw)
			}
			_, err = tx.Exec(ctx, `INSERT INTO openfox.import_items(organization_id,job_id,ordinal,external_id,raw) SELECT openfox_private.current_organization_id(),$1::uuid,v.ordinal,v.id,v.raw::jsonb FROM unnest($2::int[],$3::text[],$4::text[]) AS v(ordinal,id,raw)`, c.job, ordinals, ids, raws)
			if err != nil {
				return err
			}
		}
		var preview, config []byte
		var space string
		if err = tx.QueryRow(ctx, `SELECT preview,config,coalesce(space_id::text,'') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job).Scan(&preview, &config, &space); err != nil {
			return err
		}
		var doc previewDoc
		var cfg Config
		if json.Unmarshal(preview, &doc) != nil || json.Unmarshal(config, &cfg) != nil {
			return errAzureShape
		}
		analysis, infos, err := computeAnalysis(ctx, tx, c.job, space, nil, cfg, true, SourceAzure)
		if err != nil {
			return err
		}
		applyAnalysisToPreview(&doc, analysis, infos)
		count := len(records)
		doc.Fetch = &FetchInfo{Fetched: count, Total: &count}
		doc.Preview.TotalRows = count
		raw, _ := json.Marshal(doc)
		_, err = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=$2::jsonb,total_count=$3,phase='',lease_owner='',lease_expires_at=NULL,retry_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, raw, count)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET state='DONE',watermark=coalesce(snapshot_at,cutover),lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
		return err
	})
}

func (w *Worker) failAzureCrawl(ctx context.Context, org string, c azureCrawl, syncLease string, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := w.inOrganization(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.azureJobFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		var lease string
		if e := tx.QueryRow(ctx, `SELECT coalesce(lease::text,'') FROM openfox.import_azure_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid FOR UPDATE`, c.job, c.cycle).Scan(&lease); e != nil {
			return e
		}
		if lease != c.lease {
			return nil
		}
		var retry *integrations.AzureRetry
		var upstream *azuredevops.Error
		transient := false
		delay := 30 * time.Second
		failures := c.failures
		if errors.As(cause, &retry) {
			transient = true
			delay = max(time.Second, retry.After)
		}
		if errors.As(cause, &upstream) {
			if upstream.Code == "rate_limited" || upstream.HTTPStatus == 429 {
				transient = true
				delay = max(delay, upstream.RetryAfter)
			} else if upstream.HTTPStatus >= 500 || upstream.Code == "timeout" || upstream.Code == "unavailable" {
				transient = true
				failures++
			}
		}
		if transient && failures < 3 && time.Now().Add(delay).Before(c.deadline) {
			_, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET lease=NULL,lease_until=NULL,failures=$3,retry_at=clock_timestamp()+make_interval(secs=>$4) WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle, failures, delay.Seconds())
			if e != nil {
				return e
			}
			if c.kind == "INITIAL" {
				_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET retry_at=clock_timestamp()+make_interval(secs=>$2) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, delay.Seconds())
			}
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET state='FAILED',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle)
		if e != nil {
			return e
		}
		if c.kind == "INITIAL" {
			return discardFetch(ctx, tx, c.job, "AZURE_CRAWL_FAILED", "Azure Boards 抓取失败或超过限制；未创建部分任务，请检查来源与权限后重新导入")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if c.kind != "INITIAL" {
		return cause
	}
	return nil
}
