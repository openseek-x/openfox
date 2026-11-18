package imports

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"strings"
)

type AzureSyncMappings struct {
	Version    int64             `json:"version"`
	Types      []TypeMapping     `json:"types"`
	Statuses   []StatusMapping   `json:"statuses"`
	Priorities []PriorityMapping `json:"priorities"`
	Analysis   Analysis          `json:"analysis"`
	SpaceID    string            `json:"space_id"`
}

func (s Service) GetAzureSyncMappings(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AzureSyncMappings, error) {
	out := AzureSyncMappings{}
	job, e := s.Get(ctx, tx, org, actor, id)
	if e != nil {
		return out, e
	}
	if job.Source != SourceAzure || job.Status != StatusSucceeded || job.Sync == nil || job.SpaceID == nil {
		return out, ErrImportState
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT config FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&raw); e != nil {
		return out, e
	}
	var cfg Config
	if e = json.Unmarshal(raw, &cfg); e != nil {
		return out, e
	}
	c, e := readAzureCrawl(ctx, tx, id)
	if e != nil {
		return out, e
	}
	records, e := loadAzureRecords(ctx, tx, c)
	if e != nil {
		return out, e
	}
	infos := []rowInfo{}
	p := configuredPlan(SourceAzure, nil, cfg)
	for i := range records {
		infos = append(infos, rowInfo{Data: p.record(&records[i])})
	}
	l, e := loadLookups(ctx, tx, *job.SpaceID)
	if e != nil {
		return out, e
	}
	out.Analysis = Analysis{Types: []AnalysisType{}, Statuses: []AnalysisStatus{}, Issues: []Issue{}}
	if e = analyzeAzure(ctx, tx, id, *job.SpaceID, cfg, infos, l, &out.Analysis); e != nil {
		return out, e
	}
	out.Version = job.Sync.Version
	out.SpaceID = *job.SpaceID
	out.Types = cfg.Types
	out.Statuses = cfg.Statuses
	out.Priorities = cfg.Priorities
	if out.Types == nil {
		out.Types = []TypeMapping{}
	}
	if out.Statuses == nil {
		out.Statuses = []StatusMapping{}
	}
	if out.Priorities == nil {
		out.Priorities = []PriorityMapping{}
	}
	return out, nil
}
func (s Service) SetAzureSyncMappings(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in AzureSyncMappings) (Job, error) {
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return Job{}, e
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if _, e := lockAzureImportSource(ctx, tx, id); e != nil {
		return Job{}, e
	}
	var space string
	e := tx.QueryRow(ctx, `SELECT l.space_id::text FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.version=$2 AND l.state<>'DISCONNECTED' AND j.source='azure_devops_api' AND j.status='SUCCEEDED' FOR UPDATE OF l`, id, in.Version).Scan(&space)
	if e == pgx.ErrNoRows {
		return Job{}, ErrVersionConflict
	}
	if e != nil {
		return Job{}, e
	}
	j, e := lockJob(ctx, tx, id)
	if e != nil {
		return Job{}, e
	}
	cfg := j.Config
	cfg.Types = in.Types
	cfg.Statuses = in.Statuses
	cfg.Priorities = in.Priorities
	if e = s.validateConfig(ctx, tx, &cfg, 0); e != nil {
		return Job{}, e
	}
	c, e := readAzureCrawl(ctx, tx, id)
	if e != nil {
		return Job{}, e
	}
	records, e := loadAzureRecords(ctx, tx, c)
	if e != nil {
		return Job{}, e
	}
	p := configuredPlan(SourceAzure, nil, cfg)
	infos := []rowInfo{}
	for i := range records {
		infos = append(infos, rowInfo{Data: p.record(&records[i])})
	}
	l, e := loadLookups(ctx, tx, space)
	if e != nil {
		return Job{}, e
	}
	if l.priorityLevels < 5 {
		for _, m := range cfg.Priorities {
			if m.Priority == "LOWEST" {
				return Job{}, ErrPriorityLevelUnavailable
			}
		}
	}
	resolved := Resolved{Statuses: map[string]string{}, Types: map[string]string{}}
	if j.Preview.Resolved != nil {
		resolved = *j.Preview.Resolved
		resolved.Statuses = map[string]string{}
		resolved.Types = map[string]string{}
	}
	batch := &batchCtx{orgID: org, actor: actor, collab: collaboration.Service{}, job: runJob{ID: id, Source: SourceAzure, SpaceID: space, Cfg: cfg, Preview: j.Preview}, plan: p, warns: newWarnAcc(nil)}
	if e = (&Worker{}).resolveAzureMappings(ctx, tx, batch, &resolved, infos, l); e != nil {
		return Job{}, e
	}
	j.Preview.Resolved = &resolved
	cfgRaw, _ := json.Marshal(cfg)
	previewRaw, _ := json.Marshal(j.Preview)
	_, e = tx.Exec(ctx, `UPDATE openfox.import_jobs SET config=$2::jsonb,preview=$3::jsonb,config_version=config_version+1,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, cfgRaw, previewRaw)
	if e != nil {
		return Job{}, e
	}
	mappings := map[string]string{}
	for k, v := range resolved.Statuses {
		mappings[strings.ToLower(k)] = v
	}
	raw, _ := json.Marshal(mappings)
	_, e = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET status_mappings=$3::jsonb,lease=NULL,error_code='',version=version+1,next_check=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND version=$2`, id, in.Version, raw)
	if e != nil {
		return Job{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET lease=NULL,lease_until=NULL,retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, id)
	if e != nil {
		return Job{}, e
	}
	return s.Get(ctx, tx, org, actor, id)
}
func (s Service) RequestAzureHistory(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version int64) (Job, error) {
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return Job{}, e
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	a, e := lockAzureImportSource(ctx, tx, id)
	if e != nil {
		return Job{}, e
	}
	if a.ConnectionID == "" {
		return Job{}, ErrImportState
	}
	var project string
	e = tx.QueryRow(ctx, `SELECT l.project_id FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.job_id=$1::uuid AND l.version=$2 AND l.state='ACTIVE' AND l.lease IS NULL AND j.source='azure_devops_api' AND j.status='SUCCEEDED' FOR UPDATE OF l`, id, version).Scan(&project)
	if e == pgx.ErrNoRows {
		return Job{}, ErrVersionConflict
	}
	if e != nil {
		return Job{}, e
	}
	ready, e := integrations.IsAzureBoardsReady(ctx, tx, a.ConnectionID, project)
	if e != nil {
		return Job{}, e
	}
	if !ready {
		return Job{}, ErrImportState
	}
	c, e := readAzureCrawl(ctx, tx, id)
	if e != nil {
		return Job{}, e
	}
	if c.state != "DONE" && c.state != "FAILED" {
		return Job{}, ErrImportState
	}
	_, e = tx.Exec(ctx, `DELETE FROM openfox.import_azure_nodes WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, id)
	if e != nil {
		return Job{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.import_azure_crawls SET cycle_id=gen_random_uuid(),kind='HISTORY',state='FETCHING',include_history=true,snapshot_at=NULL,changed_from=NULL,cursor_id=0,page_ids='{}',item_count=0,data_bytes=0,failures=0,lease=NULL,lease_until=NULL,retry_at=NULL,deadline=clock_timestamp()+interval '2 hours' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid`, id)
	if e != nil {
		return Job{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET next_check=clock_timestamp(),error_code='',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND version=$2`, id, version)
	if e != nil {
		return Job{}, e
	}
	return s.Get(ctx, tx, org, actor, id)
}
