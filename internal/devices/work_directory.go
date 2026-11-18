package devices

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
	"openfox/internal/workdirpath"
)

func authorizeWorkDirectory(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, deviceID string, raw json.RawMessage) error {
	request, err := workdirpath.Parse(raw)
	if err != nil || request.OrganizationID != selection.OrganizationID || request.DeviceID != deviceID || request.UserID != selection.ActorID {
		return ErrCommandNotFound
	}
	if !request.Live(time.Now()) {
		return ErrCommandExpired
	}
	actor, err := (membership.Service{}).LoadActiveActor(ctx, tx, selection.OrganizationID, selection.ActorID)
	if err != nil {
		return ErrCommandNotFound
	}
	target, err := (conversations.Service{}).WorkDirectory(ctx, tx, selection.OrganizationID, request.ConversationID,
		conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion})
	if err != nil || target.Target == nil || *target.Target != request.Scope || !target.Supported {
		return ErrCommandNotFound
	}
	return nil
}

func validateWorkDirectoryResult(input CommandResultInput) error {
	if input.Status == CommandSucceeded {
		return workdirpath.ValidateEnvelope(input.Result)
	}
	if len(input.Result) != 0 && string(input.Result) != "null" {
		return ErrInvalidRequest
	}
	switch input.ErrorCode {
	case "WORKSPACE_UNAVAILABLE", "INVALID_PARAMS", "TIMEOUT", "UNSUPPORTED_KIND", "COMMAND_FAILED", "DAEMON_SHUTDOWN", "RESULT_TOO_LARGE":
		return nil
	}
	return ErrInvalidRequest
}
