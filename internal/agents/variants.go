package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/reposource"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

var (
	ErrVariantParent   = collaboration.NewRuleError(http.StatusConflict, "INVALID_VARIANT_PARENT", "parent Agent is archived, built-in, or a variant")
	ErrVariantComputer = collaboration.NewRuleError(http.StatusConflict, "VARIANT_COMPUTER_SCOPE", "override the computer for this target space")
)

// VariantOverrides 的 nil 组表示继承；非 nil 组表示明确覆盖，空仓库和空环境变量表示清空。
type VariantOverrides struct {
	Instructions *string            `json:"instructions,omitempty"`
	Computer     *VariantComputer   `json:"computer,omitempty"`
	Runtime      *VariantRuntime    `json:"runtime,omitempty"`
	Directory    *VariantDirectory  `json:"directory,omitempty"`
	Limits       *VariantLimits     `json:"limits,omitempty"`
	Repos        *[]RepoInput       `json:"repos,omitempty"`
	Env          *map[string]string `json:"env,omitempty"`
	RunSettings  *RunSettings       `json:"run_settings,omitempty"`
}
type VariantComputer struct {
	ComputerID string `json:"computer_id"`
	GrantID    string `json:"grant_id"`
}
type VariantRuntime struct {
	Runtime     string `json:"runtime"`
	Model       string `json:"model"`
	ModelSource string `json:"model_source,omitempty"`
}
type VariantDirectory struct {
	Mode  DirectoryMode `json:"directory_mode"`
	Roots []string      `json:"directory_roots"`
}
type VariantLimits struct {
	MaxConcurrency      int   `json:"max_concurrency"`
	QueueTimeoutSeconds int64 `json:"queue_timeout_seconds"`
	RunTimeoutSeconds   int64 `json:"run_timeout_seconds"`
}
type VariantInput struct {
	SpaceID   string           `json:"space_id"`
	Name      *string          `json:"name,omitempty"`
	Overrides VariantOverrides `json:"overrides"`
}

func (o VariantOverrides) groups() []string {
	groups := make([]string, 0, len(VariantGroups))
	if o.Instructions != nil {
		groups = append(groups, GroupInstructions)
	}
	if o.Computer != nil {
		groups = append(groups, GroupComputer)
	}
	if o.Runtime != nil {
		groups = append(groups, GroupRuntime)
	}
	if o.Directory != nil {
		groups = append(groups, GroupDirectory)
	}
	if o.Limits != nil {
		groups = append(groups, GroupLimits)
	}
	if o.Repos != nil {
		groups = append(groups, GroupRepos)
	}
	if o.Env != nil {
		groups = append(groups, GroupEnv)
	}
	if o.RunSettings != nil {
		groups = append(groups, GroupRunSettings)
	}
	return groups
}
func groupSet(groups []string) map[string]bool {
	result := make(map[string]bool, len(groups))
	for _, g := range groups {
		result[g] = true
	}
	return result
}
func mergeConfig(parent, child ConfigInput, groups map[string]bool) ConfigInput {
	if groups[GroupInstructions] {
		child.Instructions = parent.Instructions
	}
	if groups[GroupComputer] {
		child.ComputerID, child.GrantID = parent.ComputerID, parent.GrantID
	}
	if groups[GroupRuntime] {
		child.Runtime, child.Model, child.ModelSource = parent.Runtime, parent.Model, parent.ModelSource
	}
	if groups[GroupDirectory] {
		child.DirectoryMode, child.DirectoryRoots = parent.DirectoryMode, parent.DirectoryRoots
	}
	if groups[GroupLimits] {
		child.MaxConcurrency, child.QueueTimeoutSeconds, child.RunTimeoutSeconds = parent.MaxConcurrency, parent.QueueTimeoutSeconds, parent.RunTimeoutSeconds
	}
	if groups[GroupRunSettings] {
		modelSource := child.ModelSource
		child.RunSettings = parent.RunSettings
		child.ModelSource = modelSource
		if child.WorkingHours == nil {
			child.WorkingHours = json.RawMessage("null")
		}
	}
	return child
}
func configInput(c *ConfigVersion) ConfigInput {
	return ConfigInput{Instructions: c.Instructions, ComputerID: c.ComputerID, GrantID: c.GrantID, Runtime: c.Runtime, Model: c.Model, MaxConcurrency: c.MaxConcurrency, QueueTimeoutSeconds: c.QueueTimeoutSeconds, RunTimeoutSeconds: c.RunTimeoutSeconds, DirectoryMode: c.DirectoryMode, DirectoryRoots: c.DirectoryRoots, RunSettings: c.RunSettings}
}
func applyOverrides(c ConfigInput, o VariantOverrides) ConfigInput {
	if o.Instructions != nil {
		c.Instructions = *o.Instructions
	}
	if o.Computer != nil {
		c.ComputerID, c.GrantID = o.Computer.ComputerID, o.Computer.GrantID
	}
	if o.Runtime != nil {
		c.Runtime, c.Model, c.ModelSource = o.Runtime.Runtime, o.Runtime.Model, o.Runtime.ModelSource
		if c.ModelSource == "" {
			c.ModelSource = "EXPLICIT"
		}
	}
	if o.Directory != nil {
		c.DirectoryMode, c.DirectoryRoots = o.Directory.Mode, o.Directory.Roots
	}
	if o.Limits != nil {
		c.MaxConcurrency, c.QueueTimeoutSeconds, c.RunTimeoutSeconds = o.Limits.MaxConcurrency, o.Limits.QueueTimeoutSeconds, o.Limits.RunTimeoutSeconds
	}
	if o.RunSettings != nil {
		modelSource := c.ModelSource
		c.RunSettings = *o.RunSettings
		c.ModelSource = modelSource
	}
	return c
}

