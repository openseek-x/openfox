package imports

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/integrations"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
)

type ProjectSync struct {
	StatusMappings map[string]string `json:"status_mappings"`
	LastChecked    *time.Time        `json:"last_checked,omitempty"`
	Checking       bool              `json:"checking"`
	Direction      string            `json:"direction"`
	State          string            `json:"state"`
	Version        int64             `json:"version"`
	LastSuccess    *time.Time        `json:"last_success,omitempty"`
	Error          string            `json:"error_code,omitempty"`
}
type syncJob struct {
	mappings map[string]string
	jira     *jiraapi.Client
	zen      *zentao.Client
	batchCtx
	connection, project, lease, direction string
	src                                   integrations.ProjectSource
}
type textPair struct{ title, description string }
type syncedItem struct {
	status, baselineStatus, baselineSourceStatus, baselineMappedStatus *string
	task                                                               string
	version                                                            uint64
	baseline, local                                                    textPair
}

func recordSyncItem(ctx context.Context, tx pgx.Tx, job, external, task, title, desc, sourceStatus, mappedStatus string, localBaseline *string) error {
	title, _ = TruncateRunes(title, maxTitleRunes)
	desc, _ = TruncateRunes(desc, maxDescriptionRunes)
	_, err := tx.Exec(ctx, `INSERT INTO openfox.import_sync_items(organization_id,job_id,external_id,task_id,source_title,source_description,source_status,source_status_id,local_status_id)
 SELECT openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,$5,$6,$7,COALESCE($8,t.status_id::text) FROM openfox.tasks t WHERE t.organization_id=openfox_private.current_organization_id() AND t.id=$3::uuid
 ON CONFLICT(organization_id,job_id,external_id) DO UPDATE SET source_title=excluded.source_title,source_description=excluded.source_description,source_status=excluded.source_status,source_status_id=excluded.source_status_id,local_status_id=excluded.local_status_id
 WHERE import_sync_items.task_id=excluded.task_id`, job, external, task, title, desc, sourceStatus, mappedStatus, localBaseline)
	return err
}

func (w *Worker) syncProjectImports(ctx context.Context, org string) error {
	var j syncJob
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var cfg, preview, mappings []byte
		err := tx.QueryRow(ctx, `WITH picked AS (
 SELECT l.job_id FROM openfox.import_sync_links l JOIN openfox.import_jobs j USING(organization_id)
 WHERE l.organization_id=openfox_private.current_organization_id() AND j.id=l.job_id AND j.status='SUCCEEDED'
 AND l.state='ACTIVE' AND l.next_check<=clock_timestamp() ORDER BY l.next_check,l.job_id FOR UPDATE OF l SKIP LOCKED LIMIT 1)
 UPDATE openfox.import_sync_links l SET lease=gen_random_uuid(),next_check=clock_timestamp()+interval '2 minutes'
 FROM picked,openfox.import_jobs j
 WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=picked.job_id AND j.id=l.job_id AND j.organization_id=l.organization_id
 RETURNING j.id::text,j.space_id::text,j.started_by::text,j.source,j.config,j.preview,l.connection_id::text,l.project_id,l.lease::text,l.direction,l.status_mappings`).Scan(&j.job.ID, &j.job.SpaceID, &j.job.StartedBy, &j.job.Source, &cfg, &preview, &j.connection, &j.project, &j.lease, &j.direction, &mappings)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(mappings, &j.mappings); err != nil {
			return err
		}
		if err = json.Unmarshal(cfg, &j.job.Cfg); err != nil {
			return err
		}
		return json.Unmarshal(preview, &j.job.Preview)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	j.orgID = org
	j.warns = newWarnAcc(nil)
	j.plan = buildPlan(j.job.Source, headersOf(j.job.Preview), j.job.Cfg.Columns)
	if j.job.Preview.Resolved != nil {
		j.resolved = *j.job.Preview.Resolved
	}
	bounded, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	code := ""
	delay := 300
	err = w.runProjectSync(bounded, &j)
	if err == nil || errors.Is(err, errSyncConflict) || errors.Is(err, errSyncBacklog) {
		commentErr := w.syncProjectComments(bounded, &j)
		if commentErr != nil && (err == nil || errors.Is(err, errSyncBacklog)) {
			err = commentErr
		}
	}
	if err == nil || errors.Is(err, errSyncBacklog) {
		if deliveryErr := w.sendProjectComments(bounded, &j); deliveryErr != nil {
			err = deliveryErr
		}
	}
	if errors.Is(err, errCommentIdentity) {
		code = "COMMENT_IDENTITY"
	} else if errors.Is(err, errCommentDeliveryUnknown) {
		code = "COMMENT_DELIVERY_UNKNOWN"
	} else if errors.Is(err, errCommentFallback) {
		code = "COMMENT_FALLBACK"
	} else if errors.Is(err, errSyncStatusMapping) {
		code = "STATUS_MAPPING"
	} else if errors.Is(err, errSyncStatusTransition) {
		code = "STATUS_TRANSITION"
	} else if errors.Is(err, errSyncComments) {
		code = "COMMENTS"
	} else if errors.Is(err, errSyncConflict) {
		code = "CONFLICT"
	} else if errors.Is(err, errSyncBacklog) {
		delay = 2
	} else if err != nil {
		code = "SYNC_FAILED"
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET lease=NULL,next_check=clock_timestamp()+make_interval(secs=>$4),
 last_checked=clock_timestamp(),last_success=CASE WHEN $3='' THEN clock_timestamp() ELSE last_success END,error_code=$3
 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, j.job.ID, j.lease, code, delay)
		return e
	})
}

