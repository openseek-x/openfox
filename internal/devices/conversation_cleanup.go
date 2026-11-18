package devices

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/daemon/storage"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// authorizeConversationCleanup 校验带 conversation_id 的 storage_cleanup：
// 设备必须在心跳里声明 storage.conversation_filter（旧 daemon 不认识该参数，会按整机清理），
// 且请求人必须同时是设备所有者（调用前已校验）和该会话的参与者。不带会话的请求直接放行。
func authorizeConversationCleanup(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, deviceID string, params json.RawMessage) error {
	var filter storage.CleanupFilter
	if json.Unmarshal(params, &filter) != nil || filter.ConversationID == "" {
		return nil
	}
	var supported bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(capabilities->'storage.conversation_filter' = 'true'::jsonb, false)
		FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID).Scan(&supported)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceNotFound
	}
	if err != nil {
		return err
	}
	if !supported {
		return ErrCapabilityUnsupported
	}
	actor, err := (membership.Service{}).LoadActiveActor(ctx, tx, selection.OrganizationID, selection.ActorID)
	if err != nil {
		return ErrCommandNotFound
	}
	if _, err = (conversations.Service{}).Get(ctx, tx, selection.OrganizationID, filter.ConversationID,
		conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}); err != nil {
		return ErrCommandNotFound
	}
	return nil
}
