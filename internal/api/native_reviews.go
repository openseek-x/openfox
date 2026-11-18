package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// Re-enter the tenant transaction as the authenticated request's human for native
// document RLS. Device identity, Run lease/generation and grant are still checked
// inside ContextForDevice / callArtifactReviewTool before any content is returned.
func (h *Handler) nativeReviewSelection(r *http.Request, selection tenancy.OrganizationSelection) (tenancy.OrganizationSelection, error) {
	if !membership.ValidateUUID(r.PathValue("runID")) {
		return selection, dispatch.ErrInvalidRequest
	}
	var user string
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE((SELECT m.author_user_id::text FROM openfox.message_file_contexts f JOIN openfox.messages m ON m.organization_id=f.organization_id AND m.id=f.message_id JOIN openfox.conversation_inputs i ON i.organization_id=f.organization_id AND i.message_id=f.message_id WHERE f.organization_id=openfox_private.current_organization_id() AND i.run_id=$1::uuid AND f.input_snapshot->>'node_id' IS NOT NULL LIMIT 1),'')`, r.PathValue("runID")).Scan(&user)
	})
	if err != nil || user == "" {
		return selection, err
	}
	selected, _, err := h.auth.SelectOrganization(r.Context(), user, selection.OrganizationID)
	return selected, err
}