var errSyncSkipped = errors.New("item already imported outside this link")
var errSyncConflict = errors.New("both sides edited the same field")
var errSyncBacklog = errors.New("additional items remain for the next pass")

func (w *Worker) checkProjectSync(ctx context.Context, tx pgx.Tx, j *syncJob) error {
	var role string
	err := tx.QueryRow(ctx, `SELECT m.role,m.authorization_version FROM openfox.import_sync_links l
 JOIN openfox.organization_members m ON m.organization_id=l.organization_id AND m.user_id=$3::uuid
 JOIN openfox.spaces s ON s.organization_id=l.organization_id AND s.id=l.space_id
 JOIN openfox.integration_connections c ON c.organization_id=l.organization_id AND c.id=l.connection_id
 WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.lease=$2::uuid AND l.state='ACTIVE'
 AND m.status='ACTIVE' AND m.role IN('OWNER','ADMIN') AND c.status='ACTIVE' AND c.scope='ORGANIZATION' AND c.provider=$4
 AND s.deleted_at IS NULL AND s.retired_at IS NULL AND
 (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id AND sm.status='ACTIVE' AND sm.role IN ('OWNER','EDITOR','MEMBER'))
 OR (s.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id)))
 FOR SHARE OF l,c,m,s`, j.job.ID, j.lease, j.job.StartedBy, sourceSystem(j.job.Source)).Scan(&role, &j.actor.AuthorizationVersion)
	j.actor.UserID = j.job.StartedBy
	j.actor.Role = policy.OrganizationRole(strings.ToLower(role))
	return err
}

