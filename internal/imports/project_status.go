package imports

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

var errSyncStatusMapping = errors.New("sync status mapping required")
var errSyncStatusTransition = errors.New("source workflow transition unavailable")

// Keep independent source and local baselines: two external states may map to
// the same local state, and an upgraded link must not replay historical edits.
func syncStatus(v syncedItem, remote string, mappings map[string]string, direction string) (in, out *string, err error) {
	mapped := mappings[strings.ToLower(remote)]
	if remote != "" && mapped == "" {
		return nil, nil, errSyncStatusMapping
	}
	if v.status == nil || v.baselineStatus == nil || v.baselineSourceStatus == nil || v.baselineMappedStatus == nil {
		return
	}
	incoming := remote != *v.baselineSourceStatus || mapped != *v.baselineMappedStatus
	outgoing := *v.status != *v.baselineStatus
	if mapped == *v.status {
		return
	}
	if direction == "INBOUND" {
		if incoming && mapped != "" {
			in = &mapped
		}
		return
	}
	if incoming && outgoing {
		return nil, nil, errSyncConflict
	}
	if incoming && mapped != "" {
		return &mapped, nil, nil
	}
	if outgoing {
		target := ""
		for source, id := range mappings {
			if id == *v.status {
				if target != "" {
					return nil, nil, errSyncStatusMapping
				}
				target = source
			}
		}
		if target == "" {
			return nil, nil, errSyncStatusMapping
		}
		return nil, &target, nil
	}
	return
}

func (w *Worker) discoverSyncStatuses(ctx context.Context, j *syncJob, items []pendingItem) error {
	if j.mappings == nil {
		j.mappings = map[string]string{}
	}
	for name, id := range j.resolved.Statuses {
		if _, ok := j.mappings[name]; !ok {
			j.mappings[name] = id
		}
	}
	for _, item := range items {
		if isAPIProject(j, item.data(j.plan)) {
			continue
		}
		name := strings.ToLower(item.data(j.plan).Status)
		if name != "" {
			if _, ok := j.mappings[name]; !ok {
				j.mappings[name] = ""
			}
		}
	}
	if len(j.mappings) > 500 {
		return ErrInvalidRequest
	}
	raw, e := json.Marshal(j.mappings)
	if e != nil {
		return e
	}
	e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET status_mappings=$3::jsonb WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, j.job.ID, j.lease, raw)
		return e
	})
	j.resolved.Statuses = j.mappings
	return e
}

func (s Service) SetSyncStatuses(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version int64, mappings map[string]string) (Job, error) {
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return Job{}, e
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || version < 1 || len(mappings) == 0 || len(mappings) > 500 {
		return Job{}, ErrInvalidRequest
	}
	var raw []byte
	var space string
	e := tx.QueryRow(ctx, `SELECT l.status_mappings,l.space_id::text FROM openfox.import_sync_links l WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.version=$2 AND l.state<>'DISCONNECTED' FOR UPDATE`, id, version).Scan(&raw, &space)
	if errors.Is(e, pgx.ErrNoRows) {
		return Job{}, ErrVersionConflict
	}
	if e != nil {
		return Job{}, e
	}
	old := map[string]string{}
	if e = json.Unmarshal(raw, &old); e != nil {
		return Job{}, e
	}
	// Mapping edits require the same space write permission as the sync worker.
	var writable bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.spaces s WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=$1::uuid AND s.deleted_at IS NULL AND s.retired_at IS NULL AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=$2::uuid AND sm.status='ACTIVE' AND sm.role IN('OWNER','EDITOR','MEMBER')) OR (s.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=$2::uuid))))`, space, actor.UserID).Scan(&writable)
	if e != nil {
		return Job{}, e
	}
	if !writable {
		return Job{}, ErrForbidden
	}
	ids := []string{}
	for name, value := range mappings {
		if name == "" || len(name) > 500 || name != strings.ToLower(strings.TrimSpace(name)) {
			return Job{}, ErrInvalidRequest
		}
		if value != "" {
			if !membership.ValidateUUID(value) {
				return Job{}, ErrInvalidRequest
			}
			ids = append(ids, value)
		}
		old[name] = value
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_statuses WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND archived_at IS NULL`, ids).Scan(&count); e != nil {
		return Job{}, e
	}
	if count != len(ids) {
		return Job{}, ErrInvalidRequest
	}
	if len(old) > 500 {
		return Job{}, ErrInvalidRequest
	}
	b := batchCtx{orgID: org, actor: actor}
	b.job.ID = id
	b.job.SpaceID = space
	if e = (&Worker{}).ensureWorkflow(ctx, tx, &b, ids); e != nil {
		return Job{}, e
	}
	raw, e = json.Marshal(old)
	if e != nil {
		return Job{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET status_mappings=$3::jsonb,version=version+1,lease=NULL,error_code='',next_check=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND version=$2`, id, version, raw)
	if e != nil {
		return Job{}, e
	}
	return s.Get(ctx, tx, org, actor, id)
}
