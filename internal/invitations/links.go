package invitations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

// 邀请链接：每个组织一条，不绑定邮箱，角色只能是成员或访客。令牌只存哈希用于查找，
// 另用 secrets.Box 密封保存，管理员打开邀请弹窗时可以再次看到完整链接；重置后旧链接立即失效。

// Link 对应契约 InviteLink。Token 为 nil 表示还没有生成过链接。
type Link struct {
	Enabled   bool       `json:"enabled"`
	Role      string     `json:"role"`
	SpaceIDs  []string   `json:"space_ids"`
	Token     *string    `json:"token"`
	Version   uint64     `json:"version"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type LinkInput struct {
	Enabled  bool
	Role     string
	SpaceIDs []string
	// Version 为 0 表示链接还不存在（首次保存）。
	Version uint64
}

// LinkPreview 是加入页显示的信息。
type LinkPreview struct {
	OrganizationID string  `json:"organization_id"`
	Name           string  `json:"name"`
	IconKind       *string `json:"icon_kind"`
	Icon           *string `json:"icon"`
	Role           string  `json:"role"`
	InviterEmail   *string `json:"inviter_email"`
	MemberCount    int     `json:"member_count"`
	AgentCount     int     `json:"agent_count"`
	AlreadyMember  bool    `json:"already_member"`
}

type LinkJoin struct {
	OrganizationID string `json:"organization_id"`
	Role           string `json:"role"`
	// Status：JOINED 或 ALREADY_MEMBER（已是成员，没有改动）。
	Status string `json:"status"`
}

func linkAAD(organizationID string) string { return "invite-link:" + organizationID }

func (s Service) GetLink(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string, box *secrets.Box) (Link, error) {
	if s.Pool == nil {
		return Link{}, postgres.ErrNilPool
	}
	if !canManageRole(actorRole, "") {
		return Link{}, ErrForbidden
	}
	var link Link
	err := s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		link, err = loadLink(ctx, tx, box, "")
		return err
	})
	return link, normalizeDatabaseError(err)
}

// PutLink 保存启用状态、角色与加入空间；第一次保存时生成令牌。
func (s Service) PutLink(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string, box *secrets.Box, input LinkInput) (Link, error) {
	return s.writeLink(ctx, selection, actorRole, box, input, false)
}

// ResetLink 换新令牌，旧链接立即失效；其余设置不变。
func (s Service) ResetLink(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string, box *secrets.Box, version uint64) (Link, error) {
	return s.writeLink(ctx, selection, actorRole, box, LinkInput{Version: version}, true)
}

func (s Service) writeLink(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string, box *secrets.Box, input LinkInput, reset bool) (Link, error) {
	if s.Pool == nil {
		return Link{}, postgres.ErrNilPool
	}
	if !canManageRole(actorRole, "") {
		return Link{}, ErrForbidden
	}
	if box == nil {
		return Link{}, ErrSecretsUnavailable
	}
	var spaceIDs []string
	if !reset {
		input.Role = strings.ToUpper(strings.TrimSpace(input.Role))
		if input.Role != "MEMBER" && input.Role != "GUEST" {
			return Link{}, ErrInvalidRequest
		}
		var err error
		if spaceIDs, err = normalizeSpaceIDs(input.SpaceIDs); err != nil {
			return Link{}, err
		}
	}
	var link Link
	err := s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if !reset {
			if err := checkInviteSpaces(ctx, tx, actorRole, spaceIDs); err != nil {
				return err
			}
		}
		current, err := loadLink(ctx, tx, box, " FOR UPDATE")
		if err != nil {
			return err
		}
		if current.Version != input.Version || (reset && current.Token == nil) {
			return ErrConflict
		}
		var hash []byte
		var sealed secrets.Sealed
		if current.Token == nil || reset {
			token, tokenHash, err := NewToken()
			if err != nil {
				return err
			}
			if sealed, err = box.Seal([]byte(token), linkAAD(selection.OrganizationID)); err != nil {
				return err
			}
			hash = tokenHash
		}
		if current.Token == nil {
			_, err = tx.Exec(ctx, `
				INSERT INTO openfox.invite_links (organization_id, enabled, role, space_ids, token_hash,
				       token_ciphertext, token_nonce, token_key_id, updated_by)
				VALUES (openfox_private.current_organization_id(), $1, $2, $3::uuid[], $4, $5, $6, $7, $8::uuid)`,
				input.Enabled, input.Role, spaceIDs, hash, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, selection.ActorID)
		} else if reset {
			_, err = tx.Exec(ctx, `
				UPDATE openfox.invite_links
				   SET token_hash = $1, token_ciphertext = $2, token_nonce = $3, token_key_id = $4,
				       updated_by = $5::uuid, updated_at = clock_timestamp(), version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id()`,
				hash, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, selection.ActorID)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE openfox.invite_links
				   SET enabled = $1, role = $2, space_ids = $3::uuid[],
				       updated_by = $4::uuid, updated_at = clock_timestamp(), version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id()`,
				input.Enabled, input.Role, spaceIDs, selection.ActorID)
		}
		if err != nil {
			return err
		}
		if link, err = loadLink(ctx, tx, box, ""); err != nil {
			return err
		}
		action := "invitation_link.updated"
		if reset {
			action = "invitation_link.reset"
		}
		return recordAudit(ctx, tx, selection.OrganizationID, selection.ActorID, action, selection.OrganizationID,
			selection.AuthorizationVersion, map[string]string{"grant_scope": "role:" + link.Role})
	})
	if err != nil {
		return Link{}, normalizeDatabaseError(err)
	}
	return link, nil
}

