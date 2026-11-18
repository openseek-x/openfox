package integrations

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

type GongfengConfiguration struct {
	Version      uint64               `json:"version"`
	Repositories []GongfengRepository `json:"repositories"`
	HasMore      bool                 `json:"has_more"`
	NextCursor   string               `json:"next_cursor"`
}

func (s Service) GetGongfengConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, after string) (GongfengConfiguration, error) {
	out := GongfengConfiguration{Repositories: []GongfengRepository{}}
	a, err := s.gongfengMetadata(ctx, tx, org, actor, id)
	if err != nil {
		return out, err
	}
	if after != "" && !membership.ValidateUUID(after) {
		return out, ErrInvalidRequest
	}
	out.Version = a.Version
	rows, err := tx.Query(ctx, `SELECT `+gongfengRepoColumns+` FROM openfox.integration_gongfeng_repositories
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id>$2::uuid
 ORDER BY id LIMIT 101`, id, nullableGongfengCursor(after))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanGongfengRepository(rows)
		if e != nil {
			return out, e
		}
		out.Repositories = append(out.Repositories, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Repositories) > 100 {
		out.Repositories = out.Repositories[:100]
		out.HasMore = true
		out.NextCursor = out.Repositories[99].ID
	}
	return out, nil
}
func nullableGongfengCursor(after string) string {
	if after == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return after
}

func (a GongfengAccess) Projects(ctx context.Context, query string, page int) (gongfeng.Page[gongfeng.Project], error) {
	if a.Status != StatusActive {
		return gongfeng.Page[gongfeng.Project]{}, ErrNeedsReauth
	}
	if len(query) > 200 {
		return gongfeng.Page[gongfeng.Project]{}, ErrInvalidRequest
	}
	return a.Client.Projects(ctx, a.credentials.AccessToken, query, page)
}
func (s Service) CheckGongfengAccess(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, prepared GongfengAccess) error {
	a, err := s.PrepareGongfeng(ctx, tx, org, actor, prepared.ID)
	if err != nil {
		return err
	}
	if a.Version != prepared.Version || a.Status != StatusActive {
		return ErrVersionConflict
	}
	return nil
}

type GongfengWorkSummary struct {
	ID                  string     `json:"id"`
	RepositoryID        string     `json:"repository_id"`
	Kind                string     `json:"kind"`
	ObjectKey           string     `json:"object_key"`
	Status              string     `json:"status"`
	RequestedGeneration int64      `json:"requested_generation"`
	CompletedGeneration int64      `json:"completed_generation"`
	ErrorCode           string     `json:"error_code"`
	NextAttemptAt       *time.Time `json:"next_attempt_at"`
	BacklinkState       string     `json:"backlink_state"`
}
type GongfengWorkPage struct {
	Work       []GongfengWorkSummary `json:"work"`
	HasMore    bool                  `json:"has_more"`
	NextCursor string                `json:"next_cursor"`
}

func (s Service) ListGongfengWork(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, after string) (GongfengWorkPage, error) {
	out := GongfengWorkPage{Work: []GongfengWorkSummary{}}
	if _, err := s.gongfengMetadata(ctx, tx, org, actor, id); err != nil {
		return out, err
	}
	if after != "" && !membership.ValidateUUID(after) {
		return out, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `SELECT id::text,repository_id::text,kind,object_key,status,requested_generation,completed_generation,last_error_code,next_attempt_at,backlink_state
 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND kind<>'RECEIPT'
 AND id>$2::uuid ORDER BY id LIMIT 51`, id, nullableGongfengCursor(after))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item GongfengWorkSummary
		if err = rows.Scan(&item.ID, &item.RepositoryID, &item.Kind, &item.ObjectKey, &item.Status, &item.RequestedGeneration, &item.CompletedGeneration, &item.ErrorCode, &item.NextAttemptAt, &item.BacklinkState); err != nil {
			return out, err
		}
		out.Work = append(out.Work, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Work) > 50 {
		out.Work = out.Work[:50]
		out.HasMore = true
		out.NextCursor = out.Work[49].ID
	}
	return out, nil
}

func (s Service) MarkGongfengTokenRevoked(ctx context.Context, tx pgx.Tx, id string, version uint64) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='token_revoked',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='gongfeng' AND version=$2 AND status<>'DISCONNECTED'`, id, version)
	return err
}

// Retained hook IDs/URLs and work diagnostics remain available after disconnect,
// without decrypting or restoring the cleared PAT.
func (s Service) gongfengMetadata(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (Connection, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	if !membership.ValidateUUID(id) {
		return Connection{}, ErrNotFound
	}
	c, err := loadConnection(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if c.Provider != ProviderGongfeng || c.Scope != ScopeOrganization {
		return Connection{}, ErrNotFound
	}
	return c, nil
}
