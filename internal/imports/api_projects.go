package imports

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

func resolveAPIProject(ctx context.Context, tx pgx.Tx, b *batchCtx, d rowData) (fileEpic, error) {
	var p fileEpic
	if b.job.Preview.Origin.Instance == "" || d.SourceID == "" || utf8.RuneCountInString(d.Title) > 80 {
		return p, ErrInvalidRequest
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "import-project:"+b.orgID+":"+b.job.SpaceID+":"+b.job.Preview.Origin.Instance); e != nil {
		return p, e
	}
	e := tx.QueryRow(ctx, `SELECT project_id::text FROM openfox.import_project_mappings WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND source_system=$2 AND source_instance=$3 AND external_id=$4 AND released_at IS NULL`, b.job.SpaceID, sourceSystem(b.job.Source), b.job.Preview.Origin.Instance, d.SourceID).Scan(&p.ID)
	if e == nil {
		return p, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return p, e
	}
	var enabled bool
	if e = tx.QueryRow(ctx, `SELECT projects_enabled FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.job.SpaceID).Scan(&enabled); e != nil {
		return p, e
	}
	if !enabled {
		return p, collaboration.ErrProjectsDisabled
	}
	d.Description, _ = TruncateRunes(d.Description, 10000)
	e = tx.QueryRow(ctx, `INSERT INTO openfox.projects(organization_id,name,description,created_by) VALUES(openfox_private.current_organization_id(),$1,$2,$3::uuid) RETURNING id::text`, d.Title, d.Description, b.actor.UserID).Scan(&p.ID)
	if e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO openfox.project_spaces(organization_id,project_id,space_id,is_primary) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,true)`, p.ID, b.job.SpaceID); e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO openfox.import_project_mappings(organization_id,space_id,source_system,source_instance,external_id,project_id,created_job_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5::uuid,$6::uuid)`, b.job.SpaceID, sourceSystem(b.job.Source), b.job.Preview.Origin.Instance, d.SourceID, p.ID, b.job.ID); e != nil {
		return p, e
	}
	if e = recordResource(ctx, tx, b.job.ID, "PROJECT", p.ID, &b.job.SpaceID, nil); e != nil {
		return p, e
	}
	p.Created = true
	return p, nil
}

func attachRecordProject(ctx context.Context, tx pgx.Tx, b *batchCtx, d rowData, task string) error {
	p, ok := b.resolved.Epics[d.ParentID]
	if !ok {
		return nil
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.tasks SET project_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$3::uuid`, task, p.ID, b.job.SpaceID)
	return e
}
func recordSyncProject(ctx context.Context, tx pgx.Tx, job string, d rowData, project string) error {
	d.Description, _ = TruncateRunes(d.Description, 10000)
	_, e := tx.Exec(ctx, `INSERT INTO openfox.import_sync_projects(organization_id,job_id,external_id,project_id,source_name,source_description) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,$5)
 ON CONFLICT(organization_id,job_id,external_id) DO UPDATE SET source_name=excluded.source_name,source_description=excluded.source_description WHERE import_sync_projects.project_id=excluded.project_id`, job, d.SourceID, project, d.Title, d.Description)
	return e
}

func isAPIProject(j *syncJob, d rowData) bool {
	return j.plan.apiProjects && j.job.Cfg.Options.FileProjects != nil && strings.EqualFold(d.TypeName, "Epic")
}