// CreateVariant 在父级行锁下创建分身，幂等记录与配置、仓库、环境变量在同一个租户事务提交。
func (s PostgresStore) CreateVariant(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, parentSpace, parentID string, input VariantInput, key string) (Agent, bool, error) {
	parentSpace, parentID, err := s.agentRef(sel, actor, parentSpace, parentID)
	if err != nil {
		return Agent{}, false, err
	}
	input.SpaceID, err = normalizeUUID(input.SpaceID)
	if err != nil {
		return Agent{}, false, ErrInvalidRequest
	}
	if !idempotency.ValidKey(key) {
		return Agent{}, false, idempotency.ErrInvalidKey
	}
	for name, value := range valueOrEmpty(input.Overrides.Env) {
		if !envNamePattern.MatchString(name) || len(value) > secrets.MaxPlaintext {
			return Agent{}, false, ErrInvalidRequest
		}
	}
	if input.Overrides.Env != nil && len(*input.Overrides.Env) > MaxEnvVars {
		return Agent{}, false, ErrLimitReached
	}
	if input.Overrides.Repos != nil && len(*input.Overrides.Repos) > MaxRepos {
		return Agent{}, false, ErrLimitReached
	}
	var result Agent
	var replay bool
	err = s.run(ctx, sel, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, sel.OrganizationID, parentSpace, actor, policy.ActionReadSpace, true); err != nil {
			return err
		}
		if err := authorizeSpace(ctx, tx, sel.OrganizationID, input.SpaceID, actor, policy.ActionManageSpace, false); err != nil {
			return err
		}
		if err := requireWritableSpace(ctx, tx, input.SpaceID); err != nil {
			return err
		}
		// 父行锁串行化创建、配置传播和归档；触发器再对每个父级强制 20 上限。
		parent, err := lockAgent(ctx, tx, sel.OrganizationID, parentSpace, parentID)
		if err != nil {
			return err
		}
		if parent.Visibility == VisibilityPrivate && parent.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
		claim, err := idempotency.Begin(ctx, tx, "agent.variant_create", key, struct {
			ParentSpace string       `json:"parent_space"`
			ParentID    string       `json:"parent_id"`
			Input       VariantInput `json:"input"`
		}{parentSpace, parentID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replay = true
			result, err = loadAgentByID(ctx, tx, sel.OrganizationID, claim.ResourceID, actor.UserID)
			if err != nil {
				return err
			}
			return prepareVariantResponse(ctx, tx, sel.OrganizationID, actor, &result)
		}
		if parent.BuiltinKind != "" || parent.ParentAgentID != "" || parent.ArchivedAt != nil {
			return ErrVariantParent
		}
		source, err := loadAgent(ctx, tx, sel.OrganizationID, parentSpace, parentID, actor.UserID, true)
		if err != nil {
			return err
		}
		if source.Config == nil {
			return ErrInvalidConfig
		}
		if input.Overrides.Computer == nil {
			if err := grantScopeFor(ctx, tx, source.Config.GrantID, input.SpaceID, actor.UserID); err != nil {
				return ErrVariantComputer
			}
		}
		name := ""
		if input.Name != nil {
			name = strings.TrimSpace(*input.Name)
		} else {
			name, err = pickCopyName(ctx, tx, input.SpaceID, source.Name)
			if err != nil {
				return err
			}
		}
		if err := validateAgentFields(name, source.Description, source.Kind, VisibilityPrivate); err != nil {
			return err
		}
		cfg, err := normalizeConfigInput(applyOverrides(configInput(source.Config), input.Overrides))
		if err != nil {
			return err
		}
		if err := validateConfigTarget(ctx, tx, sel.OrganizationID, cfg); err != nil {
			return err
		}
		if err := grantScopeFor(ctx, tx, cfg.GrantID, input.SpaceID, actor.UserID); err != nil {
			return err
		}
		id, err := membership.NewID()
		if err != nil {
			return err
		}
		inherited := make([]string, 0, len(VariantGroups))
		overridden := groupSet(input.Overrides.groups())
		for _, group := range VariantGroups {
			if !overridden[group] {
				inherited = append(inherited, group)
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.agents (organization_id,id,device_id,space_id,owner_user_id,name,description,kind,visibility,current_config_version,version,updated_at,parent_agent_id,inherited_groups,title,avatar,allow_assign,allow_direct_chat,trigger_crew,trigger_mention,trigger_workflow)
    VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,'PRIVATE',1,1,clock_timestamp(),$9::uuid,$10,$11,$12::jsonb,$13,$14,$15,$16,$17)`, sel.OrganizationID, id, cfg.ComputerID, input.SpaceID, actor.UserID, name, source.Description, source.Kind, parentID, inherited, source.Title, source.Avatar, source.AllowAssign, source.AllowDirectChat, source.TriggerCrew, source.TriggerMention, source.TriggerWorkflow)
		if err != nil {
			return mapDatabaseError(err)
		}
		trusted, err := variantSecretTrust(ctx, tx, parentID, cfg.ComputerID, actor.UserID)
		if err != nil {
			return err
		}
		if input.Overrides.Env == nil {
			if trusted {
				if err = copyEnvVars(ctx, tx, s.Secrets, sel.OrganizationID, parentID, id, actor.UserID); err != nil {
					return err
				}
			}
		} else {
			if err = writeVariantEnv(ctx, tx, s.Secrets, sel.OrganizationID, id, actor.UserID, *input.Overrides.Env); err != nil {
				return err
			}
		}
		digest, err := computeEnvDigest(ctx, tx, s.Secrets, sel.OrganizationID, id)
		if err != nil {
			return err
		}
		if err = insertConfigWithDigest(ctx, tx, sel.OrganizationID, id, 1, actor.UserID, cfg, digest); err != nil {
			return err
		}
		if input.Overrides.Repos == nil {
			err = syncVariantReposSafe(ctx, tx, parentID, id, actor.UserID, trusted)
		} else {
			err = writeVariantRepos(ctx, tx, sel.OrganizationID, id, actor, *input.Overrides.Repos)
		}
		if err != nil {
			return err
		}
		if err = idempotency.Complete(ctx, tx, claim, id); err != nil {
			return err
		}
		if err = recordAudit(ctx, tx, sel, actor, "agent.variant_created", id, input.SpaceID, 1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, sel.OrganizationID, input.SpaceID, id, actor.UserID, false)
		if err != nil {
			return err
		}
		return prepareVariantResponse(ctx, tx, sel.OrganizationID, actor, &result)
	})
	return result, replay, err
}
func changedConfigGroups(before, after ConfigInput) map[string]bool {
	changed := map[string]bool{}
	if before.Instructions != after.Instructions {
		changed[GroupInstructions] = true
	}
	if before.ComputerID != after.ComputerID || before.GrantID != after.GrantID {
		changed[GroupComputer] = true
	}
	if before.Runtime != after.Runtime || before.Model != after.Model || before.ModelSource != after.ModelSource {
		changed[GroupRuntime] = true
	}
	if before.DirectoryMode != after.DirectoryMode || !reflect.DeepEqual(before.DirectoryRoots, after.DirectoryRoots) {
		changed[GroupDirectory] = true
	}
	if before.MaxConcurrency != after.MaxConcurrency || before.QueueTimeoutSeconds != after.QueueTimeoutSeconds || before.RunTimeoutSeconds != after.RunTimeoutSeconds {
		changed[GroupLimits] = true
	}
	beforeSettings, afterSettings := before.RunSettings, after.RunSettings
	beforeSettings.ModelSource, afterSettings.ModelSource = "", ""
	if !reflect.DeepEqual(beforeSettings, afterSettings) {
		changed[GroupRunSettings] = true
	}
	return changed
}

func valueOrEmpty(v *map[string]string) map[string]string {
	if v == nil {
		return nil
	}
	return *v
}
func grantScopeFor(ctx context.Context, tx pgx.Tx, grantID, spaceID, ownerID string) error {
	var scope string
	var scopeSpace, scopeUser *string
	err := tx.QueryRow(ctx, `SELECT scope,scope_space_id::text,scope_user_id::text FROM openfox.device_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, grantID).Scan(&scope, &scopeSpace, &scopeUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceScopeMismatch
	}
	if err != nil {
		return err
	}
	return grantAllows(scope, scopeSpace, scopeUser, spaceID, ownerID)
}

// variantSecretTrust 只允许父所有者自己的分身在父所有者的实体电脑上继承父级秘密。
func variantSecretTrust(ctx context.Context, tx pgx.Tx, parentID, computerID, childOwnerID string) (bool, error) {
	var trusted bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(p.owner_user_id=$3::uuid AND d.owner_user_id=p.owner_user_id,false)
		FROM openfox.agents p JOIN openfox.physical_devices d ON d.id=$2::uuid
		WHERE p.organization_id=openfox_private.current_organization_id() AND p.id=$1::uuid`, parentID, computerID, childOwnerID).Scan(&trusted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return trusted, err
}

func writeVariantEnv(ctx context.Context, tx pgx.Tx, box *secrets.Box, org, id, actor string, vars map[string]string) error {
	if len(vars) > 0 && box == nil {
		return ErrSecretsUnavailable
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sealed, err := box.Seal([]byte(vars[name]), secrets.EnvAAD(org, id, name))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO openfox.agent_env_vars(organization_id,agent_id,name,value_ciphertext,value_nonce,key_id,updated_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,$6::uuid)`, id, name, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, actor); err != nil {
			return mapDatabaseError(err)
		}
	}
	return nil
}
func syncVariantEnv(ctx context.Context, tx pgx.Tx, box *secrets.Box, org, from, to, actor string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.agent_env_vars WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid`, to); err != nil {
		return err
	}
	return copyEnvVars(ctx, tx, box, org, from, to, actor)
}
func syncVariantRepos(ctx context.Context, tx pgx.Tx, from, to, actor string) error {
	return syncVariantReposSafe(ctx, tx, from, to, actor, true)
}
func syncVariantReposSafe(ctx context.Context, tx pgx.Tx, from, to, actor string, trusted bool) error {
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.agent_repos WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid`, to); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO openfox.agent_repos(organization_id,agent_id,url,base_branch,position,created_by,connection_id)
		SELECT organization_id,$2::uuid,url,base_branch,position,$3::uuid,connection_id FROM openfox.agent_repos
		WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid AND ($4 OR connection_id IS NULL)`, from, to, actor, trusted)
	return mapDatabaseError(err)
}
func writeVariantRepos(ctx context.Context, tx pgx.Tx, org, id string, actor Actor, repos []RepoInput) error {
	for i, repo := range repos {
		repo.URL, repo.BaseBranch = strings.TrimSpace(repo.URL), strings.TrimSpace(repo.BaseBranch)
		if err := validateRepoFields(repo.URL, repo.BaseBranch, repo.Position); err != nil {
			return err
		}
		if err := reposource.CheckBinding(ctx, tx, repo.ConnectionID, repo.URL, actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin); err != nil {
			return ErrForbidden
		}
		p := i
		if repo.Position != nil {
			p = *repo.Position
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.agent_repos(organization_id,agent_id,url,base_branch,position,created_by,connection_id) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,nullif($7,'')::uuid)`, org, id, repo.URL, repo.BaseBranch, p, actor.UserID, repo.ConnectionID); err != nil {
			return mapRepoError(err)
		}
	}
	return nil
}

