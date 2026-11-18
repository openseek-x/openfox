package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

type gongfengScan struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Token      string    `json:"token"`
	Page       int       `json:"page"`
	State      string    `json:"state"`
	Pass       int       `json:"pass"`
	Count      int       `json:"count"`
	FirstCount int       `json:"first_count"`
	Status     string    `json:"status"`
	History    bool      `json:"history"`
	Error      string    `json:"error,omitempty"`
}
type gongfengScanJob struct {
	gongfengHookJob
	Mode      string
	ScanState gongfengScan
	Created   time.Time
	Cursor    map[string]json.RawMessage
}

// The repository's parent lock serializes the cap, round stamps and bulk
// enqueue. No per-issue query loop or unbounded in-memory ID set is needed.
func queueGongfengScanRows(ctx context.Context, tx pgx.Tx, j *gongfengScanJob, kind string, keys []string, payload any, stamp bool) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > 5000 {
		return gongfeng.ErrIncomplete
	}
	var active, matching, duplicates int
	e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN ('PENDING','RUNNING')),count(*) FILTER(WHERE scan_token=NULLIF($4,'')::uuid AND scan_round=1),count(*) FILTER(WHERE scan_token=NULLIF($4,'')::uuid AND scan_round=$5)
 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=ANY($3::text[])`, j.Repo.ID, kind, keys, j.ScanState.Token, j.ScanState.Pass).Scan(&active, &matching, &duplicates)
	if e != nil {
		return e
	}
	if stamp && (duplicates > 0 || j.ScanState.Pass == 2 && matching != len(keys)) {
		return gongfeng.ErrIncomplete
	}
	var pending int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status IN ('PENDING','RUNNING') LIMIT 10000) q`, j.Access.ID).Scan(&pending); e != nil {
		return e
	}
	if pending+len(keys)-active > 10000 {
		return ErrGongfengQueueFull
	}
	var token any
	var round any
	if stamp {
		token = j.ScanState.Token
		round = j.ScanState.Pass
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_gongfeng_work(organization_id,connection_id,repository_id,kind,object_key,payload,scan_token,scan_round)
 SELECT openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,k,$5::jsonb,$6::uuid,$7::smallint FROM unnest($4::text[]) AS k
 ON CONFLICT(organization_id,repository_id,kind,object_key) DO UPDATE SET
 payload=openfox.integration_gongfeng_work.payload||EXCLUDED.payload,requested_generation=openfox.integration_gongfeng_work.requested_generation+1,
 status=CASE WHEN openfox.integration_gongfeng_work.status='RUNNING' THEN 'RUNNING' ELSE 'PENDING' END,next_attempt_at=clock_timestamp(),
 scan_token=COALESCE(EXCLUDED.scan_token,openfox.integration_gongfeng_work.scan_token),scan_round=COALESCE(EXCLUDED.scan_round,openfox.integration_gongfeng_work.scan_round),updated_at=clock_timestamp()
 WHERE $8 OR openfox.integration_gongfeng_work.status NOT IN ('PENDING','RUNNING')`, j.Access.ID, j.Repo.ID, kind, keys, mustJSON(payload), token, round, stamp)
	return e
}

func (w *SyncWorker) runGongfengScans(ctx context.Context, org string) error {
	type row struct{ Connection, ID string }
	var due []row
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT r.connection_id::text,r.id::text FROM openfox.integration_gongfeng_repositories r JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id WHERE r.organization_id=openfox_private.current_organization_id() AND r.status='ACTIVE' AND c.status='ACTIVE' AND r.next_reconcile_at<=clock_timestamp() AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=clock_timestamp()) ORDER BY r.next_reconcile_at,r.id LIMIT 2`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			if e = rows.Scan(&r.Connection, &r.ID); e != nil {
				return e
			}
			due = append(due, r)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, v := range due {
		j := gongfengScanJob{gongfengHookJob: gongfengHookJob{Org: org}}
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			j.Access, e = w.Service.loadGongfeng(ctx, tx, org, v.Connection, true)
			if e != nil {
				return e
			}
			j.Repo, e = loadGongfengRepository(ctx, tx, v.Connection, v.ID, true)
			if e != nil {
				return e
			}
			if j.Access.Status != StatusActive || j.Repo.Status != StatusActive || j.Repo.Generation != j.Repo.HookGeneration || j.Repo.HookCredentialGeneration != j.Access.CredentialGeneration {
				return ErrVersionConflict
			}
			var raw []byte
			var watermark *time.Time
			var available bool
			e = tx.QueryRow(ctx, `SELECT history_cursor,created_at,issue_watermark,lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp() FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, v.ID).Scan(&raw, &j.Created, &watermark, &available)
			if e != nil {
				return e
			}
			if !available {
				return ErrVersionConflict
			}
			if _, _, e = gongfengLinkAuthority(ctx, tx, org, j.Access, j.Repo); e != nil {
				return e
			}
			if json.Unmarshal(raw, &j.Cursor) != nil {
				return ErrInvalidRequest
			}
			if j.Cursor == nil {
				j.Cursor = map[string]json.RawMessage{}
			}
			for _, mode := range []string{"issues", "merge_requests"} {
				var c gongfengScan
				if data := j.Cursor[mode]; data != nil && json.Unmarshal(data, &c) != nil {
					return ErrInvalidRequest
				}
				if c.Status == "running" {
					j.Mode, j.ScanState = mode, c
					break
				}
			}
			if j.Mode == "" {
				j.Mode = "issues"
				j.ScanState = gongfengScan{From: j.Created.Truncate(time.Second), To: time.Now().UTC().Truncate(time.Second), Status: "running"}
				if watermark != nil {
					j.ScanState.From = watermark.Add(-5 * time.Minute)
					if j.ScanState.From.Before(j.Created) {
						j.ScanState.From = j.Created.Truncate(time.Second)
					}
				} else if j.Repo.HistoryRequested {
					j.ScanState.From = time.Time{}
					j.ScanState.History = true
				}
			}
			if j.ScanState.Token == "" {
				j.ScanState.Token, e = membership.NewID()
				if e != nil {
					return e
				}
				j.ScanState.Page = 1
				j.ScanState.Pass = 1
				j.ScanState.State = "opened"
				j.ScanState.Count = 0
			}
			j.Lease, e = membership.NewID()
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET lease_token=$2::uuid,lease_expires_at=clock_timestamp()+interval '90 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, v.ID, j.Lease)
			return e
		})
		if errors.Is(e, ErrVersionConflict) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) {
			continue
		}
		if e != nil {
			return e
		}
		jobctx, cancel := context.WithTimeout(ctx, 75*time.Second)
		jobctx = gongfeng.WithRequestBudget(jobctx, 5)
		e = w.processGongfengScan(jobctx, &j)
		cancel()
		if e != nil && !errors.Is(e, errGongfengLeaseLost) {
			w.logger().Warn("gongfeng_scan_failed", "organization_id", org, "repository_id", v.ID, "code", gongfengErrorCode(e))
		}
	}
	return nil
}

func (w *SyncWorker) processGongfengScan(ctx context.Context, j *gongfengScanJob) error {
	probe := GongfengWork{Access: j.Access, Repository: j.Repo}
	if e := w.checkGongfengRemote(ctx, &probe); e != nil {
		return w.saveGongfengScan(ctx, j, nil, e)
	}
	j.Repo = probe.Repository
	// At most the remaining two pages after fresh permission verification; this
	// is below the approved five-page ceiling and counts every HTTP request.
	for gongfeng.RemainingRequests(ctx) > 0 && j.ScanState.Status == "running" {
		var ids []int64
		var next int
		if j.Mode == "issues" {
			p, e := j.Access.Client.Issues(ctx, j.Access.credentials.AccessToken, j.Repo.ProjectID, gongfeng.IssueQuery{State: j.ScanState.State, UpdatedAfter: j.ScanState.From, UpdatedBefore: j.ScanState.To}, j.ScanState.Page)
			if e != nil {
				return w.saveGongfengScan(ctx, j, nil, e)
			}
			next = p.Next
			for _, i := range p.Items {
				if i.UpdatedAt.After(j.ScanState.To) || !j.ScanState.From.IsZero() && i.UpdatedAt.Before(j.ScanState.From) {
					return w.saveGongfengScan(ctx, j, nil, gongfeng.ErrIncomplete)
				}
				ids = append(ids, i.ID)
			}
		} else {
			p, e := j.Access.Client.MergeRequests(ctx, j.Access.credentials.AccessToken, j.Repo.ProjectID, j.ScanState.From, j.ScanState.To, j.ScanState.Page)
			if e != nil {
				return w.saveGongfengScan(ctx, j, nil, e)
			}
			next = p.Next
			for _, m := range p.Items {
				if m.UpdatedAt.After(j.ScanState.To) || m.UpdatedAt.Before(j.ScanState.From) {
					return w.saveGongfengScan(ctx, j, nil, gongfeng.ErrIncomplete)
				}
				ids = append(ids, m.ID)
			}
		}
		if j.ScanState.Count+len(ids) > 5000 {
			return w.saveGongfengScan(ctx, j, nil, gongfeng.ErrIncomplete)
		}
		e := w.saveGongfengScan(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			kind := "ISSUE_REFRESH"
			if j.Mode == "merge_requests" {
				kind = "MR_REFRESH"
			}
			keys := make([]string, len(ids))
			for i, id := range ids {
				keys[i] = strconv.FormatInt(id, 10)
			}
			if e := queueGongfengScanRows(ctx, tx, j, kind, keys, map[string]any{"history": j.ScanState.History}, true); e != nil {
				return e
			}

			j.ScanState.Count += len(ids)
			j.ScanState.Page = next
			if next == 0 {
				if j.Mode == "issues" && j.ScanState.State == "opened" {
					j.ScanState.State = "closed"
					j.ScanState.Page = 1
				} else if j.ScanState.Pass == 1 {
					j.ScanState.FirstCount = j.ScanState.Count
					j.ScanState.Count = 0
					j.ScanState.Pass = 2
					j.ScanState.State = "opened"
					j.ScanState.Page = 1
				} else {
					if j.ScanState.Count != j.ScanState.FirstCount {
						return gongfeng.ErrIncomplete
					}
					j.ScanState.Status = "complete"
				}
			}
			return nil
		}, nil)
		if e != nil {
			return w.saveGongfengScan(ctx, j, nil, e)
		}
		// A committed page releases the repository lease so cancellation or a
		// second worker cannot share it. Continue on the next ordinary pass.
		return nil
	}
	return w.saveGongfengScan(ctx, j, nil, gongfeng.ErrBudgetExhausted)
}

func (w *SyncWorker) saveGongfengScan(ctx context.Context, j *gongfengScanJob, apply func(context.Context, pgx.Tx) error, cause error) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e := w.withGongfengHook(finish, &j.gongfengHookJob, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := gongfengLinkAuthority(ctx, tx, j.Org, j.Access, j.Repo); e != nil {
			return e
		}
		if cause == nil && apply != nil {
			if e := apply(ctx, tx); e != nil {
				return e
			}
		}
		var next any = time.Now()
		failures := 0
		status := "ACTIVE"
		if cause != nil {
			j.ScanState.Error = gongfengErrorCode(cause)
			if e := tx.QueryRow(ctx, `SELECT LEAST(consecutive_failures+1,8) FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID).Scan(&failures); e != nil {
				return e
			}
			next = gongfengRetryAt(failures-1, cause)
			if failures >= 8 || errors.Is(cause, gongfeng.ErrIncomplete) || errors.Is(cause, gongfeng.ErrInvalid) || errors.Is(cause, gongfeng.ErrForbidden) || errors.Is(cause, ErrInvalidRepository) || errors.Is(cause, errGongfengRepositoryChanged) {
				j.ScanState.Status = "incomplete"
				next = nil
				status = "NEEDS_ATTENTION"
			}
		}
		if j.ScanState.Status == "complete" {
			next = time.Now().Add(5 * time.Minute)
		}
		j.Cursor[j.Mode] = mustJSON(j.ScanState)
		if j.ScanState.Status == "complete" && j.Mode == "issues" {
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET issue_watermark=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID, j.ScanState.To); e != nil {
				return e
			}
			// Reconcile known comments without importing pre-binding history.
			rows, e := tx.Query(ctx, `SELECT issue_ref FROM openfox.task_issue_links WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND unlinked_at IS NULL ORDER BY issue_number LIMIT 5001`, j.Repo.SyncLinkID)
			if e != nil {
				return e
			}
			var refs []string
			for rows.Next() {
				var ref string
				if e = rows.Scan(&ref); e != nil {
					rows.Close()
					return e
				}
				refs = append(refs, ref)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if len(refs) > 5000 {
				return gongfeng.ErrIncomplete
			}
			if e := queueGongfengScanRows(ctx, tx, j, "NOTES_SCAN", refs, map[string]any{"page": 1, "after": j.Created.Truncate(time.Second)}, false); e != nil {
				return e
			}
			if j.Repo.MREnabled {
				rows, e := tx.Query(ctx, `SELECT object_key FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='MR_REFRESH' ORDER BY object_key LIMIT 5001`, j.Repo.ID)
				if e != nil {
					return e
				}
				var refs []string
				for rows.Next() {
					var ref string
					if e = rows.Scan(&ref); e != nil {
						rows.Close()
						return e
					}
					refs = append(refs, ref)
				}
				e = rows.Err()
				rows.Close()
				if e != nil {
					return e
				}
				if len(refs) > 5000 {
					return gongfeng.ErrIncomplete
				}
				if e = queueGongfengScanRows(ctx, tx, j, "MR_REFRESH", refs, map[string]any{}, false); e != nil {
					return e
				}
			}

		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET history_cursor=$2::jsonb,current_path=$3,private=$4,next_reconcile_at=$5,consecutive_failures=$6,status=$7,status_reason=$8,lease_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID, mustJSON(j.Cursor), j.Repo.Path, j.Repo.Private, next, failures, status, gongfengErrorCode(cause))
		if e != nil {
			return e
		}
		if status == "NEEDS_ATTENTION" {
			if _, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='NEEDS_ATTENTION',status_reason=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.SyncLinkID, gongfengErrorCode(cause)); e != nil {
				return e
			}
		}
		if errors.Is(cause, errGongfengRepositoryChanged) {
			return markGongfengRepositoryChanged(ctx, tx, j.Repo)
		}
		if errors.Is(cause, gitapi.ErrTokenRevoked) {
			if e = w.Service.MarkGongfengTokenRevoked(ctx, tx, j.Access.ID, j.Access.Version); e != nil {
				return e
			}
		}
		return refreshGongfengDisplay(ctx, tx, j.Repo)
	})
	if e != nil {
		return e
	}
	return cause
}
