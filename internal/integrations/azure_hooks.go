package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
)

var ErrAzureHookCleanup = errors.New("azure_devops: previous hook cleanup must finish")
var ErrAzureHookHTTPS = errors.New("azure_devops: hooks require an HTTPS public URL")

var azureGitEvents = []string{"git.push", "git.pullrequest.created", "git.pullrequest.updated"}
var azureBoardsEvents = []string{"workitem.created", "workitem.updated", "workitem.deleted", "workitem.restored"}

// Versions are pinned to archived official event examples, then checked against
// current publisher metadata. An unknown version is never parsed optimistically.
func azureEventVersion(event string) string {
	switch event {
	case "git.push", "git.pullrequest.created", "git.pullrequest.updated", "workitem.created", "workitem.deleted":
		return "2.0"
	case "workitem.updated", "workitem.restored":
		return "1.0"
	default:
		return ""
	}
}

type AzureRepositoryRef struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
}
type AzureRepository struct {
	ProjectID      string `json:"project_id"`
	ProjectName    string `json:"project_name"`
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
}
type AzureHookView struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	RepositoryID    string `json:"repository_id,omitempty"`
	EventType       string `json:"event_type"`
	State           string `json:"state"`
	Desired         bool   `json:"desired"`
	Generation      uint64 `json:"generation"`
	OwnerGeneration uint64 `json:"remote_generation"`
	LastError       string `json:"last_error"`
	SubscriptionID  string `json:"subscription_id,omitempty"`
	ManualCleanup   bool   `json:"manual_cleanup"`
}
type AzureHooksConfig struct {
	Version           uint64            `json:"version"`
	ConnectionVersion uint64            `json:"connection_version"`
	GrantGeneration   uint64            `json:"grant_generation"`
	Repositories      []AzureRepository `json:"repositories"`
	Hooks             []AzureHookView   `json:"hooks"`
}
type AzureRepositorySelection struct {
	access       AzureAccess
	repositories []AzureRepository
	metadata     azuredevops.HookMetadata
	verified     bool
}

func azureHookAAD(org, id string) string { return "azure-devops-hook:" + org + ":" + id }
func (s Service) azureHookURL(id string, generation uint64) (string, error) {
	u, e := url.Parse(s.PublicURL)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !membership.ValidateUUID(id) || generation == 0 {
		return "", ErrAzureHookHTTPS
	}
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/integrations/azure_devops/hooks/" + id + "/" + strconv.FormatUint(generation, 10), nil
}
func azureHookUsername(id string) string { return "openfox-" + id }
func (s Service) validateAzureHook(metadata azuredevops.HookMetadata, project, repo, event string) error {
	callback, e := s.azureHookURL("00000000-0000-4000-8000-000000000000", 1)
	if e != nil {
		return e
	}
	return azuredevops.ValidateHookCreate(azuredevops.HookCreateInput{ProjectID: project, RepositoryID: repo, EventType: event, ResourceVersion: azureEventVersion(event), CallbackURL: callback, BasicUsername: azureHookUsername("00000000-0000-4000-8000-000000000000"), BasicPassword: strings.Repeat("x", 43)}, metadata)
}
func fetchAzureHookMetadata(ctx context.Context, c *azuredevops.Client, token, organization string) (azuredevops.HookMetadata, error) {
	var m azuredevops.HookMetadata
	var e error
	m.Publishers, e = c.HookPublishers(ctx, token, organization)
	if e != nil {
		return m, e
	}
	m.Consumers, e = c.HookConsumers(ctx, token, organization)
	return m, e
}
func (s Service) VerifyAzureHookMetadata(ctx context.Context, runtime AzureRuntime, org string, access AzureAccess) (azuredevops.HookMetadata, error) {
	var m azuredevops.HookMetadata
	_, e := runtime.Do(ctx, org, access.ConnectionID, access.ConnectionVersion, access.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		m, e = fetchAzureHookMetadata(ctx, c, token, access.OrganizationName)
		return e
	})
	return m, e
}
func (s Service) VerifyAzureRepositorySelection(ctx context.Context, runtime AzureRuntime, org string, access AzureAccess, refs []AzureRepositoryRef) (AzureRepositorySelection, error) {
	out := AzureRepositorySelection{access: access, repositories: []AzureRepository{}}
	if len(refs) > 20 {
		return out, ErrInvalidRequest
	}
	seen := map[string]bool{}
	for i := range refs {
		refs[i].ProjectID = strings.ToLower(refs[i].ProjectID)
		refs[i].RepositoryID = strings.ToLower(refs[i].RepositoryID)
		key := refs[i].ProjectID + "/" + refs[i].RepositoryID
		if !azuredevops.ValidUUID(refs[i].ProjectID) || !azuredevops.ValidUUID(refs[i].RepositoryID) || seen[key] {
			return out, ErrInvalidRequest
		}
		seen[key] = true
	}
	if len(refs) == 0 {
		out.verified = true
		return out, nil
	}
	_, e := runtime.Do(ctx, org, access.ConnectionID, access.ConnectionVersion, access.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		projects := map[string]azuredevops.Project{}
		repositories := make([]AzureRepository, 0, len(refs))
		for _, ref := range refs {
			p, ok := projects[ref.ProjectID]
			if !ok {
				var e error
				p, e = c.Project(ctx, token, access.OrganizationName, ref.ProjectID)
				if e != nil {
					return e
				}
				projects[ref.ProjectID] = p
			}
			repo, e := c.Repository(ctx, token, access.OrganizationName, ref.ProjectID, ref.RepositoryID)
			if e != nil {
				return e
			}
			repositories = append(repositories, AzureRepository{ref.ProjectID, FirstLine(p.Name, 64), ref.RepositoryID, FirstLine(repo.Name, 64)})
		}
		metadata, e := fetchAzureHookMetadata(ctx, c, token, access.OrganizationName)
		if e == nil {
			out.repositories, out.metadata = repositories, metadata
		}
		return e
	})
	if e != nil {
		return AzureRepositorySelection{}, e
	}
	for _, repo := range out.repositories {
		for _, event := range azureGitEvents {
			if e = s.validateAzureHook(out.metadata, repo.ProjectID, repo.RepositoryID, event); e != nil {
				return AzureRepositorySelection{}, e
			}
		}
	}
	sort.Slice(out.repositories, func(i, j int) bool {
		a, b := out.repositories[i], out.repositories[j]
		return a.ProjectID+"/"+a.RepositoryID < b.ProjectID+"/"+b.RepositoryID
	})
	out.verified = true
	return out, nil
}

