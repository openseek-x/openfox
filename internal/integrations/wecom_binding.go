package integrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

const wecomCodeTTL = 10 * time.Minute

type WeComPersonalBinding struct {
	BotID              string `json:"bot_id"`
	BotName            string `json:"bot_name"`
	ConnectionID       string `json:"connection_id"`
	ExternalID         string `json:"external_id"`
	NotificationTarget bool   `json:"notification_target"`
}

type WeComPersonalView struct {
	Bindings []WeComPersonalBinding `json:"bindings"`
}

type WeComBindingCode struct {
	BotID     string    `json:"bot_id"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s Service) WeComPersonal(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (WeComPersonalView, error) {
	view := WeComPersonalView{Bindings: []WeComPersonalBinding{}}
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return view, err
	}
	rows, err := tx.Query(ctx, `
		SELECT b.id::text, parent.display_name, p.id::text, p.external_id, target.personal_connection_id IS NOT NULL
		  FROM openfox.integration_connections p
		  JOIN openfox.integration_connections parent
		    ON parent.organization_id = p.organization_id AND parent.id = p.parent_connection_id
		  JOIN openfox.im_bots b ON b.organization_id = parent.organization_id AND b.connection_id = parent.id
		  LEFT JOIN openfox.wecom_notification_targets target
		    ON target.organization_id = p.organization_id AND target.user_id = p.owner_user_id
		   AND target.personal_connection_id = p.id
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.provider = 'wecom'
		   AND p.scope = 'PERSONAL' AND p.owner_user_id = $1::uuid AND p.status = 'ACTIVE'
		   AND parent.status = 'ACTIVE' AND b.removed_at IS NULL
		 ORDER BY lower(parent.display_name), b.id LIMIT 200`, actor.UserID)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var binding WeComPersonalBinding
		if err := rows.Scan(&binding.BotID, &binding.BotName, &binding.ConnectionID, &binding.ExternalID, &binding.NotificationTarget); err != nil {
			return view, err
		}
		view.Bindings = append(view.Bindings, binding)
	}
	return view, rows.Err()
}

func (s Service) IssueWeComBindingCode(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string) (WeComBindingCode, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return WeComBindingCode{}, err
	}
	if !membership.ValidateUUID(botID) {
		return WeComBindingCode{}, ErrInvalidRequest
	}
	var spaceID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(b.space_id::text, a.space_id::text, '')
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.id = $1::uuid
		   AND c.provider = 'wecom' AND c.status = 'ACTIVE' AND b.removed_at IS NULL AND a.archived_at IS NULL`, botID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WeComBindingCode{}, ErrNotFound
	}
	if err != nil {
		return WeComBindingCode{}, err
	}
	if spaceID != "" {
		canRead, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID,
			conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}, spaceID)
		if err != nil {
			return WeComBindingCode{}, err
		}
		if !canRead {
			return WeComBindingCode{}, ErrForbidden
		}
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return WeComBindingCode{}, err
	}
	code := base64.RawURLEncoding.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(code))
	result := WeComBindingCode{BotID: botID, Code: code}
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.wecom_binding_codes
		    (organization_id, bot_id, user_id, code_hash, created_at, expires_at, consumed_at)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3,
		        clock_timestamp(), clock_timestamp() + make_interval(secs => $4::float8), NULL)
		ON CONFLICT (organization_id, bot_id, user_id) DO UPDATE
		   SET code_hash = EXCLUDED.code_hash, created_at = EXCLUDED.created_at,
		       expires_at = EXCLUDED.expires_at, consumed_at = NULL
		RETURNING expires_at`, botID, actor.UserID, hash[:], wecomCodeTTL.Seconds()).Scan(&result.ExpiresAt)
	return result, err
}

func (s Service) ConsumeWeComBindingCode(ctx context.Context, tx pgx.Tx, botID, externalID, code string) (bool, error) {
	if !membership.ValidateUUID(botID) || len(externalID) == 0 || len(externalID) > 128 || len(code) != 32 {
		return false, nil
	}
	hash := sha256.Sum256([]byte(code))
	var userID, parentID, role, spaceID, organizationID string
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT c.id::text, code.user_id::text, m.role, m.authorization_version,
		       COALESCE(b.space_id::text, a.space_id::text, ''), b.organization_id::text
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
		  JOIN openfox.wecom_binding_codes code ON code.organization_id = b.organization_id AND code.bot_id = b.id
		  JOIN openfox.organization_members m ON m.organization_id = code.organization_id AND m.user_id = code.user_id
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.id = $1::uuid
		   AND b.removed_at IS NULL AND c.provider = 'wecom' AND c.status = 'ACTIVE' AND a.archived_at IS NULL
		   AND code.code_hash = $2 AND code.consumed_at IS NULL AND code.expires_at > clock_timestamp()
		   AND m.status = 'ACTIVE' AND m.role <> 'GUEST'
		 FOR UPDATE OF code FOR SHARE OF b, c, a, m`, botID, hash[:]).Scan(&parentID, &userID, &role, &version, &spaceID, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if spaceID != "" {
		canRead, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID,
			conversations.Actor{UserID: userID, Role: orgRole(role), AuthorizationVersion: uint64(version)}, spaceID)
		if err != nil || !canRead {
			return false, err
		}
	}
	err = tx.QueryRow(ctx, `
		UPDATE openfox.wecom_binding_codes code
		   SET consumed_at = clock_timestamp()
		 WHERE code.organization_id = openfox_private.current_organization_id() AND code.bot_id = $1::uuid
		   AND code.user_id = $2::uuid AND code.code_hash = $3 AND code.consumed_at IS NULL
		   AND code.expires_at > clock_timestamp()
		RETURNING code.user_id::text`, botID, userID, hash[:]).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var personalID string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'wecom'
		   AND scope = 'PERSONAL' AND parent_connection_id = $1::uuid AND owner_user_id = $2::uuid
		   AND status <> 'DISCONNECTED' FOR UPDATE`, parentID, userID).Scan(&personalID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if personalID == "" {
		personalID, err = membership.NewID()
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.integration_connections
			    (organization_id, id, provider, scope, external_id, display_name, account_kind,
			     status, owner_user_id, parent_connection_id, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'wecom', 'PERSONAL', $2, $2,
			        'wecom_user', 'ACTIVE', $3::uuid, $4::uuid, $3::uuid)`, personalID, externalID, userID, parentID)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET external_id = $2, display_name = $2, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, personalID, externalID)
	}
	if isUniqueViolation(err) {
		return false, ErrVersionConflict
	}
	return err == nil, err
}

