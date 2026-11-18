package imports

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/spacebundle"
)

// The caller's row savepoint covers the task, its source mapping and all metadata.
func writeFileColumns(ctx context.Context, tx pgx.Tx, b *batchCtx, cells []string, taskID string) error {
	project := cell(cells, b.plan.project)
	epic, found := b.resolved.Epics[cell(cells, b.plan.parent)]
	if mapped, ok := b.resolved.Epics[project]; ok {
		epic, found = mapped, true
	}
	if found {
		if _, err := tx.Exec(ctx, `UPDATE openfox.tasks SET project_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$3::uuid`, taskID, epic.ID, b.job.SpaceID); err != nil {
			return err
		}
		project = ""
	}
	if b.plan.jiraFile && b.job.Cfg.Options.FileProjects != nil && !*b.job.Cfg.Options.FileProjects {
		project = ""
	}
	if project == "" && len(b.plan.custom) == 0 {
		return nil
	}
	if project != "" {
		if err := attachFileProject(ctx, tx, b, taskID, project); err != nil {
			return err
		}
	}
	for _, col := range b.plan.custom {
		value := cell(cells, col.Index)
		if value == "" {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		normalized, _, err := collaboration.NormalizeFieldValue("TEXT", "", raw, nil)
		if err != nil {
			return errors.New("自定义字段「" + col.CustomName + "」的文本值超过 256 字")
		}
		field, err := resolveImportField(ctx, tx, b.job.ID, b.actor.UserID, spacebundle.Field{Name: col.CustomName, Kind: "TEXT"})
		if err != nil {
			return err
		}
		var typeID string
		err = tx.QueryRow(ctx, `INSERT INTO openfox.task_type_fields (organization_id,space_id,task_type_id,field_id,created_by)
    SELECT organization_id,space_id,type_id,$2::uuid,$3::uuid FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
    ON CONFLICT DO NOTHING RETURNING task_type_id::text`, taskID, field.ID, b.actor.UserID).Scan(&typeID)
		if err == nil {
			if err = recordResource(ctx, tx, b.job.ID, "CUSTOM_FIELD", field.ID, &b.job.SpaceID, &typeID); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.task_field_values (organization_id,space_id,task_id,field_id,value,updated_by)
     VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::jsonb,$5::uuid)
     ON CONFLICT (organization_id,task_id,field_id) DO UPDATE SET value=excluded.value, updated_by=excluded.updated_by, updated_at=clock_timestamp()`, b.job.SpaceID, taskID, field.ID, normalized, b.actor.UserID)
		if err != nil {
			return err
		}
	}
	return nil
}

func attachFileProject(ctx context.Context, tx pgx.Tx, b *batchCtx, taskID, name string) error {
	if utf8.RuneCountInString(name) > 80 {
		return errors.New("专项名称超过 80 字")
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT p.id::text FROM openfox.projects p JOIN openfox.project_spaces s ON s.organization_id=p.organization_id AND s.project_id=p.id
   WHERE p.organization_id=openfox_private.current_organization_id() AND s.space_id=$1::uuid AND lower(p.name)=lower($2) AND p.status <> 'canceled' ORDER BY p.created_at,p.id LIMIT 1`, b.job.SpaceID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO openfox.projects(organization_id,name,created_by) VALUES(openfox_private.current_organization_id(),$1,$2::uuid) RETURNING id::text`, name, b.actor.UserID).Scan(&id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.project_spaces(organization_id,project_id,space_id,is_primary) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,true)`, id, b.job.SpaceID); err != nil {
			return err
		}
		if err = recordResource(ctx, tx, b.job.ID, "PROJECT", id, &b.job.SpaceID, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.tasks SET project_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$3::uuid`, taskID, id, b.job.SpaceID)
	return err
}

// Field records with a type identify a binding, while records without a type own the definition.
func cleanupFileColumns(ctx context.Context, tx pgx.Tx, jobID string) error {
	for _, stmt := range []string{
		`DELETE FROM openfox.task_type_fields f USING openfox.import_created_resources r
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.job_id=$1::uuid AND r.kind='CUSTOM_FIELD'
 AND f.organization_id=r.organization_id AND f.task_type_id=r.task_type_id AND f.field_id=r.resource_id AND f.created_at<=r.created_at
 AND NOT EXISTS (SELECT 1 FROM openfox.task_field_values v JOIN openfox.tasks t ON t.organization_id=v.organization_id AND t.id=v.task_id WHERE v.organization_id=f.organization_id AND v.field_id=f.field_id AND t.type_id=f.task_type_id AND t.deleted_at IS NULL)`,
		`UPDATE openfox.custom_fields f SET archived_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1
 FROM openfox.import_created_resources r WHERE r.organization_id=openfox_private.current_organization_id() AND r.job_id=$1::uuid AND r.kind='CUSTOM_FIELD' AND r.task_type_id IS NULL AND f.organization_id=r.organization_id AND f.id=r.resource_id AND f.version=1
 AND NOT EXISTS (SELECT 1 FROM openfox.task_type_fields tf WHERE tf.organization_id=f.organization_id AND tf.field_id=f.id)
 AND NOT EXISTS (SELECT 1 FROM openfox.task_field_values v JOIN openfox.tasks t ON t.organization_id=v.organization_id AND t.id=v.task_id WHERE v.organization_id=f.organization_id AND v.field_id=f.id AND t.deleted_at IS NULL)`,
		`UPDATE openfox.projects p SET status='canceled',updated_at=clock_timestamp(),version=version+1
 FROM openfox.import_created_resources r WHERE r.organization_id=openfox_private.current_organization_id() AND r.job_id=$1::uuid AND r.kind='PROJECT' AND p.organization_id=r.organization_id AND p.id=r.resource_id AND p.version=1
 AND NOT EXISTS(SELECT 1 FROM openfox.project_spaces s WHERE s.organization_id=p.organization_id AND s.project_id=p.id AND s.space_id<>r.space_id)
 AND NOT EXISTS(SELECT 1 FROM openfox.tasks t WHERE t.organization_id=p.organization_id AND t.project_id=p.id AND t.deleted_at IS NULL)`,
		`UPDATE openfox.import_project_mappings m SET released_at=clock_timestamp() WHERE m.organization_id=openfox_private.current_organization_id() AND m.created_job_id=$1::uuid AND m.released_at IS NULL AND EXISTS(SELECT 1 FROM openfox.projects p WHERE p.organization_id=m.organization_id AND p.id=m.project_id AND p.status='canceled')`,
	} {
		if _, err := tx.Exec(ctx, stmt, jobID); err != nil {
			return err
		}
	}
	return nil
}

type fileEpic struct {
	ID      string `json:"id"`
	Created bool   `json:"created,omitempty"`
}

// Resolve the Epic before its children, even when its CSV row comes last.
// A re-import follows the saved source ID and preserves local project edits.
func resolveFileEpics(ctx context.Context, tx pgx.Tx, b *batchCtx, infos []rowInfo) (map[string]fileEpic, error) {
	var projects map[string]fileEpic
	for _, info := range infos {
		if !info.Epic || info.Filtered || info.Invalid {
			continue
		}
		if projects == nil {
			projects = map[string]fileEpic{}
		}
		var project fileEpic
		d := info.Data
		d.Description, _ = TruncateRunes(d.Description, 10000)
		if b.plan.apiProjects {
			var err error
			project, err = resolveAPIProject(ctx, tx, b, d)
			if err != nil {
				return nil, err
			}
			projects[d.SourceID] = project
			if d.SourceKey != "" {
				projects[d.SourceKey] = project
			}
			continue
		}
		err := tx.QueryRow(ctx, `SELECT i.project_id::text FROM openfox.import_items i JOIN openfox.import_jobs j ON j.organization_id=i.organization_id AND j.id=i.job_id
		 JOIN openfox.projects p ON p.organization_id=i.organization_id AND p.id=i.project_id
		 JOIN openfox.project_spaces ps ON ps.organization_id=p.organization_id AND ps.project_id=p.id AND ps.space_id=j.space_id
		 WHERE i.organization_id=openfox_private.current_organization_id() AND i.external_id=$1 AND i.project_id IS NOT NULL AND j.space_id=$2::uuid
		 AND j.source=$3 AND j.status<>'ROLLED_BACK' AND p.status<>'canceled' ORDER BY j.created_at DESC LIMIT 1`, d.SourceID, b.job.SpaceID, b.job.Source).Scan(&project.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO openfox.projects(organization_id,name,description,created_by) VALUES(openfox_private.current_organization_id(),$1,$2,$3::uuid) RETURNING id::text`, d.Title, d.Description, b.actor.UserID).Scan(&project.ID)
			if err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO openfox.project_spaces(organization_id,project_id,space_id,is_primary) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,true)`, project.ID, b.job.SpaceID); err != nil {
				return nil, err
			}
			if err = recordResource(ctx, tx, b.job.ID, "PROJECT", project.ID, &b.job.SpaceID, nil); err != nil {
				return nil, err
			}
			project.Created = true
		} else if err != nil {
			return nil, err
		}
		projects[d.SourceID] = project
		if d.SourceKey != "" {
			projects[d.SourceKey] = project
		}
	}
	return projects, nil
}
