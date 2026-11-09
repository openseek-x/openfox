package agents

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/idempotency"
	"openfox/internal/policy"
	"openfox/internal/reposource"
	"openfox/internal/tenancy"
)

// Agent 的 Git 仓库（迁移 000028 agent_repos）：为空时继承空间设置；派发时读取当前值，不固定到配置版本。

// MaxRepos 是每个 Agent 的仓库上限。
const MaxRepos = 20

// 与 agent_repos_url_valid / agent_repos_branch_valid 约束一致。
var (
	repoHTTPSPattern = regexp.MustCompile(`^https://[^\s]+$`)
	repoSSHPattern   = regexp.MustCompile(`^(ssh://)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/][^\s]+$`)
)

type Repo struct {
	ConnectionID string    `json:"connection_id,omitempty"`
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	URL          string    `json:"url"`
	BaseBranch   string    `json:"base_branch"`
	Position     int       `json:"position"`
	Version      uint64    `json:"version"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RepoInput struct {
	ConnectionID string `json:"connection_id,omitempty"`
	URL          string `json:"url"`
	BaseBranch   string `json:"base_branch"`
	Position     *int   `json:"position"`
}

type RepoPatch struct {
	ConnectionID *string
	URL          *string
	BaseBranch   *string
	Position     *int
	Version      uint64
}

const repoColumns = `COALESCE(connection_id::text,''), id::text, agent_id::text, url, base_branch, position, version, created_by::text, created_at, updated_at`

func scanRepo(row rowScanner) (Repo, error) {
	var item Repo
	var version int64
	if err := row.Scan(&item.ConnectionID, &item.ID, &item.AgentID, &item.URL, &item.BaseBranch, &item.Position, &version,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Repo{}, err
	}
	item.Version = uint64(version)
	return item, nil
}

func (s PostgresStore) ListRepos(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) ([]Repo, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return nil, err
	}
	result := make([]Repo, 0)
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := requireReadableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+repoColumns+` FROM openfox.agent_repos
			 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid
			 ORDER BY position, created_at, id
			 LIMIT $2`, agentID, MaxRepos+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRepo(rows)
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

// CreateRepo 添加仓库；idempotencyKey 非空时按 agent_repo.create 幂等，重放返回 replay=true。
func (s PostgresStore) CreateRepo(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, input RepoInput, idempotencyKey string) (Repo, bool, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return Repo{}, false, err
	}
	input.URL, input.BaseBranch = strings.TrimSpace(input.URL), strings.TrimSpace(input.BaseBranch)
	if err := validateRepoFields(input.URL, input.BaseBranch, input.Position); err != nil {
		return Repo{}, false, err
	}
	var result Repo
	var replay bool
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "agent_repo.create", idempotencyKey, struct {
			AgentID string    `json:"agent_id"`
			Input   RepoInput `json:"input"`
		}{agentID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replay = true
			result, err = loadRepo(ctx, tx, agentID, claim.ResourceID)
			return err
		}
		if err := reposource.CheckBinding(ctx, tx, input.ConnectionID, input.URL, actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin); err != nil {
			return ErrForbidden
		}
		var count, nextPosition int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), COALESCE(max(position) + 1, 0) FROM openfox.agent_repos
			 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid`,
			agentID).Scan(&count, &nextPosition); err != nil {
			return err
		}
		if count >= MaxRepos {
			return ErrLimitReached
		}
		position := min(nextPosition, 63)
		if input.Position != nil {
			position = *input.Position
		}
		result, err = scanRepo(tx.QueryRow(ctx, `
			INSERT INTO openfox.agent_repos (organization_id, agent_id, url, base_branch, position, created_by, connection_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5::uuid, nullif($6,'')::uuid)
			RETURNING `+repoColumns, agentID, input.URL, input.BaseBranch, position, actor.UserID, input.ConnectionID))
		if err != nil {
			return mapRepoError(err)
		}
		if err := idempotency.Complete(ctx, tx, claim, result.ID); err != nil {
			return err
		}
		return recordAudit(ctx, tx, selection, actor, "agent.repo_created", agentID, spaceID, 1)
	})
	return result, replay, err
}

func (s PostgresStore) UpdateRepo(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID, repoID string, patch RepoPatch) (Repo, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return Repo{}, err
	}
	repoID, err = normalizeUUID(repoID)
	if err != nil {
		return Repo{}, ErrResourceNotFound
	}
	if patch.Version == 0 {
		return Repo{}, ErrInvalidRequest
	}
	var result Repo
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false); err != nil {
			return err
		}
		current, err := loadRepo(ctx, tx, agentID, repoID)
		if err != nil {
			return err
		}
		if current.Version != patch.Version {
			return ErrVersionConflict
		}
		url, branch, position := current.URL, current.BaseBranch, current.Position
		if patch.URL != nil {
			url = strings.TrimSpace(*patch.URL)
		}
		if patch.BaseBranch != nil {
			branch = strings.TrimSpace(*patch.BaseBranch)
		}
		if patch.Position != nil {
			position = *patch.Position
		}
		if err := validateRepoFields(url, branch, &position); err != nil {
			return err
		}
		connection := current.ConnectionID
		if patch.ConnectionID != nil {
			connection = *patch.ConnectionID
		}
		if connection != current.ConnectionID || url != current.URL {
			if err := reposource.CheckBinding(ctx, tx, connection, url, actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin); err != nil {
				return ErrForbidden
			}
		}
		result, err = scanRepo(tx.QueryRow(ctx, `
			UPDATE openfox.agent_repos
			   SET connection_id=nullif($7,'')::uuid, url = $3, base_branch = $4, position = $5, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND agent_id = $1::uuid AND id = $2::uuid AND version = $6
			RETURNING `+repoColumns, agentID, repoID, url, branch, position, int64(patch.Version), connection))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapRepoError(err)
		}
		return recordAudit(ctx, tx, selection, actor, "agent.repo_updated", agentID, spaceID, result.Version)
	})
	return result, err
}

func (s PostgresStore) DeleteRepo(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID, repoID string) error {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return err
	}
	repoID, err = normalizeUUID(repoID)
	if err != nil {
		return ErrResourceNotFound
	}
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM openfox.agent_repos
			 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid AND id = $2::uuid`,
			agentID, repoID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrResourceNotFound
		}
		return recordAudit(ctx, tx, selection, actor, "agent.repo_deleted", agentID, spaceID, 1)
	})
}

func loadRepo(ctx context.Context, tx pgx.Tx, agentID, repoID string) (Repo, error) {
	item, err := scanRepo(tx.QueryRow(ctx, `
		SELECT `+repoColumns+` FROM openfox.agent_repos
		 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid AND id = $2::uuid`,
		agentID, repoID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Repo{}, ErrResourceNotFound
	}
	return item, err
}

// ValidRepoURL 校验仓库地址：https://… 或 scp / ssh 形式（git@host:path、ssh://git@host/path）。
func ValidRepoURL(url string) bool {
	return url != "" && len(url) <= 2048 && (repoHTTPSPattern.MatchString(url) || repoSSHPattern.MatchString(url))
}

func validateRepoFields(url, branch string, position *int) error {
	if !ValidRepoURL(url) {
		return ErrInvalidRequest
	}
	if len(branch) > 255 || strings.ContainsAny(branch, " \t\r\n\v\f") {
		return ErrInvalidRequest
	}
	if position != nil && (*position < 0 || *position > 63) {
		return ErrInvalidRequest
	}
	return nil
}

func mapRepoError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrRepoConflict
	}
	return mapDatabaseError(err)
}
