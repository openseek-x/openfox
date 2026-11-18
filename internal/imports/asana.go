package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/asana"
	"openfox/internal/membership"
)

const SourceAsana = "asana_api"

var errAsanaLimit = errors.New("imports: Asana source exceeds bounded crawl limits")
var errAsanaShape = errors.New("imports: Asana source hierarchy is inconsistent")

type AsanaDraftInput struct {
	Source   integrations.AsanaAccess
	Project  asana.Project
	SpaceID  string
	NewSpace *NewSpace
}

func (s Service) CreateAsanaDraft(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in AsanaDraftInput) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if in.Project.GID == "" || in.Project.Workspace.GID != in.Source.WorkspaceID || in.Project.Archived || (in.SpaceID == "") == (in.NewSpace == nil) {
		return Job{}, ErrInvalidRequest
	}
	if _, err := integrations.LockAsanaAccess(ctx, tx, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.GrantGeneration); err != nil {
		return Job{}, err
	}
	// Serialize only creation against the existing org row, making the active
	// fetch bound enforceable for concurrent requests within this provider.
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
	cfg := Config{Columns: []ColumnMapping{}, Statuses: []StatusMapping{}, Users: []UserMapping{}, Options: Options{ImportOpen: true, ImportDone: true}}
	sum := sha256.Sum256([]byte(asana.APIURL + "\x00" + in.Source.WorkspaceID + "\x00" + in.Project.GID))
	doc := previewDoc{Origin: ProjectOrigin{Connection: in.Source.ConnectionID, ConnectionVersion: in.Source.ConnectionVersion, Project: in.Project.GID, Instance: hex.EncodeToString(sum[:])}, Preview: Preview{Columns: []PreviewColumn{}, RowErrors: []RowError{}}, Fetch: &FetchInfo{}, NewSpace: in.NewSpace}
	configRaw, _ := json.Marshal(cfg)
	previewRaw, _ := json.Marshal(doc)
	operation := newOperationID()
	_, err = tx.Exec(ctx, `INSERT INTO openfox.import_jobs(organization_id,id,space_id,source,source_label,status,phase,config,preview,operation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,NULLIF($2,'')::uuid,'asana_api',$3,'DRAFT','fetching',$4::jsonb,$5::jsonb,$6,$7::uuid)`, id, in.SpaceID, asanaProjectLabel(in.Project.Name, in.Project.GID), configRaw, previewRaw, operation, actor.UserID)
	if err != nil {
		return Job{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.import_asana_crawls(organization_id,job_id,kind,connection_id,connection_version,grant_generation,workspace_id,project_id) VALUES(openfox_private.current_organization_id(),$1::uuid,'INITIAL',$2::uuid,$3,$4,$5,$6)`, id, in.Source.ConnectionID, in.Source.ConnectionVersion, in.Source.GrantGeneration, in.Source.WorkspaceID, in.Project.GID)
	if err != nil {
		return Job{}, err
	}
	if err = recordAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "import.draft_created", in.SpaceID, id, operation, map[string]string{"source": SourceAsana}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, org, actor, id)
}

func asanaRecord(t asana.Task) Record {
	r := Record{ID: t.GID, Key: t.GID, Title: t.Name, Description: t.Notes, Created: t.CreatedAt, Updated: t.ModifiedAt, Due: t.DueOn, Start: t.StartOn, URL: t.PermalinkURL, Status: "incomplete", StatusCategory: "new", TypeName: t.ResourceSubtype, CompletedAt: t.CompletedAt, PlainNotes: true}
	if r.TypeName == "" {
		r.TypeName = "default_task"
	}
	if t.ApprovalStatus != "" || t.ResourceSubtype == "approval" {
		r.SourceWarnings = append(r.SourceWarnings, "asana_approval_not_recreated")
	}
	if t.DueAt != "" {
		r.SourceWarnings = append(r.SourceWarnings, "asana_timed_due_not_imported")
	}
	if t.Completed {
		r.Status = "completed"
		r.StatusCategory = "done"
	}
	if t.Parent != nil {
		r.ParentID = t.Parent.GID
	}
	if t.Assignee != nil {
		r.Assignee = Person{ID: t.Assignee.GID, Name: t.Assignee.Name}
	}
	return r
}

type asanaCrawl struct {
	job, cycle, kind, state, connection, workspace, project, lease, offset, parent string
	version, generation                                                            uint64
	depth, requests, bytes, count, failures, restarts                              int
	rootsDone                                                                      bool
	offsets                                                                        []string
	deadline                                                                       time.Time
}

func (w *Worker) asanaRuntime() integrations.AsanaRuntime {
	return integrations.AsanaRuntime{Pool: w.Pool, Service: integrations.Service{Asana: w.Asana, Box: w.Secrets}}
}
func readAsanaCrawl(ctx context.Context, tx pgx.Tx, job string, lock bool) (asanaCrawl, error) {
	var c asanaCrawl
	c.job = job
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT cycle_id::text,kind,state,connection_id::text,connection_version,grant_generation,workspace_id,project_id,coalesce(lease::text,''),root_offset,root_offsets,roots_done,request_count,data_bytes,item_count,failures,offset_restarts,deadline FROM openfox.import_asana_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`+suffix, job).Scan(&c.cycle, &c.kind, &c.state, &c.connection, &c.version, &c.generation, &c.workspace, &c.project, &c.lease, &c.offset, &c.offsets, &c.rootsDone, &c.requests, &c.bytes, &c.count, &c.failures, &c.restarts, &c.deadline)
	return c, err
}
func (w *Worker) fetchAsanaPass(ctx context.Context, org, job string) error {
	_, err := w.crawlAsanaPass(ctx, org, job, "")
	return err
}

