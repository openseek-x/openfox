package collaboration

// 空间绑定的 Git 仓库（迁移 000033 space_repos）。
// 权限由接口层判定（读 = 空间可读，写 = 空间管理）；这里只做数据规则。

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/reposource"
)

// MaxSpaceRepos 是每个空间的仓库上限。
const MaxSpaceRepos = 20

// 与 space_repos_url_valid / space_repos_branch_valid 约束一致（规则同 agent_repos）。
var (
	spaceRepoHTTPSPattern = regexp.MustCompile(`^https://[^\s]+$`)
	spaceRepoSSHPattern   = regexp.MustCompile(`^(ssh://)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/][^\s]+$`)
)

var ErrRepoExists = NewRuleError(http.StatusConflict, "REPO_EXISTS", "the repository is already linked to this space")

type SpaceRepo struct {
	ConnectionID string    `json:"connection_id,omitempty"`
	ID           string    `json:"id"`
	SpaceID      string    `json:"space_id"`
	URL          string    `json:"url"`
	Description  string    `json:"description"`
	BaseBranch   string    `json:"base_branch"`
	Position     int       `json:"position"`
	Version      uint64    `json:"version"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type SpaceRepoInput struct {
	ConnectionID string
	URL          string
	Description  string
	BaseBranch   string
}

type SpaceRepoPatch struct {
	ConnectionID *string
	URL          *string
	Description  *string
	BaseBranch   *string
	Position     *int
	Version      uint64
}

const spaceRepoColumns = `COALESCE(connection_id::text,''), id::text, space_id::text, url, description, base_branch, position, version,
	created_by::text, created_at, updated_at`

func scanSpaceRepo(row interface{ Scan(...any) error }) (SpaceRepo, error) {
	var item SpaceRepo
	var version int64
	if err := row.Scan(&item.ConnectionID, &item.ID, &item.SpaceID, &item.URL, &item.Description, &item.BaseBranch, &item.Position,
		&version, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return SpaceRepo{}, err
	}
	var err error
	item.Version, err = positiveVersion(version)
	return item, err
}

func validSpaceRepoURL(url string) bool {
	return url != "" && len(url) <= 2048 && (spaceRepoHTTPSPattern.MatchString(url) || spaceRepoSSHPattern.MatchString(url))
}

// normalizeSpaceRepoFields 去掉首尾空白并校验；返回规范化后的值。
func normalizeSpaceRepoFields(url, description, branch string) (string, string, string, error) {
	url = strings.TrimSpace(url)
	description = strings.TrimSpace(description)
	branch = strings.TrimSpace(branch)
	if !validSpaceRepoURL(url) {
		return "", "", "", invalidInput("url must be https://… or git@host:path")
	}
	if len([]rune(description)) > 500 {
		return "", "", "", invalidInput("description is too long")
	}
	if len(branch) > 255 || strings.ContainsAny(branch, " \t\r\n\v\f") {
		return "", "", "", invalidInput("base_branch is invalid")
	}
	return url, description, branch, nil
}

func spaceRepoDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrRepoExists
		case "23503", "23514":
			return invalidInput("repository fields violate a constraint")
		}
	}
	return err
}

// lockSpaceForRepos 以 FOR NO KEY UPDATE 锁定空间行（不阻塞引用空间的外键检查），把同一空间的设置写入串行化。
func lockSpaceForRepos(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND deleted_at IS NULL
		   FOR NO KEY UPDATE`, spaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	return err
}

