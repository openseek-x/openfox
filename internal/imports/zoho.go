package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/membership"
)

const SourceZoho = "zoho_bugtracker_api"

var errZohoCrawl = errors.New("imports: Zoho crawl is incomplete or exceeds limits")

type ZohoRecordMetadata struct {
	StatusLabel   string `json:"status_label"`
	SeverityLabel string `json:"severity_label,omitempty"`
	SeverityID    string `json:"severity_id,omitempty"`
}
type ZohoDraftInput struct {
	Source   integrations.ZohoAccess
	Project  zohobugtracker.Project
	SpaceID  string
	NewSpace *NewSpace
}

func (s Service) CreateZohoDraft(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in ZohoDraftInput) (Job, error) {
	if _, e := integrations.LockZohoAccess(ctx, tx, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.CredentialGeneration); e != nil {
		return Job{}, e
	}
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return Job{}, e
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if in.Project.Status != "active" || !zohobugtracker.ValidID(string(in.Project.ID)) || (in.SpaceID == "") == (in.NewSpace == nil) {
		return Job{}, ErrInvalidRequest
	}
	if e := lockZohoSpaceWriter(ctx, tx, in.SpaceID, actor.UserID); e != nil {
		return Job{}, e
	}
	var locked string
	if e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.organizations WHERE id=openfox_private.current_organization_id() FOR NO KEY UPDATE`).Scan(&locked); e != nil {
		return Job{}, e
	}
	var active int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND status='DRAFT' AND phase='fetching'`).Scan(&active); e != nil {
		return Job{}, e
	}
	if active >= maxActiveFetches {
		return Job{}, ErrImportActive
	}
	id, e := membership.NewID()
	if e != nil {
		return Job{}, e
	}
	cfg := Config{Columns: []ColumnMapping{}, Statuses: []StatusMapping{}, Users: []UserMapping{}, Options: Options{ImportOpen: true, ImportDone: true}}
	sum := sha256.Sum256([]byte("us:" + in.Source.PortalID + "\x00" + string(in.Project.ID)))
	doc := previewDoc{Origin: ProjectOrigin{Connection: in.Source.ConnectionID, ConnectionVersion: in.Source.ConnectionVersion, Project: string(in.Project.ID), Instance: hex.EncodeToString(sum[:])}, Preview: Preview{Columns: []PreviewColumn{}, RowErrors: []RowError{}}, Fetch: &FetchInfo{}, NewSpace: in.NewSpace}
	raw, _ := json.Marshal(doc)
	config, _ := json.Marshal(cfg)
	op := newOperationID()
	_, e = tx.Exec(ctx, `INSERT INTO openfox.import_jobs(organization_id,id,space_id,source,source_label,status,phase,config,preview,operation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,NULLIF($2,'')::uuid,'zoho_bugtracker_api',$3,'DRAFT','fetching',$4::jsonb,$5::jsonb,$6,$7::uuid)`, id, in.SpaceID, asanaProjectLabel(in.Project.Name, string(in.Project.ID)), config, raw, op, actor.UserID)
	if e != nil {
		return Job{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.import_zoho_crawls(organization_id,job_id,connection_id,connection_version,credential_generation,portal_id,project_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6)`, id, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.CredentialGeneration, in.Source.PortalID, string(in.Project.ID))
	if e != nil {
		return Job{}, e
	}
	if e = recordAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "import.draft_created", in.SpaceID, id, op, map[string]string{"source": SourceZoho}); e != nil {
		return Job{}, e
	}
	return s.Get(ctx, tx, org, actor, id)
}
func zohoRecord(b zohobugtracker.Bug) Record {
	r := Record{ID: string(b.ID), Key: string(b.ID), Title: b.Title, TypeName: "Bug", Status: string(b.Status.ID), StatusCategory: "indeterminate", PlainNotes: true, Zoho: &ZohoRecordMetadata{StatusLabel: b.Status.Type}, SourceWarnings: []string{"zoho_limited_fields"}}
	if b.Closed {
		r.StatusCategory = "done"
	}
	if b.Severity != nil {
		r.Priority = string(b.Severity.ID)
		r.Zoho.SeverityLabel = b.Severity.Type
		r.Zoho.SeverityID = string(b.Severity.ID)
	}
	if b.ReporterID != "" {
		p := Person{ID: "us:" + string(b.ReporterID), Name: b.ReporterName}
		r.Reporter = p
	}
	if b.CreatedMillis != nil && *b.CreatedMillis > 0 {
		r.Created = time.UnixMilli(*b.CreatedMillis).UTC().Format(time.RFC3339Nano)
	}
	return r
}

type zohoCrawl struct {
	job, connection, portal, project, partition, lease string
	version, generation                                uint64
	index, count, bytes, requests, failures            int
	deadline                                           time.Time
}

func (w *Worker) zohoRuntime() integrations.ZohoRuntime {
	return integrations.ZohoRuntime{Pool: w.Pool, Service: integrations.Service{Zoho: w.Zoho, Box: w.Secrets}}
}
func readZohoCrawl(ctx context.Context, tx pgx.Tx, job string) (zohoCrawl, error) {
	var c zohoCrawl
	c.job = job
	e := tx.QueryRow(ctx, `SELECT connection_id::text,connection_version,credential_generation,portal_id,project_id,partition,next_index,item_count,data_bytes,request_count,failures,deadline FROM openfox.import_zoho_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, job).Scan(&c.connection, &c.version, &c.generation, &c.portal, &c.project, &c.partition, &c.index, &c.count, &c.bytes, &c.requests, &c.failures, &c.deadline)
	return c, e
}
func lockZohoImportSource(ctx context.Context, tx pgx.Tx, job string) error {
	c, e := readZohoCrawl(ctx, tx, job)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = integrations.LockZohoAccess(ctx, tx, c.connection, c.version, c.generation)
	return e
}
func (w *Worker) zohoJobFence(ctx context.Context, tx pgx.Tx, c zohoCrawl) error {
	var space, user string
	e := tx.QueryRow(ctx, `SELECT coalesce(space_id::text,''),created_by::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND source='zoho_bugtracker_api' AND status='DRAFT' AND phase='fetching' AND lease_owner=$2 AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.job, w.instance()).Scan(&space, &user)
	if e != nil {
		return e
	}
	return lockZohoSpaceWriter(ctx, tx, space, user)
}
func (w *Worker) fetchZohoPass(ctx context.Context, org, job string) error {
	if w.Zoho == nil {
		return w.discardZohoDraft(ctx, org, job)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var c zohoCrawl
	var claimed bool
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = readZohoCrawl(ctx, tx, job)
		if e != nil {
			return e
		}
		if _, e = integrations.LockZohoAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
			return e
		}
		if e = w.zohoJobFence(ctx, tx, c); e != nil {
			return e
		}
		c.lease, e = membership.NewID()
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.import_zoho_crawls SET lease=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND (retry_at IS NULL OR retry_at<=clock_timestamp())`, job, c.lease)
		claimed = tag.RowsAffected() == 1
		return e
	})
	if e != nil {
		if errors.Is(e, integrations.ErrNotFound) || errors.Is(e, integrations.ErrVersionConflict) || errors.Is(e, integrations.ErrNeedsReauth) || errors.Is(e, ErrForbidden) {
			return w.discardZohoDraft(ctx, org, job)
		}
		return e
	}
	if !claimed {
		return nil
	}
	for page := 0; page < 5; page++ {
		if !time.Now().Before(c.deadline) || c.requests >= 160 {
			return w.failZohoCrawl(ctx, org, c, errZohoCrawl)
		}
		var result zohobugtracker.BugPage
		_, e = w.zohoRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(token string) error {
			var e error
			result, e = w.Zoho.BugsPage(ctx, token, c.portal, c.project, c.partition, c.index)
			return e
		})
		c.requests++
		if e != nil {
			return w.failZohoCrawl(ctx, org, c, e)
		}
		if e = w.commitZohoPage(ctx, org, &c, result); e != nil {
			return w.failZohoCrawl(ctx, org, c, e)
		}
		if c.partition == "done" {
			return nil
		}
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e = w.zohoJobFence(ctx, tx, c); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_zoho_crawls SET lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, job, c.lease)
		return e
	})
}
func (w *Worker) commitZohoPage(ctx context.Context, org string, c *zohoCrawl, page zohobugtracker.BugPage) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := integrations.LockZohoAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
			return e
		}
		if e := w.zohoJobFence(ctx, tx, *c); e != nil {
			return e
		}
		var locked string
		if e := tx.QueryRow(ctx, `SELECT job_id::text FROM openfox.import_zoho_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid AND lease_until>clock_timestamp() AND partition=$3 AND next_index=$4 FOR UPDATE`, c.job, c.lease, c.partition, c.index).Scan(&locked); e != nil {
			return e
		}
		ids := []string{}
		for _, b := range page.Bugs {
			ids = append(ids, string(b.ID))
		}
		previous := map[string][]byte{}
		rows, e := tx.Query(ctx, `SELECT external_id,raw FROM openfox.import_items WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND external_id=ANY($2::text[])`, c.job, ids)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			var raw []byte
			if e = rows.Scan(&id, &raw); e != nil {
				rows.Close()
				return e
			}
			previous[id] = raw
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		ordinals := []int32{}
		newIDs := []string{}
		raws := []string{}
		count, dataBytes := c.count, c.bytes
		for _, bug := range page.Bugs {
			record := zohoRecord(bug)
			raw, e := marshalRecord(record)
			if e != nil {
				return e
			}
			if prior, ok := previous[record.ID]; ok {
				var doc struct {
					Record Record `json:"record"`
				}
				if json.Unmarshal(prior, &doc) != nil {
					return errZohoCrawl
				}
				canonical, e := marshalRecord(doc.Record)
				if e != nil || canonical != raw {
					return errZohoCrawl
				}
				continue
			}
			ordinals = append(ordinals, int32(count))
			newIDs = append(newIDs, record.ID)
			raws = append(raws, raw)
			count++
			dataBytes += len(raw)
			if count > 5000 || dataBytes > 10<<20 {
				return errZohoCrawl
			}
		}
		if len(page.Bugs) > 0 && len(newIDs) == 0 {
			return errZohoCrawl
		}
		if len(newIDs) > 0 {
			_, e = tx.Exec(ctx, `INSERT INTO openfox.import_items(organization_id,job_id,ordinal,external_id,raw) SELECT openfox_private.current_organization_id(),$1::uuid,v.ordinal,v.id,v.raw::jsonb FROM unnest($2::int[],$3::text[],$4::text[]) AS v(ordinal,id,raw)`, c.job, ordinals, newIDs, raws)
			if e != nil {
				return e
			}
		}
		partition, index := c.partition, page.NextIndex
		if index == 0 {
			index = 1
			if partition == "open" {
				partition = "closed"
			} else {
				partition = "done"
			}
		}
		if index > 5101 {
			return errZohoCrawl
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.import_zoho_crawls SET partition=$3,next_index=$4,item_count=$5,data_bytes=$6,request_count=$7,failures=0,retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, c.job, c.lease, partition, index, count, dataBytes, c.requests)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=jsonb_set(preview,'{fetch,fetched}',to_jsonb($2::integer)),total_count=$2,lease_expires_at=clock_timestamp()+interval '60 seconds',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, count)
		if e != nil {
			return e
		}
		if partition == "done" {
			var raw, cfgRaw []byte
			var space string
			if e = tx.QueryRow(ctx, `SELECT preview,config,coalesce(space_id::text,'') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job).Scan(&raw, &cfgRaw, &space); e != nil {
				return e
			}
			var doc previewDoc
			var cfg Config
			if json.Unmarshal(raw, &doc) != nil || json.Unmarshal(cfgRaw, &cfg) != nil {
				return errZohoCrawl
			}
			cfg.normalize()
			analysis, infos, e := computeAnalysis(ctx, tx, c.job, space, nil, cfg, true, SourceZoho)
			if e != nil {
				return e
			}
			applyAnalysisToPreview(&doc, analysis, infos)
			doc.Fetch = &FetchInfo{Fetched: count, Total: &count}
			doc.Preview.TotalRows = count
			raw, _ = json.Marshal(doc)
			_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=$2::jsonb,phase='',lease_owner='',lease_expires_at=NULL,retry_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, raw)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.import_zoho_crawls SET lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
			if e != nil {
				return e
			}
		}
		c.count, c.bytes, c.partition, c.index, c.failures = count, dataBytes, partition, index, 0
		return nil
	})
}
func (w *Worker) failZohoCrawl(ctx context.Context, org string, c zohoCrawl, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return w.inOrganization(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		// Cleanup does not need source permission; it never creates business data.
		var job string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='DRAFT' AND phase='fetching' AND lease_owner=$2 FOR UPDATE`, c.job, w.instance()).Scan(&job)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		var lease string
		e = tx.QueryRow(ctx, `SELECT coalesce(lease::text,'') FROM openfox.import_zoho_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid FOR UPDATE`, c.job).Scan(&lease)
		if e != nil {
			return e
		}
		if lease != c.lease {
			return nil
		}
		delay := 30 * time.Second
		transient := false
		var retry *integrations.ZohoRetry
		var upstream *zohobugtracker.Error
		if errors.As(cause, &retry) {
			delay = retry.After
			transient = true
		}
		if errors.As(cause, &upstream) {
			if upstream.HTTPStatus == 429 && upstream.RetryAfterValid {
				delay = upstream.RetryAfter
				transient = true
			} else if upstream.HTTPStatus >= 500 || upstream.Code == zohobugtracker.CodeTimeout || upstream.Code == zohobugtracker.CodeUnavailable {
				transient = true
				c.failures++
			}
		}
		if transient && c.failures < 3 && time.Now().Add(delay).Before(c.deadline) {
			at := time.Now().Add(delay)
			if _, e = tx.Exec(ctx, `UPDATE openfox.import_zoho_crawls SET lease=NULL,lease_until=NULL,retry_at=$2,failures=$3,request_count=$4 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job, at, c.failures, c.requests); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET retry_at=$2,lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, at)
			return e
		}
		return discardFetch(ctx, tx, c.job, "ZOHO_FETCH_FAILED", "Zoho BugTracker 读取失败或超过限制；未创建部分任务，请检查权限与来源后重试")
	})
}

// A missing member or inactive/removed space is an authorization loss, not a
// missing worker lease. Stop fetching promptly instead of retaining a poison job.
func lockZohoSpaceWriter(ctx context.Context, tx pgx.Tx, space, user string) error {
	e := LockAzureSpaceWriter(ctx, tx, space, user)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrForbidden
	}
	return e
}

func (w *Worker) discardZohoDraft(ctx context.Context, org, job string) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='DRAFT' AND phase='fetching' AND lease_owner=$2 FOR UPDATE`, job, w.instance()).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		return discardFetch(ctx, tx, job, "SOURCE_CHANGED", "Zoho 连接、部署配置或目标权限已改变，请重新创建导入")
	})
}