// Resolve every Epic before importing children, independent of source ordering.
// Only projects created by this link are enrolled in bidirectional writes.
func (w *Worker) syncAPIProjects(ctx context.Context, j *syncJob, items []pendingItem) error {
	if !j.plan.apiProjects || j.job.Cfg.Options.FileProjects == nil || !*j.job.Cfg.Options.FileProjects {
		return nil
	}
	if j.resolved.Epics == nil {
		j.resolved.Epics = map[string]fileEpic{}
	}
	type snapshot struct {
		project         fileEpic
		baseline, local textPair
		version         uint64
		enrolled, alive bool
	}
	known := map[string]snapshot{}
	err := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT m.external_id,m.project_id::text,COALESCE(i.source_name,''),COALESCE(i.source_description,''),p.name,p.description,p.version,i.job_id IS NOT NULL,p.status<>'canceled' AND EXISTS(SELECT 1 FROM openfox.project_spaces ps WHERE ps.organization_id=p.organization_id AND ps.project_id=p.id AND ps.space_id=m.space_id)
		FROM openfox.import_project_mappings m JOIN openfox.projects p ON p.organization_id=m.organization_id AND p.id=m.project_id
		LEFT JOIN openfox.import_sync_projects i ON i.organization_id=m.organization_id AND i.job_id=$1::uuid AND i.external_id=m.external_id AND i.project_id=m.project_id
		WHERE m.organization_id=openfox_private.current_organization_id() AND m.space_id=$2::uuid AND m.source_system=$3 AND m.source_instance=$4 AND m.released_at IS NULL LIMIT 5001`, j.job.ID, j.job.SpaceID, sourceSystem(j.job.Source), j.job.Preview.Origin.Instance)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var v snapshot
			if e = rows.Scan(&id, &v.project.ID, &v.baseline.title, &v.baseline.description, &v.local.title, &v.local.description, &v.version, &v.enrolled, &v.alive); e != nil {
				return e
			}
			known[id] = v
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	if len(known) > 5000 {
		return ErrInvalidRequest
	}
	count := 0
	for _, item := range items {
		d := item.data(j.plan)
		if !isAPIProject(j, d) {
			continue
		}
		if utf8.RuneCountInString(d.Title) > 80 {
			return ErrInvalidRequest
		}
		d.Description, _ = TruncateRunes(d.Description, 10000)
		v, found := known[d.SourceID]
		p, baseline, local, version, enrolled, alive := v.project, v.baseline, v.local, v.version, v.enrolled, v.alive
		var e error
		if !found {
			if len(known) >= 5000 {
				return ErrInvalidRequest
			}
			if count >= 50 {
				return errSyncBacklog
			}
			count++
			e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
				if e := w.checkProjectSync(ctx, tx, j); e != nil {
					return e
				}
				var e error
				p, e = resolveAPIProject(ctx, tx, &j.batchCtx, d)
				if e != nil {
					return e
				}
				if p.Created {
					return recordSyncProject(ctx, tx, j.job.ID, d, p.ID)
				}
				return nil
			})
			if e != nil {
				return e
			}
			known[d.SourceID] = snapshot{project: p}
		}
		j.resolved.Epics[d.SourceID] = p
		if d.SourceKey != "" {
			j.resolved.Epics[d.SourceKey] = p
		}
		if !enrolled || !alive {
			continue
		}
		inTitle, outTitle, conflictTitle := syncField(baseline.title, local.title, d.Title)
		inDesc, outDesc, conflictDesc := syncField(baseline.description, local.description, d.Description)
		if j.direction == "INBOUND" {
			inTitle, outTitle, conflictTitle = inboundField(baseline.title, d.Title), nil, false
			inDesc, outDesc, conflictDesc = inboundField(baseline.description, d.Description), nil, false
		}
		if conflictTitle || conflictDesc {
			return errSyncConflict
		}
		if inTitle == nil && inDesc == nil && outTitle == nil && outDesc == nil {
			continue
		}
		if count >= 50 {
			return errSyncBacklog
		}
		count++
		verify := func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			var v uint64
			e := tx.QueryRow(ctx, `SELECT version FROM openfox.projects p WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'canceled' AND EXISTS(SELECT 1 FROM openfox.project_spaces s WHERE s.organization_id=p.organization_id AND s.project_id=p.id AND s.space_id=$2::uuid) FOR UPDATE`, p.ID, j.job.SpaceID).Scan(&v)
			if e != nil {
				return e
			}
			if v != version {
				return ErrVersionConflict
			}
			return nil
		}
		if outTitle != nil || outDesc != nil {
			if e = w.inOrganization(ctx, j.orgID, verify); e != nil {
				return e
			}
			if e = j.writeText(ctx, d, outTitle, outDesc); e != nil {
				return e
			}
			if outTitle != nil {
				d.Title = *outTitle
			}
			if outDesc != nil {
				d.Description = *outDesc
			}
		}
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := verify(ctx, tx); e != nil {
				return e
			}
			if inTitle != nil || inDesc != nil {
				_, e := j.collab.UpdateProject(ctx, tx, j.orgID, j.actor, p.ID, collaboration.ProjectPatch{Version: version, Name: inTitle, Description: inDesc})
				if e != nil {
					return e
				}
			}
			return recordSyncProject(ctx, tx, j.job.ID, d, p.ID)
		})
		if e != nil {
			return e
		}
	}
	return nil
}