// Read the bounded project once, and fetch the local baselines in one query.
// Network writes happen outside database transactions. A retry reads both sides
// again, so a successful write whose response was lost is acknowledged once.
func (w *Worker) runProjectSync(ctx context.Context, j *syncJob) error {
	items := map[string]syncedItem{}
	ignored := map[string]bool{}
	err := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		var e error
		service := integrations.Service{Box: w.Secrets}
		provider := sourceSystem(j.job.Source)
		if provider == "jira" || provider == "zentao" {
			grant, err := service.LoadImportSite(ctx, tx, j.orgID, provider, j.connection, j.actor)
			if err != nil {
				return err
			}
			if provider == "jira" && grant.Jira != nil && j.job.Preview.API != nil && j.job.Preview.API.SiteURL == grant.Site {
				httpClient := safehttp.Client(w.Safe)
				// A redirect to a login page is not acknowledgment of a Jira write.
				httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				j.jira, e = jiraapi.New(jiraapi.Config{Deployment: j.job.Preview.API.Deployment, SiteURL: grant.Site, Auth: *grant.Jira}, httpClient)
			} else if provider == "zentao" && grant.Zen != nil && grant.Site == grant.Zen.Site && ImportSiteOrigin(grant.Site, j.project).Instance == j.job.Preview.Origin.Instance {
				j.zen, e = grant.Zen.Client(w.Safe)
			} else {
				return ErrInvalidRequest
			}
		} else {
			j.src, e = service.ProjectSource(ctx, tx, j.orgID, j.actor, provider, j.connection)
		}
		if e != nil {
			return e
		}
		j.settings, e = loadSettings(ctx, tx, j.job.SpaceID)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT i.external_id,i.task_id::text,i.source_title,i.source_description,t.title,t.description,t.version,t.status_id::text,i.source_status,i.source_status_id,i.local_status_id,t.deleted_at IS NOT NULL OR t.space_id<>$2::uuid
 FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.organization_id=i.organization_id AND t.id=i.task_id
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.job_id=$1::uuid LIMIT 5001`, j.job.ID, j.job.SpaceID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			var v syncedItem
			var deleted bool
			if e = rows.Scan(&id, &v.task, &v.baseline.title, &v.baseline.description, &v.local.title, &v.local.description, &v.version, &v.status, &v.baselineSourceStatus, &v.baselineMappedStatus, &v.baselineStatus, &deleted); e != nil {
				rows.Close()
				return e
			}
			items[id] = v
			ignored[id] = deleted
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		if len(items) > 5000 {
			return ErrInvalidRequest
		}
		rows, e = tx.Query(ctx, `SELECT external_id FROM openfox.import_items WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND status IN('FAILED','SKIPPED')`, j.job.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ignored[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	remoteItems, err := j.remoteItems(ctx)
	if err != nil {
		return err
	}
	if err := w.syncAPIProjects(ctx, j, remoteItems); err != nil {
		return err
	}
	if err := w.discoverSyncStatuses(ctx, j, remoteItems); err != nil {
		return err
	}
	changes := 0
	conflicts := false
	for _, item := range remoteItems {
		d := item.data(j.plan)
		if isAPIProject(j, d) {
			continue
		}
		item.ExternalID = d.SourceID
		if ignored[d.SourceID] {
			continue
		}
		v, exists := items[d.SourceID]
		if !exists && ((rowDone(d) && !j.job.Cfg.Options.ImportDone) || (!rowDone(d) && !j.job.Cfg.Options.ImportOpen)) {
			continue
		}
		d.Title, _ = TruncateRunes(d.Title, maxTitleRunes)
		d.Description, _ = TruncateRunes(d.Description, maxDescriptionRunes)
		remote := textPair{d.Title, d.Description}
		if exists && remote == v.baseline && (remote == v.local || j.direction == "INBOUND") && v.baselineStatus != nil && v.baselineSourceStatus != nil && *v.baselineSourceStatus == d.Status && *v.status == *v.baselineStatus && v.baselineMappedStatus != nil && *v.baselineMappedStatus == j.mappings[strings.ToLower(d.Status)] {
			continue
		}
		if changes == 50 {
			return errSyncBacklog
		}
		if !exists && len(items) >= 5000 {
			return ErrInvalidRequest
		}
		err = w.applySyncItem(ctx, j, item, d, v, exists)
		if errors.Is(err, errSyncSkipped) {
			continue
		}
		if errors.Is(err, errSyncConflict) {
			conflicts = true
			continue
		}
		if err != nil {
			return err
		}
		changes++
		if !exists {
			items[d.SourceID] = syncedItem{}
		}
	}
	if conflicts {
		return errSyncConflict
	}
	return nil
}

func syncField(base, local, remote string) (in, out *string, conflict bool) {
	switch {
	case local == remote:
		return nil, nil, false
	case local == base:
		return &remote, nil, false
	case remote == base:
		return nil, &local, false
	default:
		return nil, nil, true
	}
}
func inboundField(base, remote string) *string {
	if base == remote {
		return nil
	}
	return &remote
}
func (w *Worker) applySyncItem(ctx context.Context, j *syncJob, item pendingItem, d rowData, v syncedItem, exists bool) error {
	inStatus, outStatus, statusErr := syncStatus(v, d.Status, j.mappings, j.direction)
	if exists && statusErr != nil {
		return statusErr
	}
	inTitle, outTitle, titleConflict := syncField(v.baseline.title, v.local.title, d.Title)
	inDesc, outDesc, descConflict := syncField(v.baseline.description, v.local.description, d.Description)
	if j.direction == "INBOUND" {
		// Only an actual change at the source may replace a local edit.
		inTitle, outTitle, titleConflict = inboundField(v.baseline.title, d.Title), nil, false
		inDesc, outDesc, descConflict = inboundField(v.baseline.description, d.Description), nil, false
	}
	if exists && (titleConflict || descConflict) {
		return errSyncConflict
	}
	verify := func(ctx context.Context, tx pgx.Tx) error {
		if err := w.checkProjectSync(ctx, tx, j); err != nil {
			return err
		}
		var version uint64
		if exists {
			err := tx.QueryRow(ctx, `SELECT version FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND space_id=$2::uuid AND id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, v.task, j.job.SpaceID).Scan(&version)
			if err != nil {
				return err
			}
			if version != v.version {
				return ErrVersionConflict
			}
		}
		return nil
	}
	if exists && (outTitle != nil || outDesc != nil || outStatus != nil) {
		if err := w.inOrganization(ctx, j.orgID, verify); err != nil {
			return err
		}
		if outTitle != nil || outDesc != nil {
			if err := j.writeText(ctx, d, outTitle, outDesc); err != nil {
				return err
			}
		}
		if outStatus != nil {
			if err := j.writeStatus(ctx, d, *outStatus); err != nil {
				return errors.Join(errSyncStatusTransition, err)
			}
			d.Status = *outStatus
		}
		if outTitle != nil {
			d.Title = *outTitle
		}
		if outDesc != nil {
			d.Description = *outDesc
		}
	}
	return w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := verify(ctx, tx); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('openfox.import_job',$1,true)`, j.job.ID); e != nil {
			return e
		}
		if !exists {
			// A separately imported or historical mapping is not an authorization to
			// start writing back its local contents to this provider.
			batch := j.batchCtx
			batch.job.Cfg.Options.Overwrite = false
			result, e := w.processItem(ctx, tx, &batch, item)
			if e != nil {
				return e
			}
			if result.status != "CREATED" {
				return errSyncSkipped
			}
			v.task = result.taskID
			if item.Record != nil {
				if err := attachRecordProject(ctx, tx, &j.batchCtx, d, v.task); err != nil {
					return err
				}
			}
			if item.Record == nil {
				if err := writeFileColumns(ctx, tx, &batch, item.Cells, v.task); err != nil {
					return err
				}
			}
			tag, e := tx.Exec(ctx, `UPDATE openfox.import_mappings SET sync_created=true
                WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1::uuid AND created_job_id=$2::uuid AND released_at IS NULL`, v.task, j.job.ID)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrVersionConflict
			}
		} else if inTitle != nil || inDesc != nil || inStatus != nil {
			rid, e := membership.NewID()
			if e != nil {
				return e
			}
			_, e = j.collab.UpdateTask(ctx, tx, j.orgID, j.job.SpaceID, v.task, j.actor, collaboration.TaskUpdateInput{Version: v.version, StatusID: inStatus, Title: inTitle, Description: inDesc, RequestID: rid})
			if e != nil {
				return e
			}
		}
		var localBaseline *string
		if j.direction == "INBOUND" && inStatus == nil {
			localBaseline = v.baselineStatus
		}
		return recordSyncItem(ctx, tx, j.job.ID, d.SourceID, v.task, d.Title, d.Description, d.Status, j.mappings[strings.ToLower(d.Status)], localBaseline)
	})
}

func (s Service) SetProjectSync(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, state, direction string, checkNow bool, version int64) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || version < 1 {
		return Job{}, ErrInvalidRequest
	}
	var update string
	var value string
	switch {
	case state != "" && direction == "" && !checkNow && (state == "ACTIVE" || state == "PAUSED" || state == "DISCONNECTED"):
		update = `SET state=$3,version=version+1,lease=NULL,error_code='',next_check=clock_timestamp()`
		value = state
	case state == "" && !checkNow && (direction == "INBOUND" || direction == "BIDIRECTIONAL"):
		update = `SET direction=$3,version=version+1,lease=NULL,error_code='',next_check=clock_timestamp()`
		value = direction
	case state == "" && direction == "" && checkNow:
		// A live lease is already checking. Never steal it to service a button click.
		update = `SET next_check=CASE WHEN lease IS NULL THEN clock_timestamp() ELSE next_check END,version=version+1`
	default:
		return Job{}, ErrInvalidRequest
	}
	condition := ` WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND version=$2 AND state<>'DISCONNECTED'`
	args := []any{id, version}
	if checkNow {
		condition += " AND state='ACTIVE'"
	} else {
		args = append(args, value)
	}
	if state == "ACTIVE" {
		condition += ` AND EXISTS(SELECT 1 FROM openfox.integration_connections c WHERE c.id=l.connection_id AND c.organization_id=l.organization_id AND c.status='ACTIVE')`
	}
	tag, err := tx.Exec(ctx, "UPDATE openfox.import_sync_links l "+update+condition, args...)
	if err != nil {
		return Job{}, err
	}
	if tag.RowsAffected() != 1 {
		return Job{}, ErrVersionConflict
	}
	return s.Get(ctx, tx, org, actor, id)
}
