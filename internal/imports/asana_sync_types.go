package imports

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

type AsanaTypeSource struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}
type AsanaSyncTypes struct {
	Version int64             `json:"version"`
	Types   []TypeMapping     `json:"types"`
	Sources []AsanaTypeSource `json:"sources"`
	SpaceID string            `json:"space_id"`
}

func (s Service) GetAsanaSyncTypes(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AsanaSyncTypes, error) {
	var view AsanaSyncTypes
	job, err := s.Get(ctx, tx, org, actor, id)
	if err != nil {
		return view, err
	}
	if job.Source != SourceAsana || job.Status != StatusSucceeded || job.Sync == nil || job.SpaceID == nil {
		return view, ErrImportState
	}
	view.SpaceID = *job.SpaceID
	view.Version = job.Sync.Version
	view.Types = []TypeMapping{}
	view.Sources = []AsanaTypeSource{}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT config->'types' FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&raw); err != nil {
		return view, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &view.Types); err != nil {
			return view, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT coalesce(nullif(source_data->>'resource_subtype',''),'default_task'),count(*) FROM openfox.import_asana_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid GROUP BY 1 ORDER BY 1 LIMIT 51`, id)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var source AsanaTypeSource
		if err = rows.Scan(&source.Source, &source.Count); err != nil {
			return view, err
		}
		view.Sources = append(view.Sources, source)
	}
	if err = rows.Err(); err != nil {
		return view, err
	}
	if len(view.Sources) > 50 {
		return view, errAsanaLimit
	}
	if view.Types == nil {
		view.Types = []TypeMapping{}
	}
	return view, nil
}
func (s Service) SetAsanaSyncTypes(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version int64, types []TypeMapping) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	var space string
	if err := tx.QueryRow(ctx, `SELECT l.space_id::text FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.version=$2 AND l.state<>'DISCONNECTED' AND j.source='asana_api' AND j.status='SUCCEEDED' FOR UPDATE OF l`, id, version).Scan(&space); err != nil {
		if err == pgx.ErrNoRows {
			return Job{}, ErrVersionConflict
		}
		return Job{}, err
	}
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	cfg := j.Config
	cfg.Types = types
	view, err := s.GetAsanaSyncTypes(ctx, tx, org, actor, id)
	if err != nil {
		return Job{}, err
	}
	analysis := Analysis{Types: []AnalysisType{}}
	for _, source := range view.Sources {
		analysis.Types = append(analysis.Types, AnalysisType{Source: source.Source})
	}
	if err = analyzeAsana(ctx, tx, space, cfg, &analysis); err != nil {
		return Job{}, err
	}
	if len(analysis.Issues) > 0 {
		return Job{}, ErrConfigIncomplete
	}
	resolved := map[string]string{}
	for _, m := range types {
		if !m.UseDefault {
			resolved[strings.ToLower(m.Source)] = m.TypeID
		}
	}
	rawTypes, _ := json.Marshal(types)
	rawResolved, _ := json.Marshal(resolved)
	_, err = tx.Exec(ctx, `UPDATE openfox.import_jobs SET config=jsonb_set(config,'{types}',$2::jsonb),preview=jsonb_set(preview,'{resolved,types}',$3::jsonb),config_version=config_version+1,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, rawTypes, rawResolved)
	if err != nil {
		return Job{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET lease=NULL,error_code='',version=version+1,next_check=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND version=$2`, id, version)
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, org, actor, id)
}