func (s Service) ListSpaceRepos(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) ([]SpaceRepo, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(spaceID) {
		return nil, ErrSpaceNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT `+spaceRepoColumns+`
		  FROM openfox.space_repos
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		 ORDER BY position, created_at, id
		 LIMIT $2`, spaceID, MaxSpaceRepos*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SpaceRepo, 0)
	for rows.Next() {
		item, err := scanSpaceRepo(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) CreateSpaceRepo(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input SpaceRepoInput) (SpaceRepo, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceRepo{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return SpaceRepo{}, ErrSpaceNotFound
	}
	url, description, branch, err := normalizeSpaceRepoFields(input.URL, input.Description, input.BaseBranch)
	if err != nil {
		return SpaceRepo{}, err
	}
	if err := reposource.CheckBinding(ctx, tx, input.ConnectionID, url, actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin); err != nil {
		return SpaceRepo{}, ErrActionForbidden
	}
	// 先锁空间行，再计数与取末尾位置，避免并发添加突破上限或拿到相同 position。
	if err := lockSpaceForRepos(ctx, tx, spaceID); err != nil {
		return SpaceRepo{}, err
	}
	var count, nextPosition int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(position) + 1, 0) FROM openfox.space_repos
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`,
		spaceID).Scan(&count, &nextPosition); err != nil {
		return SpaceRepo{}, err
	}
	if count >= MaxSpaceRepos {
		return SpaceRepo{}, ErrLimitReached
	}
	if nextPosition > 63 {
		// 位置约束为 0..63；删改多次后可能接近上限，此时压缩到计数值。
		nextPosition = count
	}
	item, err := scanSpaceRepo(tx.QueryRow(ctx, `
		INSERT INTO openfox.space_repos (organization_id, space_id, url, description, base_branch, position, created_by, connection_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6::uuid, nullif($7,'')::uuid)
		RETURNING `+spaceRepoColumns, spaceID, url, description, branch, nextPosition, actor.UserID, input.ConnectionID))
	if err != nil {
		return SpaceRepo{}, spaceRepoDatabaseError(err)
	}
	return item, nil
}

func (s Service) UpdateSpaceRepo(ctx context.Context, tx pgx.Tx, organizationID, spaceID, repoID string, actor Actor, patch SpaceRepoPatch) (SpaceRepo, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceRepo{}, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(repoID) {
		return SpaceRepo{}, configNotFound("repository not found")
	}
	if patch.Version == 0 {
		return SpaceRepo{}, ErrInvalidVersion
	}
	current, err := scanSpaceRepo(tx.QueryRow(ctx, `
		SELECT `+spaceRepoColumns+` FROM openfox.space_repos
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid
		   FOR UPDATE`, spaceID, repoID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceRepo{}, configNotFound("repository not found")
	}
	if err != nil {
		return SpaceRepo{}, err
	}
	if current.Version != patch.Version {
		return SpaceRepo{}, ErrVersionConflict
	}
	url, description, branch, position := current.URL, current.Description, current.BaseBranch, current.Position
	if patch.URL != nil {
		url = *patch.URL
	}
	if patch.Description != nil {
		description = *patch.Description
	}
	if patch.BaseBranch != nil {
		branch = *patch.BaseBranch
	}
	if patch.Position != nil {
		if *patch.Position < 0 || *patch.Position > 63 {
			return SpaceRepo{}, invalidInput("position must be between 0 and 63")
		}
		position = *patch.Position
	}
	url, description, branch, err = normalizeSpaceRepoFields(url, description, branch)
	if err != nil {
		return SpaceRepo{}, err
	}
	connection := current.ConnectionID
	if patch.ConnectionID != nil {
		connection = *patch.ConnectionID
	}
	if connection != current.ConnectionID || url != current.URL {
		if err := reposource.CheckBinding(ctx, tx, connection, url, actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin); err != nil {
			return SpaceRepo{}, ErrActionForbidden
		}
	}
	item, err := scanSpaceRepo(tx.QueryRow(ctx, `
		UPDATE openfox.space_repos
		   SET connection_id=nullif($8,'')::uuid, url = $3, description = $4, base_branch = $5, position = $6,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $7
		RETURNING `+spaceRepoColumns, spaceID, repoID, url, description, branch, position, int64(patch.Version), connection))
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceRepo{}, ErrVersionConflict
	}
	if err != nil {
		return SpaceRepo{}, spaceRepoDatabaseError(err)
	}
	return item, nil
}

func (s Service) DeleteSpaceRepo(ctx context.Context, tx pgx.Tx, organizationID, spaceID, repoID string, actor Actor, version uint64) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(repoID) {
		return configNotFound("repository not found")
	}
	if version == 0 {
		return ErrInvalidVersion
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.space_repos
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $3`, spaceID, repoID, int64(version))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.space_repos
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid)`,
		spaceID, repoID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return configNotFound("repository not found")
	}
	return ErrVersionConflict
}