// loadLink 读取并解封链接；没有链接时返回默认值（未启用、成员、版本 0）。
func loadLink(ctx context.Context, tx pgx.Tx, box *secrets.Box, lock string) (Link, error) {
	link := Link{Role: "MEMBER", SpaceIDs: []string{}}
	var version int64
	var updatedAt time.Time
	var sealed secrets.Sealed
	err := tx.QueryRow(ctx, `
		SELECT enabled, role, space_ids::text[], token_ciphertext, token_nonce, token_key_id, version, updated_at
		  FROM openfox.invite_links
		 WHERE organization_id = openfox_private.current_organization_id()`+lock).Scan(
		&link.Enabled, &link.Role, &link.SpaceIDs, &sealed.Ciphertext, &sealed.Nonce, &sealed.KeyID, &version, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return link, nil
	}
	if err != nil {
		return Link{}, err
	}
	if box == nil {
		return Link{}, ErrSecretsUnavailable
	}
	var organizationID string
	if err := tx.QueryRow(ctx, `SELECT openfox_private.current_organization_id()::text`).Scan(&organizationID); err != nil {
		return Link{}, err
	}
	plain, err := box.Open(sealed, linkAAD(organizationID))
	if err != nil {
		return Link{}, err
	}
	token := string(plain)
	link.Token = &token
	link.Version = uint64(version)
	link.UpdatedAt = &updatedAt
	return link, nil
}

// PreviewLink 按令牌预览；链接无效、已停用或组织已删除时返回 ErrNotFound。
func (s Service) PreviewLink(ctx context.Context, userID, token string) (LinkPreview, error) {
	if s.Pool == nil {
		return LinkPreview{}, postgres.ErrNilPool
	}
	if strings.TrimSpace(token) == "" || len(token) > 512 {
		return LinkPreview{}, ErrNotFound
	}
	var preview LinkPreview
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var upload *string
		err := tx.QueryRow(ctx, `
			SELECT organization_id::text, name, icon_kind, icon, icon_upload_id::text, role, inviter_email,
			       member_count, agent_count, already_member
			  FROM openfox_private.preview_invite_link($1::bytea, $2::uuid)`, HashToken(token), userID).Scan(
			&preview.OrganizationID, &preview.Name, &preview.IconKind, &preview.Icon, &upload, &preview.Role,
			&preview.InviterEmail, &preview.MemberCount, &preview.AgentCount, &preview.AlreadyMember)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		// 上传图标需要组织成员身份才能取到，加入前不显示。
		if err == nil && upload != nil {
			preview.IconKind, preview.Icon = nil, nil
		}
		return err
	})
	return preview, normalizeDatabaseError(err)
}

// JoinLink 凭令牌加入组织并写审计。
func (s Service) JoinLink(ctx context.Context, userID, token string) (LinkJoin, error) {
	if s.Pool == nil {
		return LinkJoin{}, postgres.ErrNilPool
	}
	if strings.TrimSpace(token) == "" || len(token) > 512 {
		return LinkJoin{}, ErrNotFound
	}
	var joined LinkJoin
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT organization_id::text, role, status FROM openfox_private.join_by_invite_link($1::bytea, $2::uuid)`,
			HashToken(token), userID).Scan(&joined.OrganizationID, &joined.Role, &joined.Status); err != nil {
			return err
		}
		if joined.Status != "JOINED" {
			return nil
		}
		var version int64
		if err := tx.QueryRow(ctx, `
			SELECT authorization_version FROM openfox.organization_members
			 WHERE organization_id = $1::uuid AND user_id = $2::uuid`, joined.OrganizationID, userID).Scan(&version); err != nil {
			return err
		}
		return recordAudit(ctx, tx, joined.OrganizationID, userID, "invitation_link.joined", joined.OrganizationID,
			uint64(version), map[string]string{"status": joined.Status, "grant_scope": "role:" + joined.Role})
	})
	if err != nil {
		return LinkJoin{}, normalizeDatabaseError(err)
	}
	return joined, nil
}