// lockVariantParent 在写分身前先锁父行；持锁期间父级不得归档或传播旧配置。
func lockVariantParent(ctx context.Context, tx pgx.Tx, org, id string) error {
	var parent *string
	err := tx.QueryRow(ctx, `SELECT parent_agent_id::text FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid`, org, id).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if parent == nil {
		return nil
	}
	var archived bool
	err = tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid FOR UPDATE`, org, *parent).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVariantParent
	}
	if err != nil {
		return err
	}
	if archived {
		return ErrVariantParent
	}
	return nil
}
func variantGroups(ctx context.Context, tx pgx.Tx, org, id string) (string, []string, error) {
	var parent *string
	var groups []string
	err := tx.QueryRow(ctx, `SELECT parent_agent_id::text,inherited_groups FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid`, org, id).Scan(&parent, &groups)
	if err != nil {
		return "", nil, err
	}
	if parent == nil {
		return "", nil, nil
	}
	return *parent, groups, nil
}

// propagateVariants 只在父行已上锁且本次变更全部持有同一事务时调用。
func (s PostgresStore) propagateVariants(ctx context.Context, tx pgx.Tx, sel tenancy.OrganizationSelection, actor Actor, parentID string, parentCfg *ConfigVersion, changed map[string]bool) error {
	rows, err := tx.Query(ctx, `SELECT id::text,space_id::text,inherited_groups FROM openfox.agents WHERE organization_id=$1::uuid AND parent_agent_id=$2::uuid AND archived_at IS NULL ORDER BY id LIMIT 21`, sel.OrganizationID, parentID)
	if err != nil {
		return err
	}
	type variant struct {
		id, space string
		groups    []string
	}
	items := []variant{}
	for rows.Next() {
		var v variant
		if err = rows.Scan(&v.id, &v.space, &v.groups); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(items) > maxVariants {
		return ErrLimitReached
	}
	for _, v := range items {
		current, err := lockAgent(ctx, tx, sel.OrganizationID, v.space, v.id)
		if err != nil {
			return err
		}
		inherited := groupSet(v.groups)
		shouldWrite := false
		for group := range changed {
			if inherited[group] {
				shouldWrite = true
			}
		}
		if !shouldWrite {
			continue
		}
		own, err := loadAgent(ctx, tx, sel.OrganizationID, v.space, v.id, actor.UserID, false)
		if err != nil {
			return err
		}
		if own.Config == nil {
			return ErrInvalidConfig
		}
		cfg, err := normalizeConfigInput(mergeConfig(configInput(parentCfg), configInput(own.Config), inherited))
		if err != nil {
			return err
		}
		if inherited[GroupComputer] && changed[GroupComputer] {
			if err := grantScopeFor(ctx, tx, cfg.GrantID, v.space, current.OwnerUserID); err != nil {
				// 父级可更换电脑，不让跨空间分身阻断更新；分身保留旧物化值并在派发时拒绝不一致版本。
				cfg.ComputerID, cfg.GrantID = own.Config.ComputerID, own.Config.GrantID
			}
		}
		trusted, err := variantSecretTrust(ctx, tx, parentID, cfg.ComputerID, current.OwnerUserID)
		if err != nil {
			return err
		}
		if inherited[GroupEnv] && changed[GroupEnv] && trusted {
			if err = syncVariantEnv(ctx, tx, s.Secrets, sel.OrganizationID, parentID, v.id, actor.UserID); err != nil {
				return err
			}
		}
		if inherited[GroupRepos] && changed[GroupRepos] {
			if err = syncVariantReposSafe(ctx, tx, parentID, v.id, actor.UserID, trusted); err != nil {
				return err
			}
		}
		digest, err := computeEnvDigest(ctx, tx, s.Secrets, sel.OrganizationID, v.id)
		if err != nil {
			return err
		}
		if err = insertConfigWithDigest(ctx, tx, sel.OrganizationID, v.id, current.CurrentConfigVersion+1, actor.UserID, cfg, digest); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE openfox.agents SET device_id=$3::uuid,current_config_version=current_config_version+1,version=version+1,updated_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid AND version=$4`, sel.OrganizationID, v.id, cfg.ComputerID, current.Version)
		if err != nil {
			return mapDatabaseError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
	}
	return nil
}

