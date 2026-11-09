package integrations

// 仓库 ↔ 空间同步关联的管理（列仓库、新建、修改、移除、重试）。
// 调用 GitHub 的请求由 API 层放在事务外：先用 Prepare* 在短事务里读出令牌，发请求，再用写入方法落库。

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
)

const (
	DirectionInbound       = "INBOUND"
	DirectionBidirectional = "BIDIRECTIONAL"

	// 唯一约束名（000045 的旧索引与 000049 的新索引），按名称区分冲突原因。
	idxLegacyUnique   = "integration_sync_links_unique"
	idxRepoUnique     = "integration_sync_links_repo_unique"
	idxBidirectionalU = "integration_sync_links_bidirectional_unique"
)

var (
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	// repositorySegment 是 GitLab 多级路径里的一段；整体层数在 ValidRepositoryNameFor 里限制（避免嵌套重复超出 RE2 的展开上限）。
	repositorySegment = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

// ValidRepositoryName 校验 owner/name 形式（GitHub）。
func ValidRepositoryName(value string) bool {
	return len(value) <= 255 && repositoryName.MatchString(value)
}

// ValidRepositoryNameFor 按服务商校验仓库名：GitLab 系允许多级群组。
func ValidRepositoryNameFor(provider, value string) bool {
	if IsGitLabFamily(provider) {
		parts := strings.Split(value, "/")
		if len(value) > 255 || len(parts) < 2 || len(parts) > 20 {
			return false
		}
		for _, part := range parts {
			if !repositorySegment.MatchString(part) || part == "." || part == ".." {
				return false
			}
		}
		return true
	}
	return ValidRepositoryName(value)
}

// SyncBackfill 对应契约 IntegrationSyncLink.backfill。
type SyncBackfill struct {
	Status   string `json:"status"`
	Imported int    `json:"imported"`
	Error    string `json:"error"`
}

// SyncOutbound 对应契约 IntegrationSyncLink.outbound。
type SyncOutbound struct {
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// LinkedSpace 是仓库已关联到的空间。
type LinkedSpace struct {
	SpaceID   string
	SpaceName string
}

// RepositoryItem 对应列仓库接口的一项。
type RepositoryItem struct {
	FullName        string  `json:"full_name"`
	Name            string  `json:"name"`
	Private         bool    `json:"private"`
	HTMLURL         string  `json:"html_url"`
	LinkedSpaceID   *string `json:"linked_space_id"`
	LinkedSpaceName *string `json:"linked_space_name"`
}

// RepositoryTarget 是列仓库需要的信息（令牌解密后只在内存里使用）。
type RepositoryTarget struct {
	Client      gitapi.Client
	Token       string
	Login       string
	UserAccount bool
	Linked      map[string]LinkedSpace
}

// Annotate 给服务商返回的仓库打上已关联的标记。
func (t RepositoryTarget) Annotate(repos []gitapi.Repo) []RepositoryItem {
	items := make([]RepositoryItem, 0, len(repos))
	for _, repo := range repos {
		item := RepositoryItem{FullName: repo.FullName, Name: repo.Name, Private: repo.Private, HTMLURL: repo.HTMLURL}
		if linked, ok := t.Linked[strings.ToLower(repo.FullName)]; ok {
			id, name := linked.SpaceID, linked.SpaceName
			item.LinkedSpaceID, item.LinkedSpaceName = &id, &name
		}
		items = append(items, item)
	}
	return items
}

type orgConnection struct {
	InstanceToken bool
	Client        gitapi.Client
	Provider      string
	Token         string
	ExternalID    string
	UserAccount   bool
}

// loadOrgConnection 读取未断开的 Git 服务商组织连接（GitHub / GitLab / 极狐），解密令牌并按连接构造客户端。
// requireActive 为 true 时只接受 ACTIVE 的连接（回链评论用）。
func (s Service) loadOrgConnection(ctx context.Context, tx pgx.Tx, organizationID, connectionID string, requireActive bool) (orgConnection, error) {
	if !membership.ValidateUUID(connectionID) {
		return orgConnection{}, ErrNotFound
	}
	var provider, externalID, kind string
	var settingsRaw, sealed []byte
	err := tx.QueryRow(ctx, `
		SELECT provider, external_id, account_kind, settings, credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND provider IN `+gitProviderIn+` AND scope = 'ORGANIZATION'
		   AND (($2::boolean AND status = 'ACTIVE') OR (NOT $2::boolean AND status <> 'DISCONNECTED'))`, connectionID, requireActive).
		Scan(&provider, &externalID, &kind, &settingsRaw, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return orgConnection{}, ErrNotFound
	}
	if err != nil {
		return orgConnection{}, err
	}
	inst, _, err := s.instanceFrom(organizationID, connectionID, settingsRaw, sealed)
	if err != nil {
		return orgConnection{}, err
	}
	creds, err := s.openCredentials(organizationID, connectionID, sealed)
	if err != nil {
		return orgConnection{}, err
	}
	client, err := s.gitClient(provider, inst)
	if err != nil {
		return orgConnection{}, err
	}
	return orgConnection{InstanceToken: inst.PersonalToken, Client: client, Provider: provider, Token: creds.AccessToken, ExternalID: externalID, UserAccount: kind == "user"}, nil
}

func (s Service) requireManager(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !CanManage(actor.Role) {
		return ErrForbidden
	}
	return nil
}

// PrepareRepositories 在短事务里读出令牌与已关联的仓库（只有所有者 / 管理员）。
func (s Service) PrepareRepositories(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, connectionID string) (RepositoryTarget, error) {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return RepositoryTarget{}, err
	}
	conn, err := s.loadOrgConnection(ctx, tx, organizationID, connectionID, false)
	if err != nil {
		return RepositoryTarget{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT lower(l.external_project_id), l.space_id::text, sp.name
		  FROM openfox.integration_sync_links l
		  JOIN openfox.spaces sp ON sp.organization_id = l.organization_id AND sp.id = l.space_id
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.connection_id = $1::uuid AND l.deleted_at IS NULL
		 LIMIT $2`, connectionID, maxDetailRows)
	if err != nil {
		return RepositoryTarget{}, err
	}
	defer rows.Close()
	target := RepositoryTarget{Client: conn.Client, Token: conn.Token, Login: conn.ExternalID, UserAccount: conn.UserAccount, Linked: map[string]LinkedSpace{}}
	for rows.Next() {
		var name string
		var linked LinkedSpace
		if err := rows.Scan(&name, &linked.SpaceID, &linked.SpaceName); err != nil {
			return RepositoryTarget{}, err
		}
		target.Linked[name] = linked
	}
	return target, rows.Err()
}

// CreateSyncInput 是新建同步的请求。
type CreateSyncInput struct {
	ConnectionID   string
	Repository     string
	SpaceID        string
	Direction      string
	ImportExisting bool
}

// Valid 校验请求字段的形状（仓库名的具体格式随服务商不同，在 PrepareCreateSync 里按连接的服务商再校验）。
func (in CreateSyncInput) Valid() bool {
	return membership.ValidateUUID(in.ConnectionID) && membership.ValidateUUID(in.SpaceID) &&
		len(in.Repository) <= 255 && strings.Contains(in.Repository, "/") &&
		(in.Direction == DirectionInbound || in.Direction == DirectionBidirectional)
}

// CreateSyncTarget 是新建同步需要的信息：客户端、令牌与连接账号 login（用来校验仓库所有者）。
type CreateSyncTarget struct {
	InstanceToken bool
	Client        gitapi.Client
	Token         string
	Owner         string
}

// PrepareCreateSync 在短事务里校验权限与连接，返回客户端、令牌与连接账号 login。
func (s Service) PrepareCreateSync(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, in CreateSyncInput) (CreateSyncTarget, error) {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return CreateSyncTarget{}, err
	}
	if !in.Valid() {
		return CreateSyncTarget{}, ErrInvalidRequest
	}
	conn, err := s.loadOrgConnection(ctx, tx, organizationID, in.ConnectionID, false)
	if err != nil {
		return CreateSyncTarget{}, err
	}
	if !ValidRepositoryNameFor(conn.Provider, in.Repository) {
		return CreateSyncTarget{}, ErrInvalidRequest
	}
	return CreateSyncTarget{InstanceToken: conn.InstanceToken, Client: conn.Client, Token: conn.Token, Owner: conn.ExternalID}, nil
}

// InsertSyncLink 在服务商校验通过后落库。id 与空间授权用的对象 ID 相同。
func (s Service) InsertSyncLink(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, in CreateSyncInput, repo gitapi.Repo) (SyncLink, error) {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return SyncLink{}, err
	}
	var connectionOK bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.integration_connections
		                WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		                  AND provider IN `+gitProviderIn+` AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED')`, in.ConnectionID).Scan(&connectionOK); err != nil {
		return SyncLink{}, err
	}
	if !connectionOK {
		return SyncLink{}, ErrNotFound
	}
	backfill := map[string]any{"status": "none", "imported": 0, "error": "", "cursor": 0}
	if in.ImportExisting {
		backfill["status"] = "running"
	}
	settings, _ := json.Marshal(map[string]any{"repository_url": repo.HTMLURL, "repository_private": repo.Private, "backfill": backfill})
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.integration_sync_links (organization_id, id, connection_id, space_id, external_project_id, external_project_name,
		       direction, settings, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8::uuid)`,
		id, in.ConnectionID, in.SpaceID, strings.ToLower(repo.FullName), repo.FullName, in.Direction, settings, actor.UserID)
	if err != nil {
		return SyncLink{}, mapSyncUnique(err)
	}
	return loadSyncLink(ctx, tx, id)
}

// mapSyncUnique 按唯一索引名区分冲突原因。
func mapSyncUnique(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		switch pg.ConstraintName {
		case idxRepoUnique, idxLegacyUnique:
			return ErrSyncLinkExists
		case idxBidirectionalU:
			return ErrSyncBidirectionalExists
		}
	}
	return err
}

// UpdateSyncInput 是修改同步的请求；Direction 与 Status 至少给一个。
type UpdateSyncInput struct {
	Version   uint64
	Direction string
	Status    string
}

// UpdateSyncLink 修改方向或暂停 / 恢复（版本号 CAS）。
func (s Service) UpdateSyncLink(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, in UpdateSyncInput) (SyncLink, error) {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return SyncLink{}, err
	}
	if !membership.ValidateUUID(id) {
		return SyncLink{}, ErrNotFound
	}
	if in.Version == 0 || (in.Direction == "" && in.Status == "") ||
		(in.Direction != "" && in.Direction != DirectionInbound && in.Direction != DirectionBidirectional) ||
		(in.Status != "" && in.Status != "ACTIVE" && in.Status != "PAUSED") {
		return SyncLink{}, ErrInvalidRequest
	}
	if in.Status == "ACTIVE" {
		var disconnected bool
		err := tx.QueryRow(ctx, `
			SELECT c.status = 'DISCONNECTED' FROM openfox.integration_sync_links l
			  JOIN openfox.integration_connections c ON c.organization_id = l.organization_id AND c.id = l.connection_id
			 WHERE l.organization_id = openfox_private.current_organization_id() AND l.id = $1::uuid AND l.deleted_at IS NULL`, id).Scan(&disconnected)
		if err == nil && disconnected {
			return SyncLink{}, ErrInvalidRequest
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_links
		   SET direction = COALESCE(NULLIF($3, ''), direction), status = COALESCE(NULLIF($4, ''), status),
		       status_reason = CASE WHEN $4 = 'PAUSED' THEN 'paused by user' WHEN $4 = 'ACTIVE' THEN '' ELSE status_reason END,
		       updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL AND version = $2`,
		id, int64(in.Version), in.Direction, in.Status)
	if err != nil {
		return SyncLink{}, mapSyncUnique(err)
	}
	if tag.RowsAffected() != 1 {
		return SyncLink{}, s.syncLinkMissOrStale(ctx, tx, id)
	}
	return loadSyncLink(ctx, tx, id)
}

func (s Service) syncLinkMissOrStale(ctx context.Context, tx pgx.Tx, id string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.integration_sync_links
		                WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrVersionConflict
}

// RemoveSyncLink 软删除同步关联：解除其下的任务 ↔ Issue 关联，未发送的出站项改为 SKIPPED。任务与 Issue 都保留。
func (s Service) RemoveSyncLink(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, version uint64) error {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(id) {
		return ErrNotFound
	}
	if version == 0 {
		return ErrInvalidRequest
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_links
		   SET deleted_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL AND version = $2`,
		id, int64(version))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return s.syncLinkMissOrStale(ctx, tx, id)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_issue_links SET unlinked_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND sync_link_id = $1::uuid AND unlinked_at IS NULL`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.integration_sync_outbox
		   SET status = 'SKIPPED', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL,
		       last_error = 'sync link removed', updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND sync_link_id = $1::uuid AND status IN ('PENDING', 'SENDING')`, id)
	return err
}

// RetrySyncLink 把该关联下发送失败的出站项改回 PENDING，返回重新排队的数量。
// 同一任务同类（ISSUE_CREATE / ISSUE_UPDATE）只保留最新一条失败项，其余改为 SKIPPED；已有待发送同类项时失败项也改为 SKIPPED（部分唯一索引不允许并存）。
func (s Service) RetrySyncLink(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (int, error) {
	if err := s.requireManager(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	if !membership.ValidateUUID(id) {
		return 0, ErrNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.integration_sync_links
		                WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_outbox f
		   SET status = 'SKIPPED', last_error = 'superseded', updated_at = clock_timestamp()
		 WHERE f.organization_id = openfox_private.current_organization_id() AND f.sync_link_id = $1::uuid
		   AND f.status = 'FAILED' AND f.kind IN ('ISSUE_CREATE', 'ISSUE_UPDATE')
		   AND (EXISTS (SELECT 1 FROM openfox.integration_sync_outbox q
		                 WHERE q.organization_id = f.organization_id AND q.task_id = f.task_id AND q.kind = f.kind
		                   AND q.status IN ('PENDING', 'SENDING'))
		        OR EXISTS (SELECT 1 FROM openfox.integration_sync_outbox n
		                    WHERE n.organization_id = f.organization_id AND n.task_id = f.task_id AND n.kind = f.kind
		                      AND n.status = 'FAILED' AND (n.created_at, n.id) > (f.created_at, f.id)))`, id); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.integration_sync_outbox
		   SET status = 'PENDING', attempts = 0, next_attempt_at = clock_timestamp(), finished_at = NULL, last_error = '',
		       lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND sync_link_id = $1::uuid AND status = 'FAILED'`, id)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---------- 读取 ----------

const syncLinkColumns = `
	l.id::text, l.connection_id::text, l.space_id::text, s.name, l.external_project_id, l.external_project_name,
	l.direction, l.status, l.status_reason, l.created_at, l.version,
	COALESCE(l.settings->>'repository_url', ''), COALESCE((l.settings->>'repository_private')::boolean, false), l.last_synced_at,
	COALESCE(l.settings #>> '{backfill,status}', 'none'), COALESCE((l.settings #>> '{backfill,imported}')::integer, 0),
	COALESCE(l.settings #>> '{backfill,error}', ''), COALESCE(o.pending, 0), COALESCE(o.failed, 0)`

// 出站项按关联批量计数（一次聚合，没有 N+1）；只统计待发送与失败的行。
const syncLinkFrom = `
	  FROM openfox.integration_sync_links l
	  JOIN openfox.integration_connections c ON c.organization_id = l.organization_id AND c.id = l.connection_id
	  JOIN openfox.spaces s ON s.organization_id = l.organization_id AND s.id = l.space_id
	  LEFT JOIN (
	       SELECT sync_link_id, count(*) FILTER (WHERE status IN ('PENDING', 'SENDING'))::integer AS pending,
	              count(*) FILTER (WHERE status = 'FAILED')::integer AS failed
	         FROM openfox.integration_sync_outbox
	        WHERE organization_id = openfox_private.current_organization_id() AND status IN ('PENDING', 'SENDING', 'FAILED')
	        GROUP BY sync_link_id) o ON o.sync_link_id = l.id`

func scanSyncLink(row pgx.Row) (SyncLink, error) {
	var item SyncLink
	var version int64
	err := row.Scan(&item.ID, &item.ConnectionID, &item.SpaceID, &item.SpaceName, &item.ExternalProjectID, &item.ExternalProjectName,
		&item.Direction, &item.Status, &item.StatusReason, &item.CreatedAt, &version,
		&item.RepositoryURL, &item.RepositoryPrivate, &item.LastSyncedAt,
		&item.Backfill.Status, &item.Backfill.Imported, &item.Backfill.Error, &item.Outbound.Pending, &item.Outbound.Failed)
	item.Version = uint64(version)
	return item, err
}

func loadSyncLink(ctx context.Context, tx pgx.Tx, id string) (SyncLink, error) {
	item, err := scanSyncLink(tx.QueryRow(ctx, `SELECT`+syncLinkColumns+syncLinkFrom+`
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.id = $1::uuid AND l.deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncLink{}, ErrNotFound
	}
	return item, err
}