// Serialize local configuration on the grant before taking the parent update
// lock. Taking SHARE then upgrading parent locks allows concurrent-save deadlocks.
func LockAzureHookConfiguration(ctx context.Context, tx pgx.Tx, access AzureAccess) error {
	var id string
	if e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, access.GrantID).Scan(&id); e != nil {
		return azureMissing(e)
	}
	if _, e := LockAzureAccess(ctx, tx, access.ConnectionID, access.ConnectionVersion, access.GrantGeneration); e != nil {
		return e
	}
	return tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, access.ConnectionID).Scan(&id)
}
func readAzureRepositorySettings(ctx context.Context, tx pgx.Tx, id string) ([]AzureRepository, uint64, error) {
	var raw []byte
	var version uint64
	e := tx.QueryRow(ctx, `SELECT coalesce(settings->'azure_repositories','[]'::jsonb),coalesce((settings->>'azure_repos_version')::bigint,0) FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION'`, id).Scan(&raw, &version)
	if e != nil {
		return nil, 0, azureMissing(e)
	}
	repos := []AzureRepository{}
	if json.Unmarshal(raw, &repos) != nil || len(repos) > 20 {
		return nil, 0, ErrInvalidRequest
	}
	return repos, version, nil
}
func (s Service) SaveAzureRepositories(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access AzureAccess, version uint64, selection AzureRepositorySelection) (AzureHooksConfig, error) {
	if !selection.verified || selection.access.ConnectionID != access.ConnectionID || selection.access.ConnectionVersion != access.ConnectionVersion || selection.access.GrantGeneration != access.GrantGeneration {
		return AzureHooksConfig{}, ErrInvalidRequest
	}
	if e := LockAzureHookConfiguration(ctx, tx, access); e != nil {
		return AzureHooksConfig{}, e
	}
	if e := azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return AzureHooksConfig{}, e
	}
	_, current, e := readAzureRepositorySettings(ctx, tx, access.ConnectionID)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	if current != version {
		return AzureHooksConfig{}, ErrVersionConflict
	}
	for _, repo := range selection.repositories {
		for _, event := range azureGitEvents {
			if e = s.ensureAzureHook(ctx, tx, org, access, repo.ProjectID, repo.RepositoryID, event, selection.metadata); e != nil {
				return AzureHooksConfig{}, e
			}
		}
	}
	raw, _ := json.Marshal(selection.repositories)
	// Withdrawal changes local generation immediately; remote_generation still
	// identifies any already-issued create whose result has not been reconciled.
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks h SET desired=false,generation=generation+1,state=CASE WHEN state='CREATE_PENDING' THEN 'DELETED' ELSE 'DELETE_PENDING' END,secret_sealed=CASE WHEN state='CREATE_PENDING' THEN NULL ELSE secret_sealed END,secret_key_id=CASE WHEN state='CREATE_PENDING' THEN '' ELSE secret_key_id END,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=clock_timestamp()+interval '24 hours',updated_at=clock_timestamp() WHERE h.organization_id=openfox_private.current_organization_id() AND h.connection_id=$1::uuid AND h.repository_id IS NOT NULL AND h.desired AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset($2::jsonb) AS a(project_id text,repository_id text) WHERE a.project_id=h.project_id::text AND a.repository_id=h.repository_id::text)`, access.ConnectionID, raw)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=settings||jsonb_build_object('azure_repositories',$2::jsonb,'azure_repos_version',$3::bigint),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, access.ConnectionID, raw, version+1)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	return s.azureHooksConfig(ctx, tx, access)
}
func (s Service) ensureAzureHook(ctx context.Context, tx pgx.Tx, org string, access AzureAccess, project, repo, event string, metadata azuredevops.HookMetadata) error {
	if e := s.validateAzureHook(metadata, project, repo, event); e != nil {
		return e
	}
	var id, state string
	var desired bool
	var generation uint64
	e := tx.QueryRow(ctx, `SELECT id::text,state,desired,generation FROM openfox.integration_azure_hooks WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND project_id=$2::uuid AND repository_id IS NOT DISTINCT FROM $3::uuid AND event_type=$4 FOR UPDATE`, access.ConnectionID, project, nullableAzureID(repo), event).Scan(&id, &state, &desired, &generation)
	create := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !create {
		return e
	}
	if !create && state != "DELETED" {
		if !desired || state == "DELETE_PENDING" || state == "DELETING" || state == "DELETE_UNKNOWN" {
			return ErrAzureHookCleanup
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET connection_version=$2,grant_generation=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, access.ConnectionVersion, access.GrantGeneration)
		return e
	}
	if create {
		id, e = membership.NewID()
		if e != nil {
			return e
		}
		generation = 1
	} else {
		generation++
	}
	secret, e := randomBase64URL(32)
	if e != nil {
		return e
	}
	sealed, key, e := s.seal([]byte(secret), azureHookAAD(org, id))
	if e != nil {
		return e
	}
	if create {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_azure_hooks(organization_id,id,connection_id,project_id,repository_id,event_type,resource_version,generation,remote_generation,connection_version,grant_generation,secret_sealed,secret_key_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$7,$8,$9,$10,$11)`, id, access.ConnectionID, project, nullableAzureID(repo), event, azureEventVersion(event), generation, access.ConnectionVersion, access.GrantGeneration, sealed, key)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET desired=true,state='CREATE_PENDING',generation=$2,remote_generation=$2,connection_version=$3,grant_generation=$4,resource_version=$5,subscription_id=NULL,secret_sealed=$6,secret_key_id=$7,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, generation, access.ConnectionVersion, access.GrantGeneration, azureEventVersion(event), sealed, key)
	}
	return e
}
func nullableAzureID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func (s Service) azureHooksConfig(ctx context.Context, tx pgx.Tx, access AzureAccess) (AzureHooksConfig, error) {
	repos, version, e := readAzureRepositorySettings(ctx, tx, access.ConnectionID)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	out := AzureHooksConfig{Version: version, ConnectionVersion: access.ConnectionVersion, GrantGeneration: access.GrantGeneration, Repositories: repos, Hooks: []AzureHookView{}}
	rows, e := tx.Query(ctx, `SELECT id::text,project_id::text,coalesce(repository_id::text,''),event_type,state,desired,generation,remote_generation,last_error,coalesce(subscription_id::text,'') FROM openfox.integration_azure_hooks WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid ORDER BY project_id,repository_id,event_type LIMIT 1001`, access.ConnectionID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var h AzureHookView
		if e = rows.Scan(&h.ID, &h.ProjectID, &h.RepositoryID, &h.EventType, &h.State, &h.Desired, &h.Generation, &h.OwnerGeneration, &h.LastError, &h.SubscriptionID); e != nil {
			return out, e
		}
		h.ManualCleanup = h.LastError == "remote_cleanup_required" || h.LastError == "create_result_unknown" || h.LastError == "duplicate_owned_hooks"
		out.Hooks = append(out.Hooks, h)
	}
	if len(out.Hooks) > 1000 {
		return AzureHooksConfig{}, ErrInvalidRequest
	}
	return out, rows.Err()
}
func (s Service) GetAzureHooksConfig(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AzureHooksConfig, error) {
	if !membership.ValidateUUID(id) {
		return AzureHooksConfig{}, ErrInvalidRequest
	}
	var a AzureAccess
	a.ConnectionID = id
	e := tx.QueryRow(ctx, `SELECT azure_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION'`, id).Scan(&a.GrantID)
	if e != nil {
		return AzureHooksConfig{}, azureMissing(e)
	}
	e = tx.QueryRow(ctx, `SELECT generation FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, a.GrantID).Scan(&a.GrantGeneration)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	e = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, id).Scan(&a.ConnectionVersion)
	if e != nil {
		return AzureHooksConfig{}, e
	}
	if e = azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return AzureHooksConfig{}, e
	}
	return s.azureHooksConfig(ctx, tx, a)
}

// Call after grant/parent locks and before any crawl/sync row lock. Callers must
// insert the new crawl reference in this same transaction before committing.
func (s Service) EnsureAzureBoardsHooks(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access AzureAccess, project string, metadata azuredevops.HookMetadata) error {
	if !azuredevops.ValidUUID(project) {
		return ErrInvalidRequest
	}
	if e := LockAzureHookConfiguration(ctx, tx, access); e != nil {
		return e
	}
	if e := azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return e
	}
	for _, event := range azureBoardsEvents {
		if e := s.ensureAzureHook(ctx, tx, org, access, strings.ToLower(project), "", event, metadata); e != nil {
			return e
		}
	}
	return nil
}
func IsAzureBoardsReady(ctx context.Context, tx pgx.Tx, connection, project string) (bool, error) {
	var ready bool
	e := tx.QueryRow(ctx, `SELECT count(*)=4 AND coalesce(bool_and(h.desired AND h.state='ACTIVE' AND h.connection_version=c.version AND h.grant_generation=g.generation AND c.status='ACTIVE' AND g.status='ACTIVE'),false) FROM openfox.integration_azure_hooks h JOIN openfox.integration_connections c ON c.organization_id=h.organization_id AND c.id=h.connection_id JOIN openfox.integration_azure_grants g ON g.organization_id=c.organization_id AND g.id=c.azure_grant_id WHERE h.organization_id=openfox_private.current_organization_id() AND h.connection_id=$1::uuid AND h.project_id=$2::uuid AND h.repository_id IS NULL AND h.event_type=ANY($3::text[])`, connection, project, azureBoardsEvents).Scan(&ready)
	return ready, e
}

// The caller removes/pauses its reference first in the same grant -> parent
// transaction. Other ACTIVE spaces and unfinished, unexpired crawls retain hooks.
func (s Service) ReconcileAzureBoardsHooks(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access AzureAccess, project string) error {
	if e := LockAzureHookConfiguration(ctx, tx, access); e != nil {
		return e
	}
	if e := azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return e
	}
	var used bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.import_sync_links WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND project_id=$2 AND state='ACTIVE') OR EXISTS(SELECT 1 FROM openfox.import_azure_crawls c JOIN openfox.import_jobs j ON j.organization_id=c.organization_id AND j.id=c.job_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.connection_id=$1::uuid AND c.project_id=$2::uuid AND ((j.status='DRAFT' AND j.created_at>clock_timestamp()-interval '24 hours') OR j.status IN('QUEUED','RUNNING')))`, access.ConnectionID, project).Scan(&used)
	if e != nil || used {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET desired=false,generation=generation+1,state=CASE WHEN state='CREATE_PENDING' THEN 'DELETED' ELSE 'DELETE_PENDING' END,secret_sealed=CASE WHEN state='CREATE_PENDING' THEN NULL ELSE secret_sealed END,secret_key_id=CASE WHEN state='CREATE_PENDING' THEN '' ELSE secret_key_id END,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=clock_timestamp()+interval '24 hours',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND project_id=$2::uuid AND repository_id IS NULL AND desired`, access.ConnectionID, project)
	return e
}