// ParentAccessible 校验分身所属父级对当前成员是否仍可读，不影响本人对分身的访问。
func ParentAccessible(ctx context.Context, tx pgx.Tx, org string, child Agent, actor Actor) (bool, error) {
	if child.ParentAgentID == "" {
		return true, nil
	}
	var space, visibility, owner string
	err := tx.QueryRow(ctx, `SELECT p.space_id::text,p.visibility,p.owner_user_id::text FROM openfox.agents p
		JOIN openfox.spaces s ON s.organization_id=p.organization_id AND s.id=p.space_id AND s.status='ACTIVE' AND s.deleted_at IS NULL
		WHERE p.organization_id=$1::uuid AND p.id=$2::uuid AND p.status='ACTIVE' AND p.archived_at IS NULL`, org, child.ParentAgentID).Scan(&space, &visibility, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if visibility != string(VisibilityShared) && owner != actor.UserID {
		return false, nil
	}
	if err := authorizeSpace(ctx, tx, org, space, actor, policy.ActionReadSpace, true); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// redactVariantConfig 不返回已失权父级的继承值，但保留本人覆盖的组和基础资料。
func redactVariantConfig(item *Agent) {
	if item.ParentAgentID == "" || !item.ParentUnavailable || item.Config == nil {
		return
	}
	cfg := *item.Config
	groups := groupSet(item.InheritedGroups)
	if groups[GroupInstructions] {
		cfg.Instructions = ""
	}
	if groups[GroupComputer] {
		cfg.ComputerID, cfg.GrantID = "", ""
		item.ComputerName = nil
		item.Online = false
	}
	if groups[GroupRuntime] {
		cfg.Runtime, cfg.Model, cfg.ModelSource = "", "", ""
	}
	if groups[GroupDirectory] {
		cfg.DirectoryMode, cfg.DirectoryRoots = "", nil
	}
	if groups[GroupLimits] {
		cfg.MaxConcurrency, cfg.QueueTimeoutSeconds, cfg.RunTimeoutSeconds = 0, 0, 0
	}
	if groups[GroupRunSettings] {
		modelSource := cfg.ModelSource
		cfg.RunSettings = RunSettings{}
		cfg.ModelSource = modelSource
	}
	if groups[GroupEnv] {
		cfg.EnvDigest = ""
	}
	item.Config = &cfg
}

func prepareVariantResponse(ctx context.Context, tx pgx.Tx, org string, actor Actor, item *Agent) error {
	if err := attachVariantLinks(ctx, tx, org, actor, item); err != nil {
		return err
	}
	if item.ParentAgentID == "" {
		return nil
	}
	accessible, err := ParentAccessible(ctx, tx, org, *item, actor)
	if err != nil {
		return err
	}
	if !accessible {
		item.Parent = nil
		item.ParentUnavailable = true
		redactVariantConfig(item)
	}
	return nil
}

// attachVariantLinks 只向有目标空间读取权限且 Agent 可见的成员展示父子摘要。
func attachVariantLinks(ctx context.Context, tx pgx.Tx, org string, actor Actor, item *Agent) error {
	if item.ParentAgentID != "" {
		parent, err := loadAgentByID(ctx, tx, org, item.ParentAgentID, actor.UserID)
		if err == nil && authorizeSpace(ctx, tx, org, parent.SpaceID, actor, policy.ActionReadSpace, true) == nil {
			item.Parent = &AgentLink{ID: parent.ID, SpaceID: parent.SpaceID, Name: parent.Name, Archived: parent.ArchivedAt != nil}
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text,space_id::text,name,archived_at IS NOT NULL,owner_user_id::text,visibility,inherited_groups FROM openfox.agents WHERE organization_id=$1::uuid AND parent_agent_id=$2::uuid ORDER BY id LIMIT 21`, org, item.ID)
	if err != nil {
		return err
	}
	type raw struct {
		link              AgentLink
		owner, visibility string
	}
	list := []raw{}
	for rows.Next() {
		var v raw
		if err = rows.Scan(&v.link.ID, &v.link.SpaceID, &v.link.Name, &v.link.Archived, &v.owner, &v.visibility, &v.link.InheritedGroups); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, v := range list {
		if (v.visibility == "SHARED" || v.owner == actor.UserID) && authorizeSpace(ctx, tx, org, v.link.SpaceID, actor, policy.ActionReadSpace, true) == nil {
			item.Variants = append(item.Variants, v.link)
		}
	}
	return nil
}

// afterGroupMutation 使分身解除该组继承，或在父级变更后原子同步尚未覆盖的分身。
func (s PostgresStore) afterGroupMutation(ctx context.Context, tx pgx.Tx, sel tenancy.OrganizationSelection, actor Actor, agent Agent, group string) error {
	if agent.ParentAgentID != "" {
		remaining := make([]string, 0, len(agent.InheritedGroups))
		for _, g := range agent.InheritedGroups {
			if g != group {
				remaining = append(remaining, g)
			}
		}
		if len(remaining) == len(agent.InheritedGroups) {
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE openfox.agents SET inherited_groups=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, sel.OrganizationID, agent.ID, remaining)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	}
	parent, err := loadAgent(ctx, tx, sel.OrganizationID, agent.SpaceID, agent.ID, actor.UserID, false)
	if err != nil {
		return err
	}
	if parent.Config == nil {
		return ErrInvalidConfig
	}
	return s.propagateVariants(ctx, tx, sel, actor, agent.ID, parent.Config, map[string]bool{group: true})
}

// ResetInheritance 将指定配置组重新从父级物化，重复恢复返回当前版本，不生成空版本。
func (s PostgresStore) ResetInheritance(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, id string, groups []string) (Agent, error) {
	space, id, err := s.agentRef(sel, actor, space, id)
	if err != nil {
		return Agent{}, err
	}
	if len(groups) == 0 || len(groups) > len(VariantGroups) {
		return Agent{}, ErrInvalidRequest
	}
	selected := groupSet(groups)
	if len(selected) != len(groups) {
		return Agent{}, ErrInvalidRequest
	}
	for g := range selected {
		if !groupSet(VariantGroups)[g] {
			return Agent{}, ErrInvalidRequest
		}
	}
	var result Agent
	err = s.run(ctx, sel, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockVariantParent(ctx, tx, sel.OrganizationID, id); err != nil {
			return err
		}
		child, err := lockEditableAgent(ctx, tx, sel.OrganizationID, space, id, actor, false)
		if err != nil {
			return err
		}
		if child.ParentAgentID == "" {
			return ErrVariantParent
		}
		parent, err := loadAgentByID(ctx, tx, sel.OrganizationID, child.ParentAgentID, actor.UserID)
		if err != nil {
			return err
		}
		if parent.Config == nil || parent.ArchivedAt != nil {
			return ErrVariantParent
		}
		remaining := groupSet(child.InheritedGroups)
		changed := false
		for _, g := range groups {
			if !remaining[g] {
				remaining[g] = true
				changed = true
			}
		}
		if !changed {
			result, err = loadAgent(ctx, tx, sel.OrganizationID, space, id, actor.UserID, false)
			if err != nil {
				return err
			}
			return prepareVariantResponse(ctx, tx, sel.OrganizationID, actor, &result)
		}
		own, err := loadAgent(ctx, tx, sel.OrganizationID, space, id, actor.UserID, false)
		if err != nil {
			return err
		}
		if own.Config == nil {
			return ErrInvalidConfig
		}
		cfg, err := normalizeConfigInput(mergeConfig(configInput(parent.Config), configInput(own.Config), selected))
		if err != nil {
			return err
		}
		if selected[GroupComputer] {
			if err := grantScopeFor(ctx, tx, cfg.GrantID, space, child.OwnerUserID); err != nil {
				return ErrVariantComputer
			}
		}
		trusted, err := variantSecretTrust(ctx, tx, parent.ID, cfg.ComputerID, child.OwnerUserID)
		if err != nil {
			return err
		}
		if selected[GroupEnv] && trusted {
			if err := syncVariantEnv(ctx, tx, s.Secrets, sel.OrganizationID, parent.ID, id, actor.UserID); err != nil {
				return err
			}
		}
		if selected[GroupRepos] {
			if err := syncVariantReposSafe(ctx, tx, parent.ID, id, actor.UserID, trusted); err != nil {
				return err
			}
		}
		digest, err := computeEnvDigest(ctx, tx, s.Secrets, sel.OrganizationID, id)
		if err != nil {
			return err
		}
		if err = insertConfigWithDigest(ctx, tx, sel.OrganizationID, id, child.CurrentConfigVersion+1, actor.UserID, cfg, digest); err != nil {
			return err
		}
		ordered := []string{}
		for _, g := range VariantGroups {
			if remaining[g] {
				ordered = append(ordered, g)
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE openfox.agents SET inherited_groups=$3,device_id=$4::uuid,current_config_version=current_config_version+1,version=version+1,updated_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid AND version=$5`, sel.OrganizationID, id, ordered, cfg.ComputerID, child.Version)
		if err != nil {
			return mapDatabaseError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if err = recordAudit(ctx, tx, sel, actor, "agent.inheritance_reset", id, space, child.Version+1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, sel.OrganizationID, space, id, actor.UserID, false)
		if err != nil {
			return err
		}
		return prepareVariantResponse(ctx, tx, sel.OrganizationID, actor, &result)
	})
	return result, err
}

// Settings omitted by a client preserve the current version; inheritance compares
// effective values rather than treating an omitted field as an explicit override.
func resolveOmittedVariantSettings(previous, next ConfigInput) ConfigInput {
	if next.WorkingHours == nil {
		next.WorkingHours = previous.WorkingHours
	}
	if next.ModelSource == "" {
		next.ModelSource = "EXPLICIT"
		if strings.TrimSpace(next.Model) == previous.Model && previous.ModelSource != "" {
			next.ModelSource = previous.ModelSource
		}
	}
	return next
}