func (s Service) UnbindWeCom(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string) error {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(botID) {
		return ErrInvalidRequest
	}
	var connectionID string
	err := tx.QueryRow(ctx, `
		SELECT p.id::text FROM openfox.integration_connections p
		  JOIN openfox.integration_connections parent ON parent.organization_id = p.organization_id AND parent.id = p.parent_connection_id
		  JOIN openfox.im_bots b ON b.organization_id = parent.organization_id AND b.connection_id = parent.id
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.provider = 'wecom'
		   AND p.scope = 'PERSONAL' AND p.owner_user_id = $1::uuid AND p.status = 'ACTIVE' AND b.id = $2::uuid
		 FOR UPDATE OF p`, actor.UserID, botID).Scan(&connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.wecom_notification_targets
		WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid
		  AND personal_connection_id = $2::uuid`, actor.UserID, connectionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.wecom_binding_codes
		WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid AND bot_id = $2::uuid`, actor.UserID, botID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_connections
		SET status = 'DISCONNECTED', disconnected_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, connectionID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_notification_outbox
		SET status = 'SKIPPED', last_error = 'account_unbound', lease_owner = '', lease_expires_at = NULL,
		    finished_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE organization_id = openfox_private.current_organization_id() AND personal_connection_id = $1::uuid
		  AND status IN ('PENDING', 'SENDING')`, connectionID)
	return err
}

func (s Service) SetWeComNotificationTarget(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string) (WeComPersonalView, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return WeComPersonalView{}, err
	}
	if botID == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM openfox.wecom_notification_targets
			WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, actor.UserID); err != nil {
			return WeComPersonalView{}, err
		}
		return s.WeComPersonal(ctx, tx, organizationID, actor)
	}
	if !membership.ValidateUUID(botID) {
		return WeComPersonalView{}, ErrInvalidRequest
	}
	var personalID, spaceID string
	err := tx.QueryRow(ctx, `
		SELECT p.id::text, COALESCE(b.space_id::text, a.space_id::text, '') FROM openfox.integration_connections p
		  JOIN openfox.integration_connections parent ON parent.organization_id = p.organization_id AND parent.id = p.parent_connection_id
		  JOIN openfox.im_bots b ON b.organization_id = parent.organization_id AND b.connection_id = parent.id
		  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.provider = 'wecom'
		   AND p.scope = 'PERSONAL' AND p.owner_user_id = $1::uuid AND p.status = 'ACTIVE'
		   AND parent.status = 'ACTIVE' AND b.id = $2::uuid AND b.removed_at IS NULL AND a.archived_at IS NULL`, actor.UserID, botID).Scan(&personalID, &spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WeComPersonalView{}, ErrNotFound
	}
	if err != nil {
		return WeComPersonalView{}, err
	}
	if spaceID == "" {
		return WeComPersonalView{}, ErrForbidden
	}
	canRead, err := (conversations.Service{}).CanReadSpace(ctx, tx, organizationID,
		conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}, spaceID)
	if err != nil {
		return WeComPersonalView{}, err
	}
	if !canRead {
		return WeComPersonalView{}, ErrForbidden
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.wecom_notification_targets (organization_id, user_id, personal_connection_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid)
		ON CONFLICT (organization_id, user_id) DO UPDATE
		   SET personal_connection_id = EXCLUDED.personal_connection_id, updated_at = clock_timestamp()`, actor.UserID, personalID)
	if err != nil {
		return WeComPersonalView{}, err
	}
	return s.WeComPersonal(ctx, tx, organizationID, actor)
}
