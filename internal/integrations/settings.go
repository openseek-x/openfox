package integrations

// 服务商级设置：关联 Pull Request / Git Commit、Pull Request 回链开关与分支格式。

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// 分支格式取值与契约 IntegrationSettingsValues.branch_format 一致。
const (
	BranchUserIdentifierTitle = "username/identifier-title"
	BranchIdentifierTitle     = "identifier-title"
	BranchUserIdentifier      = "username/identifier"
	BranchIdentifier          = "identifier"
)

type SettingsValues struct {
	LinkPullRequests  bool `json:"link_pull_requests"`
	LinkCommits       bool `json:"link_commits"`
	PRBacklinkPrivate bool `json:"pr_backlink_private"`
	PRBacklinkPublic  bool `json:"pr_backlink_public"`
	// IssueBacklink* 控制同步成任务的 Issue 下是否回链评论；私有默认开，公开默认关。
	IssueBacklinkPrivate bool   `json:"issue_backlink_private"`
	IssueBacklinkPublic  bool   `json:"issue_backlink_public"`
	BranchFormat         string `json:"branch_format"`
}

// DefaultSettings 是从未保存过设置时的默认值。
func DefaultSettings() SettingsValues {
	return SettingsValues{LinkPullRequests: true, LinkCommits: false, PRBacklinkPrivate: true, PRBacklinkPublic: false,
		IssueBacklinkPrivate: true, IssueBacklinkPublic: false, BranchFormat: BranchUserIdentifierTitle}
}

// Valid 校验枚举值。
func (v SettingsValues) Valid() bool {
	switch v.BranchFormat {
	case BranchUserIdentifierTitle, BranchIdentifierTitle, BranchUserIdentifier, BranchIdentifier:
		return true
	}
	return false
}

// Settings 对应契约 IntegrationSettings。
type Settings struct {
	Provider  string         `json:"provider"`
	Settings  SettingsValues `json:"settings"`
	CanManage bool           `json:"can_manage"`
	Version   uint64         `json:"version"`
	UpdatedAt *time.Time     `json:"updated_at"`
}

// loadSettingsValues 读取已保存的设置，缺失的键用默认值补齐。
func loadSettingsValues(ctx context.Context, tx pgx.Tx, provider string) (SettingsValues, int64, *time.Time, error) {
	values := DefaultSettings()
	var raw []byte
	var version int64
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT settings, version, updated_at FROM openfox.integration_provider_settings
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = $1`, provider).Scan(&raw, &version, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return values, 0, nil, nil
	}
	if err != nil {
		return values, 0, nil, err
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return DefaultSettings(), 0, nil, err
	}
	if !values.Valid() {
		values.BranchFormat = BranchUserIdentifierTitle
	}
	return values, version, &updatedAt, nil
}

// GetSettings 所有非访客成员可读。provider 必须是 Git 服务商（github / gitlab / gitlab_cn）。
func (s Service) GetSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, provider string) (Settings, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Settings{}, err
	}
	if !IsGitProvider(provider) {
		return Settings{}, ErrNotFound
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return Settings{}, ErrForbidden
	}
	values, version, updatedAt, err := loadSettingsValues(ctx, tx, provider)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Provider: provider, Settings: values, CanManage: CanManage(actor.Role), Version: uint64(version), UpdatedAt: updatedAt}, nil
}

// PutSettings 只有所有者 / 管理员；version 做 CAS（首次保存为 0，走 INSERT）。
func (s Service) PutSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, provider string, version uint64, values SettingsValues) (Settings, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Settings{}, err
	}
	if !IsGitProvider(provider) {
		return Settings{}, ErrNotFound
	}
	if !CanManage(actor.Role) {
		return Settings{}, ErrForbidden
	}
	if !values.Valid() {
		return Settings{}, ErrInvalidRequest
	}
	raw, _ := json.Marshal(values)
	if version == 0 {
		tag, err := tx.Exec(ctx, `
			INSERT INTO openfox.integration_provider_settings (organization_id, provider, settings, updated_by)
			VALUES (openfox_private.current_organization_id(), $1, $2::jsonb, $3::uuid)
			ON CONFLICT (organization_id, provider) DO NOTHING`, provider, raw, actor.UserID)
		if err != nil {
			return Settings{}, err
		}
		if tag.RowsAffected() != 1 {
			return Settings{}, ErrVersionConflict
		}
	} else {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.integration_provider_settings
			   SET settings = $2::jsonb, updated_by = $3::uuid, updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = $1 AND version = $4`,
			provider, raw, actor.UserID, int64(version))
		if err != nil {
			return Settings{}, err
		}
		if tag.RowsAffected() != 1 {
			return Settings{}, ErrVersionConflict
		}
	}
	return s.GetSettings(ctx, tx, organizationID, actor, provider)
}
