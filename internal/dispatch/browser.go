package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"
	"openfox/internal/browser"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/runtimepolicy"
)

type BrowserAuthorizationInput = browser.AuthorizationInput
type BrowserAuthorization = browser.Authorization

// AuthorizeBrowser validates metadata for every tool call. This does not grant
// access by itself: the local broker and host must also hold an explicit grant.
func (s Store) AuthorizeBrowser(ctx context.Context, tx pgx.Tx, org, device, runID string, input BrowserAuthorizationInput) (BrowserAuthorization, error) {
	if input.Operation != "observe" && input.Operation != "act" && input.Operation != "files" {
		return BrowserAuthorization{}, ErrInvalidRequest
	}
	if input.ClientMessageID != "" && !membership.ValidateUUID(input.ClientMessageID) {
		return BrowserAuthorization{}, ErrInvalidRequest
	}
	run, err := authorizeRunRead(ctx, tx, org, device, runID, input.Generation)
	if err != nil {
		return BrowserAuthorization{}, err
	}
	if run.State != execution.StateRunning || run.CancelRequestedAt != nil {
		return BrowserAuthorization{}, ErrStateConflict
	}
	binding, err := browser.Active(ctx, tx, org, device, input.BindingID, input.Identity)
	if err != nil {
		return BrowserAuthorization{}, err
	}
	if binding.OwnerUserID != run.ActorID || binding.ConversationID != run.ConversationID {
		return BrowserAuthorization{}, ErrNotFound
	}
	var userInput bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.physical_devices d
 WHERE d.id=$2::uuid AND d.owner_user_id=$4::uuid AND d.status='ACTIVE')
 AND EXISTS(SELECT 1 FROM openfox.conversation_inputs i JOIN openfox.messages m
 ON m.organization_id=i.organization_id AND m.conversation_id=i.conversation_id AND m.id=i.message_id
 WHERE i.organization_id=$1::uuid AND i.run_id=$3::uuid AND ($5='' OR m.client_message_id=$5))
 AND NOT EXISTS(SELECT 1 FROM openfox.conversation_inputs i LEFT JOIN openfox.messages m
 ON m.organization_id=i.organization_id AND m.conversation_id=i.conversation_id AND m.id=i.message_id
 WHERE i.organization_id=$1::uuid AND i.run_id=$3::uuid
 AND (m.authenticated_user_input IS NOT TRUE OR m.author_kind IS DISTINCT FROM 'USER'
 OR m.author_user_id IS DISTINCT FROM $4::uuid
 OR EXISTS(SELECT 1 FROM openfox.automation_runs a WHERE a.organization_id=i.organization_id AND a.message_id=i.message_id)))`, org, device, runID, run.ActorID, input.ClientMessageID).Scan(&userInput)
	if err != nil {
		return BrowserAuthorization{}, err
	}
	if !userInput {
		return BrowserAuthorization{}, ErrAuthorizationRevoked
	}
	snapshot, err := loadRunSnapshot(ctx, tx, run)
	if err != nil {
		return BrowserAuthorization{}, err
	}
	readOnly := snapshot.ApprovalMode == runtimepolicy.ReadOnly || snapshot.CollaborationMode == runtimepolicy.CollaborationPlan
	if readOnly && input.Operation != "observe" {
		return BrowserAuthorization{}, ErrAuthorizationRevoked
	}
	return BrowserAuthorization{Binding: binding, RunID: runID, Generation: run.Generation, ReadOnly: readOnly}, nil
}