// crawlAsanaPass commits at most five pages. Cursor, node identity and frontier
// survive process restarts; no task is imported before the full crawl is READY.
func (w *Worker) crawlAsanaPass(ctx context.Context, org, job, syncLease string) (bool, error) {
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ctx = bounded
	if w.Asana == nil {
		return false, integrations.ErrNotConfigured
	}
	var c asanaCrawl
	var found bool
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = readAsanaCrawl(ctx, tx, job, false)
		if e != nil {
			return e
		}
		if _, e = integrations.LockAsanaAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
			return e
		}
		if e = w.asanaJobFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		if e = checkProjectCrawlActor(ctx, tx, c.job); e != nil {
			return e
		}
		lease, e := membership.NewID()
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET lease=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND (retry_at IS NULL OR retry_at<=clock_timestamp()) AND state IN ('FETCHING','READY')`, job, lease)
		if e != nil {
			return e
		}
		found = tag.RowsAffected() == 1
		c.lease = lease
		return nil
	})
	if err != nil && c.kind == "INITIAL" && (errors.Is(err, integrations.ErrNotFound) || errors.Is(err, integrations.ErrVersionConflict) || errors.Is(err, integrations.ErrNeedsReauth) || errors.Is(err, ErrForbidden)) {
		e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.asanaJobFence(ctx, tx, c, syncLease); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET state='FAILED',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, job); e != nil {
				return e
			}
			return discardFetch(ctx, tx, job, "SOURCE_CHANGED", "Asana 连接已改变，请重新创建导入")
		})
		return false, e
	}
	if err != nil || !found {
		return false, err
	}
	for page := 0; page < 5; page++ {
		if !time.Now().Before(c.deadline) || c.requests >= 10000 {
			return false, w.failAsanaCrawl(ctx, org, c, syncLease, errAsanaLimit)
		}
		if c.state == "READY" {
			e := w.finishAsanaInitial(ctx, org, c, syncLease)
			if e != nil {
				return false, w.failAsanaCrawl(ctx, org, c, syncLease, e)
			}
			return true, nil
		}
		c.parent = ""
		c.depth = 0
		if c.rootsDone {
			err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT external_id,child_offset,child_offsets,discovery_depth FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND NOT children_done ORDER BY discovery_depth,external_id LIMIT 1`, job, c.cycle).Scan(&c.parent, &c.offset, &c.offsets, &c.depth)
			})
			if errors.Is(err, pgx.ErrNoRows) {
				c.state = "READY"
				e := w.finishAsanaInitial(ctx, org, c, syncLease)
				if e != nil {
					return false, w.failAsanaCrawl(ctx, org, c, syncLease, e)
				}
				return true, nil
			}
			if err != nil {
				return false, err
			}
		}
		// Count every attempted page, including rejected offsets and 429s.
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := checkProjectCrawlActor(ctx, tx, c.job); e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET request_count=request_count+1 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid AND lease_until>clock_timestamp() AND request_count<10000`, job, c.lease)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return errAsanaLimit
			}
			return nil
		})
		if err != nil {
			return false, w.failAsanaCrawl(ctx, org, c, syncLease, err)
		}
		c.requests++
		var result asana.TaskPage
		_, err = w.asanaRuntime().Do(ctx, org, c.connection, c.version, c.generation, func(token string) error {
			var e error
			if c.parent == "" {
				result, e = w.Asana.TasksPage(ctx, token, c.project, c.offset)
			} else {
				result, e = w.Asana.SubtasksPage(ctx, token, c.parent, c.offset)
			}
			return e
		})
		if err != nil {
			return false, w.failAsanaCrawl(ctx, org, c, syncLease, err)
		}
		err = w.commitAsanaPage(ctx, org, &c, syncLease, result)
		if err != nil {
			return false, w.failAsanaCrawl(ctx, org, c, syncLease, err)
		}
	}
	err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, job, c.lease)
		return e
	})
	return false, err
}
func (w *Worker) asanaJobFence(ctx context.Context, tx pgx.Tx, c asanaCrawl, syncLease string) error {
	var id string
	if c.kind == "INITIAL" {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND source='asana_api' AND status='DRAFT' AND phase='fetching' AND lease_owner=$2 AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.job, w.instance()).Scan(&id)
	}
	return tx.QueryRow(ctx, `SELECT l.job_id::text FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.lease=$2::uuid AND l.state='ACTIVE' AND j.status='SUCCEEDED' FOR UPDATE OF l`, c.job, syncLease).Scan(&id)
}
func (w *Worker) commitAsanaPage(ctx context.Context, org string, c *asanaCrawl, syncLease string, page asana.TaskPage) error {
	if page.NextOffset != "" {
		sum := sha256.Sum256([]byte(page.NextOffset))
		hash := hex.EncodeToString(sum[:])
		for _, old := range c.offsets {
			if old == hash {
				return errAsanaShape
			}
		}
		if len(c.offsets) >= 50 {
			return errAsanaLimit
		}
		c.offsets = append(c.offsets, hash)
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := integrations.LockAsanaAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
			return e
		}
		if e := w.asanaJobFence(ctx, tx, *c, syncLease); e != nil {
			return e
		}
		if e := checkProjectCrawlActor(ctx, tx, c.job); e != nil {
			return e
		}
		var current string
		if e := tx.QueryRow(ctx, `SELECT cycle_id::text FROM openfox.import_asana_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid AND lease_until>clock_timestamp() FOR UPDATE`, c.job, c.lease).Scan(&current); e != nil {
			return e
		}
		if current != c.cycle {
			return errAsanaShape
		}
		for _, task := range page.Data {
			if task.Workspace.GID != c.workspace || task.Parent != nil && task.Parent.GID == task.GID {
				return errAsanaShape
			}
			if c.parent != "" && c.depth >= 9 {
				return errAsanaShape
			}
			if c.parent != "" && (task.Parent == nil || task.Parent.GID != c.parent) {
				return errAsanaShape
			}
			if c.parent == "" {
				linked := false
				for _, p := range task.Projects {
					if p.GID == c.project {
						linked = true
					}
				}
				if !linked {
					return errAsanaShape
				}
			}
			raw, e := json.Marshal(task)
			if e != nil {
				return e
			}
			if len(raw) > 262144 {
				return errAsanaLimit
			}
			parent := ""
			if task.Parent != nil {
				parent = task.Parent.GID
			}
			var previous []byte
			e = tx.QueryRow(ctx, `SELECT source_data FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND external_id=$3`, c.job, c.cycle, task.GID).Scan(&previous)
			if e == nil {
				var old asana.Task
				if json.Unmarshal(previous, &old) != nil {
					return errAsanaShape
				}
				encoded, _ := json.Marshal(old)
				if string(encoded) != string(raw) {
					return errAsanaShape
				}
				continue
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			c.count++
			c.bytes += len(raw)
			if c.count > 5000 || c.bytes > 10<<20 {
				return errAsanaLimit
			}
			_, e = tx.Exec(ctx, `INSERT INTO openfox.import_asana_nodes(organization_id,job_id,cycle_id,external_id,parent_id,source_data,children_done,discovery_depth) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::jsonb,$6,$7)`, c.job, c.cycle, task.GID, parent, raw, task.NumSubtasks == 0, func() int {
				if c.parent != "" {
					return c.depth + 1
				}
				return 0
			}())
			if e != nil {
				return e
			}
		}
		if c.parent == "" {
			c.rootsDone = page.NextOffset == ""
			_, err := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET root_offset=$3,root_offsets=$4,roots_done=$5 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle, page.NextOffset, c.offsets, c.rootsDone)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(ctx, `UPDATE openfox.import_asana_nodes SET child_offset=$4,child_offsets=$5,children_done=$6 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid AND external_id=$3`, c.job, c.cycle, c.parent, page.NextOffset, c.offsets, page.NextOffset == "")
			if err != nil {
				return err
			}
		}
		c.offset = page.NextOffset
		c.failures = 0
		_, err := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET request_count=$3,data_bytes=$4,item_count=$5,failures=0,retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid`, c.job, c.cycle, c.requests, c.bytes, c.count)
		if err != nil {
			return err
		}
		if c.kind == "INITIAL" {
			_, err = tx.Exec(ctx, `UPDATE openfox.import_jobs SET preview=jsonb_set(preview,'{fetch,fetched}',to_jsonb($2::integer)),total_count=$2,lease_expires_at=clock_timestamp()+interval '60 seconds',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, c.count)
		}
		return err
	})
}
func (w *Worker) failAsanaCrawl(ctx context.Context, org string, c asanaCrawl, syncLease string, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var outward error
	err := w.inOrganization(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := w.asanaJobFence(ctx, tx, c, syncLease); err != nil {
			return err
		}
		var lease string
		if err := tx.QueryRow(ctx, `SELECT coalesce(lease::text,'') FROM openfox.import_asana_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid FOR UPDATE`, c.job).Scan(&lease); err != nil {
			return err
		}
		if lease != c.lease {
			return nil
		}
		var upstream *asana.Error
		var retry *integrations.AsanaRetry
		delay := 30 * time.Second
		transient := false
		if errors.As(cause, &retry) {
			delay = retry.After
			transient = true
		}
		if errors.As(cause, &upstream) {
			if upstream.HTTPStatus == 429 {
				delay = max(upstream.RetryAfter, 30*time.Second)
				transient = true
			}
			if upstream.HTTPStatus >= 500 || upstream.Code == asana.CodeTimeout || upstream.Code == asana.CodeUnavailable {
				transient = true
				c.failures++
			}
			if upstream.Code == asana.CodeBadRequest && c.offset != "" && c.restarts == 0 {
				// Expiring pagination offsets get one bounded full restart, not repeated
				// hidden truncation. No imported tasks exist until the snapshot completes.
				if _, e := tx.Exec(ctx, `DELETE FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job); e != nil {
					return e
				}
				_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET root_offset='',root_offsets='{}',roots_done=false,item_count=0,data_bytes=0,offset_restarts=1,lease=NULL,lease_until=NULL,retry_at=clock_timestamp()+interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
				return e
			}
		}
		if transient && c.failures < 3 && time.Now().Add(delay).Before(c.deadline) {
			_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET lease=NULL,lease_until=NULL,failures=$2,retry_at=clock_timestamp()+make_interval(secs=>$3) WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job, c.failures, delay.Seconds())
			if e != nil {
				return e
			}
			if c.kind == "INITIAL" {
				_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET retry_at=clock_timestamp()+make_interval(secs=>$2) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job, delay.Seconds())
			}
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET state='FAILED',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
		if e != nil {
			return e
		}
		if c.kind == "INITIAL" {
			return discardFetch(ctx, tx, c.job, "ASANA_CRAWL_FAILED", "Asana 项目抓取失败或超过限制；未创建部分导入，请检查授权和来源后重试")
		}
		outward = cause
		return nil
	})
	if err != nil {
		return err
	}
	return outward
}
func loadAsanaRecords(ctx context.Context, tx pgx.Tx, c asanaCrawl) ([]Record, error) {
	rows, err := tx.Query(ctx, `SELECT source_data FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND cycle_id=$2::uuid ORDER BY external_id LIMIT 5001`, c.job, c.cycle)
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
		var task asana.Task
		if json.Unmarshal(raw, &task) != nil {
			return nil, errAsanaShape
		}
		r := asanaRecord(task)
		records = append(records, r)
		parents[r.ID] = r.ParentID
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(records) > 5000 {
		return nil, errAsanaLimit
	}
	for _, r := range records {
		seen := map[string]bool{}
		id := r.ID
		for depth := 0; id != ""; depth++ {
			if _, exists := parents[id]; !exists {
				break
			}
			if seen[id] || depth >= 10 {
				return nil, errAsanaShape
			}
			seen[id] = true
			id = parents[id]
		}
	}
	// A project task may have a parent outside the selected project. Keep it as a
	// top-level imported task; the missing parent is not fetched across scope.
	for i := range records {
		if records[i].ParentID != "" {
			if _, ok := parents[records[i].ParentID]; !ok {
				records[i].SourceWarnings = append(records[i].SourceWarnings, "asana_parent_outside_project")
				records[i].ParentID = ""
			}
		}
	}
	return records, nil
}
func (w *Worker) finishAsanaInitial(ctx context.Context, org string, c asanaCrawl, syncLease string) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := integrations.LockAsanaAccess(ctx, tx, c.connection, c.version, c.generation); e != nil {
			return e
		}
		if e := w.asanaJobFence(ctx, tx, c, syncLease); e != nil {
			return e
		}
		if e := checkProjectCrawlActor(ctx, tx, c.job); e != nil {
			return e
		}
		var cycle string
		if e := tx.QueryRow(ctx, `SELECT cycle_id::text FROM openfox.import_asana_crawls WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid FOR UPDATE`, c.job, c.lease).Scan(&cycle); e != nil {
			return e
		}
		if cycle != c.cycle {
			return errAsanaShape
		}
		records, err := loadAsanaRecords(ctx, tx, c)
		if err != nil {
			return err
		}
		if c.kind == "SYNC" {
			_, err = tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET state='READY',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
			return err
		}
		for start := 0; start < len(records); start += insertChunk {
			end := min(start+insertChunk, len(records))
			ordinals := []int32{}
			ids := []string{}
			raws := []string{}
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
		var preview, cfgRaw []byte
		var space string
		err = tx.QueryRow(ctx, `SELECT preview,config,coalesce(space_id::text,'') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.job).Scan(&preview, &cfgRaw, &space)
		if err != nil {
			return err
		}
		var doc previewDoc
		var cfg Config
		if json.Unmarshal(preview, &doc) != nil || json.Unmarshal(cfgRaw, &cfg) != nil {
			return errAsanaShape
		}
		cfg.normalize()
		analysis, infos, err := computeAnalysis(ctx, tx, c.job, space, nil, cfg, true, SourceAsana)
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
		_, err = tx.Exec(ctx, `UPDATE openfox.import_asana_crawls SET state='DONE',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, c.job)
		return err
	})
}

// Keep a safe stable label for API responses; never derive source namespaces from
// a mutable project name or a reconnect's local UUID.
func asanaProjectLabel(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	runes := []rune(name)
	if len(runes) > 255 {
		name = string(runes[:255])
	}
	return name
}

func checkProjectCrawlActor(ctx context.Context, tx pgx.Tx, job string) error {
	var user string
	err := tx.QueryRow(ctx, `SELECT m.user_id::text FROM openfox.import_jobs j JOIN openfox.organization_members m ON m.organization_id=j.organization_id AND m.user_id=CASE WHEN j.status='SUCCEEDED' THEN j.started_by ELSE j.created_by END WHERE j.organization_id=openfox_private.current_organization_id() AND j.id=$1::uuid AND m.status='ACTIVE' AND m.role IN('OWNER','ADMIN') AND (j.space_id IS NULL OR EXISTS(SELECT 1 FROM openfox.spaces s WHERE s.organization_id=j.organization_id AND s.id=j.space_id AND s.deleted_at IS NULL AND s.retired_at IS NULL AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id AND sm.status='ACTIVE' AND sm.role IN('OWNER','EDITOR','MEMBER')) OR (s.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id))))) FOR SHARE OF m`, job).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	return err
}
